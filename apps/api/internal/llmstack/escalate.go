package llmstack

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// EscalationLadder is the conductor for primitives the stack already
// owns separately: CONFIDENCE, NOVELTY, CASCADE, CACHE.LAYERS,
// SHADOW.EVAL, JURY, ANSWER.CANARY. Every instrument, no conductor.
// Production teams end up writing this exact dispatcher by hand —
// a Python rules engine or a CEL/expr DSL that says:
//
//   if cache_score >= 0.90:                serve_from_cache()
//   elif novelty < 0.4 and confidence >= 0.7: cheap_model()
//   elif novelty > 0.85 or confidence < 0.3:  escalate_to_human()
//   else:                                  expensive_model()
//
// ESCALATE.* ships that rules engine as a first-class primitive: a
// named policy with per-tier expressions evaluated in priority
// order (cache → cheap → expensive → human). DECIDE returns the
// winning tier plus the reason + signal values that fired it — so
// observability of *why* a decision was made is automatic, not
// glued on with print statements.
//
// Commands:
//
//   ESCALATE.CONFIG policy-id [CACHE_IF expr] [CHEAP_IF expr]
//        [EXPENSIVE_IF expr] [HUMAN_IF expr]
//        Set tier expressions. Missing tiers fall through.
//   ESCALATE.DECIDE policy-id [signal=value ...]
//        → {tier, reason, signals:{...}}
//        signal names are free-form (cache_score, novelty,
//        confidence, latency_p95, cost_per_1k, ...). Comparison
//        operators: >= <= > < ==.
//   ESCALATE.RECORD policy-id tier outcome [QUALITY q]
//        Close the loop: log what tier was used + how it went.
//        Quality ∈ [0,1].
//   ESCALATE.REPORT policy-id
//        → per-tier counts, mean quality, outcome breakdown.
//   ESCALATE.RESET policy-id|ALL
//   ESCALATE.LIST
//   ESCALATE.POLICY policy-id     → current per-tier expressions
//   ESCALATE.STATS
//
// Hot path: DECIDE walks at most 4 expressions; each expression is
// pre-compiled into a flat AST of comparison clauses joined by AND/OR.
// O(clauses) per signal lookup, single-digit microseconds.
type EscalationLadder struct {
	mu       sync.RWMutex
	policies map[string]*escalatePolicy

	totalDecisions atomic.Int64
	totalRecords   atomic.Int64
}

type escalatePolicy struct {
	mu          sync.RWMutex
	exprs       map[string]*escalateExpr // tier → compiled expr
	tierStats   map[string]*escalateTier
}

type escalateTier struct {
	Count       int64
	QualitySum  float64
	QualityN    int64
	OutcomeWin  int64
	OutcomeLose int64
}

// NewEscalationLadder returns an empty store.
func NewEscalationLadder() *EscalationLadder {
	return &EscalationLadder{policies: map[string]*escalatePolicy{}}
}

// validTiers is the priority order DECIDE walks. cache (cheapest)
// first, human (most expensive) last. A policy without a HUMAN_IF
// expression treats human as the implicit fallback.
var validTiers = []string{"cache", "cheap", "expensive", "human"}

func isValidTier(t string) bool {
	for _, v := range validTiers {
		if v == t {
			return true
		}
	}
	return false
}

// Configure sets / replaces tier expressions for a policy. Empty
// expression strings keep prior; pass "-" to clear a tier.
func (e *EscalationLadder) Configure(policyID string, exprs map[string]string) error {
	if policyID == "" {
		return errors.New("policy_id required")
	}
	for tier, expr := range exprs {
		if !isValidTier(tier) {
			return errors.New("unknown tier (must be cache | cheap | expensive | human): " + tier)
		}
		if expr == "" || expr == "-" {
			continue
		}
		if _, err := compileEscalateExpr(expr); err != nil {
			return errors.New("tier " + tier + ": " + err.Error())
		}
	}
	p := e.policyOrCreate(policyID)
	p.mu.Lock()
	defer p.mu.Unlock()
	for tier, expr := range exprs {
		if expr == "-" {
			delete(p.exprs, tier)
			continue
		}
		if expr == "" {
			continue
		}
		compiled, _ := compileEscalateExpr(expr) // validated above
		p.exprs[tier] = compiled
	}
	return nil
}

// EscalateDecision is DECIDE's return.
type EscalateDecision struct {
	PolicyID string             `json:"policy_id"`
	Tier     string             `json:"tier"`
	Reason   string             `json:"reason"`
	Signals  map[string]float64 `json:"signals"`
}

