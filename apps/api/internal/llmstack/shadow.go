package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ShadowEval is the risk-averse cousin of ANSWER.CANARY.
//
// ANSWER.CANARY serves the candidate variant to N% of real users.
// Healthcare, finance, legal, and regulated B2B can't do that —
// shipping an unproven prompt to real customer outcomes is a
// compliance event. Shadow evaluation lets them learn whether the
// new prompt/model is better *without exposing a single user*:
//
//   100% of prod traffic flows to the baseline (served to user).
//   100% of the same traffic is mirrored to the candidate
//     (the candidate response is never returned).
//   Both outputs are scored offline; we surface the lift.
//
// Because the two variants see the *same input*, paired-comparison
// statistics are tighter than two independent samples — fewer
// observations needed for a confident decision. SHADOW.REPORT also
// returns per-input regressions (cases where the candidate scored
// notably worse), which is the bit teams actually care about: "is
// the new prompt better on average, AND do the worst cases stay
// inside acceptable bounds?"
//
// Commands:
//
//   SHADOW.CONFIG exp-id [BASELINE name] [CANDIDATE name]
//        [REGRESSION_THRESHOLD f] [SAMPLE_RATE f]
//        Default regression threshold = 0.20 (candidate must score
//        at least 0.20 less to count as a regression). Default
//        sample_rate = 1.0 (mirror everything).
//   SHADOW.MIRROR exp-id req-id input
//        Reserve a request id; the caller is about to run both
//        variants offline.
//   SHADOW.RECORD exp-id req-id BASELINE q CANDIDATE q
//        [LATENCY_BASELINE_MS n] [LATENCY_CANDIDATE_MS n]
//        Log paired outcomes.
//   SHADOW.REPORT exp-id [REGRESSION_LIMIT n]
//        → {n, win_rate_candidate, mean_lift, latency_lift_ms,
//           regressions:[{req_id, baseline, candidate, diff}, ...]}
//   SHADOW.PROMOTE exp-id [RATE f]
//        Returns the ANSWER.CANARY config you'd ship to graduate
//        the experiment (rate defaults to 0.10).
//   SHADOW.RESET exp-id
//        Clears results, preserves config.
//   SHADOW.LIST
//   SHADOW.STATS
//
// Hot path: MIRROR is one map insert; RECORD updates a paired-Welford
// accumulator on baseline and candidate plus a per-regression slice.
// REPORT is O(stored regressions); a strict-mode app caps at the
// top-N worst.
type ShadowEval struct {
	mu  sync.RWMutex
	exp map[string]*shadowExp

	totalMirrors atomic.Int64
	totalRecords atomic.Int64
}

type shadowExp struct {
	mu                  sync.RWMutex
	baselineName        string
	candidateName       string
	regressionThreshold float64
	sampleRate          float64
	reserved            map[string]string // req_id → input (for MIRROR audit)
	baselineN           int64
	candidateN          int64
	baselineQualitySum  float64
	candidateQualitySum float64
	baselineQualityM2   float64
	candidateQualityM2  float64
	baselineLatencySum  int64
	candidateLatencySum int64
	candidateWins       int64
	regressions         []shadowRegression
	createdAt           int64
}

type shadowRegression struct {
	ReqID    string
	Baseline float64
	Candidate float64
	Diff     float64 // candidate - baseline (negative for regressions)
	TS       int64
}

// NewShadowEval returns an empty store.
func NewShadowEval() *ShadowEval {
	return &ShadowEval{exp: map[string]*shadowExp{}}
}

// Configure creates / updates an experiment. Empty string keeps prior;
// negative regressionThreshold or sampleRate leaves them unchanged.
func (s *ShadowEval) Configure(expID, baseline, candidate string, regressionThreshold, sampleRate float64) error {
	if expID == "" {
		return errors.New("experiment id required")
	}
	if regressionThreshold > 0 && (regressionThreshold > 1) {
		return errors.New("regression_threshold must be in (0,1]")
	}
	if sampleRate > 0 && sampleRate > 1 {
		return errors.New("sample_rate must be in (0,1]")
	}
	s.mu.Lock()
	e, ok := s.exp[expID]
	if !ok {
		e = &shadowExp{
			regressionThreshold: 0.20,
			sampleRate:          1.0,
			reserved:            map[string]string{},
			createdAt:           time.Now().UnixNano(),
		}
		s.exp[expID] = e
	}
	s.mu.Unlock()
	e.mu.Lock()
	if baseline != "" {
		e.baselineName = baseline
	}
	if candidate != "" {
		e.candidateName = candidate
	}
	if regressionThreshold > 0 {
		e.regressionThreshold = regressionThreshold
	}
	if sampleRate > 0 {
		e.sampleRate = sampleRate
	}
	e.mu.Unlock()
	return nil
}

