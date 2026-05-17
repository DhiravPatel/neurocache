package llmstack

import (
	"errors"
	"testing"
	"time"
)

// fakeNetExecutor records what would have posted.
type fakeNetExecutor struct {
	posts []struct {
		Ledger, TxnID, Memo string
		Debits, Credits     []SettleLine
	}
	failAt int // 1-indexed; 0 = never
	failed bool
}

func (f *fakeNetExecutor) PostTxn(ledger, txnID, memo string, debits, credits []SettleLine) error {
	// Fail one-shot: failed flag flips on first hit, subsequent calls
	// (e.g. the rollback path) succeed so the test can assert the
	// best-effort rollback path actually ran.
	if f.failAt > 0 && !f.failed && len(f.posts) == f.failAt-1 {
		f.failed = true
		return errors.New("simulated failure")
	}
	f.posts = append(f.posts, struct {
		Ledger, TxnID, Memo string
		Debits, Credits     []SettleLine
	}{ledger, txnID, memo, debits, credits})
	return nil
}

func TestNettingOpenAddClose(t *testing.T) {
	n := NewNetting()
	n.Open("c1", 0)
	n.Add("c1", "A", "B", 100, "")
	n.Add("c1", "B", "A", 30, "")
	plan, err := n.Close("c1", false)
	if err != nil {
		t.Fatal(err)
	}
	// A→B 100, B→A 30 → net A→B 70. One transfer.
	if plan.NetTransfers != 1 {
		t.Fatalf("net transfers = %d, want 1; plan=%+v", plan.NetTransfers, plan.Plan)
	}
	if plan.Plan[0].From != "B" || plan.Plan[0].To != "A" {
		// Net position: A: -100+30=-70 (debtor), B: +100-30=+70 (creditor)
		// So A pays B 70.
		// Wait — re-check directionality.
		// obligation "A→B 100" means A debtor of B, so A.net = -100, B.net = +100.
		// obligation "B→A 30" means B.net -= 30 → 70; A.net += 30 → -70.
		// So A owes B 70. Plan: From=A, To=B.
		if plan.Plan[0].From != "A" || plan.Plan[0].To != "B" {
			t.Fatalf("direction wrong: %+v", plan.Plan[0])
		}
	}
	if plan.Plan[0].Amount != 70 {
		t.Fatalf("amount = %f", plan.Plan[0].Amount)
	}
}

func TestNettingThreeWayCircleCollapsed(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	// A→B 100, B→C 100, C→A 100. Net: everyone owes 0. ZERO transfers.
	n.Add("c", "A", "B", 100, "")
	n.Add("c", "B", "C", 100, "")
	n.Add("c", "C", "A", 100, "")
	plan, _ := n.Close("c", false)
	if plan.NetTransfers != 0 {
		t.Fatalf("perfect circle should net to zero: %+v", plan.Plan)
	}
	if plan.SavingsPct < 99 {
		t.Fatalf("savings should be ~100%%: %f", plan.SavingsPct)
	}
}

func TestNettingChainCollapsedToOne(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	// A→B 50, B→C 50. Net: A owes C 50. ONE transfer (instead of 2).
	n.Add("c", "A", "B", 50, "")
	n.Add("c", "B", "C", 50, "")
	plan, _ := n.Close("c", false)
	if plan.NetTransfers != 1 {
		t.Fatalf("chain should collapse to 1: %+v", plan.Plan)
	}
	if plan.Plan[0].From != "A" || plan.Plan[0].To != "C" || plan.Plan[0].Amount != 50 {
		t.Fatalf("plan wrong: %+v", plan.Plan[0])
	}
}

func TestNettingDryRunDoesntLock(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	n.Add("c", "A", "B", 50, "")
	plan, _ := n.Close("c", true)
	if !plan.DryRun {
		t.Fatal("DryRun flag should be set")
	}
	// Should still be able to ADD
	if err := n.Add("c", "B", "A", 30, ""); err != nil {
		t.Fatalf("dry-run should leave cycle open: %v", err)
	}
}

func TestNettingApplyPostsViaExecutor(t *testing.T) {
	n := NewNetting()
	exec := &fakeNetExecutor{}
	n.SetExecutor(exec)
	n.Open("c", 0)
	n.Add("c", "A", "B", 100, "")
	n.Close("c", false)
	r, err := n.Apply("c", "default")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "applied" {
		t.Fatalf("state: %s", r.State)
	}
	if len(exec.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(exec.posts))
	}
}

func TestNettingApplyRollbackOnFailure(t *testing.T) {
	n := NewNetting()
	exec := &fakeNetExecutor{failAt: 2} // fail on second post
	n.SetExecutor(exec)
	n.Open("c", 0)
	// Two independent obligations → two transfers
	n.Add("c", "A", "B", 100, "")
	n.Add("c", "C", "D", 50, "")
	n.Close("c", false)
	r, _ := n.Apply("c", "default")
	if r.State != "apply_failed" {
		t.Fatalf("expected apply_failed: %+v", r)
	}
	if r.FailedAt != 2 {
		t.Fatalf("failed_at = %d", r.FailedAt)
	}
	// Posted=1, then one rollback. Total posts attempted = 1 success + 1 fail + 1 rollback = 2 in exec.posts (1 forward, 1 reversal)
	if len(exec.posts) != 2 {
		t.Fatalf("expected 2 successful posts (1 forward + 1 rollback): %d", len(exec.posts))
	}
}

