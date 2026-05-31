package llmstack

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Debates is the structured multi-agent decision consensus primitive.
// Distinct from CRDT (which is data consensus); distinct from QUORUM
// (which is an N-of-M approval gate). DEBATE captures the
// *deliberation*: proposal → rounds of critique → convergence (or
// recorded dissent).
//
// Every multi-agent system fakes this with prompt glue — "agent A
// proposes, agents B and C critique, agent D synthesizes." DEBATE
// makes the structure first-class so:
//
//   - The application can audit every round (who said what, when).
//   - Convergence is a deterministic check (vote on the final
//     proposal), not "did the prompt feel like it concluded?"
//   - Dissent is permanently recorded — when later analysis shows
//     the decision was wrong, you can name the agents who flagged it.
//
// Lifecycle:
//
//   START → proposing → critiquing → voting → resolved
//
// Commands:
//
//   DEBATE.START debate-id proposer "<proposal text>"
//        → state=proposing
//   DEBATE.CRITIQUE debate-id agent "<critique text>"
//        Appends a critique. Allowed in proposing/critiquing.
//   DEBATE.REVISE debate-id "<new proposal text>"
//        Replace the current proposal (only the original proposer).
//        Bumps revision number.
//   DEBATE.VOTE debate-id agent approve|reject [REASON "..."]
//        Each agent votes once. Vote replaces any prior vote from
//        that agent within the same revision.
//   DEBATE.RESOLVE debate-id [QUORUM n]
//        Closes the debate. Default quorum = majority of voters.
//        State → resolved. Returns approved + dissent list.
//   DEBATE.GET debate-id
//        Full transcript: proposal + critiques + votes per revision.
//   DEBATE.LIST [STATE s]
//   DEBATE.FORGET debate-id|ALL
//   DEBATE.STATS
//
// The hot path is O(1) for every mutation; state-change validation
// rejects out-of-order ops (e.g. VOTE before START).
type Debates struct {
	mu      sync.RWMutex
	debates map[string]*debate

	totalStarts     atomic.Int64
	totalCritiques  atomic.Int64
	totalVotes      atomic.Int64
	totalResolves   atomic.Int64
}

type debate struct {
	mu         sync.Mutex
	id         string
	proposer   string
	state      string // proposing, critiquing, voting, resolved
	revision   int
	proposal   string
	critiques  []debateCritique
	votes      map[string]debateVote // agent → vote (most recent for current revision)
	startedAt  time.Time
	resolvedAt time.Time
	approved   bool
	dissent    []string
}

type debateCritique struct {
	Agent   string
	Text    string
	At      time.Time
	OnRev   int
}

type debateVote struct {
	Agent   string
	Approve bool
	Reason  string
	OnRev   int
	At      time.Time
}

// NewDebates returns an empty registry.
func NewDebates() *Debates {
	return &Debates{debates: map[string]*debate{}}
}

// Start opens a debate with the proposer's initial proposal.
func (d *Debates) Start(id, proposer, proposal string) error {
	if id == "" {
		return errors.New("debate_id required")
	}
	if proposer == "" {
		return errors.New("proposer required")
	}
	if proposal == "" {
		return errors.New("proposal required")
	}
	d.totalStarts.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.debates[id]; ok {
		return errors.New("debate already exists: " + id)
	}
	d.debates[id] = &debate{
		id: id, proposer: proposer, state: "proposing",
		proposal: proposal, revision: 1,
		votes: map[string]debateVote{},
		startedAt: time.Now(),
	}
	return nil
}

// Critique adds one critique to the current revision.
func (d *Debates) Critique(id, agent, text string) error {
	if id == "" || agent == "" || text == "" {
		return errors.New("debate_id, agent, text required")
	}
	d.totalCritiques.Add(1)
	d.mu.RLock()
	deb, ok := d.debates[id]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown debate: " + id)
	}
	deb.mu.Lock()
	defer deb.mu.Unlock()
	if deb.state == "resolved" {
		return errors.New("debate already resolved")
	}
	if deb.state == "voting" {
		return errors.New("debate is in voting phase; revise to re-open critique")
	}
	deb.state = "critiquing"
	deb.critiques = append(deb.critiques, debateCritique{
		Agent: agent, Text: text, At: time.Now(), OnRev: deb.revision,
	})
	return nil
}

