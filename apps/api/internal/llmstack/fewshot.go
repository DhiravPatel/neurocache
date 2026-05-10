package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// FewShotBank is a labeled-example library with semantic retrieval
// for in-context learning (ICL). Every team that builds an LLM agent
// hits the same step: "give me the K most similar past examples for
// this input so I can include them in the prompt." Apps re-implement
// this with cosine sim over a list of (input, output) tuples in
// every project.
//
// FEWSHOT.* gives the cache a single store + retrieval API:
//
//   - FEWSHOT.ADD bank-id ex-id input output [TAGS ...] [EMBED vec]
//   - FEWSHOT.QUERY bank-id input K [TAGS ...]
//   - FEWSHOT.GET / DEL / LIST / STATS
//
// Why this lives in the cache:
//
//   - Examples are reused across requests and across processes.
//     Caching them locally per-process means each replica re-loads
//     them from a SQL store on boot — wasteful + slow.
//   - Retrieval is hot-path: every prompt build pulls top-K. Cosine
//     sim against a few thousand examples is microseconds in Go.
//   - Apps want to add/remove examples at runtime as the agent's
//     skill grows. RESP commands are the right interface.
//
// Embedding model: each bank lazily computes embeddings on first
// query when none are provided at ADD time, using the lightweight
// hashed-bag-of-words fallback (same as the embcache fallback).
// Apps that want better quality pass real embeddings (from OpenAI /
// Cohere / local model) at ADD time via the EMBED option.
//
// Optional tag filter on QUERY narrows the search space — useful
// for multi-tenant banks where a tenant only sees their own examples.
type FewShotBank struct {
	mu    sync.RWMutex
	banks map[string]*shotBank

	totalAdds    atomic.Int64
	totalQueries atomic.Int64
	totalReturns atomic.Int64
}

type shotBank struct {
	id       string
	examples map[string]*shotExample // ex_id -> example
	dim      int                     // active embedding dim (set on first vector)
}

type shotExample struct {
	id     string
	input  string
	output string
	tags   map[string]bool
	vec    []float64
}

// NewFewShotBank returns an empty registry.
func NewFewShotBank() *FewShotBank {
	return &FewShotBank{banks: map[string]*shotBank{}}
}

// AddOpts configures FEWSHOT.ADD.
type AddOpts struct {
	Tags []string
	Vec  []float64 // optional pre-computed embedding
}

// Add registers (or replaces) an example. Empty bank-id / ex-id /
// input is rejected.
func (f *FewShotBank) Add(bankID, exID, input, output string, opts AddOpts) error {
	if bankID == "" || exID == "" {
		return errors.New("bank_id and ex_id required")
	}
	if input == "" {
		return errors.New("input required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.banks[bankID]
	if !ok {
		b = &shotBank{id: bankID, examples: map[string]*shotExample{}}
		f.banks[bankID] = b
	}
	tags := map[string]bool{}
	for _, t := range opts.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			tags[t] = true
		}
	}
	vec := opts.Vec
	if vec == nil {
		vec = embedFallback(input)
	}
	if b.dim == 0 {
		b.dim = len(vec)
	} else if len(vec) != b.dim {
		return errors.New("embedding dim mismatch with existing bank examples")
	}
	b.examples[exID] = &shotExample{
		id:     exID,
		input:  input,
		output: output,
		tags:   tags,
		vec:    vec,
	}
	f.totalAdds.Add(1)
	return nil
}

// QueryHit is one retrieved example with its similarity score.
type QueryHit struct {
	ID     string   `json:"id"`
	Input  string   `json:"input"`
	Output string   `json:"output"`
	Tags   []string `json:"tags,omitempty"`
	Score  float64  `json:"score"`
}

// QueryOpts narrows the QUERY search.
type QueryOpts struct {
	K    int
	Tags []string  // hits must have ALL specified tags
	Vec  []float64 // optional pre-computed query embedding
}

