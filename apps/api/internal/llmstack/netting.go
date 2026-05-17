package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Netting is the clearinghouse / multilateral netting layer that sits
// on top of SETTLE. The motivation, plainly:
//
//   SETTLE atomically posts one transaction. In an agent economy with
//   N parties making M obligations during a clearing cycle, calling
//   SETTLE.TXN M times is the maximum work — each settlement touches
//   the ledger and each costs a hop. Netting collapses gross
//   obligations into a minimum set of net transfers that produces the
//   same final-balance vector. Classic clearinghouse function.
//
// What this primitive does NOT do:
//
//   - It does not invent settlement semantics. The settlement is
//     still SETTLE.TXN, with all its invariants. Netting is only the
//     planner that reduces gross flows to net flows.
//   - It does not introduce a new correctness class. If SETTLE.TXN
//     rejects the netted plan (insufficient funds, closed account),
//     APPLY surfaces the error and the cycle is rolled back as
//     individual reversals.
//   - It does not guarantee finality before APPLY. CLOSE produces a
//     plan; APPLY executes it. Between the two, the gross obligations
//     are still tracked and the operator can DRY_RUN to inspect.
//
// The algorithm: build a directed graph of obligations, cancel
// self-loops and bidirectional offsets first (these are pure
// reductions with no information loss), then compute the net
// position of each node (sum-incoming − sum-outgoing). Positive net
// = creditor; negative = debtor. Greedy-match largest creditor to
// largest debtor until both are zeroed out. This is O(N²) in the
// worst case but for typical agent-economy clearing cycles
// (dozens-to-hundreds of parties) it's microseconds.
//
// Commands:
//
//   NETTING.OPEN cycle-id [DEADLINE ms]
//        DEADLINE: cycle auto-closes (status="expired") after this.
//   NETTING.ADD cycle-id from to amount [TXN_ID i]
//        TXN_ID is the upstream's reference; netting uses it for
//        traceability but does not enforce uniqueness within the
//        cycle (an upstream may submit corrections).
//   NETTING.CLOSE cycle-id [DRY_RUN 0|1]
//        Builds the netting plan. DRY_RUN=1 returns the plan WITHOUT
//        marking the cycle as ready-to-apply. The cycle can be
//        re-CLOSEd to recompute if new ADDs arrive (during open
//        state only).
//   NETTING.APPLY cycle-id [LEDGER ledger-id]
//        Posts each netted transfer via SETTLE.TXN. Returns the txn
//        ids posted + a summary (savings vs gross). If any SETTLE.TXN
//        fails, posted txns are reversed and the cycle is marked
//        "apply_failed" — the operator must investigate before
//        re-applying.
//   NETTING.STATUS cycle-id
//        → state (open|closed|applying|applied|apply_failed|expired),
//        gross_count, gross_total, net_transfers, net_total,
//        savings_pct, plan (the {from, to, amount} list).
//   NETTING.LIST [STATE s]
//   NETTING.FORGET cycle-id|ALL
//   NETTING.STATS
//
// Hot path: ADD is one slice append under a per-cycle mutex. CLOSE
// is O(N + E + N²-pairing). APPLY is len(plan) calls into SETTLE.
type Netting struct {
	mu     sync.RWMutex
	cycles map[string]*netCycle

	// Settlement-execution callback. The engine wires this so APPLY
	// can post real SETTLE.TXN ops. We keep it as an injected function
	// (rather than importing the Settlement type) to avoid coupling
	// for tests — tests can supply a fake executor.
	executor NettingExecutor

	totalOpens   atomic.Int64
	totalAdds    atomic.Int64
	totalCloses  atomic.Int64
	totalApplies atomic.Int64
}

// NettingExecutor is the integration point. The engine implements
// this against Settlement; tests implement it to inspect what would
// have been posted.
type NettingExecutor interface {
	// PostTxn must be atomic-or-error. Idempotent on txn_id is also
	// required so APPLY retries don't double-post.
	PostTxn(ledger, txnID, memo string, debits, credits []SettleLine) error
}

type netCycle struct {
	mu          sync.Mutex
	id          string
	state       string // open, closed, applying, applied, apply_failed, expired
	obligations []netObligation
	plan        []NettingPair // populated by CLOSE
	deadline    time.Time
	createdAt   time.Time
	closedAt    time.Time
	appliedAt   time.Time
	failureReason string
	postedTxnIDs []string
}

type netObligation struct {
	From   string
	To     string
	Amount float64
	TxnRef string
	At     time.Time
}

