package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// LLMLimiter is a token-aware sliding-window rate limiter for LLM
// API calls. Standard request-count rate limiters miss the real
// constraint: LLM providers limit on TOKENS per minute, not
// requests. A single 32k-token request can blow a 100k-tokens/min
// budget; counting requests is useless here.
//
// LIMITER.LLM.* exposes the two-phase reserve-then-record pattern
// that handles the "estimated tokens vs actually-spent tokens"
// gap apps run into:
//
//   1. RESERVE — atomic check + reserve before the upstream call.
//      Rejects if budget would be exceeded. Returns allowed +
//      remaining + reset_ms.
//   2. RECORD — after the upstream completes, RECORD the actual
//      token spend. If actual > reserved, the limiter eats the
//      overshoot (your reservation was wrong; the budget is now
//      a bit tighter). If actual < reserved, the difference is
//      returned to the bucket (no wasted capacity).
//
// Commands:
//
//   LIMITER.LLM.CONFIG provider-id tokens-per-min [TENANT t]
//   LIMITER.LLM.RESERVE provider-id tokens [TENANT t]
//        → [allowed, reserved, remaining, reset_ms]
//   LIMITER.LLM.RECORD provider-id actual-tokens [TENANT t]
//        [RESERVED n]
//   LIMITER.LLM.USAGE provider-id [TENANT t]
//   LIMITER.LLM.RESET [provider-id] [TENANT t]
//   LIMITER.LLM.STATS
//
// Storage: per-(provider, tenant) sliding-window with sub-minute
// resolution (10s buckets, 6 buckets = 1-minute window).
// Atomic counter updates so RESERVE is sub-microsecond.
type LLMLimiter struct {
	mu        sync.RWMutex
	limits    map[string]*llmLimitState // key = "provider|tenant"

	totalReserves atomic.Int64
	totalAllowed  atomic.Int64
	totalRejected atomic.Int64
	totalRecords  atomic.Int64
}

type llmLimitState struct {
	provider string
	tenant   string
	capPerMin int64

	mu       sync.Mutex
	buckets  [6]int64 // sliding 10s buckets; total = sum
	bucketAt [6]int64 // unix-second of each bucket's start
}

// NewLLMLimiter returns an empty limiter.
func NewLLMLimiter() *LLMLimiter {
	return &LLMLimiter{limits: map[string]*llmLimitState{}}
}

// Config sets (or updates) the per-minute token cap for a
// (provider, tenant) pair. Empty tenant = global cap.
func (l *LLMLimiter) Config(provider, tenant string, tokensPerMin int64) error {
	if provider == "" {
		return errors.New("provider required")
	}
	if tokensPerMin <= 0 {
		return errors.New("tokens_per_min must be positive")
	}
	key := provider + "|" + tenant
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.limits[key]
	if !ok {
		st = &llmLimitState{provider: provider, tenant: tenant}
		l.limits[key] = st
	}
	st.capPerMin = tokensPerMin
	return nil
}

// ReserveResult is RESERVE's return.
type ReserveResult struct {
	Allowed   bool  `json:"allowed"`
	Reserved  int64 `json:"reserved"`
	Remaining int64 `json:"remaining"`
	ResetMS   int64 `json:"reset_ms"`
}

