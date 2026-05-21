package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// DriftDetector watches an input-text stream and flags when its
// distribution has drifted from a baseline. Real production pain:
// the prompt stream silently shifts — new product launched, new
// user cohort onboarded, viral topic — and the app's downstream
// pipeline starts producing weird outputs. Standard monitoring
// (latency, error-rate) catches NONE of this.
//
// DRIFT.* gives the cache a token-distribution-drift watcher:
//
//   DRIFT.BASELINE.SET tracker-id text [text...]
//        Build the baseline 1-gram + 2-gram bag from sample texts.
//
//   DRIFT.OBSERVE tracker-id text [WINDOW n]
//        Record the observation in the rolling window (default 1000).
//        Returns the current drift score + sample count.
//
//   DRIFT.SCORE tracker-id
//        Compute drift score = 1 - Jaccard(baseline_bag, recent_bag).
//        Returns score + verdict (stable/drifting/diverged).
//
//   DRIFT.RESET tracker-id
//   DRIFT.TRACKERS
//   DRIFT.STATS
//
// Verdicts:
//   < 0.30 → stable
//   < 0.55 → drifting  (alert threshold)
//   ≥ 0.55 → diverged  (escalate)
//
// Storage: per-tracker baseline-bag (set once) + FIFO recent-window.
// OBSERVE is atomic: tokenise the text, merge into the recent bag.
// Re-computing the drift score on every observe would be expensive;
// instead we batch — score is computed lazily on DRIFT.SCORE or
// every Nth OBSERVE (default 50).
type DriftDetector struct {
	mu       sync.RWMutex
	trackers map[string]*driftTracker

	totalBaselines atomic.Int64
	totalObserves  atomic.Int64
	totalScores    atomic.Int64
}

type driftTracker struct {
	mu sync.RWMutex

	baseline map[string]struct{} // 1-gram + 2-gram bag

	// Recent observations: ring buffer of bags. Each entry is a
	// per-observation bag; merge-on-demand for the current window
	// bag.
	observations []map[string]struct{}
	head         int
	full         bool
	window       int

	// Cached score (recomputed every reFreqN observations)
	cachedScore   atomic.Uint64 // float64-bits
	observesSince atomic.Int64
	reFreqN       int64
}

// NewDriftDetector returns an empty detector.
func NewDriftDetector() *DriftDetector {
	return &DriftDetector{trackers: map[string]*driftTracker{}}
}

// SetBaseline (re-)initialises a tracker's baseline distribution
// from the supplied sample texts.
func (d *DriftDetector) SetBaseline(trackerID string, samples []string, window int) error {
	if trackerID == "" {
		return errors.New("tracker_id required")
	}
	if len(samples) == 0 {
		return errors.New("at least one sample required")
	}
	if window <= 0 {
		window = 1000
	}
	bag := map[string]struct{}{}
	for _, s := range samples {
		for k := range ngramBag(s) {
			bag[k] = struct{}{}
		}
	}
	d.mu.Lock()
	d.trackers[trackerID] = &driftTracker{
		baseline:     bag,
		observations: make([]map[string]struct{}, window),
		window:       window,
		reFreqN:      50,
	}
	d.mu.Unlock()
	d.totalBaselines.Add(1)
	return nil
}

// ObserveResult is what Observe returns to the caller.
type ObserveResult struct {
	Samples int     `json:"samples"`
	Score   float64 `json:"score"`
	Verdict string  `json:"verdict"`
}

// Observe records one text in the rolling window and returns the
// (possibly cached) drift score + verdict.
func (d *DriftDetector) Observe(trackerID, text string) (ObserveResult, bool) {
	d.totalObserves.Add(1)
	d.mu.RLock()
	t, ok := d.trackers[trackerID]
	d.mu.RUnlock()
	if !ok {
		return ObserveResult{}, false
	}
	bag := ngramBag(text)
	t.mu.Lock()
	t.observations[t.head] = bag
	t.head++
	if t.head >= t.window {
		t.head = 0
		t.full = true
	}
	t.mu.Unlock()

	since := t.observesSince.Add(1)
	if since >= t.reFreqN {
		t.observesSince.Store(0)
		score := d.computeScore(t)
		t.cachedScore.Store(float64ToBitsUint(score))
	}
	score := bitsToFloat64(t.cachedScore.Load())
	return ObserveResult{
		Samples: t.sampleCount(),
		Score:   score,
		Verdict: driftVerdict(score),
	}, true
}

