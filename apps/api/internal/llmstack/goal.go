package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// GoalTracker watches whether agent runs are actually making
// progress toward their goal, not just burning steps. AGENTLOOP.*
// counts steps / tool_calls / tokens — useful for budget caps but
// blind to "the agent is making 30 tool calls and getting nowhere."
// GOAL.* answers the harder question by tracking semantic progress
// + semantic stagnation:
//
//   progress    = cosine(goal_embedding, latest_update_embedding)
//                 — how aligned current state is with the goal
//   stagnation  = "the last N updates look semantically identical
//                  to each other" — the agent is in a loop
//   stalled_steps = how many consecutive updates have shown no
//                  diversity (caller can gate on this for early
//                  termination)
//
// Commands:
//
//   GOAL.SET session-id goal-text
//   GOAL.PROGRESS session-id update-text
//        Records one progress observation.
//   GOAL.CHECK session-id
//        → [progress, stagnation, stalled_steps, hint, total_updates]
//        hint: "progress" | "stalled" | "loop" | "complete" | "unset"
//   GOAL.STATUS session-id   → full snapshot
//   GOAL.HISTORY session-id [LIMIT n]  → recent updates
//   GOAL.FORGET session-id
//   GOAL.STATS
//
// Hot path: CHECK is O(window) cosines over the recent updates
// — at typical window=8 × 128-dim that's ~1 µs. Apps call it
// every few agent steps to decide whether to early-terminate.
type GoalTracker struct {
	mu       sync.RWMutex
	sessions map[string]*goalSession

	totalSets       atomic.Int64
	totalProgresses atomic.Int64
	totalChecks     atomic.Int64
	totalLoops      atomic.Int64
}

type goalSession struct {
	mu        sync.RWMutex
	goalText  string
	goalVec   []float64
	updates   []goalUpdate
	startedAt int64
}

type goalUpdate struct {
	text string
	vec  []float64
	ts   int64
}

// NewGoalTracker returns an empty tracker.
func NewGoalTracker() *GoalTracker {
	return &GoalTracker{sessions: map[string]*goalSession{}}
}

// Set registers (or replaces) the goal for a session.
func (g *GoalTracker) Set(sessionID, goalText string) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	if goalText == "" {
		return errors.New("goal text required")
	}
	g.totalSets.Add(1)
	vec := embedFallback(goalText)
	g.mu.Lock()
	g.sessions[sessionID] = &goalSession{
		goalText:  goalText,
		goalVec:   vec,
		startedAt: time.Now().UnixNano(),
	}
	g.mu.Unlock()
	return nil
}

// Progress records one progress observation against an existing goal.
func (g *GoalTracker) Progress(sessionID, updateText string) error {
	if updateText == "" {
		return errors.New("update text required")
	}
	g.totalProgresses.Add(1)
	g.mu.RLock()
	s, ok := g.sessions[sessionID]
	g.mu.RUnlock()
	if !ok {
		return errors.New("unknown session_id (call GOAL.SET first): " + sessionID)
	}
	vec := embedFallback(updateText)
	s.mu.Lock()
	s.updates = append(s.updates, goalUpdate{
		text: updateText, vec: vec, ts: time.Now().UnixNano(),
	})
	// Soft cap: keep last 200 updates per session
	if len(s.updates) > 200 {
		s.updates = s.updates[len(s.updates)-200:]
	}
	s.mu.Unlock()
	return nil
}

// GoalCheckResult is what CHECK returns.
type GoalCheckResult struct {
	Progress      float64 `json:"progress"`       // cosine(goal, latest)
	Stagnation    bool    `json:"stagnation"`     // recent updates look identical
	StalledSteps  int     `json:"stalled_steps"`  // consecutive non-diverse updates
	Hint          string  `json:"hint"`           // progress | stalled | loop | complete | unset
	TotalUpdates  int     `json:"total_updates"`
}

