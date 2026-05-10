package llmstack

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// Coalescer is a single-flight primitive for preventing thundering-
// herd traffic to expensive upstreams. The pain it solves: when 100
// users all ask "what's the latest about X?" within a few seconds,
// every cache miss fires its own upstream LLM call, and you pay 100x
// what one good answer would have cost. Worse, the duplicates often
// fight each other for rate-limited slots and time out.
//
// COALESCE.* gives the cache a "first-caller wins, everyone else
// waits" protocol:
//
//   COALESCE.LOCK key timeout-ms
//      → {owner: 1|0, token: "..."}
//      Atomic: if no entry, create one and return owner=1 + a token.
//      If an entry exists and isn't stale (lockedAt within timeout)
//      and isn't published, return owner=0 — caller should WAIT.
//      If existing entry is stale, claim it (the previous owner
//      probably crashed; let someone else try).
//
//   COALESCE.PUBLISH key token result
//      → 1 if the publish succeeded (token matches the owner), 0
//      otherwise. Wakes every WAIT'er with the result.
//
//   COALESCE.WAIT key timeout-ms
//      → {got: 1|0, result: "..."}
//      Blocks until the key is published or the timeout fires.
//      If the key was already published, returns immediately.
//
//   COALESCE.STATUS key — current state.
//   COALESCE.FORGET key — wipe.
//
// Why this lives in the cache: cross-process coordination needs a
// single source of truth. The cache already serves every replica;
// the lock state should live there too.
//
// Implementation: per-key state with `done chan struct{}` that's
// closed exactly once on publish (or on the lock-timeout sweep).
// WAIT does select{<-done, <-time.After(timeout)}. No polling, no
// busy-loops, scales to thousands of concurrent waiters per key
// because they all share the same channel close.
type Coalescer struct {
	mu      sync.RWMutex
	entries map[string]*coalesceEntry

	totalLocks     atomic.Int64
	totalAcquires  atomic.Int64 // owner=1
	totalContended atomic.Int64 // owner=0
	totalPublishes atomic.Int64
	totalWaits     atomic.Int64
	totalWaitHits  atomic.Int64
	totalWaitTimeo atomic.Int64
}

type coalesceEntry struct {
	owner       string // owner_token
	result      string
	published   atomic.Bool
	lockedAtNS  int64
	publishedNS atomic.Int64
	timeoutMS   int64
	done        chan struct{}
}

// NewCoalescer returns an empty single-flight registry.
func NewCoalescer() *Coalescer {
	return &Coalescer{entries: map[string]*coalesceEntry{}}
}

// LockResult is the COALESCE.LOCK return.
type LockResult struct {
	Owner bool   `json:"owner"`
	Token string `json:"token"`
}

// Lock attempts to claim ownership of `key`. Returns owner=true with
// a fresh token when the caller is the new owner; owner=false when
// another process already owns it (caller should WAIT). Stale locks
// (held longer than their declared timeoutMS without publishing) are
// reclaimable by the next caller — assumes the previous owner died.
func (c *Coalescer) Lock(key string, timeoutMS int64) LockResult {
	c.totalLocks.Add(1)
	if timeoutMS <= 0 {
		timeoutMS = 30_000 // 30s default
	}
	now := time.Now().UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		// Already published — caller should WAIT (which returns immediately).
		if e.published.Load() {
			c.totalContended.Add(1)
			return LockResult{Owner: false, Token: ""}
		}
		// Stale lock — reclaim.
		if now-e.lockedAtNS > e.timeoutMS*int64(time.Millisecond) {
			// Wake any pre-existing waiters with empty so they can retry.
			close(e.done)
			tok := newCoalesceToken()
			c.entries[key] = &coalesceEntry{
				owner:      tok,
				lockedAtNS: now,
				timeoutMS:  timeoutMS,
				done:       make(chan struct{}),
			}
			c.totalAcquires.Add(1)
			return LockResult{Owner: true, Token: tok}
		}
		// Still owned by someone — caller should WAIT.
		c.totalContended.Add(1)
		return LockResult{Owner: false, Token: ""}
	}
	tok := newCoalesceToken()
	c.entries[key] = &coalesceEntry{
		owner:      tok,
		lockedAtNS: now,
		timeoutMS:  timeoutMS,
		done:       make(chan struct{}),
	}
	c.totalAcquires.Add(1)
	return LockResult{Owner: true, Token: tok}
}

