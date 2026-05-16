package llmstack

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestVecQuantSetAndTopK(t *testing.T) {
	v := NewVecQuantMatrix()
	v.Set("d", "alpha", []float64{1, 0, 0})
	v.Set("d", "beta", []float64{0, 1, 0})
	v.Set("d", "gamma", []float64{0.9, 0.1, 0})
	hits := v.TopK("d", []float64{1, 0, 0}, TopKOpts{K: 2})
	if len(hits) != 2 {
		t.Fatalf("len = %d, want 2", len(hits))
	}
	if hits[0].RowID != "alpha" {
		t.Fatalf("top hit = %s, want alpha", hits[0].RowID)
	}
}

func TestVecQuantCosineApproxFloat(t *testing.T) {
	// Compare quantized cosine to float64 cosine — should be within ~1%
	v := NewVecQuantMatrix()
	a := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	b := []float64{2, 1, 4, 3, 6, 5, 8, 7}
	v.Set("d", "a", a)
	v.Set("d", "b", b)
	quant, _ := v.Cosine("d", "a", "b")
	// Float reference
	dot := dotProduct(a, b)
	na := math.Sqrt(dotProduct(a, a))
	nb := math.Sqrt(dotProduct(b, b))
	floatCos := dot / (na * nb)
	if math.Abs(quant-floatCos) > 0.02 {
		t.Fatalf("quantized cosine drift: quant=%f float=%f", quant, floatCos)
	}
}

func TestVecQuantDimMismatch(t *testing.T) {
	v := NewVecQuantMatrix()
	v.Set("d", "a", []float64{1, 0, 0})
	if err := v.Set("d", "b", []float64{1, 0}); err == nil {
		t.Fatal("dim mismatch should fail")
	}
}

func TestVecQuantZeroVecRejected(t *testing.T) {
	v := NewVecQuantMatrix()
	if err := v.Set("d", "a", []float64{0, 0, 0}); err == nil {
		t.Fatal("zero vec should fail")
	}
}

func TestVecQuantDelAndLen(t *testing.T) {
	v := NewVecQuantMatrix()
	v.Set("d", "a", []float64{1, 0, 0})
	v.Set("d", "b", []float64{0, 1, 0})
	n, _ := v.Len("d")
	if n != 2 {
		t.Fatalf("len = %d", n)
	}
	if !v.Del("d", "a") {
		t.Fatal("del should return true")
	}
	n, _ = v.Len("d")
	if n != 1 {
		t.Fatalf("len after del = %d", n)
	}
}

func TestVecQuantForgetReturnsRows(t *testing.T) {
	v := NewVecQuantMatrix()
	v.Set("d", "a", []float64{1, 0, 0})
	v.Set("d", "b", []float64{0, 1, 0})
	if n := v.Forget("d"); n != 2 {
		t.Fatalf("forget = %d, want 2", n)
	}
}

func TestVecQuantRecallVsFloat(t *testing.T) {
	q := NewVecQuantMatrix()
	f := NewEmbedMatrix()
	dim := 256
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 500; i++ {
		vec := make([]float64, dim)
		for j := range vec {
			vec[j] = rng.NormFloat64()
		}
		id := fmt.Sprintf("d%d", i)
		q.Set("bench", id, vec)
		f.Set("bench", id, vec)
	}
	query := make([]float64, dim)
	for j := range query {
		query[j] = rng.NormFloat64()
	}
	quantHits := q.TopK("bench", query, TopKOpts{K: 10})
	floatHits := f.TopK("bench", query, TopKOpts{K: 10})
	quantSet := map[string]bool{}
	for _, h := range quantHits {
		quantSet[h.RowID] = true
	}
	overlap := 0
	for _, h := range floatHits {
		if quantSet[h.RowID] {
			overlap++
		}
	}
	// Quantized recall typically 9-10/10
	if overlap < 8 {
		t.Fatalf("quant recall too low: only %d/10 overlap with float", overlap)
	}
}

func TestVecQuantStatsAdvance(t *testing.T) {
	v := NewVecQuantMatrix()
	v.Set("d", "a", []float64{1, 0})
	v.Set("d", "b", []float64{0, 1})
	v.TopK("d", []float64{1, 0}, TopKOpts{K: 1})
	s := v.Stats()
	if s.TotalSets != 2 || s.TotalTopKs != 1 || s.TotalRows != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if s.BytesPerRowSample != 2+16 {
		t.Fatalf("bytes_per_row = %d, want %d", s.BytesPerRowSample, 2+16)
	}
}
