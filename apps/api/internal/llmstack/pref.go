package llmstack

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Preferences accumulates (prompt, chosen, rejected) triples — the
// labeled data shape DPO/RLHF fine-tuning consumes. Every thumbs-up
// vs thumbs-down, every JURY verdict, every ANSWER.CANARY winner is
// a preference signal — and almost every team throws this away then
// pays to collect the same data again before fine-tuning.
//
// PREF.* turns the existing signals into a training asset with no
// extra plumbing: APIs you already call (jury, canary, feedback)
// post a PREF.RECORD, and EXPORT emits ready-to-train JSONL.
//
// Each pair carries:
//   - dataset       — group key (typically per-feature: "summarizer",
//                     "code-reviewer", "router")
//   - prompt        — the input
//   - chosen        — the preferred response
//   - rejected      — the dispreferred response
//   - source        — which signal produced it ("jury", "canary",
//                     "thumbs", "judge", ...). Useful for filtering
//                     during EXPORT (e.g. "DPO on JURY pairs only").
//   - margin        — a confidence weight in [0,1]. EXPORT can filter
//                     low-margin pairs (LLM-judge noise).
//   - dedupe        — pair hash so the same triple isn't recorded
//                     twice (a common artifact when canaries replay).
//
// Commands:
//
//   PREF.RECORD dataset prompt CHOSEN c REJECTED r [SOURCE s] [MARGIN m]
//   PREF.STATS dataset
//        → pairs / mean_margin / clean_pairs (margin >= 0.1)
//        / source breakdown
//   PREF.EXPORT dataset [FORMAT dpo|sft|rlhf] [MIN_MARGIN f] [SOURCE s] [LIMIT n]
//        → JSONL string (one line per pair, in the requested format)
//   PREF.LIST                     — every dataset
//   PREF.RESET dataset|ALL
//   PREF.STATS_GLOBAL
//
// Hot path: RECORD is one map lookup + map insert (deduped). STATS is
// a counter read. EXPORT is a linear walk + JSON encode.
type Preferences struct {
	mu       sync.RWMutex
	datasets map[string]*prefSet

	totalRecords atomic.Int64
	totalExports atomic.Int64
	totalDupes   atomic.Int64
}

type prefSet struct {
	mu        sync.RWMutex
	name      string
	pairs     []*prefPair
	dedupe    map[string]bool
}

type prefPair struct {
	Prompt   string
	Chosen   string
	Rejected string
	Source   string
	Margin   float64
	At       time.Time
}

// NewPreferences returns an empty store.
func NewPreferences() *Preferences {
	return &Preferences{datasets: map[string]*prefSet{}}
}

// PrefRecordResult is RECORD's return.
type PrefRecordResult struct {
	Recorded bool `json:"recorded"`
	Duplicate bool `json:"duplicate"`
}

// Record appends one preference triple, deduping by content hash.
// Recording the same (prompt, chosen, rejected) twice is a no-op
// (Duplicate=true). Margin > 1 or < 0 is clamped.
func (p *Preferences) Record(dataset, prompt, chosen, rejected, source string, margin float64) (PrefRecordResult, error) {
	if dataset == "" {
		return PrefRecordResult{}, errors.New("dataset required")
	}
	if prompt == "" {
		return PrefRecordResult{}, errors.New("prompt required")
	}
	if chosen == "" {
		return PrefRecordResult{}, errors.New("chosen required")
	}
	if rejected == "" {
		return PrefRecordResult{}, errors.New("rejected required")
	}
	if chosen == rejected {
		return PrefRecordResult{}, errors.New("chosen == rejected has no signal")
	}
	if margin < 0 {
		margin = 0
	}
	if margin > 1 {
		margin = 1
	}
	if margin == 0 {
		margin = 0.5 // neutral default; let EXPORT MIN_MARGIN filter it out
	}
	p.totalRecords.Add(1)
	s := p.setOrCreate(dataset)
	key := pairHash(prompt, chosen, rejected)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dedupe[key] {
		p.totalDupes.Add(1)
		return PrefRecordResult{Recorded: false, Duplicate: true}, nil
	}
	s.dedupe[key] = true
	s.pairs = append(s.pairs, &prefPair{
		Prompt: prompt, Chosen: chosen, Rejected: rejected,
		Source: source, Margin: margin, At: time.Now(),
	})
	return PrefRecordResult{Recorded: true}, nil
}

// PrefStats is STATS's return.
type PrefStats struct {
	Dataset     string            `json:"dataset"`
	Pairs       int               `json:"pairs"`
	MeanMargin  float64           `json:"mean_margin"`
	CleanPairs  int               `json:"clean_pairs"`
	BySource    map[string]int    `json:"by_source"`
}

