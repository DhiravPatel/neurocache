package llmstack

import (
	"sync"
	"testing"
)

func TestSettleOpenAndBalanceZero(t *testing.T) {
	s := NewSettlement()
	if err := s.Open("acme", AcctAsset, "USD", nil); err != nil {
		t.Fatal(err)
	}
	b, ok := s.Balance("acme")
	if !ok || b.Balance != 0 {
		t.Fatalf("balance: %+v", b)
	}
}

func TestSettleAtomicBalancedTxn(t *testing.T) {
	s := NewSettlement()
	s.Open("acme", AcctAsset, "USD", nil)
	s.Open("revenue", AcctIncome, "USD", nil)
	// Seed acme with assets
	s.Txn("seed", "", []SettleLine{{Account: "acme", Amount: 100}}, []SettleLine{{Account: "revenue", Amount: 100}})
	a, _ := s.Balance("acme")
	r, _ := s.Balance("revenue")
	if a.Balance != 100 || r.Balance != 100 {
		t.Fatalf("a=%f r=%f", a.Balance, r.Balance)
	}
}

func TestSettleImbalanceRejected(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	if _, err := s.Txn("t", "", []SettleLine{{Account: "a", Amount: 100}}, []SettleLine{{Account: "b", Amount: 50}}); err == nil {
		t.Fatal("imbalance must be rejected")
	}
}

func TestSettleIdempotencyOnTxnID(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	r1, _ := s.Txn("t1", "", []SettleLine{{Account: "a", Amount: 10}}, []SettleLine{{Account: "b", Amount: 10}})
	r2, _ := s.Txn("t1", "", []SettleLine{{Account: "a", Amount: 10}}, []SettleLine{{Account: "b", Amount: 10}})
	if !r1.Posted || r2.Posted || !r2.Duplicate {
		t.Fatalf("idempotency: r1=%+v r2=%+v", r1, r2)
	}
	a, _ := s.Balance("a")
	if a.Balance != 10 {
		t.Fatalf("balance double-applied: %f", a.Balance)
	}
}

func TestSettleNoNegativeAssetEnforced(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	// Crediting an asset reduces it; with no balance and NO_NEGATIVE, should reject
	if _, err := s.Txn("t", "", []SettleLine{{Account: "b", Amount: 50}}, []SettleLine{{Account: "a", Amount: 50}}); err == nil {
		t.Fatal("overdraw asset should be rejected")
	}
}

func TestSettleLiabilityAllowsNegative(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("payable", AcctLiability, "USD", nil)
	// Seed asset
	s.Txn("seed", "", []SettleLine{{Account: "a", Amount: 100}}, []SettleLine{{Account: "payable", Amount: 100}})
	// "Pay down" payable beyond zero — debit liability further
	_, err := s.Txn("pay", "", []SettleLine{{Account: "payable", Amount: 200}}, []SettleLine{{Account: "a", Amount: 200}})
	// Payable can go negative (you've paid more than you owed = prepayment).
	// But asset is no_negative and we only have 100; transaction should fail.
	if err == nil {
		t.Fatal("asset overdraw should still reject")
	}
}

func TestSettleNoNegativeCanBeDisabled(t *testing.T) {
	s := NewSettlement()
	yes := false
	s.Open("a", AcctAsset, "USD", &yes) // explicitly allow negative
	s.Open("b", AcctIncome, "USD", nil)
	// Now an overdrawing txn should succeed
	if _, err := s.Txn("t", "", []SettleLine{{Account: "b", Amount: 50}}, []SettleLine{{Account: "a", Amount: 50}}); err != nil {
		t.Fatalf("no-negative=false should allow: %v", err)
	}
}

func TestSettleMultiDebitMultiCredit(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctAsset, "USD", nil)
	s.Open("rev1", AcctIncome, "USD", nil)
	s.Open("rev2", AcctIncome, "USD", nil)
	debits := []SettleLine{{Account: "a", Amount: 40}, {Account: "b", Amount: 60}}
	credits := []SettleLine{{Account: "rev1", Amount: 30}, {Account: "rev2", Amount: 70}}
	if _, err := s.Txn("t", "", debits, credits); err != nil {
		t.Fatal(err)
	}
	ba, _ := s.Balance("a")
	bb, _ := s.Balance("b")
	if ba.Balance != 40 || bb.Balance != 60 {
		t.Fatalf("multi-line: a=%f b=%f", ba.Balance, bb.Balance)
	}
}

func TestSettleRequiresBothSides(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	if _, err := s.Txn("t", "", []SettleLine{{Account: "a", Amount: 10}}, nil); err == nil {
		t.Fatal("no credit side should fail")
	}
}

