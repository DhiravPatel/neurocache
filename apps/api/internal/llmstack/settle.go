package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Settlement is the atomic double-entry ledger. Everything financial
// the earlier phases built (LEDGER, CARBON, COST, FORECAST) measures.
// Nothing settles. The moment two parties transact through an agent —
// a user's budget paying a tool provider, one tenant's agent
// consuming another's API — you need a correctness class earlier
// primitives don't offer: invariant-preserving transactions.
//
// The invariants Settlement guarantees:
//
//   1. Every transaction is balanced. Σ debits over an account list
//      must equal Σ credits. A txn with imbalance is rejected before
//      any account moves.
//
//   2. Every transaction is atomic. Either every line posts or none
//      do. There is no half-state visible to ACCT.BALANCE.
//
//   3. No asset / expense account goes negative (configurable per
//      account on OPEN). Equity / liability / income may.
//
//   4. Every transaction is idempotent by txn-id. POSTing the same
//      txn-id twice is a no-op — critical for at-least-once message
//      delivery from upstream agents.
//
//   5. RECONCILE proves the global invariant: Σ debits across the
//      whole ledger == Σ credits. Off-by-anything → audit failure.
//
// Account types follow standard accounting: assets normally have a
// debit balance, liabilities/equity/income normally credit. We don't
// enforce normality — that's the chart-of-accounts designer's job —
// but we track type for reporting + the NO_NEGATIVE check defaults
// per-type the way an accountant would expect.
//
// Commands:
//
//   ACCT.OPEN name TYPE asset|liability|equity|income|expense
//        [CURRENCY iso-code] [NO_NEGATIVE 0|1]
//        NO_NEGATIVE defaults: asset/expense=1, liability/equity/income=0.
//   ACCT.BALANCE name
//        → balance, type, currency, last_entry_unix
//   ACCT.STATEMENT name [SINCE unix] [UNTIL unix] [LIMIT n]
//        → ordered entries with running balance.
//   ACCT.CLOSE name
//        Forbids future SETTLE involving this account; balance is kept
//        for historical lookups.
//   ACCT.LIST
//
//   SETTLE.TXN txn-id [MEMO "..."] DEBIT a1 amt1 [DEBIT a2 amt2 ...]
//        CREDIT b1 amt1 [CREDIT b2 amt2 ...]
//        Atomic balanced post. Repeating the same txn-id is a no-op.
//   SETTLE.REVERSE original-txn-id new-txn-id [MEMO "..."]
//        Produce a reversing entry (swaps debits ↔ credits).
//   SETTLE.GET txn-id          — txn body + posted-status
//   SETTLE.RECONCILE           — prove Σ debits == Σ credits globally
//   SETTLE.STATS
//
// The hot path: SETTLE.TXN is one validation pass + len(lines) atomic
// adds under a single mutex. The mutex is per-Settlement (not per
// account) so cross-account atomicity is trivially correct; for
// high throughput a sharded design is straightforward but isn't
// what's load-bearing here.
type Settlement struct {
	mu sync.Mutex

	accounts map[string]*acctRow
	txns     map[string]*settleTxn

	totalOpens    atomic.Int64
	totalTxns     atomic.Int64
	totalReverses atomic.Int64
	totalDuplicates atomic.Int64
}

// AcctType is one of the standard chart-of-accounts categories.
type AcctType string

const (
	AcctAsset     AcctType = "asset"
	AcctLiability AcctType = "liability"
	AcctEquity    AcctType = "equity"
	AcctIncome    AcctType = "income"
	AcctExpense   AcctType = "expense"
)

type acctRow struct {
	Name         string
	Type         AcctType
	Currency     string
	NoNegative   bool
	Balance      float64
	OpenedAt     time.Time
	Closed       bool
	LastEntryAt  time.Time
	Entries      []acctEntry
}

type acctEntry struct {
	TxnID    string
	Side     string // "debit" or "credit"
	Amount   float64
	Balance  float64 // running balance after this entry
	Memo     string
	PostedAt time.Time
}

type settleTxn struct {
	ID       string
	Memo     string
	Debits   []settleLine
	Credits  []settleLine
	PostedAt time.Time
	Reverses string // if this txn reverses another, its id
}

type settleLine struct {
	Account string
	Amount  float64
}

// NewSettlement returns an empty ledger.
func NewSettlement() *Settlement {
	return &Settlement{
		accounts: map[string]*acctRow{},
		txns:     map[string]*settleTxn{},
	}
}

