package llmstack

import (
	"testing"
)

func TestTuneKnobAndObjective(t *testing.T) {
	tn := NewTuneRegistry()
	if err := tn.Knob("c", "SEMANTIC_THRESHOLD", 0.7, 0.92, 10); err != nil {
		t.Fatal(err)
	}
	if err := tn.Objective("c", "max", "hit_rate"); err != nil {
		t.Fatal(err)
	}
}

func TestTuneRejectsBadInput(t *testing.T) {
	tn := NewTuneRegistry()
	if err := tn.Knob("", "k", 0, 1, 10); err == nil {
		t.Fatal("empty tune id should fail")
	}
	if err := tn.Knob("c", "", 0, 1, 10); err == nil {
		t.Fatal("empty knob should fail")
	}
	if err := tn.Knob("c", "k", 1, 0, 10); err == nil {
		t.Fatal("low >= high should fail")
	}
	if err := tn.Knob("c", "k", 0, 1, 1000); err == nil {
		t.Fatal("buckets > 256 should fail")
	}
	tn.Knob("c", "k", 0, 1, 10)
	if err := tn.Objective("c", "bogus", "x"); err == nil {
		t.Fatal("unknown direction should fail")
	}
	if err := tn.Objective("c", "max", ""); err == nil {
		t.Fatal("empty expr should fail")
	}
}

func TestTuneSuggestStays(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 10)
	tn.Objective("c", "max", "x")
	v, ok := tn.Suggest("c")
	if !ok {
		t.Fatal("suggest")
	}
	if v < 0 || v > 1 {
		t.Fatalf("suggest out of range: %f", v)
	}
}

func TestTuneObserveUpdatesPosterior(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 10, 5)
	tn.Objective("c", "max", "x")
	// Reward higher x → should drive winning bucket to high end
	for i := 0; i < 200; i++ {
		val := float64(i % 10)
		// Reward = the value itself
		tn.Observe("c", val, map[string]float64{"x": val})
	}
	apply, _ := tn.Apply("c")
	// Best bucket should be in the upper half (>= 5.0)
	if apply.BestValue < 5.0 {
		t.Fatalf("best should be high: %+v", apply)
	}
}

func TestTuneObjectiveMinDirection(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 10, 5)
	tn.Objective("c", "min", "x")
	for i := 0; i < 200; i++ {
		val := float64(i % 10)
		tn.Observe("c", val, map[string]float64{"x": val})
	}
	apply, _ := tn.Apply("c")
	if apply.BestValue > 5.0 {
		t.Fatalf("min direction best should be low: %+v", apply)
	}
}

func TestTuneObjectiveExpression(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 10)
	tn.Objective("c", "max", "hit_rate - 0.3*stale_rate")
	if _, err := tn.Observe("c", 0.5, map[string]float64{"hit_rate": 0.9, "stale_rate": 0.2}); err != nil {
		t.Fatal(err)
	}
}

func TestTuneObserveRejectsMissingMetric(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 10)
	tn.Objective("c", "max", "x + y")
	if _, err := tn.Observe("c", 0.5, map[string]float64{"x": 1.0}); err == nil {
		t.Fatal("missing metric y should fail")
	}
}

func TestTuneObserveWithoutObjectiveFails(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 10)
	if _, err := tn.Observe("c", 0.5, map[string]float64{}); err == nil {
		t.Fatal("observe without objective should fail")
	}
}

func TestTuneStatusReportsBuckets(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 5)
	tn.Objective("c", "max", "x")
	tn.Observe("c", 0.1, map[string]float64{"x": 0.5})
	st, _ := tn.Status("c")
	if len(st.Buckets) != 5 {
		t.Fatalf("buckets = %d", len(st.Buckets))
	}
}

func TestTuneApplyConfidenceTiers(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 10)
	tn.Objective("c", "max", "x")
	for i := 0; i < 5; i++ {
		tn.Observe("c", 0.5, map[string]float64{"x": 0.5})
	}
	a, _ := tn.Apply("c")
	if a.Confidence != "LOW" {
		t.Fatalf("low confidence: %s", a.Confidence)
	}
	for i := 0; i < 20; i++ {
		tn.Observe("c", 0.5, map[string]float64{"x": 0.5})
	}
	a, _ = tn.Apply("c")
	if a.Confidence != "MEDIUM" {
		t.Fatalf("medium confidence: %s", a.Confidence)
	}
	for i := 0; i < 100; i++ {
		tn.Observe("c", 0.5, map[string]float64{"x": 0.5})
	}
	a, _ = tn.Apply("c")
	if a.Confidence != "HIGH" {
		t.Fatalf("high confidence: %s", a.Confidence)
	}
}

func TestTuneHistory(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 5)
	tn.Objective("c", "max", "x")
	for i := 0; i < 10; i++ {
		tn.Observe("c", 0.5, map[string]float64{"x": float64(i)})
	}
	h, _ := tn.History("c", 3)
	if len(h) != 3 {
		t.Fatalf("history = %d", len(h))
	}
}

func TestTuneForget(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("a", "k", 0, 1, 5)
	tn.Knob("b", "k", 0, 1, 5)
	if tn.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if tn.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestTuneList(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("zeta", "k", 0, 1, 5)
	tn.Knob("alpha", "k", 0, 1, 5)
	l := tn.List()
	if l[0] != "alpha" {
		t.Fatalf("list = %v", l)
	}
}

func TestTuneStats(t *testing.T) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 5)
	tn.Objective("c", "max", "x")
	tn.Suggest("c")
	tn.Observe("c", 0.5, map[string]float64{"x": 0.5})
	s := tn.Stats()
	if s.TotalSuggests != 1 || s.TotalObserves != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestTuneExprParserVariety(t *testing.T) {
	for expr, want := range map[string]float64{
		"1 + 2 * 3":   7,
		"(1 + 2) * 3": 9,
		"10 / 2 - 1":  4,
		"-5 + 10":     5,
		"a + b":       3, // with vars
	} {
		v, err := evalExpr(expr, map[string]float64{"a": 1, "b": 2})
		if err != nil {
			t.Fatalf("expr %s: %v", expr, err)
		}
		if v != want {
			t.Fatalf("expr %s: got %f, want %f", expr, v, want)
		}
	}
}

func TestTuneExprDivByZero(t *testing.T) {
	if _, err := evalExpr("1 / 0", nil); err == nil {
		t.Fatal("div by zero should error")
	}
}

func TestTuneExprParsesMalformed(t *testing.T) {
	if _, err := parseExpr("(1 + 2"); err == nil {
		t.Fatal("unclosed paren should fail")
	}
	if _, err := parseExpr("1 + + 2"); err == nil {
		t.Fatal("double op should fail (well, + is unary too — accept this edge)")
	}
}

func BenchmarkTuneSuggest(b *testing.B) {
	tn := NewTuneRegistry()
	tn.Knob("c", "k", 0, 1, 10)
	tn.Objective("c", "max", "x")
	for i := 0; i < 100; i++ {
		tn.Observe("c", 0.5, map[string]float64{"x": 0.5})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tn.Suggest("c")
	}
}
