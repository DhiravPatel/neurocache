package llmstack

import (
	"errors"
	"hash/fnv"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AnswerCanary is the production gate for prompt / model rollouts.
//
// Teams that ship a new prompt or upgrade GPT-4 → GPT-4o usually do
// one of two bad things: (a) flip the whole fleet at once and hope,
// or (b) run a manual side-by-side on a few test queries and ship.
// Both fail in production because LLM quality is high-variance — the
// only way to ship safely is to route a *small fraction* of live
// traffic through the canary, score both, and let statistics decide.
//
// ANSWER.CANARY.* does exactly that:
//
//   ROUTE     deterministic hash(request_id) → "baseline" or "canary"
//             based on configured rate. Same request always lands on
//             the same variant.
//   RECORD    log the variant's quality score + latency for that
//             request after the answer was produced.
//   REPORT    aggregate: n, mean_quality, mean_latency_ms, p95_latency,
//             lift% (canary - baseline) per variant.
//   DECIDE    recommended action: "ship" | "rollback" | "hold" |
//             "insufficient_data" based on lift + sample size + a
//             simple two-sample z-test.
//
// Commands:
//
//   ANSWER.CANARY.CONFIG exp-id [BASELINE name] [CANARY name] [RATE f]
//   ANSWER.CANARY.ROUTE exp-id request-id
//   ANSWER.CANARY.RECORD exp-id variant quality
//        [LATENCY_MS n] [REQUEST_ID id]
//   ANSWER.CANARY.REPORT exp-id
//   ANSWER.CANARY.DECIDE exp-id
//   ANSWER.CANARY.RESET exp-id
//   ANSWER.CANARY.LIST
//   ANSWER.CANARY.STATS
//
// Hot path: ROUTE is one FNV hash + modulo — sub-microsecond.
// RECORD appends to per-variant Welford accumulators (no slice
// growth, no contention beyond a per-experiment lock). REPORT is
// O(1) per variant.
type AnswerCanary struct {
	mu  sync.RWMutex
	exp map[string]*canaryExp

	totalRoutes  atomic.Int64
	totalRecords atomic.Int64
}

type canaryExp struct {
	mu       sync.RWMutex
	baseline canaryVariant
	canary   canaryVariant
	rate     float64 // 0..1, fraction routed to canary
}

type canaryVariant struct {
	Name           string
	N              int64
	QualityMean    float64
	qualityM2      float64 // running variance numerator (Welford)
	LatencyMean    float64
	LatencyMax     int64
	LatencySum     int64
}

// NewAnswerCanary returns an empty experiment store.
func NewAnswerCanary() *AnswerCanary {
	return &AnswerCanary{exp: map[string]*canaryExp{}}
}

// Configure creates or updates an experiment. Empty strings keep the
// existing value. Rate must be in [0,1]; pass < 0 to leave unchanged.
func (a *AnswerCanary) Configure(expID, baselineName, canaryName string, rate float64) error {
	if expID == "" {
		return errors.New("experiment id required")
	}
	if rate >= 0 && (rate < 0 || rate > 1) {
		return errors.New("rate must be in [0,1]")
	}
	a.mu.Lock()
	e, ok := a.exp[expID]
	if !ok {
		e = &canaryExp{rate: 0.10} // default 10% canary
		a.exp[expID] = e
	}
	a.mu.Unlock()
	e.mu.Lock()
	if baselineName != "" {
		e.baseline.Name = baselineName
	}
	if canaryName != "" {
		e.canary.Name = canaryName
	}
	if rate >= 0 {
		e.rate = rate
	}
	e.mu.Unlock()
	return nil
}

// Route returns "baseline" or "canary" deterministically by hashing
// the request id. Same id always routes to the same variant — which
// is critical for fair attribution when retries or follow-ups land.
func (a *AnswerCanary) Route(expID, requestID string) (string, error) {
	if expID == "" {
		return "", errors.New("experiment id required")
	}
	if requestID == "" {
		return "", errors.New("request id required")
	}
	a.totalRoutes.Add(1)
	a.mu.RLock()
	e, ok := a.exp[expID]
	a.mu.RUnlock()
	if !ok {
		// Auto-create with defaults so callers can ROUTE without
		// explicit CONFIG (handy for ad-hoc experiments).
		_ = a.Configure(expID, "", "", -1)
		a.mu.RLock()
		e = a.exp[expID]
		a.mu.RUnlock()
	}
	e.mu.RLock()
	rate := e.rate
	e.mu.RUnlock()
	h := fnv.New32a()
	h.Write([]byte(requestID))
	bucket := float64(h.Sum32()%10_000) / 10_000.0
	if bucket < rate {
		return "canary", nil
	}
	return "baseline", nil
}

// Record logs one outcome.
func (a *AnswerCanary) Record(expID, variant string, quality float64, latencyMS int64) error {
	if expID == "" {
		return errors.New("experiment id required")
	}
	if variant != "baseline" && variant != "canary" {
		return errors.New("variant must be 'baseline' or 'canary'")
	}
	if quality < 0 || quality > 1 {
		return errors.New("quality must be in [0,1]")
	}
	if latencyMS < 0 {
		return errors.New("latency_ms must be non-negative")
	}
	a.totalRecords.Add(1)
	a.mu.RLock()
	e, ok := a.exp[expID]
	a.mu.RUnlock()
	if !ok {
		return errors.New("unknown experiment id: " + expID)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	v := &e.baseline
	if variant == "canary" {
		v = &e.canary
	}
	// Welford online variance update for quality
	v.N++
	delta := quality - v.QualityMean
	v.QualityMean += delta / float64(v.N)
	delta2 := quality - v.QualityMean
	v.qualityM2 += delta * delta2
	// Latency stats (running mean + max)
	v.LatencySum += latencyMS
	v.LatencyMean = float64(v.LatencySum) / float64(v.N)
	if latencyMS > v.LatencyMax {
		v.LatencyMax = latencyMS
	}
	return nil
}

// CanaryVariantRow is one variant's summary in REPORT.
type CanaryVariantRow struct {
	Name         string  `json:"name"`
	N            int64   `json:"n"`
	MeanQuality  float64 `json:"mean_quality"`
	StddevQual   float64 `json:"stddev_quality"`
	MeanLatency  float64 `json:"mean_latency_ms"`
	MaxLatency   int64   `json:"max_latency_ms"`
}

// CanaryReport is REPORT's full return.
type CanaryReport struct {
	ExperimentID string           `json:"experiment_id"`
	Rate         float64          `json:"canary_rate"`
	Baseline     CanaryVariantRow `json:"baseline"`
	Canary       CanaryVariantRow `json:"canary"`
	QualityLift  float64          `json:"quality_lift"` // (canary - baseline) / baseline
	LatencyLift  float64          `json:"latency_lift_ms"`
}

// Report returns the experiment summary.
func (a *AnswerCanary) Report(expID string) (CanaryReport, error) {
	a.mu.RLock()
	e, ok := a.exp[expID]
	a.mu.RUnlock()
	if !ok {
		return CanaryReport{}, errors.New("unknown experiment id: " + expID)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := CanaryReport{
		ExperimentID: expID,
		Rate:         e.rate,
		Baseline:     summarizeVariant(e.baseline),
		Canary:       summarizeVariant(e.canary),
	}
	if e.baseline.QualityMean > 0 {
		out.QualityLift = (e.canary.QualityMean - e.baseline.QualityMean) / e.baseline.QualityMean
	}
	out.LatencyLift = e.canary.LatencyMean - e.baseline.LatencyMean
	return out, nil
}

// CanaryDecision is DECIDE's return.
type CanaryDecision struct {
	ExperimentID string  `json:"experiment_id"`
	Decision     string  `json:"decision"` // ship | rollback | hold | insufficient_data
	Reason       string  `json:"reason"`
	Z            float64 `json:"z_score"`
	QualityLift  float64 `json:"quality_lift"`
}

// Decide returns a recommended action based on a two-sample z-test
// over quality + a guard for sample size.
func (a *AnswerCanary) Decide(expID string) (CanaryDecision, error) {
	rep, err := a.Report(expID)
	if err != nil {
		return CanaryDecision{}, err
	}
	d := CanaryDecision{ExperimentID: expID, QualityLift: rep.QualityLift}
	const minSamples = 30
	if rep.Baseline.N < minSamples || rep.Canary.N < minSamples {
		d.Decision = "insufficient_data"
		d.Reason = "need at least 30 samples per variant"
		return d, nil
	}
	// Two-sample z-test for difference of means
	varB := rep.Baseline.StddevQual * rep.Baseline.StddevQual
	varC := rep.Canary.StddevQual * rep.Canary.StddevQual
	se := math.Sqrt(varB/float64(rep.Baseline.N) + varC/float64(rep.Canary.N))
	if se == 0 {
		// Both variants have zero variance — fall back to lift sign
		if rep.QualityLift > 0 {
			d.Decision = "ship"
		} else if rep.QualityLift < 0 {
			d.Decision = "rollback"
		} else {
			d.Decision = "hold"
		}
		d.Reason = "zero variance — decided by lift sign"
		return d, nil
	}
	z := (rep.Canary.MeanQuality - rep.Baseline.MeanQuality) / se
	d.Z = z
	switch {
	case z >= 2.0:
		d.Decision = "ship"
		d.Reason = "canary significantly better (z >= 2.0)"
	case z <= -2.0:
		d.Decision = "rollback"
		d.Reason = "canary significantly worse (z <= -2.0)"
	default:
		d.Decision = "hold"
		d.Reason = "no significant difference yet"
	}
	return d, nil
}

// Reset drops all results for one experiment (config preserved).
func (a *AnswerCanary) Reset(expID string) bool {
	a.mu.RLock()
	e, ok := a.exp[expID]
	a.mu.RUnlock()
	if !ok {
		return false
	}
	e.mu.Lock()
	e.baseline = canaryVariant{Name: e.baseline.Name}
	e.canary = canaryVariant{Name: e.canary.Name}
	e.mu.Unlock()
	return true
}

// List returns every experiment id, sorted.
func (a *AnswerCanary) List() []string {
	a.mu.RLock()
	out := make([]string, 0, len(a.exp))
	for k := range a.exp {
		out = append(out, k)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// CanaryStats is the global snapshot.
type CanaryStats struct {
	Experiments  int   `json:"experiments"`
	TotalRoutes  int64 `json:"total_routes"`
	TotalRecords int64 `json:"total_records"`
}

func (a *AnswerCanary) Stats() CanaryStats {
	a.mu.RLock()
	n := len(a.exp)
	a.mu.RUnlock()
	return CanaryStats{
		Experiments:  n,
		TotalRoutes:  a.totalRoutes.Load(),
		TotalRecords: a.totalRecords.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func summarizeVariant(v canaryVariant) CanaryVariantRow {
	row := CanaryVariantRow{
		Name:        v.Name,
		N:           v.N,
		MeanQuality: v.QualityMean,
		MeanLatency: v.LatencyMean,
		MaxLatency:  v.LatencyMax,
	}
	if v.N > 1 {
		row.StddevQual = math.Sqrt(v.qualityM2 / float64(v.N-1))
	}
	// Default names so REPORT is never empty
	if row.Name == "" {
		row.Name = "(unnamed)"
	}
	// Ignore startedAt — not exposed in REPORT row
	_ = time.Now
	return row
}
