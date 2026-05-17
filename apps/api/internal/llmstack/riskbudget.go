package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// RiskBudgets accumulates per-session hallucination risk. Distinct
// from token/cost budgets: each low-GROUND answer debits a risk
// balance, and once the balance is exhausted the orchestrator is
// forced to verify (require GROUND.CHECK) or escalate (route to a
// stronger model) before serving more answers from that session.
//
// Why this is its own primitive: cost budgets cap *spend*; risk
// budgets cap *unsafe output*. A session with a 100-token quota
// could still deliver 100 low-confidence answers and accumulate a
// catastrophic hallucination probability. The two are orthogonal.
//
// The debit model:
//   - score in [0,1], where 1 = high-confidence (no debit) and 0 =
//     no grounding at all (max debit).
//   - debit = (1 - score) * weight. Default weight = 1.0.
//   - balance starts at budget and decrements.
//   - balance <= 0 → enforce=true (caller must escalate/verify).
//
// Commands:
//
//   RISK.BUDGET.SET session budget [WEIGHT w]
//        Sets the session's risk budget. budget=0 → use default (10.0).
//   RISK.BUDGET.DEBIT session score [REASON r]
//        score is the answer's confidence (typically GROUND.CHECK
//        output). Returns balance + enforce flag.
//   RISK.BUDGET.STATUS session
//        → balance / budget / enforce / debits / mean_score
//   RISK.BUDGET.RESET session|ALL
//   RISK.BUDGET.LIST
//   RISK.BUDGET.STATS
//
// Hot path: DEBIT is one map lookup + atomic float math.
type RiskBudgets struct {
	mu       sync.RWMutex
	sessions map[string]*riskSess

	totalDebits   atomic.Int64
	totalEnforced atomic.Int64
}

type riskSess struct {
	mu      sync.Mutex
	budget  float64
	balance float64
	weight  float64
	debits  int64
	sumScore float64
	lastReason string
}

const defaultRiskBudget = 10.0

// NewRiskBudgets returns an empty registry.
func NewRiskBudgets() *RiskBudgets {
	return &RiskBudgets{sessions: map[string]*riskSess{}}
}

// Set configures a session. budget=0 → default. weight=0 → 1.0.
// Resetting a session via Set zeros prior debits and balance.
func (r *RiskBudgets) Set(session string, budget, weight float64) error {
	if session == "" {
		return errors.New("session required")
	}
	if budget < 0 {
		return errors.New("budget must be non-negative")
	}
	if budget == 0 {
		budget = defaultRiskBudget
	}
	if weight < 0 {
		return errors.New("weight must be non-negative")
	}
	if weight == 0 {
		weight = 1.0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session] = &riskSess{budget: budget, balance: budget, weight: weight}
	return nil
}

// RiskDebitResult is DEBIT's return.
type RiskDebitResult struct {
	Balance float64 `json:"balance"`
	Budget  float64 `json:"budget"`
	Enforce bool    `json:"enforce"`
	Debited float64 `json:"debited"`
}

// Debit reduces the session's balance by (1-score)*weight. Sessions
// that haven't been SET are auto-created with default budget. Returns
// Enforce=true once the balance falls to or below zero — the caller
// reads this and routes the next request through GROUND/JUDGE.
func (r *RiskBudgets) Debit(session string, score float64, reason string) (RiskDebitResult, error) {
	if session == "" {
		return RiskDebitResult{}, errors.New("session required")
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	r.totalDebits.Add(1)
	s := r.sessOrCreate(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	debit := (1 - score) * s.weight
	s.balance -= debit
	s.debits++
	s.sumScore += score
	if reason != "" {
		s.lastReason = reason
	}
	out := RiskDebitResult{
		Balance: s.balance, Budget: s.budget, Debited: debit,
	}
	if s.balance <= 0 {
		out.Enforce = true
		r.totalEnforced.Add(1)
	}
	return out, nil
}

// RiskStatus is STATUS's return.
type RiskStatus struct {
	Session    string  `json:"session"`
	Budget     float64 `json:"budget"`
	Balance    float64 `json:"balance"`
	Debits     int64   `json:"debits"`
	MeanScore  float64 `json:"mean_score"`
	Enforce    bool    `json:"enforce"`
	LastReason string  `json:"last_reason,omitempty"`
}

// Status returns the non-blocking snapshot.
func (r *RiskBudgets) Status(session string) (RiskStatus, bool) {
	r.mu.RLock()
	s, ok := r.sessions[session]
	r.mu.RUnlock()
	if !ok {
		return RiskStatus{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RiskStatus{
		Session: session, Budget: s.budget, Balance: s.balance,
		Debits: s.debits, Enforce: s.balance <= 0, LastReason: s.lastReason,
	}
	if s.debits > 0 {
		out.MeanScore = s.sumScore / float64(s.debits)
	}
	return out, true
}

// Reset wipes a session (or all). session="ALL" wipes all.
func (r *RiskBudgets) Reset(session string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session == "ALL" {
		n := len(r.sessions)
		r.sessions = map[string]*riskSess{}
		return n
	}
	if _, ok := r.sessions[session]; ok {
		delete(r.sessions, session)
		return 1
	}
	return 0
}

// List returns every session id.
func (r *RiskBudgets) List() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.sessions))
	for k := range r.sessions {
		out = append(out, k)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// RiskStats is the global snapshot.
type RiskStats struct {
	Sessions      int   `json:"sessions"`
	TotalDebits   int64 `json:"total_debits"`
	TotalEnforced int64 `json:"total_enforced"`
}

func (r *RiskBudgets) Stats() RiskStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RiskStats{
		Sessions:      len(r.sessions),
		TotalDebits:   r.totalDebits.Load(),
		TotalEnforced: r.totalEnforced.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (r *RiskBudgets) sessOrCreate(session string) *riskSess {
	r.mu.RLock()
	s, ok := r.sessions[session]
	r.mu.RUnlock()
	if ok {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[session]; ok {
		return s
	}
	s = &riskSess{budget: defaultRiskBudget, balance: defaultRiskBudget, weight: 1.0}
	r.sessions[session] = s
	return s
}