func TestNettingApplyRequiresClosed(t *testing.T) {
	n := NewNetting()
	exec := &fakeNetExecutor{}
	n.SetExecutor(exec)
	n.Open("c", 0)
	n.Add("c", "A", "B", 1, "")
	// Skip close
	if _, err := n.Apply("c", "L"); err == nil {
		t.Fatal("apply without close should fail")
	}
}

func TestNettingApplyRequiresExecutor(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	n.Add("c", "A", "B", 1, "")
	n.Close("c", false)
	if _, err := n.Apply("c", "L"); err == nil {
		t.Fatal("no executor should fail")
	}
}

func TestNettingSelfTransferRejected(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	if err := n.Add("c", "A", "A", 1, ""); err == nil {
		t.Fatal("self-transfer should fail")
	}
}

func TestNettingNegativeAmount(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	if err := n.Add("c", "A", "B", -1, ""); err == nil {
		t.Fatal("negative amount should fail")
	}
	if err := n.Add("c", "A", "B", 0, ""); err == nil {
		t.Fatal("zero amount should fail")
	}
}

func TestNettingClosedRejectsAdd(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	n.Add("c", "A", "B", 1, "")
	n.Close("c", false)
	if err := n.Add("c", "C", "D", 1, ""); err == nil {
		t.Fatal("closed cycle should reject add")
	}
}

func TestNettingExpiresWithDeadline(t *testing.T) {
	n := NewNetting()
	n.Open("c", 5*time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	if err := n.Add("c", "A", "B", 1, ""); err == nil {
		t.Fatal("expired cycle should reject add")
	}
	s, _ := n.Status("c")
	if s.State != "expired" {
		t.Fatalf("state = %s", s.State)
	}
}

func TestNettingStatusSavingsPct(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	// Chain A→B→C→D, each 100. Gross=300; net=A→D 100. Savings=66.7%.
	n.Add("c", "A", "B", 100, "")
	n.Add("c", "B", "C", 100, "")
	n.Add("c", "C", "D", 100, "")
	n.Close("c", false)
	s, _ := n.Status("c")
	if s.SavingsPct < 66 || s.SavingsPct > 67 {
		t.Fatalf("savings = %f", s.SavingsPct)
	}
}

func TestNettingListByState(t *testing.T) {
	n := NewNetting()
	n.Open("a", 0)
	n.Open("b", 0)
	n.Add("b", "x", "y", 1, "")
	n.Close("b", false)
	rows := n.List("closed")
	if len(rows) != 1 || rows[0].CycleID != "b" {
		t.Fatalf("list: %+v", rows)
	}
}

func TestNettingStats(t *testing.T) {
	n := NewNetting()
	exec := &fakeNetExecutor{}
	n.SetExecutor(exec)
	n.Open("c", 0)
	n.Add("c", "A", "B", 1, "")
	n.Close("c", false)
	n.Apply("c", "L")
	st := n.Stats()
	if st.TotalOpens != 1 || st.TotalAdds != 1 || st.TotalCloses != 1 || st.TotalApplies != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestNettingForget(t *testing.T) {
	n := NewNetting()
	n.Open("a", 0)
	n.Open("b", 0)
	if n.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if n.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestNettingDuplicateOpen(t *testing.T) {
	n := NewNetting()
	n.Open("c", 0)
	if err := n.Open("c", 0); err == nil {
		t.Fatal("duplicate open should fail")
	}
}

func TestNettingDeterministicPlanOrder(t *testing.T) {
	// Same inputs in different add-order → same plan order.
	for trial := 0; trial < 3; trial++ {
		n := NewNetting()
		n.Open("c", 0)
		n.Add("c", "Bob", "Alice", 50, "")
		n.Add("c", "Carol", "Alice", 30, "")
		p1, _ := n.Close("c", true)

		n2 := NewNetting()
		n2.Open("c", 0)
		n2.Add("c", "Carol", "Alice", 30, "")
		n2.Add("c", "Bob", "Alice", 50, "")
		p2, _ := n2.Close("c", true)

		if len(p1.Plan) != len(p2.Plan) {
			t.Fatalf("plan length differs: %d vs %d", len(p1.Plan), len(p2.Plan))
		}
		for i := range p1.Plan {
			if p1.Plan[i] != p2.Plan[i] {
				t.Fatalf("plan[%d] differs: %+v vs %+v", i, p1.Plan[i], p2.Plan[i])
			}
		}
	}
}

func TestNettingLargeCycleReasonable(t *testing.T) {
	// 50 parties, ~200 obligations — sanity-check the algorithm scales.
	n := NewNetting()
	n.Open("big", 0)
	for i := 0; i < 200; i++ {
		from := "p-" + itoaInline(i%50)
		to := "p-" + itoaInline((i+1)%50)
		n.Add("big", from, to, 1.0, "")
	}
	plan, _ := n.Close("big", false)
	if plan.NetTransfers > 50 {
		t.Fatalf("plan should have at most N transfers for N parties: %d", plan.NetTransfers)
	}
}
