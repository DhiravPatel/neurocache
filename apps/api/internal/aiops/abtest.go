package aiops

import (
	"hash/fnv"
	"math"
	"sort"
	"sync"
	"time"
)

// Experiments runs sticky A/B/n assignment with outcome tracking.
// Replaces a category of feature-flag SaaS — every YC startup needs
// this and it's a 200-line problem, not a $500/mo problem.
//
// Assignment is deterministic per (experiment, user) so a user always
// sees the same variant across reconnects, server restarts, and
// failovers. Hashing distributes users uniformly across variants.
type Experiments struct {
	mu     sync.RWMutex
	expts  map[string]*experiment
}

type experiment struct {
	variants []string                 // ordered list, same as configured
	weights  []float64                // sums to 1.0
	outcomes map[string]*outcomeBucket // variant → counters
	createdAt time.Time
}

type outcomeBucket struct {
	exposures int64
	wins      int64
	totalVal  float64 // sum of recorded outcome values (e.g. revenue)
}

// NewExperiments returns an empty manager.
func NewExperiments() *Experiments {
	return &Experiments{expts: map[string]*experiment{}}
}

// Define declares an experiment with named variants and equal weights
// (most common case). Replaces an existing definition; outcomes are
// preserved across re-defines unless variant names change.
func (m *Experiments) Define(name string, variants []string) {
	m.DefineWeighted(name, variants, nil)
}

// DefineWeighted is Define with explicit weights. Weights need not sum
// to 1 — we normalize. Negative weights are clamped to 0.
func (m *Experiments) DefineWeighted(name string, variants []string, weights []float64) {
	if len(variants) == 0 {
		return
	}
	if len(weights) != len(variants) {
		weights = make([]float64, len(variants))
		for i := range weights {
			weights[i] = 1
		}
	}
	sum := 0.0
	for i, w := range weights {
		if w < 0 {
			weights[i] = 0
		}
		sum += weights[i]
	}
	if sum == 0 {
		// fall back to uniform when caller passed all zeros
		for i := range weights {
			weights[i] = 1
		}
		sum = float64(len(weights))
	}
	for i := range weights {
		weights[i] /= sum
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.expts[name]
	if !ok {
		e = &experiment{
			outcomes:  map[string]*outcomeBucket{},
			createdAt: time.Now(),
		}
		m.expts[name] = e
	}
	e.variants = append([]string{}, variants...)
	e.weights = append([]float64{}, weights...)
	for _, v := range variants {
		if _, ok := e.outcomes[v]; !ok {
			e.outcomes[v] = &outcomeBucket{}
		}
	}
}

// Assign returns the variant for (experiment, user). Sticky: same
// (experiment, user) always returns the same variant for as long as
// the experiment definition exists. Returns "" + false when the
// experiment isn't defined.
func (m *Experiments) Assign(name, user string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.expts[name]
	if !ok || len(e.variants) == 0 {
		return "", false
	}
	// Hash to a deterministic 0..1 number, walk the cumulative
	// weights, return the variant whose bucket contains the number.
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(user))
	bucket := float64(h.Sum64()) / float64(math.MaxUint64)
	cum := 0.0
	for i, w := range e.weights {
		cum += w
		if bucket <= cum {
			return e.variants[i], true
		}
	}
	return e.variants[len(e.variants)-1], true
}

// Expose records that we showed `variant` to a user (incrementing
// the exposure counter — used as the denominator for win-rate).
func (m *Experiments) Expose(name, variant string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.expts[name]
	if !ok {
		return
	}
	b, ok := e.outcomes[variant]
	if !ok {
		return
	}
	b.exposures++
}

// Record bumps the win counter and adds value to the running total
// (e.g. revenue, latency-saved-ms, conversion=1). Variants that aren't
// part of the experiment are ignored silently.
func (m *Experiments) Record(name, variant string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.expts[name]
	if !ok {
		return
	}
	b, ok := e.outcomes[variant]
	if !ok {
		return
	}
	b.wins++
	b.totalVal += value
}

// VariantStats is one variant's outcome summary.
type VariantStats struct {
	Variant   string  `json:"variant"`
	Exposures int64   `json:"exposures"`
	Wins      int64   `json:"wins"`
	WinRate   float64 `json:"win_rate"`
	TotalValue float64 `json:"total_value"`
	AvgValue  float64 `json:"avg_value"`
}

// ExperimentStats is the full experiment snapshot.
type ExperimentStats struct {
	Name      string         `json:"name"`
	Variants  []VariantStats `json:"variants"`
	Winner    string         `json:"winner,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Stats snapshots an experiment with computed win-rate per variant
// and the leader (highest win rate, with at least 30 exposures —
// arbitrary threshold to avoid declaring a winner from noise).
func (m *Experiments) Stats(name string) (ExperimentStats, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.expts[name]
	if !ok {
		return ExperimentStats{}, false
	}
	out := ExperimentStats{Name: name, CreatedAt: e.createdAt}
	for _, v := range e.variants {
		b := e.outcomes[v]
		stats := VariantStats{Variant: v, Exposures: b.exposures, Wins: b.wins, TotalValue: b.totalVal}
		if b.exposures > 0 {
			stats.WinRate = float64(b.wins) / float64(b.exposures)
		}
		if b.wins > 0 {
			stats.AvgValue = b.totalVal / float64(b.wins)
		}
		out.Variants = append(out.Variants, stats)
	}
	// Sort variants for stable output; pick the leader.
	sort.SliceStable(out.Variants, func(i, j int) bool {
		return out.Variants[i].WinRate > out.Variants[j].WinRate
	})
	if len(out.Variants) > 0 && out.Variants[0].Exposures >= 30 {
		out.Winner = out.Variants[0].Variant
	}
	return out, true
}

// List returns every experiment name (sorted is the caller's job).
func (m *Experiments) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.expts))
	for k := range m.expts {
		out = append(out, k)
	}
	return out
}

// Reset zeroes the outcome counters for an experiment without
// dropping the variant configuration.
func (m *Experiments) Reset(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.expts[name]
	if !ok {
		return false
	}
	for _, b := range e.outcomes {
		b.exposures = 0
		b.wins = 0
		b.totalVal = 0
	}
	return true
}

// Delete drops an experiment entirely.
func (m *Experiments) Delete(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.expts[name]
	delete(m.expts, name)
	return ok
}
