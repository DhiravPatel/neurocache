package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// EntropyMonitor detects population-level mode collapse. STREAM.WATCH
// catches one degenerating stream; ENTROPY catches the case where
// every stream looks fine individually but, across thousands of
// users this week, the outputs have converged to a bland sameness.
//
// Two signals per population:
//
//   - Shannon entropy of the output distribution (token n-grams or
//     full-line frequencies). Healthy diverse output: high entropy
//     (~ log(N) bits for N distinct items). Mode-collapsed: entropy
//     drops sharply as one or two modes dominate.
//
//   - Unique-fraction: # distinct outputs / # total observations.
//     Drops as the population converges.
//
// Together with a sliding rolling window, this gives an early warning
// before users notice that the model started repeating itself.
//
// Commands:
//
//   ENTROPY.OBSERVE pop-id output-text
//        We hash the text (full-line) and tally; rolling window of
//        the last N=10000 observations per pop-id.
//   ENTROPY.REPORT pop-id [TOP n]
//        → shannon_bits, max_possible_bits, unique_fraction,
//        top_modes (top-n most-common outputs)
//   ENTROPY.RESET pop-id|ALL
//   ENTROPY.LIST
//   ENTROPY.STATS
//
// The hot path: OBSERVE is one map lookup + counter bump. REPORT is
// O(N) sort over distinct outputs in the window.
type EntropyMonitor struct {
	mu  sync.RWMutex
	pops map[string]*entropyPop

	totalObserves atomic.Int64
	totalReports  atomic.Int64
}

type entropyPop struct {
	mu      sync.Mutex
	window  []string
	max     int
	counts  map[string]int
}

const entropyWindowMax = 10000

// NewEntropyMonitor returns an empty monitor.
func NewEntropyMonitor() *EntropyMonitor {
	return &EntropyMonitor{pops: map[string]*entropyPop{}}
}

// Observe records one output.
func (e *EntropyMonitor) Observe(popID, output string) error {
	if popID == "" {
		return errors.New("pop_id required")
	}
	if output == "" {
		return errors.New("output required")
	}
	e.totalObserves.Add(1)
	p := e.popOrCreate(popID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.window = append(p.window, output)
	p.counts[output]++
	if len(p.window) > p.max {
		drop := p.window[0]
		p.window = p.window[1:]
		p.counts[drop]--
		if p.counts[drop] == 0 {
			delete(p.counts, drop)
		}
	}
	return nil
}

// EntropyReport is REPORT's return.
type EntropyReport struct {
	PopID            string             `json:"pop_id"`
	N                int                `json:"n"`
	Distinct         int                `json:"distinct"`
	ShannonBits      float64            `json:"shannon_bits"`
	MaxPossibleBits  float64            `json:"max_possible_bits"`
	UniqueFraction   float64            `json:"unique_fraction"`
	TopModes         []EntropyModeRow   `json:"top_modes"`
	Verdict          string             `json:"verdict"` // HEALTHY|DEGRADED|COLLAPSED|INSUFFICIENT
	Reason           string             `json:"reason"`
}

// EntropyModeRow is one top-mode row.
type EntropyModeRow struct {
	Output string `json:"output"`
	Count  int    `json:"count"`
}

// Report returns the population stats. topN defaults to 5.
func (e *EntropyMonitor) Report(popID string, topN int) (EntropyReport, bool) {
	if topN <= 0 {
		topN = 5
	}
	e.totalReports.Add(1)
	e.mu.RLock()
	p, ok := e.pops[popID]
	e.mu.RUnlock()
	if !ok {
		return EntropyReport{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.window)
	out := EntropyReport{
		PopID: popID, N: n, Distinct: len(p.counts),
	}
	if n < 20 {
		out.Verdict = "INSUFFICIENT"
		out.Reason = "need at least 20 observations"
		return out, true
	}
	// Shannon entropy
	for _, c := range p.counts {
		p_i := float64(c) / float64(n)
		if p_i > 0 {
			out.ShannonBits -= p_i * math.Log2(p_i)
		}
	}
	out.MaxPossibleBits = math.Log2(float64(len(p.counts)))
	out.UniqueFraction = float64(len(p.counts)) / float64(n)
	// Top modes
	modes := make([]EntropyModeRow, 0, len(p.counts))
	for s, c := range p.counts {
		modes = append(modes, EntropyModeRow{Output: s, Count: c})
	}
	sort.Slice(modes, func(i, j int) bool { return modes[i].Count > modes[j].Count })
	if len(modes) > topN {
		modes = modes[:topN]
	}
	out.TopModes = modes
	// Verdict
	switch {
	case out.UniqueFraction < 0.05:
		out.Verdict = "COLLAPSED"
		out.Reason = "unique fraction < 5% — population output collapsed to a few modes"
	case out.MaxPossibleBits > 0 && out.ShannonBits/out.MaxPossibleBits < 0.4:
		out.Verdict = "COLLAPSED"
		out.Reason = "Shannon entropy < 40% of max — distribution is heavily skewed"
	case out.UniqueFraction < 0.20:
		out.Verdict = "DEGRADED"
		out.Reason = "unique fraction drifting low — investigate before users notice"
	default:
		out.Verdict = "HEALTHY"
		out.Reason = "output distribution well-spread"
	}
	return out, true
}

// Reset wipes a population (or all).
func (e *EntropyMonitor) Reset(popID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if popID == "ALL" {
		n := len(e.pops)
		e.pops = map[string]*entropyPop{}
		return n
	}
	if _, ok := e.pops[popID]; ok {
		delete(e.pops, popID)
		return 1
	}
	return 0
}

// List returns every known pop id.
func (e *EntropyMonitor) List() []string {
	e.mu.RLock()
	out := make([]string, 0, len(e.pops))
	for k := range e.pops {
		out = append(out, k)
	}
	e.mu.RUnlock()
	sort.Strings(out)
	return out
}

// EntropyStats is the global snapshot.
type EntropyStats struct {
	Pops          int   `json:"pops"`
	TotalObserves int64 `json:"total_observes"`
	TotalReports  int64 `json:"total_reports"`
}

func (e *EntropyMonitor) Stats() EntropyStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return EntropyStats{
		Pops:          len(e.pops),
		TotalObserves: e.totalObserves.Load(),
		TotalReports:  e.totalReports.Load(),
	}
}

func (e *EntropyMonitor) popOrCreate(id string) *entropyPop {
	e.mu.RLock()
	p, ok := e.pops[id]
	e.mu.RUnlock()
	if ok {
		return p
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.pops[id]; ok {
		return p
	}
	p = &entropyPop{max: entropyWindowMax, counts: map[string]int{}}
	e.pops[id] = p
	return p
}
