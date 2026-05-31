package llmstack

import "testing"

func TestCarbonChargeAndAggregate(t *testing.T) {
	c := NewCarbonLedger()
	c.Intensity("gpt-4o", 2.0)
	c.Region("us-east", 380)
	r, err := c.Charge("acme", "summarize", "gpt-4o", "us-east", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// 2 Wh per 1k tokens * 1000 tokens / 1000 = 2 Wh
	if r.EnergyWh < 1.99 || r.EnergyWh > 2.01 {
		t.Fatalf("energy = %f", r.EnergyWh)
	}
	// 2 Wh / 1000 = 0.002 kWh * 380 g/kWh = 0.76 g
	if r.CO2Gram < 0.75 || r.CO2Gram > 0.77 {
		t.Fatalf("co2 = %f", r.CO2Gram)
	}
}

func TestCarbonAggregateFilters(t *testing.T) {
	c := NewCarbonLedger()
	c.Charge("acme", "f1", "gpt-4o", "", 1000)
	c.Charge("acme", "f2", "gpt-4o", "", 2000)
	c.Charge("globex", "f1", "gpt-4o", "", 500)
	rows := c.Aggregate("acme", "", "")
	if len(rows) != 2 {
		t.Fatalf("acme rows = %d", len(rows))
	}
	rows = c.Aggregate("acme", "f2", "")
	if len(rows) != 1 || rows[0].Tokens != 2000 {
		t.Fatalf("filter: %+v", rows)
	}
}

func TestCarbonBudgetOver(t *testing.T) {
	c := NewCarbonLedger()
	c.Intensity("m", 1000.0)
	c.Charge("acme", "f", "m", "", 10000) // big charge
	c.Budget("acme", 0.001)                // tiny budget
	r, _ := c.Over("acme")
	if !r.OverBudget {
		t.Fatalf("should be over: %+v", r)
	}
}

func TestCarbonBudgetUnderlimit(t *testing.T) {
	c := NewCarbonLedger()
	c.Intensity("m", 0.001)
	c.Charge("acme", "f", "m", "", 100)
	c.Budget("acme", 1e6)
	r, _ := c.Over("acme")
	if r.OverBudget {
		t.Fatalf("should be under: %+v", r)
	}
}

func TestCarbonResetTenant(t *testing.T) {
	c := NewCarbonLedger()
	c.Charge("a", "f", "m", "", 100)
	c.Charge("b", "f", "m", "", 100)
	if c.Reset("TENANT", "a") != 1 {
		t.Fatal("reset tenant")
	}
}

func TestCarbonRejectsBadInput(t *testing.T) {
	c := NewCarbonLedger()
	if err := c.Intensity("", 1); err == nil {
		t.Fatal("empty model")
	}
	if err := c.Intensity("m", -1); err == nil {
		t.Fatal("negative wh")
	}
	if err := c.Region("", 1); err == nil {
		t.Fatal("empty region")
	}
	if _, err := c.Charge("", "f", "m", "", 0); err == nil {
		t.Fatal("empty tenant")
	}
	if _, err := c.Charge("a", "f", "m", "", -1); err == nil {
		t.Fatal("negative tokens")
	}
	if err := c.Budget("", 100); err == nil {
		t.Fatal("empty tenant budget")
	}
}

func TestCarbonStats(t *testing.T) {
	c := NewCarbonLedger()
	c.Intensity("m", 1)
	c.Region("r", 100)
	c.Budget("t", 100)
	c.Charge("t", "f", "m", "r", 100)
	s := c.Stats()
	if s.Models != 1 || s.Tenants != 1 || s.Budgets != 1 || s.TotalCharges != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