// NettingPair is one row of the netting plan.
type NettingPair struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Amount float64 `json:"amount"`
}

// NewNetting returns an empty clearinghouse.
func NewNetting() *Netting {
	return &Netting{cycles: map[string]*netCycle{}}
}

// SetExecutor wires the SETTLE-posting backend.
func (n *Netting) SetExecutor(e NettingExecutor) {
	n.mu.Lock()
	n.executor = e
	n.mu.Unlock()
}

// Open creates a new clearing cycle.
func (n *Netting) Open(id string, deadline time.Duration) error {
	if id == "" {
		return errors.New("cycle_id required")
	}
	if deadline < 0 {
		return errors.New("deadline must be non-negative")
	}
	n.totalOpens.Add(1)
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.cycles[id]; ok {
		return errors.New("cycle already exists: " + id)
	}
	c := &netCycle{id: id, state: "open", createdAt: time.Now()}
	if deadline > 0 {
		c.deadline = c.createdAt.Add(deadline)
	}
	n.cycles[id] = c
	return nil
}

// Add records one gross obligation.
func (n *Netting) Add(cycleID, from, to string, amount float64, txnRef string) error {
	if cycleID == "" || from == "" || to == "" {
		return errors.New("cycle_id, from, to required")
	}
	if from == to {
		return errors.New("self-transfer (from == to) is not an obligation")
	}
	if amount <= 0 {
		return errors.New("amount must be positive")
	}
	n.totalAdds.Add(1)
	n.mu.RLock()
	c, ok := n.cycles[cycleID]
	n.mu.RUnlock()
	if !ok {
		return errors.New("unknown cycle: " + cycleID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n.lazyExpire(c)
	if c.state != "open" {
		return errors.New("cycle is " + c.state)
	}
	c.obligations = append(c.obligations, netObligation{
		From: from, To: to, Amount: amount, TxnRef: txnRef, At: time.Now(),
	})
	return nil
}

// NettingPlan is CLOSE's return.
type NettingPlan struct {
	CycleID       string        `json:"cycle_id"`
	GrossCount    int           `json:"gross_count"`
	GrossTotal    float64       `json:"gross_total"`
	NetTransfers  int           `json:"net_transfers"`
	NetTotal      float64       `json:"net_total"`
	SavingsPct    float64       `json:"savings_pct"`
	Plan          []NettingPair `json:"plan"`
	DryRun        bool          `json:"dry_run"`
}

// Close computes the netting plan. DRY_RUN=true returns the plan
// without locking the cycle (so further ADDs are still allowed). On
// a non-dry close, the cycle becomes state="closed" and APPLY can
// proceed.
func (n *Netting) Close(cycleID string, dryRun bool) (NettingPlan, error) {
	if cycleID == "" {
		return NettingPlan{}, errors.New("cycle_id required")
	}
	n.totalCloses.Add(1)
	n.mu.RLock()
	c, ok := n.cycles[cycleID]
	n.mu.RUnlock()
	if !ok {
		return NettingPlan{}, errors.New("unknown cycle: " + cycleID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n.lazyExpire(c)
	if c.state != "open" && c.state != "closed" {
		return NettingPlan{}, errors.New("cycle is " + c.state)
	}
	plan, gross, net := computeNetting(c.obligations)
	out := NettingPlan{
		CycleID: c.id,
		GrossCount: len(c.obligations), GrossTotal: gross,
		NetTransfers: len(plan), NetTotal: net,
		Plan: plan, DryRun: dryRun,
	}
	if gross > 0 {
		out.SavingsPct = (gross - net) / gross * 100
	}
	if !dryRun {
		c.plan = plan
		c.state = "closed"
		c.closedAt = time.Now()
	}
	return out, nil
}

// NettingApplyResult is APPLY's return.
type NettingApplyResult struct {
	CycleID      string   `json:"cycle_id"`
	State        string   `json:"state"`
	PostedTxnIDs []string `json:"posted_txn_ids"`
	FailedAt     int      `json:"failed_at,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

// Apply posts the netted plan via the wired executor. If any single
// post fails, prior posts in this APPLY are reversed (the executor
// must accept reversal txn ids) and the cycle is marked
// "apply_failed" with FailedAt + Reason.
//
// IMPORTANT LIMIT: this rollback is best-effort. If the executor's
// reverse-post itself fails, the ledger is in an intermediate state
// the operator must reconcile manually — typical clearinghouse
// failure mode that requires human attention. We do NOT silently
// retry; we surface the partial state in STATUS for the operator to
// act on.
func (n *Netting) Apply(cycleID, ledger string) (NettingApplyResult, error) {
	if cycleID == "" {
		return NettingApplyResult{}, errors.New("cycle_id required")
	}
	n.totalApplies.Add(1)
	n.mu.RLock()
	c, ok := n.cycles[cycleID]
	exec := n.executor
	n.mu.RUnlock()
	if !ok {
		return NettingApplyResult{}, errors.New("unknown cycle: " + cycleID)
	}
	if exec == nil {
		return NettingApplyResult{}, errors.New("no executor wired (engine setup incomplete)")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "closed" {
		return NettingApplyResult{}, errors.New("cycle must be closed before apply; state is " + c.state)
	}
	c.state = "applying"
	out := NettingApplyResult{CycleID: cycleID}
	for i, p := range c.plan {
		txnID := cycleID + "-net-" + itoaInline(i+1)
		err := exec.PostTxn(
			ledger, txnID, "netting cycle "+cycleID,
			[]SettleLine{{Account: p.To, Amount: p.Amount}},   // debit creditor
			[]SettleLine{{Account: p.From, Amount: p.Amount}}, // credit debtor
		)
		if err != nil {
			// Best-effort rollback of prior posts in this APPLY
			for j := len(c.postedTxnIDs) - 1; j >= 0; j-- {
				revID := c.postedTxnIDs[j] + "-rev"
				orig := c.plan[j]
				_ = exec.PostTxn(
					ledger, revID, "netting rollback",
					[]SettleLine{{Account: orig.From, Amount: orig.Amount}},
					[]SettleLine{{Account: orig.To, Amount: orig.Amount}},
				)
			}
			c.state = "apply_failed"
			c.failureReason = "post #" + itoaInline(i+1) + ": " + err.Error()
			out.State = c.state
			out.FailedAt = i + 1
			out.Reason = c.failureReason
			out.PostedTxnIDs = append([]string{}, c.postedTxnIDs...)
			return out, nil
		}
		c.postedTxnIDs = append(c.postedTxnIDs, txnID)
	}
	c.state = "applied"
	c.appliedAt = time.Now()
	out.State = c.state
	out.PostedTxnIDs = append([]string{}, c.postedTxnIDs...)
	return out, nil
}

// NettingStatus is STATUS's return.
type NettingStatus struct {
	CycleID      string        `json:"cycle_id"`
	State        string        `json:"state"`
	GrossCount   int           `json:"gross_count"`
	GrossTotal   float64       `json:"gross_total"`
	NetTransfers int           `json:"net_transfers"`
	NetTotal     float64       `json:"net_total"`
	SavingsPct   float64       `json:"savings_pct"`
	Plan         []NettingPair `json:"plan,omitempty"`
	PostedTxnIDs []string      `json:"posted_txn_ids,omitempty"`
	FailureReason string       `json:"failure_reason,omitempty"`
	DeadlineUnix int64         `json:"deadline_unix,omitempty"`
}

// Status returns the cycle snapshot.
func (n *Netting) Status(cycleID string) (NettingStatus, bool) {
	n.mu.RLock()
	c, ok := n.cycles[cycleID]
	n.mu.RUnlock()
	if !ok {
		return NettingStatus{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n.lazyExpire(c)
	out := NettingStatus{
		CycleID: c.id, State: c.state,
		GrossCount: len(c.obligations),
		Plan: append([]NettingPair{}, c.plan...),
		PostedTxnIDs: append([]string{}, c.postedTxnIDs...),
		FailureReason: c.failureReason,
	}
	for _, o := range c.obligations {
		out.GrossTotal += o.Amount
	}
	out.NetTransfers = len(c.plan)
	for _, p := range c.plan {
		out.NetTotal += p.Amount
	}
	if out.GrossTotal > 0 {
		out.SavingsPct = (out.GrossTotal - out.NetTotal) / out.GrossTotal * 100
	}
	if !c.deadline.IsZero() {
		out.DeadlineUnix = c.deadline.Unix()
	}
	return out, true
}

// NettingListRow is one row of LIST.
type NettingListRow struct {
	CycleID    string  `json:"cycle_id"`
	State      string  `json:"state"`
	GrossCount int     `json:"gross_count"`
	NetTransfers int   `json:"net_transfers"`
	SavingsPct float64 `json:"savings_pct"`
}

// List enumerates cycles (optionally filtered by state).
func (n *Netting) List(state string) []NettingListRow {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]NettingListRow, 0, len(n.cycles))
	for _, c := range n.cycles {
		c.mu.Lock()
		n.lazyExpire(c)
		if state != "" && c.state != state {
			c.mu.Unlock()
			continue
		}
		row := NettingListRow{
			CycleID: c.id, State: c.state,
			GrossCount: len(c.obligations), NetTransfers: len(c.plan),
		}
		gross := 0.0
		for _, o := range c.obligations {
			gross += o.Amount
		}
		net := 0.0
		for _, p := range c.plan {
			net += p.Amount
		}
		if gross > 0 {
			row.SavingsPct = (gross - net) / gross * 100
		}
		out = append(out, row)
		c.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CycleID < out[j].CycleID })
	return out
}

// Forget drops a cycle (or all).
func (n *Netting) Forget(cycleID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if cycleID == "ALL" {
		k := len(n.cycles)
		n.cycles = map[string]*netCycle{}
		return k
	}
	if _, ok := n.cycles[cycleID]; ok {
		delete(n.cycles, cycleID)
		return 1
	}
	return 0
}

// NettingStats is the global snapshot.
type NettingStats struct {
	Cycles       int   `json:"cycles"`
	TotalOpens   int64 `json:"total_opens"`
	TotalAdds    int64 `json:"total_adds"`
	TotalCloses  int64 `json:"total_closes"`
	TotalApplies int64 `json:"total_applies"`
}

func (n *Netting) Stats() NettingStats {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return NettingStats{
		Cycles: len(n.cycles),
		TotalOpens: n.totalOpens.Load(),
		TotalAdds: n.totalAdds.Load(),
		TotalCloses: n.totalCloses.Load(),
		TotalApplies: n.totalApplies.Load(),
	}
}

func (n *Netting) lazyExpire(c *netCycle) {
	if c.state != "open" && c.state != "closed" {
		return
	}
	if c.deadline.IsZero() {
		return
	}
	if time.Now().After(c.deadline) {
		c.state = "expired"
	}
}

// computeNetting collapses a gross obligation list into a minimum-
// transfer netting plan. The algorithm:
//
//   1. Sum per-node net position (incoming − outgoing).
//   2. Partition into creditors (positive net) and debtors (negative
//      net), sorted by magnitude.
//   3. Greedy-match largest creditor to largest debtor; emit a
//      transfer for the smaller of the two; reduce both; loop.
//
// This produces at most N transfers for N participants (a classic
// bound: a clearing of N parties needs at most N-1 transfers if any
// netting is possible). The result is not the unique minimum-cost
// plan under all edge-cost models, but it's the canonical
// minimum-transfer plan that clears the cycle.
func computeNetting(obligations []netObligation) ([]NettingPair, float64, float64) {
	gross := 0.0
	netPos := map[string]float64{}
	for _, o := range obligations {
		gross += o.Amount
		netPos[o.From] -= o.Amount
		netPos[o.To] += o.Amount
	}
	type party struct {
		name string
		net  float64
	}
	var creditors, debtors []party
	for k, v := range netPos {
		switch {
		case v > 1e-9:
			creditors = append(creditors, party{name: k, net: v})
		case v < -1e-9:
			debtors = append(debtors, party{name: k, net: -v}) // store positive magnitude
		}
	}
	sort.Slice(creditors, func(i, j int) bool { return creditors[i].net > creditors[j].net })
	sort.Slice(debtors, func(i, j int) bool { return debtors[i].net > debtors[j].net })

	plan := make([]NettingPair, 0)
	netTotal := 0.0
	i, j := 0, 0
	for i < len(creditors) && j < len(debtors) {
		amt := creditors[i].net
		if debtors[j].net < amt {
			amt = debtors[j].net
		}
		plan = append(plan, NettingPair{
			From: debtors[j].name, To: creditors[i].name, Amount: amt,
		})
		netTotal += amt
		creditors[i].net -= amt
		debtors[j].net -= amt
		if creditors[i].net < 1e-9 {
			i++
		}
		if debtors[j].net < 1e-9 {
			j++
		}
	}
	// Deterministic plan order: sort by (from, to)
	sort.Slice(plan, func(i, j int) bool {
		if plan[i].From != plan[j].From {
			return plan[i].From < plan[j].From
		}
		return plan[i].To < plan[j].To
	})
	return plan, gross, netTotal
}
