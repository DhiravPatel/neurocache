// Package llmstack implements three production primitives every LLM app
// rebuilds in client code:
//
//   - Embedding cache (EMB.*) — embeddings are deterministic per (model,
//     text). Caching them at the engine kills the "same vector recomputed
//     a thousand times" cost.
//   - Conversation/session management (CONV.*) — ordered turn log with
//     token-aware windowing. Centralizes the truncation logic so apps
//     can't accidentally ship a context-overflow 500.
//   - Versioned prompt templates (PROMPT.*) — registry of prompt strings
//     with version history and {variable} interpolation. Auditability
//     plus safe rollback when a new prompt underperforms.
//
// All three are persistence-aware: writes flow through the engine's
// AOF/replication hooks, and the in-memory state is rebuilt from the
// command log on restart.
package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EmbCache caches deterministic embedding vectors keyed by the canonical
// hash of the input text. Two callers asking for the embedding of the
// same text — possibly with whitespace or case differences — see the
// same vector and don't re-pay the embedding cost.
type EmbCache struct {
	mu      sync.RWMutex
	entries map[string]*embEntry

	hits   atomic.Int64
	misses atomic.Int64

	// CostPerCall, in dollars, is the operator-supplied cost of one
	// embedding API call. Multiplied by hits to compute "$$ saved" in
	// EMB.STATS so dashboards can surface a real number.
	costPerCall atomic.Uint64 // float64 bits
}

type embEntry struct {
	vector    []float32
	storedAt  time.Time
	expireAt  time.Time // zero = no TTL
	hits      int64
}

// NewEmbCache returns an empty cache.
func NewEmbCache() *EmbCache {
	return &EmbCache{entries: map[string]*embEntry{}}
}

// canon normalizes the input so equivalent prompts collide on the same
// cache slot. We trim and lowercase — same heuristic used by SEMANTIC_*.
// More aggressive normalization (stemming, etc.) belongs at a higher
// layer; this one is meant to be stable + obvious.
func canon(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}

// hashKey returns the sha256-hex of the canonicalized text. Length is
// fixed so the in-memory map keys stay compact regardless of input.
func hashKey(text string) string {
	sum := sha256.Sum256([]byte(canon(text)))
	return hex.EncodeToString(sum[:])
}

// Set stores vector under text. ttl=0 means no expiry. Returns the hash
// key for callers that want to record it (e.g. for audit logs).
func (c *EmbCache) Set(text string, vector []float32, ttl time.Duration) string {
	if len(vector) == 0 {
		return ""
	}
	key := hashKey(text)
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	cp := make([]float32, len(vector))
	copy(cp, vector)
	c.mu.Lock()
	c.entries[key] = &embEntry{vector: cp, storedAt: time.Now(), expireAt: exp}
	c.mu.Unlock()
	return key
}

// Get returns (vector, true) on hit, (nil, false) on miss. Expired
// entries count as misses and are evicted on read.
func (c *EmbCache) Get(text string) ([]float32, bool) {
	key := hashKey(text)
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	if !e.expireAt.IsZero() && time.Now().After(e.expireAt) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	atomic.AddInt64(&e.hits, 1)
	out := make([]float32, len(e.vector))
	copy(out, e.vector)
	return out, true
}

// Delete drops a single entry. Returns true if it was present.
func (c *EmbCache) Delete(text string) bool {
	key := hashKey(text)
	c.mu.Lock()
	_, ok := c.entries[key]
	delete(c.entries, key)
	c.mu.Unlock()
	return ok
}

// Purge wipes the cache. Returns the count of dropped entries.
func (c *EmbCache) Purge() int {
	c.mu.Lock()
	n := len(c.entries)
	c.entries = map[string]*embEntry{}
	c.mu.Unlock()
	return n
}

// SetCost records the per-call dollar cost used by Stats() to compute
// estimated savings. Stored as float64 bits in an atomic uint64 so reads
// stay lock-free on the hot path.
func (c *EmbCache) SetCost(usd float64) {
	c.costPerCall.Store(float64Bits(usd))
}

// Stats snapshots the current cache state for EMB.STATS / dashboards.
type EmbStats struct {
	Entries     int     `json:"entries"`
	Hits        int64   `json:"hits"`
	Misses      int64   `json:"misses"`
	HitRate     float64 `json:"hit_rate"`
	CostPerCall float64 `json:"cost_per_call_usd"`
	SavedUSD    float64 `json:"saved_usd"`
}

// Stats returns a point-in-time snapshot.
func (c *EmbCache) Stats() EmbStats {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	hits := c.hits.Load()
	misses := c.misses.Load()
	rate := 0.0
	if total := hits + misses; total > 0 {
		rate = float64(hits) / float64(total)
	}
	cost := bitsFloat64(c.costPerCall.Load())
	return EmbStats{
		Entries:     n,
		Hits:        hits,
		Misses:      misses,
		HitRate:     rate,
		CostPerCall: cost,
		SavedUSD:    cost * float64(hits),
	}
}

// HashKey exposes the canonical hash so HTTP / SDK layers can address
// entries directly without reimplementing the normalization.
func (c *EmbCache) HashKey(text string) string { return hashKey(text) }

// SweepExpired drops every entry whose TTL has elapsed. Called from a
// background tick in the engine; cheap when the cache is small.
func (c *EmbCache) SweepExpired() int {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	dropped := 0
	for k, e := range c.entries {
		if !e.expireAt.IsZero() && now.After(e.expireAt) {
			delete(c.entries, k)
			dropped++
		}
	}
	return dropped
}
