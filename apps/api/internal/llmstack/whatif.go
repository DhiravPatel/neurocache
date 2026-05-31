package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// WhatIfSimulator predicts the cost / quality / latency of routing a
// query against a chosen model *before* spending a cent. The
// simulator is fed by historical observations (the same numbers
// CONFIDENCE / LEDGER / JUDGE collect for real traffic) and emits a
// projection with confidence bounds so the caller can compare
// candidate routes side-by-side.
//
// The model is intentionally simple — it's a Bayesian-mean-with-prior
// estimator per (route, metric), not a learned regression. A learned
// model would be more accurate but would need its own training/
// retraining infrastructure. The mean-with-prior estimator gives you
// 80% of the value for 1% of the complexity, which is the right
// trade-off for a primitive that lives in the cache layer.
//
// Commands:
//
//   WHATIF.OBSERVE route quality cost_usd latency_ms
//        Append one real observation. WHATIF projections improve as
//        observations accumulate.
//   WHATIF.SIMULATE route [REPEATS n]
//        → projected_quality / projected_cost_usd / projected_p99_ms
//        + sample_n + confidence
//   WHATIF.COMPARE route-a route-b
//        Side-by-side projection: which one is better on quality?
//        on cost? on latency?
//   WHATIF.ROUTES
//   WHATIF.FORGET route|ALL
//   WHATIF.STATS
//
// Hot path: OBSERVE is one atomic running-stats update. SIMULATE is
// a posterior-mean computation; COMPARE runs SIMULATE twice. None
// of this involves a model call — purely arithmetic over telemetry.
type WhatIfSimulator struct {
	mu     sync.RWMutex
	routes map[string]*whatifRoute

	totalObservations atomic.Int64
	totalSimulations  atomic.Int64
}

type whatifRoute struct {
	mu sync.Mutex
	// Running stats (Welford) for online mean + variance
	n              int64
	qMean, qM2     float64
	cMean, cM2     float64
	lMean, lM2     float64
	lSamples       []float64 // for p99 (bounded ring, last 256)
}

const whatifRingMax = 256

// NewWhatIfSimulator returns an empty simulator.
func NewWhatIfSimulator() *WhatIfSimulator {
	return &WhatIfSimulator{routes: map[string]*whatifRoute{}}
}

// Observe records one real call's outcome.
func (w *WhatIfSimulator) Observe(route string, quality, costUSD, latencyMS float64) error {
	if route == "" {
		return errors.New("route required")
	}
	if quality < 0 || quality > 1 {
		return errors.New("quality must be in [0,1]")
	}
	if costUSD < 0 || latencyMS < 0 {
		return errors.New("cost_usd and latency_ms must be non-negative")
	}
	w.totalObservations.Add(1)
	r := w.routeOrCreate(route)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	// Welford's online update for each stream
	welford(&r.qMean, &r.qM2, quality, r.n)
	welford(&r.cMean, &r.cM2, costUSD, r.n)
	welford(&r.lMean, &r.lM2, latencyMS, r.n)
	r.lSamples = append(r.lSamples, latencyMS)
	if len(r.lSamples) > whatifRingMax {
		r.lSamples = r.lSamples[len(r.lSamples)-whatifRingMax:]
	}
	return nil
}

// WhatIfProjection is SIMULATE's structured return.
type WhatIfProjection struct {
	Route             string  `json:"route"`
	SampleN           int64   `json:"sample_n"`
	ProjectedQuality  float64 `json:"projected_quality"`
	QualityCILow      float64 `json:"quality_ci_low"`
	QualityCIHigh     float64 `json:"quality_ci_high"`
	ProjectedCostUSD  float64 `json:"projected_cost_usd"`
	ProjectedP99MS    float64 `json:"projected_p99_ms"`
	ProjectedMeanMS   float64 `json:"projected_mean_ms"`
	Confidence        string  `json:"confidence"` // LOW|MEDIUM|HIGH
}