// Publish stores the result for the key and wakes every waiter. Only
// the owner (matching token) can publish. Returns true on success.
func (c *Coalescer) Publish(key, token, result string) bool {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	if e.owner != token {
		return false
	}
	if e.published.Load() {
		// Idempotent — if owner accidentally publishes twice, no-op.
		return true
	}
	e.result = result
	e.publishedNS.Store(time.Now().UnixNano())
	e.published.Store(true)
	close(e.done)
	c.totalPublishes.Add(1)
	return true
}

// WaitResult is the COALESCE.WAIT return.
type WaitResult struct {
	Got    bool   `json:"got"`
	Result string `json:"result"`
}

// Wait blocks until the key is published or `timeout` elapses. If the
// key was already published, returns immediately. If the key doesn't
// exist (no one ever LOCKed), returns got=false immediately.
func (c *Coalescer) Wait(key string, timeout time.Duration) WaitResult {
	c.totalWaits.Add(1)
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		c.totalWaitTimeo.Add(1)
		return WaitResult{Got: false}
	}
	if e.published.Load() {
		c.totalWaitHits.Add(1)
		return WaitResult{Got: true, Result: e.result}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	select {
	case <-e.done:
		if e.published.Load() {
			c.totalWaitHits.Add(1)
			return WaitResult{Got: true, Result: e.result}
		}
		// done closed by stale-reclaim — return empty so caller retries
		c.totalWaitTimeo.Add(1)
		return WaitResult{Got: false}
	case <-time.After(timeout):
		c.totalWaitTimeo.Add(1)
		return WaitResult{Got: false}
	}
}

// CoalesceStatus is the per-key snapshot.
type CoalesceStatus struct {
	Key         string `json:"key"`
	State       string `json:"state"` // locked|published|stale
	LockedAt    int64  `json:"locked_at_unix"`
	PublishedAt int64  `json:"published_at_unix"`
	TimeoutMS   int64  `json:"timeout_ms"`
	HasResult   bool   `json:"has_result"`
}

// Status returns the per-key snapshot or false if not present.
func (c *Coalescer) Status(key string) (CoalesceStatus, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return CoalesceStatus{}, false
	}
	state := "locked"
	if e.published.Load() {
		state = "published"
	} else if time.Now().UnixNano()-e.lockedAtNS > e.timeoutMS*int64(time.Millisecond) {
		state = "stale"
	}
	return CoalesceStatus{
		Key:         key,
		State:       state,
		LockedAt:    e.lockedAtNS / int64(time.Second),
		PublishedAt: e.publishedNS.Load() / int64(time.Second),
		TimeoutMS:   e.timeoutMS,
		HasResult:   e.published.Load(),
	}, true
}

// Forget drops the entry. Wakes any pending waiters with empty.
func (c *Coalescer) Forget(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return false
	}
	if !e.published.Load() {
		close(e.done)
	}
	delete(c.entries, key)
	return true
}

// Keys returns every active key, sorted newest-first by lockedAt.
// Useful for debugging stuck herds.
func (c *Coalescer) Keys() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.entries))
	for k := range c.entries {
		out = append(out, k)
	}
	c.mu.RUnlock()
	return out
}

// CoalesceStats is the global counters snapshot.
type CoalesceStats struct {
	Active         int   `json:"active"`
	TotalLocks     int64 `json:"total_locks"`
	TotalAcquires  int64 `json:"total_acquires"`
	TotalContended int64 `json:"total_contended"`
	TotalPublishes int64 `json:"total_publishes"`
	TotalWaits     int64 `json:"total_waits"`
	TotalWaitHits  int64 `json:"total_wait_hits"`
	TotalWaitMisses int64 `json:"total_wait_misses"`
	SaveRate       float64 `json:"save_rate"` // contended / locks — fraction we deduplicated
}

func (c *Coalescer) Stats() CoalesceStats {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	locks := c.totalLocks.Load()
	cont := c.totalContended.Load()
	rate := 0.0
	if locks > 0 {
		rate = float64(cont) / float64(locks)
	}
	return CoalesceStats{
		Active:          n,
		TotalLocks:      locks,
		TotalAcquires:   c.totalAcquires.Load(),
		TotalContended:  cont,
		TotalPublishes:  c.totalPublishes.Load(),
		TotalWaits:      c.totalWaits.Load(),
		TotalWaitHits:   c.totalWaitHits.Load(),
		TotalWaitMisses: c.totalWaitTimeo.Load(),
		SaveRate:        rate,
	}
}

// ─── helpers ───────────────────────────────────────────────────

// newCoalesceToken returns 16 random hex chars (64 bits of entropy).
// Plenty for distinguishing owners at the cache-cluster scale.
func newCoalesceToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
