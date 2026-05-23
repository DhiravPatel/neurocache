package llmstack

import (
	"testing"
	"time"
)

func TestNLISetAndGet(t *testing.T) {
	n := NewNLICache()
	n.Set("Paris is the capital of France",
		"France's capital is Paris", "entails", 0.95, 0)
	r, ok := n.Get("Paris is the capital of France", "France's capital is Paris")
	if !ok {
		t.Fatal("get returned false")
	}
	if r.Relation != "entails" || r.Score != 0.95 {
		t.Fatalf("got = %+v", r)
	}
}

func TestNLIThreeRelations(t *testing.T) {
	n := NewNLICache()
	n.Set("A", "A1", "entails", 0.9, 0)
	n.Set("A", "A2", "contradicts", 0.85, 0)
	n.Set("A", "A3", "neutral", 0.5, 0)
	r1, _ := n.Get("A", "A1")
	r2, _ := n.Get("A", "A2")
	r3, _ := n.Get("A", "A3")
	if r1.Relation != "entails" || r2.Relation != "contradicts" || r3.Relation != "neutral" {
		t.Fatalf("relations = %v %v %v", r1, r2, r3)
	}
}

func TestNLIRejectsBadRelation(t *testing.T) {
	n := NewNLICache()
	if err := n.Set("a", "b", "magic", 0.5, 0); err == nil {
		t.Fatal("invalid relation should fail")
	}
}

func TestNLIRejectsBadScore(t *testing.T) {
	n := NewNLICache()
	if err := n.Set("a", "b", "entails", -0.1, 0); err == nil {
		t.Fatal("score < 0 should fail")
	}
	if err := n.Set("a", "b", "entails", 1.5, 0); err == nil {
		t.Fatal("score > 1 should fail")
	}
}

func TestNLIMiss(t *testing.T) {
	n := NewNLICache()
	if _, ok := n.Get("a", "b"); ok {
		t.Fatal("expected miss")
	}
}

func TestNLICheckUsesDefaultOnMiss(t *testing.T) {
	n := NewNLICache()
	r := n.Check("a", "b", "neutral")
	if r.Cached {
		t.Fatal("Cached should be false on miss")
	}
	if r.Relation != "neutral" {
		t.Fatalf("default not applied: %+v", r)
	}
}

func TestNLICheckUsesCacheOnHit(t *testing.T) {
	n := NewNLICache()
	n.Set("a", "b", "entails", 0.9, 0)
	r := n.Check("a", "b", "neutral")
	if !r.Cached {
		t.Fatal("Cached should be true on hit")
	}
	if r.Relation != "entails" {
		t.Fatalf("cached value not used: %+v", r)
	}
}

func TestNLITTL(t *testing.T) {
	n := NewNLICache()
	n.Set("a", "b", "entails", 0.9, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := n.Get("a", "b"); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestNLIMGet(t *testing.T) {
	n := NewNLICache()
	n.Set("doc", "claim1", "entails", 0.9, 0)
	n.Set("doc", "claim2", "contradicts", 0.85, 0)
	rows := n.MGet("doc", []string{"claim1", "claim2", "claim3"})
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if !rows[0].Cached || rows[0].Relation != "entails" {
		t.Fatalf("row[0] = %+v", rows[0])
	}
	if !rows[1].Cached || rows[1].Relation != "contradicts" {
		t.Fatalf("row[1] = %+v", rows[1])
	}
	if rows[2].Cached {
		t.Fatalf("row[2] should miss: %+v", rows[2])
	}
}

func TestNLIForget(t *testing.T) {
	n := NewNLICache()
	n.Set("a", "b", "entails", 0.5, 0)
	if !n.Forget("a", "b") {
		t.Fatal("forget should return true")
	}
	if n.Forget("a", "b") {
		t.Fatal("forget on missing should return false")
	}
}

func TestNLIPerRelationStats(t *testing.T) {
	n := NewNLICache()
	n.Set("a", "b1", "entails", 0.9, 0)
	n.Set("a", "b2", "contradicts", 0.9, 0)
	for i := 0; i < 3; i++ {
		n.Get("a", "b1")
	}
	for i := 0; i < 2; i++ {
		n.Get("a", "b2")
	}
	s := n.Stats()
	if s.HitsEntails != 3 || s.HitsContradicts != 2 {
		t.Fatalf("per-relation hits = %+v", s)
	}
}

func TestNLIStatsAdvance(t *testing.T) {
	n := NewNLICache()
	n.Set("a", "b", "entails", 0.5, 0)
	n.Get("a", "b")
	n.Get("a", "missing")
	s := n.Stats()
	if s.TotalGets != 2 || s.TotalHits != 1 || s.TotalMisses != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
