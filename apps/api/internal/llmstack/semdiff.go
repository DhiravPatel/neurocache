package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SemDiffStore answers "are these two pieces of text semantically the
// same, or has someone meaningfully changed them?" This is the version-
// control problem for prompts / instructions / RAG documents — you can
// diff text byte-for-byte, but a one-word fix and a complete rewrite
// look identical to `diff`.
//
// SEMDIFF.* compares texts in embedding space with a four-tier verdict
// and also keeps a named history so apps can ship prompt regressions:
//
//   identical  cosine ≥ 0.98  (whitespace / synonym change)
//   equivalent cosine ≥ 0.90  (wording shift, same intent)
//   related    cosine ≥ 0.70  (drifting same-topic)
//   divergent  cosine <  0.70 (different meaning — flag for review)
//
// Commands:
//
//   SEMDIFF.CHECK text-a text-b
//        One-shot diff → {cosine, verdict, identical, equivalent}
//   SEMDIFF.PUT name version text
//        Store a named version of the prompt/document.
//   SEMDIFF.GET name [VERSION v]
//        Retrieve a stored version (defaults to latest).
//   SEMDIFF.COMPARE name v1 v2
//        Diff two stored versions of the same name.
//   SEMDIFF.HISTORY name
//        Version list with per-version stats.
//   SEMDIFF.LATEST name
//        Latest version label and text.
//   SEMDIFF.DELETE name
//        Drop every version under name.
//   SEMDIFF.STATS
//
// Hot path: CHECK is two embedFallback calls + one dot product —
// ~1 µs on a 128-dim normalized vector. PUT amortises: embedding is
// computed once and cached on the version row, so COMPARE is just a
// dot product on already-resident vectors.
type SemDiffStore struct {
	mu    sync.RWMutex
	names map[string]*semDiffName

	totalChecks   atomic.Int64
	totalPuts     atomic.Int64
	totalCompares atomic.Int64
}

type semDiffName struct {
	mu       sync.RWMutex
	versions []semDiffVersion
	byLabel  map[string]int // label → index in versions
}

type semDiffVersion struct {
	Label string
	Text  string
	Vec   []float64
	TS    int64
}

// NewSemDiffStore returns an empty store.
func NewSemDiffStore() *SemDiffStore {
	return &SemDiffStore{names: map[string]*semDiffName{}}
}

// SemDiffResult is CHECK / COMPARE output.
type SemDiffResult struct {
	Cosine     float64 `json:"cosine"`
	Verdict    string  `json:"verdict"`   // identical|equivalent|related|divergent
	Identical  bool    `json:"identical"` // cosine ≥ 0.98
	Equivalent bool    `json:"equivalent"` // cosine ≥ 0.90
}

// Check runs the one-shot diff between two arbitrary texts.
func (s *SemDiffStore) Check(a, b string) SemDiffResult {
	s.totalChecks.Add(1)
	va := embedFallback(a)
	vb := embedFallback(b)
	cos := dotProduct(va, vb)
	return verdictFor(cos)
}

// Put stores a labelled version. Overwrites if the label already exists
// for that name (latest wins).
func (s *SemDiffStore) Put(name, label, text string) error {
	if name == "" {
		return errors.New("name required")
	}
	if label == "" {
		return errors.New("version label required")
	}
	s.totalPuts.Add(1)
	vec := embedFallback(text)
	s.mu.Lock()
	n, ok := s.names[name]
	if !ok {
		n = &semDiffName{byLabel: map[string]int{}}
		s.names[name] = n
	}
	s.mu.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	if idx, exists := n.byLabel[label]; exists {
		n.versions[idx] = semDiffVersion{
			Label: label, Text: text, Vec: vec, TS: time.Now().UnixNano(),
		}
		return nil
	}
	n.byLabel[label] = len(n.versions)
	n.versions = append(n.versions, semDiffVersion{
		Label: label, Text: text, Vec: vec, TS: time.Now().UnixNano(),
	})
	return nil
}

