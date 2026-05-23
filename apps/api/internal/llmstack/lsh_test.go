package llmstack

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestLSHCreateAndSet(t *testing.T) {
	l := NewLSHIndex()
	if err := l.Create("docs", 128, 16); err != nil {
		t.Fatal(err)
	}
	vec := make([]float64, 128)
	vec[0] = 1
	if err := l.Set("docs", "alpha", vec); err != nil {
		t.Fatal(err)
	}
	n, _ := l.Len("docs")
	if n != 1 {
		t.Fatalf("len = %d", n)
	}
}

func TestLSHSignIsStable(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 128, 16)
	vec := make([]float64, 128)
	vec[0] = 1
	sig1, _ := l.Sign("docs", vec)
	sig2, _ := l.Sign("docs", vec)
	if sig1 != sig2 {
		t.Fatalf("signature not stable: %d vs %d", sig1, sig2)
	}
}

func TestLSHRejectsBadCreate(t *testing.T) {
	l := NewLSHIndex()
	if err := l.Create("", 128, 16); err == nil {
		t.Fatal("empty bucket_id should fail")
	}
	if err := l.Create("d", 0, 16); err == nil {
		t.Fatal("zero dim should fail")
	}
	if err := l.Create("d", 128, 65); err == nil {
		t.Fatal("bits > 64 should fail")
	}
}

func TestLSHSetDimMismatch(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 128, 16)
	if err := l.Set("docs", "a", []float64{1, 0, 0}); err == nil {
		t.Fatal("dim mismatch should fail")
	}
}

func TestLSHNeighborsFindsSimilar(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 32, 8) // small for stronger bucket overlap
	rng := rand.New(rand.NewSource(1))
	// Insert one anchor vector and several near-duplicates
	anchor := make([]float64, 32)
	for j := range anchor {
		anchor[j] = rng.NormFloat64()
	}
	l.Set("docs", "anchor", anchor)
	for i := 0; i < 20; i++ {
		dup := make([]float64, 32)
		for j := range dup {
			dup[j] = anchor[j] + 0.01*rng.NormFloat64() // tiny perturbation
		}
		l.Set("docs", fmt.Sprintf("dup%d", i), dup)
	}
	// Add unrelated noise
	for i := 0; i < 50; i++ {
		noise := make([]float64, 32)
		for j := range noise {
			noise[j] = rng.NormFloat64()
		}
		l.Set("docs", fmt.Sprintf("noise%d", i), noise)
	}
	hits := l.Neighbors("docs", anchor, NeighborsOpts{K: 10, Radius: 3})
	if len(hits) == 0 {
		t.Fatal("expected at least some neighbors")
	}
	// Top hit should be the anchor itself
	if hits[0].RowID != "anchor" {
		t.Fatalf("top hit = %s, want anchor", hits[0].RowID)
	}
	// Most of the top-K should be near-duplicates
	dupCount := 0
	for _, h := range hits {
		if len(h.RowID) >= 3 && h.RowID[:3] == "dup" {
			dupCount++
		}
	}
	if dupCount < 5 {
		t.Fatalf("only %d/10 near-dups found among neighbors (out of 20 inserted)", dupCount)
	}
}

func TestLSHDel(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 8, 4)
	vec := []float64{1, 0, 0, 0, 0, 0, 0, 0}
	l.Set("docs", "a", vec)
	if !l.Del("docs", "a") {
		t.Fatal("del should return true")
	}
	if l.Del("docs", "a") {
		t.Fatal("del on missing should return false")
	}
}

func TestLSHReplaceUpdatesBucket(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 8, 4)
	l.Set("docs", "a", []float64{1, 0, 0, 0, 0, 0, 0, 0})
	l.Set("docs", "a", []float64{0, 0, 0, 0, 0, 0, 0, 1}) // different sig
	n, _ := l.Len("docs")
	if n != 1 {
		t.Fatalf("len after replace = %d, want 1", n)
	}
}

func TestLSHForget(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 4, 4)
	l.Set("docs", "a", []float64{1, 0, 0, 0})
	l.Set("docs", "b", []float64{0, 1, 0, 0})
	if n := l.Forget("docs"); n != 2 {
		t.Fatalf("forget = %d, want 2", n)
	}
}

func TestLSHStatsAdvance(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 4, 4)
	l.Set("docs", "a", []float64{1, 0, 0, 0})
	l.Neighbors("docs", []float64{1, 0, 0, 0}, NeighborsOpts{K: 1})
	s := l.Stats()
	if s.TotalSets != 1 || s.TotalNeighbors != 1 || s.TotalRows != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if len(s.Buckets) != 1 {
		t.Fatalf("buckets = %d", len(s.Buckets))
	}
}

func TestLSHZeroVecRejected(t *testing.T) {
	l := NewLSHIndex()
	l.Create("docs", 4, 4)
	if err := l.Set("docs", "a", []float64{0, 0, 0, 0}); err == nil {
		t.Fatal("zero-norm should fail")
	}
}
