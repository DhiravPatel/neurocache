package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ContinualLearning guards online learners (TRUST, BANDIT, RAG.GAP,
// FED) against catastrophic forgetting. Every learner in the engine
// drifts forever in one direction with no protection against a
// poisoned feedback burst overwriting months of good signal.
//
// The classical protection from the ML literature is "rehearsal" —
// hold out an anchor set of known-good (input, expected) pairs,
// periodically score the live learner against it, and if performance
// on the anchors degrades beyond a threshold, ROLLBACK.
//
// We provide the bookkeeping primitive:
//
//   - CHECKPOINT captures a named state snapshot for a learner.
//     Snapshot payloads are opaque — the calling primitive supplies
//     a JSON blob of "whatever I need to restore." We don't
//     interpret it.
//
//   - ANCHOR adds an (input, expected, observed) tuple. Anchors are
//     held-out gold standards — the calling primitive evaluates its
//     current learner against the anchors and posts observed.
//
//   - REHEARSE walks the anchors and reports drift: mean error,
//     pass rate (observed within tolerance of expected), trend (is
//     this checkpoint's drift worse than the previous?).
//
//   - DIVERGENCE returns a single quality score for a learner —
//     drop in pass-rate from the last "blessed" checkpoint. If
//     drift > threshold, the app's AUTO rule can ROLLBACK.
//
//   - ROLLBACK returns the named checkpoint payload so the app can
//     restore it.
//
// Commands:
//
//   CONTINUAL.CHECKPOINT learner-id checkpoint-id payload-json [BLESS 0|1]
//        BLESS marks this checkpoint as the reference for DIVERGENCE.
//   CONTINUAL.ANCHOR learner-id anchor-id "input" "expected" [TOL f]
//        Pre-register the gold standard (input is opaque, expected
//        is a numeric or string label; TOL is the numeric tolerance).
//   CONTINUAL.REHEARSE learner-id observation-id anchor-id "observed"
//        Post one rehearsal result. We compare to expected.
//   CONTINUAL.DIVERGENCE learner-id [SINCE checkpoint-id]
//        → pass_rate, mean_error, drift_vs_blessed, verdict
//        (HEALTHY|DRIFTING|FORGOTTEN|INSUFFICIENT)
//   CONTINUAL.ROLLBACK learner-id [TO checkpoint-id]
//        Returns the payload to restore (the app must apply it).
//        Default TO = the blessed checkpoint.
//   CONTINUAL.LIST [LEARNER l]
//   CONTINUAL.FORGET learner-id|ALL
//   CONTINUAL.STATS
//
// Hot path: ANCHOR and REHEARSE are O(1). DIVERGENCE walks the
// anchor set (typically dozens-to-hundreds) — fine for a periodic
// check, not for the request path.
type ContinualLearning struct {
	mu       sync.RWMutex
	learners map[string]*continualLearner

	totalCheckpoints atomic.Int64
	totalRehearses   atomic.Int64
	totalRollbacks   atomic.Int64
}

type continualLearner struct {
	mu             sync.Mutex
	id             string
	checkpoints    map[string]continualCheckpoint
	checkpointOrder []string
	blessed        string // currently-blessed checkpoint id
	anchors        map[string]continualAnchor
	rehearsals     map[string][]continualRehearsal // anchorID → most recent observations
	createdAt      time.Time
}

type continualCheckpoint struct {
	ID        string
	Payload   string
	At        time.Time
	Blessed   bool
}

type continualAnchor struct {
	ID       string
	Input    string
	Expected string
	Tol      float64
	AddedAt  time.Time
}

type continualRehearsal struct {
	ObservationID string
	Observed      string
	At            time.Time
	Pass          bool
	Error         float64 // |expected - observed| if numeric, else 0/1
}

// NewContinualLearning returns an empty registry.
func NewContinualLearning() *ContinualLearning {
	return &ContinualLearning{learners: map[string]*continualLearner{}}
}

// Checkpoint stores a named snapshot. payload is opaque to us; the
// calling primitive supplies whatever it needs to restore. If bless
// is true, this checkpoint becomes the DIVERGENCE reference.
func (c *ContinualLearning) Checkpoint(learnerID, checkpointID, payload string, bless bool) error {
	if learnerID == "" {
		return errors.New("learner_id required")
	}
	if checkpointID == "" {
		return errors.New("checkpoint_id required")
	}
	c.totalCheckpoints.Add(1)
	l := c.learnerOrCreate(learnerID)
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := continualCheckpoint{
		ID: checkpointID, Payload: payload,
		At: time.Now(), Blessed: bless,
	}
	if _, exists := l.checkpoints[checkpointID]; !exists {
		l.checkpointOrder = append(l.checkpointOrder, checkpointID)
	}
	l.checkpoints[checkpointID] = cp
	if bless {
		// Un-bless the prior blessed checkpoint
		if l.blessed != "" {
			if prev, ok := l.checkpoints[l.blessed]; ok {
				prev.Blessed = false
				l.checkpoints[l.blessed] = prev
			}
		}
		l.blessed = checkpointID
	}
	return nil
}

// Anchor registers a (input, expected, tol) gold-standard tuple.
func (c *ContinualLearning) Anchor(learnerID, anchorID, input, expected string, tol float64) error {
	if learnerID == "" || anchorID == "" {
		return errors.New("learner_id and anchor_id required")
	}
	if input == "" || expected == "" {
		return errors.New("input and expected required")
	}
	if tol < 0 {
		return errors.New("tolerance must be non-negative")
	}
	l := c.learnerOrCreate(learnerID)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.anchors[anchorID] = continualAnchor{
		ID: anchorID, Input: input, Expected: expected,
		Tol: tol, AddedAt: time.Now(),
	}
	return nil
}

// ContinualRehearseResult is REHEARSE's return.
type ContinualRehearseResult struct {
	Pass  bool    `json:"pass"`
	Error float64 `json:"error"`
}

// Rehearse posts the learner's current output for an anchor. Pass /
// error are computed using the anchor's tolerance (numeric anchors)
// or exact match (string anchors).
func (c *ContinualLearning) Rehearse(learnerID, observationID, anchorID, observed string) (ContinualRehearseResult, error) {
	if learnerID == "" || observationID == "" || anchorID == "" {
		return ContinualRehearseResult{}, errors.New("learner, observation, anchor ids required")
	}
	c.totalRehearses.Add(1)
	c.mu.RLock()
	l, ok := c.learners[learnerID]
	c.mu.RUnlock()
	if !ok {
		return ContinualRehearseResult{}, errors.New("unknown learner: " + learnerID)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.anchors[anchorID]
	if !ok {
		return ContinualRehearseResult{}, errors.New("unknown anchor: " + anchorID)
	}
	pass, errVal := evalAnchor(a, observed)
	r := continualRehearsal{
		ObservationID: observationID, Observed: observed,
		At: time.Now(), Pass: pass, Error: errVal,
	}
	l.rehearsals[anchorID] = append(l.rehearsals[anchorID], r)
	// Cap per-anchor observations to last 100
	if rs := l.rehearsals[anchorID]; len(rs) > 100 {
		l.rehearsals[anchorID] = rs[len(rs)-100:]
	}
	return ContinualRehearseResult{Pass: pass, Error: errVal}, nil
}

// ContinualDivergence is DIVERGENCE's return.
type ContinualDivergence struct {
	LearnerID         string  `json:"learner_id"`
	Anchors           int     `json:"anchors"`
	ObservationsSeen  int     `json:"observations_seen"`
	PassRate          float64 `json:"pass_rate"`
	MeanError         float64 `json:"mean_error"`
	BlessedCheckpoint string  `json:"blessed_checkpoint"`
	Verdict           string  `json:"verdict"`
	Reason            string  `json:"reason"`
}

// Divergence summarises the learner's current performance on the
// anchor set. We use the latest observation per anchor.
//
// Verdict thresholds:
//   - pass_rate >= 0.9                          → HEALTHY
//   - 0.7 <= pass_rate < 0.9                    → DRIFTING
//   - pass_rate < 0.7                           → FORGOTTEN
//   - < 5 anchor-observations                   → INSUFFICIENT
func (c *ContinualLearning) Divergence(learnerID string) (ContinualDivergence, bool) {
	c.mu.RLock()
	l, ok := c.learners[learnerID]
	c.mu.RUnlock()
	if !ok {
		return ContinualDivergence{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := ContinualDivergence{
		LearnerID: learnerID, Anchors: len(l.anchors),
		BlessedCheckpoint: l.blessed,
	}
	passes, total := 0, 0
	sumErr := 0.0
	for _, a := range l.anchors {
		rs := l.rehearsals[a.ID]
		if len(rs) == 0 {
			continue
		}
		last := rs[len(rs)-1]
		total++
		if last.Pass {
			passes++
		}
		sumErr += last.Error
	}
	out.ObservationsSeen = total
	if total == 0 {
		out.Verdict = "INSUFFICIENT"
		out.Reason = "no anchor observations yet"
		return out, true
	}
	if total < 5 {
		out.Verdict = "INSUFFICIENT"
		out.Reason = "need at least 5 anchor observations"
	}
	out.PassRate = float64(passes) / float64(total)
	out.MeanError = sumErr / float64(total)
	if out.Verdict == "" {
		switch {
		case out.PassRate >= 0.9:
			out.Verdict = "HEALTHY"
			out.Reason = "learner agrees with anchors"
		case out.PassRate >= 0.7:
			out.Verdict = "DRIFTING"
			out.Reason = "anchor pass-rate slipping — investigate before forgetting completes"
		default:
			out.Verdict = "FORGOTTEN"
			out.Reason = "anchor pass-rate below 70% — consider rollback"
		}
	}
	return out, true
}

// ContinualRollback is ROLLBACK's return — the payload to restore.
type ContinualRollback struct {
	LearnerID    string `json:"learner_id"`
	CheckpointID string `json:"checkpoint_id"`
	Payload      string `json:"payload"`
}

// Rollback returns the requested checkpoint's payload. The app
// applies it. checkpointID="" picks the blessed checkpoint.
func (c *ContinualLearning) Rollback(learnerID, checkpointID string) (ContinualRollback, error) {
	if learnerID == "" {
		return ContinualRollback{}, errors.New("learner_id required")
	}
	c.totalRollbacks.Add(1)
	c.mu.RLock()
	l, ok := c.learners[learnerID]
	c.mu.RUnlock()
	if !ok {
		return ContinualRollback{}, errors.New("unknown learner: " + learnerID)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if checkpointID == "" {
		checkpointID = l.blessed
	}
	if checkpointID == "" {
		return ContinualRollback{}, errors.New("no blessed checkpoint and no TO supplied")
	}
	cp, ok := l.checkpoints[checkpointID]
	if !ok {
		return ContinualRollback{}, errors.New("unknown checkpoint: " + checkpointID)
	}
	return ContinualRollback{
		LearnerID: learnerID, CheckpointID: checkpointID, Payload: cp.Payload,
	}, nil
}

// ContinualListRow is one row of LIST.
type ContinualListRow struct {
	LearnerID   string `json:"learner_id"`
	Checkpoints int    `json:"checkpoints"`
	Anchors     int    `json:"anchors"`
	Blessed     string `json:"blessed_checkpoint"`
}

// List enumerates learners (optionally filtered).
func (c *ContinualLearning) List(filter string) []ContinualListRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ContinualListRow, 0, len(c.learners))
	for _, l := range c.learners {
		l.mu.Lock()
		if filter != "" && l.id != filter {
			l.mu.Unlock()
			continue
		}
		out = append(out, ContinualListRow{
			LearnerID: l.id, Checkpoints: len(l.checkpoints),
			Anchors: len(l.anchors), Blessed: l.blessed,
		})
		l.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LearnerID < out[j].LearnerID })
	return out
}

// Forget drops a learner (or all).
func (c *ContinualLearning) Forget(learnerID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if learnerID == "ALL" {
		n := len(c.learners)
		c.learners = map[string]*continualLearner{}
		return n
	}
	if _, ok := c.learners[learnerID]; ok {
		delete(c.learners, learnerID)
		return 1
	}
	return 0
}

// ContinualStats is the global snapshot.
type ContinualStats struct {
	Learners         int   `json:"learners"`
	TotalCheckpoints int64 `json:"total_checkpoints"`
	TotalRehearses   int64 `json:"total_rehearses"`
	TotalRollbacks   int64 `json:"total_rollbacks"`
}

func (c *ContinualLearning) Stats() ContinualStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ContinualStats{
		Learners:         len(c.learners),
		TotalCheckpoints: c.totalCheckpoints.Load(),
		TotalRehearses:   c.totalRehearses.Load(),
		TotalRollbacks:   c.totalRollbacks.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (c *ContinualLearning) learnerOrCreate(id string) *continualLearner {
	c.mu.RLock()
	l, ok := c.learners[id]
	c.mu.RUnlock()
	if ok {
		return l
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.learners[id]; ok {
		return l
	}
	l = &continualLearner{
		id: id,
		checkpoints: map[string]continualCheckpoint{},
		anchors:     map[string]continualAnchor{},
		rehearsals:  map[string][]continualRehearsal{},
		createdAt:   time.Now(),
	}
	c.learners[id] = l
	return l
}

// evalAnchor compares observed to expected. If both parse as floats,
// pass = |observed - expected| <= tol and error is that magnitude.
// Otherwise it's exact-string match: pass = exact, error = 0/1.
func evalAnchor(a continualAnchor, observed string) (bool, float64) {
	expFloat, expOk := parseFloatLax(a.Expected)
	obsFloat, obsOk := parseFloatLax(observed)
	if expOk && obsOk {
		diff := math.Abs(expFloat - obsFloat)
		return diff <= a.Tol, diff
	}
	if a.Expected == observed {
		return true, 0
	}
	return false, 1
}

func parseFloatLax(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	// Hand-roll a forgiving parser to avoid importing strconv twice
	var f float64
	var frac float64 = 1
	seenDot := false
	neg := false
	start := 0
	if s[0] == '-' {
		neg = true
		start = 1
	}
	for i := start; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if seenDot {
				return 0, false
			}
			seenDot = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		d := float64(c - '0')
		if seenDot {
			frac *= 10
			f += d / frac
		} else {
			f = f*10 + d
		}
	}
	if neg {
		f = -f
	}
	return f, true
}