// Get returns a stored version's text. label="" → latest.
func (s *SemDiffStore) Get(name, label string) (string, string, bool) {
	s.mu.RLock()
	n, ok := s.names[name]
	s.mu.RUnlock()
	if !ok {
		return "", "", false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	if len(n.versions) == 0 {
		return "", "", false
	}
	if label == "" {
		v := n.versions[len(n.versions)-1]
		return v.Label, v.Text, true
	}
	idx, ok := n.byLabel[label]
	if !ok {
		return "", "", false
	}
	v := n.versions[idx]
	return v.Label, v.Text, true
}

// Compare diffs two stored versions of the same name.
func (s *SemDiffStore) Compare(name, labelA, labelB string) (SemDiffResult, error) {
	s.totalCompares.Add(1)
	s.mu.RLock()
	n, ok := s.names[name]
	s.mu.RUnlock()
	if !ok {
		return SemDiffResult{}, errors.New("unknown name: " + name)
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	ai, ok := n.byLabel[labelA]
	if !ok {
		return SemDiffResult{}, errors.New("unknown version: " + labelA)
	}
	bi, ok := n.byLabel[labelB]
	if !ok {
		return SemDiffResult{}, errors.New("unknown version: " + labelB)
	}
	cos := dotProduct(n.versions[ai].Vec, n.versions[bi].Vec)
	return verdictFor(cos), nil
}

// SemDiffHistoryRow is one row of HISTORY.
type SemDiffHistoryRow struct {
	Label   string  `json:"label"`
	TS      int64   `json:"ts"`
	Chars   int     `json:"chars"`
	VsPrev  float64 `json:"vs_prev_cosine"` // 0 if this is the first version
}

// History returns the version list (oldest first), with per-version
// cosine against the prior version so callers can see drift.
func (s *SemDiffStore) History(name string) ([]SemDiffHistoryRow, bool) {
	s.mu.RLock()
	n, ok := s.names[name]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]SemDiffHistoryRow, len(n.versions))
	for i, v := range n.versions {
		row := SemDiffHistoryRow{
			Label: v.Label, TS: v.TS / int64(time.Second), Chars: len(v.Text),
		}
		if i > 0 {
			row.VsPrev = dotProduct(n.versions[i-1].Vec, v.Vec)
		}
		out[i] = row
	}
	return out, true
}

// Latest returns the most recent version's label and text.
func (s *SemDiffStore) Latest(name string) (string, string, bool) {
	return s.Get(name, "")
}

// Delete drops every version under a name.
func (s *SemDiffStore) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.names[name]
	delete(s.names, name)
	return ok
}

// Names returns every known name, sorted.
func (s *SemDiffStore) Names() []string {
	s.mu.RLock()
	out := make([]string, 0, len(s.names))
	for k := range s.names {
		out = append(out, k)
	}
	s.mu.RUnlock()
	sort.Strings(out)
	return out
}

// SemDiffStats is the global snapshot.
type SemDiffStats struct {
	Names         int   `json:"names"`
	TotalVersions int   `json:"total_versions"`
	TotalChecks   int64 `json:"total_checks"`
	TotalPuts     int64 `json:"total_puts"`
	TotalCompares int64 `json:"total_compares"`
}

func (s *SemDiffStore) Stats() SemDiffStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, n := range s.names {
		n.mu.RLock()
		total += len(n.versions)
		n.mu.RUnlock()
	}
	return SemDiffStats{
		Names:         len(s.names),
		TotalVersions: total,
		TotalChecks:   s.totalChecks.Load(),
		TotalPuts:     s.totalPuts.Load(),
		TotalCompares: s.totalCompares.Load(),
	}
}

// verdictFor maps a cosine score to the four-tier verdict.
func verdictFor(cos float64) SemDiffResult {
	r := SemDiffResult{Cosine: cos}
	switch {
	case cos >= 0.98:
		r.Verdict = "identical"
		r.Identical = true
		r.Equivalent = true
	case cos >= 0.90:
		r.Verdict = "equivalent"
		r.Equivalent = true
	case cos >= 0.70:
		r.Verdict = "related"
	default:
		r.Verdict = "divergent"
	}
	return r
}
