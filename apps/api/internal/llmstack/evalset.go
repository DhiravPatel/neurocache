package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// EvalSetStore is the versioned-golden-set + regression-diff layer
// every team rebuilds.
//
// JUDGE runs cases live against a single model. EVALSET.* is the
// CI gate: you FREEZE a snapshot of cases (immutable from then on),
// RECORD a model's per-case scores, and DIFF two model runs over
// the same frozen version to surface exactly which cases regressed
// vs improved between v1 and v2 of the prompt or model.
//
// Why a frozen snapshot matters: any team that runs evals against a
// "current set of cases" with no version pin will silently change
// the cases between runs and never be able to attribute a regression
// to the model change vs the eval change. FREEZE pins the cases so
// the only variable is the model.
//
// Commands:
//
//   EVALSET.CREATE  eval-id
//        Open a new eval set in "draft" mode (cases can be added).
//   EVALSET.ADDCASE eval-id case-id input [EXPECTED expected]
//        Add a case to the draft. EXPECTED is optional — many evals
//        score by an LLM-judge rubric and have no canonical expected.
//   EVALSET.FREEZE  eval-id version-tag
//        Snapshot current draft cases as an immutable version. Cases
//        added after FREEZE are part of the *next* version, not this
//        one.
//   EVALSET.RECORD  eval-id version case-id model-tag SCORE q
//        [OUTPUT out]
//        Score one case under one model. q ∈ [0,1].
//   EVALSET.DIFF    eval-id version model-a model-b
//        → {regressions, improvements, no_change, new_failures,
//           newly_passing, total_a, total_b, delta_mean}
//   EVALSET.STATUS  eval-id [VERSION v]
//        Versions / case counts / models run.
//   EVALSET.LIST
//   EVALSET.DROP    eval-id|ALL
//   EVALSET.STATS
//
// Hot path: RECORD is one map insert under a per-version lock. DIFF
// is O(cases) per model pair — single-digit microseconds for ~hundreds
// of cases.
type EvalSetStore struct {
	mu    sync.RWMutex
	sets  map[string]*evalSet

	totalAdds    atomic.Int64
	totalFreezes atomic.Int64
	totalRecords atomic.Int64
	totalDiffs   atomic.Int64
}

type evalSet struct {
	mu       sync.RWMutex
	draft    map[string]evalCase    // case_id → case (mutable)
	versions map[string]*evalVersion // version_tag → frozen snapshot
}

type evalCase struct {
	ID       string
	Input    string
	Expected string
}

type evalVersion struct {
	mu       sync.RWMutex
	tag      string
	frozen   map[string]evalCase
	order    []string                       // case insertion order
	runs     map[string]map[string]evalRun  // model_tag → case_id → run
	frozenAt int64
}

type evalRun struct {
	Score  float64
	Output string
	TS     int64
}

// NewEvalSetStore returns an empty store.
func NewEvalSetStore() *EvalSetStore {
	return &EvalSetStore{sets: map[string]*evalSet{}}
}