// Decide evaluates each tier expression in priority order; first
// match wins. Returns the implicit fallback tier ("expensive") when
// no expression matches and no HUMAN_IF is configured.
func (e *EscalationLadder) Decide(policyID string, signals map[string]float64) (EscalateDecision, error) {
	if policyID == "" {
		return EscalateDecision{}, errors.New("policy_id required")
	}
	e.totalDecisions.Add(1)
	e.mu.RLock()
	p, ok := e.policies[policyID]
	e.mu.RUnlock()
	if !ok {
		return EscalateDecision{}, errors.New("unknown policy_id: " + policyID)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := EscalateDecision{PolicyID: policyID, Signals: signals}
	for _, tier := range validTiers {
		expr, ok := p.exprs[tier]
		if !ok {
			continue
		}
		matched, reason := expr.eval(signals)
		if matched {
			out.Tier = tier
			out.Reason = reason
			return out, nil
		}
	}
	out.Tier = "expensive"
	out.Reason = "no tier gate matched — default expensive"
	return out, nil
}

// Record logs that the orchestrator served from `tier` with the
// reported outcome. quality is optional (passed as 0 to skip).
func (e *EscalationLadder) Record(policyID, tier, outcome string, quality float64) error {
	if policyID == "" {
		return errors.New("policy_id required")
	}
	if !isValidTier(tier) {
		return errors.New("tier must be cache | cheap | expensive | human")
	}
	if quality < 0 || quality > 1 {
		return errors.New("quality must be in [0,1]")
	}
	e.totalRecords.Add(1)
	p := e.policyOrCreate(policyID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tierStats == nil {
		p.tierStats = map[string]*escalateTier{}
	}
	t, ok := p.tierStats[tier]
	if !ok {
		t = &escalateTier{}
		p.tierStats[tier] = t
	}
	t.Count++
	if quality > 0 {
		t.QualitySum += quality
		t.QualityN++
	}
	switch strings.ToLower(outcome) {
	case "win", "resolved", "good":
		t.OutcomeWin++
	case "lose", "fail", "bad":
		t.OutcomeLose++
	}
	return nil
}

// EscalateReportRow is one tier row of REPORT.
type EscalateReportRow struct {
	Tier         string  `json:"tier"`
	Count        int64   `json:"count"`
	MeanQuality  float64 `json:"mean_quality"`
	OutcomeWin   int64   `json:"outcome_win"`
	OutcomeLose  int64   `json:"outcome_lose"`
}

// Report returns per-tier rows in priority order.
func (e *EscalationLadder) Report(policyID string) ([]EscalateReportRow, bool) {
	e.mu.RLock()
	p, ok := e.policies[policyID]
	e.mu.RUnlock()
	if !ok {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]EscalateReportRow, 0, len(validTiers))
	for _, tier := range validTiers {
		t, ok := p.tierStats[tier]
		if !ok {
			out = append(out, EscalateReportRow{Tier: tier})
			continue
		}
		row := EscalateReportRow{
			Tier:        tier,
			Count:       t.Count,
			OutcomeWin:  t.OutcomeWin,
			OutcomeLose: t.OutcomeLose,
		}
		if t.QualityN > 0 {
			row.MeanQuality = t.QualitySum / float64(t.QualityN)
		}
		out = append(out, row)
	}
	return out, true
}

// EscalatePolicyRow is one row of POLICY output.
type EscalatePolicyRow struct {
	Tier string `json:"tier"`
	Expr string `json:"expr"`
}

// Policy returns the current per-tier expressions for one policy.
func (e *EscalationLadder) Policy(policyID string) ([]EscalatePolicyRow, bool) {
	e.mu.RLock()
	p, ok := e.policies[policyID]
	e.mu.RUnlock()
	if !ok {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]EscalatePolicyRow, 0, len(validTiers))
	for _, tier := range validTiers {
		if ex, ok := p.exprs[tier]; ok {
			out = append(out, EscalatePolicyRow{Tier: tier, Expr: ex.source})
		}
	}
	return out, true
}

