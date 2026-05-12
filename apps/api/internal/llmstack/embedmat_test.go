package llmstack

import (
	"math"
	"strings"
	"testing"
)

func TestEmbedMatSetAndTopK(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "alpha", []float64{1, 0, 0})
	m.Set("docs", "beta", []float64{0, 1, 0})
	m.Set("docs", "gamma", []float64{0.9, 0.1, 0})
	hits := m.TopK("docs", []float64{1, 0, 0}, TopKOpts{K: 2})
	if len(hits) != 2 {
		t.Fatalf("len = %d, want 2", len(hits))
	}
	if hits[0].RowID != "alpha" {
		t.Fatalf("top hit = %s, want alpha", hits[0].RowID)
	}
	if hits[1].RowID != "gamma" {
		t.Fatalf("second hit = %s, want gamma", hits[1].RowID)
	}
}

func TestEmbedMatNormalisedStorage(t *testing.T) {
	// Unnormalised input should be normalised on insert so that
	// cosine = dot product.
	m := NewEmbedMatrix()
	m.Set("docs", "a", []float64{10, 0, 0}) // norm=10
	m.Set("docs", "b", []float64{20, 0, 0}) // norm=20
	v, ok := m.Cosine("docs", "a", "b")
	if !ok {
		t.Fatal("cosine returned false")
	}
	if math.Abs(v-1.0) > 1e-9 {
		t.Fatalf("cosine of parallel vectors = %f, want 1.0", v)
	}
}

func TestEmbedMatDimMismatch(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "a", []float64{1, 0, 0})
	if err := m.Set("docs", "b", []float64{1, 0}); err == nil {
		t.Fatal("expected dim mismatch error")
	}
}

func TestEmbedMatTopKDimMismatch(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "a", []float64{1, 0, 0})
	if hits := m.TopK("docs", []float64{1, 0}, TopKOpts{K: 1}); hits != nil {
		t.Fatalf("dim mismatch should return nil, got %v", hits)
	}
}

func TestEmbedMatFilter(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "user-1", []float64{1, 0, 0})
	m.Set("docs", "user-2", []float64{0.9, 0.1, 0})
	m.Set("docs", "admin-1", []float64{0.95, 0.05, 0})
	hits := m.TopK("docs", []float64{1, 0, 0}, TopKOpts{K: 10, Filter: "user-"})
	if len(hits) != 2 {
		t.Fatalf("filter should keep 2 rows, got %d", len(hits))
	}
	for _, h := range hits {
		if !strings.HasPrefix(h.RowID, "user-") {
			t.Fatalf("filter leaked: %s", h.RowID)
		}
	}
}

func TestEmbedMatDel(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "a", []float64{1, 0, 0})
	if !m.Del("docs", "a") {
		t.Fatal("del should return true")
	}
	if m.Del("docs", "a") {
		t.Fatal("del on missing should return false")
	}
}

func TestEmbedMatForgetReturnsRowCount(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "a", []float64{1, 0, 0})
	m.Set("docs", "b", []float64{0, 1, 0})
	if n := m.Forget("docs"); n != 2 {
		t.Fatalf("forget = %d, want 2", n)
	}
}

func TestEmbedMatLen(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "a", []float64{1, 0, 0})
	m.Set("docs", "b", []float64{0, 1, 0})
	n, ok := m.Len("docs")
	if !ok || n != 2 {
		t.Fatalf("len = %d, ok = %v", n, ok)
	}
	if _, ok := m.Len("nope"); ok {
		t.Fatal("unknown matrix should return false")
	}
}

func TestEmbedMatListPrefix(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("docs", "user-1", []float64{1, 0, 0})
	m.Set("docs", "user-2", []float64{0.9, 0.1, 0})
	m.Set("docs", "admin-1", []float64{0.95, 0.05, 0})
	rows := m.List("docs", "user-")
	if len(rows) != 2 {
		t.Fatalf("list = %d, want 2", len(rows))
	}
}

func TestEmbedMatZeroVecRejected(t *testing.T) {
	m := NewEmbedMatrix()
	if err := m.Set("docs", "a", []float64{0, 0, 0}); err == nil {
		t.Fatal("zero-norm should be rejected")
	}
	if err := m.Set("docs", "a", []float64{}); err == nil {
		t.Fatal("empty vec should be rejected")
	}
}

func TestEmbedMatStatsAdvance(t *testing.T) {
	m := NewEmbedMatrix()
	m.Set("a", "x", []float64{1, 0})
	m.Set("a", "y", []float64{0, 1})
	m.TopK("a", []float64{1, 0}, TopKOpts{K: 1})
	s := m.Stats()
	if s.TotalSets != 2 || s.TotalTopKs != 1 || s.TotalRows != 2 {
		t.Fatalf("stats = %+v", s)
	}
}