// Reserve atomically checks and reserves `tokens` against the
// current window's budget. Returns allowed=false if the
// reservation would exceed the cap.
func (l *LLMLimiter) Reserve(provider, tenant string, tokens int64) (ReserveResult, error) {
	if tokens < 0 {
		return ReserveResult{}, errors.New("tokens must be non-negative")
	}
	l.totalReserves.Add(1)
	key := provider + "|" + tenant
	l.mu.RLock()
	st, ok := l.limits[key]
	l.mu.RUnlock()
	if !ok {
		return ReserveResult{}, errors.New("no cap configured for " + provider + "|" + tenant)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now().Unix()
	st.sweepLocked(now)
	used := st.sumLocked()
	if used+tokens > st.capPerMin {
		l.totalRejected.Add(1)
		return ReserveResult{
			Allowed:   false,
			Reserved:  0,
			Remaining: st.capPerMin - used,
			ResetMS:   resetMS(st.bucketAt, now),
		}, nil
	}
	bucket := int(now / 10) % 6
	if st.bucketAt[bucket] != now/10*10 {
		st.bucketAt[bucket] = now / 10 * 10
		st.buckets[bucket] = 0
	}
	st.buckets[bucket] += tokens
	l.totalAllowed.Add(1)
	return ReserveResult{
		Allowed:   true,
		Reserved:  tokens,
		Remaining: st.capPerMin - used - tokens,
		ResetMS:   resetMS(st.bucketAt, now),
	}, nil
}

// Record adjusts the bucket post-call. If actual > reserved, the
// overshoot is added; if actual < reserved, the difference is
// returned (reserved is debited). Pass RESERVED=0 if the caller
// didn't pre-reserve (e.g. fire-and-forget recording).
func (l *LLMLimiter) Record(provider, tenant string, actual, reserved int64) error {
	if actual < 0 || reserved < 0 {
		return errors.New("actual and reserved must be non-negative")
	}
	l.totalRecords.Add(1)
	key := provider + "|" + tenant
	l.mu.RLock()
	st, ok := l.limits[key]
	l.mu.RUnlock()
	if !ok {
		return errors.New("no cap configured for " + provider + "|" + tenant)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now().Unix()
	st.sweepLocked(now)
	delta := actual - reserved
	if delta == 0 {
		return nil
	}
	bucket := int(now / 10) % 6
	if st.bucketAt[bucket] != now/10*10 {
		st.bucketAt[bucket] = now / 10 * 10
		st.buckets[bucket] = 0
	}
	st.buckets[bucket] += delta
	if st.buckets[bucket] < 0 {
		// Don't let the current bucket go negative; clamp.
		st.buckets[bucket] = 0
	}
	return nil
}

// UsageRow is USAGE's return.
type UsageRow struct {
	Provider  string `json:"provider"`
	Tenant    string `json:"tenant,omitempty"`
	CapPerMin int64  `json:"cap_per_min"`
	Used      int64  `json:"used"`
	Remaining int64  `json:"remaining"`
	ResetMS   int64  `json:"reset_ms"`
}

// Usage returns the current state for (provider, tenant), or false.
func (l *LLMLimiter) Usage(provider, tenant string) (UsageRow, bool) {
	key := provider + "|" + tenant
	l.mu.RLock()
	st, ok := l.limits[key]
	l.mu.RUnlock()
	if !ok {
		return UsageRow{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now().Unix()
	st.sweepLocked(now)
	used := st.sumLocked()
	return UsageRow{
		Provider:  provider,
		Tenant:    tenant,
		CapPerMin: st.capPerMin,
		Used:      used,
		Remaining: st.capPerMin - used,
		ResetMS:   resetMS(st.bucketAt, now),
	}, true
}

// Reset zeroes the buckets. Empty provider = wipe everything.
func (l *LLMLimiter) Reset(provider, tenant string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if provider == "" {
		n := len(l.limits)
		l.limits = map[string]*llmLimitState{}
		return n
	}
	key := provider + "|" + tenant
	st, ok := l.limits[key]
	if !ok {
		return 0
	}
	st.mu.Lock()
	for i := range st.buckets {
		st.buckets[i] = 0
		st.bucketAt[i] = 0
	}
	st.mu.Unlock()
	return 1
}

// All returns every configured (provider, tenant), sorted.
func (l *LLMLimiter) All() []UsageRow {
	now := time.Now().Unix()
	l.mu.RLock()
	out := make([]UsageRow, 0, len(l.limits))
	for _, st := range l.limits {
		st.mu.Lock()
		st.sweepLocked(now)
		used := st.sumLocked()
		out = append(out, UsageRow{
			Provider:  st.provider,
			Tenant:    st.tenant,
			CapPerMin: st.capPerMin,
			Used:      used,
			Remaining: st.capPerMin - used,
			ResetMS:   resetMS(st.bucketAt, now),
		})
		st.mu.Unlock()
	}
	l.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Tenant < out[j].Tenant
	})
	return out
}

// LLMLimiterStats is the global snapshot.
type LLMLimiterStats struct {
	ConfiguredKeys int   `json:"configured_keys"`
	TotalReserves  int64 `json:"total_reserves"`
	TotalAllowed   int64 `json:"total_allowed"`
	TotalRejected  int64 `json:"total_rejected"`
	TotalRecords   int64 `json:"total_records"`
}

func (l *LLMLimiter) Stats() LLMLimiterStats {
	l.mu.RLock()
	n := len(l.limits)
	l.mu.RUnlock()
	return LLMLimiterStats{
		ConfiguredKeys: n,
		TotalReserves:  l.totalReserves.Load(),
		TotalAllowed:   l.totalAllowed.Load(),
		TotalRejected:  l.totalRejected.Load(),
		TotalRecords:   l.totalRecords.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

// sweepLocked drops buckets older than 60 seconds.
func (st *llmLimitState) sweepLocked(now int64) {
	cutoff := now - 60
	for i := range st.bucketAt {
		if st.bucketAt[i] > 0 && st.bucketAt[i] <= cutoff {
			st.bucketAt[i] = 0
			st.buckets[i] = 0
		}
	}
}

func (st *llmLimitState) sumLocked() int64 {
	var s int64
	for _, b := range st.buckets {
		s += b
	}
	return s
}

// resetMS returns ms until the oldest bucket expires.
func resetMS(bucketAt [6]int64, now int64) int64 {
	oldest := int64(0)
	for _, t := range bucketAt {
		if t > 0 && (oldest == 0 || t < oldest) {
			oldest = t
		}
	}
	if oldest == 0 {
		return 0
	}
	expire := oldest + 60 - now
	if expire < 0 {
		return 0
	}
	return expire * 1000
}
