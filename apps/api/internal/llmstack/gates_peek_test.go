package llmstack

import "testing"

// TestRiskPeekDoesNotMutate — Peek must compute the forward debit without
// touching the session balance or creating an unknown session.
func TestRiskPeekDoesNotMutate(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("s", 1.0, 1.0)

	// score 0 → debit (1-0)*1 = 1 → balance 1-1 = 0 → enforce.
	p := r.Peek("s", 0.0)
	if !p.Enforce {
		t.Fatalf("peek enforce=false, balance=%v (want enforce at/below 0)", p.Balance)
	}
	if st, _ := r.Status("s"); st.Balance != 1.0 {
		t.Fatalf("peek mutated balance to %v, want 1.0 untouched", st.Balance)
	}

	// Peeking a never-set session must not create it.
	_ = r.Peek("ghost", 0.5)
	if _, ok := r.Status("ghost"); ok {
		t.Fatal("peek created a session")
	}

	// A high-confidence score barely debits → no enforce.
	if p := r.Peek("s", 1.0); p.Enforce {
		t.Fatalf("score 1.0 should not enforce; balance=%v", p.Balance)
	}
}

// TestCarbonSimulateForwardLooking — Simulate projects the next charge and
// flags a breach without recording usage.
func TestCarbonSimulateForwardLooking(t *testing.T) {
	c := NewCarbonLedger()
	_ = c.Intensity("m", 1000) // 1000 Wh / 1k tokens
	_ = c.Budget("t", 1000)    // 1000 gCO₂ ceiling

	// 1000 tokens → 1000 Wh → 430 gCO₂ (default region 430 g/kWh) < 1000.
	sim, has := c.Simulate("t", "f", "m", "default", 1000)
	if !has {
		t.Fatal("Simulate reported no budget though one was set")
	}
	if sim.CO2Gram < 429 || sim.CO2Gram > 431 {
		t.Fatalf("co2 = %v, want ~430", sim.CO2Gram)
	}
	if sim.WouldExceed {
		t.Fatalf("430 g should fit under 1000 g budget")
	}
	// Simulate must not record usage.
	if over, _ := c.Over("t"); over.UsedG != 0 {
		t.Fatalf("simulate recorded usage: %v", over.UsedG)
	}

	// A larger request projects over budget.
	big, _ := c.Simulate("t", "f", "m", "default", 5000) // ~2150 gCO₂
	if !big.WouldExceed {
		t.Fatalf("5000 tokens (~2150g) should exceed 1000g; got %+v", big)
	}
}
