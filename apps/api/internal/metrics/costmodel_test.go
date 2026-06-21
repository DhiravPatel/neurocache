package metrics

import "testing"

func TestCostModelRuntimeUpdate(t *testing.T) {
	m := New()
	defer m.Stop()

	// Defaults set in New().
	if tph, usd := m.CostModel(); tph != 1000 || usd != 10 {
		t.Fatalf("default cost model = (%d, %v), want (1000, 10)", tph, usd)
	}

	// Update both dimensions.
	tph, usd := m.SetCostModel(1500, 2.5)
	if tph != 1500 || usd != 2.5 {
		t.Fatalf("SetCostModel returned (%d, %v), want (1500, 2.5)", tph, usd)
	}
	if gtph, gusd := m.CostModel(); gtph != 1500 || gusd != 2.5 {
		t.Fatalf("CostModel after set = (%d, %v), want (1500, 2.5)", gtph, gusd)
	}

	// Non-positive args leave a field unchanged (update one at a time).
	tph, usd = m.SetCostModel(0, 7)
	if tph != 1500 || usd != 7 {
		t.Fatalf("partial update = (%d, %v), want (1500, 7)", tph, usd)
	}
	tph, usd = m.SetCostModel(800, -1)
	if tph != 800 || usd != 7 {
		t.Fatalf("partial update = (%d, %v), want (800, 7)", tph, usd)
	}

	// Savings accounting uses the live model: one LLM hit at 800 tok × $7/M.
	m.RecordLLM(true)
	want := 800 * 7.0 / 1_000_000.0
	if got := m.Summary().EstSavingsUSD; got != want {
		t.Fatalf("EstSavingsUSD = %v, want %v", got, want)
	}
}