// Revise replaces the proposal (only the original proposer). Bumps
// revision number; clears votes (they're tied to the old revision).
// Critiques are preserved with their original OnRev annotation.
func (d *Debates) Revise(id, proposer, newProposal string) error {
	if id == "" || proposer == "" || newProposal == "" {
		return errors.New("debate_id, proposer, proposal required")
	}
	d.mu.RLock()
	deb, ok := d.debates[id]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown debate: " + id)
	}
	deb.mu.Lock()
	defer deb.mu.Unlock()
	if deb.state == "resolved" {
		return errors.New("debate already resolved")
	}
	if proposer != deb.proposer {
		return errors.New("only the original proposer can revise")
	}
	deb.revision++
	deb.proposal = newProposal
	deb.state = "proposing"
	deb.votes = map[string]debateVote{}
	return nil
}

// Vote records or replaces an agent's vote for the current revision.
// Calling VOTE transitions state → voting.
func (d *Debates) Vote(id, agent string, approve bool, reason string) error {
	if id == "" || agent == "" {
		return errors.New("debate_id and agent required")
	}
	d.totalVotes.Add(1)
	d.mu.RLock()
	deb, ok := d.debates[id]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown debate: " + id)
	}
	deb.mu.Lock()
	defer deb.mu.Unlock()
	if deb.state == "resolved" {
		return errors.New("debate already resolved")
	}
	deb.state = "voting"
	deb.votes[agent] = debateVote{
		Agent: agent, Approve: approve, Reason: reason,
		OnRev: deb.revision, At: time.Now(),
	}
	return nil
}

// DebateResolveResult is RESOLVE's return.
type DebateResolveResult struct {
	DebateID string   `json:"debate_id"`
	Approved bool     `json:"approved"`
	Votes    int      `json:"votes"`
	ApproveN int      `json:"approve_n"`
	RejectN  int      `json:"reject_n"`
	Quorum   int      `json:"quorum"`
	Dissent  []string `json:"dissent"`
}

// Resolve closes the debate. quorum=0 means majority of voters.
// Returns approved=true if approve count ≥ quorum AND > reject count.
func (d *Debates) Resolve(id string, quorum int) (DebateResolveResult, error) {
	if id == "" {
		return DebateResolveResult{}, errors.New("debate_id required")
	}
	d.totalResolves.Add(1)
	d.mu.RLock()
	deb, ok := d.debates[id]
	d.mu.RUnlock()
	if !ok {
		return DebateResolveResult{}, errors.New("unknown debate: " + id)
	}
	deb.mu.Lock()
	defer deb.mu.Unlock()
	if deb.state == "resolved" {
		return DebateResolveResult{}, errors.New("already resolved")
	}
	approveN, rejectN := 0, 0
	var dissent []string
	for _, v := range deb.votes {
		if v.OnRev != deb.revision {
			continue
		}
		if v.Approve {
			approveN++
		} else {
			rejectN++
			dissent = append(dissent, v.Agent)
		}
	}
	total := approveN + rejectN
	if quorum <= 0 {
		quorum = total/2 + 1
	}
	approved := approveN >= quorum && approveN > rejectN
	deb.state = "resolved"
	deb.approved = approved
	deb.dissent = dissent
	deb.resolvedAt = time.Now()
	sort.Strings(dissent)
	return DebateResolveResult{
		DebateID: id, Approved: approved, Votes: total,
		ApproveN: approveN, RejectN: rejectN, Quorum: quorum,
		Dissent: dissent,
	}, nil
}

