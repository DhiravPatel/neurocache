package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// CarbonLedger attributes energy and CO₂ per inference to tenants
// and features. Increasingly a hard procurement gate in EU enterprise
// RFPs; no infra product ships this as a first-class primitive next
// to the cost ledger. The accounting is straightforward — Wh per
// 1k tokens × usage — but having it in the same primitive surface as
// COST closes a deal.
//
// Two intensity tables:
//
//   - per-model energy intensity (Wh / 1k tokens). Caller supplies
//     reasonable defaults (the literature has ranges; we let the
//     operator set them) via INTENSITY.
//   - per-region carbon intensity (gCO₂ / kWh). Defaults to the
//     world average ~430.
//
// CHARGE records one inference; AGGREGATE returns per-tenant /
// per-feature / per-model totals; BUDGET enforces a per-tenant CO₂
// ceiling (parallel to COST.BUDGET).
//
// Commands:
//
//   CARBON.INTENSITY model wh-per-1k-tokens
//   CARBON.REGION region g-co2-per-kwh
//   CARBON.CHARGE tenant feature model tokens [REGION r]
//        → energy_wh, co2_g
//   CARBON.AGGREGATE [TENANT t] [FEATURE f] [MODEL m]
//        → totals (energy_wh, co2_g, tokens, calls)
//   CARBON.BUDGET tenant co2-grams
//        Sets a per-tenant CO₂ ceiling.
//   CARBON.OVER tenant
//        → over_budget (bool), used_g, budget_g
//   CARBON.RESET TENANT t|MODEL m|ALL
//   CARBON.STATS
//
// All numbers are floats — the engine doesn't impose units beyond
// "wh-per-1k-tokens for energy" and "g-co2-per-kwh for carbon".
type CarbonLedger struct {
	mu sync.RWMutex

	// model → Wh per 1k tokens
	intensity map[string]float64
	// region → g CO₂ per kWh
	regions map[string]float64
	// tenant → ceiling (gCO₂)
	budgets map[string]float64

	// Aggregates keyed by (tenant|feature|model)
	usage map[string]*carbonRow

	totalCharges atomic.Int64
}

type carbonRow struct {
	Tenant   string
	Feature  string
	Model    string
	Tokens   int64
	Calls    int64
	EnergyWh float64
	CO2Gram  float64
}

// NewCarbonLedger returns an empty ledger.
func NewCarbonLedger() *CarbonLedger {
	return &CarbonLedger{
		intensity: map[string]float64{},
		regions:   map[string]float64{"default": 430.0}, // world average
		budgets:   map[string]float64{},
		usage:     map[string]*carbonRow{},
	}
}

// Intensity sets the energy intensity for a model.
func (c *CarbonLedger) Intensity(model string, whPer1k float64) error {
	if model == "" {
		return errors.New("model required")
	}
	if whPer1k < 0 {
		return errors.New("wh_per_1k must be non-negative")
	}
	c.mu.Lock()
	c.intensity[model] = whPer1k
	c.mu.Unlock()
	return nil
}

// Region sets the carbon intensity for a region.
func (c *CarbonLedger) Region(region string, gCO2PerKWh float64) error {
	if region == "" {
		return errors.New("region required")
	}
	if gCO2PerKWh < 0 {
		return errors.New("g_co2_per_kwh must be non-negative")
	}
	c.mu.Lock()
	c.regions[region] = gCO2PerKWh
	c.mu.Unlock()
	return nil
}

// CarbonChargeResult is CHARGE's return.
type CarbonChargeResult struct {
	EnergyWh float64 `json:"energy_wh"`
	CO2Gram  float64 `json:"co2_g"`
}

// Charge accounts one inference.
func (c *CarbonLedger) Charge(tenant, feature, model, region string, tokens int64) (CarbonChargeResult, error) {
	if tenant == "" || feature == "" || model == "" {
		return CarbonChargeResult{}, errors.New("tenant, feature, model required")
	}
	if tokens < 0 {
		return CarbonChargeResult{}, errors.New("tokens must be non-negative")
	}
	c.totalCharges.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	wh1k, ok := c.intensity[model]
	if !ok {
		// Unknown model → use a fallback; operators should set INTENSITY
		// explicitly but we shouldn't crash the request path.
		wh1k = 1.5 // typical small model
	}
	if region == "" {
		region = "default"
	}
	gPerKWh, ok := c.regions[region]
	if !ok {
		gPerKWh = c.regions["default"]
	}
	energyWh := wh1k * float64(tokens) / 1000.0
	co2g := energyWh / 1000.0 * gPerKWh
	key := tenant + "|" + feature + "|" + model
	row, ok := c.usage[key]
	if !ok {
		row = &carbonRow{Tenant: tenant, Feature: feature, Model: model}
		c.usage[key] = row
	}
	row.Tokens += tokens
	row.Calls++
	row.EnergyWh += energyWh
	row.CO2Gram += co2g
	return CarbonChargeResult{EnergyWh: energyWh, CO2Gram: co2g}, nil
}

