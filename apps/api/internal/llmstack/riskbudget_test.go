package llmstack

import (
	"testing"
)

func TestRiskBudgetDebitDecreasesBalance(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("sess", 10, 0)
	d, _ := r.Debit("sess", 0.9, "")
	if d.Balance >= 10 {
		t.Fatalf("balance not debited: %f", d.Balance)
	}
	if d.Enforce {
		t.Fatal("high-score debit should not enforce")
	}
}

func TestRiskBudgetEnforcesOnExhaust(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("sess", 1.0, 1.0) // 1 unit
	d, _ := r.Debit("sess", 0.0, "no grounding") // debits 1.0
	if !d.Enforce {
		t.Fatalf("should enforce on exhaust: %+v", d)
	}
}

func TestRiskBudgetAutoCreatesSession(t *testing.T) {
	r := NewRiskBudgets()
	d, _ := r.Debit("never-set", 0.5, "")
	if d.Budget != defaultRiskBudget {
		t.Fatalf("default not applied: %+v", d)
	}
}

func TestRiskBudgetStatusBreakdown(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("s", 10, 0)
	r.Debit("s", 0.8, "")
	r.Debit("s", 0.2, "")
	st, ok := r.Status("s")
	if !ok || st.Debits != 2 {
		t.Fatalf("status = %+v", st)
	}
	if st.MeanScore < 0.49 || st.MeanScore > 0.51 {
		t.Fatalf("mean = %f", st.MeanScore)
	}
}

func TestRiskBudgetScoreClamp(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("s", 10, 0)
	// Out-of-range scores should clamp, not crash
	d1, _ := r.Debit("s", -1, "")
	d2, _ := r.Debit("s", 5, "")
	// Score -1 → 0 → debit 1.0
	if d1.Debited != 1.0 {
		t.Fatalf("score -1 not clamped to 0: debited=%f", d1.Debited)
	}
	// Score 5 → 1 → no debit
	if d2.Debited != 0 {
		t.Fatalf("score 5 not clamped to 1: debited=%f", d2.Debited)
	}
}

func TestRiskBudgetWeight(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("s", 10, 5.0) // weight 5x
	d, _ := r.Debit("s", 0.0, "")
	if d.Debited != 5.0 {
		t.Fatalf("weight not applied: %f", d.Debited)
	}
}

func TestRiskBudgetReset(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("a", 10, 0)
	r.Set("b", 10, 0)
	if r.Reset("a") != 1 {
		t.Fatal("reset a")
	}
	if r.Reset("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestRiskBudgetSetClearsHistory(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("s", 10, 0)
	r.Debit("s", 0.0, "")
	r.Set("s", 10, 0) // reset via Set
	st, _ := r.Status("s")
	if st.Debits != 0 || st.Balance != 10 {
		t.Fatalf("set should reset: %+v", st)
	}
}

func TestRiskBudgetStats(t *testing.T) {
	r := NewRiskBudgets()
	r.Set("s", 1.0, 1.0)
	r.Debit("s", 0.0, "") // enforce
	r.Debit("s", 0.5, "") // also enforced (balance already < 0)
	stats := r.Stats()
	if stats.TotalDebits != 2 || stats.TotalEnforced < 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRiskBudgetList(t *testing.T) {
	r := NewRiskBudgets()
	r.Debit("zeta", 0.5, "")
	r.Debit("alpha", 0.5, "")
	l := r.List()
	if l[0] != "alpha" {
		t.Fatalf("list = %v", l)
	}
}

func TestRiskBudgetRejectsBadInput(t *testing.T) {
	r := NewRiskBudgets()
	if err := r.Set("", 10, 0); err == nil {
		t.Fatal("empty session should fail")
	}
	if err := r.Set("s", -1, 0); err == nil {
		t.Fatal("negative budget should fail")
	}
	if err := r.Set("s", 10, -1); err == nil {
		t.Fatal("negative weight should fail")
	}
}