// DebateView is GET's return.
type DebateView struct {
	DebateID    string             `json:"debate_id"`
	Proposer    string             `json:"proposer"`
	State       string             `json:"state"`
	Revision    int                `json:"revision"`
	Proposal    string             `json:"proposal"`
	Critiques   []DebateCritiqueRow `json:"critiques"`
	Votes       []DebateVoteRow    `json:"votes"`
	Approved    bool               `json:"approved"`
	Dissent     []string           `json:"dissent"`
	StartedUnix int64              `json:"started_unix"`
}

type DebateCritiqueRow struct {
	Agent  string `json:"agent"`
	Text   string `json:"text"`
	OnRev  int    `json:"on_rev"`
	AtUnix int64  `json:"at_unix"`
}

type DebateVoteRow struct {
	Agent   string `json:"agent"`
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
	OnRev   int    `json:"on_rev"`
	AtUnix  int64  `json:"at_unix"`
}

// Get returns the full transcript.
func (d *Debates) Get(id string) (DebateView, bool) {
	if id == "" {
		return DebateView{}, false
	}
	d.mu.RLock()
	deb, ok := d.debates[id]
	d.mu.RUnlock()
	if !ok {
		return DebateView{}, false
	}
	deb.mu.Lock()
	defer deb.mu.Unlock()
	v := DebateView{
		DebateID: deb.id, Proposer: deb.proposer, State: deb.state,
		Revision: deb.revision, Proposal: deb.proposal,
		Approved: deb.approved, Dissent: append([]string{}, deb.dissent...),
		StartedUnix: deb.startedAt.Unix(),
	}
	for _, c := range deb.critiques {
		v.Critiques = append(v.Critiques, DebateCritiqueRow{
			Agent: c.Agent, Text: c.Text, OnRev: c.OnRev, AtUnix: c.At.Unix(),
		})
	}
	for _, vt := range deb.votes {
		v.Votes = append(v.Votes, DebateVoteRow{
			Agent: vt.Agent, Approve: vt.Approve, Reason: vt.Reason,
			OnRev: vt.OnRev, AtUnix: vt.At.Unix(),
		})
	}
	sort.Slice(v.Votes, func(i, j int) bool { return v.Votes[i].Agent < v.Votes[j].Agent })
	return v, true
}

// DebateListRow is one row of LIST.
type DebateListRow struct {
	DebateID string `json:"debate_id"`
	State    string `json:"state"`
	Revision int    `json:"revision"`
}

// List returns debates (optionally filtered by state).
func (d *Debates) List(state string) []DebateListRow {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]DebateListRow, 0, len(d.debates))
	for _, deb := range d.debates {
		deb.mu.Lock()
		if state != "" && deb.state != state {
			deb.mu.Unlock()
			continue
		}
		out = append(out, DebateListRow{
			DebateID: deb.id, State: deb.state, Revision: deb.revision,
		})
		deb.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DebateID < out[j].DebateID })
	return out
}

// Forget drops a debate (or all).
func (d *Debates) Forget(id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == "ALL" {
		n := len(d.debates)
		d.debates = map[string]*debate{}
		return n
	}
	if _, ok := d.debates[id]; ok {
		delete(d.debates, id)
		return 1
	}
	return 0
}

// DebateStats is the global snapshot.
type DebateStats struct {
	Debates        int   `json:"debates"`
	TotalStarts    int64 `json:"total_starts"`
	TotalCritiques int64 `json:"total_critiques"`
	TotalVotes     int64 `json:"total_votes"`
	TotalResolves  int64 `json:"total_resolves"`
}

func (d *Debates) Stats() DebateStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return DebateStats{
		Debates:        len(d.debates),
		TotalStarts:    d.totalStarts.Load(),
		TotalCritiques: d.totalCritiques.Load(),
		TotalVotes:     d.totalVotes.Load(),
		TotalResolves:  d.totalResolves.Load(),
	}
}

// Helper for status formatting used in tests
func (d *Debates) describe(id string) string {
	v, ok := d.Get(id)
	if !ok {
		return ""
	}
	return v.DebateID + ":" + v.State + ":r" + strconv.Itoa(v.Revision)
}
