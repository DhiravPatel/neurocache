package llmstack

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestMatryoshkaSetAndTopK(t *testing.T) {
	m := NewMatryoshkaMatrix()
	dim := 256
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 100; i++ {
		vec := make([]float64, dim)
		for j := range vec {
			vec[j] = rng.NormFloat64()
		}
		m.Set("docs", fmt.Sprintf("d%d", i), vec)
	}
	// Build a query that's identical to doc 42
	query := make([]float64, dim)
	rng2 := rand.New(rand.NewSource(1))
	for i := 0; i < 43; i++ {
		for j := range query {
			query[j] = rng2.NormFloat64()
		}
	}
	hits := m.TopK("docs", query, MatryoshkaOpts{K: 5})
	if len(hits) != 5 {
		t.Fatalf("len = %d, want 5", len(hits))
	}
	if hits[0].RowID != "d42" {
		t.Fatalf("top hit = %s, want d42", hits[0].RowID)
	}
}

func TestMatryoshkaRejectsBelow256(t *testing.T) {
	m := NewMatryoshkaMatrix()
	if err := m.Set("d", "r", []float64{1, 2, 3}); err == nil {
		t.Fatal("dim < 256 should fail")
	}
	short := make([]float64, 128)
	short[0] = 1
	if err := m.Set("d", "r", short); err == nil {
		t.Fatal("dim=128 should fail (matryoshka needs >=256)")
	}
}

func TestMatryoshkaDimMismatch(t *testing.T) {
	m := NewMatryoshkaMatrix()
	a := make([]float64, 256)
	a[0] = 1
	m.Set("d", "a", a)
	b := make([]float64, 512)
	b[0] = 1
	if err := m.Set("d", "b", b); err == nil {
		t.Fatal("dim mismatch (256 vs 512) should fail")
	}
}

func TestMatryoshkaDelAndLen(t *testing.T) {
	m := NewMatryoshkaMatrix()
	v := make([]float64, 256)
	v[0] = 1
	m.Set("d", "a", v)
	m.Set("d", "b", v)
	n, _ := m.Len("d")
	if n != 2 {
		t.Fatalf("len = %d, want 2", n)
	}
	if !m.Del("d", "a") {
		t.Fatal("del should return true")
	}
	n, _ = m.Len("d")
	if n != 1 {
		t.Fatalf("len after del = %d, want 1", n)
	}
}

func TestMatryoshkaForget(t *testing.T) {
	m := NewMatryoshkaMatrix()
	v := make([]float64, 256)
	v[0] = 1
	m.Set("d", "a", v)
	m.Set("d", "b", v)
	if n := m.Forget("d"); n != 2 {
		t.Fatalf("forget = %d, want 2", n)
	}
}

func TestMatryoshkaRecallVsFlat(t *testing.T) {
	// Build the same matrix in MatryoshkaMatrix and EmbedMatrix;
	// verify the top-10 results overlap significantly.
	m := NewMatryoshkaMatrix()
	e := NewEmbedMatrix()
	dim := 256
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		vec := make([]float64, dim)
		for j := range vec {
			vec[j] = rng.NormFloat64()
		}
		id := fmt.Sprintf("d%d", i)
		m.Set("bench", id, vec)
		e.Set("bench", id, vec)
	}
	query := make([]float64, dim)
	for j := range query {
		query[j] = rng.NormFloat64()
	}
	matResults := m.TopK("bench", query, MatryoshkaOpts{K: 10})
	embResults := e.TopK("bench", query, TopKOpts{K: 10})
	matSet := map[string]bool{}
	for _, h := range matResults {
		matSet[h.RowID] = true
	}
	overlap := 0
	for _, h := range embResults {
		if matSet[h.RowID] {
			overlap++
		}
	}
	// Expect >=7/10 overlap with random vectors (no real structure)
	if overlap < 7 {
		t.Fatalf("matryoshka recall too low: only %d/10 overlap with flat", overlap)
	}
}

func TestMatryoshkaStatsAdvance(t *testing.T) {
	m := NewMatryoshkaMatrix()
	v := make([]float64, 256)
	v[0] = 1
	m.Set("d", "a", v)
	m.TopK("d", v, MatryoshkaOpts{K: 1})
	s := m.Stats()
	if s.TotalSets != 1 || s.TotalTopKs != 1 || s.TotalRows != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
