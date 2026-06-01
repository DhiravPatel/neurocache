package aiops

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// QUOTA is composite admission control: a single ADMIT decision that ANDs
// (or ORs) together the five budget/gate primitives NeuroCache already
// ships — COST (USD), CARBON (gCO₂), RISK (hallucination balance),
// RATELIMIT (rps) and MARKET (contention price). Today an app hand-rolls
// the AND across five round-trips, and — worse — has no way to ask "would
// this be admitted?" without consuming each budget just to find out.
//
// QUOTA fixes both:
//
//   - QUOTA.ADMIT is two-phase: it PEEKS every required gate first, and
//     only commits (consumes) them if the overall decision passes. So a
//     request that fails the carbon gate never spends a rate-limit token.
//   - QUOTA.SIMULATE is the peek with no commit — the dry run every one of
//     the underlying budgets lacked individually.
//
// This pure package owns only the policy registry and counters. The actual
// gate evaluation lives in the engine (engine.QuotaEvaluate), which is the
// only place with handles to all five subsystems — keeping aiops free of an
// import edge to llmstack/primitives.
type QuotaManager struct {
	mu       sync.Mutex
	policies map[string]*QuotaPolicy

	admits      atomic.Int64
	denials     atomic.Int64
	simulations atomic.Int64
}

// Gate identifiers used in a policy's REQUIRE list and in QuotaDims.
const (
	GateCost   = "cost"
	GateCarbon = "carbon"
	GateRisk   = "risk"
	GateRate   = "rate"
	GateMarket = "market"
)

// Admission modes.
const (
	QuotaModeAll = "all" // admit iff every required gate allows (the AND)
	QuotaModeAny = "any" // admit iff at least one required gate allows
)

var validGates = map[string]bool{
	GateCost: true, GateCarbon: true, GateRisk: true, GateRate: true, GateMarket: true,
}

// QuotaPolicy is a named composite gate: which budgets a request must clear
// and whether all of them or any of them.
type QuotaPolicy struct {
	Name  string   `json:"name"`
	Gates []string `json:"gates"`
	Mode  string   `json:"mode"`
}

// QuotaDims carries the per-gate request parameters. Only the gates the
// policy requires are consulted; a required gate whose Has* flag is unset
// is a caller error (the engine rejects it) rather than a silent pass.
type QuotaDims struct {
	HasCost   bool
	CostScope string
	CostUSD   float64

	HasCarbon     bool
	CarbonTenant  string
	CarbonFeature string
	CarbonModel   string
	CarbonRegion  string
	CarbonTokens  int64

	HasRisk     bool
	RiskSession string
	RiskScore   float64

	HasRate      bool
	RateKey      string
	RateWindowMs int64
	RateMax      int64
	RateCost     int64

	HasMarket      bool
	MarketID       string
	MarketMaxPrice float64
}

// QuotaGateResult is one gate's verdict inside a decision.
type QuotaGateResult struct {
	Gate         string  `json:"gate"`
	Allowed      bool    `json:"allowed"`
	Consumed     bool    `json:"consumed"`
	Reason       string  `json:"reason,omitempty"`
	RetryAfterMs int64   `json:"retry_after_ms,omitempty"`
	Detail       float64 `json:"detail,omitempty"` // gate-specific number (balance, price, projected gCO₂, …)
}

// QuotaDecision is the ADMIT / SIMULATE return.
type QuotaDecision struct {
	Policy       string            `json:"policy"`
	Mode         string            `json:"mode"`
	Admitted     bool              `json:"admitted"`
	Committed    bool              `json:"committed"`
	DeniedBy     []string          `json:"denied_by,omitempty"`
	RetryAfterMs int64             `json:"retry_after_ms,omitempty"`
	Gates        []QuotaGateResult `json:"gates"`
}

// NewQuotaManager returns an empty registry.
func NewQuotaManager() *QuotaManager {
	return &QuotaManager{policies: map[string]*QuotaPolicy{}}
}

// Define creates or replaces a policy. gates must be non-empty and drawn
// from the known set; mode defaults to "all".
func (q *QuotaManager) Define(name string, gates []string, mode string) (QuotaPolicy, error) {
	if name == "" {
		return QuotaPolicy{}, errors.New("policy name required")
	}
	if len(gates) == 0 {
		return QuotaPolicy{}, errors.New("at least one gate is required")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = QuotaModeAll
	}
	if mode != QuotaModeAll && mode != QuotaModeAny {
		return QuotaPolicy{}, errors.New("mode must be all or any")
	}
	seen := map[string]bool{}
	norm := make([]string, 0, len(gates))
	for _, g := range gates {
		g = strings.ToLower(strings.TrimSpace(g))
		if !validGates[g] {
			return QuotaPolicy{}, errors.New("unknown gate: " + g)
		}
		if seen[g] {
			continue
		}
		seen[g] = true
		norm = append(norm, g)
	}
	p := &QuotaPolicy{Name: name, Gates: norm, Mode: mode}
	q.mu.Lock()
	q.policies[name] = p
	q.mu.Unlock()
	return *p, nil
}

// Get returns a copy of a policy.
func (q *QuotaManager) Get(name string) (QuotaPolicy, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.policies[name]
	if !ok {
		return QuotaPolicy{}, false
	}
	return *p, true
}

// List returns every policy, name-sorted.
func (q *QuotaManager) List() []QuotaPolicy {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QuotaPolicy, 0, len(q.policies))
	for _, p := range q.policies {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete drops a policy. Returns false if it was absent.
func (q *QuotaManager) Delete(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.policies[name]; !ok {
		return false
	}
	delete(q.policies, name)
	return true
}

// RecordDecision updates the counters after the engine evaluates a request.
// simulate=true routes to the dry-run counter and never touches admit/deny.
func (q *QuotaManager) RecordDecision(d QuotaDecision, simulate bool) {
	if simulate {
		q.simulations.Add(1)
		return
	}
	if d.Admitted {
		q.admits.Add(1)
	} else {
		q.denials.Add(1)
	}
}

// QuotaStats is the QUOTA.STATS snapshot.
type QuotaStats struct {
	Policies    int   `json:"policies"`
	Admits      int64 `json:"admits"`
	Denials     int64 `json:"denials"`
	Simulations int64 `json:"simulations"`
}

func (q *QuotaManager) Stats() QuotaStats {
	q.mu.Lock()
	n := len(q.policies)
	q.mu.Unlock()
	return QuotaStats{
		Policies:    n,
		Admits:      q.admits.Load(),
		Denials:     q.denials.Load(),
		Simulations: q.simulations.Load(),
	}
}
