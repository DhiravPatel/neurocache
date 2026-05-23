package llmstack

import (
	"testing"
)

func TestInvalidatorTrackAndSemanticHit(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("semantic", "answer:abc",
		"customers can get a refund within 30 days of purchase", nil)
	inv.Track("semantic", "answer:xyz",
		"shipping takes 3-5 business days", nil)

	r := inv.Semantic("refund policy", SemanticOpts{Threshold: 0.20})
	if r.Total == 0 {
		t.Fatal("refund-related entry should match")
	}
	foundRefund := false
	for _, h := range r.Hits {
		if h.Key == "answer:abc" {
			foundRefund = true
		}
	}
	if !foundRefund {
		t.Fatalf("expected answer:abc in hits, got %+v", r.Hits)
	}
}

func TestInvalidatorRejectsEmpty(t *testing.T) {
	inv := NewSemanticInvalidator()
	if err := inv.Track("", "k", "t", nil); err == nil {
		t.Fatal("empty layer should fail")
	}
	if err := inv.Track("l", "", "t", nil); err == nil {
		t.Fatal("empty key should fail")
	}
	if err := inv.Track("l", "k", "", nil); err == nil {
		t.Fatal("empty text should fail")
	}
}

func TestInvalidatorLayerFilter(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("semantic", "a", "refund policy text here", nil)
	inv.Track("llm", "b", "refund policy text here", nil)
	r := inv.Semantic("refund", SemanticOpts{Threshold: 0.10, Layers: []string{"semantic"}})
	for _, h := range r.Hits {
		if h.Layer != "semantic" {
			t.Fatalf("layer filter leaked: %s", h.Layer)
		}
	}
}

func TestInvalidatorUntrack(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("l", "k", "x", nil)
	if !inv.Untrack("l", "k") {
		t.Fatal("untrack should return true")
	}
	if inv.Untrack("l", "k") {
		t.Fatal("untrack on missing should return false")
	}
}

func TestInvalidatorList(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("a", "k1", "text1", nil)
	inv.Track("a", "k2", "text2", nil)
	inv.Track("b", "k3", "text3", nil)
	rows := inv.List("", 0)
	if len(rows) != 3 {
		t.Fatalf("list = %d, want 3", len(rows))
	}
	rowsA := inv.List("a", 0)
	if len(rowsA) != 2 {
		t.Fatalf("layer filter = %d, want 2", len(rowsA))
	}
}

func TestInvalidatorListLimit(t *testing.T) {
	inv := NewSemanticInvalidator()
	for i := 0; i < 20; i++ {
		inv.Track("l", string(rune('a'+i)), "text", nil)
	}
	rows := inv.List("", 5)
	if len(rows) != 5 {
		t.Fatalf("limit not honored: %d", len(rows))
	}
}

func TestInvalidatorPurgeLayer(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("a", "k1", "x", nil)
	inv.Track("a", "k2", "x", nil)
	inv.Track("b", "k3", "x", nil)
	if n := inv.PurgeLayer("a"); n != 2 {
		t.Fatalf("purge a = %d, want 2", n)
	}
	rows := inv.List("", 0)
	if len(rows) != 1 || rows[0].Key != "k3" {
		t.Fatalf("after purge a: %+v", rows)
	}
}

func TestInvalidatorThresholdFilter(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("l", "near", "refund policy thirty day window", nil)
	inv.Track("l", "far", "totally unrelated topic about weather", nil)
	// High threshold — only very close match should survive
	r := inv.Semantic("refund policy thirty day window", SemanticOpts{Threshold: 0.85})
	for _, h := range r.Hits {
		if h.Key == "far" {
			t.Fatalf("far entry should not match at threshold 0.85: %+v", h)
		}
	}
}

func TestInvalidatorStatsAdvance(t *testing.T) {
	inv := NewSemanticInvalidator()
	inv.Track("l", "k", "text here for matching", nil)
	inv.Semantic("matching text", SemanticOpts{Threshold: 0.2})
	s := inv.Stats()
	if s.TotalTracks != 1 || s.TotalScans != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
