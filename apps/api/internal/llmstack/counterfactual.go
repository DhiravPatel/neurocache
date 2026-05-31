package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CounterfactualCache keys answers by (query, context-hash) so the
// same query against different supporting context yields distinct
// cache entries — and the deltas can be diffed. Classic semantic
// caches collapse "What's our refund policy?" to one entry regardless
// of which doc version was retrieved; counterfactual cache preserves
// the dependency, so you can answer:
//
//   - "what would the answer be if doc-44 said X instead of Y?"
//   - "show me every distinct answer we've ever given to this query"
//   - "the doc changed; how did the answer change?"
//
// This is the diff-aware RAG cache that no semantic cache ships.
//
// Commands:
//
//   CFCACHE.PUT query context-hash answer [REFS r1 r2 ...] [TTL seconds]
//        Stores one (query, context-hash) → answer pair.
//   CFCACHE.GET query context-hash
//        → answer / age_ms / refs / hit=1, or hit=0
//   CFCACHE.VARIANTS query [LIMIT n]
//        Every distinct (context-hash, answer) we've stored for the query.
//   CFCACHE.DIFF query context-hash-a context-hash-b
//        Compare two variants for the same query.
//   CFCACHE.FORGET query [CTX h]   — drop one query or one specific variant
//   CFCACHE.LIST                   — every known query
//   CFCACHE.STATS
//
// Hot path: GET is two map lookups. PUT is one map insert + an index
// append into the query → variants list. DIFF is a per-line string
// compare.
type CounterfactualCache struct {
	mu      sync.RWMutex
	queries map[string]*cfQuery

	totalPuts   atomic.Int64
	totalHits   atomic.Int64
	totalMisses atomic.Int64
}

type cfQuery struct {
	mu       sync.RWMutex
	variants map[string]*cfVariant // ctx-hash → variant
}

type cfVariant struct {
	Answer    string
	Refs      []string
	StoredAt  time.Time
	ExpiresAt time.Time
}

// NewCounterfactualCache returns an empty cache.
func NewCounterfactualCache() *CounterfactualCache {
	return &CounterfactualCache{queries: map[string]*cfQuery{}}
}

// Put stores (query, ctxHash) → answer.
func (c *CounterfactualCache) Put(query, ctxHash, answer string, refs []string, ttl time.Duration) error {
	if query == "" {
		return errors.New("query required")
	}
	if ctxHash == "" {
		return errors.New("context_hash required")
	}
	if answer == "" {
		return errors.New("answer required")
	}
	if ttl < 0 {
		return errors.New("ttl must be non-negative")
	}
	c.totalPuts.Add(1)
	q := c.queryOrCreate(query)
	v := &cfVariant{Answer: answer, Refs: append([]string{}, refs...), StoredAt: time.Now()}
	if ttl > 0 {
		v.ExpiresAt = v.StoredAt.Add(ttl)
	}
	q.mu.Lock()
	q.variants[ctxHash] = v
	q.mu.Unlock()
	return nil
}

// CFGetResult is GET's return.
type CFGetResult struct {
	Hit     bool     `json:"hit"`
	Answer  string   `json:"answer,omitempty"`
	Refs    []string `json:"refs,omitempty"`
	AgeMS   int64    `json:"age_ms,omitempty"`
}

// Get returns the cached answer for (query, ctxHash), or hit=false.
func (c *CounterfactualCache) Get(query, ctxHash string) CFGetResult {
	if query == "" || ctxHash == "" {
		c.totalMisses.Add(1)
		return CFGetResult{}
	}
	c.mu.RLock()
	q, ok := c.queries[query]
	c.mu.RUnlock()
	if !ok {
		c.totalMisses.Add(1)
		return CFGetResult{}
	}
	q.mu.RLock()
	v, ok := q.variants[ctxHash]
	q.mu.RUnlock()
	if !ok {
		c.totalMisses.Add(1)
		return CFGetResult{}
	}
	now := time.Now()
	if !v.ExpiresAt.IsZero() && now.After(v.ExpiresAt) {
		// Stale → drop + miss
		q.mu.Lock()
		delete(q.variants, ctxHash)
		q.mu.Unlock()
		c.totalMisses.Add(1)
		return CFGetResult{}
	}
	c.totalHits.Add(1)
	return CFGetResult{
		Hit: true, Answer: v.Answer,
		Refs:  append([]string{}, v.Refs...),
		AgeMS: now.Sub(v.StoredAt).Milliseconds(),
	}
}

// CFVariantRow is one row of VARIANTS.
type CFVariantRow struct {
	CtxHash string   `json:"ctx_hash"`
	Answer  string   `json:"answer"`
	Refs    []string `json:"refs,omitempty"`
	AgeMS   int64    `json:"age_ms"`
}

