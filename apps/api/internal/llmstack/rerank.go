package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// RerankCache memoises cross-encoder rerank scores by (query, doc_id)
// pair. Every production RAG app eventually adds a reranker after the
// hybrid (BM25 + vector) retrieval step — Cohere Rerank, BGE-rerank,
// Jina, Voyage. Each call costs money or local-GPU time; the same
// (query, doc) pair is rescored thousands of times across a session.
//
// Why this lives in the cache, not in app code:
//
//   - Reranker calls are per-pair, not per-query. A query that returns
//     20 candidates fires 20 rerank calls. Caching at the (query, doc)
//     pair level deduplicates across user sessions, not just within
//     one request.
//   - Document content rarely changes; query strings repeat (popular
//     queries follow a power law). The hit rate is empirically very
//     high — teams report 60-90% on production traffic.
//   - The cache layer is the natural place for it: same RESP server
//     that holds the docs serves the rerank scores.
//   - Stats track saved_calls + saved_usd so dashboards can show the
//     cost reduction directly.
//
// Storage model: in-memory map keyed by `query_hash:doc_id`. Each
// entry stores (score, expires_at). Optional TTL per entry (default
// 24h — reranker outputs are stable for the same content but stale
// quickly when docs update). LRU-ish eviction via a soft cap; when
// full, ~10% of the oldest entries are dropped (single sweep).
//
// Bulk API (RERANK.SCORE) is the production hot path: pass the query
// + doc_ids, get back the cached scores in the same order with a
// parallel hits/misses bitmap so apps know which pairs still need an
// upstream call.
type RerankCache struct {
	mu sync.RWMutex

	entries map[string]*rerankEntry
	cap     int // soft cap; 0 = unlimited
	costUSD float64 // configured cost per upstream call

	totalGets   atomic.Int64
	totalHits   atomic.Int64
	totalMisses atomic.Int64
	totalSets   atomic.Int64
	savedCalls  atomic.Int64
	totalEvicts atomic.Int64
}

type rerankEntry struct {
	score     float64
	expiresAt int64 // unix-nano; 0 = no expiry
	createdAt int64 // unix-nano, used for soft eviction order
}

// NewRerankCache returns an empty cache with a 100k entry soft cap
// and zero per-call cost (set via SetCostUSD).
func NewRerankCache() *RerankCache {
	return &RerankCache{
		entries: map[string]*rerankEntry{},
		cap:     100_000,
	}
}

// SetCap adjusts the soft eviction threshold. <= 0 disables eviction.
func (c *RerankCache) SetCap(n int) {
	c.mu.Lock()
	c.cap = n
	c.mu.Unlock()
}

// SetCostUSD records the upstream rerank-call cost so STATS can report
// saved_usd. Apps configure once at boot.
func (c *RerankCache) SetCostUSD(usd float64) {
	c.mu.Lock()
	c.costUSD = usd
	c.mu.Unlock()
}

// Get looks up a cached score. Returns (score, true) on hit;
// (0, false) on miss or expired.
func (c *RerankCache) Get(query, docID string) (float64, bool) {
	c.totalGets.Add(1)
	k := rerankKey(query, docID)
	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()
	if !ok {
		c.totalMisses.Add(1)
		return 0, false
	}
	if e.expiresAt != 0 && time.Now().UnixNano() > e.expiresAt {
		c.mu.Lock()
		delete(c.entries, k)
		c.mu.Unlock()
		c.totalMisses.Add(1)
		return 0, false
	}
	c.totalHits.Add(1)
	c.savedCalls.Add(1)
	return e.score, true
}