// DriftScore is the DRIFT.SCORE return.
type DriftScore struct {
	TrackerID string  `json:"tracker_id"`
	Samples   int     `json:"samples"`
	Score     float64 `json:"score"`
	Verdict   string  `json:"verdict"`
}

// Score forces a recompute and returns the current drift score.
func (d *DriftDetector) Score(trackerID string) (DriftScore, bool) {
	d.totalScores.Add(1)
	d.mu.RLock()
	t, ok := d.trackers[trackerID]
	d.mu.RUnlock()
	if !ok {
		return DriftScore{}, false
	}
	score := d.computeScore(t)
	t.cachedScore.Store(float64ToBitsUint(score))
	t.observesSince.Store(0)
	return DriftScore{
		TrackerID: trackerID,
		Samples:   t.sampleCount(),
		Score:     score,
		Verdict:   driftVerdict(score),
	}, true
}

// Reset wipes the rolling window (baseline is preserved).
func (d *DriftDetector) Reset(trackerID string) bool {
	d.mu.RLock()
	t, ok := d.trackers[trackerID]
	d.mu.RUnlock()
	if !ok {
		return false
	}
	t.mu.Lock()
	t.head = 0
	t.full = false
	for i := range t.observations {
		t.observations[i] = nil
	}
	t.mu.Unlock()
	t.cachedScore.Store(0)
	t.observesSince.Store(0)
	return true
}

// Forget drops a tracker entirely (baseline + window).
func (d *DriftDetector) Forget(trackerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.trackers[trackerID]
	delete(d.trackers, trackerID)
	return ok
}

// TrackerRow is one row of DRIFT.TRACKERS.
type TrackerRow struct {
	TrackerID    string  `json:"tracker_id"`
	BaselineSize int     `json:"baseline_size"`
	Samples      int     `json:"samples"`
	Score        float64 `json:"score"`
	Verdict      string  `json:"verdict"`
}

// Trackers returns every tracker, sorted by id.
func (d *DriftDetector) Trackers() []TrackerRow {
	d.mu.RLock()
	out := make([]TrackerRow, 0, len(d.trackers))
	for id, t := range d.trackers {
		score := bitsToFloat64(t.cachedScore.Load())
		out = append(out, TrackerRow{
			TrackerID:    id,
			BaselineSize: len(t.baseline),
			Samples:      t.sampleCount(),
			Score:        score,
			Verdict:      driftVerdict(score),
		})
	}
	d.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].TrackerID < out[j].TrackerID })
	return out
}

// DriftStats is the global counters snapshot.
type DriftStats struct {
	Trackers       int   `json:"trackers"`
	TotalBaselines int64 `json:"total_baselines"`
	TotalObserves  int64 `json:"total_observes"`
	TotalScores    int64 `json:"total_scores"`
}

func (d *DriftDetector) Stats() DriftStats {
	d.mu.RLock()
	n := len(d.trackers)
	d.mu.RUnlock()
	return DriftStats{
		Trackers:       n,
		TotalBaselines: d.totalBaselines.Load(),
		TotalObserves:  d.totalObserves.Load(),
		TotalScores:    d.totalScores.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (d *DriftDetector) computeScore(t *driftTracker) float64 {
	t.mu.RLock()
	// Merge all observation bags into the recent-window bag.
	recent := map[string]struct{}{}
	for _, obs := range t.observations {
		if obs == nil {
			continue
		}
		for k := range obs {
			recent[k] = struct{}{}
		}
	}
	baseline := t.baseline
	t.mu.RUnlock()
	if len(recent) == 0 || len(baseline) == 0 {
		return 0
	}
	return 1 - jaccard(baseline, recent)
}

func (t *driftTracker) sampleCount() int {
	if t.full {
		return t.window
	}
	return t.head
}

func driftVerdict(score float64) string {
	switch {
	case score < 0.30:
		return "stable"
	case score < 0.55:
		return "drifting"
	default:
		return "diverged"
	}
}

func float64ToBitsUint(f float64) uint64 {
	return math.Float64bits(f)
}

func bitsToFloat64(b uint64) float64 {
	return math.Float64frombits(b)
}
