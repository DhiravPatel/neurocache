package store

import "testing"

func TestConsumerGroupBasicFlow(t *testing.T) {
	s := New()
	if _, err := s.XAdd("orders", "*", []string{"a", "1"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.XGroupCreate("orders", "g1", "0", false); err != nil {
		t.Fatalf("xgroup create: %v", err)
	}
	if _, err := s.XAdd("orders", "*", []string{"a", "2"}, 0); err != nil {
		t.Fatal(err)
	}
	out, err := s.XReadGroup("g1", "alice", []string{"orders"}, []string{">"}, 0, false)
	if err != nil {
		t.Fatalf("readgroup: %v", err)
	}
	if got := len(out["orders"]); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}
	// pending should reflect both entries
	resp, err := s.XPending("orders", "g1", true, "-", "+", 0, "")
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	sum := resp.(PendingSummary)
	if sum.Count != 2 {
		t.Fatalf("expected 2 pending, got %d", sum.Count)
	}
	// ACK the first
	first := out["orders"][0].ID.String()
	n, err := s.XAck("orders", "g1", []string{first})
	if err != nil || n != 1 {
		t.Fatalf("xack: n=%d err=%v", n, err)
	}
}

func TestXClaimReassigns(t *testing.T) {
	s := New()
	_, _ = s.XAdd("k", "*", []string{"v", "1"}, 0)
	_ = s.XGroupCreate("k", "g", "0", false)
	out, _ := s.XReadGroup("g", "c1", []string{"k"}, []string{">"}, 0, false)
	id := out["k"][0].ID.String()
	claimed, _, err := s.XClaim("k", "g", "c2", 0, []string{id}, XClaimOpts{})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claimed))
	}
}
