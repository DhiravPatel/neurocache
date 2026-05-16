package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ReplayStore is the deterministic record-and-replay layer for
// non-deterministic agent runs — the debugging primitive that
// nobody ships and everyone in agent-stack land complains about.
//
// The setup: an agent run in prod failed badly. The chain is 18
// steps long, makes 6 LLM calls and 9 tool calls, picks routes via
// a bandit, samples with temperature > 0. You cannot reproduce it
// because re-running the same input gets different LLM outputs and
// the bandit picks something else.
//
// REPLAY.* fixes this by capturing every step's input + output
// keyed by (session, step, kind). In replay mode, REPLAY.NEXT
// feeds the *recorded* output back to the agent code instead of
// calling the upstream provider. The agent's logic re-executes
// deterministically against the same observations, exposing where
// the broken decision was made. REPLAY.DIFF compares two runs of
// the same session and surfaces the first divergence.
//
// Commands:
//
//   REPLAY.RECORD sess-id STEP n KIND llm|tool|route IN input OUT output
//        Append one step to the session's trace.
//   REPLAY.OPEN  sess-id
//        Mark the session as "replay mode" for subsequent NEXT calls.
//   REPLAY.NEXT  sess-id KIND k IN input
//        → recorded output for the next un-consumed step of that kind.
//        Errors with REPLAYDRIFT when the input doesn't match.
//   REPLAY.CLOSE sess-id
//        Exit replay mode. NEXT cursor resets on next OPEN.
//   REPLAY.DIFF  sess-a sess-b
//        → step-by-step divergence between two recorded sessions.
//   REPLAY.GET   sess-id [STEP n]
//        Full trace or one step.
//   REPLAY.EXPORT sess-id
//        Flat JSON bundle for a bug report.
//   REPLAY.RESET sess-id|ALL
//   REPLAY.STATS
//
// Hot path: RECORD is one append under a per-session lock. NEXT is
// one read + cursor bump. DIFF is O(min(len_a, len_b)).
type ReplayStore struct {
	mu       sync.RWMutex
	sessions map[string]*replaySession

	totalRecords atomic.Int64
	totalNexts   atomic.Int64
	totalDiffs   atomic.Int64
	totalDrifts  atomic.Int64
}

type replaySession struct {
	mu       sync.RWMutex
	steps    []replayStep
	open     bool
	cursor   int // next un-consumed step in replay mode
	createdAt int64
}

type replayStep struct {
	Step   int    `json:"step"`
	Kind   string `json:"kind"`
	In     string `json:"in"`
	Out    string `json:"out"`
	TS     int64  `json:"ts"`
}

// NewReplayStore returns an empty store.
func NewReplayStore() *ReplayStore {
	return &ReplayStore{sessions: map[string]*replaySession{}}
}

// Record appends one step to a session's trace. step must be
// strictly greater than the previously recorded step.
func (r *ReplayStore) Record(sessionID string, step int, kind, in, out string) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	if step < 0 {
		return errors.New("step must be non-negative")
	}
	if !validReplayKind(kind) {
		return errors.New("kind must be llm | tool | route")
	}
	r.totalRecords.Add(1)
	s := r.sessionOrCreate(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Enforce monotonic step ordering — same step replaces.
	if n := len(s.steps); n > 0 {
		last := s.steps[n-1].Step
		if step < last {
			return errors.New("step must be >= last recorded step")
		}
		if step == last {
			s.steps[n-1] = replayStep{
				Step: step, Kind: kind, In: in, Out: out, TS: time.Now().UnixNano(),
			}
			return nil
		}
	}
	s.steps = append(s.steps, replayStep{
		Step: step, Kind: kind, In: in, Out: out, TS: time.Now().UnixNano(),
	})
	return nil
}

// Open marks a session as in replay mode. NEXT starts from step 0.
func (r *ReplayStore) Open(sessionID string) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	r.mu.RLock()
	s, ok := r.sessions[sessionID]
	r.mu.RUnlock()
	if !ok {
		return errors.New("unknown session_id: " + sessionID)
	}
	s.mu.Lock()
	s.open = true
	s.cursor = 0
	s.mu.Unlock()
	return nil
}

