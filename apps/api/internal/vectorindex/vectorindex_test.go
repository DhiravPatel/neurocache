package vectorindex

import (
	"strconv"
	"testing"
)

// ── ParseVector / EncodeVector roundtrip ──────────────────────────

func TestParseVectorBinaryRoundtrip(t *testing.T) {
	want := []float32{1.5, -2.25, 3.75, 0, 4.125}
	encoded := EncodeVector(want)
	got, err := ParseVector(encoded, len(want))
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("element %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParseVectorCSV(t *testing.T) {
	got, err := ParseVector("1.0, 2.0,3.5 , -4", 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []float32{1, 2, 3.5, -4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("element %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParseVectorDimMismatch(t *testing.T) {
	if _, err := ParseVector("1,2,3", 4); err == nil {
		t.Fatal("expected dimension-mismatch error")
	}
}

// ── FLAT ──────────────────────────────────────────────────────────

func TestFlatExactKNN(t *testing.T) {
	idx, err := New(Options{Algo: AlgoFlat, Dim: 3, Metric: MetricL2})
	if err != nil {
		t.Fatal(err)
	}
	idx.Set("close", []float32{1, 0, 0})
	idx.Set("mid", []float32{0.5, 0.5, 0})
	idx.Set("far", []float32{0, 0, 1})
	res := idx.KNN([]float32{1, 0, 0}, 3)
	if len(res) != 3 || res[0].ID != "close" {
		t.Fatalf("FLAT KNN result wrong: %+v", res)
	}
	if res[2].ID != "far" {
		t.Fatalf("expected 'far' last, got %s", res[2].ID)
	}
}

func TestFlatReplaceVector(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoFlat, Dim: 2, Metric: MetricL2})
	idx.Set("k", []float32{0, 0})
	idx.Set("k", []float32{10, 10}) // replace
	v, ok := idx.Get("k")
	if !ok || v[0] != 10 {
		t.Fatalf("replace failed: v=%v ok=%v", v, ok)
	}
	if idx.Card() != 1 {
		t.Fatalf("replace should not duplicate; card=%d", idx.Card())
	}
}

func TestFlatDel(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoFlat, Dim: 1, Metric: MetricL2})
	idx.Set("a", []float32{1})
	idx.Set("b", []float32{2})
	if !idx.Del("a") {
		t.Fatal("Del should report true on existing id")
	}
	if idx.Del("a") {
		t.Fatal("repeat Del should report false")
	}
	if idx.Card() != 1 {
		t.Fatalf("card after Del = %d, want 1", idx.Card())
	}
}

// ── HNSW ──────────────────────────────────────────────────────────

func TestHNSWFindsNearestOfMany(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoHNSW, Dim: 2, Metric: MetricL2, M: 16, EFC: 200, EFR: 50})
	for i := 0; i < 200; i++ {
		x := float32(i)
		idx.Set("p"+strconv.Itoa(i), []float32{x, x})
	}
	// Query right at p50; top-1 should be p50 (or very near).
	res := idx.KNN([]float32{50, 50}, 5)
	if len(res) != 5 {
		t.Fatalf("expected 5 results, got %d", len(res))
	}
	// Probabilistic — accept anything within ±2 of the true nearest.
	if res[0].ID != "p50" && res[0].ID != "p49" && res[0].ID != "p51" {
		t.Fatalf("HNSW top hit too far from query: %s", res[0].ID)
	}
}

func TestHNSWLinksAfterInsert(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoHNSW, Dim: 2, Metric: MetricL2, M: 8})
	for i := 0; i < 20; i++ {
		x := float32(i)
		idx.Set("n"+strconv.Itoa(i), []float32{x, x})
	}
	links := idx.Links("n10")
	if len(links) == 0 {
		t.Fatalf("expected at least the layer-0 neighbour list, got none")
	}
	for _, layer := range links {
		for _, neighbour := range layer {
			if neighbour == "n10" {
				t.Fatal("a node should not list itself as a neighbour")
			}
		}
	}
}

// ── attributes ────────────────────────────────────────────────────

func TestAttrLifecycle(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoFlat, Dim: 1, Metric: MetricL2})
	idx.Set("a", []float32{1})
	if !idx.SetAttr("a", `{"tag":"x"}`) {
		t.Fatal("SetAttr should succeed for existing id")
	}
	if got, ok := idx.GetAttr("a"); !ok || got != `{"tag":"x"}` {
		t.Fatalf("GetAttr returned %q ok=%v", got, ok)
	}
	if !idx.DelAttr("a") {
		t.Fatal("DelAttr should report true on existing attr")
	}
	if _, ok := idx.GetAttr("a"); ok {
		t.Fatal("attr should be gone after DelAttr")
	}
}

func TestAttrRejectsMissingMember(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoFlat, Dim: 1, Metric: MetricL2})
	if idx.SetAttr("ghost", `{}`) {
		t.Fatal("SetAttr should refuse unknown member")
	}
}

// ── invariants ────────────────────────────────────────────────────

func TestKNNDimensionMismatchSafe(t *testing.T) {
	idx, _ := New(Options{Algo: AlgoFlat, Dim: 3, Metric: MetricL2})
	idx.Set("a", []float32{1, 2, 3})
	if got := idx.KNN([]float32{1, 2}, 5); got != nil {
		t.Fatal("KNN with wrong dim must return nil, not a partial result")
	}
}

func TestNewRequiresDim(t *testing.T) {
	if _, err := New(Options{Algo: AlgoFlat, Dim: 0}); err == nil {
		t.Fatal("New with Dim=0 must error")
	}
}
