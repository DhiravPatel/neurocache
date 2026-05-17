package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// RegWatch maps system outputs to regulatory obligations. The reality
// of EU enterprise procurement in 2026: an AI system has to declare
// its risk tier (EU AI Act categories: minimal / limited / high /
// unacceptable), and config changes that cross a tier need explicit
// review. Today every team rebuilds this poorly in a spreadsheet
// and only realises they tripped a tier when the legal team flags it
// pre-launch.
//
// REGWATCH is the structured registry:
//
//   - RULE declares an obligation: "if the system does X, it falls
//     into risk tier T and obligations O apply".
//   - CHECK takes a config / capability claim and returns the rules
//     it would trigger.
//   - CROSS reports whether a proposed change moves the system
//     across a tier boundary.
//
// The matching is intentionally regex-light — we ship simple keyword
// matching over a "capability" string. Real compliance teams maintain
// the rule catalog; the engine just keeps them queryable.
//
// Commands:
//
//   REGWATCH.RULE rule-id TIER tier MATCHES "kw1,kw2" OBLIGATION "..."
//        [JURIS jurisdiction]
//   REGWATCH.UNRULE rule-id
//   REGWATCH.CHECK capability-text
//        → triggered_rules, max_tier, obligations
//   REGWATCH.CROSS before-capability after-capability
//        → tier_before, tier_after, crossed, new_rules
//   REGWATCH.RULES [JURIS j]
//   REGWATCH.STATS
type RegWatch struct {
	mu    sync.RWMutex
	rules map[string]*regRule

	totalChecks  atomic.Int64
	totalCrosses atomic.Int64
}

type regRule struct {
	ID          string
	Tier        string // minimal, limited, high, unacceptable
	Matches     []string
	Obligation  string
	Jurisdiction string
}

// NewRegWatch returns an empty registry.
func NewRegWatch() *RegWatch {
	return &RegWatch{rules: map[string]*regRule{}}
}

var tierOrder = map[string]int{
	"minimal": 1, "limited": 2, "high": 3, "unacceptable": 4,
}

// Rule registers an obligation.
func (r *RegWatch) Rule(id, tier string, matches []string, obligation, juris string) error {
	if id == "" {
		return errors.New("rule_id required")
	}
	if _, ok := tierOrder[strings.ToLower(tier)]; !ok {
		return errors.New("tier must be minimal|limited|high|unacceptable")
	}
	if len(matches) == 0 {
		return errors.New("at least one match keyword required")
	}
	if obligation == "" {
		return errors.New("obligation required")
	}
	cleaned := make([]string, 0, len(matches))
	for _, m := range matches {
		m = strings.TrimSpace(strings.ToLower(m))
		if m != "" {
			cleaned = append(cleaned, m)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules[id] = &regRule{
		ID: id, Tier: strings.ToLower(tier),
		Matches: cleaned, Obligation: obligation,
		Jurisdiction: juris,
	}
	return nil
}

// Unrule drops a rule.
func (r *RegWatch) Unrule(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rules[id]; ok {
		delete(r.rules, id)
		return 1
	}
	return 0
}

// RegWatchCheckResult is CHECK's return.
type RegWatchCheckResult struct {
	Capability      string             `json:"capability"`
	TriggeredRules  []RegWatchRuleRow  `json:"triggered_rules"`
	MaxTier         string             `json:"max_tier"`
	Obligations     []string           `json:"obligations"`
}

// RegWatchRuleRow is one rule row.
type RegWatchRuleRow struct {
	RuleID       string `json:"rule_id"`
	Tier         string `json:"tier"`
	Obligation   string `json:"obligation"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
}

// Check returns rules whose keywords appear in the capability text.
func (r *RegWatch) Check(capability string) RegWatchCheckResult {
	r.totalChecks.Add(1)
	cap := strings.ToLower(capability)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := RegWatchCheckResult{Capability: capability}
	maxTier := 0
	maxName := ""
	for _, rule := range r.rules {
		hit := false
		for _, m := range rule.Matches {
			if strings.Contains(cap, m) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		out.TriggeredRules = append(out.TriggeredRules, RegWatchRuleRow{
			RuleID: rule.ID, Tier: rule.Tier,
			Obligation: rule.Obligation, Jurisdiction: rule.Jurisdiction,
		})
		out.Obligations = append(out.Obligations, rule.Obligation)
		t := tierOrder[rule.Tier]
		if t > maxTier {
			maxTier = t
			maxName = rule.Tier
		}
	}
	out.MaxTier = maxName
	sort.Slice(out.TriggeredRules, func(i, j int) bool {
		return out.TriggeredRules[i].RuleID < out.TriggeredRules[j].RuleID
	})
	return out
}

// RegWatchCrossResult is CROSS's return.
type RegWatchCrossResult struct {
	TierBefore string   `json:"tier_before"`
	TierAfter  string   `json:"tier_after"`
	Crossed    bool     `json:"crossed"`
	NewRules   []string `json:"new_rules"`
}

// Cross reports tier change + new rules triggered by the proposed
// after-capability that weren't in the before-capability.
func (r *RegWatch) Cross(before, after string) RegWatchCrossResult {
	r.totalCrosses.Add(1)
	b := r.Check(before)
	a := r.Check(after)
	out := RegWatchCrossResult{
		TierBefore: b.MaxTier, TierAfter: a.MaxTier,
	}
	if tierOrder[a.MaxTier] > tierOrder[b.MaxTier] {
		out.Crossed = true
	}
	beforeSet := map[string]bool{}
	for _, x := range b.TriggeredRules {
		beforeSet[x.RuleID] = true
	}
	for _, x := range a.TriggeredRules {
		if !beforeSet[x.RuleID] {
			out.NewRules = append(out.NewRules, x.RuleID)
		}
	}
	sort.Strings(out.NewRules)
	return out
}

// Rules lists all registered rules (optionally filtered by jurisdiction).
func (r *RegWatch) Rules(juris string) []RegWatchRuleRow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RegWatchRuleRow, 0, len(r.rules))
	for _, rule := range r.rules {
		if juris != "" && rule.Jurisdiction != juris {
			continue
		}
		out = append(out, RegWatchRuleRow{
			RuleID: rule.ID, Tier: rule.Tier,
			Obligation: rule.Obligation, Jurisdiction: rule.Jurisdiction,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out
}

// RegWatchStats is the global snapshot.
type RegWatchStats struct {
	Rules        int   `json:"rules"`
	TotalChecks  int64 `json:"total_checks"`
	TotalCrosses int64 `json:"total_crosses"`
}

func (r *RegWatch) Stats() RegWatchStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RegWatchStats{
		Rules: len(r.rules),
		TotalChecks: r.totalChecks.Load(),
		TotalCrosses: r.totalCrosses.Load(),
	}
}
