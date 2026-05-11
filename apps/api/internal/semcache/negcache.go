package semcache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// NegCache (negative semantic cache) records queries that recently
// returned no match from SEMANTIC_GET. Subsequent identical queries
// skip the cosine-similarity scan entirely — the expensive part.
//
// Why this matters for AI apps: under any RAG / semantic-cache
// workload, a substantial fraction of incoming queries have no good
// match. The semantic-cache miss path costs O(N) cosine comparisons
// (every entry in the cache), and on a 100k-entry cache that's a
// real ~5-10 ms slug. If users repeat the same miss within seconds —
// a chatbot that answers fallback-style for a niche topic, an
// internal tool with the same handful of "no idea" queries — the
// negative cache turns those into ~50 ns lookups.
//
// Lock-free reads: sync.Map.Load + atomic counters. No mutex on the
// hot path. Per-entry TTL means the cache self-cleans without a
// background sweeper (lazy eviction in Check).
//
// Storage cost: 32-byte sha256 hash + ~32-byte timestamp/metadata =
// ~64 B per cached miss. 100k cached misses = ~6.4 MB. Cheap.
type NegCache struct {
	entries sync.Map // map[string]*negEntry — key is sha256(normalized query)

	hits      atomic.Int64 // returned "yes, this query was a recent miss"
	misses    atomic.Int64 // returned "no, never seen / expired"
	marks     atomic.Int64 // SEMNEG.MARK calls
	expirePurges atomic.Int64
}

// negEntry holds one cached miss.
type negEntry struct {
	expireAt time.Time // zero = lifetime; checked lazily on read
	markedAt time.Time
	count    atomic.Int64 // how many times this query was Check-hit
}

// NewNegCache returns an empty cache.
func NewNegCache() *NegCache { return &NegCache{} }

// normalizeQuery applies the same whitespace + case rules the
// semantic cache uses for key normalization. Keeps SEMNEG.MARK("FOO
// bar") and SEMNEG.CHECK("foo  bar") consistent — operators get the
// "obvious" behavior without needing to canonicalize on the client.
func normalizeQuery(q string) string {
	q = strings.TrimSpace(strings.ToLower(q))
	// Collapse runs of whitespace into single spaces. Cheap; a
	// strings.Fields + Join would be cleaner but that allocates
	// twice — this single pass keeps NegCache.Check at <100 ns.
	var b strings.Builder
	b.Grow(len(q))
	prevSpace := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteByte(c)
		prevSpace = false
	}
	return b.String()
}

// hashQuery returns the storage key for a query.
func hashQuery(q string) string {
	h := sha256.Sum256([]byte(normalizeQuery(q)))
	return hex.EncodeToString(h[:])
}

// Mark records that `query` had no semantic-cache hit. ttl=0 means
// "lifetime" (until SEMNEG.CLEAR or process restart); a positive
// ttl auto-expires the entry. Subsequent SEMANTIC_GET callers that
// run Check on the same query inside the TTL get a fast negative
// reply.
func (n *NegCache) Mark(query string, ttl time.Duration) {
	now := time.Now()
	e := &negEntry{markedAt: now}
	if ttl > 0 {
		e.expireAt = now.Add(ttl)
	}
	n.entries.Store(hashQuery(query), e)
	n.marks.Add(1)
}

// Check returns true if `query` was recently marked as a miss and
// the entry hasn't expired. Bumps the hit counter on the entry so
// SEMNEG.LIST can show "most-skipped queries" — useful for
// operators who want to know which queries are repeatedly missing
// (and might warrant a manual cache-warm).
//
// Lock-free: one sync.Map.Load + an atomic compare. Lazy expiry —
// expired entries are deleted on read so we don't need a sweeper
// goroutine.
func (n *NegCache) Check(query string) bool {
	v, ok := n.entries.Load(hashQuery(query))
	if !ok {
		n.misses.Add(1)
		return false
	}
	e := v.(*negEntry)
	if !e.expireAt.IsZero() && time.Now().After(e.expireAt) {
		n.entries.Delete(hashQuery(query))
		n.expirePurges.Add(1)
		n.misses.Add(1)
		return false
	}
	e.count.Add(1)
	n.hits.Add(1)
	return true
}

// Forget removes a single query's entry. Returns true if it was
// present. Used by ops paths after a manual cache-warm.
func (n *NegCache) Forget(query string) bool {
	_, was := n.entries.LoadAndDelete(hashQuery(query))
	return was
}

// Clear wipes the entire cache. Returns count removed.
func (n *NegCache) Clear() int {
	count := 0
	n.entries.Range(func(k, _ any) bool {
		n.entries.Delete(k)
		count++
		return true
	})
	return count
}

// NegStats is the snapshot returned by SEMNEG.STATS.
type NegStats struct {
	Hits          int64   `json:"hits"`
	Misses        int64   `json:"misses"`
	Marks         int64   `json:"marks"`
	ExpirePurges  int64   `json:"expire_purges"`
	HitRate       float64 `json:"hit_rate"`
	UniqueEntries int     `json:"unique_entries"`
}

// Stats returns a snapshot. UniqueEntries walks sync.Map (O(N))
// because there's no cheap Len(); fine for an observability call.
func (n *NegCache) Stats() NegStats {
	hits := n.hits.Load()
	misses := n.misses.Load()
	total := hits + misses
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	count := 0
	n.entries.Range(func(_, _ any) bool { count++; return true })
	return NegStats{
		Hits:          hits,
		Misses:        misses,
		Marks:         n.marks.Load(),
		ExpirePurges:  n.expirePurges.Load(),
		HitRate:       rate,
		UniqueEntries: count,
	}
}

// NegEntry is one row in SEMNEG.LIST output.
type NegEntry struct {
	Hash     string `json:"hash"`
	Hits     int64  `json:"hits"`     // times Check returned true for this entry
	AgeSec   int64  `json:"age_sec"`  // since markedAt
	TTLSec   int64  `json:"ttl_sec"`  // -1 = lifetime
}

// List returns up to `limit` cached entries, newest-first by
// markedAt. limit<=0 returns all. Used by the dashboard's "queries
// being skipped" panel.
func (n *NegCache) List(limit int) []NegEntry {
	now := time.Now()
	var rows []NegEntry
	n.entries.Range(func(k, v any) bool {
		e := v.(*negEntry)
		row := NegEntry{
			Hash:   k.(string),
			Hits:   e.count.Load(),
			AgeSec: int64(now.Sub(e.markedAt).Seconds()),
			TTLSec: -1,
		}
		if !e.expireAt.IsZero() {
			row.TTLSec = int64(e.expireAt.Sub(now).Seconds())
		}
		rows = append(rows, row)
		return true
	})
	// Sort newest-first (by smallest AgeSec). Insertion-sort is fine —
	// limit is typically small (UI lists 20-100 rows).
	for i := 1; i < len(rows); i++ {
		j := i
		for j > 0 && rows[j].AgeSec < rows[j-1].AgeSec {
			rows[j], rows[j-1] = rows[j-1], rows[j]
			j--
		}
	}
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows
}
