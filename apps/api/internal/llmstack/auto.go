package llmstack

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AutoRules is the closed-loop rules engine. Every detector primitive
// (VECSPACE.HEALTH, DRIFT, RAG.GAP, TRUST.SCORE, FORECAST, TOOLDRIFT)
// requires the application to poll and react. That's the wrong
// shape — the engine sees the signal first and the app finds out
// late. AUTO lets the engine close the loop itself: a trigger
// condition over any primitive's verdict, bound to an action.
//
// This is a category shift. NeuroCache stops being a thing you query
// and becomes a thing that acts.
//
// Rule definition:
//
//   WHEN <metric> <op> <value>
//        Currently supported metrics:
//          - vecspace.<space>.verdict       (HEALTHY/DEGRADED/COLLAPSED/INSUFFICIENT)
//          - vecspace.<space>.cosine        (mean pairwise cosine)
//          - trust.<entity>.score           (Bayesian mean)
//          - trust.<entity>.n               (observation count)
//          - risk.<session>.enforce         (0/1)
//          - risk.<session>.balance         (float)
//          - market.<id>.price              (clearing price)
//          - market.<id>.starved            (count of agents starved)
//          - cfcache.hit_rate               (global hit rate)
//        Ops: ==, !=, <, <=, >, >=
//
//   DO <command-template>
//        Free-form string — the action runner is application-supplied
//        (the engine doesn't execute commands on its own; AUTO produces
//        a FIRE event that the application listens to). Keeps the
//        primitive sandbox-safe.
//
// The flow:
//
//   1. App posts AUTO.RULE registering a (WHEN, DO) pair.
//   2. App periodically calls AUTO.EVALUATE (or any state change
//      explicitly triggers it via the engine). Each eligible rule
//      that newly transitions from false→true fires once and is
//      recorded in FIRES.
//   3. Edge-triggered: a rule that stays true does NOT re-fire until
//      its condition first goes back to false (or cooldown expires).
//
// Commands:
//
//   AUTO.RULE rule-id WHEN "<condition>" DO "<action>" [COOLDOWN ms]
//   AUTO.UNRULE rule-id
//   AUTO.EVALUATE [LIMIT n]      — evaluate all rules, return fires
//   AUTO.DRYRUN rule-id          — would this rule fire right now?
//   AUTO.FIRES [RULE r] [LIMIT n] — audit trail of fires
//   AUTO.PAUSE rule-id           — disable without removing
//   AUTO.RESUME rule-id
//   AUTO.LIST
//   AUTO.GET rule-id
//   AUTO.STATS
//
// AUTO does not execute commands — it produces FIRE events. The
// application's dispatcher reads them and invokes whatever the
// action string maps to. This is intentional: the engine refusing to
// self-exec keeps the security model simple ("a registered AUTO rule
// can only DO what the calling application chooses to honour").
type AutoRules struct {
	mu    sync.RWMutex
	rules map[string]*autoRule
	fires []autoFire

	totalEvals  atomic.Int64
	totalFires  atomic.Int64

	// snapshot adapters — wired by the engine after construction
	evalContext AutoEvalContext
}

// AutoEvalContext is the read-only state surface the rule evaluator
// consults. The engine implements this against the live state of the
// other llmstack primitives.
type AutoEvalContext interface {
	VecSpaceVerdict(space string) (string, bool)
	VecSpaceMeanCosine(space string) (float64, bool)
	TrustScore(entity string) (float64, int64, bool)
	RiskEnforce(session string) (bool, bool) // (enforce, found)
	RiskBalance(session string) (float64, bool)
	MarketPrice(market string) (float64, bool)
	MarketStarvedCount(market string) (int, bool)
	CFCacheHitRate() (float64, bool)
}

type autoRule struct {
	ID         string
	Condition  string // raw "WHEN" expression
	Action     string
	Cooldown   time.Duration
	Paused     bool
	LastFired  time.Time
	WasTrue    bool // for edge-trigger
	CreatedAt  time.Time
}

