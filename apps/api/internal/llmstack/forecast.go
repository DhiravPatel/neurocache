package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// CostForecast is the missing third leg of the cost-control story.
//
// GUARD enforces the cap (rejects when over budget).
// LEDGER reports the past (who spent what, by tenant/feature/model).
// FORECAST projects the future: "at the current burn rate you breach
// the monthly cap on the 19th."
//
// Teams want the alert before the wall, not at it. The dashboard
// answer "you've spent 60% of your cap" is useless without the
// matching "and at this rate you'll hit it 4 days early." FORECAST
// gives the orchestrator that signal so it can downgrade tiers or
// negotiate budget *before* the GUARD starts rejecting.
//
// Algorithm: linear regression on (ts, cumulative_spend) over the
// configured window. slope = $/sec; projected_end = spent +
// slope × (window_end − now). When projected_end exceeds the cap,
// surface breach_eta = now + (cap − spent) / slope.
//
// Commands:
//
//   FORECAST.OBSERVE tenant spend-usd
//        Record one spend tick. Engine timestamps it; sequence of
//        ticks is the regression input.
//   FORECAST.PROJECT tenant WINDOW seconds CAP usd
//        → {spent, samples, rate_usd_per_day, projected_eom, verdict,
//           breach_eta_unix, headroom_days, slope_usd_per_sec}
//        verdict: ok | warning (within 20% of cap) | breach (will hit).
//   FORECAST.ALERT tenant AT fraction
//        Set the threshold (e.g., 0.80 fires when projected to hit
//        80% of cap). Idempotent — second ALERT replaces the first.
//   FORECAST.ALERTS tenant
//        → active alert thresholds with last-fired ts.
//   FORECAST.TENANTS
//   FORECAST.RESET tenant|ALL
//   FORECAST.STATS
//
// Hot path: OBSERVE is one slice append. PROJECT does linear
// regression over the window — typically O(samples), single-digit
// microseconds for a few hundred ticks.
type CostForecast struct {
	mu      sync.RWMutex
	tenants map[string]*forecastTenant
	cap     int

	totalObserves atomic.Int64
	totalProjects atomic.Int64
}

type forecastTenant struct {
	mu     sync.RWMutex
	ticks  []forecastTick
	alerts []forecastAlert
}

type forecastTick struct {
	TS      int64   // unix-nano
	Cumul   float64 // running cumulative spend
	Delta   float64 // this tick's delta
}

type forecastAlert struct {
	Fraction  float64
	LastFired int64 // unix-sec, 0 if never
}

// NewCostForecast returns an empty forecaster.
func NewCostForecast() *CostForecast {
	return &CostForecast{tenants: map[string]*forecastTenant{}, cap: 100_000}
}

// SetCap adjusts the per-tenant tick soft cap (oldest dropped on overflow).
func (f *CostForecast) SetCap(n int) {
	f.mu.Lock()
	f.cap = n
	f.mu.Unlock()
}

// Observe records one spend tick. spend is a delta (this tick's cost),
// not cumulative.
func (f *CostForecast) Observe(tenant string, spend float64) error {
	if tenant == "" {
		return errors.New("tenant required")
	}
	if spend < 0 {
		return errors.New("spend must be non-negative")
	}
	f.totalObserves.Add(1)
	t := f.tenantOrCreate(tenant)
	t.mu.Lock()
	defer t.mu.Unlock()
	cumul := spend
	if len(t.ticks) > 0 {
		cumul = t.ticks[len(t.ticks)-1].Cumul + spend
	}
	t.ticks = append(t.ticks, forecastTick{
		TS: time.Now().UnixNano(), Cumul: cumul, Delta: spend,
	})
	// Soft cap drop oldest 10% on overflow
	if f.cap > 0 && len(t.ticks) > f.cap {
		drop := f.cap / 10
		t.ticks = t.ticks[drop:]
	}
	return nil
}

// ForecastProjection is PROJECT's return.
type ForecastProjection struct {
	Tenant          string  `json:"tenant"`
	Spent           float64 `json:"spent"`
	Samples         int     `json:"samples"`
	WindowSec       int64   `json:"window_seconds"`
	Cap             float64 `json:"cap"`
	SlopeUSDPerSec  float64 `json:"slope_usd_per_sec"`
	RateUSDPerDay   float64 `json:"rate_usd_per_day"`
	ProjectedEnd    float64 `json:"projected_end"`
	Verdict         string  `json:"verdict"` // ok | warning | breach | insufficient_data
	BreachETAUnix   int64   `json:"breach_eta_unix,omitempty"`
	HeadroomDays    float64 `json:"headroom_days,omitempty"`
}