// Check returns the per-session progress / stagnation analysis.
//
// Heuristics:
//   - progress     = cosine(goal, latest update). 0..1.
//   - stagnation   = max pairwise cosine over last 4 updates >= 0.92
//   - stalled_steps = how many of the trailing updates have cosine
//                     >= 0.92 to the one before them
//   - hint:
//       "unset"     — no goal set
//       "complete"  — progress >= 0.80 (agent has reached the goal)
//       "loop"      — stalled_steps >= 4 (clearly in a loop)
//       "stalled"   — stalled_steps >= 2
//       "progress"  — otherwise
func (g *GoalTracker) Check(sessionID string) (GoalCheckResult, bool) {
	g.totalChecks.Add(1)
	g.mu.RLock()
	s, ok := g.sessions[sessionID]
	g.mu.RUnlock()
	if !ok {
		return GoalCheckResult{Hint: "unset"}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := GoalCheckResult{TotalUpdates: len(s.updates)}
	if len(s.updates) == 0 {
		out.Hint = "progress" // goal set but no updates yet
		return out, true
	}

	last := s.updates[len(s.updates)-1]
	out.Progress = dotProduct(s.goalVec, last.vec)

	// Count trailing stalled steps: consecutive pairs with cosine ≥ 0.92
	const stallSim = 0.92
	stalled := 0
	for i := len(s.updates) - 1; i > 0; i-- {
		sim := dotProduct(s.updates[i].vec, s.updates[i-1].vec)
		if sim >= stallSim {
			stalled++
		} else {
			break
		}
	}
	out.StalledSteps = stalled

	// Stagnation flag: any pair in the last 4 has cosine >= 0.92
	start := len(s.updates) - 4
	if start < 0 {
		start = 0
	}
	for i := start; i < len(s.updates); i++ {
		for j := i + 1; j < len(s.updates); j++ {
			if dotProduct(s.updates[i].vec, s.updates[j].vec) >= stallSim {
				out.Stagnation = true
				break
			}
		}
		if out.Stagnation {
			break
		}
	}

	switch {
	case out.Progress >= 0.80:
		out.Hint = "complete"
	case out.StalledSteps >= 4:
		out.Hint = "loop"
		g.totalLoops.Add(1)
	case out.StalledSteps >= 2:
		out.Hint = "stalled"
	default:
		out.Hint = "progress"
	}
	return out, true
}

// GoalStatusResult is GOAL.STATUS's full snapshot.
type GoalStatusResult struct {
	SessionID    string  `json:"session_id"`
	Goal         string  `json:"goal"`
	StartedAt    int64   `json:"started_at_unix"`
	TotalUpdates int     `json:"total_updates"`
	LatestUpdate string  `json:"latest_update,omitempty"`
	Progress     float64 `json:"progress"`
	Hint         string  `json:"hint"`
}

// Status returns the full per-session snapshot.
func (g *GoalTracker) Status(sessionID string) (GoalStatusResult, bool) {
	g.mu.RLock()
	s, ok := g.sessions[sessionID]
	g.mu.RUnlock()
	if !ok {
		return GoalStatusResult{}, false
	}
	c, _ := g.Check(sessionID) // reuses the analysis
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := GoalStatusResult{
		SessionID:    sessionID,
		Goal:         s.goalText,
		StartedAt:    s.startedAt / int64(time.Second),
		TotalUpdates: len(s.updates),
		Progress:     c.Progress,
		Hint:         c.Hint,
	}
	if len(s.updates) > 0 {
		out.LatestUpdate = s.updates[len(s.updates)-1].text
	}
	return out, true
}

// HistoryRow is one row of GOAL.HISTORY.
type GoalHistoryRow struct {
	TS   int64  `json:"ts"`
	Text string `json:"text"`
}

// History returns recent updates (newest last). limit=0 returns all.
func (g *GoalTracker) History(sessionID string, limit int) []GoalHistoryRow {
	g.mu.RLock()
	s, ok := g.sessions[sessionID]
	g.mu.RUnlock()
	if !ok {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	updates := s.updates
	if limit > 0 && limit < len(updates) {
		updates = updates[len(updates)-limit:]
	}
	out := make([]GoalHistoryRow, len(updates))
	for i, u := range updates {
		out[i] = GoalHistoryRow{TS: u.ts / int64(time.Second), Text: u.text}
	}
	return out
}

// Forget drops a session entirely.
func (g *GoalTracker) Forget(sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.sessions[sessionID]
	delete(g.sessions, sessionID)
	return ok
}

// GoalStats is the global snapshot.
type GoalStats struct {
	Sessions        int   `json:"sessions"`
	TotalSets       int64 `json:"total_sets"`
	TotalProgresses int64 `json:"total_progresses"`
	TotalChecks     int64 `json:"total_checks"`
	TotalLoops      int64 `json:"total_loops_detected"`
}

func (g *GoalTracker) Stats() GoalStats {
	g.mu.RLock()
	n := len(g.sessions)
	g.mu.RUnlock()
	return GoalStats{
		Sessions:        n,
		TotalSets:       g.totalSets.Load(),
		TotalProgresses: g.totalProgresses.Load(),
		TotalChecks:     g.totalChecks.Load(),
		TotalLoops:      g.totalLoops.Load(),
	}
}

// Sessions returns every active session id, sorted.
func (g *GoalTracker) Sessions() []string {
	g.mu.RLock()
	out := make([]string, 0, len(g.sessions))
	for id := range g.sessions {
		out = append(out, id)
	}
	g.mu.RUnlock()
	sort.Strings(out)
	return out
}
