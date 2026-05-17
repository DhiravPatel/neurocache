package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// QuorumGates is the N-of-M agent approval gate for side-effecting
// autonomous actions. Distinct from DEBATE (which is deliberation):
// QUORUM gates *commitment*. "No single agent can wire $10k; needs
// 2-of-3 agent sign-off." A commit gate for autonomous actions where
// the cost of a wrong commit is high enough that one agent shouldn't
// own the decision.
//
// Lifecycle: PROPOSE → (APPROVE | REJECT)* → COMMIT (if quorum met)
//                                          ↓
//                                       EXPIRE (deadline)
//
// Each gate is parameterised by:
//   - quorum_n     — how many APPROVALs needed (e.g. 2)
//   - voters       — explicit allow-list (so a random agent can't
//                    sign off)
//   - deadline     — gate auto-rejects after this time
//   - one_per_agent — true (default): each agent can only approve once
//                     within the same proposal
//
// Commands:
//
//   QUORUM.PROPOSE gate-id action-payload QUORUM n VOTERS a1,a2,...
//        [DEADLINE ms]
//   QUORUM.APPROVE gate-id agent [REASON r]
//   QUORUM.REJECT  gate-id agent [REASON r]
//        A REJECT from any allowed voter immediately fails the gate
//        unless explicitly disabled (we don't expose the override —
//        it's safer to require everyone agree by default).
//   QUORUM.COMMIT gate-id        — confirm + lock in the action.
//        Errors if quorum unmet.
//   QUORUM.STATUS gate-id        → state, approvals, deadline
//   QUORUM.LIST [STATE s]
//   QUORUM.FORGET gate-id|ALL
//   QUORUM.STATS
//
// The hot path: every mutation is O(voters) (typically a handful).
type QuorumGates struct {
	mu    sync.RWMutex
	gates map[string]*quorumGate

	totalProposals atomic.Int64
	totalApprovals atomic.Int64
	totalRejects   atomic.Int64
	totalCommits   atomic.Int64
}

type quorumGate struct {
	mu        sync.Mutex
	id        string
	payload   string
	quorumN   int
	voters    map[string]bool
	deadline  time.Time
	state     string // pending, committed, rejected, expired
	approvals map[string]quorumVote
	rejects   map[string]quorumVote
	createdAt time.Time
}

type quorumVote struct {
	Agent  string
	Reason string
	At     time.Time
}

// NewQuorumGates returns an empty registry.
func NewQuorumGates() *QuorumGates {
	return &QuorumGates{gates: map[string]*quorumGate{}}
}

// Propose opens a new gate.
func (q *QuorumGates) Propose(id, payload string, quorumN int, voters []string, deadline time.Duration) error {
	if id == "" {
		return errors.New("gate_id required")
	}
	if payload == "" {
		return errors.New("payload required")
	}
	if quorumN <= 0 {
		return errors.New("quorum must be positive")
	}
	if len(voters) == 0 {
		return errors.New("at least one voter required")
	}
	if quorumN > len(voters) {
		return errors.New("quorum cannot exceed voter count")
	}
	if deadline < 0 {
		return errors.New("deadline must be non-negative")
	}
	q.totalProposals.Add(1)
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.gates[id]; ok {
		return errors.New("gate already exists: " + id)
	}
	vset := map[string]bool{}
	for _, v := range voters {
		if v == "" {
			return errors.New("empty voter not allowed")
		}
		vset[v] = true
	}
	g := &quorumGate{
		id: id, payload: payload, quorumN: quorumN, voters: vset,
		state:     "pending",
		approvals: map[string]quorumVote{},
		rejects:   map[string]quorumVote{},
		createdAt: time.Now(),
	}
	if deadline > 0 {
		g.deadline = g.createdAt.Add(deadline)
	}
	q.gates[id] = g
	return nil
}

