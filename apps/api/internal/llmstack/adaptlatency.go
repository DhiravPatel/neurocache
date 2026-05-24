package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AdaptLatency is the latency-driven counterpart to CASCADE.
//
// CASCADE picks a model tier by *input difficulty* — small / cheap
// for easy questions, big / expensive for hard ones. It is blind to
// what's happening upstream right now. During a traffic spike the
// expensive tier's p99 climbs past the SLO and nothing in CASCADE
// reacts; teams hand-roll a circuit-breaker on top.
//
// ADAPT.LATENCY.* is that lever as a primitive: every call OBSERVE's
// the model's observed latency; PICK returns the most expensive
// model in the configured cascade whose current p99 still fits
// under the SLO. When the expensive tier breaches, PICK silently
// falls back to the next-cheaper option until p99 recovers.
//
// Commands:
//
//   ADAPT.LATENCY.CONFIG policy-id
//        [TARGETS model:cost,...] [WINDOW seconds] [MIN_SAMPLES n]
//        Defaults: WINDOW=60s, MIN_SAMPLES=20.
//   ADAPT.LATENCY.OBSERVE policy-id model latency_ms
//        Record one latency tick. Rolling window per model.
//   ADAPT.LATENCY.PICK policy-id TARGET_P99_MS n
//        → {model, reason, p99, cost}
//        Picks the most expensive model whose p99 < target. Falls
//        back to the cheapest configured model if none qualify.
//   ADAPT.LATENCY.STATUS policy-id
//        → per-model {samples, p50, p95, p99} rows.
//   ADAPT.LATENCY.LIST
//   ADAPT.LATENCY.RESET policy-id|ALL
//   ADAPT.LATENCY.STATS
//
// Hot path: OBSERVE is one slice append under a per-policy lock.
// PICK is O(samples × models) but typically <50 samples per model
// at the default window — single-digit microseconds.
type AdaptLatency struct {
	mu       sync.RWMutex
	policies map[string]*adaptPolicy

	totalObserves atomic.Int64
	totalPicks    atomic.Int64
	totalDemotes  atomic.Int64
}

type adaptPolicy struct {
	mu      sync.RWMutex
	models  []adaptModel // priority desc by cost
	samples map[string][]adaptSample
	window  time.Duration
	minN    int
}

type adaptModel struct {
	Tag  string
	Cost float64
}

type adaptSample struct {
	TS  int64
	MS  int64
}

// NewAdaptLatency returns an empty policy store.
func NewAdaptLatency() *AdaptLatency {
	return &AdaptLatency{policies: map[string]*adaptPolicy{}}
}