// Variants returns every distinct (ctxHash → answer) for this query.
func (c *CounterfactualCache) Variants(query string, limit int) ([]CFVariantRow, bool) {
	if query == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 50
	}
	c.mu.RLock()
	q, ok := c.queries[query]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	now := time.Now()
	out := make([]CFVariantRow, 0, len(q.variants))
	for h, v := range q.variants {
		if !v.ExpiresAt.IsZero() && now.After(v.ExpiresAt) {
			continue
		}
		out = append(out, CFVariantRow{
			CtxHash: h, Answer: v.Answer,
			Refs:  append([]string{}, v.Refs...),
			AgeMS: now.Sub(v.StoredAt).Milliseconds(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CtxHash < out[j].CtxHash })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, true
}

// CFDiffResult is DIFF's return.
type CFDiffResult struct {
	Query      string   `json:"query"`
	CtxA       string   `json:"ctx_a"`
	CtxB       string   `json:"ctx_b"`
	Identical  bool     `json:"identical"`
	OnlyInA    []string `json:"only_in_a"`
	OnlyInB    []string `json:"only_in_b"`
	CommonLines int     `json:"common_lines"`
}

// Diff compares two variants for the same query. Diff granularity is
// line-level — sufficient for "did the answer change" questions and
// avoids dragging in a real edit-distance library.
func (c *CounterfactualCache) Diff(query, ctxA, ctxB string) (CFDiffResult, bool) {
	if query == "" {
		return CFDiffResult{}, false
	}
	c.mu.RLock()
	q, ok := c.queries[query]
	c.mu.RUnlock()
	if !ok {
		return CFDiffResult{}, false
	}
	q.mu.RLock()
	va, okA := q.variants[ctxA]
	vb, okB := q.variants[ctxB]
	q.mu.RUnlock()
	if !okA || !okB {
		return CFDiffResult{}, false
	}
	out := CFDiffResult{Query: query, CtxA: ctxA, CtxB: ctxB}
	la := strings.Split(va.Answer, "\n")
	lb := strings.Split(vb.Answer, "\n")
	inB := map[string]bool{}
	for _, l := range lb {
		inB[l] = true
	}
	inA := map[string]bool{}
	for _, l := range la {
		inA[l] = true
	}
	for _, l := range la {
		if !inB[l] {
			out.OnlyInA = append(out.OnlyInA, l)
		} else {
			out.CommonLines++
		}
	}
	for _, l := range lb {
		if !inA[l] {
			out.OnlyInB = append(out.OnlyInB, l)
		}
	}
	out.Identical = len(out.OnlyInA) == 0 && len(out.OnlyInB) == 0
	return out, true
}

// Forget drops one query or one variant. If ctxHash="", the entire
// query is dropped. Use query="ALL" to wipe everything.
func (c *CounterfactualCache) Forget(query, ctxHash string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if query == "ALL" {
		n := 0
		for _, q := range c.queries {
			q.mu.RLock()
			n += len(q.variants)
			q.mu.RUnlock()
		}
		c.queries = map[string]*cfQuery{}
		return n
	}
	q, ok := c.queries[query]
	if !ok {
		return 0
	}
	if ctxHash == "" {
		n := len(q.variants)
		delete(c.queries, query)
		return n
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.variants[ctxHash]; ok {
		delete(q.variants, ctxHash)
		return 1
	}
	return 0
}

// List returns every known query (sorted).
func (c *CounterfactualCache) List() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.queries))
	for k := range c.queries {
		out = append(out, k)
	}
	c.mu.RUnlock()
	sort.Strings(out)
	return out
}

// CFStats is the global snapshot.
type CFStats struct {
	Queries     int   `json:"queries"`
	TotalVariants int `json:"total_variants"`
	TotalPuts   int64 `json:"total_puts"`
	TotalHits   int64 `json:"total_hits"`
	TotalMisses int64 `json:"total_misses"`
	HitRate     float64 `json:"hit_rate"`
}

func (c *CounterfactualCache) Stats() CFStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v := 0
	for _, q := range c.queries {
		q.mu.RLock()
		v += len(q.variants)
		q.mu.RUnlock()
	}
	hits := c.totalHits.Load()
	misses := c.totalMisses.Load()
	rate := 0.0
	if hits+misses > 0 {
		rate = float64(hits) / float64(hits+misses)
	}
	return CFStats{
		Queries: len(c.queries), TotalVariants: v,
		TotalPuts: c.totalPuts.Load(),
		TotalHits: hits, TotalMisses: misses, HitRate: rate,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (c *CounterfactualCache) queryOrCreate(query string) *cfQuery {
	c.mu.RLock()
	q, ok := c.queries[query]
	c.mu.RUnlock()
	if ok {
		return q
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if q, ok := c.queries[query]; ok {
		return q
	}
	q = &cfQuery{variants: map[string]*cfVariant{}}
	c.queries[query] = q
	return q
}
