package llmstack

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// CostGuard enforces hard spending caps on LLM API calls. Apps
// configure a cap per scope (per-user, per-session, per-day-global)
// and call Check before every chargeable LLM call. Check returns an
// error when the call would exceed the cap, so the app can short-
// circuit without paying for the upstream API call.
//
// Why this exists: every production LLM app needs a "kill switch"
// for runaway costs. A misbehaving agent loop, a leaked API key, a
// new feature that turned out 100× more expensive than planned —
// you find out via the bill. CostGuard makes the bound a property
// of the cache, not application code that someone might forget to
// add.
//
// Hot path:
//   Check: atomic.Int64.Load (compare against cap) — lock-free.
//   Record: atomic.Int64.Add — lock-free.
//   Reset (called by the rollover goroutine when window expires):
//     atomic.Store under cap-config mutex.
//
// Atomic counters are denominated in micro-USD (1_000_000 = $1.00)
// so we stay in int64 territory regardless of $ amount; converted to
// float at read time only.
type CostGuard struct {
	// caps: name → *capState. New scopes are added under capsMu;
	// hot-path readers do a sync.Map.Load (no lock).
	caps   sync.Map // map[string]*capState
	capsMu sync.Mutex

	// totalChecks / totalRejections: process-wide observability
	totalChecks     atomic.Int64
	totalRejections atomic.Int64
}

// capState is one configured spending cap.
type capState struct {
	scope     string
	limitUSDMicro int64 // hard cap in micro-USD
	windowSec int64        // rolling window in seconds; 0 = no rollover
	spent     atomic.Int64 // current window spend in micro-USD
	startedAt atomic.Int64 // unix-nano of current window start
}

// NewCostGuard returns an empty guard.
func NewCostGuard() *CostGuard { return &CostGuard{} }

// SetCap configures (or updates) the cap for scope. limitUSD is the
// human-friendly $ amount (we store as micro-USD internally).
// windowSec=0 means "lifetime" (no rollover); otherwise the spend
// counter resets when the window elapses.
//
// Updating an existing cap preserves the current `spent` counter so
// in-flight requests continue to be checked against the new limit
// without losing state.
func (g *CostGuard) SetCap(scope string, limitUSD float64, windowSec int64) {
	g.capsMu.Lock()
	defer g.capsMu.Unlock()
	limitMicro := int64(limitUSD * 1_000_000)
	if existing, ok := g.caps.Load(scope); ok {
		c := existing.(*capState)
		atomic.StoreInt64(&c.limitUSDMicro, limitMicro)
		atomic.StoreInt64(&c.windowSec, windowSec)
		return
	}
	c := &capState{
		scope:         scope,
		limitUSDMicro: limitMicro,
		windowSec:     windowSec,
	}
	c.startedAt.Store(time.Now().UnixNano())
	g.caps.Store(scope, c)
}

// ErrCapExceeded is returned by Check / Record when the proposed
// spend would exceed the configured cap.
var ErrCapExceeded = errors.New("CAPEXCEEDED would exceed configured spend cap")

// ErrUnknownScope means no cap is configured for this scope. Callers
// can choose to treat this as "no limit" or as a hard error — we
// surface the distinction so apps can fail-closed.
var ErrUnknownScope = errors.New("UNKNOWNSCOPE no cap configured for scope")

// Check reports whether a proposed `costUSD` charge would fit under
// the cap. Lock-free: just a sync.Map.Load + atomic compare. Does
// NOT record the spend; callers must follow up with Record() once
// the upstream call succeeds. Splitting check + record lets apps
// fail-closed cleanly: if the LLM call errors, you don't bump the
// counter.
func (g *CostGuard) Check(scope string, costUSD float64) error {
	g.totalChecks.Add(1)
	v, ok := g.caps.Load(scope)
	if !ok {
		return ErrUnknownScope
	}
	c := v.(*capState)
	g.maybeRollWindow(c)
	costMicro := int64(costUSD * 1_000_000)
	cur := c.spent.Load()
	if cur+costMicro > atomic.LoadInt64(&c.limitUSDMicro) {
		g.totalRejections.Add(1)
		return ErrCapExceeded
	}
	return nil
}

// Record increments the scope's spend counter atomically. Returns
// the new total in $. Pair with Check for "fail-closed" semantics:
// app calls Check, makes the upstream call, records the spend on
// success. A racy concurrent caller may push the counter slightly
// over the cap (the cap is "soft" under contention) — for the rare
// production case where this matters, use CheckAndRecord which is
// strictly atomic.
func (g *CostGuard) Record(scope string, costUSD float64) (float64, error) {
	v, ok := g.caps.Load(scope)
	if !ok {
		return 0, ErrUnknownScope
	}
	c := v.(*capState)
	g.maybeRollWindow(c)
	newMicro := c.spent.Add(int64(costUSD * 1_000_000))
	return float64(newMicro) / 1_000_000.0, nil
}