// Configure creates / updates a policy. Empty targets keeps prior;
// non-positive window/minN keeps prior.
func (a *AdaptLatency) Configure(policyID string, targets []adaptModel, window time.Duration, minN int) error {
	if policyID == "" {
		return errors.New("policy_id required")
	}
	if window < 0 {
		return errors.New("window must be non-negative")
	}
	if minN < 0 {
		return errors.New("min_samples must be non-negative")
	}
	p := a.policyOrCreate(policyID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(targets) > 0 {
		// Sort cost desc so PICK walks expensive→cheap
		p.models = make([]adaptModel, len(targets))
		copy(p.models, targets)
		sort.Slice(p.models, func(i, j int) bool { return p.models[i].Cost > p.models[j].Cost })
	}
	if window > 0 {
		p.window = window
	}
	if minN > 0 {
		p.minN = minN
	}
	return nil
}

// Observe records one latency sample for a (policy, model) pair.
// The model does not need to be in the configured cascade — apps
// often record for diagnostic models that aren't currently picked.
func (a *AdaptLatency) Observe(policyID, model string, latencyMS int64) error {
	if policyID == "" {
		return errors.New("policy_id required")
	}
	if model == "" {
		return errors.New("model required")
	}
	if latencyMS < 0 {
		return errors.New("latency_ms must be non-negative")
	}
	a.totalObserves.Add(1)
	p := a.policyOrCreate(policyID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.samples == nil {
		p.samples = map[string][]adaptSample{}
	}
	p.samples[model] = append(p.samples[model], adaptSample{
		TS: time.Now().UnixNano(), MS: latencyMS,
	})
	// Soft cap per model: keep last 2000
	if len(p.samples[model]) > 2000 {
		p.samples[model] = p.samples[model][len(p.samples[model])-2000:]
	}
	return nil
}

// AdaptPick is PICK's return.
type AdaptPick struct {
	PolicyID    string  `json:"policy_id"`
	Model       string  `json:"model"`
	Cost        float64 `json:"cost"`
	P99MS       float64 `json:"p99_ms"`
	Samples     int     `json:"samples"`
	TargetMS    int64   `json:"target_p99_ms"`
	Reason      string  `json:"reason"`
	Demoted     bool    `json:"demoted"` // true if not the most expensive model
}

// Pick returns the most expensive model whose current p99 fits the
// target. Falls back to the cheapest configured model if none qualify
// — "best-effort" beats "no answer" under load. When a configured
// model has fewer than MIN_SAMPLES in-window, it's optimistically
// assumed to meet the SLO (we don't have evidence it doesn't).
func (a *AdaptLatency) Pick(policyID string, targetMS int64) (AdaptPick, error) {
	if policyID == "" {
		return AdaptPick{}, errors.New("policy_id required")
	}
	if targetMS <= 0 {
		return AdaptPick{}, errors.New("target_p99_ms must be positive")
	}
	a.totalPicks.Add(1)
	a.mu.RLock()
	p, ok := a.policies[policyID]
	a.mu.RUnlock()
	if !ok {
		return AdaptPick{}, errors.New("unknown policy_id (call ADAPT.LATENCY.CONFIG first): " + policyID)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.models) == 0 {
		return AdaptPick{}, errors.New("policy has no configured TARGETS")
	}
	cutoff := time.Now().UnixNano() - p.window.Nanoseconds()
	out := AdaptPick{PolicyID: policyID, TargetMS: targetMS}
	for idx, m := range p.models {
		latencies := windowed(p.samples[m.Tag], cutoff)
		out.Model = m.Tag
		out.Cost = m.Cost
		out.Samples = len(latencies)
		if len(latencies) < p.minN {
			// Optimistic: not enough data to know it breaches
			out.P99MS = 0
			out.Reason = "insufficient samples — optimistic pick"
			out.Demoted = idx > 0
			if out.Demoted {
				a.totalDemotes.Add(1)
			}
			return out, nil
		}
		p99 := percentileOf(latencies, 0.99)
		out.P99MS = p99
		if int64(p99) < targetMS {
			out.Reason = "p99 within target"
			out.Demoted = idx > 0
			if out.Demoted {
				a.totalDemotes.Add(1)
			}
			return out, nil
		}
	}
	// No model meets the SLO — fall back to the cheapest
	cheapest := p.models[len(p.models)-1]
	out.Model = cheapest.Tag
	out.Cost = cheapest.Cost
	out.Samples = len(windowed(p.samples[cheapest.Tag], cutoff))
	out.P99MS = percentileOf(windowed(p.samples[cheapest.Tag], cutoff), 0.99)
	out.Reason = "all tiers breach SLO — fell back to cheapest (best-effort)"
	out.Demoted = true
	a.totalDemotes.Add(1)
	return out, nil
}

// AdaptStatusRow is one per-model row in STATUS.
type AdaptStatusRow struct {
	Model   string  `json:"model"`
	Cost    float64 `json:"cost"`
	Samples int     `json:"samples"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
}

// Status returns per-model latency snapshot.
func (a *AdaptLatency) Status(policyID string) ([]AdaptStatusRow, bool) {
	a.mu.RLock()
	p, ok := a.policies[policyID]
	a.mu.RUnlock()
	if !ok {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	cutoff := time.Now().UnixNano() - p.window.Nanoseconds()
	out := make([]AdaptStatusRow, 0, len(p.models))
	for _, m := range p.models {
		lats := windowed(p.samples[m.Tag], cutoff)
		row := AdaptStatusRow{Model: m.Tag, Cost: m.Cost, Samples: len(lats)}
		if len(lats) > 0 {
			row.P50 = percentileOf(lats, 0.50)
			row.P95 = percentileOf(lats, 0.95)
			row.P99 = percentileOf(lats, 0.99)
		}
		out = append(out, row)
	}
	return out, true
}

// List returns every policy id, sorted.
func (a *AdaptLatency) List() []string {
	a.mu.RLock()
	out := make([]string, 0, len(a.policies))
	for k := range a.policies {
		out = append(out, k)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a policy. policyID="ALL" wipes all.
func (a *AdaptLatency) Reset(policyID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if policyID == "ALL" {
		n := len(a.policies)
		a.policies = map[string]*adaptPolicy{}
		return n
	}
	if _, ok := a.policies[policyID]; ok {
		delete(a.policies, policyID)
		return 1
	}
	return 0
}

// AdaptLatencyStats is the global snapshot.
type AdaptLatencyStats struct {
	Policies      int   `json:"policies"`
	TotalObserves int64 `json:"total_observes"`
	TotalPicks    int64 `json:"total_picks"`
	TotalDemotes  int64 `json:"total_demotes"`
}

func (a *AdaptLatency) Stats() AdaptLatencyStats {
	a.mu.RLock()
	n := len(a.policies)
	a.mu.RUnlock()
	return AdaptLatencyStats{
		Policies:      n,
		TotalObserves: a.totalObserves.Load(),
		TotalPicks:    a.totalPicks.Load(),
		TotalDemotes:  a.totalDemotes.Load(),
	}
}

// AdaptLatencyTarget is the public model:cost pair the resp handler
// builds from the TARGETS argument.
type AdaptLatencyTarget struct {
	Model string
	Cost  float64
}

// ConfigurePublic is the variant exposed to the resp layer.
func (a *AdaptLatency) ConfigurePublic(policyID string, targets []AdaptLatencyTarget, window time.Duration, minN int) error {
	mods := make([]adaptModel, len(targets))
	for i, t := range targets {
		mods[i] = adaptModel{Tag: t.Model, Cost: t.Cost}
	}
	return a.Configure(policyID, mods, window, minN)
}

// ─── internals ──────────────────────────────────────────────────

func (a *AdaptLatency) policyOrCreate(id string) *adaptPolicy {
	a.mu.RLock()
	p, ok := a.policies[id]
	a.mu.RUnlock()
	if ok {
		return p
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.policies[id]; ok {
		return p
	}
	p = &adaptPolicy{
		samples: map[string][]adaptSample{},
		window:  60 * time.Second,
		minN:    20,
	}
	a.policies[id] = p
	return p
}

// windowed returns latencies in [cutoff, now] as a []float64.
func windowed(samples []adaptSample, cutoff int64) []float64 {
	out := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.TS >= cutoff {
			out = append(out, float64(s.MS))
		}
	}
	return out
}

// percentileOf returns the p-quantile of an unsorted slice.
func percentileOf(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	sort.Float64s(cp)
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	idx := int(p * float64(len(cp)-1))
	return cp[idx]
}
