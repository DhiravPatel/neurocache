package llmstack

import (
	"testing"
	"time"
)

func TestMarketCreateAndBid(t *testing.T) {
	m := NewMarket()
	if err := m.Create("gpu", 8, "uniform", 0, 0); err != nil {
		t.Fatal(err)
	}
	r, err := m.Bid("gpu", "a1", 0.5, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.BidID == "" {
		t.Fatal("bid id empty")
	}
}

func TestMarketClearUniformHighestWins(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 4, "uniform", 0, 0)
	m.Bid("gpu", "a1", 0.10, 2, 0)
	m.Bid("gpu", "a2", 0.50, 2, 0)
	m.Bid("gpu", "a3", 0.30, 2, 0)
	r, _ := m.Clear("gpu")
	// 4 slots; a2 (0.50, 2) + a3 (0.30, 2) win. a1 unfilled.
	if len(r.Filled) != 2 {
		t.Fatalf("filled = %d", len(r.Filled))
	}
	if r.Filled[0].AgentID != "a2" {
		t.Fatalf("highest bidder not first: %+v", r.Filled)
	}
	if r.ClearingPrice != 0.30 {
		t.Fatalf("uniform clearing = %f, want 0.30 (lowest winning)", r.ClearingPrice)
	}
}

func TestMarketClearSecondPrice(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 2, "second_price", 0, 0)
	m.Bid("gpu", "a1", 0.10, 1, 0)
	m.Bid("gpu", "a2", 0.50, 1, 0)
	m.Bid("gpu", "a3", 0.30, 1, 0)
	r, _ := m.Clear("gpu")
	// Winners: a2, a3 (top-2). Losing bid: a1 at 0.10. Vickrey price = 0.10.
	if r.ClearingPrice != 0.10 {
		t.Fatalf("second_price clearing = %f, want 0.10", r.ClearingPrice)
	}
}

func TestMarketPartialFill(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 3, "uniform", 0, 0)
	m.Bid("gpu", "a1", 0.5, 2, 0)
	m.Bid("gpu", "a2", 0.3, 2, 0)
	r, _ := m.Clear("gpu")
	// a1 wins 2, a2 wins 1 (partial)
	if r.Filled[1].Awarded != 1 {
		t.Fatalf("partial fill = %d, want 1", r.Filled[1].Awarded)
	}
}

func TestMarketCapacityExceeded(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 2, "uniform", 0, 0)
	m.Bid("gpu", "a1", 0.5, 1, 0)
	m.Bid("gpu", "a2", 0.4, 1, 0)
	m.Bid("gpu", "a3", 0.3, 1, 0) // won't fit
	r, _ := m.Clear("gpu")
	if len(r.Filled) != 2 || len(r.Unfilled) != 1 {
		t.Fatalf("filled=%d unfilled=%d", len(r.Filled), len(r.Unfilled))
	}
	if r.Unfilled[0].AgentID != "a3" {
		t.Fatalf("loser = %s", r.Unfilled[0].AgentID)
	}
}

func TestMarketWindowMemoization(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 4, "uniform", time.Hour, 0)
	m.Bid("gpu", "a1", 0.5, 2, 0)
	r1, _ := m.Clear("gpu")
	m.Bid("gpu", "a2", 0.9, 2, 0)
	r2, _ := m.Clear("gpu")
	// Within the window, same result returned (a2's bid doesn't enter)
	if r1.ClearingPrice != r2.ClearingPrice {
		t.Fatalf("memoization failed: %f vs %f", r1.ClearingPrice, r2.ClearingPrice)
	}
}

func TestMarketLeaseAndRelease(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 2, "uniform", 0, 0)
	m.Bid("gpu", "a1", 0.5, 1, 0)
	m.Clear("gpu")
	l, err := m.Lease("gpu", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if l.Token == "" {
		t.Fatal("token empty")
	}
	if n, _ := m.Release("gpu", l.Token); n != 1 {
		t.Fatal("release should drop 1")
	}
}

func TestMarketLeaseRefusesUnwinningAgent(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	m.Bid("gpu", "winner", 0.5, 1, 0)
	m.Bid("gpu", "loser", 0.1, 1, 0)
	m.Clear("gpu")
	if _, err := m.Lease("gpu", "loser"); err == nil {
		t.Fatal("loser should not get a lease")
	}
}

