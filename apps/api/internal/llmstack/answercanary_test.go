package llmstack

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestAnswerCanaryConfigCreates(t *testing.T) {
	a := NewAnswerCanary()
	if err := a.Configure("exp1", "gpt-4", "gpt-4o", 0.2); err != nil {
		t.Fatal(err)
	}
	rep, _ := a.Report("exp1")
	if rep.Baseline.Name != "gpt-4" || rep.Canary.Name != "gpt-4o" {
		t.Fatalf("variants = %+v / %+v", rep.Baseline, rep.Canary)
	}
	if rep.Rate != 0.2 {
		t.Fatalf("rate = %f", rep.Rate)
	}
}

func TestAnswerCanaryRouteDeterministic(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	r1, _ := a.Route("exp1", "req-42")
	r2, _ := a.Route("exp1", "req-42")
	if r1 != r2 {
		t.Fatalf("non-deterministic routing: %s vs %s", r1, r2)
	}
}

func TestAnswerCanaryRouteRespectRate(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.10) // ~10% canary
	canaryHits := 0
	const n = 5000
	for i := 0; i < n; i++ {
		v, _ := a.Route("exp1", fmt.Sprintf("req-%d", i))
		if v == "canary" {
			canaryHits++
		}
	}
	frac := float64(canaryHits) / float64(n)
	// Expect ~10% ± 2% with 5000 samples
	if frac < 0.07 || frac > 0.13 {
		t.Fatalf("canary fraction = %f, want ~0.10", frac)
	}
}

func TestAnswerCanaryRecordAndReport(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	for i := 0; i < 50; i++ {
		a.Record("exp1", "baseline", 0.7, 100)
		a.Record("exp1", "canary", 0.85, 120)
	}
	rep, err := a.Report("exp1")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Baseline.N != 50 || rep.Canary.N != 50 {
		t.Fatalf("N = %d / %d", rep.Baseline.N, rep.Canary.N)
	}
	if abs(rep.Baseline.MeanQuality-0.7) > 1e-9 {
		t.Fatalf("baseline mean = %f", rep.Baseline.MeanQuality)
	}
	if rep.QualityLift < 0.15 {
		t.Fatalf("lift should be ~21%%, got %f", rep.QualityLift)
	}
}

func TestAnswerCanaryDecideInsufficientData(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	for i := 0; i < 5; i++ {
		a.Record("exp1", "baseline", 0.7, 100)
		a.Record("exp1", "canary", 0.9, 110)
	}
	d, _ := a.Decide("exp1")
	if d.Decision != "insufficient_data" {
		t.Fatalf("decision = %s", d.Decision)
	}
}

func TestAnswerCanaryDecideShip(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 200; i++ {
		// Baseline mean 0.6 ± 0.05
		a.Record("exp1", "baseline", clamp01(0.6+rng.NormFloat64()*0.05), 100)
		// Canary mean 0.8 ± 0.05 — clearly better
		a.Record("exp1", "canary", clamp01(0.8+rng.NormFloat64()*0.05), 110)
	}
	d, _ := a.Decide("exp1")
	if d.Decision != "ship" {
		t.Fatalf("decision = %s (z=%f, lift=%f)", d.Decision, d.Z, d.QualityLift)
	}
}

func TestAnswerCanaryDecideRollback(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		a.Record("exp1", "baseline", clamp01(0.8+rng.NormFloat64()*0.05), 100)
		a.Record("exp1", "canary", clamp01(0.5+rng.NormFloat64()*0.05), 110) // worse
	}
	d, _ := a.Decide("exp1")
	if d.Decision != "rollback" {
		t.Fatalf("decision = %s (z=%f)", d.Decision, d.Z)
	}
}

func TestAnswerCanaryDecideHold(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 200; i++ {
		a.Record("exp1", "baseline", clamp01(0.7+rng.NormFloat64()*0.1), 100)
		a.Record("exp1", "canary", clamp01(0.71+rng.NormFloat64()*0.1), 100) // ~equal
	}
	d, _ := a.Decide("exp1")
	if d.Decision != "hold" {
		t.Fatalf("decision = %s (z=%f)", d.Decision, d.Z)
	}
}

func TestAnswerCanaryReset(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("exp1", "b", "c", 0.5)
	a.Record("exp1", "baseline", 0.5, 100)
	a.Reset("exp1")
	rep, _ := a.Report("exp1")
	if rep.Baseline.N != 0 {
		t.Fatal("reset did not zero baseline N")
	}
	if rep.Baseline.Name != "b" {
		t.Fatalf("reset lost name: %s", rep.Baseline.Name)
	}
}

func TestAnswerCanaryListSorted(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("z", "b", "c", 0.1)
	a.Configure("a", "b", "c", 0.1)
	a.Configure("m", "b", "c", 0.1)
	l := a.List()
	if len(l) != 3 || l[0] != "a" || l[2] != "z" {
		t.Fatalf("list = %v", l)
	}
}

func TestAnswerCanaryRejectsBadInput(t *testing.T) {
	a := NewAnswerCanary()
	if err := a.Configure("", "b", "c", 0.5); err == nil {
		t.Fatal("empty exp id should fail")
	}
	if err := a.Configure("a", "b", "c", 1.5); err == nil {
		t.Fatal("rate > 1 should fail")
	}
	a.Configure("a", "b", "c", 0.1)
	if err := a.Record("a", "ghost", 0.5, 100); err == nil {
		t.Fatal("unknown variant should fail")
	}
	if err := a.Record("a", "baseline", 1.5, 100); err == nil {
		t.Fatal("quality > 1 should fail")
	}
}

func TestAnswerCanaryAutoCreateOnRoute(t *testing.T) {
	a := NewAnswerCanary()
	// No CONFIG — ROUTE should auto-create with defaults
	v, err := a.Route("adhoc", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if v != "baseline" && v != "canary" {
		t.Fatalf("variant = %s", v)
	}
	if len(a.List()) != 1 {
		t.Fatal("auto-create did not register exp")
	}
}

func TestAnswerCanaryStatsAdvance(t *testing.T) {
	a := NewAnswerCanary()
	a.Configure("e", "b", "c", 0.5)
	a.Route("e", "x")
	a.Record("e", "baseline", 0.5, 50)
	s := a.Stats()
	if s.Experiments != 1 || s.TotalRoutes != 1 || s.TotalRecords != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func BenchmarkAnswerCanaryRoute(b *testing.B) {
	a := NewAnswerCanary()
	a.Configure("e", "b", "c", 0.1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Route("e", "req-many")
	}
}

func BenchmarkAnswerCanaryRecord(b *testing.B) {
	a := NewAnswerCanary()
	a.Configure("e", "b", "c", 0.5)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Record("e", "baseline", 0.75, 120)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