// Stats returns the per-dataset breakdown.
func (p *Preferences) Stats(dataset string) (PrefStats, bool) {
	p.mu.RLock()
	s, ok := p.datasets[dataset]
	p.mu.RUnlock()
	if !ok {
		return PrefStats{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := PrefStats{Dataset: dataset, Pairs: len(s.pairs), BySource: map[string]int{}}
	if len(s.pairs) == 0 {
		return out, true
	}
	sum, clean := 0.0, 0
	for _, pr := range s.pairs {
		sum += pr.Margin
		if pr.Margin >= 0.1 {
			clean++
		}
		out.BySource[pr.Source]++
	}
	out.MeanMargin = sum / float64(len(s.pairs))
	out.CleanPairs = clean
	return out, true
}

// PrefExport is EXPORT's return.
type PrefExport struct {
	Dataset string `json:"dataset"`
	Format  string `json:"format"`
	Pairs   int    `json:"pairs"`
	JSONL   string `json:"jsonl"`
}

// Export serialises pairs to JSONL. Formats:
//
//   dpo   (default) — {"prompt": ..., "chosen": ..., "rejected": ...}
//   sft            — {"prompt": ..., "completion": <chosen>} (chosen-only)
//   rlhf           — {"prompt": ..., "responses": [chosen, rejected],
//                     "labels": [1.0, 0.0]}
//
// Filters: minMargin defaults to 0 (keep everything). source="" matches
// any source. limit<=0 means no cap.
func (p *Preferences) Export(dataset, format string, minMargin float64, source string, limit int) (PrefExport, bool) {
	if format == "" {
		format = "dpo"
	}
	format = strings.ToLower(format)
	switch format {
	case "dpo", "sft", "rlhf":
	default:
		return PrefExport{}, false
	}
	if minMargin < 0 {
		minMargin = 0
	}
	p.totalExports.Add(1)
	p.mu.RLock()
	s, ok := p.datasets[dataset]
	p.mu.RUnlock()
	if !ok {
		return PrefExport{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb strings.Builder
	count := 0
	for _, pr := range s.pairs {
		if pr.Margin < minMargin {
			continue
		}
		if source != "" && pr.Source != source {
			continue
		}
		var obj any
		switch format {
		case "dpo":
			obj = map[string]any{"prompt": pr.Prompt, "chosen": pr.Chosen, "rejected": pr.Rejected, "margin": pr.Margin}
		case "sft":
			obj = map[string]any{"prompt": pr.Prompt, "completion": pr.Chosen}
		case "rlhf":
			obj = map[string]any{"prompt": pr.Prompt, "responses": []string{pr.Chosen, pr.Rejected}, "labels": []float64{1.0, 0.0}}
		}
		bs, _ := json.Marshal(obj)
		sb.Write(bs)
		sb.WriteByte('\n')
		count++
		if limit > 0 && count >= limit {
			break
		}
	}
	return PrefExport{Dataset: dataset, Format: format, Pairs: count, JSONL: sb.String()}, true
}

// List returns every dataset id, sorted.
func (p *Preferences) List() []string {
	p.mu.RLock()
	out := make([]string, 0, len(p.datasets))
	for k := range p.datasets {
		out = append(out, k)
	}
	p.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a dataset. dataset="ALL" wipes all.
func (p *Preferences) Reset(dataset string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if dataset == "ALL" {
		n := len(p.datasets)
		p.datasets = map[string]*prefSet{}
		return n
	}
	if _, ok := p.datasets[dataset]; ok {
		delete(p.datasets, dataset)
		return 1
	}
	return 0
}

// PrefGlobalStats is STATS_GLOBAL's return.
type PrefGlobalStats struct {
	Datasets     int   `json:"datasets"`
	TotalPairs   int   `json:"total_pairs"`
	TotalRecords int64 `json:"total_records"`
	TotalExports int64 `json:"total_exports"`
	TotalDupes   int64 `json:"total_dupes"`
}

// GlobalStats returns the global counters.
func (p *Preferences) GlobalStats() PrefGlobalStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pairs := 0
	for _, s := range p.datasets {
		s.mu.RLock()
		pairs += len(s.pairs)
		s.mu.RUnlock()
	}
	return PrefGlobalStats{
		Datasets:     len(p.datasets),
		TotalPairs:   pairs,
		TotalRecords: p.totalRecords.Load(),
		TotalExports: p.totalExports.Load(),
		TotalDupes:   p.totalDupes.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (p *Preferences) setOrCreate(name string) *prefSet {
	p.mu.RLock()
	s, ok := p.datasets[name]
	p.mu.RUnlock()
	if ok {
		return s
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.datasets[name]; ok {
		return s
	}
	s = &prefSet{name: name, dedupe: map[string]bool{}}
	p.datasets[name] = s
	return s
}

func pairHash(prompt, chosen, rejected string) string {
	// Cheap stable hash — fnv1a over the concat (with delimiters that
	// can't appear in user content). The dedupe map is only ever
	// compared, never persisted across processes.
	h := uint32(2166136261)
	mix := func(s string) {
		for i := 0; i < len(s); i++ {
			h ^= uint32(s[i])
			h *= 16777619
		}
	}
	mix(prompt)
	h ^= 0
	mix(chosen)
	h ^= 1
	mix(rejected)
	return u32x(h)
}

func u32x(v uint32) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hex[v&0xf]
		v >>= 4
	}
	return string(out)
}