// Mirror reserves a request id and stores the input. The app is
// expected to run both variants offline, then call Record. Returns
// "skip" when sample_rate would drop the request.
func (s *ShadowEval) Mirror(expID, reqID, input string) (string, error) {
	if expID == "" {
		return "", errors.New("experiment id required")
	}
	if reqID == "" {
		return "", errors.New("request id required")
	}
	s.totalMirrors.Add(1)
	s.mu.RLock()
	e, ok := s.exp[expID]
	s.mu.RUnlock()
	if !ok {
		return "", errors.New("unknown experiment id (call SHADOW.CONFIG first): " + expID)
	}
	e.mu.Lock()
	rate := e.sampleRate
	e.mu.Unlock()
	if rate < 1.0 {
		// Deterministic per-request sampling: hash(req_id) within rate
		if float64(fnv1a32(reqID)%10_000)/10_000.0 >= rate {
			return "skip", nil
		}
	}
	e.mu.Lock()
	e.reserved[reqID] = input
	e.mu.Unlock()
	return "mirror", nil
}

// Record logs paired baseline + candidate outcomes for one request.
// Both quality scores must be in [0,1]. Reservation is consumed.
func (s *ShadowEval) Record(expID, reqID string, baselineQ, candidateQ float64, latencyBaseMS, latencyCandMS int64) error {
	if expID == "" {
		return errors.New("experiment id required")
	}
	if reqID == "" {
		return errors.New("request id required")
	}
	if baselineQ < 0 || baselineQ > 1 || candidateQ < 0 || candidateQ > 1 {
		return errors.New("quality must be in [0,1]")
	}
	if latencyBaseMS < 0 || latencyCandMS < 0 {
		return errors.New("latency must be non-negative")
	}
	s.totalRecords.Add(1)
	s.mu.RLock()
	e, ok := s.exp[expID]
	s.mu.RUnlock()
	if !ok {
		return errors.New("unknown experiment id: " + expID)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// Welford updates per variant
	e.baselineN++
	dBase := baselineQ - (e.baselineQualitySum / float64(e.baselineN-1+1))
	_ = dBase // kept for clarity; we use the direct running mean below
	e.baselineQualitySum += baselineQ
	meanB := e.baselineQualitySum / float64(e.baselineN)
	e.baselineQualityM2 += (baselineQ - meanB) * (baselineQ - meanB)
	e.baselineLatencySum += latencyBaseMS

	e.candidateN++
	e.candidateQualitySum += candidateQ
	meanC := e.candidateQualitySum / float64(e.candidateN)
	e.candidateQualityM2 += (candidateQ - meanC) * (candidateQ - meanC)
	e.candidateLatencySum += latencyCandMS

	diff := candidateQ - baselineQ
	if diff > 0 {
		e.candidateWins++
	}
	if -diff >= e.regressionThreshold {
		// Cap regressions buffer to last 500 to bound memory
		if len(e.regressions) >= 500 {
			e.regressions = e.regressions[1:]
		}
		e.regressions = append(e.regressions, shadowRegression{
			ReqID: reqID, Baseline: baselineQ, Candidate: candidateQ,
			Diff: diff, TS: time.Now().UnixNano(),
		})
	}
	delete(e.reserved, reqID)
	return nil
}

// ShadowReport is REPORT's full return.
type ShadowReport struct {
	ExperimentID         string                  `json:"experiment_id"`
	BaselineName         string                  `json:"baseline_name"`
	CandidateName        string                  `json:"candidate_name"`
	N                    int64                   `json:"n"`
	WinRateCandidate     float64                 `json:"win_rate_candidate"`
	MeanLift             float64                 `json:"mean_lift"`
	BaselineMean         float64                 `json:"baseline_mean"`
	CandidateMean        float64                 `json:"candidate_mean"`
	BaselineStddev       float64                 `json:"baseline_stddev"`
	CandidateStddev      float64                 `json:"candidate_stddev"`
	LatencyLiftMS        float64                 `json:"latency_lift_ms"`
	RegressionsCount     int                     `json:"regressions_count"`
	Regressions          []ShadowRegressionRow   `json:"regressions"`
}

// ShadowRegressionRow is one row of REPORT.regressions.
type ShadowRegressionRow struct {
	ReqID    string  `json:"req_id"`
	Baseline float64 `json:"baseline"`
	Candidate float64 `json:"candidate"`
	Diff     float64 `json:"diff"`
	TS       int64   `json:"ts"`
}

// Report returns the experiment summary with the worst regressions.
func (s *ShadowEval) Report(expID string, regressionLimit int) (ShadowReport, error) {
	s.mu.RLock()
	e, ok := s.exp[expID]
	s.mu.RUnlock()
	if !ok {
		return ShadowReport{}, errors.New("unknown experiment id: " + expID)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	n := e.baselineN
	if e.candidateN < n {
		n = e.candidateN
	}
	r := ShadowReport{
		ExperimentID:     expID,
		BaselineName:     e.baselineName,
		CandidateName:    e.candidateName,
		N:                n,
		RegressionsCount: len(e.regressions),
	}
	if e.baselineN > 0 {
		r.BaselineMean = e.baselineQualitySum / float64(e.baselineN)
	}
	if e.candidateN > 0 {
		r.CandidateMean = e.candidateQualitySum / float64(e.candidateN)
	}
	r.MeanLift = r.CandidateMean - r.BaselineMean
	if e.baselineN > 1 {
		r.BaselineStddev = math.Sqrt(e.baselineQualityM2 / float64(e.baselineN-1))
	}
	if e.candidateN > 1 {
		r.CandidateStddev = math.Sqrt(e.candidateQualityM2 / float64(e.candidateN-1))
	}
	if n > 0 {
		r.WinRateCandidate = float64(e.candidateWins) / float64(n)
	}
	if e.baselineN > 0 && e.candidateN > 0 {
		baseAvg := float64(e.baselineLatencySum) / float64(e.baselineN)
		candAvg := float64(e.candidateLatencySum) / float64(e.candidateN)
		r.LatencyLiftMS = candAvg - baseAvg
	}
	// Worst regressions: smallest diff first (most negative).
	regs := make([]shadowRegression, len(e.regressions))
	copy(regs, e.regressions)
	sort.Slice(regs, func(i, j int) bool { return regs[i].Diff < regs[j].Diff })
	if regressionLimit > 0 && len(regs) > regressionLimit {
		regs = regs[:regressionLimit]
	}
	r.Regressions = make([]ShadowRegressionRow, len(regs))
	for i, x := range regs {
		r.Regressions[i] = ShadowRegressionRow{
			ReqID: x.ReqID, Baseline: x.Baseline, Candidate: x.Candidate,
			Diff: x.Diff, TS: x.TS / int64(time.Second),
		}
	}
	return r, nil
}

// ShadowPromotion is PROMOTE's return — the ANSWER.CANARY config the
// app would ship.
type ShadowPromotion struct {
	ExperimentID  string  `json:"experiment_id"`
	BaselineName  string  `json:"baseline_name"`
	CandidateName string  `json:"candidate_name"`
	SuggestedRate float64 `json:"suggested_rate"`
	BasedOnN      int64   `json:"based_on_n"`
	MeanLift      float64 `json:"mean_lift"`
	Verdict       string  `json:"verdict"` // ready | hold | not_recommended
	Reason        string  `json:"reason"`
}

// Promote returns the ANSWER.CANARY config the app would ship.
// Suggested rate defaults to 0.10; caller may override.
func (s *ShadowEval) Promote(expID string, rate float64) (ShadowPromotion, error) {
	rep, err := s.Report(expID, 0)
	if err != nil {
		return ShadowPromotion{}, err
	}
	if rate <= 0 || rate > 1 {
		rate = 0.10
	}
	out := ShadowPromotion{
		ExperimentID:  expID,
		BaselineName:  rep.BaselineName,
		CandidateName: rep.CandidateName,
		SuggestedRate: rate,
		BasedOnN:      rep.N,
		MeanLift:      rep.MeanLift,
	}
	const minSamples = 100
	switch {
	case rep.N < minSamples:
		out.Verdict = "hold"
		out.Reason = "fewer than 100 paired samples — keep mirroring"
	case rep.MeanLift < 0:
		out.Verdict = "not_recommended"
		out.Reason = "candidate underperforms baseline in shadow"
	case rep.MeanLift < 0.02:
		out.Verdict = "hold"
		out.Reason = "lift below ship threshold (≥ 2 %)"
	default:
		out.Verdict = "ready"
		out.Reason = "candidate beats baseline with usable lift; ship to ANSWER.CANARY"
	}
	return out, nil
}

// Reset drops paired observations + regressions; config preserved.
func (s *ShadowEval) Reset(expID string) bool {
	s.mu.RLock()
	e, ok := s.exp[expID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	e.mu.Lock()
	e.baselineN, e.candidateN = 0, 0
	e.baselineQualitySum, e.candidateQualitySum = 0, 0
	e.baselineQualityM2, e.candidateQualityM2 = 0, 0
	e.baselineLatencySum, e.candidateLatencySum = 0, 0
	e.candidateWins = 0
	e.regressions = nil
	e.reserved = map[string]string{}
	e.mu.Unlock()
	return true
}

// List returns every experiment id, sorted.
func (s *ShadowEval) List() []string {
	s.mu.RLock()
	out := make([]string, 0, len(s.exp))
	for k := range s.exp {
		out = append(out, k)
	}
	s.mu.RUnlock()
	sort.Strings(out)
	return out
}

// ShadowStats is the global snapshot.
type ShadowStats struct {
	Experiments  int   `json:"experiments"`
	TotalMirrors int64 `json:"total_mirrors"`
	TotalRecords int64 `json:"total_records"`
}

func (s *ShadowEval) Stats() ShadowStats {
	s.mu.RLock()
	n := len(s.exp)
	s.mu.RUnlock()
	return ShadowStats{
		Experiments:  n,
		TotalMirrors: s.totalMirrors.Load(),
		TotalRecords: s.totalRecords.Load(),
	}
}