// CheckAndRecord is an atomic alternative to Check + Record. Uses a
// CAS loop on the spent counter so two concurrent callers can't both
// pass the cap. Slower than Check (typical 1-2 retries under
// contention) but the right choice when the cap is a hard $ figure
// rather than a soft alert threshold.
func (g *CostGuard) CheckAndRecord(scope string, costUSD float64) (float64, error) {
	g.totalChecks.Add(1)
	v, ok := g.caps.Load(scope)
	if !ok {
		return 0, ErrUnknownScope
	}
	c := v.(*capState)
	g.maybeRollWindow(c)
	costMicro := int64(costUSD * 1_000_000)
	cap := atomic.LoadInt64(&c.limitUSDMicro)
	for {
		cur := c.spent.Load()
		if cur+costMicro > cap {
			g.totalRejections.Add(1)
			return float64(cur) / 1_000_000.0, ErrCapExceeded
		}
		if c.spent.CompareAndSwap(cur, cur+costMicro) {
			return float64(cur+costMicro) / 1_000_000.0, nil
		}
		// CAS lost — another goroutine bumped the counter.
		// Re-check against the cap and retry.
	}
}

// maybeRollWindow resets the spend counter when the rolling window
// expires. Lock-free: a single CAS on startedAt decides which
// goroutine actually resets, others observe the new window.
func (g *CostGuard) maybeRollWindow(c *capState) {
	w := atomic.LoadInt64(&c.windowSec)
	if w <= 0 {
		return // lifetime cap
	}
	now := time.Now().UnixNano()
	started := c.startedAt.Load()
	if now-started < w*int64(time.Second) {
		return
	}
	// Try to claim the rollover. The CAS ensures exactly one goroutine
	// resets the counter even under heavy contention.
	if c.startedAt.CompareAndSwap(started, now) {
		c.spent.Store(0)
	}
}

// Spent returns the current window's spend in $ for scope.
func (g *CostGuard) Spent(scope string) (float64, error) {
	v, ok := g.caps.Load(scope)
	if !ok {
		return 0, ErrUnknownScope
	}
	c := v.(*capState)
	g.maybeRollWindow(c)
	return float64(c.spent.Load()) / 1_000_000.0, nil
}

// Limit returns the configured cap in $ for scope.
func (g *CostGuard) Limit(scope string) (float64, error) {
	v, ok := g.caps.Load(scope)
	if !ok {
		return 0, ErrUnknownScope
	}
	c := v.(*capState)
	return float64(atomic.LoadInt64(&c.limitUSDMicro)) / 1_000_000.0, nil
}

// Reset clears the spend counter for scope (without changing the
// cap or window). Used by ops paths after manual review of an alert.
func (g *CostGuard) Reset(scope string) error {
	v, ok := g.caps.Load(scope)
	if !ok {
		return ErrUnknownScope
	}
	c := v.(*capState)
	c.spent.Store(0)
	c.startedAt.Store(time.Now().UnixNano())
	return nil
}

// CostStatus is one row in COST.LIST output.
type CostStatus struct {
	Scope        string  `json:"scope"`
	LimitUSD     float64 `json:"limit_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	WindowSec    int64   `json:"window_sec"` // 0 = lifetime
	WindowAgeSec int64   `json:"window_age_sec"`
	UtilPercent  float64 `json:"util_percent"`
}

// List returns every configured scope's status. Useful for the
// dashboard's Cost Guard panel.
func (g *CostGuard) List() []CostStatus {
	now := time.Now().UnixNano()
	var out []CostStatus
	g.caps.Range(func(k, v any) bool {
		c := v.(*capState)
		g.maybeRollWindow(c)
		limit := atomic.LoadInt64(&c.limitUSDMicro)
		spent := c.spent.Load()
		util := 0.0
		if limit > 0 {
			util = float64(spent) / float64(limit) * 100
		}
		out = append(out, CostStatus{
			Scope:        k.(string),
			LimitUSD:     float64(limit) / 1_000_000.0,
			SpentUSD:     float64(spent) / 1_000_000.0,
			WindowSec:    atomic.LoadInt64(&c.windowSec),
			WindowAgeSec: (now - c.startedAt.Load()) / int64(time.Second),
			UtilPercent:  util,
		})
		return true
	})
	return out
}

// CostMeta is the global counters snapshot for COST.STATS.
type CostMeta struct {
	TotalChecks     int64 `json:"total_checks"`
	TotalRejections int64 `json:"total_rejections"`
}

func (g *CostGuard) Meta() CostMeta {
	return CostMeta{
		TotalChecks:     g.totalChecks.Load(),
		TotalRejections: g.totalRejections.Load(),
	}
}
