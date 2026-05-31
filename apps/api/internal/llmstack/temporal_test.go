package llmstack

import (
	"testing"
	"time"
)

func TestTemporalSnapshotContributeClose(t *testing.T) {
	tm := NewTemporalSnapshots()
	if err := tm.Snapshot("s1", nil); err != nil {
		t.Fatal(err)
	}
	tm.Contribute("s1", "memory", `{"k":"v"}`)
	tm.Contribute("s1", "trust", `{"x":0.5}`)
	tm.Close("s1")
	v, _ := tm.Get("s1")
	if !v.Closed || len(v.Stores) != 2 {
		t.Fatalf("snapshot: %+v", v)
	}
}

func TestTemporalContributeRejectedAfterClose(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("s", nil)
	tm.Close("s")
	if err := tm.Contribute("s", "x", "y"); err == nil {
		t.Fatal("post-close contribute should fail")
	}
}

func TestTemporalAtFindsClosest(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("a", nil)
	tm.Close("a")
	time.Sleep(20 * time.Millisecond)
	tm.Snapshot("b", nil)
	tm.Close("b")
	// At "now" → b
	v, ok := tm.At(time.Now().UnixMilli())
	if !ok || v.SnapID != "b" {
		t.Fatalf("at now = %+v", v)
	}
}

func TestTemporalAtIgnoresOpenSnapshots(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("open", nil) // not closed
	if _, ok := tm.At(time.Now().UnixMilli()); ok {
		t.Fatal("open snapshot should not be returned")
	}
}

func TestTemporalDiff(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("a", nil)
	tm.Contribute("a", "x", "1")
	tm.Contribute("a", "y", "1")
	tm.Close("a")
	tm.Snapshot("b", nil)
	tm.Contribute("b", "x", "1")     // same
	tm.Contribute("b", "y", "2")     // changed
	tm.Contribute("b", "z", "3")     // only in b
	tm.Close("b")
	d, _ := tm.Diff("a", "b")
	if d.Identical {
		t.Fatal("should be different")
	}
	if len(d.Changed) != 1 || d.Changed[0] != "y" {
		t.Fatalf("changed: %+v", d.Changed)
	}
	if len(d.OnlyInB) != 1 || d.OnlyInB[0] != "z" {
		t.Fatalf("only_in_b: %+v", d.OnlyInB)
	}
}

func TestTemporalListReverseChrono(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("a", nil)
	tm.Close("a")
	time.Sleep(5 * time.Millisecond)
	tm.Snapshot("b", nil)
	tm.Close("b")
	rows := tm.List(0)
	if rows[0].SnapID != "b" {
		t.Fatalf("not reverse chrono: %+v", rows)
	}
}

func TestTemporalDuplicateSnapshotFails(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("s", nil)
	if err := tm.Snapshot("s", nil); err == nil {
		t.Fatal("duplicate snapshot should fail")
	}
}

func TestTemporalForget(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("a", nil)
	tm.Snapshot("b", nil)
	if tm.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if tm.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestTemporalStats(t *testing.T) {
	tm := NewTemporalSnapshots()
	tm.Snapshot("a", nil)
	tm.Contribute("a", "x", "y")
	tm.Close("a")
	tm.Get("a")
	s := tm.Stats()
	if s.TotalSnaps != 1 || s.TotalContrib != 1 || s.TotalQueries == 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestTemporalRejectsBadInput(t *testing.T) {
	tm := NewTemporalSnapshots()
	if err := tm.Snapshot("", nil); err == nil {
		t.Fatal("empty id")
	}
	if err := tm.Contribute("", "x", "y"); err == nil {
		t.Fatal("empty snap id")
	}
	if err := tm.Contribute("nope", "x", "y"); err == nil {
		t.Fatal("unknown snap should fail")
	}
}
