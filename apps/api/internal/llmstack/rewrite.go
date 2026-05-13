package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RewriteCache memoises query-rewrite results by (technique, query).
// Every advanced RAG pipeline does query rewriting BEFORE retrieval —
// the techniques are well-known and the calls are expensive:
//
//   - hyDE:        hallucinate a hypothetical answer, embed THAT
//                  instead of the original question (better recall).
//   - step-back:   generalise the question one level ("Who is Einstein?"
//                  → "famous physicists") to grab broader context.
//   - decompose:   break a multi-part question into sub-questions, run
//                  each through retrieval separately.
//   - multi-query: paraphrase N times, union the retrieval results.
//   - paraphrase:  one alternative phrasing, often used as a fallback.
//
// Each call is a model call (cheap models are fine for rewriting, but
// they're not free) and the same query rewrites identically every
// time. Cache it.
//
// REWRITE.* gives the cache a single store:
//
//   REWRITE.SET technique query rewritten [EX sec]
//   REWRITE.GET technique query                -> rewritten or nil
//   REWRITE.SET_MULTI technique query v1 v2 v3 [EX sec]
//   REWRITE.LIST technique query               -> array of variants
//   REWRITE.FORGET technique query
//   REWRITE.PURGE [TECHNIQUE name]
//   REWRITE.STATS
//
// Lock-free reads via sync.Map + atomic counters. Key is sha256-
// hashed prefix to avoid blowing the map with multi-KB queries.
// Soft cap (default 50k entries per technique) with oldest-10%
// eviction when full.
type RewriteCache struct {
	mu      sync.RWMutex
	entries map[string]*rewriteEntry // hash -> entry

	cap        int
	costPerCallUSD float64

	totalGets   atomic.Int64
	totalHits   atomic.Int64
	totalMisses atomic.Int64
	totalSets   atomic.Int64
	savedCalls  atomic.Int64
	totalEvicts atomic.Int64

	// Per-technique counters via sync.Map for lock-free updates.
	perTech sync.Map // technique -> *techStat
}

type rewriteEntry struct {
	technique string
	query     string
	variants  []string
	expiresAt int64 // unix-nano; 0 = no expiry
	createdAt int64
}

type techStat struct {
	hits  atomic.Int64
	misses atomic.Int64
	sets  atomic.Int64
}

// NewRewriteCache returns an empty cache with a 50k soft cap and
// $0 per-call cost (configure via SetCostUSD).
func NewRewriteCache() *RewriteCache {
	return &RewriteCache{
		entries: map[string]*rewriteEntry{},
		cap:     50_000,
	}
}

// SetCap adjusts the soft eviction threshold. <= 0 disables eviction.
func (r *RewriteCache) SetCap(n int) {
	r.mu.Lock()
	r.cap = n
	r.mu.Unlock()
}

// SetCostUSD records $/upstream-rewrite-call so STATS can report
// saved_usd.
func (r *RewriteCache) SetCostUSD(usd float64) {
	r.mu.Lock()
	r.costPerCallUSD = usd
	r.mu.Unlock()
}

// Set stores a single rewritten variant.
func (r *RewriteCache) Set(technique, query, rewritten string, ttl time.Duration) error {
	return r.SetMulti(technique, query, []string{rewritten}, ttl)
}