// Approve records an approval.
func (q *QuorumGates) Approve(id, agent, reason string) error {
	if id == "" || agent == "" {
		return errors.New("gate_id and agent required")
	}
	q.totalApprovals.Add(1)
	q.mu.RLock()
	g, ok := q.gates[id]
	q.mu.RUnlock()
	if !ok {
		return errors.New("unknown gate: " + id)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	q.lazyExpire(g)
	if g.state != "pending" {
		return errors.New("gate is " + g.state)
	}
	if !g.voters[agent] {
		return errors.New("agent not in voter set")
	}
	g.approvals[agent] = quorumVote{Agent: agent, Reason: reason, At: time.Now()}
	delete(g.rejects, agent) // approval supersedes prior reject
	return nil
}

// Reject records a rejection. Any reject from an allowed voter
// transitions state → rejected.
func (q *QuorumGates) Reject(id, agent, reason string) error {
	if id == "" || agent == "" {
		return errors.New("gate_id and agent required")
	}
	q.totalRejects.Add(1)
	q.mu.RLock()
	g, ok := q.gates[id]
	q.mu.RUnlock()
	if !ok {
		return errors.New("unknown gate: " + id)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	q.lazyExpire(g)
	if g.state != "pending" {
		return errors.New("gate is " + g.state)
	}
	if !g.voters[agent] {
		return errors.New("agent not in voter set")
	}
	g.rejects[agent] = quorumVote{Agent: agent, Reason: reason, At: time.Now()}
	delete(g.approvals, agent)
	g.state = "rejected"
	return nil
}

// QuorumCommitResult is COMMIT's return.
type QuorumCommitResult struct {
	GateID      string `json:"gate_id"`
	State       string `json:"state"`
	Approvals   int    `json:"approvals"`
	QuorumN     int    `json:"quorum_n"`
}

// Commit closes the gate. Errors if quorum unmet or state non-pending.
func (q *QuorumGates) Commit(id string) (QuorumCommitResult, error) {
	if id == "" {
		return QuorumCommitResult{}, errors.New("gate_id required")
	}
	q.totalCommits.Add(1)
	q.mu.RLock()
	g, ok := q.gates[id]
	q.mu.RUnlock()
	if !ok {
		return QuorumCommitResult{}, errors.New("unknown gate: " + id)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	q.lazyExpire(g)
	if g.state != "pending" {
		return QuorumCommitResult{}, errors.New("gate is " + g.state)
	}
	if len(g.approvals) < g.quorumN {
		return QuorumCommitResult{}, errors.New("quorum unmet: have " +
			itoaInline(len(g.approvals)) + ", need " + itoaInline(g.quorumN))
	}
	g.state = "committed"
	return QuorumCommitResult{
		GateID: id, State: g.state,
		Approvals: len(g.approvals), QuorumN: g.quorumN,
	}, nil
}

// QuorumStatus is STATUS's return.
type QuorumStatus struct {
	GateID       string         `json:"gate_id"`
	State        string         `json:"state"`
	Payload      string         `json:"payload"`
	QuorumN      int            `json:"quorum_n"`
	Voters       []string       `json:"voters"`
	Approvals    []QuorumVoteRow `json:"approvals"`
	Rejects      []QuorumVoteRow `json:"rejects"`
	DeadlineUnix int64          `json:"deadline_unix"`
}

type QuorumVoteRow struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
	AtUnix int64  `json:"at_unix"`
}

// Status returns the full state of the gate.
func (q *QuorumGates) Status(id string) (QuorumStatus, bool) {
	q.mu.RLock()
	g, ok := q.gates[id]
	q.mu.RUnlock()
	if !ok {
		return QuorumStatus{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	q.lazyExpire(g)
	out := QuorumStatus{
		GateID: g.id, State: g.state, Payload: g.payload, QuorumN: g.quorumN,
	}
	for v := range g.voters {
		out.Voters = append(out.Voters, v)
	}
	sort.Strings(out.Voters)
	for _, a := range g.approvals {
		out.Approvals = append(out.Approvals, QuorumVoteRow{Agent: a.Agent, Reason: a.Reason, AtUnix: a.At.Unix()})
	}
	sort.Slice(out.Approvals, func(i, j int) bool { return out.Approvals[i].Agent < out.Approvals[j].Agent })
	for _, r := range g.rejects {
		out.Rejects = append(out.Rejects, QuorumVoteRow{Agent: r.Agent, Reason: r.Reason, AtUnix: r.At.Unix()})
	}
	sort.Slice(out.Rejects, func(i, j int) bool { return out.Rejects[i].Agent < out.Rejects[j].Agent })
	if !g.deadline.IsZero() {
		out.DeadlineUnix = g.deadline.Unix()
	}
	return out, true
}

// QuorumListRow is one row of LIST.
type QuorumListRow struct {
	GateID    string `json:"gate_id"`
	State     string `json:"state"`
	Approvals int    `json:"approvals"`
	QuorumN   int    `json:"quorum_n"`
}

// List enumerates gates (optionally filtered by state).
func (q *QuorumGates) List(state string) []QuorumListRow {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]QuorumListRow, 0, len(q.gates))
	for _, g := range q.gates {
		g.mu.Lock()
		q.lazyExpire(g)
		if state != "" && g.state != state {
			g.mu.Unlock()
			continue
		}
		out = append(out, QuorumListRow{
			GateID: g.id, State: g.state,
			Approvals: len(g.approvals), QuorumN: g.quorumN,
		})
		g.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GateID < out[j].GateID })
	return out
}

// Forget drops a gate (or all).
func (q *QuorumGates) Forget(id string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if id == "ALL" {
		n := len(q.gates)
		q.gates = map[string]*quorumGate{}
		return n
	}
	if _, ok := q.gates[id]; ok {
		delete(q.gates, id)
		return 1
	}
	return 0
}

// QuorumStats is the global snapshot.
type QuorumStats struct {
	Gates          int   `json:"gates"`
	TotalProposals int64 `json:"total_proposals"`
	TotalApprovals int64 `json:"total_approvals"`
	TotalRejects   int64 `json:"total_rejects"`
	TotalCommits   int64 `json:"total_commits"`
}

func (q *QuorumGates) Stats() QuorumStats {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return QuorumStats{
		Gates:          len(q.gates),
		TotalProposals: q.totalProposals.Load(),
		TotalApprovals: q.totalApprovals.Load(),
		TotalRejects:   q.totalRejects.Load(),
		TotalCommits:   q.totalCommits.Load(),
	}
}

// lazyExpire flips state to "expired" if past deadline. Called under
// g.mu.
func (q *QuorumGates) lazyExpire(g *quorumGate) {
	if g.state != "pending" {
		return
	}
	if g.deadline.IsZero() {
		return
	}
	if time.Now().After(g.deadline) {
		g.state = "expired"
	}
}
