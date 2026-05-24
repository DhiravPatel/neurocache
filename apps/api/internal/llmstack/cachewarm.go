package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// CacheWarmer turns historical query logs into a cache-warming
// dataset for cold starts.
//
// The cold-start problem: a new region, a new tenant, a major
// product launch — the semantic cache is empty, hit-rate is 0%,
// every request costs the full LLM. Teams that already have a
// production query log have the answer sitting in it: replay the
// last 30 days of queries to pre-populate the cache before the
// traffic spike. Most teams write that replay script by hand and
// forget to deduplicate, ending up paying for "summarize the doc"
// 200 times.
//
// CACHE.WARM.* is the warming dataset as a primitive:
//
//   RECORD    feed a historical query (with optional weight).
//   PLAN      return the deduplicated, weight-sorted plan of
//             queries the caller should fire to warm the cache.
//             Dedup is semantic — paraphrases collapse onto one
//             plan entry whose weight is the sum.
//   MARK      caller calls this after each warm so PROGRESS works.
//   PROGRESS  total / warmed / remaining counters.
//
// Commands:
//
//   CACHE.WARM.RECORD warm-id query [WEIGHT w]
//        WEIGHT defaults to 1 (one occurrence). Apps that already
//        aggregated their log pass the actual count.
//   CACHE.WARM.PLAN warm-id [LIMIT n] [MIN_SIM f]
//        → ordered list of {query, weight} to fire. MIN_SIM
//        defaults to 0.85 — closer paraphrases collapse.
//   CACHE.WARM.MARK warm-id query
//        Idempotent. Tracks which planned queries have been warmed.
//   CACHE.WARM.PROGRESS warm-id
//        → total / warmed / remaining / pct_complete.
//   CACHE.WARM.LIST
//   CACHE.WARM.RESET warm-id|ALL
//   CACHE.WARM.STATS
//
// Hot path: RECORD is one embedFallback + linear-scan dedup. PLAN
// returns the pre-deduplicated list sorted by weight desc.
type CacheWarmer struct {
	mu    sync.RWMutex
	plans map[string]*warmPlan

	totalRecords atomic.Int64
	totalPlans   atomic.Int64
	totalMarks   atomic.Int64
}

type warmPlan struct {
	mu      sync.RWMutex
	entries []*warmEntry
	warmed  map[string]bool // entry key (canonical query) → marked
	minSim  float64
}

type warmEntry struct {
	Query    string
	Vec      []float64
	Weight   float64
}

// NewCacheWarmer returns an empty warmer.
func NewCacheWarmer() *CacheWarmer {
	return &CacheWarmer{plans: map[string]*warmPlan{}}
}

// Record adds (or merges into an existing semantic neighbour) one
// historical query. Default merge threshold is 0.85.
func (c *CacheWarmer) Record(warmID, query string, weight float64) error {
	if warmID == "" {
		return errors.New("warm_id required")
	}
	if query == "" {
		return errors.New("query required")
	}
	if weight < 0 {
		return errors.New("weight must be non-negative")
	}
	if weight == 0 {
		weight = 1
	}
	c.totalRecords.Add(1)
	p := c.planOrCreate(warmID)
	vec := embedFallback(query)
	p.mu.Lock()
	defer p.mu.Unlock()
	minSim := p.minSim
	if minSim <= 0 {
		minSim = 0.85
	}
	// Find an existing entry to merge into
	for _, e := range p.entries {
		if dotProduct(e.Vec, vec) >= minSim {
			e.Weight += weight
			return nil
		}
	}
	p.entries = append(p.entries, &warmEntry{
		Query: query, Vec: vec, Weight: weight,
	})
	return nil
}

// SetMinSim overrides the default merge threshold for one warm plan.
// Useful when the caller has wired a real sentence-transformer that
// produces higher cosines than the hashed-BoW fallback.
func (c *CacheWarmer) SetMinSim(warmID string, sim float64) error {
	if warmID == "" {
		return errors.New("warm_id required")
	}
	if sim < 0 || sim > 1 {
		return errors.New("min_sim must be in [0,1]")
	}
	p := c.planOrCreate(warmID)
	p.mu.Lock()
	p.minSim = sim
	p.mu.Unlock()
	return nil
}