// SetMulti stores multiple variants (for multi-query / decompose /
// paraphrase techniques that produce N outputs per call).
func (r *RewriteCache) SetMulti(technique, query string, variants []string, ttl time.Duration) error {
	if technique == "" {
		return errors.New("technique required")
	}
	if query == "" {
		return errors.New("query required")
	}
	if len(variants) == 0 {
		return errors.New("at least one variant required")
	}
	r.totalSets.Add(1)
	r.statFor(technique).sets.Add(1)
	k := rewriteKey(technique, query)
	now := time.Now().UnixNano()
	exp := int64(0)
	if ttl > 0 {
		exp = now + ttl.Nanoseconds()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cap > 0 && len(r.entries) >= r.cap {
		r.evictOldestLocked(r.cap / 10)
	}
	r.entries[k] = &rewriteEntry{
		technique: technique,
		query:     query,
		variants:  variants,
		expiresAt: exp,
		createdAt: now,
	}
	return nil
}

// Get returns the FIRST variant for (technique, query). Use List
// for techniques that produce multiple variants.
func (r *RewriteCache) Get(technique, query string) (string, bool) {
	vs, ok := r.List(technique, query)
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

// List returns every cached variant for (technique, query). Miss
// returns (nil, false).
func (r *RewriteCache) List(technique, query string) ([]string, bool) {
	r.totalGets.Add(1)
	k := rewriteKey(technique, query)
	r.mu.RLock()
	e, ok := r.entries[k]
	r.mu.RUnlock()
	stat := r.statFor(technique)
	if !ok {
		r.totalMisses.Add(1)
		stat.misses.Add(1)
		return nil, false
	}
	if e.expiresAt != 0 && time.Now().UnixNano() > e.expiresAt {
		r.mu.Lock()
		delete(r.entries, k)
		r.mu.Unlock()
		r.totalMisses.Add(1)
		stat.misses.Add(1)
		return nil, false
	}
	r.totalHits.Add(1)
	r.savedCalls.Add(1)
	stat.hits.Add(1)
	out := make([]string, len(e.variants))
	copy(out, e.variants)
	return out, true
}

// Forget drops one entry.
func (r *RewriteCache) Forget(technique, query string) bool {
	k := rewriteKey(technique, query)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[k]
	delete(r.entries, k)
	return ok
}

// Purge wipes either everything (technique="") or one technique's
// entries. Returns the number dropped.
func (r *RewriteCache) Purge(technique string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if technique == "" {
		n := len(r.entries)
		r.entries = map[string]*rewriteEntry{}
		return n
	}
	n := 0
	for k, e := range r.entries {
		if e.technique == technique {
			delete(r.entries, k)
			n++
		}
	}
	return n
}

// RewriteTechStatsRow is one row of REWRITE.STATS techniques list.
type RewriteTechStatsRow struct {
	Technique string  `json:"technique"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Sets      int64   `json:"sets"`
	HitRate   float64 `json:"hit_rate"`
}

// RewriteStats is the global counters snapshot.
type RewriteStats struct {
	Entries     int     `json:"entries"`
	Cap         int     `json:"cap"`
	TotalGets   int64   `json:"total_gets"`
	TotalHits   int64   `json:"total_hits"`
	TotalMisses int64   `json:"total_misses"`
	TotalSets   int64   `json:"total_sets"`
	SavedCalls  int64   `json:"saved_calls"`
	SavedUSD    float64 `json:"saved_usd"`
	HitRate     float64 `json:"hit_rate"`
	TotalEvicts int64   `json:"total_evicts"`
	CostUSD     float64 `json:"cost_usd"`
	Techniques  []RewriteTechStatsRow `json:"techniques,omitempty"`
}

func (r *RewriteCache) Stats() RewriteStats {
	r.mu.RLock()
	n := len(r.entries)
	cap := r.cap
	cost := r.costPerCallUSD
	r.mu.RUnlock()
	gets := r.totalGets.Load()
	hits := r.totalHits.Load()
	rate := 0.0
	if gets > 0 {
		rate = float64(hits) / float64(gets)
	}
	saved := r.savedCalls.Load()
	out := RewriteStats{
		Entries:     n,
		Cap:         cap,
		TotalGets:   gets,
		TotalHits:   hits,
		TotalMisses: r.totalMisses.Load(),
		TotalSets:   r.totalSets.Load(),
		SavedCalls:  saved,
		SavedUSD:    float64(saved) * cost,
		HitRate:     rate,
		TotalEvicts: r.totalEvicts.Load(),
		CostUSD:     cost,
	}
	r.perTech.Range(func(k, v any) bool {
		ts := v.(*techStat)
		h := ts.hits.Load()
		m := ts.misses.Load()
		hr := 0.0
		if h+m > 0 {
			hr = float64(h) / float64(h+m)
		}
		out.Techniques = append(out.Techniques, RewriteTechStatsRow{
			Technique: k.(string),
			Hits:      h, Misses: m, Sets: ts.sets.Load(),
			HitRate: hr,
		})
		return true
	})
	sort.Slice(out.Techniques, func(i, j int) bool {
		return out.Techniques[i].Technique < out.Techniques[j].Technique
	})
	return out
}

// ─── helpers ───────────────────────────────────────────────────

func (r *RewriteCache) statFor(technique string) *techStat {
	if v, ok := r.perTech.Load(technique); ok {
		return v.(*techStat)
	}
	fresh := &techStat{}
	actual, _ := r.perTech.LoadOrStore(technique, fresh)
	return actual.(*techStat)
}

// rewriteKey hashes (technique, query) into a 16-byte hex prefix.
func rewriteKey(technique, query string) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(technique)))
	h.Write([]byte{0})
	h.Write([]byte(query))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// evictOldestLocked drops the n oldest entries by createdAt.
func (r *RewriteCache) evictOldestLocked(n int) {
	if n <= 0 || len(r.entries) == 0 {
		return
	}
	type kv struct {
		k  string
		at int64
	}
	all := make([]kv, 0, len(r.entries))
	for k, e := range r.entries {
		all = append(all, kv{k, e.createdAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
	if n > len(all) {
		n = len(all)
	}
	for i := 0; i < n; i++ {
		delete(r.entries, all[i].k)
	}
	r.totalEvicts.Add(int64(n))
}