// Simulate returns the projection for one route. n<=0 means a single
// call; n>0 multiplies cost (linear) but quality/latency are per-call.
func (w *WhatIfSimulator) Simulate(route string, repeats int) (WhatIfProjection, bool) {
	if route == "" {
		return WhatIfProjection{}, false
	}
	if repeats <= 0 {
		repeats = 1
	}
	w.totalSimulations.Add(1)
	w.mu.RLock()
	r, ok := w.routes[route]
	w.mu.RUnlock()
	if !ok {
		return WhatIfProjection{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return WhatIfProjection{Route: route, Confidence: "LOW"}, true
	}
	qVar := variance(r.qM2, r.n)
	out := WhatIfProjection{
		Route: route, SampleN: r.n,
		ProjectedQuality: r.qMean,
		ProjectedCostUSD: r.cMean * float64(repeats),
		ProjectedMeanMS:  r.lMean,
		ProjectedP99MS:   percentileOf100(r.lSamples, 99),
	}
	if r.n >= 2 {
		// Approx 95% CI via normal-approx
		sd := math.Sqrt(qVar / float64(r.n))
		out.QualityCILow = r.qMean - 1.96*sd
		out.QualityCIHigh = r.qMean + 1.96*sd
		if out.QualityCILow < 0 {
			out.QualityCILow = 0
		}
		if out.QualityCIHigh > 1 {
			out.QualityCIHigh = 1
		}
	} else {
		out.QualityCILow = 0
		out.QualityCIHigh = 1
	}
	switch {
	case r.n >= 100:
		out.Confidence = "HIGH"
	case r.n >= 20:
		out.Confidence = "MEDIUM"
	default:
		out.Confidence = "LOW"
	}
	return out, true
}

// WhatIfComparison is COMPARE's return.
type WhatIfComparison struct {
	A             WhatIfProjection `json:"a"`
	B             WhatIfProjection `json:"b"`
	QualityWinner string           `json:"quality_winner"`
	CostWinner    string           `json:"cost_winner"`
	LatencyWinner string           `json:"latency_winner"`
	Recommendation string          `json:"recommendation"`
}

// Compare runs Simulate on both routes and emits a recommendation.
// The recommendation picks the dominant route (better on all 3) if
// one exists; otherwise it reports the trade-off explicitly.
func (w *WhatIfSimulator) Compare(routeA, routeB string) (WhatIfComparison, error) {
	pa, okA := w.Simulate(routeA, 1)
	pb, okB := w.Simulate(routeB, 1)
	if !okA || !okB {
		return WhatIfComparison{}, errors.New("one or both routes unknown")
	}
	out := WhatIfComparison{A: pa, B: pb}
	if pa.ProjectedQuality > pb.ProjectedQuality {
		out.QualityWinner = routeA
	} else if pb.ProjectedQuality > pa.ProjectedQuality {
		out.QualityWinner = routeB
	} else {
		out.QualityWinner = "tie"
	}
	if pa.ProjectedCostUSD < pb.ProjectedCostUSD {
		out.CostWinner = routeA
	} else if pb.ProjectedCostUSD < pa.ProjectedCostUSD {
		out.CostWinner = routeB
	} else {
		out.CostWinner = "tie"
	}
	if pa.ProjectedP99MS < pb.ProjectedP99MS {
		out.LatencyWinner = routeA
	} else if pb.ProjectedP99MS < pa.ProjectedP99MS {
		out.LatencyWinner = routeB
	} else {
		out.LatencyWinner = "tie"
	}
	// Recommendation: route that wins on all three, otherwise call it out
	if out.QualityWinner == out.CostWinner && out.CostWinner == out.LatencyWinner && out.QualityWinner != "tie" {
		out.Recommendation = out.QualityWinner + " dominates on all three axes"
	} else {
		out.Recommendation = "trade-off: pick by which axis matters most for this query"
	}
	return out, nil
}

// Routes returns every known route id.
func (w *WhatIfSimulator) Routes() []string {
	w.mu.RLock()
	out := make([]string, 0, len(w.routes))
	for k := range w.routes {
		out = append(out, k)
	}
	w.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Forget drops a route (or all).
func (w *WhatIfSimulator) Forget(route string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if route == "ALL" {
		n := len(w.routes)
		w.routes = map[string]*whatifRoute{}
		return n
	}
	if _, ok := w.routes[route]; ok {
		delete(w.routes, route)
		return 1
	}
	return 0
}

// WhatIfStats is the global snapshot.
type WhatIfStats struct {
	Routes            int   `json:"routes"`
	TotalObservations int64 `json:"total_observations"`
	TotalSimulations  int64 `json:"total_simulations"`
}

func (w *WhatIfSimulator) Stats() WhatIfStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return WhatIfStats{
		Routes:            len(w.routes),
		TotalObservations: w.totalObservations.Load(),
		TotalSimulations:  w.totalSimulations.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (w *WhatIfSimulator) routeOrCreate(route string) *whatifRoute {
	w.mu.RLock()
	r, ok := w.routes[route]
	w.mu.RUnlock()
	if ok {
		return r
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if r, ok := w.routes[route]; ok {
		return r
	}
	r = &whatifRoute{}
	w.routes[route] = r
	return r
}

func welford(mean, m2 *float64, x float64, n int64) {
	delta := x - *mean
	*mean += delta / float64(n)
	*m2 += delta * (x - *mean)
}

func variance(m2 float64, n int64) float64 {
	if n < 2 {
		return 0
	}
	return m2 / float64(n-1)
}

func percentileOf100(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	cp := make([]float64, len(samples))
	copy(cp, samples)
	sort.Float64s(cp)
	// "Higher" percentile: pick the smallest sample at or above the p-th rank.
	// For n=100, p99 → index 99 (the 100th element), so a single tail value shows up.
	// "Higher" interpolation: a single tail value at p99 is captured
	// (out of 100 samples, p99 = the 100th sorted value, i.e. index 99).
	idx := int(math.Floor(p / 100 * float64(len(cp))))
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return cp[idx]
}
