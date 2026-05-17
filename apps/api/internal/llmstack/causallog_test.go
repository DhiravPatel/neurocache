package llmstack

import (
	"testing"
)

func TestCausalAppendAndRead(t *testing.T) {
	c := NewCausalLog()
	a, _ := c.Append("log", "alice", "hello", nil)
	b, _ := c.Append("log", "bob", "world", []string{a.EventID})
	rows, _ := c.Read("log", 0)
	if len(rows) != 2 || rows[0].EventID != a.EventID || rows[1].EventID != b.EventID {
		t.Fatalf("topo: %+v", rows)
	}
}

func TestCausalConcurrentEventsStable(t *testing.T) {
	c := NewCausalLog()
	// Two events from different actors, no AFTER relation — concurrent
	c.Append("l", "alice", "1", nil)
	c.Append("l", "bob", "2", nil)
	rows, _ := c.Read("l", 0)
	// Both should be present, in arrival order (stable tie-break)
	if rows[0].Actor != "alice" || rows[1].Actor != "bob" {
		t.Fatalf("stability: %+v", rows)
	}
}

func TestCausalAfterEnforced(t *testing.T) {
	c := NewCausalLog()
	// Append B first with AFTER=A — A doesn't exist yet
	if _, err := c.Append("l", "x", "B", []string{"e-99"}); err == nil {
		t.Fatal("AFTER unknown event should fail")
	}
}

func TestCausalHappensBefore(t *testing.T) {
	c := NewCausalLog()
	a, _ := c.Append("l", "x", "A", nil)
	b, _ := c.Append("l", "y", "B", []string{a.EventID})
	hb, _ := c.HappensBefore("l", a.EventID, b.EventID)
	if !hb.HappensBefore {
		t.Fatalf("a should happen-before b: %+v", hb)
	}
}

func TestCausalConcurrentReport(t *testing.T) {
	c := NewCausalLog()
	a, _ := c.Append("l", "x", "A", nil)
	b, _ := c.Append("l", "y", "B", nil)
	hb, _ := c.HappensBefore("l", a.EventID, b.EventID)
	if !hb.Concurrent {
		t.Fatalf("should be concurrent: %+v", hb)
	}
}

func TestCausalReverseHappensBefore(t *testing.T) {
	c := NewCausalLog()
	a, _ := c.Append("l", "x", "A", nil)
	b, _ := c.Append("l", "y", "B", []string{a.EventID})
	hb, _ := c.HappensBefore("l", b.EventID, a.EventID)
	if hb.HappensBefore || hb.Concurrent {
		t.Fatalf("b should NOT happen-before a: %+v", hb)
	}
}

func TestCausalClockCounter(t *testing.T) {
	c := NewCausalLog()
	c.Append("l", "alice", "1", nil)
	c.Append("l", "alice", "2", nil)
	c.Append("l", "alice", "3", nil)
	n, ok := c.Clock("l", "alice")
	if !ok || n != 3 {
		t.Fatalf("clock = %d", n)
	}
}

func TestCausalClockMergesOnAfter(t *testing.T) {
	c := NewCausalLog()
	a, _ := c.Append("l", "alice", "1", nil)
	a2, _ := c.Append("l", "alice", "2", nil)
	// Bob appends after alice's two events — his clock should reflect them
	c.Append("l", "bob", "3", []string{a.EventID, a2.EventID})
	c.Append("l", "bob", "4", nil) // bob's local clock = 2 now
	n, _ := c.Clock("l", "bob")
	if n != 2 {
		t.Fatalf("bob clock = %d", n)
	}
}

func TestCausalReadLimit(t *testing.T) {
	c := NewCausalLog()
	for i := 0; i < 10; i++ {
		c.Append("l", "x", itoaBench(i), nil)
	}
	rows, _ := c.Read("l", 3)
	if len(rows) != 3 {
		t.Fatalf("limit = %d", len(rows))
	}
}

func TestCausalForget(t *testing.T) {
	c := NewCausalLog()
	c.Append("a", "x", "y", nil)
	c.Append("b", "x", "y", nil)
	if c.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if c.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestCausalStats(t *testing.T) {
	c := NewCausalLog()
	c.Append("l", "x", "1", nil)
	c.Read("l", 0)
	s := c.Stats()
	if s.TotalAppends != 1 || s.TotalReads != 1 || s.TotalEvents != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestCausalRejectsBadInput(t *testing.T) {
	c := NewCausalLog()
	if _, err := c.Append("", "x", "y", nil); err == nil {
		t.Fatal("empty log should fail")
	}
	if _, err := c.Append("l", "", "y", nil); err == nil {
		t.Fatal("empty actor should fail")
	}
}
