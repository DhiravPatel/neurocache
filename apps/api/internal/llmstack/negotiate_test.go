package llmstack

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestNegoOpenOfferAccept(t *testing.T) {
	n := NewNegotiations()
	n.Open("d1", "buyer", "seller", "widget", NegoOpenOpts{})
	n.Offer("d1", "seller", 100, "")
	n.Accept("d1", "buyer")
	v, _ := n.Get("d1")
	if v.State != "accepted" || v.CurrentPrice != 100 {
		t.Fatalf("get: %+v", v)
	}
}

func TestNegoCounterFlow(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "buyer", "seller", "x", NegoOpenOpts{})
	n.Offer("d", "seller", 100, "")
	n.Counter("d", "buyer", 80, "")
	n.Counter("d", "seller", 90, "")
	n.Accept("d", "buyer")
	v, _ := n.Get("d")
	if v.CurrentPrice != 90 {
		t.Fatalf("expected 90: %+v", v)
	}
	if len(v.Moves) != 4 {
		t.Fatalf("moves: %d", len(v.Moves))
	}
}

func TestNegoBatnaBuyerEnforced(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "buyer", "seller", "x", NegoOpenOpts{
		BatnaBuyer: f64(50),
	})
	n.Offer("d", "seller", 100, "")
	if err := n.Accept("d", "buyer"); err == nil {
		t.Fatal("buyer accepting above BATNA should fail")
	}
}

func TestNegoBatnaSellerEnforced(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "buyer", "seller", "x", NegoOpenOpts{
		BatnaSeller: f64(80),
	})
	n.Offer("d", "buyer", 50, "")
	if err := n.Accept("d", "seller"); err == nil {
		t.Fatal("seller accepting below BATNA should fail")
	}
}

func TestNegoSelfAcceptRejected(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "b", "s", "x", NegoOpenOpts{})
	n.Offer("d", "s", 100, "")
	if err := n.Accept("d", "s"); err == nil {
		t.Fatal("self-accept should fail")
	}
}

func TestNegoWalkAwayIsFinal(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "b", "s", "x", NegoOpenOpts{})
	n.Walk("d", "b", "not interested")
	if err := n.Offer("d", "s", 10, ""); err == nil {
		t.Fatal("post-walk offer should fail")
	}
}

func TestNegoRejectIsFinal(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "b", "s", "x", NegoOpenOpts{})
	n.Offer("d", "s", 100, "")
	n.Reject("d", "b", "")
	if err := n.Counter("d", "b", 90, ""); err == nil {
		t.Fatal("post-reject counter should fail")
	}
}

func TestNegoUnknownParty(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "b", "s", "x", NegoOpenOpts{})
	if err := n.Offer("d", "intruder", 100, ""); err == nil {
		t.Fatal("unknown party should fail")
	}
}

func TestNegoBuyerSellerSameRejected(t *testing.T) {
	n := NewNegotiations()
	if err := n.Open("d", "same", "same", "x", NegoOpenOpts{}); err == nil {
		t.Fatal("buyer == seller should fail")
	}
}

func TestNegoDeadlineExpires(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "b", "s", "x", NegoOpenOpts{Deadline: 5 * time.Millisecond})
	time.Sleep(15 * time.Millisecond)
	if err := n.Offer("d", "s", 100, ""); err == nil {
		t.Fatal("expired nego should refuse offers")
	}
}

func TestNegoListByState(t *testing.T) {
	n := NewNegotiations()
	n.Open("a", "b", "s", "x", NegoOpenOpts{})
	n.Open("b", "b", "s", "x", NegoOpenOpts{})
	n.Offer("a", "s", 100, "")
	n.Accept("a", "b")
	rows := n.List("accepted")
	if len(rows) != 1 || rows[0].NegoID != "a" {
		t.Fatalf("list: %+v", rows)
	}
}

func TestNegoStats(t *testing.T) {
	n := NewNegotiations()
	n.Open("d", "b", "s", "x", NegoOpenOpts{})
	n.Offer("d", "s", 10, "")
	n.Accept("d", "b")
	st := n.Stats()
	if st.TotalOpens != 1 || st.TotalOffers != 1 || st.TotalAccepts != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestNegoRejectsBadInput(t *testing.T) {
	n := NewNegotiations()
	if err := n.Open("", "b", "s", "x", NegoOpenOpts{}); err == nil {
		t.Fatal("empty id")
	}
	if err := n.Open("d", "", "s", "x", NegoOpenOpts{}); err == nil {
		t.Fatal("empty buyer")
	}
	n.Open("d", "b", "s", "x", NegoOpenOpts{})
	if err := n.Offer("d", "b", -1, ""); err == nil {
		t.Fatal("negative price")
	}
}
