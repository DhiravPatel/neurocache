package llmstack

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// CanaryDeploys runs gradual rollouts of prompt changes. Every team
// shipping LLM features hits the same problem: a "small tweak to the
// system prompt" silently regresses output quality, only caught when
// users complain a week later. CANARY.* solves this by routing a
// configurable fraction of traffic to the new prompt, tracking per-arm
// success scores, and (optionally) auto-rolling back when the canary
// drifts more than a configurable delta below baseline.
//
// What this is, exactly:
//
//   - A canary is identified by a `canary_id` (e.g. "checkout-summary").
//   - Has a baseline prompt + a candidate prompt.
//   - Has a `traffic_percent` (0-100) that picks the candidate.
//   - Has running per-arm score tallies (sum, count → mean).
//   - Has a `delta_threshold` (default 0.05) below which auto-rollback
//     fires, and a `min_samples` (default 50) before any verdict is
//     returned (avoid flipping on the first three observations).
//
// Lifecycle:
//
//   CANARY.CREATE id baseline candidate [PCT n] [DELTA d] [MIN_N n]
//   CANARY.PICK   id [seed]                -> arm name + prompt to use
//   CANARY.RECORD id arm score             -> updates per-arm tally
//   CANARY.STATUS id                       -> baseline_n / candidate_n / means / verdict
//   CANARY.PROMOTE id                      -> candidate becomes baseline
//   CANARY.ROLLBACK id                     -> wipes candidate, traffic_percent=0
//   CANARY.LIST                            -> active canaries
//   CANARY.FORGET id                       -> drops everything
//
// Why this lives in the cache, not in app code:
//
//   - Distributed sticky-bucketing requires a single source of truth.
//     A shared cache is the simplest place. Keeps every replica in
//     sync without a separate experimentation service.
//   - Per-arm tallies are atomic counters — exactly the thing the
//     cache is good at.
//   - Ops want to flip canary state from a console without bouncing
//     app servers. RESP commands are perfect for that.
//
// This is intentionally simpler than a full A/B service (no
// confidence intervals, no covariate balancing, no multi-arm
// optimisation). Apps that need those graduate to AB.* (Phase 11).
// CANARY is the lightweight path teams reach for first.
type CanaryDeploys struct {
	mu      sync.RWMutex
	canaries map[string]*canaryState

	totalCreates   atomic.Int64
	totalPicks     atomic.Int64
	totalRecords   atomic.Int64
	totalRollbacks atomic.Int64
	totalPromotes  atomic.Int64
}

type canaryState struct {
	id        string
	baseline  string
	candidate string

	// Atomic so PICK is lock-free. Stored as 0-100 percent.
	trafficPct atomic.Int64

	deltaThreshold float64 // for auto-rollback decision
	minSamples     int64   // pre-verdict ignore threshold

	baselineSum   atomic.Uint64 // float64-as-bits accumulator
	baselineCount atomic.Int64
	candidateSum   atomic.Uint64
	candidateCount atomic.Int64

	createdAt time.Time
	updatedAt atomic.Int64 // unix-nano

	// Once auto-rolled back this is set to the unix-nano timestamp.
	// Subsequent PICKs always return baseline; STATUS shows verdict
	// = "auto_rollback".
	rolledBackAt atomic.Int64
}

// NewCanaryDeploys returns an empty deploy registry.
func NewCanaryDeploys() *CanaryDeploys {
	return &CanaryDeploys{
		canaries: map[string]*canaryState{},
	}
}

// CanaryOpts configures a CANARY.CREATE call.
type CanaryOpts struct {
	TrafficPct     int     // 0-100; defaults to 10 if zero
	DeltaThreshold float64 // defaults to 0.05 if zero
	MinSamples     int64   // defaults to 50 if zero
}

// Create registers a new canary. Replacing an existing id is
// allowed (operators occasionally want to redo the rollout) but
// resets the tallies.
func (c *CanaryDeploys) Create(id, baseline, candidate string, opts CanaryOpts) error {
	if id == "" {
		return errors.New("canary id required")
	}
	if baseline == "" || candidate == "" {
		return errors.New("baseline and candidate required")
	}
	if opts.TrafficPct == 0 {
		opts.TrafficPct = 10
	}
	if opts.TrafficPct < 0 || opts.TrafficPct > 100 {
		return errors.New("traffic_percent must be 0-100")
	}
	if opts.DeltaThreshold == 0 {
		opts.DeltaThreshold = 0.05
	}
	if opts.MinSamples == 0 {
		opts.MinSamples = 50
	}
	cs := &canaryState{
		id:             id,
		baseline:       baseline,
		candidate:      candidate,
		deltaThreshold: opts.DeltaThreshold,
		minSamples:     opts.MinSamples,
		createdAt:      time.Now(),
	}
	cs.trafficPct.Store(int64(opts.TrafficPct))
	cs.updatedAt.Store(time.Now().UnixNano())
	c.mu.Lock()
	c.canaries[id] = cs
	c.mu.Unlock()
	c.totalCreates.Add(1)
	return nil
}