// Close exits replay mode for a session.
func (r *ReplayStore) Close(sessionID string) {
	r.mu.RLock()
	s, ok := r.sessions[sessionID]
	r.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	s.open = false
	s.cursor = 0
	s.mu.Unlock()
}

// Next returns the next recorded output of the given kind. Errors
// with REPLAYDRIFT when the live input doesn't match the recorded
// input (caller's behavior diverged from the original run).
func (r *ReplayStore) Next(sessionID, kind, in string) (replayStep, error) {
	r.totalNexts.Add(1)
	if sessionID == "" {
		return replayStep{}, errors.New("session_id required")
	}
	if !validReplayKind(kind) {
		return replayStep{}, errors.New("kind must be llm | tool | route")
	}
	r.mu.RLock()
	s, ok := r.sessions[sessionID]
	r.mu.RUnlock()
	if !ok {
		return replayStep{}, errors.New("unknown session_id: " + sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return replayStep{}, errors.New("session not in replay mode — call REPLAY.OPEN first")
	}
	// Find next un-consumed step matching kind
	for s.cursor < len(s.steps) {
		st := s.steps[s.cursor]
		s.cursor++
		if st.Kind != kind {
			continue
		}
		if st.In != in {
			r.totalDrifts.Add(1)
			return st, errReplayDrift{step: st.Step, kind: kind, wantIn: st.In, gotIn: in}
		}
		return st, nil
	}
	return replayStep{}, errors.New("no more steps of kind " + kind)
}

// errReplayDrift is returned from Next when the caller's input
// diverges from the recorded input.
type errReplayDrift struct {
	step           int
	kind           string
	wantIn, gotIn  string
}

func (e errReplayDrift) Error() string {
	return "REPLAYDRIFT: caller input diverged at step " + itoaBenchPub(e.step) + " kind " + e.kind
}

// IsReplayDrift reports whether err is an errReplayDrift (so callers
// can branch on the specific drift case vs other errors).
func IsReplayDrift(err error) bool {
	_, ok := err.(errReplayDrift)
	return ok
}

// ReplayDiffRow is one divergence row in DIFF output.
type ReplayDiffRow struct {
	Step    int    `json:"step"`
	Kind    string `json:"kind"`
	Field   string `json:"field"`    // "in" | "out"
	A       string `json:"a"`
	B       string `json:"b"`
}

// Diff compares two sessions step-by-step and returns the rows
// where they diverge. Stops after the first 100 divergences.
func (r *ReplayStore) Diff(a, b string) ([]ReplayDiffRow, error) {
	r.totalDiffs.Add(1)
	r.mu.RLock()
	sa, okA := r.sessions[a]
	sb, okB := r.sessions[b]
	r.mu.RUnlock()
	if !okA {
		return nil, errors.New("unknown session_id: " + a)
	}
	if !okB {
		return nil, errors.New("unknown session_id: " + b)
	}
	sa.mu.RLock()
	defer sa.mu.RUnlock()
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	n := len(sa.steps)
	if len(sb.steps) < n {
		n = len(sb.steps)
	}
	out := make([]ReplayDiffRow, 0, 4)
	for i := 0; i < n; i++ {
		ia, ib := sa.steps[i], sb.steps[i]
		if ia.Kind != ib.Kind {
			out = append(out, ReplayDiffRow{Step: ia.Step, Kind: ia.Kind, Field: "kind", A: ia.Kind, B: ib.Kind})
		}
		if ia.In != ib.In {
			out = append(out, ReplayDiffRow{Step: ia.Step, Kind: ia.Kind, Field: "in", A: ia.In, B: ib.In})
		}
		if ia.Out != ib.Out {
			out = append(out, ReplayDiffRow{Step: ia.Step, Kind: ia.Kind, Field: "out", A: ia.Out, B: ib.Out})
		}
		if len(out) >= 100 {
			break
		}
	}
	// Trailing length mismatch
	if len(sa.steps) != len(sb.steps) {
		out = append(out, ReplayDiffRow{
			Step:  -1,
			Kind:  "length",
			Field: "n_steps",
			A:     itoaBenchPub(len(sa.steps)),
			B:     itoaBenchPub(len(sb.steps)),
		})
	}
	return out, nil
}

// ReplayStepRow is one trace row exposed via GET / EXPORT.
type ReplayStepRow struct {
	Step int    `json:"step"`
	Kind string `json:"kind"`
	In   string `json:"in"`
	Out  string `json:"out"`
	TS   int64  `json:"ts"`
}

// Get returns the full trace, or just one step if stepFilter >= 0.
func (r *ReplayStore) Get(sessionID string, stepFilter int) ([]ReplayStepRow, bool) {
	r.mu.RLock()
	s, ok := r.sessions[sessionID]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReplayStepRow, 0, len(s.steps))
	for _, st := range s.steps {
		if stepFilter >= 0 && st.Step != stepFilter {
			continue
		}
		out = append(out, ReplayStepRow{
			Step: st.Step, Kind: st.Kind, In: st.In, Out: st.Out,
			TS: st.TS / int64(time.Second),
		})
	}
	return out, true
}

