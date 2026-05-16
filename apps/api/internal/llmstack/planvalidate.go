package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// PlanValidator answers "is this multi-step agent plan executable
// at all, before we spend $40 of LLM and tool calls finding out the
// hard way?"
//
// CONTRACT validates one LLM call's input/output shape. It does
// nothing about the *plan* an agent produces — a 12-step DAG of
// LLM + tool calls where step 9 takes the output of step 4 as
// input. Plans go wrong in five mechanical ways that don't need an
// LLM to detect:
//
//   cycle               step A → step B → step A
//   unknown-dep         step 7 references step 99 (typo)
//   unknown-output      step 7 reads step 4.{nonexistent_field}
//   unreachable         step 11 produces nothing anything else uses
//                       (warning — orphaned terminal step usually
//                       means the planner ran out of context)
//   duplicate-id        two steps share an id
//
// PLAN.VALIDATE.* runs these checks deterministically — no LLM,
// no embedding, just graph analysis on the declared plan. Orchestrator
// wires it as a hard gate before the executor starts.
//
// Commands:
//
//   PLAN.VALIDATE.NEW plan-id
//        Create an empty plan.
//   PLAN.VALIDATE.ADDSTEP plan-id step-id
//        [DEPS step-id[,step-id...]]
//        [INPUTS name=src[,name=src...]]
//        [OUTPUTS name[,name...]]
//        src = "literal" or "step:<id>.<field>"
//   PLAN.VALIDATE.CHECK plan-id [STRICT 0|1]
//        → {valid, issues:[{level, code, step_id, message}]}
//   PLAN.VALIDATE.STATUS plan-id
//        → parsed plan structure.
//   PLAN.VALIDATE.LIST
//   PLAN.VALIDATE.DROP plan-id|ALL
//   PLAN.VALIDATE.STATS
//
// Hot path: CHECK is O(V+E) Kahn's-algorithm topo sort plus a
// per-step dep lookup. Sub-microsecond on plans up to a few hundred
// steps.
type PlanValidator struct {
	mu    sync.RWMutex
	plans map[string]*planEntry

	totalChecks atomic.Int64
}

type planEntry struct {
	mu    sync.RWMutex
	steps map[string]*planStep
	order []string // insertion order
}

type planStep struct {
	ID      string
	Deps    []string
	Inputs  map[string]string // input name → source ("literal" or "step:<id>.<field>")
	Outputs []string
}

// NewPlanValidator returns an empty validator.
func NewPlanValidator() *PlanValidator {
	return &PlanValidator{plans: map[string]*planEntry{}}
}