// PickResult is the CANARY.PICK return.
type PickResult struct {
	Arm    string `json:"arm"`    // "baseline" or "candidate"
	Prompt string `json:"prompt"`
}

// Pick deterministically routes `seed` to baseline or candidate
// using the configured traffic_percent. Empty seed = uniformly
// random. After auto-rollback, always returns baseline.
func (c *CanaryDeploys) Pick(id, seed string) (PickResult, bool) {
	c.mu.RLock()
	cs, ok := c.canaries[id]
	c.mu.RUnlock()
	if !ok {
		return PickResult{}, false
	}
	c.totalPicks.Add(1)
	if cs.rolledBackAt.Load() != 0 {
		return PickResult{Arm: "baseline", Prompt: cs.baseline}, true
	}
	pct := cs.trafficPct.Load()
	if pct <= 0 {
		return PickResult{Arm: "baseline", Prompt: cs.baseline}, true
	}
	if pct >= 100 {
		return PickResult{Arm: "candidate", Prompt: cs.candidate}, true
	}
	bucket := bucketFromSeed(seed) // 0-99
	if int64(bucket) < pct {
		return PickResult{Arm: "candidate", Prompt: cs.candidate}, true
	}
	return PickResult{Arm: "baseline", Prompt: cs.baseline}, true
}

// Record adds an observation to the named arm. Score is treated as
// "higher is better"; teams typically pass success-rate proxies in
// 0..1 (e.g. eval-pass = 1, eval-fail = 0). Returns the post-record
// snapshot so apps can react inline if auto-rollback fired.
func (c *CanaryDeploys) Record(id, arm string, score float64) (CanaryStatus, bool) {
	c.mu.RLock()
	cs, ok := c.canaries[id]
	c.mu.RUnlock()
	if !ok {
		return CanaryStatus{}, false
	}
	c.totalRecords.Add(1)

	switch arm {
	case "baseline":
		addAtomicFloat(&cs.baselineSum, score)
		cs.baselineCount.Add(1)
	case "candidate":
		addAtomicFloat(&cs.candidateSum, score)
		cs.candidateCount.Add(1)
	default:
		return CanaryStatus{}, false
	}
	cs.updatedAt.Store(time.Now().UnixNano())
	c.checkAutoRollback(cs)
	return c.statusOf(cs), true
}

// CanaryStatus is the CANARY.STATUS return.
type CanaryStatus struct {
	ID             string  `json:"id"`
	Baseline       string  `json:"baseline"`
	Candidate      string  `json:"candidate"`
	TrafficPercent int64   `json:"traffic_percent"`
	BaselineN      int64   `json:"baseline_n"`
	CandidateN     int64   `json:"candidate_n"`
	BaselineMean   float64 `json:"baseline_mean"`
	CandidateMean  float64 `json:"candidate_mean"`
	Delta          float64 `json:"delta"` // candidate - baseline
	Verdict        string  `json:"verdict"`
	DeltaThreshold float64 `json:"delta_threshold"`
	MinSamples     int64   `json:"min_samples"`
	CreatedAt      int64   `json:"created_at_unix"`
	UpdatedAt      int64   `json:"updated_at_unix"`
	RolledBackAt   int64   `json:"rolled_back_at_unix"`
}

// Status returns the canary snapshot.
func (c *CanaryDeploys) Status(id string) (CanaryStatus, bool) {
	c.mu.RLock()
	cs, ok := c.canaries[id]
	c.mu.RUnlock()
	if !ok {
		return CanaryStatus{}, false
	}
	return c.statusOf(cs), true
}

func (c *CanaryDeploys) statusOf(cs *canaryState) CanaryStatus {
	bN := cs.baselineCount.Load()
	cN := cs.candidateCount.Load()
	bMean := 0.0
	cMean := 0.0
	if bN > 0 {
		bMean = loadAtomicFloat(&cs.baselineSum) / float64(bN)
	}
	if cN > 0 {
		cMean = loadAtomicFloat(&cs.candidateSum) / float64(cN)
	}
	delta := cMean - bMean
	verdict := "monitoring"
	if cs.rolledBackAt.Load() != 0 {
		verdict = "auto_rollback"
	} else if cN >= cs.minSamples && bN >= cs.minSamples {
		switch {
		case delta < -cs.deltaThreshold:
			verdict = "regressed"
		case delta > cs.deltaThreshold:
			verdict = "improved"
		default:
			verdict = "neutral"
		}
	}
	return CanaryStatus{
		ID:             cs.id,
		Baseline:       cs.baseline,
		Candidate:      cs.candidate,
		TrafficPercent: cs.trafficPct.Load(),
		BaselineN:      bN,
		CandidateN:     cN,
		BaselineMean:   bMean,
		CandidateMean:  cMean,
		Delta:          delta,
		Verdict:        verdict,
		DeltaThreshold: cs.deltaThreshold,
		MinSamples:     cs.minSamples,
		CreatedAt:      cs.createdAt.Unix(),
		UpdatedAt:      cs.updatedAt.Load() / int64(time.Second),
		RolledBackAt:   cs.rolledBackAt.Load() / int64(time.Second),
	}
}