// Query returns the top-K most-similar examples by cosine. Empty K
// defaults to 3.
func (f *FewShotBank) Query(bankID, input string, opts QueryOpts) ([]QueryHit, bool) {
	f.totalQueries.Add(1)
	f.mu.RLock()
	b, ok := f.banks[bankID]
	if !ok {
		f.mu.RUnlock()
		return nil, false
	}
	k := opts.K
	if k <= 0 {
		k = 3
	}
	queryVec := opts.Vec
	if queryVec == nil {
		queryVec = embedFallback(input)
	}
	if b.dim == 0 || len(queryVec) != b.dim {
		// Empty bank or dim mismatch — treat as empty result, not error.
		f.mu.RUnlock()
		return []QueryHit{}, true
	}
	want := map[string]bool{}
	for _, t := range opts.Tags {
		want[strings.ToLower(strings.TrimSpace(t))] = true
	}
	hits := make([]QueryHit, 0, len(b.examples))
	for _, ex := range b.examples {
		if !tagsMatch(ex.tags, want) {
			continue
		}
		score := cosine(queryVec, ex.vec)
		hits = append(hits, QueryHit{
			ID:     ex.id,
			Input:  ex.input,
			Output: ex.output,
			Tags:   tagsList(ex.tags),
			Score:  score,
		})
	}
	f.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	f.totalReturns.Add(int64(len(hits)))
	return hits, true
}

// Get returns a single example or false.
func (f *FewShotBank) Get(bankID, exID string) (QueryHit, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	b, ok := f.banks[bankID]
	if !ok {
		return QueryHit{}, false
	}
	ex, ok := b.examples[exID]
	if !ok {
		return QueryHit{}, false
	}
	return QueryHit{
		ID: ex.id, Input: ex.input, Output: ex.output,
		Tags: tagsList(ex.tags), Score: 1.0,
	}, true
}

// Del drops one example. Returns true if it existed.
func (f *FewShotBank) Del(bankID, exID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.banks[bankID]
	if !ok {
		return false
	}
	_, was := b.examples[exID]
	delete(b.examples, exID)
	return was
}

// Forget drops an entire bank.
func (f *FewShotBank) Forget(bankID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.banks[bankID]
	delete(f.banks, bankID)
	return ok
}

// List returns every example in a bank, ordered by id.
func (f *FewShotBank) List(bankID string) []QueryHit {
	f.mu.RLock()
	defer f.mu.RUnlock()
	b, ok := f.banks[bankID]
	if !ok {
		return nil
	}
	out := make([]QueryHit, 0, len(b.examples))
	for _, ex := range b.examples {
		out = append(out, QueryHit{
			ID: ex.id, Input: ex.input, Output: ex.output,
			Tags: tagsList(ex.tags),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BankRow is one row of FEWSHOT.BANKS.
type BankRow struct {
	BankID    string `json:"bank_id"`
	Examples  int    `json:"examples"`
	Dim       int    `json:"dim"`
}

func (f *FewShotBank) Banks() []BankRow {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]BankRow, 0, len(f.banks))
	for id, b := range f.banks {
		out = append(out, BankRow{BankID: id, Examples: len(b.examples), Dim: b.dim})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BankID < out[j].BankID })
	return out
}

// FewShotStats is the global counters snapshot.
type FewShotStats struct {
	TotalAdds    int64 `json:"total_adds"`
	TotalQueries int64 `json:"total_queries"`
	TotalReturns int64 `json:"total_returns"`
	Banks        int   `json:"banks"`
	Examples     int   `json:"examples"`
}

func (f *FewShotBank) Stats() FewShotStats {
	f.mu.RLock()
	banks := len(f.banks)
	examples := 0
	for _, b := range f.banks {
		examples += len(b.examples)
	}
	f.mu.RUnlock()
	return FewShotStats{
		TotalAdds:    f.totalAdds.Load(),
		TotalQueries: f.totalQueries.Load(),
		TotalReturns: f.totalReturns.Load(),
		Banks:        banks,
		Examples:     examples,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func tagsMatch(have, want map[string]bool) bool {
	if len(want) == 0 {
		return true
	}
	for t := range want {
		if !have[t] {
			return false
		}
	}
	return true
}

func tagsList(tags map[string]bool) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	for t := range tags {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// embedFallback is a deterministic 128-dim hashed-BoW vector. Same
// algorithm as embcache's fallback so the two are interchangeable
// when the app hasn't wired a real embedding model. Cheap, stable,
// captures topical similarity well enough for ICL when no model is
// available.
func embedFallback(text string) []float64 {
	const dim = 128
	out := make([]float64, dim)
	tokens := tokenize(text)
	for _, t := range tokens {
		h := fnv1a32(t)
		out[h%uint32(dim)] += 1
	}
	// L2-normalize so cosine reduces to dot product cleanly.
	var sum float64
	for _, v := range out {
		sum += v * v
	}
	if sum == 0 {
		return out
	}
	norm := math.Sqrt(sum)
	for i := range out {
		out[i] /= norm
	}
	return out
}

func fnv1a32(s string) uint32 {
	const offset = 2166136261
	const prime = 16777619
	h := uint32(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}