// Set stores a score with optional TTL (zero = no expiry). Evicts
// the oldest 10% if the cache is at cap.
func (c *RerankCache) Set(query, docID string, score float64, ttl time.Duration) {
	c.totalSets.Add(1)
	k := rerankKey(query, docID)
	now := time.Now().UnixNano()
	exp := int64(0)
	if ttl > 0 {
		exp = now + ttl.Nanoseconds()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cap > 0 && len(c.entries) >= c.cap {
		c.evictOldestLocked(c.cap / 10)
	}
	c.entries[k] = &rerankEntry{
		score:     score,
		expiresAt: exp,
		createdAt: now,
	}
}

// ScoreBatch is the bulk API for the production hot path. Returns
// scores in the same order as docIDs; misses get score=NaN. The
// `hits` bool slice tells apps which entries need an upstream call.
type BatchResult struct {
	Scores []float64 `json:"scores"`
	Hits   []bool    `json:"hits"`
	HitN   int       `json:"hit_n"`
	MissN  int       `json:"miss_n"`
}

func (c *RerankCache) ScoreBatch(query string, docIDs []string) BatchResult {
	out := BatchResult{
		Scores: make([]float64, len(docIDs)),
		Hits:   make([]bool, len(docIDs)),
	}
	for i, d := range docIDs {
		s, ok := c.Get(query, d)
		if ok {
			out.Scores[i] = s
			out.Hits[i] = true
			out.HitN++
		} else {
			out.Scores[i] = math.NaN()
			out.MissN++
		}
	}
	return out
}

// Forget drops one entry. Returns true if it existed.
func (c *RerankCache) Forget(query, docID string) bool {
	k := rerankKey(query, docID)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[k]
	delete(c.entries, k)
	return ok
}

// Purge wipes the cache. Returns the number of dropped entries.
func (c *RerankCache) Purge() int {
	c.mu.Lock()
	n := len(c.entries)
	c.entries = map[string]*rerankEntry{}
	c.mu.Unlock()
	return n
}

// RerankStats is the global counters snapshot.
type RerankStats struct {
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
}

func (c *RerankCache) Stats() RerankStats {
	c.mu.RLock()
	n := len(c.entries)
	cap := c.cap
	cost := c.costUSD
	c.mu.RUnlock()
	gets := c.totalGets.Load()
	hits := c.totalHits.Load()
	rate := 0.0
	if gets > 0 {
		rate = float64(hits) / float64(gets)
	}
	saved := c.savedCalls.Load()
	return RerankStats{
		Entries:     n,
		Cap:         cap,
		TotalGets:   gets,
		TotalHits:   hits,
		TotalMisses: c.totalMisses.Load(),
		TotalSets:   c.totalSets.Load(),
		SavedCalls:  saved,
		SavedUSD:    float64(saved) * cost,
		HitRate:     rate,
		TotalEvicts: c.totalEvicts.Load(),
		CostUSD:     cost,
	}
}

// ─── helpers ───────────────────────────────────────────────────

// rerankKey hashes (query, doc_id) into a 16-byte hex prefix. Avoids
// blowing the map keyspace with multi-KB query strings while keeping
// collisions vanishingly unlikely (64 bits of entropy).
func rerankKey(query, docID string) string {
	h := sha256.New()
	h.Write([]byte(query))
	h.Write([]byte{0})
	h.Write([]byte(docID))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// evictOldestLocked drops the n oldest entries by createdAt. Caller
// holds c.mu (write).
func (c *RerankCache) evictOldestLocked(n int) {
	if n <= 0 || len(c.entries) == 0 {
		return
	}
	type kv struct {
		k  string
		at int64
	}
	all := make([]kv, 0, len(c.entries))
	for k, e := range c.entries {
		all = append(all, kv{k, e.createdAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
	if n > len(all) {
		n = len(all)
	}
	for i := 0; i < n; i++ {
		delete(c.entries, all[i].k)
	}
	c.totalEvicts.Add(int64(n))
}

// SortedDocIDsByScore is a small convenience: given a query, return
// cached doc_ids ordered by score desc. Linear scan (the cache is
// keyed by hashed pairs) — only used for inspection, never on the
// hot path.
func (c *RerankCache) SortedDocIDsByScore(query string, candidates []string) []string {
	type kv struct {
		id    string
		score float64
	}
	pairs := make([]kv, 0, len(candidates))
	for _, d := range candidates {
		if s, ok := c.Get(query, d); ok {
			pairs = append(pairs, kv{d, s})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.id
	}
	return out
}