func TestSettleReverse(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	s.Txn("orig", "", []SettleLine{{Account: "a", Amount: 100}}, []SettleLine{{Account: "b", Amount: 100}})
	if _, err := s.Reverse("orig", "rev", "oops"); err != nil {
		t.Fatal(err)
	}
	ba, _ := s.Balance("a")
	if ba.Balance != 0 {
		t.Fatalf("reverse should zero a: %f", ba.Balance)
	}
}

func TestSettleReconcileBalanced(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	s.Txn("t1", "", []SettleLine{{Account: "a", Amount: 100}}, []SettleLine{{Account: "b", Amount: 100}})
	s.Txn("t2", "", []SettleLine{{Account: "a", Amount: 50}}, []SettleLine{{Account: "b", Amount: 50}})
	r := s.Reconcile()
	if !r.Balanced || r.TotalDebits != r.TotalCredits {
		t.Fatalf("reconcile: %+v", r)
	}
}

func TestSettleStatement(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	s.Txn("t1", "first", []SettleLine{{Account: "a", Amount: 10}}, []SettleLine{{Account: "b", Amount: 10}})
	s.Txn("t2", "second", []SettleLine{{Account: "a", Amount: 20}}, []SettleLine{{Account: "b", Amount: 20}})
	rows, _ := s.Statement("a", 0, 0, 0)
	if len(rows) != 2 || rows[1].Balance != 30 {
		t.Fatalf("statement: %+v", rows)
	}
}

func TestSettleClosedAccountRejected(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	s.Close("a")
	if _, err := s.Txn("t", "", []SettleLine{{Account: "a", Amount: 10}}, []SettleLine{{Account: "b", Amount: 10}}); err == nil {
		t.Fatal("closed account should reject")
	}
}

func TestSettleUnknownAccountRejected(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	if _, err := s.Txn("t", "", []SettleLine{{Account: "a", Amount: 1}}, []SettleLine{{Account: "ghost", Amount: 1}}); err == nil {
		t.Fatal("unknown account should reject")
	}
}

func TestSettleNegativeAmountRejected(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	if _, err := s.Txn("t", "", []SettleLine{{Account: "a", Amount: -10}}, []SettleLine{{Account: "b", Amount: -10}}); err == nil {
		t.Fatal("negative amount should reject")
	}
}

func TestSettleConcurrentSafe(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	// Many parallel txns; idempotency on txn-id means duplicates are no-ops.
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Txn("t-"+itoaBench(i), "", []SettleLine{{Account: "a", Amount: 1}}, []SettleLine{{Account: "b", Amount: 1}})
		}(i)
	}
	wg.Wait()
	ba, _ := s.Balance("a")
	if ba.Balance != N {
		t.Fatalf("concurrent: balance %f != %d", ba.Balance, N)
	}
	r := s.Reconcile()
	if !r.Balanced {
		t.Fatalf("concurrent reconcile failed: %+v", r)
	}
}

func TestSettleStats(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	s.Txn("t", "", []SettleLine{{Account: "a", Amount: 1}}, []SettleLine{{Account: "b", Amount: 1}})
	s.Txn("t", "", []SettleLine{{Account: "a", Amount: 1}}, []SettleLine{{Account: "b", Amount: 1}}) // dupe
	st := s.Stats()
	if st.TotalTxns != 1 || st.TotalDuplicates != 1 || st.TotalOpens != 2 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestSettleRejectsBadInput(t *testing.T) {
	s := NewSettlement()
	if err := s.Open("", AcctAsset, "USD", nil); err == nil {
		t.Fatal("empty name")
	}
	if err := s.Open("a", "bogus", "USD", nil); err == nil {
		t.Fatal("bad type")
	}
	if err := s.Open("a", AcctAsset, "USD", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Open("a", AcctAsset, "USD", nil); err == nil {
		t.Fatal("duplicate open should reject")
	}
}

func TestSettleGetRoundtrip(t *testing.T) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("b", AcctIncome, "USD", nil)
	s.Txn("t", "memo!", []SettleLine{{Account: "a", Amount: 5}}, []SettleLine{{Account: "b", Amount: 5}})
	v, ok := s.Get("t")
	if !ok || v.Memo != "memo!" {
		t.Fatalf("get: %+v", v)
	}
}

func BenchmarkSettleTxn(b *testing.B) {
	s := NewSettlement()
	s.Open("a", AcctAsset, "USD", nil)
	s.Open("rev", AcctIncome, "USD", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Txn("t-"+itoaBench(i), "", []SettleLine{{Account: "a", Amount: 1}}, []SettleLine{{Account: "rev", Amount: 1}})
	}
}
