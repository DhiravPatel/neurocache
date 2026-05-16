package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// ConfidenceCalibrator tracks the gap between predicted and actual
// model confidence — and exposes a CALIBRATE call that maps a raw
// confidence to the empirical hit-rate the cache has measured for
// that bin. The classic production failure mode:
//
//   - Model says "I'm 80% confident in this answer"
//   - App routes high-confidence answers to no-review
//   - Actual hit rate at 0.8 is 0.45 — half the no-review answers
//     are wrong, ops finds out via Twitter
//
// CONFIDENCE.* gives the cache a single command set:
//
//   CONFIDENCE.RECORD model-id predicted actual    → records pair
//   CONFIDENCE.CURVE model-id [BINS n]              → reliability bins
//   CONFIDENCE.ECE model-id [BINS n]                → Expected Cal Error
//   CONFIDENCE.CALIBRATE model-id raw-conf [BINS n] → empirical hit rate
//   CONFIDENCE.RESET model-id
//   CONFIDENCE.MODELS
//   CONFIDENCE.STATS
//
// Storage: per-model atomic-ring sample buffer (default 10k pairs).
// Lock-free RECORD via atomic-index + per-slot write — no mutex
// contention even at high QPS. CURVE and CALIBRATE take a brief
// read lock to snapshot the buffer.
//
// The CALIBRATE primitive is the production hot path: app receives
// a raw 0.8 confidence from the model, calls CALIBRATE → gets back
// the empirical hit rate the cache has measured for the 0.7-0.85
// bin (e.g. 0.45), and uses THAT for gating decisions.
type ConfidenceCalibrator struct {
	mu     sync.RWMutex
	models map[string]*confidenceModel

	totalRecords atomic.Int64
	totalCurves  atomic.Int64
	totalCals    atomic.Int64
}

type confidenceModel struct {
	mu     sync.RWMutex
	pairs  []confidencePair // ring buffer
	head   int              // next write index
	full   bool
	cap    int
}

type confidencePair struct {
	predicted float64
	actual    float64 // 0 or 1 (or any [0,1] score)
}

// NewConfidenceCalibrator returns an empty calibrator with 10k
// sample-buffer default.
func NewConfidenceCalibrator() *ConfidenceCalibrator {
	return &ConfidenceCalibrator{models: map[string]*confidenceModel{}}
}

// Record logs one (predicted, actual) pair. Predicted MUST be in
// [0, 1]; actual is typically 0 or 1 but [0,1] is accepted (for
// partial-credit graders).
func (c *ConfidenceCalibrator) Record(modelID string, predicted, actual float64) error {
	if modelID == "" {
		return errors.New("model_id required")
	}
	if predicted < 0 || predicted > 1 {
		return errors.New("predicted must be in [0, 1]")
	}
	if actual < 0 || actual > 1 {
		return errors.New("actual must be in [0, 1]")
	}
	c.totalRecords.Add(1)
	m := c.modelFor(modelID)
	m.mu.Lock()
	m.pairs[m.head] = confidencePair{predicted: predicted, actual: actual}
	m.head++
	if m.head >= m.cap {
		m.head = 0
		m.full = true
	}
	m.mu.Unlock()
	return nil
}

// ReliabilityBin is one bucket of the reliability curve.
type ReliabilityBin struct {
	BinLo          float64 `json:"bin_lo"`
	BinHi          float64 `json:"bin_hi"`
	PredictedAvg   float64 `json:"predicted_avg"`
	ActualRate     float64 `json:"actual_rate"`
	Count          int     `json:"count"`
	GapAbs         float64 `json:"gap_abs"` // |predicted_avg - actual_rate|
}