func TestMarketLeaseIsSingleUse(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	m.Bid("gpu", "a", 0.5, 1, 0)
	m.Clear("gpu")
	if _, err := m.Lease("gpu", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Lease("gpu", "a"); err == nil {
		t.Fatal("second lease should fail")
	}
}

func TestMarketPriceSignal(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	m.Bid("gpu", "a", 0.5, 1, 0)
	m.Clear("gpu")
	p, _ := m.Price("gpu")
	if p != 0.5 {
		t.Fatalf("price = %f", p)
	}
}

func TestMarketDeadlineDropsBid(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 4, "uniform", 0, 0)
	m.Bid("gpu", "a", 0.5, 1, 5*time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	r, _ := m.Clear("gpu")
	if len(r.Filled) != 0 {
		t.Fatalf("expired bid filled: %+v", r.Filled)
	}
}

func TestMarketMaxBidsPerAgent(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 10, "uniform", 0, 2)
	m.Bid("gpu", "a", 0.5, 1, 0)
	m.Bid("gpu", "a", 0.5, 1, 0)
	if _, err := m.Bid("gpu", "a", 0.5, 1, 0); err == nil {
		t.Fatal("third bid should fail (cap=2)")
	}
}

func TestMarketStarvedTracking(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	// Loser bids every round, never wins
	for i := 0; i < 5; i++ {
		m.Bid("gpu", "winner", 0.9, 1, 0)
		m.Bid("gpu", "loser", 0.1, 1, 0)
		m.Clear("gpu")
	}
	rows, _ := m.Starved("gpu", 3)
	if len(rows) != 1 || rows[0].AgentID != "loser" {
		t.Fatalf("starved = %+v", rows)
	}
}

func TestMarketStatus(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 4, "uniform", 0, 0)
	m.Bid("gpu", "a", 0.5, 1, 0)
	st, _ := m.Status("gpu")
	if st.PendingBids != 1 || st.Capacity != 4 {
		t.Fatalf("status = %+v", st)
	}
}

func TestMarketUnfilledBidsCarryForward(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	m.Bid("gpu", "winner", 0.9, 1, 0)
	m.Bid("gpu", "loser", 0.1, 1, 0)
	m.Clear("gpu")
	// Winner's bid is consumed; loser stays for the next round
	st, _ := m.Status("gpu")
	if st.PendingBids != 1 {
		t.Fatalf("loser should carry forward: pending=%d", st.PendingBids)
	}
}

func TestMarketForgetAndList(t *testing.T) {
	m := NewMarket()
	m.Create("a", 1, "uniform", 0, 0)
	m.Create("b", 1, "uniform", 0, 0)
	if l := m.List(); len(l) != 2 {
		t.Fatalf("list = %v", l)
	}
	if m.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if m.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestMarketStats(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	m.Bid("gpu", "a", 0.5, 1, 0)
	m.Clear("gpu")
	m.Lease("gpu", "a")
	s := m.Stats()
	if s.TotalBids != 1 || s.TotalClears != 1 || s.TotalLeases != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestMarketRejectsBadInput(t *testing.T) {
	m := NewMarket()
	if err := m.Create("", 1, "uniform", 0, 0); err == nil {
		t.Fatal("empty market should fail")
	}
	if err := m.Create("m", 0, "uniform", 0, 0); err == nil {
		t.Fatal("zero capacity should fail")
	}
	if err := m.Create("m", 1, "invalid", 0, 0); err == nil {
		t.Fatal("unknown clearing should fail")
	}
	m.Create("m", 1, "uniform", 0, 0)
	if _, err := m.Bid("m", "", 0.5, 1, 0); err == nil {
		t.Fatal("empty agent should fail")
	}
	if _, err := m.Bid("m", "a", -1, 1, 0); err == nil {
		t.Fatal("negative price should fail")
	}
	if _, err := m.Bid("m", "a", 0.5, 0, 0); err == nil {
		t.Fatal("zero qty should fail")
	}
}

func TestMarketTieBrokenByPostingOrder(t *testing.T) {
	m := NewMarket()
	m.Create("gpu", 1, "uniform", 0, 0)
	m.Bid("gpu", "first", 0.5, 1, 0)
	time.Sleep(1 * time.Millisecond)
	m.Bid("gpu", "second", 0.5, 1, 0)
	r, _ := m.Clear("gpu")
	if r.Filled[0].AgentID != "first" {
		t.Fatalf("ties should favour earlier bid: %+v", r.Filled)
	}
}

func BenchmarkMarketBid(b *testing.B) {
	m := NewMarket()
	m.Create("gpu", 100, "uniform", 0, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Bid("gpu", "a", 0.5, 1, 0)
	}
}

func BenchmarkMarketClear(b *testing.B) {
	m := NewMarket()
	m.Create("gpu", 50, "uniform", 0, 0)
	for i := 0; i < 200; i++ {
		m.Bid("gpu", "a", float64(i)/200, 1, 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Clear("gpu")
	}
}
