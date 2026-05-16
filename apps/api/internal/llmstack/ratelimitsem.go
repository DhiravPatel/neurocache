package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SemRateLimiter is a *semantic* rate limiter for LLM workloads.
//
// Classical rate limits (N requests per minute per tenant) miss the
// real abuse pattern in LLM apps: the same expensive question phrased
// 8 different ways. Per-key idempotency caches help when the question
// is *exactly* repeated but a determined caller can defeat them with
// a comma. SEM rate-limits in embedding space:
//
//   "if there are already MAX similar requests (cosine ≥ THRESHOLD)
//    in the last WINDOW from this tenant, deny."
//
// Defaults: max=5 similar, threshold=0.85, window=60s. Tunable per
// tenant. Useful for: expensive-tool guard, free-tier paraphrase abuse,
// chat-bombing detection.
//
// Commands:
//
//   RATELIMIT.SEM.CHECK tenant text
//        Check and record on allow. → {allow, reason, similar_count}
//   RATELIMIT.SEM.PEEK tenant text
//        Check WITHOUT recording. Same return shape.
//   RATELIMIT.SEM.CONFIG tenant
//        [LIMIT n] [THRESHOLD f] [WINDOW seconds]
//   RATELIMIT.SEM.STATUS tenant
//   RATELIMIT.SEM.RESET tenant
//   RATELIMIT.SEM.LIST
//   RATELIMIT.SEM.RECENT tenant   → last requests in window
//   RATELIMIT.SEM.STATS
//
// Hot path: CHECK scans the in-window recent buffer (typically 5-50
// entries) and does one dot product per entry. ~3 µs on 128-dim,
// happily 100k checks/sec on a single core. Old entries are evicted
// lazily on the next read.
type SemRateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*semBucket
	cfgDef  semConfig

	totalChecks  atomic.Int64
	totalAllowed atomic.Int64
	totalDenied  atomic.Int64
	totalPeeks   atomic.Int64
}

type semBucket struct {
	mu      sync.RWMutex
	cfg     semConfig
	recent  []semRequest // newest last; trimmed lazily on read/write
}

type semRequest struct {
	Text string
	Vec  []float64
	TS   int64
}

type semConfig struct {
	Limit     int
	Threshold float64
	Window    time.Duration
}

// NewSemRateLimiter returns a limiter with sensible defaults.
func NewSemRateLimiter() *SemRateLimiter {
	return &SemRateLimiter{
		buckets: map[string]*semBucket{},
		cfgDef: semConfig{
			Limit:     5,
			Threshold: 0.85,
			Window:    60 * time.Second,
		},
	}
}

// SemRateResult is CHECK / PEEK output.
type SemRateResult struct {
	Allow        bool    `json:"allow"`
	Reason       string  `json:"reason"`         // "ok" | "rate_limit_exceeded" | "no_history"
	SimilarCount int     `json:"similar_count"`  // matches against cfg.Threshold in window
	TopCosine    float64 `json:"top_cosine"`     // highest cosine to a prior request
	Limit        int     `json:"limit"`
	WindowSec    int64   `json:"window_seconds"`
}

// Check performs the decision and records on allow.
func (r *SemRateLimiter) Check(tenant, text string) (SemRateResult, error) {
	if tenant == "" {
		return SemRateResult{}, errors.New("tenant required")
	}
	r.totalChecks.Add(1)
	bucket := r.getOrCreateBucket(tenant)
	vec := embedFallback(text)
	res := r.decide(bucket, vec)
	if res.Allow {
		bucket.mu.Lock()
		bucket.recent = append(bucket.recent, semRequest{
			Text: text, Vec: vec, TS: time.Now().UnixNano(),
		})
		bucket.mu.Unlock()
		r.totalAllowed.Add(1)
	} else {
		r.totalDenied.Add(1)
	}
	return res, nil
}

// Peek runs the check without recording.
func (r *SemRateLimiter) Peek(tenant, text string) (SemRateResult, error) {
	if tenant == "" {
		return SemRateResult{}, errors.New("tenant required")
	}
	r.totalPeeks.Add(1)
	bucket := r.getOrCreateBucket(tenant)
	vec := embedFallback(text)
	return r.decide(bucket, vec), nil
}

// Configure updates per-tenant limits. Zero values keep the existing
// (or default) setting for that field.
func (r *SemRateLimiter) Configure(tenant string, limit int, threshold float64, window time.Duration) error {
	if tenant == "" {
		return errors.New("tenant required")
	}
	if threshold < 0 || threshold > 1 {
		return errors.New("threshold must be in [0,1]")
	}
	if limit < 0 {
		return errors.New("limit must be non-negative")
	}
	if window < 0 {
		return errors.New("window must be non-negative")
	}
	b := r.getOrCreateBucket(tenant)
	b.mu.Lock()
	if limit > 0 {
		b.cfg.Limit = limit
	}
	if threshold > 0 {
		b.cfg.Threshold = threshold
	}
	if window > 0 {
		b.cfg.Window = window
	}
	b.mu.Unlock()
	return nil
}