// ReplayExport is the bundle returned from EXPORT.
type ReplayExport struct {
	SessionID  string          `json:"session_id"`
	CreatedAt  int64           `json:"created_at"`
	NSteps     int             `json:"n_steps"`
	Steps      []ReplayStepRow `json:"steps"`
}

// Export returns the full session trace as a bundle suitable for
// shipping with a bug report.
func (r *ReplayStore) Export(sessionID string) (ReplayExport, bool) {
	r.mu.RLock()
	s, ok := r.sessions[sessionID]
	r.mu.RUnlock()
	if !ok {
		return ReplayExport{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	steps := make([]ReplayStepRow, len(s.steps))
	for i, st := range s.steps {
		steps[i] = ReplayStepRow{
			Step: st.Step, Kind: st.Kind, In: st.In, Out: st.Out,
			TS: st.TS / int64(time.Second),
		}
	}
	return ReplayExport{
		SessionID: sessionID,
		CreatedAt: s.createdAt / int64(time.Second),
		NSteps:    len(s.steps),
		Steps:     steps,
	}, true
}

// Sessions returns every session id, sorted.
func (r *ReplayStore) Sessions() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.sessions))
	for k := range r.sessions {
		out = append(out, k)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a session. sessionID="ALL" wipes all.
func (r *ReplayStore) Reset(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sessionID == "ALL" {
		n := len(r.sessions)
		r.sessions = map[string]*replaySession{}
		return n
	}
	if _, ok := r.sessions[sessionID]; ok {
		delete(r.sessions, sessionID)
		return 1
	}
	return 0
}

// ReplayStats is the global snapshot.
type ReplayStats struct {
	Sessions     int   `json:"sessions"`
	TotalSteps   int   `json:"total_steps"`
	TotalRecords int64 `json:"total_records"`
	TotalNexts   int64 `json:"total_nexts"`
	TotalDiffs   int64 `json:"total_diffs"`
	TotalDrifts  int64 `json:"total_drifts"`
}

func (r *ReplayStore) Stats() ReplayStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	steps := 0
	for _, s := range r.sessions {
		s.mu.RLock()
		steps += len(s.steps)
		s.mu.RUnlock()
	}
	return ReplayStats{
		Sessions:     len(r.sessions),
		TotalSteps:   steps,
		TotalRecords: r.totalRecords.Load(),
		TotalNexts:   r.totalNexts.Load(),
		TotalDiffs:   r.totalDiffs.Load(),
		TotalDrifts:  r.totalDrifts.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (r *ReplayStore) sessionOrCreate(id string) *replaySession {
	r.mu.RLock()
	s, ok := r.sessions[id]
	r.mu.RUnlock()
	if ok {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok {
		return s
	}
	s = &replaySession{createdAt: time.Now().UnixNano()}
	r.sessions[id] = s
	return s
}

func validReplayKind(k string) bool {
	switch k {
	case "llm", "tool", "route":
		return true
	}
	return false
}

// itoaBenchPub mirrors the unexported itoaBench in bench files but is
// available outside _test.go contexts (used to format error messages).
func itoaBenchPub(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
