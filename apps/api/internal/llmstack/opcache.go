package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// OpCache memoises deterministic LLM operations by exact (op_id,
// input, model, params) match. Different from the semantic cache —
// SEM matches paraphrases ("what's bitcoin?" hits "tell me about
// bitcoin"). OPCACHE matches EXACTLY, intended for temperature=0
// workloads where identical inputs must produce identical outputs:
//
//   - Code completion / generation
//   - SQL generation from natural language
//   - Named-entity / structured-data extraction
//   - Function-call argument synthesis
//   - JSON-mode outputs with fixed schemas
//
// For these, a paraphrase match is WRONG (you want bit-identical
// behaviour) but exact-match caching is hugely valuable — the same
// app sends the same prompt repeatedly across users and a hit drops
// upstream cost to zero.
//
// OPCACHE.* gives the cache one command set:
//
//   OPCACHE.SET op-id input output [MODEL m] [PARAMS json] [EX sec]
//   OPCACHE.GET op-id input [MODEL m] [PARAMS json]   -> output or nil
//   OPCACHE.FORGET op-id input [MODEL m] [PARAMS json]
//   OPCACHE.PURGE [OP op-id]                          -> int dropped
//   OPCACHE.SETCAP n
//   OPCACHE.SETCOST usd
//   OPCACHE.STATS                                     -> per-op hit rate
//
// Key is sha256(op_id|input|model|params)[:16]. PARAMS is hashed
// verbatim — different temperature / max_tokens / top_p produce
// different cache entries (correctly: their outputs differ).
//
// Storage: lock-free reads via RWMutex, atomic counters. Per-op_id
// stats via sync.Map. Soft cap (default 100k) with oldest-10% sweep
// eviction. Throughput target: GET in <300 ns.
type OpCache struct {
	mu      sync.RWMutex
	entries map[string]*opEntry
	cap     int
	costUSD float64

	totalGets   atomic.Int64
	totalHits   atomic.Int64
	totalMisses atomic.Int64
	totalSets   atomic.Int64
	savedCalls  atomic.Int64
	totalEvicts atomic.Int64

	perOp sync.Map // op_id -> *opStat
}

type opEntry struct {
	opID      string
	value     string
	expiresAt int64
	createdAt int64
}

type opStat struct {
	hits   atomic.Int64
	misses atomic.Int64
	sets   atomic.Int64
}

// NewOpCache returns an empty cache.
func NewOpCache() *OpCache {
	return &OpCache{
		entries: map[string]*opEntry{},
		cap:     100_000,
	}
}

// SetCap adjusts the soft eviction threshold.
func (o *OpCache) SetCap(n int) {
	o.mu.Lock()
	o.cap = n
	o.mu.Unlock()
}

// SetCostUSD configures $/upstream-call so STATS reports saved_usd.
func (o *OpCache) SetCostUSD(usd float64) {
	o.mu.Lock()
	o.costUSD = usd
	o.mu.Unlock()
}

// OpKey identifies a single entry. Apps pass model/params via OpKey
// when those affect the output (temp=0 → params usually fixed; if
// they vary, include them).
type OpKey struct {
	OpID   string
	Input  string
	Model  string
	Params string
}