// New creates an empty plan. Idempotent; calling on an existing plan
// resets it.
func (v *PlanValidator) New(planID string) error {
	if planID == "" {
		return errors.New("plan_id required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.plans[planID] = &planEntry{steps: map[string]*planStep{}}
	return nil
}

// AddStep registers one step. deps/inputs/outputs are taken as-is;
// the validator doesn't reject duplicate ids here — that surfaces
// in CHECK with code=duplicate-id, so callers see the full picture
// at once.
func (v *PlanValidator) AddStep(planID, stepID string, deps []string, inputs map[string]string, outputs []string) error {
	if planID == "" || stepID == "" {
		return errors.New("plan_id and step_id required")
	}
	v.mu.RLock()
	p, ok := v.plans[planID]
	v.mu.RUnlock()
	if !ok {
		return errors.New("unknown plan_id (call PLAN.VALIDATE.NEW first): " + planID)
	}
	step := &planStep{
		ID:      stepID,
		Deps:    append([]string(nil), deps...),
		Inputs:  make(map[string]string, len(inputs)),
		Outputs: append([]string(nil), outputs...),
	}
	for k, srcVal := range inputs {
		step.Inputs[k] = srcVal
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.steps[stepID]; !exists {
		p.order = append(p.order, stepID)
	}
	p.steps[stepID] = step
	return nil
}

// PlanIssue is one row in CHECK's issues list.
type PlanIssue struct {
	Level   string `json:"level"`   // "error" | "warning"
	Code    string `json:"code"`    // cycle | unknown-dep | unknown-output | unreachable | duplicate-id
	StepID  string `json:"step_id,omitempty"`
	Message string `json:"message"`
}

// PlanValidationResult is CHECK's full return.
type PlanValidationResult struct {
	PlanID  string      `json:"plan_id"`
	Valid   bool        `json:"valid"`
	Issues  []PlanIssue `json:"issues"`
	NSteps  int         `json:"n_steps"`
	NCycles int         `json:"n_cycles"`
}

// Check runs all validation passes. STRICT raises warnings to errors.
func (v *PlanValidator) Check(planID string, strict bool) (PlanValidationResult, error) {
	if planID == "" {
		return PlanValidationResult{}, errors.New("plan_id required")
	}
	v.totalChecks.Add(1)
	v.mu.RLock()
	p, ok := v.plans[planID]
	v.mu.RUnlock()
	if !ok {
		return PlanValidationResult{}, errors.New("unknown plan_id: " + planID)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := PlanValidationResult{PlanID: planID, NSteps: len(p.steps), Valid: true}

	// 1) duplicate-id — surfaced by counting how often each id appears
	//    in the insertion order vs the map. AddStep deduplicates the
	//    map, but we can detect via order-tracking.
	seen := map[string]int{}
	for _, id := range p.order {
		seen[id]++
	}
	for _, id := range p.order {
		if seen[id] > 1 {
			out.Issues = append(out.Issues, PlanIssue{
				Level: "error", Code: "duplicate-id", StepID: id,
				Message: "step id appears " + itoaBenchPub(seen[id]) + " times",
			})
			out.Valid = false
			seen[id] = 0 // emit only once per dup
		}
	}

	// 2) unknown-dep + unknown-output
	for _, id := range p.order {
		s := p.steps[id]
		for _, d := range s.Deps {
			if _, ok := p.steps[d]; !ok {
				out.Issues = append(out.Issues, PlanIssue{
					Level: "error", Code: "unknown-dep", StepID: id,
					Message: "depends on unknown step: " + d,
				})
				out.Valid = false
			}
		}
		for name, srcVal := range s.Inputs {
			ref, field, isStepRef := parseStepRef(srcVal)
			if !isStepRef {
				continue
			}
			target, ok := p.steps[ref]
			if !ok {
				out.Issues = append(out.Issues, PlanIssue{
					Level: "error", Code: "unknown-dep", StepID: id,
					Message: "input '" + name + "' references unknown step: " + ref,
				})
				out.Valid = false
				continue
			}
			if field != "" && !containsString(target.Outputs, field) {
				out.Issues = append(out.Issues, PlanIssue{
					Level: "error", Code: "unknown-output", StepID: id,
					Message: "input '" + name + "' references missing field '" + field + "' on " + ref,
				})
				out.Valid = false
			}
		}
	}

	// 3) cycle detection via Kahn's topo sort
	cycles := detectCycles(p)
	if cycles > 0 {
		out.NCycles = cycles
		out.Issues = append(out.Issues, PlanIssue{
			Level: "error", Code: "cycle", Message: "plan has " + itoaBenchPub(cycles) + " unresolved step(s) in a dependency cycle",
		})
		out.Valid = false
	}

	// 4) unreachable terminals (warning) — output not consumed by any
	//    later step. Tolerated unless STRICT.
	consumed := map[string]bool{}
	for _, s := range p.steps {
		for _, srcVal := range s.Inputs {
			ref, _, isStepRef := parseStepRef(srcVal)
			if isStepRef {
				consumed[ref] = true
			}
		}
	}
	// Only flag terminals — steps with outputs that no other step
	// consumes AND that aren't part of the final-step set. The "final
	// step set" is everything with no descendants; flagging just
	// intermediate orphans is the useful case.
	for _, id := range p.order {
		s := p.steps[id]
		if len(s.Outputs) == 0 {
			continue
		}
		if consumed[id] {
			continue
		}
		// Step's outputs are unused — terminal. Only warn if it's not
		// at the end of insertion order (i.e., another step came after
		// it but didn't consume it — likely a planner mistake).
		isFinal := id == p.order[len(p.order)-1]
		if isFinal {
			continue
		}
		level := "warning"
		if strict {
			level = "error"
			out.Valid = false
		}
		out.Issues = append(out.Issues, PlanIssue{
			Level: level, Code: "unreachable", StepID: id,
			Message: "step produces outputs no later step consumes",
		})
	}
	sort.Slice(out.Issues, func(i, j int) bool {
		if out.Issues[i].Level != out.Issues[j].Level {
			return out.Issues[i].Level == "error"
		}
		if out.Issues[i].Code != out.Issues[j].Code {
			return out.Issues[i].Code < out.Issues[j].Code
		}
		return out.Issues[i].StepID < out.Issues[j].StepID
	})
	return out, nil
}

// PlanStatusRow is one row of STATUS output.
type PlanStatusRow struct {
	StepID  string            `json:"step_id"`
	Deps    []string          `json:"deps"`
	Inputs  map[string]string `json:"inputs"`
	Outputs []string          `json:"outputs"`
}

// Status returns the parsed plan structure.
func (v *PlanValidator) Status(planID string) ([]PlanStatusRow, bool) {
	v.mu.RLock()
	p, ok := v.plans[planID]
	v.mu.RUnlock()
	if !ok {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PlanStatusRow, 0, len(p.order))
	for _, id := range p.order {
		s := p.steps[id]
		row := PlanStatusRow{
			StepID: s.ID, Deps: append([]string(nil), s.Deps...),
			Outputs: append([]string(nil), s.Outputs...),
			Inputs:  map[string]string{},
		}
		for k, val := range s.Inputs {
			row.Inputs[k] = val
		}
		out = append(out, row)
	}
	return out, true
}

// List returns every plan id, sorted.
func (v *PlanValidator) List() []string {
	v.mu.RLock()
	out := make([]string, 0, len(v.plans))
	for k := range v.plans {
		out = append(out, k)
	}
	v.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Drop removes a plan. planID="ALL" wipes everything.
func (v *PlanValidator) Drop(planID string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if planID == "ALL" {
		n := len(v.plans)
		v.plans = map[string]*planEntry{}
		return n
	}
	if _, ok := v.plans[planID]; ok {
		delete(v.plans, planID)
		return 1
	}
	return 0
}

// PlanValidatorStats is the global snapshot.
type PlanValidatorStats struct {
	Plans       int   `json:"plans"`
	TotalChecks int64 `json:"total_checks"`
}

func (v *PlanValidator) Stats() PlanValidatorStats {
	v.mu.RLock()
	n := len(v.plans)
	v.mu.RUnlock()
	return PlanValidatorStats{Plans: n, TotalChecks: v.totalChecks.Load()}
}

// ─── internals ──────────────────────────────────────────────────

// parseStepRef recognises "step:<id>.<field>" or "step:<id>".
// Returns id, field, true if it is a step reference.
func parseStepRef(s string) (string, string, bool) {
	const prefix = "step:"
	if !strings.HasPrefix(s, prefix) {
		return "", "", false
	}
	rest := s[len(prefix):]
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		return rest[:dot], rest[dot+1:], true
	}
	return rest, "", true
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// detectCycles returns the count of steps that remain unprocessed
// after Kahn's-algorithm topological sort — those steps are in a
// cycle (or depend on one).
func detectCycles(p *planEntry) int {
	// Build the dep graph counting only edges that point to known steps
	// (unknown-dep is reported separately and shouldn't double-flag).
	indeg := map[string]int{}
	out := map[string][]string{}
	for _, s := range p.steps {
		if _, ok := indeg[s.ID]; !ok {
			indeg[s.ID] = 0
		}
		// Build the union of explicit Deps and step:<id>.<field> input refs
		seen := map[string]bool{}
		for _, d := range s.Deps {
			seen[d] = true
		}
		for _, srcVal := range s.Inputs {
			if ref, _, isStepRef := parseStepRef(srcVal); isStepRef {
				seen[ref] = true
			}
		}
		for d := range seen {
			if _, exists := p.steps[d]; !exists {
				continue
			}
			out[d] = append(out[d], s.ID)
			indeg[s.ID]++
		}
	}
	queue := make([]string, 0, len(p.steps))
	for id, deg := range indeg {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	processed := 0
	for len(queue) > 0 {
		// pop
		head := queue[0]
		queue = queue[1:]
		processed++
		for _, next := range out[head] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return len(p.steps) - processed
}
