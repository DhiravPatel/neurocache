package llmstack

import (
	"math"
	"testing"
)

func TestEmbedPoolMean(t *testing.T) {
	p := NewEmbedPooler()
	out, err := p.Mean([][]float64{
		{1, 2, 3},
		{3, 4, 5},
		{5, 6, 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{3, 4, 5}
	for i, v := range out {
		if math.Abs(v-want[i]) > 1e-9 {
			t.Fatalf("out[%d] = %f, want %f", i, v, want[i])
		}
	}
}

func TestEmbedPoolMax(t *testing.T) {
	p := NewEmbedPooler()
	out, err := p.Max([][]float64{
		{1, 5, 3},
		{4, 2, 6},
		{0, 4, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{4, 5, 6}
	for i, v := range out {
		if v != want[i] {
			t.Fatalf("out[%d] = %f, want %f", i, v, want[i])
		}
	}
}

func TestEmbedPoolWeighted(t *testing.T) {
	p := NewEmbedPooler()
	out, err := p.Weighted([]float64{1, 2, 1}, [][]float64{
		{1, 1},
		{2, 2},
		{3, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	// (1*1 + 2*2 + 1*3) / 4 = 8/4 = 2
	if math.Abs(out[0]-2.0) > 1e-9 {
		t.Fatalf("out[0] = %f, want 2.0", out[0])
	}
}

func TestEmbedPoolNormSum(t *testing.T) {
	p := NewEmbedPooler()
	out, err := p.NormSum([][]float64{
		{1, 0},
		{1, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sum = [2, 0], normalised = [1, 0]
	if math.Abs(out[0]-1.0) > 1e-9 || math.Abs(out[1]) > 1e-9 {
		t.Fatalf("normsum = %v, want [1, 0]", out)
	}
}

func TestEmbedPoolRejectsEmpty(t *testing.T) {
	p := NewEmbedPooler()
	if _, err := p.Mean(nil); err == nil {
		t.Fatal("nil vecs should fail")
	}
	if _, err := p.Mean([][]float64{{}}); err == nil {
		t.Fatal("empty vec should fail")
	}
}

func TestEmbedPoolRejectsDimMismatch(t *testing.T) {
	p := NewEmbedPooler()
	if _, err := p.Mean([][]float64{{1, 2}, {1, 2, 3}}); err == nil {
		t.Fatal("dim mismatch should fail")
	}
}

func TestEmbedPoolWeightedMismatch(t *testing.T) {
	p := NewEmbedPooler()
	if _, err := p.Weighted([]float64{1, 2}, [][]float64{{1}, {1}, {1}}); err == nil {
		t.Fatal("weight/vec count mismatch should fail")
	}
}

func TestEmbedPoolWeightedZeroWeights(t *testing.T) {
	p := NewEmbedPooler()
	if _, err := p.Weighted([]float64{0, 0}, [][]float64{{1}, {1}}); err == nil {
		t.Fatal("zero-sum weights should fail")
	}
}

func TestEmbedPoolStatsAdvance(t *testing.T) {
	p := NewEmbedPooler()
	p.Mean([][]float64{{1, 2}, {3, 4}})
	p.Max([][]float64{{1, 2}})
	p.NormSum([][]float64{{1, 0}})
	s := p.Stats()
	if s.TotalMeans != 1 || s.TotalMaxes != 1 || s.TotalNormSum != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.TotalVecsIn != 4 {
		t.Fatalf("total_vecs_in = %d, want 4", s.TotalVecsIn)
	}
}