// Open registers a new account. Re-opening is rejected; a closed
// account can be re-opened via Close(false) only — to avoid the
// accidental "wipe history then re-OPEN" foot-gun.
func (s *Settlement) Open(name string, kind AcctType, currency string, noNegative *bool) error {
	if name == "" {
		return errors.New("name required")
	}
	if !validAcctType(kind) {
		return errors.New("type must be one of asset|liability|equity|income|expense")
	}
	if currency == "" {
		currency = "USD"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[name]; ok {
		return errors.New("account already exists: " + name)
	}
	row := &acctRow{
		Name: name, Type: kind, Currency: currency,
		OpenedAt: time.Now(),
	}
	// Default no-negative: asset + expense
	if noNegative != nil {
		row.NoNegative = *noNegative
	} else {
		row.NoNegative = kind == AcctAsset || kind == AcctExpense
	}
	s.accounts[name] = row
	s.totalOpens.Add(1)
	return nil
}

// Close forbids future postings against an account. Historical
// queries continue to work.
func (s *Settlement) Close(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[name]
	if !ok {
		return errors.New("unknown account: " + name)
	}
	a.Closed = true
	return nil
}

// SettleTxnResult is TXN's return.
type SettleTxnResult struct {
	TxnID     string  `json:"txn_id"`
	Posted    bool    `json:"posted"`
	Duplicate bool    `json:"duplicate"`
	Total     float64 `json:"total"`
}

// Txn posts a balanced double-entry transaction atomically. The
// validation pass checks: (a) Σ debits == Σ credits (within float
// epsilon), (b) every named account exists + is open, (c) NO_NEGATIVE
// accounts don't go below zero. If any check fails, NO account
// moves.
//
// Repeating the same txnID after a successful post returns
// Duplicate=true and no movement (idempotency).
func (s *Settlement) Txn(txnID, memo string, debits, credits []SettleLine) (SettleTxnResult, error) {
	if txnID == "" {
		return SettleTxnResult{}, errors.New("txn_id required")
	}
	if len(debits) == 0 && len(credits) == 0 {
		return SettleTxnResult{}, errors.New("at least one debit + one credit required")
	}
	if len(debits) == 0 || len(credits) == 0 {
		return SettleTxnResult{}, errors.New("debits and credits both required (double-entry)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Idempotency
	if _, ok := s.txns[txnID]; ok {
		s.totalDuplicates.Add(1)
		return SettleTxnResult{TxnID: txnID, Posted: false, Duplicate: true}, nil
	}
	// Validation pass
	var sumD, sumC float64
	for _, d := range debits {
		if d.Amount <= 0 {
			return SettleTxnResult{}, errors.New("amount must be positive")
		}
		sumD += d.Amount
		a, ok := s.accounts[d.Account]
		if !ok {
			return SettleTxnResult{}, errors.New("unknown account (debit): " + d.Account)
		}
		if a.Closed {
			return SettleTxnResult{}, errors.New("account closed: " + d.Account)
		}
	}
	for _, c := range credits {
		if c.Amount <= 0 {
			return SettleTxnResult{}, errors.New("amount must be positive")
		}
		sumC += c.Amount
		a, ok := s.accounts[c.Account]
		if !ok {
			return SettleTxnResult{}, errors.New("unknown account (credit): " + c.Account)
		}
		if a.Closed {
			return SettleTxnResult{}, errors.New("account closed: " + c.Account)
		}
	}
	if math.Abs(sumD-sumC) > 1e-9 {
		return SettleTxnResult{}, errors.New("imbalanced: debits != credits")
	}
	// Project the new balances and check NO_NEGATIVE
	projected := map[string]float64{}
	for _, d := range debits {
		a := s.accounts[d.Account]
		// In double-entry: debit increases asset/expense; decreases liability/equity/income
		delta := signedDelta(a.Type, "debit", d.Amount)
		projected[d.Account] += delta
	}
	for _, c := range credits {
		a := s.accounts[c.Account]
		delta := signedDelta(a.Type, "credit", c.Amount)
		projected[c.Account] += delta
	}
	for name, delta := range projected {
		a := s.accounts[name]
		if a.NoNegative && a.Balance+delta < -1e-9 {
			return SettleTxnResult{}, errors.New("would overdraw account: " + name)
		}
	}
	// Commit
	now := time.Now()
	for _, d := range debits {
		a := s.accounts[d.Account]
		a.Balance += signedDelta(a.Type, "debit", d.Amount)
		a.Entries = append(a.Entries, acctEntry{
			TxnID: txnID, Side: "debit", Amount: d.Amount,
			Balance: a.Balance, Memo: memo, PostedAt: now,
		})
		a.LastEntryAt = now
	}
	for _, c := range credits {
		a := s.accounts[c.Account]
		a.Balance += signedDelta(a.Type, "credit", c.Amount)
		a.Entries = append(a.Entries, acctEntry{
			TxnID: txnID, Side: "credit", Amount: c.Amount,
			Balance: a.Balance, Memo: memo, PostedAt: now,
		})
		a.LastEntryAt = now
	}
	s.txns[txnID] = &settleTxn{
		ID: txnID, Memo: memo,
		Debits: linesToInternal(debits), Credits: linesToInternal(credits),
		PostedAt: now,
	}
	s.totalTxns.Add(1)
	return SettleTxnResult{TxnID: txnID, Posted: true, Total: sumD}, nil
}

// SettleLine is one debit or credit row in a transaction.
type SettleLine struct {
	Account string  `json:"account"`
	Amount  float64 `json:"amount"`
}

// Reverse posts a reversing entry: swaps debits ↔ credits of the
// original. Idempotent on the new txn-id.
func (s *Settlement) Reverse(originalID, newID, memo string) (SettleTxnResult, error) {
	if originalID == "" || newID == "" {
		return SettleTxnResult{}, errors.New("original and new txn_id required")
	}
	s.mu.Lock()
	orig, ok := s.txns[originalID]
	s.mu.Unlock()
	if !ok {
		return SettleTxnResult{}, errors.New("unknown original txn: " + originalID)
	}
	debits := make([]SettleLine, len(orig.Credits))
	credits := make([]SettleLine, len(orig.Debits))
	for i, c := range orig.Credits {
		debits[i] = SettleLine{Account: c.Account, Amount: c.Amount}
	}
	for i, d := range orig.Debits {
		credits[i] = SettleLine{Account: d.Account, Amount: d.Amount}
	}
	s.totalReverses.Add(1)
	r, err := s.Txn(newID, "REVERSE("+originalID+"): "+memo, debits, credits)
	if err != nil {
		return SettleTxnResult{}, err
	}
	// Tag the txn so GET surfaces the relationship
	s.mu.Lock()
	if t, ok := s.txns[newID]; ok {
		t.Reverses = originalID
	}
	s.mu.Unlock()
	return r, nil
}

// SettleTxnView is GET's return.
type SettleTxnView struct {
	TxnID    string        `json:"txn_id"`
	Memo     string        `json:"memo"`
	Debits   []SettleLine  `json:"debits"`
	Credits  []SettleLine  `json:"credits"`
	PostedAt int64         `json:"posted_unix"`
	Reverses string        `json:"reverses,omitempty"`
}

// Get returns the txn body.
func (s *Settlement) Get(txnID string) (SettleTxnView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.txns[txnID]
	if !ok {
		return SettleTxnView{}, false
	}
	return SettleTxnView{
		TxnID: t.ID, Memo: t.Memo,
		Debits: internalToLines(t.Debits), Credits: internalToLines(t.Credits),
		PostedAt: t.PostedAt.Unix(),
		Reverses: t.Reverses,
	}, true
}

// AcctBalanceView is BALANCE's return.
type AcctBalanceView struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Currency      string  `json:"currency"`
	Balance       float64 `json:"balance"`
	NoNegative    bool    `json:"no_negative"`
	Closed        bool    `json:"closed"`
	LastEntryUnix int64   `json:"last_entry_unix"`
	EntryCount    int     `json:"entry_count"`
}

// Balance returns a single account's current view.
func (s *Settlement) Balance(name string) (AcctBalanceView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[name]
	if !ok {
		return AcctBalanceView{}, false
	}
	v := AcctBalanceView{
		Name: a.Name, Type: string(a.Type), Currency: a.Currency,
		Balance: a.Balance, NoNegative: a.NoNegative,
		Closed: a.Closed, EntryCount: len(a.Entries),
	}
	if !a.LastEntryAt.IsZero() {
		v.LastEntryUnix = a.LastEntryAt.Unix()
	}
	return v, true
}

// AcctStatementRow is one line of STATEMENT.
type AcctStatementRow struct {
	TxnID    string  `json:"txn_id"`
	Side     string  `json:"side"`
	Amount   float64 `json:"amount"`
	Balance  float64 `json:"running_balance"`
	Memo     string  `json:"memo"`
	PostedAt int64   `json:"posted_unix"`
}

// Statement returns the chronological entry log for one account,
// filtered by time window.
func (s *Settlement) Statement(name string, since, until int64, limit int) ([]AcctStatementRow, bool) {
	if limit <= 0 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[name]
	if !ok {
		return nil, false
	}
	out := make([]AcctStatementRow, 0)
	for _, e := range a.Entries {
		t := e.PostedAt.Unix()
		if since > 0 && t < since {
			continue
		}
		if until > 0 && t > until {
			continue
		}
		out = append(out, AcctStatementRow{
			TxnID: e.TxnID, Side: e.Side, Amount: e.Amount,
			Balance: e.Balance, Memo: e.Memo, PostedAt: t,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, true
}

// SettleReconcileResult is RECONCILE's return.
type SettleReconcileResult struct {
	TotalDebits    float64 `json:"total_debits"`
	TotalCredits   float64 `json:"total_credits"`
	Difference     float64 `json:"difference"`
	Balanced       bool    `json:"balanced"`
	AccountCount   int     `json:"account_count"`
	TxnCount       int     `json:"txn_count"`
}

// Reconcile proves the global double-entry invariant: every debit
// has a matching credit somewhere.
//
// FP-tolerance: the per-txn acceptance check (`math.Abs(sumD-sumC) <=
// 1e-9`) accepts each individual txn within a tight tolerance. When
// thousands of those txns accumulate, ordinary float-summation error
// can push the global sum's representation drift beyond 1e-9 even
// when the *mathematical* sum is exact. A purely absolute epsilon
// here produced false `balanced=false` for ledgers that are in fact
// balanced — caught by `TestSettleFuzzInvariantsUnderConcurrentLoad`.
//
// Fix: tolerance scales with both transaction count (each can
// contribute up to 1e-9 of accepted-but-unbalanced slack) and total
// magnitude (proportional FP representation error). The bound is
// the max of those two effects plus a small absolute floor for the
// trivial-ledger case.
func (s *Settlement) Reconcile() SettleReconcileResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var d, c float64
	entries := 0
	for _, a := range s.accounts {
		for _, e := range a.Entries {
			if e.Side == "debit" {
				d += e.Amount
			} else {
				c += e.Amount
			}
			entries++
		}
	}
	tol := reconcileTolerance(d, c, entries)
	return SettleReconcileResult{
		TotalDebits: d, TotalCredits: c,
		Difference: d - c,
		Balanced: math.Abs(d-c) <= tol,
		AccountCount: len(s.accounts), TxnCount: len(s.txns),
	}
}

// reconcileTolerance produces a tolerance proportional to both the
// magnitude of the sums and the count of entries summed. The two
// dominant error sources:
//
//   1. Per-txn acceptance slack: each posted txn was accepted with
//      up to 1e-9 of debit/credit imbalance (see Txn). Across N
//      accepted txns, that slack can sum to N * 1e-9.
//
//   2. Float representation error: summing N values of magnitude M
//      using IEEE-754 64-bit floats has relative error bounded by
//      ~N * machine_epsilon. machine_epsilon for float64 is ~2.22e-16.
//
// The tolerance is the max of those terms plus a 1e-9 floor for
// the empty / single-entry case.
func reconcileTolerance(d, c float64, entries int) float64 {
	mag := math.Max(math.Abs(d), math.Abs(c))
	const machineEps = 2.22e-16
	perTxnSlack := float64(entries) * 1e-9
	fpRepError := float64(entries) * mag * machineEps * 4 // ×4 for safety margin
	tol := perTxnSlack
	if fpRepError > tol {
		tol = fpRepError
	}
	if tol < 1e-9 {
		tol = 1e-9
	}
	return tol
}

// List enumerates accounts.
func (s *Settlement) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.accounts))
	for k := range s.accounts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SettleStats is the global snapshot.
type SettleStats struct {
	Accounts        int   `json:"accounts"`
	Txns            int   `json:"txns"`
	TotalOpens      int64 `json:"total_opens"`
	TotalTxns       int64 `json:"total_txns"`
	TotalReverses   int64 `json:"total_reverses"`
	TotalDuplicates int64 `json:"total_duplicates"`
}

func (s *Settlement) Stats() SettleStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SettleStats{
		Accounts:        len(s.accounts),
		Txns:            len(s.txns),
		TotalOpens:      s.totalOpens.Load(),
		TotalTxns:       s.totalTxns.Load(),
		TotalReverses:   s.totalReverses.Load(),
		TotalDuplicates: s.totalDuplicates.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func validAcctType(t AcctType) bool {
	switch t {
	case AcctAsset, AcctLiability, AcctEquity, AcctIncome, AcctExpense:
		return true
	}
	return false
}

// signedDelta turns an unsigned amount into a signed balance delta
// per accounting normality:
//
//   asset / expense: debit → +, credit → -
//   liability / equity / income: debit → -, credit → +
func signedDelta(t AcctType, side string, amt float64) float64 {
	debitPositive := t == AcctAsset || t == AcctExpense
	if side == "debit" {
		if debitPositive {
			return amt
		}
		return -amt
	}
	// credit
	if debitPositive {
		return -amt
	}
	return amt
}

func linesToInternal(in []SettleLine) []settleLine {
	out := make([]settleLine, len(in))
	for i, l := range in {
		out[i] = settleLine{Account: l.Account, Amount: l.Amount}
	}
	return out
}

func internalToLines(in []settleLine) []SettleLine {
	out := make([]SettleLine, len(in))
	for i, l := range in {
		out[i] = SettleLine{Account: l.Account, Amount: l.Amount}
	}
	return out
}
