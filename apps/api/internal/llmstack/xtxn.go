package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// XTxnCoordinator is the cross-primitive two-phase commit coordinator.
// SETTLE.TXN is atomic within the ledger. PROV is atomic within
// itself. TRUST is atomic within itself. But there is no primitive
// for "post this settlement AND update trust AND record provenance
// as one atomic unit." When part-way through, a caller crash or
// validation failure leaves the system in a partial state — three
// stores that disagree.
//
// XTXN is the coordinator that solves this for the specific shape
// of "I have N independent ops, I want them all to succeed or none."
//
// The protocol is intentionally classical two-phase commit, scoped
// to participants that opt in by implementing the small XTxnParticipant
// contract:
//
//   Prepare(args) → ok, prepareToken, err
//     Stage the op. Reserve whatever's needed. Return a token that
//     identifies the prepared state. Validation failures abort here.
//
//   Commit(prepareToken) → err
//     Make the staged op visible / persistent. Must not fail under
//     normal operation — preparation is where validation happens.
//
//   Abort(prepareToken)
//     Discard the staged op. Idempotent + best-effort.
//
// The honest limits — read these before relying on this primitive:
//
//   1. This is a coordinator in a single process. Participants are
//      in-engine primitives. No distributed 2PC, no quorum, no fault-
//      tolerant TM. Process crash mid-COMMIT can leave partial state
//      that requires the standard "uncertain transaction" operator
//      action — surface it in STATUS so a human can drive it home.
//
//   2. Commit-phase failure is uncertain by definition. If a
//      participant's Commit returns error after others have
//      committed, you have a divergence that no 2PC implementation
//      can hide. We mark the XTXN as "commit_partial" and the operator
//      acts on the participant-level audit log.
//
//   3. Participants are responsible for their own durability of the
//      prepared state. If a participant crashes after Prepare but
//      before Commit, the next time it boots it must surface the
//      prepared-token to the coordinator (via STATUS or recovery).
//      AIWAL is the companion primitive for this.
//
// Despite the limits, this primitive does solve the "X+Y+Z together
// or nothing" problem inside one engine — which is the realistic
// scope for a Redis-shaped service. We deliberately do not pretend
// to be Spanner.
//
// Commands:
//
//   XTXN.BEGIN xid [META k v ...]
//   XTXN.STAGE xid participant op-name [ARG k v ...]
//        Stages one op. participant must be a name the coordinator
//        knows; engine wires the registry.
//   XTXN.PREPARE xid
//        Calls Prepare on every staged participant. First failure
//        aborts the rest + marks the XTXN aborted.
//   XTXN.COMMIT xid
//        Calls Commit on every prepared participant. Atomic-ish
//        within the coordinator's mutex.
//   XTXN.ABORT xid [REASON r]
//   XTXN.STATUS xid
//        → state, staged, prepared, committed, aborted, reason
//   XTXN.LIST [STATE s]
//   XTXN.FORGET xid|ALL
//   XTXN.STATS
//
// The state machine: open → prepared → committed (terminal) | aborted (terminal) | commit_partial (terminal-needs-human)
type XTxnCoordinator struct {
	mu      sync.Mutex
	txns    map[string]*xtxn
	parties map[string]XTxnParticipant

	totalBegins   atomic.Int64
	totalStages   atomic.Int64
	totalPrepares atomic.Int64
	totalCommits  atomic.Int64
	totalAborts   atomic.Int64
	totalPartials atomic.Int64
}

// XTxnParticipant is the contract every primitive implements to
// opt into XTXN. The contract is deliberately minimal so each
// primitive owns its own validation/staging.
type XTxnParticipant interface {
	// Prepare returns a prepareToken if validation passed. The
	// prepareToken is opaque to the coordinator — it's how the
	// participant identifies the staged op back to itself on Commit
	// / Abort.
	Prepare(op string, args map[string]string) (token string, err error)
	// Commit makes the prepared op visible. Must not fail under
	// normal conditions; if it does, the coordinator surfaces
	// "commit_partial" for human intervention.
	Commit(token string) error
	// Abort discards the prepared op. Idempotent.
	Abort(token string)
}

type xtxn struct {
	mu          sync.Mutex
	id          string
	state       string // open, preparing, prepared, committing, committed, aborted, commit_partial
	meta        map[string]string
	staged      []xtxnStaged
	prepared    []xtxnPrepared
	committed   []string
	createdAt   time.Time
	finishedAt  time.Time
	abortReason string
}

type xtxnStaged struct {
	Participant string
	Op          string
	Args        map[string]string
}

type xtxnPrepared struct {
	Participant string
	Token       string
}

// NewXTxnCoordinator returns an empty coordinator.
func NewXTxnCoordinator() *XTxnCoordinator {
	return &XTxnCoordinator{
		txns:    map[string]*xtxn{},
		parties: map[string]XTxnParticipant{},
	}
}