// SemRateStatus is per-tenant snapshot.
type SemRateStatus struct {
	Tenant       string  `json:"tenant"`
	BucketSize   int     `json:"bucket_size"`
	Limit        int     `json:"limit"`
	Threshold    float64 `json:"threshold"`
	WindowSec    int64   `json:"window_seconds"`
}

// Status reports the current per-tenant state. Returns ok=false when
// the tenant has no bucket yet.
func (r *SemRateLimiter) Status(tenant string) (SemRateStatus, bool) {
	r.mu.RLock()
	b, ok := r.buckets[tenant]
	r.mu.RUnlock()
	if !ok {
		return SemRateStatus{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.evictLocked()
	return SemRateStatus{
		Tenant:     tenant,
		BucketSize: len(b.recent),
		Limit:      b.cfg.Limit,
		Threshold:  b.cfg.Threshold,
		WindowSec:  int64(b.cfg.Window / time.Second),
	}, true
}

// Reset drops the bucket for tenant (config is preserved).
func (r *SemRateLimiter) Reset(tenant string) bool {
	r.mu.RLock()
	b, ok := r.buckets[tenant]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	b.mu.Lock()
	b.recent = nil
	b.mu.Unlock()
	return true
}

// List returns every tenant id known to the limiter, sorted.
func (r *SemRateLimiter) List() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.buckets))
	for k := range r.buckets {
		out = append(out, k)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// SemRateRecentRow is one item in RECENT output.
type SemRateRecentRow struct {
	TS   int64  `json:"ts"`
	Text string `json:"text"`
}

// Recent returns in-window requests for a tenant (newest last).
func (r *SemRateLimiter) Recent(tenant string) ([]SemRateRecentRow, bool) {
	r.mu.RLock()
	b, ok := r.buckets[tenant]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	b.mu.Lock()
	b.evictLocked()
	out := make([]SemRateRecentRow, len(b.recent))
	for i, req := range b.recent {
		out[i] = SemRateRecentRow{TS: req.TS / int64(time.Second), Text: req.Text}
	}
	b.mu.Unlock()
	return out, true
}

// SemRateStats is the global snapshot.
type SemRateStats struct {
	Tenants      int   `json:"tenants"`
	TotalChecks  int64 `json:"total_checks"`
	TotalAllowed int64 `json:"total_allowed"`
	TotalDenied  int64 `json:"total_denied"`
	TotalPeeks   int64 `json:"total_peeks"`
}

func (r *SemRateLimiter) Stats() SemRateStats {
	r.mu.RLock()
	n := len(r.buckets)
	r.mu.RUnlock()
	return SemRateStats{
		Tenants:      n,
		TotalChecks:  r.totalChecks.Load(),
		TotalAllowed: r.totalAllowed.Load(),
		TotalDenied:  r.totalDenied.Load(),
		TotalPeeks:   r.totalPeeks.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (r *SemRateLimiter) getOrCreateBucket(tenant string) *semBucket {
	r.mu.RLock()
	b, ok := r.buckets[tenant]
	r.mu.RUnlock()
	if ok {
		return b
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.buckets[tenant]; ok {
		return b
	}
	b = &semBucket{cfg: r.cfgDef}
	r.buckets[tenant] = b
	return b
}

func (r *SemRateLimiter) decide(b *semBucket, vec []float64) SemRateResult {
	b.mu.Lock()
	b.evictLocked()
	cfg := b.cfg
	similar := 0
	top := 0.0
	for _, req := range b.recent {
		cos := dotProduct(vec, req.Vec)
		if cos > top {
			top = cos
		}
		if cos >= cfg.Threshold {
			similar++
		}
	}
	b.mu.Unlock()
	res := SemRateResult{
		SimilarCount: similar,
		TopCosine:    top,
		Limit:        cfg.Limit,
		WindowSec:    int64(cfg.Window / time.Second),
	}
	if similar >= cfg.Limit {
		res.Allow = false
		res.Reason = "rate_limit_exceeded"
	} else {
		res.Allow = true
		res.Reason = "ok"
	}
	return res
}

// evictLocked drops items older than cfg.Window. Caller must hold
// b.mu (write lock; we mutate the slice).
func (b *semBucket) evictLocked() {
	if b.cfg.Window <= 0 || len(b.recent) == 0 {
		return
	}
	cutoff := time.Now().UnixNano() - b.cfg.Window.Nanoseconds()
	// Find first index with ts >= cutoff (slice is append-only so sorted)
	keep := 0
	for keep < len(b.recent) && b.recent[keep].TS < cutoff {
		keep++
	}
	if keep > 0 {
		b.recent = append(b.recent[:0], b.recent[keep:]...)
	}
}