// CarbonAggregateRow is one row of AGGREGATE.
type CarbonAggregateRow struct {
	Tenant   string  `json:"tenant"`
	Feature  string  `json:"feature"`
	Model    string  `json:"model"`
	Tokens   int64   `json:"tokens"`
	Calls    int64   `json:"calls"`
	EnergyWh float64 `json:"energy_wh"`
	CO2Gram  float64 `json:"co2_g"`
}

// Aggregate filters by any combination of tenant/feature/model
// (empty = wildcard).
func (c *CarbonLedger) Aggregate(tenant, feature, model string) []CarbonAggregateRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]CarbonAggregateRow, 0)
	for _, row := range c.usage {
		if tenant != "" && row.Tenant != tenant {
			continue
		}
		if feature != "" && row.Feature != feature {
			continue
		}
		if model != "" && row.Model != model {
			continue
		}
		out = append(out, CarbonAggregateRow{
			Tenant: row.Tenant, Feature: row.Feature, Model: row.Model,
			Tokens: row.Tokens, Calls: row.Calls,
			EnergyWh: row.EnergyWh, CO2Gram: row.CO2Gram,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		if out[i].Feature != out[j].Feature {
			return out[i].Feature < out[j].Feature
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Budget sets a tenant's CO₂ ceiling.
func (c *CarbonLedger) Budget(tenant string, gCO2 float64) error {
	if tenant == "" {
		return errors.New("tenant required")
	}
	if gCO2 < 0 {
		return errors.New("co2_g must be non-negative")
	}
	c.mu.Lock()
	c.budgets[tenant] = gCO2
	c.mu.Unlock()
	return nil
}

// CarbonOverResult is OVER's return.
type CarbonOverResult struct {
	Tenant     string  `json:"tenant"`
	OverBudget bool    `json:"over_budget"`
	UsedG      float64 `json:"used_g"`
	BudgetG    float64 `json:"budget_g"`
}

// Over reports whether the tenant has exceeded its CO₂ budget.
func (c *CarbonLedger) Over(tenant string) (CarbonOverResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	budget, hasBudget := c.budgets[tenant]
	used := 0.0
	for _, row := range c.usage {
		if row.Tenant == tenant {
			used += row.CO2Gram
		}
	}
	out := CarbonOverResult{Tenant: tenant, UsedG: used, BudgetG: budget}
	if hasBudget {
		out.OverBudget = used > budget
	}
	return out, hasBudget
}

// Reset clears usage by scope.
func (c *CarbonLedger) Reset(kind, name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == "" && name == "ALL" {
		n := len(c.usage)
		c.usage = map[string]*carbonRow{}
		return n
	}
	dropped := 0
	for k, row := range c.usage {
		match := false
		switch kind {
		case "TENANT":
			match = row.Tenant == name
		case "MODEL":
			match = row.Model == name
		case "FEATURE":
			match = row.Feature == name
		default:
			return 0
		}
		if match {
			delete(c.usage, k)
			dropped++
		}
	}
	return dropped
}

// CarbonStats is the global snapshot.
type CarbonStats struct {
	Models       int   `json:"models_with_intensity"`
	Regions      int   `json:"regions_with_intensity"`
	Tenants      int   `json:"tenants_with_usage"`
	Rows         int   `json:"usage_rows"`
	Budgets      int   `json:"tenants_with_budget"`
	TotalCharges int64 `json:"total_charges"`
}

func (c *CarbonLedger) Stats() CarbonStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tenants := map[string]bool{}
	for _, row := range c.usage {
		tenants[row.Tenant] = true
	}
	return CarbonStats{
		Models:       len(c.intensity),
		Regions:      len(c.regions),
		Tenants:      len(tenants),
		Rows:         len(c.usage),
		Budgets:      len(c.budgets),
		TotalCharges: c.totalCharges.Load(),
	}
}
