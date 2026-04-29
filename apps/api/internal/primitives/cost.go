package primitives

import (
	"sort"
	"sync"
	"time"
)

// CostTable tracks "what did this cache entry cost to compute" so the
// eviction loop can drop low-value bytes first instead of blindly
// LRUing. Cost is opaque (USD, ms, tokens, custom score) — we just
// rank by cost-per-byte when evicting.
//
// CACHE.WEIGH key cost          — annotate an existing key
// CACHE.UNWEIGH key             — drop the annotation
// CACHE.WEIGHTS                 — list every annotated key (debug)
// CACHE.STATS                   — rollup of total tracked + savings
//
// The store's eviction scorer queries this table when present and
// folds the cost into its scoring formula — keys with no cost
// annotation behave exactly like before.
type CostTable struct {
	mu      sync.RWMutex
	entries map[string]*costEntry
	stats   costStats
}

type costEntry struct {
	cost      float64
	hits      int64
	addedAt   time.Time
}

type costStats struct {
	tracked       int64
	totalCost     float64
	hitsServed    int64
	totalSaved    float64
}

// NewCostTable returns an empty table.
func NewCostTable() *CostTable {
	return &CostTable{entries: map[string]*costEntry{}}
}

// Weigh annotates `key` with `cost`. Subsequent SCAN-style eviction
// queries `Score(key)` to rank candidates.
func (c *CostTable) Weigh(key string, cost float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cost <= 0 {
		delete(c.entries, key)
		return
	}
	if e, ok := c.entries[key]; ok {
		c.stats.totalCost -= e.cost
	} else {
		c.stats.tracked++
	}
	c.entries[key] = &costEntry{cost: cost, addedAt: time.Now()}
	c.stats.totalCost += cost
}

// Unweigh drops the annotation.
func (c *CostTable) Unweigh(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.stats.totalCost -= e.cost
		c.stats.tracked--
		delete(c.entries, key)
		return true
	}
	return false
}

// RecordHit lets callers credit the cache savings — typically the
// LLM-cache or semantic-cache wrapper invokes this on every hit.
func (c *CostTable) RecordHit(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.hits++
		c.stats.hitsServed++
		c.stats.totalSaved += e.cost
	}
}

// Cost returns the annotated cost (0 when missing).
func (c *CostTable) Cost(key string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.entries[key]; ok {
		return e.cost
	}
	return 0
}

// Score is what the eviction scorer multiplies by. We define it as
// cost × (1 + hits) so frequently-hit, expensive entries score
// highest (most worth keeping) and never-hit cheap entries score
// lowest. Returning 1.0 for un-annotated keys keeps the existing
// scoring untouched.
func (c *CostTable) Score(key string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return 1.0
	}
	return e.cost * float64(1+e.hits)
}

// Snapshot returns the table sorted by descending score — useful for
// CACHE.WEIGHTS introspection.
type CostRow struct {
	Key   string
	Cost  float64
	Hits  int64
	Score float64
}

func (c *CostTable) Snapshot() []CostRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]CostRow, 0, len(c.entries))
	for k, e := range c.entries {
		out = append(out, CostRow{Key: k, Cost: e.cost, Hits: e.hits, Score: e.cost * float64(1+e.hits)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Stats returns the running totals shown by CACHE.STATS.
type CostStats struct {
	TrackedKeys int64
	TotalCost   float64
	HitsServed  int64
	TotalSaved  float64
}

func (c *CostTable) Stats() CostStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CostStats{
		TrackedKeys: c.stats.tracked,
		TotalCost:   c.stats.totalCost,
		HitsServed:  c.stats.hitsServed,
		TotalSaved:  c.stats.totalSaved,
	}
}
