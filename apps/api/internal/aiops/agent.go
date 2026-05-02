// Package aiops implements the second-tier AI primitives that close the
// gaps every LLM application rebuilds in client code:
//
//   - AGENT.*    — tool-call result caching for AI agents
//   - STREAM.*   — token-stream caching with replay
//   - COST.*     — per-tenant LLM cost budgeting
//   - SHADOW.*   — stale-while-revalidate with a declared backing source
//   - PERSONA.*  — multi-persona memory routing
//   - SAFE.*     — moderation result caching + simple injection detection
//   - LINEAGE.*  — provenance tracking for AI outputs
//   - SLO.*      — per-command SLO tracking with breach signals
//   - AB.*       — sticky experiment assignment + outcome tracking
//   - GRAPH.*    — lightweight knowledge graph (triples + traversal)
//   - SCHEDULE.* — delayed command execution
//   - EVENT.*    — append-only event log with declarative projections
//   - POLICY.*   — RBAC / ABAC verdict caching
//   - INFER.*    — LLM call proxy with caching, retries, cost tracking
//
// Each domain lives in its own .go file in this package, and the engine
// wires one *Manager per domain via internal/engine. RESP handlers live
// in internal/resp/commands_aiops.go; HTTP routes in internal/http/aiops.go.
package aiops

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// AgentToolCache memoizes (tool, args) → result pairs so an agent
// doesn't pay for the same Brave Search 50 times in a session. Keying
// is by canonical hash of (tool, args) — args ordering doesn't matter
// because we sort them before hashing.
//
// Each tool can declare a "determinism profile":
//   - DeterminismAlways : same input → same output forever (most search APIs, math)
//   - DeterminismDay    : same input → same output for 24h (weather, news)
//   - DeterminismNever  : never cache (e.g. anything that mutates state)
type AgentToolCache struct {
	mu    sync.RWMutex
	calls map[string]*agentEntry
	prof  map[string]Determinism

	hits   atomic.Int64
	misses atomic.Int64
}

// Determinism profile for a tool.
type Determinism int

const (
	DeterminismAlways Determinism = iota
	DeterminismDay
	DeterminismNever
)

// String returns the human-readable name for the profile.
func (d Determinism) String() string {
	switch d {
	case DeterminismAlways:
		return "always"
	case DeterminismDay:
		return "day"
	case DeterminismNever:
		return "never"
	}
	return "unknown"
}

type agentEntry struct {
	result   string
	expireAt time.Time // zero = no expiry (DeterminismAlways)
	hits     int64
}

// NewAgentToolCache returns an empty cache.
func NewAgentToolCache() *AgentToolCache {
	return &AgentToolCache{
		calls: map[string]*agentEntry{},
		prof:  map[string]Determinism{},
	}
}

// SetProfile declares the determinism profile for a tool. Future
// AGENT.CALL invocations honour this when deciding TTL and whether to
// cache at all.
func (c *AgentToolCache) SetProfile(tool string, d Determinism) {
	c.mu.Lock()
	c.prof[tool] = d
	c.mu.Unlock()
}

// Profile returns the declared profile, or DeterminismDay as a safe
// default for unknown tools (caches for 24h — agents rarely care about
// finer freshness than that).
func (c *AgentToolCache) Profile(tool string) Determinism {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if d, ok := c.prof[tool]; ok {
		return d
	}
	return DeterminismDay
}

// Get returns the cached result for (tool, argsHash). Misses are
// counted; expired entries count as misses and are evicted on read.
func (c *AgentToolCache) Get(tool, argsHash string) (string, bool) {
	key := agentKey(tool, argsHash)
	c.mu.RLock()
	e, ok := c.calls[key]
	c.mu.RUnlock()
	if !ok {
		c.misses.Add(1)
		return "", false
	}
	if !e.expireAt.IsZero() && time.Now().After(e.expireAt) {
		c.mu.Lock()
		delete(c.calls, key)
		c.mu.Unlock()
		c.misses.Add(1)
		return "", false
	}
	c.hits.Add(1)
	atomic.AddInt64(&e.hits, 1)
	return e.result, true
}

// Set records the result for (tool, argsHash). The profile drives the
// TTL; never-cache returns immediately without storing.
func (c *AgentToolCache) Set(tool, argsHash, result string) {
	prof := c.Profile(tool)
	if prof == DeterminismNever {
		return
	}
	exp := time.Time{}
	if prof == DeterminismDay {
		exp = time.Now().Add(24 * time.Hour)
	}
	c.mu.Lock()
	c.calls[agentKey(tool, argsHash)] = &agentEntry{
		result:   result,
		expireAt: exp,
	}
	c.mu.Unlock()
}

// Forget drops a single cache entry (e.g. when the operator knows the
// underlying tool result has changed).
func (c *AgentToolCache) Forget(tool, argsHash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := agentKey(tool, argsHash)
	_, ok := c.calls[key]
	delete(c.calls, key)
	return ok
}

// Purge wipes the whole cache.
func (c *AgentToolCache) Purge() int {
	c.mu.Lock()
	n := len(c.calls)
	c.calls = map[string]*agentEntry{}
	c.mu.Unlock()
	return n
}

// AgentStats snapshots the cache state.
type AgentStats struct {
	Entries  int     `json:"entries"`
	Profiles int     `json:"profiles"`
	Hits     int64   `json:"hits"`
	Misses   int64   `json:"misses"`
	HitRate  float64 `json:"hit_rate"`
}

// Stats returns a point-in-time snapshot.
func (c *AgentToolCache) Stats() AgentStats {
	c.mu.RLock()
	n := len(c.calls)
	p := len(c.prof)
	c.mu.RUnlock()
	hits := c.hits.Load()
	misses := c.misses.Load()
	rate := 0.0
	if hits+misses > 0 {
		rate = float64(hits) / float64(hits+misses)
	}
	return AgentStats{
		Entries:  n,
		Profiles: p,
		Hits:     hits,
		Misses:   misses,
		HitRate:  rate,
	}
}

// HashArgs canonicalizes args (sorted) and returns a sha256-hex digest.
// Public so callers can compute the hash client-side once and pass it
// in.
func HashArgs(args ...string) string {
	// Single allocation: combine in a deterministic order. Args are
	// already provided in the order the agent wants them — we don't
	// sort because for tools like (q, page=2) the order matters.
	h := sha256.New()
	for _, a := range args {
		_, _ = h.Write([]byte(a))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func agentKey(tool, argsHash string) string {
	return tool + ":" + argsHash
}