type autoFire struct {
	RuleID   string    `json:"rule_id"`
	Action   string    `json:"action"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
}

// NewAutoRules returns an empty registry. The context is nil-safe
// until SetContext is called — evaluation of rules whose metrics
// touch unset adapters returns the rule as "unknown" (not fired).
func NewAutoRules() *AutoRules {
	return &AutoRules{rules: map[string]*autoRule{}}
}

// SetContext wires the engine's live state into the evaluator. Safe
// to call multiple times.
func (a *AutoRules) SetContext(ctx AutoEvalContext) {
	a.mu.Lock()
	a.evalContext = ctx
	a.mu.Unlock()
}

// Rule registers (or replaces) one rule.
func (a *AutoRules) Rule(id, condition, action string, cooldown time.Duration) error {
	if id == "" {
		return errors.New("rule_id required")
	}
	if condition == "" {
		return errors.New("WHEN condition required")
	}
	if action == "" {
		return errors.New("DO action required")
	}
	if cooldown < 0 {
		return errors.New("cooldown must be non-negative")
	}
	// Validate the condition parses
	if _, err := parseAutoCondition(condition); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rules[id] = &autoRule{
		ID: id, Condition: condition, Action: action,
		Cooldown: cooldown, CreatedAt: time.Now(),
	}
	return nil
}

// Unrule drops a rule.
func (a *AutoRules) Unrule(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.rules[id]; ok {
		delete(a.rules, id)
		return 1
	}
	return 0
}

// Pause / Resume disable / re-enable without dropping the rule (so the
// cooldown + edge-trigger state survive).
func (a *AutoRules) Pause(id string) error  { return a.setPaused(id, true) }
func (a *AutoRules) Resume(id string) error { return a.setPaused(id, false) }

func (a *AutoRules) setPaused(id string, paused bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rules[id]
	if !ok {
		return errors.New("unknown rule_id: " + id)
	}
	r.Paused = paused
	return nil
}

// AutoFire is one FIRE event. Returned by EVALUATE and recorded in FIRES.
type AutoFire struct {
	RuleID string `json:"rule_id"`
	Action string `json:"action"`
	AtUnix int64  `json:"at_unix"`
	Reason string `json:"reason"`
}

// Evaluate runs every non-paused rule once. Returns the fires that
// just happened on this call. Edge-triggered: a rule that was already
// true and is still true does NOT re-fire.
func (a *AutoRules) Evaluate(limit int) []AutoFire {
	if limit <= 0 {
		limit = 64
	}
	a.totalEvals.Add(1)
	a.mu.Lock()
	ctx := a.evalContext
	rules := make([]*autoRule, 0, len(a.rules))
	for _, r := range a.rules {
		rules = append(rules, r)
	}
	a.mu.Unlock()
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	now := time.Now()
	var fires []AutoFire
	for _, r := range rules {
		if r.Paused {
			continue
		}
		cond, err := parseAutoCondition(r.Condition)
		if err != nil {
			continue
		}
		truth, reason := cond.evaluate(ctx)
		// Edge trigger: only fire on false → true
		if truth && !r.WasTrue {
			// Cooldown gate
			if r.Cooldown > 0 && !r.LastFired.IsZero() && now.Sub(r.LastFired) < r.Cooldown {
				r.WasTrue = true
				continue
			}
			f := autoFire{RuleID: r.ID, Action: r.Action, At: now, Reason: reason}
			a.mu.Lock()
			a.fires = append(a.fires, f)
			if len(a.fires) > 10000 {
				a.fires = a.fires[len(a.fires)-10000:]
			}
			a.mu.Unlock()
			a.totalFires.Add(1)
			r.LastFired = now
			fires = append(fires, AutoFire{
				RuleID: r.ID, Action: r.Action,
				AtUnix: now.Unix(), Reason: reason,
			})
			if len(fires) >= limit {
				break
			}
		}
		r.WasTrue = truth
	}
	return fires
}

// AutoDryRunResult is DRYRUN's return.
type AutoDryRunResult struct {
	RuleID  string `json:"rule_id"`
	Truth   bool   `json:"truth"`
	Reason  string `json:"reason"`
	WouldFire bool `json:"would_fire"`
}

// DryRun tells you what the rule WOULD evaluate to right now, without
// firing it or updating its edge-trigger state.
func (a *AutoRules) DryRun(id string) (AutoDryRunResult, bool) {
	a.mu.RLock()
	r, ok := a.rules[id]
	ctx := a.evalContext
	a.mu.RUnlock()
	if !ok {
		return AutoDryRunResult{}, false
	}
	cond, err := parseAutoCondition(r.Condition)
	if err != nil {
		return AutoDryRunResult{RuleID: id, Reason: "parse error: " + err.Error()}, true
	}
	truth, reason := cond.evaluate(ctx)
	wouldFire := truth && !r.WasTrue && !r.Paused
	if wouldFire && r.Cooldown > 0 && !r.LastFired.IsZero() && time.Since(r.LastFired) < r.Cooldown {
		wouldFire = false
		reason += " (cooldown active)"
	}
	return AutoDryRunResult{
		RuleID: id, Truth: truth, Reason: reason, WouldFire: wouldFire,
	}, true
}

// Fires returns the audit trail. ruleID="" returns all.
func (a *AutoRules) Fires(ruleID string, limit int) []AutoFire {
	if limit <= 0 {
		limit = 100
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AutoFire, 0, limit)
	// Reverse chronological
	for i := len(a.fires) - 1; i >= 0 && len(out) < limit; i-- {
		f := a.fires[i]
		if ruleID != "" && f.RuleID != ruleID {
			continue
		}
		out = append(out, AutoFire{
			RuleID: f.RuleID, Action: f.Action,
			AtUnix: f.At.Unix(), Reason: f.Reason,
		})
	}
	return out
}

// AutoRuleView is one row of LIST / GET.
type AutoRuleView struct {
	ID         string `json:"rule_id"`
	Condition  string `json:"when"`
	Action     string `json:"do"`
	CooldownMS int64  `json:"cooldown_ms"`
	Paused     bool   `json:"paused"`
	LastFiredUnix int64 `json:"last_fired_unix"`
	WasTrue    bool   `json:"was_true"`
}

// List returns all rules.
func (a *AutoRules) List() []AutoRuleView {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AutoRuleView, 0, len(a.rules))
	for _, r := range a.rules {
		out = append(out, viewAutoRule(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one rule.
func (a *AutoRules) Get(id string) (AutoRuleView, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	r, ok := a.rules[id]
	if !ok {
		return AutoRuleView{}, false
	}
	return viewAutoRule(r), true
}

func viewAutoRule(r *autoRule) AutoRuleView {
	v := AutoRuleView{
		ID: r.ID, Condition: r.Condition, Action: r.Action,
		CooldownMS: r.Cooldown.Milliseconds(),
		Paused: r.Paused, WasTrue: r.WasTrue,
	}
	if !r.LastFired.IsZero() {
		v.LastFiredUnix = r.LastFired.Unix()
	}
	return v
}

// AutoStats is the global snapshot.
type AutoStats struct {
	Rules      int   `json:"rules"`
	Fires      int   `json:"fires_logged"`
	TotalEvals int64 `json:"total_evals"`
	TotalFires int64 `json:"total_fires"`
}

func (a *AutoRules) Stats() AutoStats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return AutoStats{
		Rules: len(a.rules), Fires: len(a.fires),
		TotalEvals: a.totalEvals.Load(),
		TotalFires: a.totalFires.Load(),
	}
}

// ─── condition parser + evaluator ───────────────────────────────

type autoCondition struct {
	metric  string // dotted path, e.g. "vecspace.docs.verdict"
	op      string
	operand string
}

func parseAutoCondition(expr string) (*autoCondition, error) {
	s := strings.TrimSpace(expr)
	// Split on operator. Order matters: try the longest first.
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		idx := strings.Index(s, op)
		if idx < 0 {
			continue
		}
		left := strings.TrimSpace(s[:idx])
		right := strings.TrimSpace(s[idx+len(op):])
		// Strip surrounding quotes from right side
		if len(right) >= 2 && right[0] == '"' && right[len(right)-1] == '"' {
			right = right[1 : len(right)-1]
		}
		if left == "" || right == "" {
			return nil, errors.New("malformed condition: " + expr)
		}
		return &autoCondition{metric: left, op: op, operand: right}, nil
	}
	return nil, errors.New("condition must contain ==, !=, <, <=, >, or >=")
}

func (c *autoCondition) evaluate(ctx AutoEvalContext) (bool, string) {
	if ctx == nil {
		return false, "no evaluation context wired"
	}
	parts := strings.Split(c.metric, ".")
	switch parts[0] {
	case "vecspace":
		if len(parts) != 3 {
			return false, "vecspace metric requires vecspace.<space>.<field>"
		}
		space, field := parts[1], parts[2]
		switch field {
		case "verdict":
			v, ok := ctx.VecSpaceVerdict(space)
			if !ok {
				return false, "vecspace " + space + " unknown"
			}
			return compareString(v, c.op, c.operand), v + " " + c.op + " " + c.operand
		case "cosine":
			v, ok := ctx.VecSpaceMeanCosine(space)
			if !ok {
				return false, "vecspace " + space + " unknown"
			}
			f, err := strconv.ParseFloat(c.operand, 64)
			if err != nil {
				return false, "operand not float"
			}
			return compareFloat(v, c.op, f), strconv.FormatFloat(v, 'f', 4, 64) + " " + c.op + " " + c.operand
		}
	case "trust":
		if len(parts) != 3 {
			return false, "trust metric requires trust.<entity>.<field>"
		}
		entity, field := parts[1], parts[2]
		score, n, ok := ctx.TrustScore(entity)
		if !ok {
			return false, "trust " + entity + " unknown"
		}
		switch field {
		case "score":
			f, err := strconv.ParseFloat(c.operand, 64)
			if err != nil {
				return false, "operand not float"
			}
			return compareFloat(score, c.op, f), strconv.FormatFloat(score, 'f', 4, 64) + " " + c.op + " " + c.operand
		case "n":
			ni, err := strconv.ParseInt(c.operand, 10, 64)
			if err != nil {
				return false, "operand not int"
			}
			return compareFloat(float64(n), c.op, float64(ni)), strconv.FormatInt(n, 10) + " " + c.op + " " + c.operand
		}
	case "risk":
		if len(parts) != 3 {
			return false, "risk metric requires risk.<session>.<field>"
		}
		session, field := parts[1], parts[2]
		switch field {
		case "enforce":
			v, ok := ctx.RiskEnforce(session)
			if !ok {
				return false, "risk session " + session + " unknown"
			}
			actual := "0"
			if v {
				actual = "1"
			}
			return compareString(actual, c.op, c.operand), actual + " " + c.op + " " + c.operand
		case "balance":
			v, ok := ctx.RiskBalance(session)
			if !ok {
				return false, "risk session " + session + " unknown"
			}
			f, err := strconv.ParseFloat(c.operand, 64)
			if err != nil {
				return false, "operand not float"
			}
			return compareFloat(v, c.op, f), strconv.FormatFloat(v, 'f', 4, 64) + " " + c.op + " " + c.operand
		}
	case "market":
		if len(parts) != 3 {
			return false, "market metric requires market.<id>.<field>"
		}
		mid, field := parts[1], parts[2]
		switch field {
		case "price":
			v, ok := ctx.MarketPrice(mid)
			if !ok {
				return false, "market " + mid + " unknown"
			}
			f, err := strconv.ParseFloat(c.operand, 64)
			if err != nil {
				return false, "operand not float"
			}
			return compareFloat(v, c.op, f), strconv.FormatFloat(v, 'f', 4, 64) + " " + c.op + " " + c.operand
		case "starved":
			n, ok := ctx.MarketStarvedCount(mid)
			if !ok {
				return false, "market " + mid + " unknown"
			}
			ni, err := strconv.Atoi(c.operand)
			if err != nil {
				return false, "operand not int"
			}
			return compareFloat(float64(n), c.op, float64(ni)), strconv.Itoa(n) + " " + c.op + " " + c.operand
		}
	case "cfcache":
		if len(parts) != 2 || parts[1] != "hit_rate" {
			return false, "cfcache metric requires cfcache.hit_rate"
		}
		v, ok := ctx.CFCacheHitRate()
		if !ok {
			return false, "cfcache stats unavailable"
		}
		f, err := strconv.ParseFloat(c.operand, 64)
		if err != nil {
			return false, "operand not float"
		}
		return compareFloat(v, c.op, f), strconv.FormatFloat(v, 'f', 4, 64) + " " + c.op + " " + c.operand
	}
	return false, "unknown metric: " + c.metric
}

func compareString(actual, op, expected string) bool {
	switch op {
	case "==":
		return actual == expected
	case "!=":
		return actual != expected
	}
	return false
}

func compareFloat(actual float64, op string, expected float64) bool {
	switch op {
	case "==":
		return actual == expected
	case "!=":
		return actual != expected
	case "<":
		return actual < expected
	case "<=":
		return actual <= expected
	case ">":
		return actual > expected
	case ">=":
		return actual >= expected
	}
	return false
}
