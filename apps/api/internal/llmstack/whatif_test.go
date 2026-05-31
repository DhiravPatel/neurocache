package llmstack

import (
	"testing"
)

func TestWhatIfObserveAndSimulate(t *testing.T) {
	w := NewWhatIfSimulator()
	w.Observe("gpt-4o", 0.9, 0.01, 400)
	w.Observe("gpt-4o", 0.8, 0.02, 500)
	p, ok := w.Simulate("gpt-4o", 1)
	if !ok {
		t.Fatal("simulate missing")
	}
	if p.ProjectedQuality < 0.84 || p.ProjectedQuality > 0.86 {
		t.Fatalf("quality = %f", p.ProjectedQuality)
	}
	if p.ProjectedCostUSD < 0.014 || p.ProjectedCostUSD > 0.016 {
		t.Fatalf("cost = %f", p.ProjectedCostUSD)
	}
}

func TestWhatIfRepeatsScalesCost(t *testing.T) {
	w := NewWhatIfSimulator()
	w.Observe("r", 0.5, 0.01, 100)
	p, _ := w.Simulate("r", 100)
	if p.ProjectedCostUSD < 0.999 || p.ProjectedCostUSD > 1.001 {
		t.Fatalf("100x cost = %f", p.ProjectedCostUSD)
	}
}

func TestWhatIfConfidenceIncreasesWithSamples(t *testing.T) {
	w := NewWhatIfSimulator()
	for i := 0; i < 5; i++ {
		w.Observe("r", 0.5, 0.01, 100)
	}
	low, _ := w.Simulate("r", 0)
	for i := 0; i < 20; i++ {
		w.Observe("r", 0.5, 0.01, 100)
	}
	med, _ := w.Simulate("r", 0)
	for i := 0; i < 100; i++ {
		w.Observe("r", 0.5, 0.01, 100)
	}
	high, _ := w.Simulate("r", 0)
	if low.Confidence != "LOW" || med.Confidence != "MEDIUM" || high.Confidence != "HIGH" {
		t.Fatalf("conf low=%s med=%s high=%s", low.Confidence, med.Confidence, high.Confidence)
	}
}

func TestWhatIfP99ReflectsTail(t *testing.T) {
	w := NewWhatIfSimulator()
	for i := 0; i < 99; i++ {
		w.Observe("r", 0.5, 0.01, 100)
	}
	w.Observe("r", 0.5, 0.01, 10000) // tail
	p, _ := w.Simulate("r", 0)
	if p.ProjectedP99MS < 5000 {
		t.Fatalf("p99 should reflect tail: %f", p.ProjectedP99MS)
	}
}

func TestWhatIfCompareDominant(t *testing.T) {
	w := NewWhatIfSimulator()
	for i := 0; i < 10; i++ {
		w.Observe("good", 0.9, 0.01, 100)
		w.Observe("bad", 0.5, 0.10, 500)
	}
	c, err := w.Compare("good", "bad")
	if err != nil {
		t.Fatal(err)
	}
	if c.QualityWinner != "good" || c.CostWinner != "good" || c.LatencyWinner != "good" {
		t.Fatalf("not dominant: %+v", c)
	}
}

func TestWhatIfCompareTradeoff(t *testing.T) {
	w := NewWhatIfSimulator()
	w.Observe("fast-cheap", 0.5, 0.01, 100)
	w.Observe("slow-good", 0.9, 0.10, 500)
	c, _ := w.Compare("fast-cheap", "slow-good")
	if c.QualityWinner != "slow-good" || c.CostWinner != "fast-cheap" {
		t.Fatalf("tradeoff: %+v", c)
	}
}

func TestWhatIfCompareUnknown(t *testing.T) {
	w := NewWhatIfSimulator()
	if _, err := w.Compare("nope", "also-nope"); err == nil {
		t.Fatal("unknown should fail")
	}
}

func TestWhatIfRoutes(t *testing.T) {
	w := NewWhatIfSimulator()
	w.Observe("z", 0.5, 0, 0)
	w.Observe("a", 0.5, 0, 0)
	r := w.Routes()
	if r[0] != "a" {
		t.Fatalf("routes = %v", r)
	}
}

func TestWhatIfForget(t *testing.T) {
	w := NewWhatIfSimulator()
	w.Observe("a", 0.5, 0, 0)
	w.Observe("b", 0.5, 0, 0)
	if w.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if w.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestWhatIfStats(t *testing.T) {
	w := NewWhatIfSimulator()
	w.Observe("r", 0.5, 0, 0)
	w.Simulate("r", 0)
	s := w.Stats()
	if s.TotalObservations != 1 || s.TotalSimulations != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestWhatIfRejectsBadInput(t *testing.T) {
	w := NewWhatIfSimulator()
	if err := w.Observe("", 0.5, 0, 0); err == nil {
		t.Fatal("empty route should fail")
	}
	if err := w.Observe("r", 1.5, 0, 0); err == nil {
		t.Fatal("quality > 1 should fail")
	}
	if err := w.Observe("r", -0.1, 0, 0); err == nil {
		t.Fatal("quality < 0 should fail")
	}
	if err := w.Observe("r", 0.5, -1, 0); err == nil {
		t.Fatal("negative cost should fail")
	}
}

func TestWhatIfEmptySimulate(t *testing.T) {
	w := NewWhatIfSimulator()
	if _, ok := w.Simulate("nope", 0); ok {
		t.Fatal("unknown route should miss")
	}
}
