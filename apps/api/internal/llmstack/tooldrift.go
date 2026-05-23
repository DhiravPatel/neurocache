package llmstack

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ToolDriftWatcher watches tool / API responses for schema or
// distribution drift. Agents call dozens of tools (search, calc,
// weather, internal microservices); every one of those tools can
// silently change its response shape — a renamed key, a new error
// envelope, a number that became a string. The agent breaks
// downstream in a way that's brutal to debug because nothing
// raised an exception, it just produced bad answers.
//
// TOOLDRIFT.* extracts a *shape signature* from each payload (set
// of (key-path, value-type) pairs for JSON; character-class
// fingerprint for plain text). When the live signature drifts away
// from the baseline by > THRESHOLD cosine, the watcher flips to
// "warning" then "drift" so the orchestrator can quarantine the
// tool or page the team.
//
// Commands:
//
//   TOOLDRIFT.BASELINE tool-id payload [payload...]
//        Seed the baseline from K known-good samples.
//   TOOLDRIFT.SAMPLE tool-id payload
//        Record one observation. Lazy verdict update.
//   TOOLDRIFT.CHECK tool-id payload
//        Score this payload vs baseline →
//        {drift_score, verdict, signature_size, baseline_size}
//        verdict: stable | warning | drift | no_baseline
//   TOOLDRIFT.STATUS tool-id
//   TOOLDRIFT.RECENT tool-id [LIMIT n]
//   TOOLDRIFT.LIST
//   TOOLDRIFT.RESET tool-id
//   TOOLDRIFT.STATS
//
// Hot path: CHECK extracts the signature once (O(payload-size)),
// hashes into a 128-dim sparse vector, takes one dot product
// against the baseline vector. ~3 µs for a typical 500-byte JSON
// response.
type ToolDriftWatcher struct {
	mu      sync.RWMutex
	tools   map[string]*toolDriftEntry
	cfg     toolDriftConfig

	totalSamples atomic.Int64
	totalChecks  atomic.Int64
	totalDrifts  atomic.Int64
}

type toolDriftConfig struct {
	WarnThreshold  float64 // cosine drop below this → warning
	DriftThreshold float64 // cosine drop below this → drift
	RecentCap      int
}

type toolDriftEntry struct {
	mu           sync.RWMutex
	baselineVec  []float64
	baselineSize int
	recent       []toolDriftSample
	lastVerdict  string
	lastScore    float64
	lastTS       int64
}

type toolDriftSample struct {
	Payload string
	Score   float64 // cosine vs baseline at sample time; 0 if no baseline
	Verdict string
	TS      int64
}

// NewToolDriftWatcher returns a watcher with sane defaults.
func NewToolDriftWatcher() *ToolDriftWatcher {
	return &ToolDriftWatcher{
		tools: map[string]*toolDriftEntry{},
		cfg: toolDriftConfig{
			WarnThreshold:  0.85,
			DriftThreshold: 0.65,
			RecentCap:      100,
		},
	}
}

// Baseline replaces the baseline for tool-id from K sample payloads.
// All payloads are averaged into one signature vector.
func (w *ToolDriftWatcher) Baseline(toolID string, payloads []string) error {
	if toolID == "" {
		return errors.New("tool_id required")
	}
	if len(payloads) == 0 {
		return errors.New("at least one baseline payload required")
	}
	avg := make([]float64, 128)
	for _, p := range payloads {
		v := extractShapeVec(p)
		for i, x := range v {
			avg[i] += x
		}
	}
	// L2-normalize the averaged vector
	var sum float64
	for _, x := range avg {
		sum += x * x
	}
	if sum > 0 {
		norm := math.Sqrt(sum)
		for i := range avg {
			avg[i] /= norm
		}
	}
	e := w.entryOrCreate(toolID)
	e.mu.Lock()
	e.baselineVec = avg
	e.baselineSize = len(payloads)
	e.mu.Unlock()
	return nil
}