// Curve returns reliability bins. Standard "calibration plot" data:
// for each bin of predicted confidence, the average predicted value
// and the actual hit rate. BINS defaults to 10 (equal-width 0.0-1.0).
func (c *ConfidenceCalibrator) Curve(modelID string, bins int) ([]ReliabilityBin, bool) {
	c.totalCurves.Add(1)
	if bins <= 0 {
		bins = 10
	}
	c.mu.RLock()
	m, ok := c.models[modelID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	pairs := m.snapshot()
	if len(pairs) == 0 {
		return []ReliabilityBin{}, true
	}
	out := make([]ReliabilityBin, bins)
	binCounts := make([]int, bins)
	predSums := make([]float64, bins)
	actSums := make([]float64, bins)
	binWidth := 1.0 / float64(bins)
	for _, p := range pairs {
		b := int(p.predicted / binWidth)
		if b >= bins {
			b = bins - 1
		}
		binCounts[b]++
		predSums[b] += p.predicted
		actSums[b] += p.actual
	}
	for i := 0; i < bins; i++ {
		out[i].BinLo = float64(i) * binWidth
		out[i].BinHi = float64(i+1) * binWidth
		out[i].Count = binCounts[i]
		if binCounts[i] > 0 {
			out[i].PredictedAvg = predSums[i] / float64(binCounts[i])
			out[i].ActualRate = actSums[i] / float64(binCounts[i])
			out[i].GapAbs = math.Abs(out[i].PredictedAvg - out[i].ActualRate)
		}
	}
	return out, true
}

// ECE returns the Expected Calibration Error — weighted average of
// |predicted_avg - actual_rate| across bins. Lower is better
// (perfectly calibrated = 0). >0.05 is typically poor calibration.
func (c *ConfidenceCalibrator) ECE(modelID string, bins int) (float64, int, bool) {
	curve, ok := c.Curve(modelID, bins)
	if !ok {
		return 0, 0, false
	}
	total := 0
	for _, b := range curve {
		total += b.Count
	}
	if total == 0 {
		return 0, 0, true
	}
	ece := 0.0
	for _, b := range curve {
		if b.Count == 0 {
			continue
		}
		ece += float64(b.Count) / float64(total) * b.GapAbs
	}
	return ece, total, true
}

// CALIBRATE returns the empirical hit rate for the bin containing
// rawConf. The production hot path: apps replace raw model
// confidence with this for gating decisions.
//
// Sparse bins fall back to the predicted value (better to use the
// raw value than to make up a number from 2 samples).
func (c *ConfidenceCalibrator) Calibrate(modelID string, rawConf float64, bins int) (float64, bool) {
	c.totalCals.Add(1)
	if rawConf < 0 || rawConf > 1 {
		return rawConf, false
	}
	curve, ok := c.Curve(modelID, bins)
	if !ok {
		return rawConf, false
	}
	if bins <= 0 {
		bins = 10
	}
	binWidth := 1.0 / float64(bins)
	b := int(rawConf / binWidth)
	if b >= bins {
		b = bins - 1
	}
	const minSamples = 10
	if curve[b].Count < minSamples {
		return rawConf, true // fall back to raw — too few samples to trust
	}
	return curve[b].ActualRate, true
}

// Reset wipes the per-model sample buffer.
func (c *ConfidenceCalibrator) Reset(modelID string) bool {
	c.mu.RLock()
	m, ok := c.models[modelID]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	m.mu.Lock()
	m.head = 0
	m.full = false
	for i := range m.pairs {
		m.pairs[i] = confidencePair{}
	}
	m.mu.Unlock()
	return true
}

// Models returns every tracked model id with its sample count.
type ModelRow struct {
	ModelID string `json:"model_id"`
	Samples int    `json:"samples"`
	Cap     int    `json:"cap"`
}

func (c *ConfidenceCalibrator) Models() []ModelRow {
	c.mu.RLock()
	out := make([]ModelRow, 0, len(c.models))
	for id, m := range c.models {
		m.mu.RLock()
		n := m.head
		if m.full {
			n = m.cap
		}
		out = append(out, ModelRow{ModelID: id, Samples: n, Cap: m.cap})
		m.mu.RUnlock()
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out
}

// ConfidenceStats is the global snapshot.
type ConfidenceStats struct {
	Models       int   `json:"models"`
	TotalRecords int64 `json:"total_records"`
	TotalCurves  int64 `json:"total_curves"`
	TotalCals    int64 `json:"total_calibrates"`
}

func (c *ConfidenceCalibrator) Stats() ConfidenceStats {
	c.mu.RLock()
	n := len(c.models)
	c.mu.RUnlock()
	return ConfidenceStats{
		Models:       n,
		TotalRecords: c.totalRecords.Load(),
		TotalCurves:  c.totalCurves.Load(),
		TotalCals:    c.totalCals.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (c *ConfidenceCalibrator) modelFor(modelID string) *confidenceModel {
	c.mu.RLock()
	m, ok := c.models[modelID]
	c.mu.RUnlock()
	if ok {
		return m
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.models[modelID]; ok {
		return m
	}
	fresh := &confidenceModel{
		pairs: make([]confidencePair, 10_000),
		cap:   10_000,
	}
	c.models[modelID] = fresh
	return fresh
}

func (m *confidenceModel) snapshot() []confidencePair {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.full {
		out := make([]confidencePair, m.head)
		copy(out, m.pairs[:m.head])
		return out
	}
	out := make([]confidencePair, m.cap)
	copy(out, m.pairs[m.head:])
	copy(out[m.cap-m.head:], m.pairs[:m.head])
	return out
}