// checkAutoRollback fires when both arms have at least min_samples
// AND candidate is more than delta_threshold worse than baseline.
// Idempotent: subsequent calls after rollback do nothing.
func (c *CanaryDeploys) checkAutoRollback(cs *canaryState) {
	if cs.rolledBackAt.Load() != 0 {
		return
	}
	bN := cs.baselineCount.Load()
	cN := cs.candidateCount.Load()
	if bN < cs.minSamples || cN < cs.minSamples {
		return
	}
	bMean := loadAtomicFloat(&cs.baselineSum) / float64(bN)
	cMean := loadAtomicFloat(&cs.candidateSum) / float64(cN)
	if (bMean - cMean) > cs.deltaThreshold {
		cs.rolledBackAt.Store(time.Now().UnixNano())
		cs.trafficPct.Store(0)
		c.totalRollbacks.Add(1)
	}
}

// SetTraffic adjusts the live percent. Operators ramp this up
// manually after observing a few hundred neutral candidate
// responses.
func (c *CanaryDeploys) SetTraffic(id string, pct int) bool {
	if pct < 0 || pct > 100 {
		return false
	}
	c.mu.RLock()
	cs, ok := c.canaries[id]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	cs.trafficPct.Store(int64(pct))
	cs.updatedAt.Store(time.Now().UnixNano())
	return true
}

// Promote replaces the baseline with the candidate and clears
// tallies. Intended for the happy path: candidate proved itself,
// ship it. New canaries can be re-created against the new baseline.
func (c *CanaryDeploys) Promote(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cs, ok := c.canaries[id]
	if !ok {
		return false
	}
	cs.baseline = cs.candidate
	cs.candidate = ""
	cs.trafficPct.Store(0)
	cs.baselineSum.Store(0)
	cs.baselineCount.Store(0)
	cs.candidateSum.Store(0)
	cs.candidateCount.Store(0)
	cs.rolledBackAt.Store(0)
	cs.updatedAt.Store(time.Now().UnixNano())
	c.totalPromotes.Add(1)
	return true
}

// Rollback wipes the candidate and zeros traffic. Manual lever
// for operators who don't want to wait for auto-rollback.
func (c *CanaryDeploys) Rollback(id string) bool {
	c.mu.RLock()
	cs, ok := c.canaries[id]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	cs.rolledBackAt.Store(time.Now().UnixNano())
	cs.trafficPct.Store(0)
	cs.updatedAt.Store(time.Now().UnixNano())
	c.totalRollbacks.Add(1)
	return true
}

// Forget drops a canary entirely.
func (c *CanaryDeploys) Forget(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.canaries[id]
	delete(c.canaries, id)
	return ok
}

// List returns every canary status, ordered by creation time.
func (c *CanaryDeploys) List() []CanaryStatus {
	c.mu.RLock()
	out := make([]CanaryStatus, 0, len(c.canaries))
	for _, cs := range c.canaries {
		out = append(out, c.statusOf(cs))
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// CanaryDeployStats is the global counters snapshot.
type CanaryDeployStats struct {
	TotalCreates   int64 `json:"total_creates"`
	TotalPicks     int64 `json:"total_picks"`
	TotalRecords   int64 `json:"total_records"`
	TotalRollbacks int64 `json:"total_rollbacks"`
	TotalPromotes  int64 `json:"total_promotes"`
	ActiveCanaries int   `json:"active_canaries"`
}

func (c *CanaryDeploys) Stats() CanaryDeployStats {
	c.mu.RLock()
	n := len(c.canaries)
	c.mu.RUnlock()
	return CanaryDeployStats{
		TotalCreates:   c.totalCreates.Load(),
		TotalPicks:     c.totalPicks.Load(),
		TotalRecords:   c.totalRecords.Load(),
		TotalRollbacks: c.totalRollbacks.Load(),
		TotalPromotes:  c.totalPromotes.Load(),
		ActiveCanaries: n,
	}
}

// ─── helpers ───────────────────────────────────────────────────

// bucketFromSeed returns 0..99 stable for a given seed. Empty seed
// uses time so different concurrent picks land on different buckets.
func bucketFromSeed(seed string) uint32 {
	if seed == "" {
		// xorshift on time-nanos for cheap entropy. Not crypto.
		x := uint32(time.Now().UnixNano())
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		return x % 100
	}
	h := sha256.Sum256([]byte(seed))
	return binary.BigEndian.Uint32(h[:4]) % 100
}

// addAtomicFloat is CAS-loop float64 addition over an atomic.Uint64
// holding the float bits. Used for lock-free running sums.
func addAtomicFloat(slot *atomic.Uint64, delta float64) {
	for {
		oldBits := slot.Load()
		newBits := math.Float64frombits(oldBits) + delta
		if slot.CompareAndSwap(oldBits, math.Float64bits(newBits)) {
			return
		}
	}
}

func loadAtomicFloat(slot *atomic.Uint64) float64 {
	return math.Float64frombits(slot.Load())
}