// Sample records one observation and rolls the recent-sample buffer.
// Verdict is computed lazily inside CHECK to avoid duplicating work
// when both are called.
func (w *ToolDriftWatcher) Sample(toolID, payload string) (ToolDriftResult, error) {
	if toolID == "" {
		return ToolDriftResult{}, errors.New("tool_id required")
	}
	w.totalSamples.Add(1)
	r, _ := w.Check(toolID, payload)
	e := w.entryOrCreate(toolID)
	e.mu.Lock()
	if len(e.recent) >= w.cfg.RecentCap {
		e.recent = e.recent[1:]
	}
	e.recent = append(e.recent, toolDriftSample{
		Payload: payload, Score: r.DriftScore, Verdict: r.Verdict, TS: time.Now().UnixNano(),
	})
	e.lastVerdict = r.Verdict
	e.lastScore = r.DriftScore
	e.lastTS = time.Now().UnixNano()
	e.mu.Unlock()
	return r, nil
}

// ToolDriftResult is CHECK / SAMPLE output.
type ToolDriftResult struct {
	DriftScore    float64 `json:"drift_score"`    // 1.0 - cosine; 0=identical, 1=opposite
	Verdict       string  `json:"verdict"`        // stable | warning | drift | no_baseline
	SignatureSize int     `json:"signature_size"` // non-zero buckets in this payload's signature
	BaselineSize  int     `json:"baseline_size"`  // baseline samples
}

// Check scores a payload against the baseline (no mutation).
func (w *ToolDriftWatcher) Check(toolID, payload string) (ToolDriftResult, error) {
	if toolID == "" {
		return ToolDriftResult{}, errors.New("tool_id required")
	}
	w.totalChecks.Add(1)
	e := w.entryOrCreate(toolID)
	e.mu.RLock()
	base := e.baselineVec
	baseSize := e.baselineSize
	e.mu.RUnlock()
	v := extractShapeVec(payload)
	nonZero := 0
	for _, x := range v {
		if x != 0 {
			nonZero++
		}
	}
	out := ToolDriftResult{SignatureSize: nonZero, BaselineSize: baseSize}
	if base == nil {
		out.Verdict = "no_baseline"
		return out, nil
	}
	cos := dotProduct(base, v)
	out.DriftScore = 1.0 - cos
	switch {
	case cos >= w.cfg.WarnThreshold:
		out.Verdict = "stable"
	case cos >= w.cfg.DriftThreshold:
		out.Verdict = "warning"
	default:
		out.Verdict = "drift"
		w.totalDrifts.Add(1)
	}
	return out, nil
}

// ToolDriftStatus is the per-tool snapshot.
type ToolDriftStatus struct {
	ToolID       string  `json:"tool_id"`
	BaselineSize int     `json:"baseline_size"`
	RecentSize   int     `json:"recent_size"`
	LastVerdict  string  `json:"last_verdict"`
	LastScore    float64 `json:"last_score"`
	LastTS       int64   `json:"last_ts"`
}

