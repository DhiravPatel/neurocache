package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ChainStateMgr is a multi-step workflow state machine with per-step
// artifact storage. Different from AGENTLOOP (which is just budgets)
// — this is the resumable-workflow primitive every agentic app
// reinvents:
//
//   - Define a chain (sequence of named steps) once.
//   - Start a run; each step completes by storing its artifact.
//   - RESUME after a crash returns the next pending step + every
//     artifact produced so far.
//   - Status tracking (running / complete / failed) with reason.
//
// The pain it solves: today, agentic frameworks lose all
// intermediate state on crash. The agent re-plans the whole task
// from scratch, often producing different (or worse) artifacts the
// second time around. CHAINSTATE.* lets a worker pick up exactly
// where it left off — with every prior step's output already
// available.
//
// Commands:
//
//   CHAINSTATE.DEFINE chain-id step1 step2 step3 ...
//   CHAINSTATE.START run-id chain-id          → starts a run
//   CHAINSTATE.DONE run-id step-name artifact → marks step complete
//   CHAINSTATE.FAIL run-id step-name reason   → fails the run
//   CHAINSTATE.RESUME run-id
//        → {next_step, step_idx, total_steps, artifacts: {...}}
//   CHAINSTATE.ARTIFACT run-id step-name      → just one artifact
//   CHAINSTATE.STATUS run-id                  → full state
//   CHAINSTATE.RUNS chain-id [STATUS ...]    → list runs
//   CHAINSTATE.FORGET run-id
//   CHAINSTATE.FORGET_CHAIN chain-id          → drop a chain
//                                                + all its runs
//   CHAINSTATE.STATS
//
// All increments via atomics; per-run state behind a small mutex.
// RESUME is O(1) — just reads the current step_idx.
type ChainStateMgr struct {
	mu     sync.RWMutex
	chains map[string]*chainDef
	runs   map[string]*chainRun

	totalRuns      atomic.Int64
	totalCompletes atomic.Int64
	totalFails     atomic.Int64
	totalSteps     atomic.Int64
}

type chainDef struct {
	id    string
	steps []string
	stepIdx map[string]int // step_name -> position
}

type chainRun struct {
	mu        sync.Mutex
	id        string
	chainID   string
	stepIdx   int      // next step to run
	status    string   // running | complete | failed
	reason    string
	artifacts map[string]string // step_name -> artifact
	startedAt int64
	updatedAt int64
}

// NewChainStateMgr returns an empty manager.
func NewChainStateMgr() *ChainStateMgr {
	return &ChainStateMgr{
		chains: map[string]*chainDef{},
		runs:   map[string]*chainRun{},
	}
}

// Define registers (or replaces) a chain by id. Steps must be
// non-empty and distinct.
func (c *ChainStateMgr) Define(chainID string, steps []string) error {
	if chainID == "" {
		return errors.New("chain_id required")
	}
	if len(steps) == 0 {
		return errors.New("at least one step required")
	}
	idx := map[string]int{}
	for i, s := range steps {
		if s == "" {
			return errors.New("step name cannot be empty")
		}
		if _, dup := idx[s]; dup {
			return errors.New("duplicate step name: " + s)
		}
		idx[s] = i
	}
	c.mu.Lock()
	c.chains[chainID] = &chainDef{id: chainID, steps: append([]string(nil), steps...), stepIdx: idx}
	c.mu.Unlock()
	return nil
}

