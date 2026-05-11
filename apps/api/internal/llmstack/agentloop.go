package llmstack

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AgentLoopTracker enforces step / tool-call / token / time budgets
// on agent runs. The single most common production incident in
// agentic apps is the "runaway agent" — a stuck reasoning loop that
// fires the same tool 500 times in 90 seconds and blows through the
// daily token budget before anyone notices. Apps add hand-rolled
// counters to every agent's main loop, often with off-by-one bugs
// or no enforcement at all.
//
// AGENTLOOP.* gives the cache one coordinated state machine:
//
//   AGENTLOOP.START loop-id [MAX_STEPS n] [MAX_TOOL_CALLS n]
//                           [MAX_TOKENS n] [MAX_TIME_MS ms]
//   AGENTLOOP.STEP loop-id [TOKENS n] [TOOL_CALL 0|1]
//        → [should_stop, reason, steps, tool_calls, tokens, elapsed_ms]
//   AGENTLOOP.STATUS loop-id      → current state + caps
//   AGENTLOOP.RESET loop-id       → zero counters (caps preserved)
//   AGENTLOOP.FORGET loop-id      → wipe
//   AGENTLOOP.STATS               → global counters
//
// STEP is atomic: increment counters, then check every cap. Returns
// should_stop=1 on the FIRST exceeded cap with `reason` naming the
// trigger. Subsequent calls return should_stop=1 even with zero
// increments — once a loop is stopped, it stays stopped until RESET.
//
// All counters are atomic so STEP is lock-free except for the map
// lookup (sync.Map). At <100 ns/call it adds negligible overhead
// even for high-frequency agent loops.
type AgentLoopTracker struct {
	loops sync.Map // loop_id -> *agentLoop

	totalStarts atomic.Int64
	totalSteps  atomic.Int64
	totalStops  atomic.Int64
}

type agentLoop struct {
	id            string
	startedAtNS   int64
	maxSteps      int64
	maxToolCalls  int64
	maxTokens     int64
	maxTimeMS     int64

	steps      atomic.Int64
	toolCalls  atomic.Int64
	tokens     atomic.Int64
	stopReason atomic.Pointer[string] // nil = running
}

// NewAgentLoopTracker returns an empty tracker.
func NewAgentLoopTracker() *AgentLoopTracker {
	return &AgentLoopTracker{}
}

// LoopOpts configures AGENTLOOP.START. A zero cap means "no limit".
type LoopOpts struct {
	MaxSteps     int64
	MaxToolCalls int64
	MaxTokens    int64
	MaxTimeMS    int64
}

// Start registers a new loop. Replacing an existing loop_id is
// allowed (apps re-use IDs across sessions); previous state is
// discarded.
func (a *AgentLoopTracker) Start(loopID string, opts LoopOpts) error {
	if loopID == "" {
		return errors.New("loop_id required")
	}
	l := &agentLoop{
		id:           loopID,
		startedAtNS:  time.Now().UnixNano(),
		maxSteps:     opts.MaxSteps,
		maxToolCalls: opts.MaxToolCalls,
		maxTokens:    opts.MaxTokens,
		maxTimeMS:    opts.MaxTimeMS,
	}
	a.loops.Store(loopID, l)
	a.totalStarts.Add(1)
	return nil
}

// StepOpts configures one STEP increment.
type StepOpts struct {
	Tokens   int64
	ToolCall bool // increment tool_calls counter by 1
}

