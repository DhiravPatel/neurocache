package aiops

import (
	"errors"
	"sync"
	"time"
)

// CostBudgets tracks per-tenant LLM spend against a sliding window.
// Operators set a budget per tenant (`COST.BUDGET tenant max-usd
// window-ms`); every recorded charge deducts from the available
// remainder. Over-budget calls error fast — saving real money for
// multi-tenant AI products that would otherwise pay for runaway loops.
type CostBudgets struct {
	mu       sync.RWMutex
	tenants  map[string]*tenantBudget
}

type tenantBudget struct {
	maxUSD   float64
	windowMs int64
	// charges is a chronological log of (ts, usd) for the active
	// window. We compact it lazily on each charge / read.
	charges []charge
}

type charge struct {
	at  time.Time
	usd float64
}

// NewCostBudgets returns an empty manager.
func NewCostBudgets() *CostBudgets {
	return &CostBudgets{tenants: map[string]*tenantBudget{}}
}

// SetBudget configures a tenant's allowance. maxUSD <= 0 effectively
// disables the budget (returns Allow always); windowMs <= 0 is
// rejected as a misconfiguration.
func (b *CostBudgets) SetBudget(tenant string, maxUSD float64, windowMs int64) error {
	if windowMs <= 0 {
		return errors.New("window-ms must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tenants[tenant]
	if !ok {
		t = &tenantBudget{}
		b.tenants[tenant] = t
	}
	t.maxUSD = maxUSD
	t.windowMs = windowMs
	return nil
}

// Charge records a spend against a tenant. Returns (allowed, remaining,
// err). If the charge would push usage past the budget, the call is
// rejected (allowed=false) and nothing is recorded — letting callers
// short-circuit before paying for an LLM call they can't afford.
func (b *CostBudgets) Charge(tenant string, usd float64) (bool, float64, error) {
	if usd < 0 {
		return false, 0, errors.New("usd must be non-negative")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tenants[tenant]
	if !ok {
		return true, 0, nil // no budget configured = unlimited
	}
	t.compact()
	used := t.spent()
	if t.maxUSD > 0 && used+usd > t.maxUSD {
		return false, t.maxUSD - used, nil
	}
	t.charges = append(t.charges, charge{at: time.Now(), usd: usd})
	return true, t.maxUSD - (used + usd), nil
}

// Usage returns (used, remaining, max, windowMs) for a tenant.
func (b *CostBudgets) Usage(tenant string) (float64, float64, float64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tenants[tenant]
	if !ok {
		return 0, 0, 0, 0
	}
	t.compact()
	used := t.spent()
	return used, t.maxUSD - used, t.maxUSD, t.windowMs
}

// Reset zeroes a tenant's spending log without changing its budget.
func (b *CostBudgets) Reset(tenant string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tenants[tenant]
	if !ok {
		return false
	}
	t.charges = nil
	return true
}

// List returns every configured tenant.
func (b *CostBudgets) List() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.tenants))
	for k := range b.tenants {
		out = append(out, k)
	}
	return out
}

// compact drops charges older than the window. Caller holds b.mu.
func (t *tenantBudget) compact() {
	if t.windowMs <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(t.windowMs) * time.Millisecond)
	i := 0
	for i < len(t.charges) && t.charges[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		t.charges = t.charges[i:]
	}
}

// spent sums charges in the active window. Caller holds b.mu.
func (t *tenantBudget) spent() float64 {
	sum := 0.0
	for _, c := range t.charges {
		sum += c.usd
	}
	return sum
}