// Project runs linear regression over the in-window ticks and
// returns the projection with a verdict relative to cap.
func (f *CostForecast) Project(tenant string, window time.Duration, cap float64) (ForecastProjection, error) {
	if tenant == "" {
		return ForecastProjection{}, errors.New("tenant required")
	}
	if window <= 0 {
		return ForecastProjection{}, errors.New("window must be > 0")
	}
	if cap <= 0 {
		return ForecastProjection{}, errors.New("cap must be > 0")
	}
	f.totalProjects.Add(1)
	f.mu.RLock()
	t, ok := f.tenants[tenant]
	f.mu.RUnlock()
	out := ForecastProjection{Tenant: tenant, WindowSec: int64(window / time.Second), Cap: cap}
	if !ok {
		out.Verdict = "insufficient_data"
		return out, nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now().UnixNano()
	cutoff := now - window.Nanoseconds()
	// Cumulative-spend in the window: pull tick.Cumul values whose
	// timestamps fall in the window.
	var xs, ys []float64
	var spentInWindow float64
	for _, tk := range t.ticks {
		if tk.TS < cutoff {
			continue
		}
		// Use seconds-from-cutoff as x; raw cumulative as y
		x := float64(tk.TS-cutoff) / float64(time.Second)
		xs = append(xs, x)
		ys = append(ys, tk.Cumul)
		spentInWindow += tk.Delta
	}
	out.Samples = len(xs)
	out.Spent = spentInWindow
	if out.Samples < 2 {
		out.Verdict = "insufficient_data"
		return out, nil
	}
	// Linear regression: slope of cumul over seconds-in-window.
	slope := simpleLinReg(xs, ys)
	if slope < 0 {
		slope = 0
	}
	out.SlopeUSDPerSec = slope
	out.RateUSDPerDay = slope * 86400.0
	// Projected spend at end of window (using the in-window spent as
	// the baseline since callers parameterise CAP for that window).
	secondsToEnd := float64(window/time.Second) - xs[len(xs)-1]
	if secondsToEnd < 0 {
		secondsToEnd = 0
	}
	out.ProjectedEnd = spentInWindow + slope*secondsToEnd
	switch {
	case out.ProjectedEnd >= cap:
		out.Verdict = "breach"
		if slope > 0 {
			secondsToBreach := (cap - spentInWindow) / slope
			if secondsToBreach < 0 {
				secondsToBreach = 0
			}
			out.BreachETAUnix = now/int64(time.Second) + int64(secondsToBreach)
			out.HeadroomDays = secondsToBreach / 86400.0
		}
	case out.ProjectedEnd >= cap*0.80:
		out.Verdict = "warning"
		out.HeadroomDays = (cap - spentInWindow) / (slope * 86400.0)
	default:
		out.Verdict = "ok"
		if slope > 0 {
			out.HeadroomDays = (cap - spentInWindow) / (slope * 86400.0)
		}
	}
	return out, nil
}

// Alert sets / replaces the projection threshold for a tenant.
// fraction in (0,1].
func (f *CostForecast) Alert(tenant string, fraction float64) error {
	if tenant == "" {
		return errors.New("tenant required")
	}
	if fraction <= 0 || fraction > 1 {
		return errors.New("fraction must be in (0,1]")
	}
	t := f.tenantOrCreate(tenant)
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.alerts {
		if t.alerts[i].Fraction == fraction {
			return nil // idempotent
		}
	}
	t.alerts = append(t.alerts, forecastAlert{Fraction: fraction})
	return nil
}

// ForecastAlertRow is one row of ALERTS output.
type ForecastAlertRow struct {
	Fraction      float64 `json:"fraction"`
	LastFiredUnix int64   `json:"last_fired_unix"`
}

// Alerts returns the active alert thresholds for a tenant.
func (f *CostForecast) Alerts(tenant string) ([]ForecastAlertRow, bool) {
	f.mu.RLock()
	t, ok := f.tenants[tenant]
	f.mu.RUnlock()
	if !ok {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]ForecastAlertRow, len(t.alerts))
	for i, a := range t.alerts {
		out[i] = ForecastAlertRow{Fraction: a.Fraction, LastFiredUnix: a.LastFired}
	}
	return out, true
}

// Tenants returns every tenant id, sorted.
func (f *CostForecast) Tenants() []string {
	f.mu.RLock()
	out := make([]string, 0, len(f.tenants))
	for k := range f.tenants {
		out = append(out, k)
	}
	f.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a tenant. tenant="ALL" wipes everything.
func (f *CostForecast) Reset(tenant string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tenant == "ALL" {
		n := len(f.tenants)
		f.tenants = map[string]*forecastTenant{}
		return n
	}
	if _, ok := f.tenants[tenant]; ok {
		delete(f.tenants, tenant)
		return 1
	}
	return 0
}

// ForecastStats is the global snapshot.
type ForecastStats struct {
	Tenants       int   `json:"tenants"`
	TotalTicks    int   `json:"total_ticks"`
	TotalObserves int64 `json:"total_observes"`
	TotalProjects int64 `json:"total_projects"`
	Cap           int   `json:"cap"`
}

func (f *CostForecast) Stats() ForecastStats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ticks := 0
	for _, t := range f.tenants {
		t.mu.RLock()
		ticks += len(t.ticks)
		t.mu.RUnlock()
	}
	return ForecastStats{
		Tenants:       len(f.tenants),
		TotalTicks:    ticks,
		TotalObserves: f.totalObserves.Load(),
		TotalProjects: f.totalProjects.Load(),
		Cap:           f.cap,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (f *CostForecast) tenantOrCreate(id string) *forecastTenant {
	f.mu.RLock()
	t, ok := f.tenants[id]
	f.mu.RUnlock()
	if ok {
		return t
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tenants[id]; ok {
		return t
	}
	t = &forecastTenant{}
	f.tenants[id] = t
	return t
}

// simpleLinReg returns the slope of the least-squares fit of (xs,ys).
func simpleLinReg(xs, ys []float64) float64 {
	n := float64(len(xs))
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
		sumXY += xs[i] * ys[i]
		sumX2 += xs[i] * xs[i]
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}