// Start kicks off a run. Replacing an existing run-id resets state.
func (c *ChainStateMgr) Start(runID, chainID string) error {
	if runID == "" {
		return errors.New("run_id required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.chains[chainID]
	if !ok {
		return errors.New("unknown chain_id: " + chainID)
	}
	now := time.Now().Unix()
	c.runs[runID] = &chainRun{
		id:        runID,
		chainID:   chainID,
		status:    "running",
		artifacts: map[string]string{},
		startedAt: now,
		updatedAt: now,
	}
	c.totalRuns.Add(1)
	return nil
}

// DoneResult is what Done returns to the caller.
type DoneResult struct {
	NextStep   string `json:"next_step,omitempty"`
	StepIdx    int    `json:"step_idx"`
	TotalSteps int    `json:"total_steps"`
	Status     string `json:"status"`
}

// Done marks a step complete and advances the run. The step name
// must be the CURRENT step (out-of-order completion fails). Returns
// the next step + status (or status=complete if no more steps).
func (c *ChainStateMgr) Done(runID, stepName, artifact string) (DoneResult, error) {
	c.mu.RLock()
	run, ok := c.runs[runID]
	c.mu.RUnlock()
	if !ok {
		return DoneResult{}, errors.New("unknown run_id: " + runID)
	}
	c.mu.RLock()
	chain := c.chains[run.chainID]
	c.mu.RUnlock()
	if chain == nil {
		return DoneResult{}, errors.New("chain definition missing — was it forgotten?")
	}

	run.mu.Lock()
	defer run.mu.Unlock()
	if run.status != "running" {
		return DoneResult{}, errors.New("run is " + run.status + ", cannot advance")
	}
	if run.stepIdx >= len(chain.steps) {
		return DoneResult{}, errors.New("all steps already complete")
	}
	expected := chain.steps[run.stepIdx]
	if expected != stepName {
		return DoneResult{}, errors.New("expected step '" + expected + "' but got '" + stepName + "'")
	}
	run.artifacts[stepName] = artifact
	run.stepIdx++
	run.updatedAt = time.Now().Unix()
	c.totalSteps.Add(1)

	result := DoneResult{
		StepIdx:    run.stepIdx,
		TotalSteps: len(chain.steps),
		Status:     run.status,
	}
	if run.stepIdx >= len(chain.steps) {
		run.status = "complete"
		result.Status = "complete"
		c.totalCompletes.Add(1)
	} else {
		result.NextStep = chain.steps[run.stepIdx]
	}
	return result, nil
}

// Fail marks the run failed with a caller-supplied reason. Idempotent.
func (c *ChainStateMgr) Fail(runID, stepName, reason string) error {
	c.mu.RLock()
	run, ok := c.runs[runID]
	c.mu.RUnlock()
	if !ok {
		return errors.New("unknown run_id: " + runID)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.status == "complete" || run.status == "failed" {
		return nil
	}
	run.status = "failed"
	run.reason = reason
	run.updatedAt = time.Now().Unix()
	c.totalFails.Add(1)
	return nil
}

// ResumeResult is what Resume returns to a worker recovering after
// a crash.
type ResumeResult struct {
	RunID      string            `json:"run_id"`
	ChainID    string            `json:"chain_id"`
	NextStep   string            `json:"next_step,omitempty"`
	StepIdx    int               `json:"step_idx"`
	TotalSteps int               `json:"total_steps"`
	Status     string            `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Artifacts  map[string]string `json:"artifacts"`
}

// Resume returns the run state so a worker can pick up where the
// last process left off. Includes every prior artifact.
func (c *ChainStateMgr) Resume(runID string) (ResumeResult, bool) {
	c.mu.RLock()
	run, ok := c.runs[runID]
	c.mu.RUnlock()
	if !ok {
		return ResumeResult{}, false
	}
	c.mu.RLock()
	chain := c.chains[run.chainID]
	c.mu.RUnlock()

	run.mu.Lock()
	defer run.mu.Unlock()
	out := ResumeResult{
		RunID:     runID,
		ChainID:   run.chainID,
		StepIdx:   run.stepIdx,
		Status:    run.status,
		Reason:    run.reason,
		Artifacts: copyStringMap(run.artifacts),
	}
	if chain != nil {
		out.TotalSteps = len(chain.steps)
		if run.stepIdx < len(chain.steps) && run.status == "running" {
			out.NextStep = chain.steps[run.stepIdx]
		}
	}
	return out, true
}

// Artifact returns one step's artifact or false.
func (c *ChainStateMgr) Artifact(runID, stepName string) (string, bool) {
	c.mu.RLock()
	run, ok := c.runs[runID]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	v, ok := run.artifacts[stepName]
	return v, ok
}

// RunRow is one row of RUNS.
type ChainRunRow struct {
	RunID     string `json:"run_id"`
	ChainID   string `json:"chain_id"`
	Status    string `json:"status"`
	StepIdx   int    `json:"step_idx"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Runs returns every run for a chain, optionally filtered by status.
func (c *ChainStateMgr) Runs(chainID, statusFilter string) []ChainRunRow {
	c.mu.RLock()
	out := make([]ChainRunRow, 0)
	for _, run := range c.runs {
		if run.chainID != chainID {
			continue
		}
		run.mu.Lock()
		status := run.status
		row := ChainRunRow{
			RunID: run.id, ChainID: run.chainID, Status: status,
			StepIdx: run.stepIdx, StartedAt: run.startedAt, UpdatedAt: run.updatedAt,
		}
		run.mu.Unlock()
		if statusFilter != "" && !strings.EqualFold(status, statusFilter) {
			continue
		}
		out = append(out, row)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// Forget drops a single run.
func (c *ChainStateMgr) Forget(runID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.runs[runID]
	delete(c.runs, runID)
	return ok
}

// ForgetChain drops a chain definition + every run under it. Returns
// (chain-existed, runs-dropped).
func (c *ChainStateMgr) ForgetChain(chainID string) (bool, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.chains[chainID]
	delete(c.chains, chainID)
	dropped := 0
	for id, run := range c.runs {
		if run.chainID == chainID {
			delete(c.runs, id)
			dropped++
		}
	}
	return ok, dropped
}

// ChainStateStats is the global snapshot.
type ChainStateStats struct {
	Chains         int   `json:"chains"`
	ActiveRuns     int   `json:"active_runs"`
	TotalRuns      int64 `json:"total_runs"`
	TotalCompletes int64 `json:"total_completes"`
	TotalFails     int64 `json:"total_fails"`
	TotalSteps     int64 `json:"total_steps"`
}

func (c *ChainStateMgr) Stats() ChainStateStats {
	c.mu.RLock()
	chains := len(c.chains)
	active := 0
	for _, r := range c.runs {
		r.mu.Lock()
		if r.status == "running" {
			active++
		}
		r.mu.Unlock()
	}
	c.mu.RUnlock()
	return ChainStateStats{
		Chains:         chains,
		ActiveRuns:     active,
		TotalRuns:      c.totalRuns.Load(),
		TotalCompletes: c.totalCompletes.Load(),
		TotalFails:     c.totalFails.Load(),
		TotalSteps:     c.totalSteps.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
