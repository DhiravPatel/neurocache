package primitives

import (
	"sync"
	"time"
)

// RateLimiter implements GCRA (Generic Cell Rate Algorithm) — the
// algorithm rate-limiters everywhere converge on once they outgrow
// "increment + EXPIRE" patterns. GCRA gives smooth bursts (up to N
// without delay) and exact recovery rate (one slot per period/N) in
// constant memory per key.
//
// The Allow call returns:
//
//   allowed       — whether the call passed
//   remaining     — slots left in the current burst window
//   retryAfterMs  — milliseconds until the next slot opens (0 when allowed)
//   resetMs       — milliseconds until the bucket fully refills
//
// Storage: one (key → tat) entry per limited key, where tat is the
// "theoretical arrival time". GC happens lazily on Allow when the key
// is fully recovered.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]time.Time // key -> theoretical arrival time
}

// NewRateLimiter returns an empty limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{buckets: map[string]time.Time{}}
}

// Allow checks one event against the bucket configured by (period, max).
// max is the burst capacity; period is the window over which `max`
// events are allowed. Cost == 1 is the typical "one event" call;
// callers can pass higher costs for batched ops.
func (r *RateLimiter) Allow(key string, period time.Duration, max int64, cost int64) (bool, int64, int64, int64) {
	if max <= 0 {
		return false, 0, period.Milliseconds(), period.Milliseconds()
	}
	if cost <= 0 {
		cost = 1
	}
	now := time.Now()
	emissionInterval := time.Duration(int64(period) / max)
	r.mu.Lock()
	defer r.mu.Unlock()
	tat, ok := r.buckets[key]
	if !ok || tat.Before(now) {
		tat = now
	}
	newTat := tat.Add(emissionInterval * time.Duration(cost))
	allowAt := newTat.Add(-period)
	if allowAt.After(now) {
		// rejected — return retry hints
		retry := allowAt.Sub(now).Milliseconds()
		reset := tat.Sub(now).Milliseconds()
		remaining := int64(0)
		return false, remaining, retry, reset
	}
	r.buckets[key] = newTat
	remaining := int64(period-newTat.Sub(now)) / int64(emissionInterval)
	if remaining < 0 {
		remaining = 0
	}
	reset := newTat.Sub(now).Milliseconds()
	return true, remaining, 0, reset
}

// Reset clears any usage on `key`.
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	delete(r.buckets, key)
	r.mu.Unlock()
}