// Status returns the per-tool snapshot.
func (w *ToolDriftWatcher) Status(toolID string) (ToolDriftStatus, bool) {
	w.mu.RLock()
	e, ok := w.tools[toolID]
	w.mu.RUnlock()
	if !ok {
		return ToolDriftStatus{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return ToolDriftStatus{
		ToolID:       toolID,
		BaselineSize: e.baselineSize,
		RecentSize:   len(e.recent),
		LastVerdict:  e.lastVerdict,
		LastScore:    e.lastScore,
		LastTS:       e.lastTS / int64(time.Second),
	}, true
}

// ToolDriftRecentRow is one RECENT row.
type ToolDriftRecentRow struct {
	TS      int64   `json:"ts"`
	Verdict string  `json:"verdict"`
	Score   float64 `json:"score"`
	Payload string  `json:"payload"`
}

// Recent returns the last samples (newest last). limit=0 means all.
func (w *ToolDriftWatcher) Recent(toolID string, limit int) ([]ToolDriftRecentRow, bool) {
	w.mu.RLock()
	e, ok := w.tools[toolID]
	w.mu.RUnlock()
	if !ok {
		return nil, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	samples := e.recent
	if limit > 0 && limit < len(samples) {
		samples = samples[len(samples)-limit:]
	}
	out := make([]ToolDriftRecentRow, len(samples))
	for i, s := range samples {
		out[i] = ToolDriftRecentRow{
			TS:      s.TS / int64(time.Second),
			Verdict: s.Verdict,
			Score:   s.Score,
			Payload: s.Payload,
		}
	}
	return out, true
}

// List returns every tool id known to the watcher, sorted.
func (w *ToolDriftWatcher) List() []string {
	w.mu.RLock()
	out := make([]string, 0, len(w.tools))
	for k := range w.tools {
		out = append(out, k)
	}
	w.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops every sample + baseline for tool-id.
func (w *ToolDriftWatcher) Reset(toolID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.tools[toolID]
	delete(w.tools, toolID)
	return ok
}

// ToolDriftStats is the global snapshot.
type ToolDriftStats struct {
	Tools        int   `json:"tools"`
	TotalSamples int64 `json:"total_samples"`
	TotalChecks  int64 `json:"total_checks"`
	TotalDrifts  int64 `json:"total_drifts_detected"`
}

func (w *ToolDriftWatcher) Stats() ToolDriftStats {
	w.mu.RLock()
	n := len(w.tools)
	w.mu.RUnlock()
	return ToolDriftStats{
		Tools:        n,
		TotalSamples: w.totalSamples.Load(),
		TotalChecks:  w.totalChecks.Load(),
		TotalDrifts:  w.totalDrifts.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (w *ToolDriftWatcher) entryOrCreate(toolID string) *toolDriftEntry {
	w.mu.RLock()
	e, ok := w.tools[toolID]
	w.mu.RUnlock()
	if ok {
		return e
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.tools[toolID]; ok {
		return e
	}
	e = &toolDriftEntry{}
	w.tools[toolID] = e
	return e
}

// extractShapeVec hashes a payload into a 128-dim normalized vector.
// If the payload parses as JSON, the signature is {key-path:type}
// pairs (so "added.field" or "string→number" change the vector). If
// not, we fall back to a character-class fingerprint (digits, letters,
// spaces, punct distribution).
func extractShapeVec(payload string) []float64 {
	const dim = 128
	out := make([]float64, dim)
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err == nil {
		walkShape(v, "", out)
	} else {
		fingerprintChars(payload, out)
	}
	// L2-normalize so cosine = dot product
	var sum float64
	for _, x := range out {
		sum += x * x
	}
	if sum == 0 {
		return out
	}
	norm := math.Sqrt(sum)
	for i := range out {
		out[i] /= norm
	}
	return out
}

func walkShape(v any, path string, out []float64) {
	switch n := v.(type) {
	case map[string]any:
		bumpShape(path+":object", out)
		// Sort keys so order doesn't matter
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkShape(n[k], path+"."+k, out)
		}
	case []any:
		bumpShape(path+":array", out)
		// Walk only the first element — arrays are uniform in well-
		// behaved APIs; sampling all elements would inflate weight
		// on long lists and miss the actual schema change.
		if len(n) > 0 {
			walkShape(n[0], path+"[]", out)
		}
	case string:
		bumpShape(path+":string", out)
	case float64:
		bumpShape(path+":number", out)
	case bool:
		bumpShape(path+":bool", out)
	case nil:
		bumpShape(path+":null", out)
	}
}

func bumpShape(token string, out []float64) {
	h := fnv1a32(token)
	out[h%uint32(len(out))] += 1
}

func fingerprintChars(s string, out []float64) {
	// Bag-of-character-trigrams. Each 3-byte window is hashed into one
	// vector bucket. After L2-normalize, payloads with similar surface
	// shape (same nginx headers) stay close; structurally different
	// payloads (HTTP header vs error trace) drift apart cleanly —
	// character-class histograms alone normalize to look identical.
	for i := 0; i+3 <= len(s); i++ {
		h := fnv1a32(s[i : i+3])
		out[h%uint32(len(out))] += 1
	}
}