// Create opens a new eval set. Idempotent — calling on an existing id
// preserves prior cases/versions (use DROP to reset).
func (e *EvalSetStore) Create(evalID string) error {
	if evalID == "" {
		return errors.New("eval_id required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.sets[evalID]; ok {
		return nil
	}
	e.sets[evalID] = &evalSet{
		draft:    map[string]evalCase{},
		versions: map[string]*evalVersion{},
	}
	return nil
}

// AddCase appends a case to the draft. Re-adding the same case_id
// replaces it (you're iterating on the eval before freezing).
func (e *EvalSetStore) AddCase(evalID, caseID, input, expected string) error {
	if evalID == "" || caseID == "" {
		return errors.New("eval_id and case_id required")
	}
	e.totalAdds.Add(1)
	e.mu.RLock()
	s, ok := e.sets[evalID]
	e.mu.RUnlock()
	if !ok {
		return errors.New("unknown eval_id (call EVALSET.CREATE first): " + evalID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draft[caseID] = evalCase{ID: caseID, Input: input, Expected: expected}
	return nil
}

// Freeze snapshots the draft as an immutable version.
func (e *EvalSetStore) Freeze(evalID, versionTag string) error {
	if evalID == "" || versionTag == "" {
		return errors.New("eval_id and version_tag required")
	}
	e.totalFreezes.Add(1)
	e.mu.RLock()
	s, ok := e.sets[evalID]
	e.mu.RUnlock()
	if !ok {
		return errors.New("unknown eval_id: " + evalID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.versions[versionTag]; exists {
		return errors.New("version already frozen: " + versionTag)
	}
	if len(s.draft) == 0 {
		return errors.New("draft is empty; add cases before freezing")
	}
	frozen := make(map[string]evalCase, len(s.draft))
	order := make([]string, 0, len(s.draft))
	for id, c := range s.draft {
		frozen[id] = c
		order = append(order, id)
	}
	sort.Strings(order)
	s.versions[versionTag] = &evalVersion{
		tag:      versionTag,
		frozen:   frozen,
		order:    order,
		runs:     map[string]map[string]evalRun{},
		frozenAt: time.Now().UnixNano(),
	}
	return nil
}

// Record stores one model's score for one case under one version.
func (e *EvalSetStore) Record(evalID, versionTag, caseID, modelTag string, score float64, output string) error {
	if evalID == "" || versionTag == "" || caseID == "" || modelTag == "" {
		return errors.New("eval_id, version_tag, case_id, model_tag required")
	}
	if score < 0 || score > 1 {
		return errors.New("score must be in [0,1]")
	}
	e.totalRecords.Add(1)
	e.mu.RLock()
	s, ok := e.sets[evalID]
	e.mu.RUnlock()
	if !ok {
		return errors.New("unknown eval_id: " + evalID)
	}
	s.mu.RLock()
	v, ok := s.versions[versionTag]
	s.mu.RUnlock()
	if !ok {
		return errors.New("unknown version_tag (call EVALSET.FREEZE first): " + versionTag)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.frozen[caseID]; !ok {
		return errors.New("case_id not part of frozen version: " + caseID)
	}
	if v.runs[modelTag] == nil {
		v.runs[modelTag] = map[string]evalRun{}
	}
	v.runs[modelTag][caseID] = evalRun{
		Score: score, Output: output, TS: time.Now().UnixNano(),
	}
	return nil
}

// EvalDiffRow is one regression / improvement row.
type EvalDiffRow struct {
	CaseID  string  `json:"case_id"`
	ScoreA  float64 `json:"score_a"`
	ScoreB  float64 `json:"score_b"`
	Delta   float64 `json:"delta"` // b - a
}

// EvalDiffResult is DIFF's return.
type EvalDiffResult struct {
	EvalID        string        `json:"eval_id"`
	Version       string        `json:"version"`
	ModelA        string        `json:"model_a"`
	ModelB        string        `json:"model_b"`
	Regressions   []EvalDiffRow `json:"regressions"`    // sorted worst→best
	Improvements  []EvalDiffRow `json:"improvements"`   // sorted best→worst
	NoChange      int           `json:"no_change"`
	NewFailures   []string      `json:"new_failures"`   // case_ids that pass (>=0.5) in A but fail in B
	NewlyPassing  []string      `json:"newly_passing"`  // case_ids that fail in A but pass in B
	TotalA        int           `json:"total_a"`
	TotalB        int           `json:"total_b"`
	DeltaMean     float64       `json:"delta_mean"`
}

// Diff compares two model runs over the same frozen version.
// regressionThreshold defaults to 0.05 (5-point drop counts).
func (e *EvalSetStore) Diff(evalID, versionTag, modelA, modelB string) (EvalDiffResult, error) {
	e.totalDiffs.Add(1)
	if evalID == "" || versionTag == "" || modelA == "" || modelB == "" {
		return EvalDiffResult{}, errors.New("eval_id, version_tag, model_a, model_b required")
	}
	e.mu.RLock()
	s, ok := e.sets[evalID]
	e.mu.RUnlock()
	if !ok {
		return EvalDiffResult{}, errors.New("unknown eval_id: " + evalID)
	}
	s.mu.RLock()
	v, ok := s.versions[versionTag]
	s.mu.RUnlock()
	if !ok {
		return EvalDiffResult{}, errors.New("unknown version_tag: " + versionTag)
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	runsA, okA := v.runs[modelA]
	runsB, okB := v.runs[modelB]
	if !okA {
		return EvalDiffResult{}, errors.New("no runs recorded for model: " + modelA)
	}
	if !okB {
		return EvalDiffResult{}, errors.New("no runs recorded for model: " + modelB)
	}
	out := EvalDiffResult{
		EvalID: evalID, Version: versionTag,
		ModelA: modelA, ModelB: modelB,
		TotalA: len(runsA), TotalB: len(runsB),
	}
	const regThreshold = 0.05
	const passThreshold = 0.50
	var deltaSum float64
	var n int
	for _, caseID := range v.order {
		rA, hasA := runsA[caseID]
		rB, hasB := runsB[caseID]
		if !hasA || !hasB {
			continue
		}
		delta := rB.Score - rA.Score
		deltaSum += delta
		n++
		row := EvalDiffRow{CaseID: caseID, ScoreA: rA.Score, ScoreB: rB.Score, Delta: delta}
		switch {
		case delta <= -regThreshold:
			out.Regressions = append(out.Regressions, row)
		case delta >= regThreshold:
			out.Improvements = append(out.Improvements, row)
		default:
			out.NoChange++
		}
		passA := rA.Score >= passThreshold
		passB := rB.Score >= passThreshold
		if passA && !passB {
			out.NewFailures = append(out.NewFailures, caseID)
		} else if !passA && passB {
			out.NewlyPassing = append(out.NewlyPassing, caseID)
		}
	}
	if n > 0 {
		out.DeltaMean = deltaSum / float64(n)
	}
	sort.Slice(out.Regressions, func(i, j int) bool { return out.Regressions[i].Delta < out.Regressions[j].Delta })
	sort.Slice(out.Improvements, func(i, j int) bool { return out.Improvements[i].Delta > out.Improvements[j].Delta })
	sort.Strings(out.NewFailures)
	sort.Strings(out.NewlyPassing)
	return out, nil
}

// EvalSetStatus is STATUS's return.
type EvalSetStatus struct {
	EvalID     string             `json:"eval_id"`
	DraftCases int                `json:"draft_cases"`
	Versions   []EvalVersionRow   `json:"versions"`
}

// EvalVersionRow is one row of STATUS.versions.
type EvalVersionRow struct {
	Version   string   `json:"version"`
	Cases     int      `json:"cases"`
	Models    []string `json:"models"`
	FrozenAt  int64    `json:"frozen_at"`
}

// Status returns per-set version snapshot.
func (e *EvalSetStore) Status(evalID string) (EvalSetStatus, bool) {
	e.mu.RLock()
	s, ok := e.sets[evalID]
	e.mu.RUnlock()
	if !ok {
		return EvalSetStatus{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := EvalSetStatus{EvalID: evalID, DraftCases: len(s.draft)}
	tags := make([]string, 0, len(s.versions))
	for t := range s.versions {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	for _, t := range tags {
		v := s.versions[t]
		v.mu.RLock()
		models := make([]string, 0, len(v.runs))
		for m := range v.runs {
			models = append(models, m)
		}
		sort.Strings(models)
		out.Versions = append(out.Versions, EvalVersionRow{
			Version: t, Cases: len(v.frozen),
			Models: models, FrozenAt: v.frozenAt / int64(time.Second),
		})
		v.mu.RUnlock()
	}
	return out, true
}

// List returns every eval id, sorted.
func (e *EvalSetStore) List() []string {
	e.mu.RLock()
	out := make([]string, 0, len(e.sets))
	for k := range e.sets {
		out = append(out, k)
	}
	e.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Drop removes an eval. evalID="ALL" wipes everything.
func (e *EvalSetStore) Drop(evalID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if evalID == "ALL" {
		n := len(e.sets)
		e.sets = map[string]*evalSet{}
		return n
	}
	if _, ok := e.sets[evalID]; ok {
		delete(e.sets, evalID)
		return 1
	}
	return 0
}

// EvalSetStats is the global snapshot.
type EvalSetStats struct {
	Sets         int   `json:"sets"`
	TotalAdds    int64 `json:"total_adds"`
	TotalFreezes int64 `json:"total_freezes"`
	TotalRecords int64 `json:"total_records"`
	TotalDiffs   int64 `json:"total_diffs"`
}

func (e *EvalSetStore) Stats() EvalSetStats {
	e.mu.RLock()
	n := len(e.sets)
	e.mu.RUnlock()
	return EvalSetStats{
		Sets:         n,
		TotalAdds:    e.totalAdds.Load(),
		TotalFreezes: e.totalFreezes.Load(),
		TotalRecords: e.totalRecords.Load(),
		TotalDiffs:   e.totalDiffs.Load(),
	}
}