// Set stores an output keyed by the full OpKey.
func (o *OpCache) Set(key OpKey, output string, ttl time.Duration) error {
	if key.OpID == "" || key.Input == "" {
		return errors.New("op_id and input required")
	}
	o.totalSets.Add(1)
	o.opStatFor(key.OpID).sets.Add(1)
	k := opCacheKey(key)
	now := time.Now().UnixNano()
	exp := int64(0)
	if ttl > 0 {
		exp = now + ttl.Nanoseconds()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cap > 0 && len(o.entries) >= o.cap {
		o.evictOldestLocked(o.cap / 10)
	}
	o.entries[k] = &opEntry{
		opID:      key.OpID,
		value:     output,
		expiresAt: exp,
		createdAt: now,
	}
	return nil
}

// Get returns the cached output or (empty, false) on miss.
func (o *OpCache) Get(key OpKey) (string, bool) {
	o.totalGets.Add(1)
	stat := o.opStatFor(key.OpID)
	k := opCacheKey(key)
	o.mu.RLock()
	e, ok := o.entries[k]
	o.mu.RUnlock()
	if !ok {
		o.totalMisses.Add(1)
		stat.misses.Add(1)
		return "", false
	}
	if e.expiresAt != 0 && time.Now().UnixNano() > e.expiresAt {
		o.mu.Lock()
		delete(o.entries, k)
		o.mu.Unlock()
		o.totalMisses.Add(1)
		stat.misses.Add(1)
		return "", false
	}
	o.totalHits.Add(1)
	o.savedCalls.Add(1)
	stat.hits.Add(1)
	return e.value, true
}

// Forget drops one entry.
func (o *OpCache) Forget(key OpKey) bool {
	k := opCacheKey(key)
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.entries[k]
	delete(o.entries, k)
	return ok
}

// Purge wipes entries. Empty op_id = all; otherwise just that op.
func (o *OpCache) Purge(opID string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	if opID == "" {
		n := len(o.entries)
		o.entries = map[string]*opEntry{}
		return n
	}
	n := 0
	for k, e := range o.entries {
		if e.opID == opID {
			delete(o.entries, k)
			n++
		}
	}
	return n
}

// OpStatsRow is one row of OPCACHE.STATS per-op list.
type OpStatsRow struct {
	OpID    string  `json:"op_id"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Sets    int64   `json:"sets"`
	HitRate float64 `json:"hit_rate"`
}

// OpCacheStats is the global counters snapshot.
type OpCacheStats struct {
	Entries     int          `json:"entries"`
	Cap         int          `json:"cap"`
	TotalGets   int64        `json:"total_gets"`
	TotalHits   int64        `json:"total_hits"`
	TotalMisses int64        `json:"total_misses"`
	TotalSets   int64        `json:"total_sets"`
	SavedCalls  int64        `json:"saved_calls"`
	SavedUSD    float64      `json:"saved_usd"`
	HitRate     float64      `json:"hit_rate"`
	TotalEvicts int64        `json:"total_evicts"`
	CostUSD     float64      `json:"cost_usd"`
	Ops         []OpStatsRow `json:"ops,omitempty"`
}

func (o *OpCache) Stats() OpCacheStats {
	o.mu.RLock()
	n := len(o.entries)
	cap := o.cap
	cost := o.costUSD
	o.mu.RUnlock()
	gets := o.totalGets.Load()
	hits := o.totalHits.Load()
	rate := 0.0
	if gets > 0 {
		rate = float64(hits) / float64(gets)
	}
	saved := o.savedCalls.Load()
	out := OpCacheStats{
		Entries:     n,
		Cap:         cap,
		TotalGets:   gets,
		TotalHits:   hits,
		TotalMisses: o.totalMisses.Load(),
		TotalSets:   o.totalSets.Load(),
		SavedCalls:  saved,
		SavedUSD:    float64(saved) * cost,
		HitRate:     rate,
		TotalEvicts: o.totalEvicts.Load(),
		CostUSD:     cost,
	}
	o.perOp.Range(func(k, v any) bool {
		os := v.(*opStat)
		h := os.hits.Load()
		m := os.misses.Load()
		hr := 0.0
		if h+m > 0 {
			hr = float64(h) / float64(h+m)
		}
		out.Ops = append(out.Ops, OpStatsRow{
			OpID: k.(string), Hits: h, Misses: m,
			Sets: os.sets.Load(), HitRate: hr,
		})
		return true
	})
	sort.Slice(out.Ops, func(i, j int) bool { return out.Ops[i].OpID < out.Ops[j].OpID })
	return out
}

// ─── helpers ───────────────────────────────────────────────────

func (o *OpCache) opStatFor(opID string) *opStat {
	if v, ok := o.perOp.Load(opID); ok {
		return v.(*opStat)
	}
	fresh := &opStat{}
	actual, _ := o.perOp.LoadOrStore(opID, fresh)
	return actual.(*opStat)
}

func opCacheKey(key OpKey) string {
	h := sha256.New()
	h.Write([]byte(key.OpID))
	h.Write([]byte{0})
	h.Write([]byte(key.Input))
	h.Write([]byte{0})
	h.Write([]byte(key.Model))
	h.Write([]byte{0})
	h.Write([]byte(key.Params))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (o *OpCache) evictOldestLocked(n int) {
	if n <= 0 || len(o.entries) == 0 {
		return
	}
	type kv struct {
		k  string
		at int64
	}
	all := make([]kv, 0, len(o.entries))
	for k, e := range o.entries {
		all = append(all, kv{k, e.createdAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
	if n > len(all) {
		n = len(all)
	}
	for i := 0; i < n; i++ {
		delete(o.entries, all[i].k)
	}
	o.totalEvicts.Add(int64(n))
}