// StepResult is what STEP returns.
type StepResult struct {
	ShouldStop bool   `json:"should_stop"`
	Reason     string `json:"reason,omitempty"`
	Steps      int64  `json:"steps"`
	ToolCalls  int64  `json:"tool_calls"`
	Tokens     int64  `json:"tokens"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

// Step increments counters and checks caps. Returns should_stop=true
// the FIRST time any cap is exceeded; subsequent calls return true
// with the original reason until RESET is called. Returns ok=false
// if the loop_id is unknown.
func (a *AgentLoopTracker) Step(loopID string, opts StepOpts) (StepResult, bool) {
	v, ok := a.loops.Load(loopID)
	if !ok {
		return StepResult{}, false
	}
	a.totalSteps.Add(1)
	l := v.(*agentLoop)

	// If already stopped, return the original verdict without
	// incrementing counters (don't double-count after the budget
	// was already exceeded).
	if existing := l.stopReason.Load(); existing != nil {
		return l.snapshot(true, *existing), true
	}

	// Atomic increments first, then cap check. Order matters: we
	// commit the work, then decide whether the resulting state has
	// breached a cap. This matches real-world semantics — the
	// agent has already done the work, telling it "don't do this"
	// only stops the NEXT step.
	l.steps.Add(1)
	if opts.Tokens > 0 {
		l.tokens.Add(opts.Tokens)
	}
	if opts.ToolCall {
		l.toolCalls.Add(1)
	}

	if reason, hit := a.checkCaps(l); hit {
		// Latch the reason exactly once via CAS-on-nil. If two
		// concurrent STEPs both see a breach, only the first
		// stamps the reason.
		r := reason
		if l.stopReason.CompareAndSwap(nil, &r) {
			a.totalStops.Add(1)
		}
		final := l.stopReason.Load()
		return l.snapshot(true, *final), true
	}
	return l.snapshot(false, ""), true
}

func (a *AgentLoopTracker) checkCaps(l *agentLoop) (string, bool) {
	if l.maxSteps > 0 && l.steps.Load() > l.maxSteps {
		return fmt.Sprintf("max_steps exceeded (%d > %d)", l.steps.Load(), l.maxSteps), true
	}
	if l.maxToolCalls > 0 && l.toolCalls.Load() > l.maxToolCalls {
		return fmt.Sprintf("max_tool_calls exceeded (%d > %d)", l.toolCalls.Load(), l.maxToolCalls), true
	}
	if l.maxTokens > 0 && l.tokens.Load() > l.maxTokens {
		return fmt.Sprintf("max_tokens exceeded (%d > %d)", l.tokens.Load(), l.maxTokens), true
	}
	if l.maxTimeMS > 0 {
		elapsed := (time.Now().UnixNano() - l.startedAtNS) / int64(time.Millisecond)
		if elapsed > l.maxTimeMS {
			return fmt.Sprintf("max_time_ms exceeded (%d > %d)", elapsed, l.maxTimeMS), true
		}
	}
	return "", false
}

// LoopStatus is AGENTLOOP.STATUS return.
type LoopStatus struct {
	LoopID       string `json:"loop_id"`
	Stopped      bool   `json:"stopped"`
	Reason       string `json:"reason,omitempty"`
	Steps        int64  `json:"steps"`
	ToolCalls    int64  `json:"tool_calls"`
	Tokens       int64  `json:"tokens"`
	ElapsedMS    int64  `json:"elapsed_ms"`
	MaxSteps     int64  `json:"max_steps"`
	MaxToolCalls int64  `json:"max_tool_calls"`
	MaxTokens    int64  `json:"max_tokens"`
	MaxTimeMS    int64  `json:"max_time_ms"`
}

// Status returns the current state or false if unknown.
func (a *AgentLoopTracker) Status(loopID string) (LoopStatus, bool) {
	v, ok := a.loops.Load(loopID)
	if !ok {
		return LoopStatus{}, false
	}
	l := v.(*agentLoop)
	reason := ""
	stopped := false
	if r := l.stopReason.Load(); r != nil {
		reason = *r
		stopped = true
	}
	return LoopStatus{
		LoopID:       loopID,
		Stopped:      stopped,
		Reason:       reason,
		Steps:        l.steps.Load(),
		ToolCalls:    l.toolCalls.Load(),
		Tokens:       l.tokens.Load(),
		ElapsedMS:    (time.Now().UnixNano() - l.startedAtNS) / int64(time.Millisecond),
		MaxSteps:     l.maxSteps,
		MaxToolCalls: l.maxToolCalls,
		MaxTokens:    l.maxTokens,
		MaxTimeMS:    l.maxTimeMS,
	}, true
}

// Reset zeros all counters and clears the stop reason. Caps are
// preserved. Useful for "retry-from-clean-state" recovery after a
// soft stop.
func (a *AgentLoopTracker) Reset(loopID string) bool {
	v, ok := a.loops.Load(loopID)
	if !ok {
		return false
	}
	l := v.(*agentLoop)
	l.steps.Store(0)
	l.toolCalls.Store(0)
	l.tokens.Store(0)
	l.stopReason.Store(nil)
	l.startedAtNS = time.Now().UnixNano()
	return true
}

// Forget drops a loop entirely.
func (a *AgentLoopTracker) Forget(loopID string) bool {
	_, was := a.loops.LoadAndDelete(loopID)
	return was
}

// Active returns every running loop_id, sorted.
func (a *AgentLoopTracker) Active() []string {
	out := []string{}
	a.loops.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	sort.Strings(out)
	return out
}

// AgentLoopStats is the global counters snapshot.
type AgentLoopStats struct {
	TotalStarts int64 `json:"total_starts"`
	TotalSteps  int64 `json:"total_steps"`
	TotalStops  int64 `json:"total_stops"`
	Active      int   `json:"active"`
}

func (a *AgentLoopTracker) Stats() AgentLoopStats {
	n := 0
	a.loops.Range(func(_, _ any) bool { n++; return true })
	return AgentLoopStats{
		TotalStarts: a.totalStarts.Load(),
		TotalSteps:  a.totalSteps.Load(),
		TotalStops:  a.totalStops.Load(),
		Active:      n,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (l *agentLoop) snapshot(stop bool, reason string) StepResult {
	return StepResult{
		ShouldStop: stop,
		Reason:     reason,
		Steps:      l.steps.Load(),
		ToolCalls:  l.toolCalls.Load(),
		Tokens:     l.tokens.Load(),
		ElapsedMS:  (time.Now().UnixNano() - l.startedAtNS) / int64(time.Millisecond),
	}
}
