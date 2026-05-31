package llmstack

import (
	"math"
	"math/rand"
	"testing"
)

func TestVecSpaceHealthy(t *testing.T) {
	v := NewVecSpaceHealth()
	// Random uniform vectors — should be HEALTHY
	r := rand.New(rand.NewSource(42))
	const dim = 64
	vecs := make([][]float64, 50)
	for i := range vecs {
		x := make([]float64, dim)
		for j := range x {
			x[j] = r.NormFloat64()
		}
		vecs[i] = x
	}
	v.Sample("idx", vecs)
	rep, _ := v.Health("idx", 0, 0)
	if rep.Verdict != "HEALTHY" {
		t.Fatalf("random vectors should be HEALTHY: %+v", rep)
	}
}

func TestVecSpaceCollapsed(t *testing.T) {
	v := NewVecSpaceHealth()
	const dim = 64
	// All vectors equal → mean pairwise cosine = 1.0
	vecs := make([][]float64, 50)
	base := make([]float64, dim)
	for j := range base {
		base[j] = 1.0
	}
	for i := range vecs {
		x := make([]float64, dim)
		copy(x, base)
		vecs[i] = x
	}
	v.Sample("idx", vecs)
	rep, _ := v.Health("idx", 0, 0)
	if rep.Verdict != "COLLAPSED" {
		t.Fatalf("identical vectors should be COLLAPSED: %+v", rep)
	}
	if rep.MeanPairwiseCosine < 0.99 {
		t.Fatalf("mean cosine = %f", rep.MeanPairwiseCosine)
	}
}

func TestVecSpaceNaNCollapsed(t *testing.T) {
	v := NewVecSpaceHealth()
	const dim = 32
	vecs := make([][]float64, 20)
	for i := range vecs {
		x := make([]float64, dim)
		for j := range x {
			x[j] = math.NaN()
		}
		vecs[i] = x
	}
	v.Sample("idx", vecs)
	rep, _ := v.Health("idx", 0, 0)
	if rep.Verdict != "COLLAPSED" {
		t.Fatalf("NaN vectors should be COLLAPSED: %+v", rep)
	}
}

func TestVecSpaceInsufficientSample(t *testing.T) {
	v := NewVecSpaceHealth()
	v.Sample("idx", [][]float64{{1, 2, 3}})
	rep, _ := v.Health("idx", 0, 0)
	if rep.Verdict != "INSUFFICIENT" {
		t.Fatalf("single sample = %s", rep.Verdict)
	}
}

func TestVecSpaceDimMismatchRejected(t *testing.T) {
	v := NewVecSpaceHealth()
	v.Sample("idx", [][]float64{{1, 2}})
	if err := v.Sample("idx", [][]float64{{1, 2, 3}}); err == nil {
		t.Fatal("dim mismatch should fail")
	}
}

func TestVecSpaceRollingWindow(t *testing.T) {
	v := NewVecSpaceHealth()
	const dim = 8
	r := rand.New(rand.NewSource(1))
	// Pump more than vsMaxBuf vectors
	for batch := 0; batch < 10; batch++ {
		vecs := make([][]float64, 200)
		for i := range vecs {
			x := make([]float64, dim)
			for j := range x {
				x[j] = r.NormFloat64()
			}
			vecs[i] = x
		}
		v.Sample("idx", vecs)
	}
	rep, _ := v.Health("idx", 0, 0)
	if rep.SampleN > vsMaxBuf {
		t.Fatalf("rolling window not enforced: %d > %d", rep.SampleN, vsMaxBuf)
	}
}

func TestVecSpaceLowEffectiveDim(t *testing.T) {
	v := NewVecSpaceHealth()
	const dim = 64
	r := rand.New(rand.NewSource(2))
	// Vectors that vary along one axis only — effective dim ≈ 1
	vecs := make([][]float64, 50)
	for i := range vecs {
		x := make([]float64, dim)
		x[0] = r.NormFloat64()
		vecs[i] = x
	}
	v.Sample("idx", vecs)
	rep, _ := v.Health("idx", 0, 0)
	if rep.EffectiveDim > 4 {
		t.Fatalf("effective dim should be tiny: %f", rep.EffectiveDim)
	}
	if rep.Verdict != "COLLAPSED" {
		t.Fatalf("low-rank space should be COLLAPSED: %+v", rep)
	}
}

func TestVecSpaceReset(t *testing.T) {
	v := NewVecSpaceHealth()
	v.Sample("a", [][]float64{{1, 0}})
	v.Sample("b", [][]float64{{1, 0}})
	if v.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
	if v.Reset("ALL") != 1 {
		t.Fatal("ALL should drop 1 remaining")
	}
}

func TestVecSpaceList(t *testing.T) {
	v := NewVecSpaceHealth()
	v.Sample("zeta", [][]float64{{1}})
	v.Sample("alpha", [][]float64{{1}})
	l := v.List()
	if l[0] != "alpha" {
		t.Fatalf("list = %v", l)
	}
}

func TestVecSpaceStats(t *testing.T) {
	v := NewVecSpaceHealth()
	v.Sample("a", [][]float64{{1}, {2}})
	v.Health("a", 0, 0)
	s := v.Stats()
	if s.Spaces != 1 || s.TotalSamples != 2 || s.TotalHealths != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestVecSpaceRejectsBadInput(t *testing.T) {
	v := NewVecSpaceHealth()
	if err := v.Sample("", [][]float64{{1}}); err == nil {
		t.Fatal("empty space should fail")
	}
	if err := v.Sample("a", nil); err == nil {
		t.Fatal("no vectors should fail")
	}
	if _, ok := v.Health("nope", 0, 0); ok {
		t.Fatal("unknown space should be missing")
	}
}

func BenchmarkVecSpaceHealth(b *testing.B) {
	v := NewVecSpaceHealth()
	const dim = 128
	r := rand.New(rand.NewSource(7))
	vecs := make([][]float64, 200)
	for i := range vecs {
		x := make([]float64, dim)
		for j := range x {
			x[j] = r.NormFloat64()
		}
		vecs[i] = x
	}
	v.Sample("idx", vecs)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		v.Health("idx", 0, 0)
	}
}