// Register wires a participant. Re-registering overwrites (so the
// engine can swap implementations during boot).
func (x *XTxnCoordinator) Register(name string, p XTxnParticipant) {
	x.mu.Lock()
	x.parties[name] = p
	x.mu.Unlock()
}

// Begin opens a new transaction.
func (x *XTxnCoordinator) Begin(xid string, meta map[string]string) error {
	if xid == "" {
		return errors.New("xid required")
	}
	x.totalBegins.Add(1)
	cp := map[string]string{}
	for k, v := range meta {
		cp[k] = v
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.txns[xid]; ok {
		return errors.New("xid already exists: " + xid)
	}
	x.txns[xid] = &xtxn{
		id: xid, state: "open", meta: cp,
		createdAt: time.Now(),
	}
	return nil
}

// Stage records an intent. The participant must be Register'd or
// STAGE rejects immediately — better to catch the wiring bug here
// than at PREPARE.
func (x *XTxnCoordinator) Stage(xid, participant, op string, args map[string]string) error {
	if xid == "" || participant == "" || op == "" {
		return errors.New("xid, participant, op required")
	}
	x.totalStages.Add(1)
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.txns[xid]
	if !ok {
		return errors.New("unknown xid: " + xid)
	}
	if _, ok := x.parties[participant]; !ok {
		return errors.New("unknown participant: " + participant)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != "open" {
		return errors.New("xtxn is " + t.state)
	}
	cp := map[string]string{}
	for k, v := range args {
		cp[k] = v
	}
	t.staged = append(t.staged, xtxnStaged{
		Participant: participant, Op: op, Args: cp,
	})
	return nil
}

// XTxnPrepareResult is PREPARE's return.
type XTxnPrepareResult struct {
	XID      string `json:"xid"`
	State    string `json:"state"`
	Prepared int    `json:"prepared"`
	Reason   string `json:"reason,omitempty"`
}

// Prepare walks the staged list and calls Prepare on each participant.
// On any failure, every already-prepared participant gets Abort and
// the txn moves to "aborted".
func (x *XTxnCoordinator) Prepare(xid string) (XTxnPrepareResult, error) {
	if xid == "" {
		return XTxnPrepareResult{}, errors.New("xid required")
	}
	x.totalPrepares.Add(1)
	x.mu.Lock()
	t, ok := x.txns[xid]
	parties := x.parties
	x.mu.Unlock()
	if !ok {
		return XTxnPrepareResult{}, errors.New("unknown xid: " + xid)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != "open" {
		return XTxnPrepareResult{}, errors.New("xtxn is " + t.state)
	}
	t.state = "preparing"
	for _, s := range t.staged {
		p := parties[s.Participant]
		token, err := p.Prepare(s.Op, s.Args)
		if err != nil {
			// Abort everything prepared so far
			for _, pp := range t.prepared {
				parties[pp.Participant].Abort(pp.Token)
			}
			t.state = "aborted"
			t.abortReason = "prepare failed on " + s.Participant + ": " + err.Error()
			t.finishedAt = time.Now()
			x.totalAborts.Add(1)
			return XTxnPrepareResult{
				XID: xid, State: t.state, Prepared: len(t.prepared),
				Reason: t.abortReason,
			}, nil
		}
		t.prepared = append(t.prepared, xtxnPrepared{
			Participant: s.Participant, Token: token,
		})
	}
	t.state = "prepared"
	return XTxnPrepareResult{
		XID: xid, State: t.state, Prepared: len(t.prepared),
	}, nil
}

// XTxnCommitResult is COMMIT's return.
type XTxnCommitResult struct {
	XID        string `json:"xid"`
	State      string `json:"state"`
	Committed  int    `json:"committed"`
	Reason     string `json:"reason,omitempty"`
}

// Commit walks the prepared list and calls Commit on each. If a
// commit fails after others have already committed, the txn enters
// "commit_partial" — the operator must investigate. This is the
// inherent limit of single-process 2PC; we surface it cleanly
// rather than hide it.
func (x *XTxnCoordinator) Commit(xid string) (XTxnCommitResult, error) {
	if xid == "" {
		return XTxnCommitResult{}, errors.New("xid required")
	}
	x.totalCommits.Add(1)
	x.mu.Lock()
	t, ok := x.txns[xid]
	parties := x.parties
	x.mu.Unlock()
	if !ok {
		return XTxnCommitResult{}, errors.New("unknown xid: " + xid)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != "prepared" {
		return XTxnCommitResult{}, errors.New("xtxn is " + t.state + " (must be prepared)")
	}
	t.state = "committing"
	for _, pp := range t.prepared {
		if err := parties[pp.Participant].Commit(pp.Token); err != nil {
			t.state = "commit_partial"
			t.abortReason = "commit failed on " + pp.Participant + " after " +
				itoaInline(len(t.committed)) + " participants committed: " + err.Error()
			t.finishedAt = time.Now()
			x.totalPartials.Add(1)
			return XTxnCommitResult{
				XID: xid, State: t.state, Committed: len(t.committed),
				Reason: t.abortReason,
			}, nil
		}
		t.committed = append(t.committed, pp.Participant)
	}
	t.state = "committed"
	t.finishedAt = time.Now()
	return XTxnCommitResult{
		XID: xid, State: t.state, Committed: len(t.committed),
	}, nil
}

// Abort explicitly tears down an open or prepared txn.
func (x *XTxnCoordinator) Abort(xid, reason string) error {
	if xid == "" {
		return errors.New("xid required")
	}
	x.totalAborts.Add(1)
	x.mu.Lock()
	t, ok := x.txns[xid]
	parties := x.parties
	x.mu.Unlock()
	if !ok {
		return errors.New("unknown xid: " + xid)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != "open" && t.state != "prepared" {
		return errors.New("xtxn is " + t.state + " (cannot abort)")
	}
	for _, pp := range t.prepared {
		parties[pp.Participant].Abort(pp.Token)
	}
	t.state = "aborted"
	if reason == "" {
		reason = "user aborted"
	}
	t.abortReason = reason
	t.finishedAt = time.Now()
	return nil
}

// XTxnStatus is STATUS's return.
type XTxnStatus struct {
	XID           string            `json:"xid"`
	State         string            `json:"state"`
	StagedCount   int               `json:"staged_count"`
	PreparedCount int               `json:"prepared_count"`
	CommittedCount int              `json:"committed_count"`
	Reason        string            `json:"reason,omitempty"`
	CreatedUnix   int64             `json:"created_unix"`
	FinishedUnix  int64             `json:"finished_unix,omitempty"`
	Meta          map[string]string `json:"meta,omitempty"`
}

// Status returns the snapshot.
func (x *XTxnCoordinator) Status(xid string) (XTxnStatus, bool) {
	x.mu.Lock()
	t, ok := x.txns[xid]
	x.mu.Unlock()
	if !ok {
		return XTxnStatus{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := XTxnStatus{
		XID: t.id, State: t.state,
		StagedCount: len(t.staged), PreparedCount: len(t.prepared),
		CommittedCount: len(t.committed),
		Reason: t.abortReason,
		CreatedUnix: t.createdAt.Unix(),
	}
	if !t.finishedAt.IsZero() {
		out.FinishedUnix = t.finishedAt.Unix()
	}
	if len(t.meta) > 0 {
		out.Meta = map[string]string{}
		for k, v := range t.meta {
			out.Meta[k] = v
		}
	}
	return out, true
}

// XTxnListRow is one row of LIST.
type XTxnListRow struct {
	XID    string `json:"xid"`
	State  string `json:"state"`
	Staged int    `json:"staged"`
}

// List enumerates transactions.
func (x *XTxnCoordinator) List(state string) []XTxnListRow {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]XTxnListRow, 0, len(x.txns))
	for _, t := range x.txns {
		t.mu.Lock()
		if state != "" && t.state != state {
			t.mu.Unlock()
			continue
		}
		out = append(out, XTxnListRow{
			XID: t.id, State: t.state, Staged: len(t.staged),
		})
		t.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].XID < out[j].XID })
	return out
}

// Forget drops a txn (or all).
func (x *XTxnCoordinator) Forget(xid string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	if xid == "ALL" {
		n := len(x.txns)
		x.txns = map[string]*xtxn{}
		return n
	}
	if _, ok := x.txns[xid]; ok {
		delete(x.txns, xid)
		return 1
	}
	return 0
}

// XTxnStats is the global snapshot.
type XTxnStats struct {
	Active        int   `json:"active"`
	Participants  int   `json:"participants_registered"`
	TotalBegins   int64 `json:"total_begins"`
	TotalStages   int64 `json:"total_stages"`
	TotalPrepares int64 `json:"total_prepares"`
	TotalCommits  int64 `json:"total_commits"`
	TotalAborts   int64 `json:"total_aborts"`
	TotalPartials int64 `json:"total_commit_partials"`
}

func (x *XTxnCoordinator) Stats() XTxnStats {
	x.mu.Lock()
	defer x.mu.Unlock()
	return XTxnStats{
		Active: len(x.txns), Participants: len(x.parties),
		TotalBegins: x.totalBegins.Load(),
		TotalStages: x.totalStages.Load(),
		TotalPrepares: x.totalPrepares.Load(),
		TotalCommits: x.totalCommits.Load(),
		TotalAborts: x.totalAborts.Load(),
		TotalPartials: x.totalPartials.Load(),
	}
}

// Participants returns the list of registered participant names.
func (x *XTxnCoordinator) Participants() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]string, 0, len(x.parties))
	for k := range x.parties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