// WarmPlanRow is one row of PLAN output.
type WarmPlanRow struct {
	Query  string  `json:"query"`
	Weight float64 `json:"weight"`
	Warmed bool    `json:"warmed"`
}

// Plan returns the deduplicated, weight-sorted plan of queries to fire.
func (c *CacheWarmer) Plan(warmID string, limit int) ([]WarmPlanRow, bool) {
	c.totalPlans.Add(1)
	c.mu.RLock()
	p, ok := c.plans[warmID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]WarmPlanRow, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, WarmPlanRow{
			Query: e.Query, Weight: e.Weight,
			Warmed: p.warmed[e.Query],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Warmed != out[j].Warmed {
			// Unwarmed first (queue priority for the caller)
			return !out[i].Warmed
		}
		return out[i].Weight > out[j].Weight
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, true
}

// Mark records that a planned query has been warmed. The marker is
// keyed on the canonical query string (the one returned by PLAN),
// not on the original RECORD inputs.
func (c *CacheWarmer) Mark(warmID, query string) error {
	if warmID == "" {
		return errors.New("warm_id required")
	}
	if query == "" {
		return errors.New("query required")
	}
	c.totalMarks.Add(1)
	c.mu.RLock()
	p, ok := c.plans[warmID]
	c.mu.RUnlock()
	if !ok {
		return errors.New("unknown warm_id: " + warmID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warmed == nil {
		p.warmed = map[string]bool{}
	}
	p.warmed[query] = true
	return nil
}

// WarmProgress is PROGRESS's return.
type WarmProgress struct {
	WarmID      string  `json:"warm_id"`
	Total       int     `json:"total"`
	Warmed      int     `json:"warmed"`
	Remaining   int     `json:"remaining"`
	PctComplete float64 `json:"pct_complete"`
}

// Progress returns the warm-pipeline status.
func (c *CacheWarmer) Progress(warmID string) (WarmProgress, bool) {
	c.mu.RLock()
	p, ok := c.plans[warmID]
	c.mu.RUnlock()
	if !ok {
		return WarmProgress{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	total := len(p.entries)
	warmed := 0
	for _, e := range p.entries {
		if p.warmed[e.Query] {
			warmed++
		}
	}
	out := WarmProgress{
		WarmID: warmID, Total: total,
		Warmed: warmed, Remaining: total - warmed,
	}
	if total > 0 {
		out.PctComplete = float64(warmed) / float64(total)
	}
	return out, true
}

// List returns every warm id, sorted.
func (c *CacheWarmer) List() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.plans))
	for k := range c.plans {
		out = append(out, k)
	}
	c.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a warm plan. warmID="ALL" wipes all.
func (c *CacheWarmer) Reset(warmID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if warmID == "ALL" {
		n := len(c.plans)
		c.plans = map[string]*warmPlan{}
		return n
	}
	if _, ok := c.plans[warmID]; ok {
		delete(c.plans, warmID)
		return 1
	}
	return 0
}

// CacheWarmStats is the global snapshot.
type CacheWarmStats struct {
	Plans        int   `json:"plans"`
	TotalEntries int   `json:"total_entries"`
	TotalRecords int64 `json:"total_records"`
	TotalPlans   int64 `json:"total_plans"`
	TotalMarks   int64 `json:"total_marks"`
}

func (c *CacheWarmer) Stats() CacheWarmStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := 0
	for _, p := range c.plans {
		p.mu.RLock()
		entries += len(p.entries)
		p.mu.RUnlock()
	}
	return CacheWarmStats{
		Plans:        len(c.plans),
		TotalEntries: entries,
		TotalRecords: c.totalRecords.Load(),
		TotalPlans:   c.totalPlans.Load(),
		TotalMarks:   c.totalMarks.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (c *CacheWarmer) planOrCreate(id string) *warmPlan {
	c.mu.RLock()
	p, ok := c.plans[id]
	c.mu.RUnlock()
	if ok {
		return p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.plans[id]; ok {
		return p
	}
	p = &warmPlan{warmed: map[string]bool{}}
	c.plans[id] = p
	return p
}
