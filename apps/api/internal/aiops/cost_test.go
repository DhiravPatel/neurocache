package aiops

import "testing"

func TestListUsageSnapshot(t *testing.T) {
	b := NewCostBudgets()

	// No tenants configured → empty snapshot.
	if got := b.ListUsage(); len(got) != 0 {
		t.Fatalf("expected empty usage, got %d entries", len(got))
	}

	if err := b.SetBudget("zeta", 10, 60000); err != nil {
		t.Fatalf("SetBudget zeta: %v", err)
	}
	if err := b.SetBudget("alpha", 5, 60000); err != nil {
		t.Fatalf("SetBudget alpha: %v", err)
	}
	if allowed, _, err := b.Charge("alpha", 2); err != nil || !allowed {
		t.Fatalf("Charge alpha: allowed=%v err=%v", allowed, err)
	}

	usage := b.ListUsage()
	if len(usage) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(usage))
	}
	// Sorted by tenant name → alpha first.
	if usage[0].Tenant != "alpha" || usage[1].Tenant != "zeta" {
		t.Fatalf("expected sorted [alpha, zeta], got [%s, %s]", usage[0].Tenant, usage[1].Tenant)
	}
	if usage[0].Used != 2 || usage[0].Max != 5 || usage[0].Remaining != 3 {
		t.Fatalf("alpha usage wrong: %+v", usage[0])
	}
	if usage[1].Used != 0 || usage[1].Max != 10 || usage[1].Remaining != 10 {
		t.Fatalf("zeta usage wrong: %+v", usage[1])
	}
}