// List returns every policy id, sorted.
func (e *EscalationLadder) List() []string {
	e.mu.RLock()
	out := make([]string, 0, len(e.policies))
	for k := range e.policies {
		out = append(out, k)
	}
	e.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a policy. policyID="ALL" wipes all.
func (e *EscalationLadder) Reset(policyID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if policyID == "ALL" {
		n := len(e.policies)
		e.policies = map[string]*escalatePolicy{}
		return n
	}
	if _, ok := e.policies[policyID]; ok {
		delete(e.policies, policyID)
		return 1
	}
	return 0
}

// EscalateStats is the global snapshot.
type EscalateStats struct {
	Policies       int   `json:"policies"`
	TotalDecisions int64 `json:"total_decisions"`
	TotalRecords   int64 `json:"total_records"`
}

func (e *EscalationLadder) Stats() EscalateStats {
	e.mu.RLock()
	n := len(e.policies)
	e.mu.RUnlock()
	return EscalateStats{
		Policies:       n,
		TotalDecisions: e.totalDecisions.Load(),
		TotalRecords:   e.totalRecords.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (e *EscalationLadder) policyOrCreate(id string) *escalatePolicy {
	e.mu.RLock()
	p, ok := e.policies[id]
	e.mu.RUnlock()
	if ok {
		return p
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.policies[id]; ok {
		return p
	}
	p = &escalatePolicy{
		exprs:     map[string]*escalateExpr{},
		tierStats: map[string]*escalateTier{},
	}
	e.policies[id] = p
	return p
}

// escalateExpr is a compiled expression of the form
//   clause [ (AND|OR) clause ]*
// where clause = name OP value, OP ∈ {>=, <=, >, <, ==}.
// Single-pass left-to-right evaluation (no operator precedence — AND
// and OR bind equally, which keeps the rule grammar predictable for
// operators reading it cold).
type escalateExpr struct {
	source  string
	clauses []escalateClause
	joins   []string // "AND" or "OR" between consecutive clauses
}

type escalateClause struct {
	name string
	op   string
	val  float64
}

func compileEscalateExpr(s string) (*escalateExpr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty expression")
	}
	out := &escalateExpr{source: s}
	tokens := tokenizeExpr(s)
	expectingClause := true
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if expectingClause {
			if i+2 >= len(tokens) {
				return nil, errors.New("incomplete clause at '" + tok + "'")
			}
			op := tokens[i+1]
			val, err := strconv.ParseFloat(tokens[i+2], 64)
			if err != nil {
				return nil, errors.New("value must be numeric, got '" + tokens[i+2] + "'")
			}
			if !isCompareOp(op) {
				return nil, errors.New("operator must be >= <= > < ==, got '" + op + "'")
			}
			out.clauses = append(out.clauses, escalateClause{name: tok, op: op, val: val})
			i += 2
			expectingClause = false
		} else {
			up := strings.ToUpper(tok)
			if up != "AND" && up != "OR" {
				return nil, errors.New("expected AND/OR, got '" + tok + "'")
			}
			out.joins = append(out.joins, up)
			expectingClause = true
		}
	}
	if expectingClause {
		return nil, errors.New("expression ends with a joiner")
	}
	return out, nil
}

func tokenizeExpr(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '>' || c == '<' || c == '=':
			flush()
			b.WriteByte(c)
			if i+1 < len(s) && s[i+1] == '=' {
				b.WriteByte('=')
				i++
			}
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}

func isCompareOp(op string) bool {
	switch op {
	case ">=", "<=", ">", "<", "==":
		return true
	}
	return false
}

// eval walks clauses left-to-right combining with joins. Returns
// (matched, reason). reason names the first satisfied clause.
func (e *escalateExpr) eval(signals map[string]float64) (bool, string) {
	if len(e.clauses) == 0 {
		return false, ""
	}
	result := evalClause(e.clauses[0], signals)
	reason := clauseReason(e.clauses[0], signals)
	for i, j := range e.joins {
		if i+1 >= len(e.clauses) {
			break
		}
		next := evalClause(e.clauses[i+1], signals)
		switch j {
		case "AND":
			result = result && next
			if !next {
				reason = "failed: " + clauseReason(e.clauses[i+1], signals)
			}
		case "OR":
			if next && !result {
				reason = "matched: " + clauseReason(e.clauses[i+1], signals)
			}
			result = result || next
		}
	}
	if result {
		return true, reason
	}
	return false, reason
}

func evalClause(c escalateClause, signals map[string]float64) bool {
	got, ok := signals[c.name]
	if !ok {
		return false
	}
	switch c.op {
	case ">=":
		return got >= c.val
	case "<=":
		return got <= c.val
	case ">":
		return got > c.val
	case "<":
		return got < c.val
	case "==":
		return got == c.val
	}
	return false
}

func clauseReason(c escalateClause, signals map[string]float64) string {
	got, ok := signals[c.name]
	if !ok {
		return c.name + " not provided"
	}
	return c.name + "=" + strconv.FormatFloat(got, 'g', -1, 64) + " " + c.op + " " + strconv.FormatFloat(c.val, 'g', -1, 64)
}
