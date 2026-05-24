package llmstack

import (
	"testing"
	"time"
)

func TestAdaptLatencyConfigureSortsByCostDesc(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1},
		{Model: "expensive", Cost: 10},
		{Model: "mid", Cost: 5},
	}, 60*time.Second, 5)
	rows, _ := a.Status("p")
	// Should be in cost-desc order
	if rows[0].Model != "expensive" || rows[2].Model != "cheap" {
		t.Fatalf("ordering wrong: %+v", rows)
	}
}

func TestAdaptLatencyObserveRequiresPolicy(t *testing.T) {
	a := NewAdaptLatency()
	// Observe auto-creates the policy (no upfront CONFIG required)
	if err := a.Observe("p", "gpt-4", 100); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptLatencyPickRequiresConfiguredTargets(t *testing.T) {
	a := NewAdaptLatency()
	a.Observe("p", "gpt-4", 100)
	if _, err := a.Pick("p", 500); err == nil {
		t.Fatal("pick without TARGETS should fail")
	}
}

func TestAdaptLatencyPickReturnsMostExpensiveUnderTarget(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1},
		{Model: "expensive", Cost: 10},
	}, 60*time.Second, 3)
	// Both models well within SLO
	for i := 0; i < 5; i++ {
		a.Observe("p", "cheap", 50)
		a.Observe("p", "expensive", 200)
	}
	d, _ := a.Pick("p", 1000)
	if d.Model != "expensive" {
		t.Fatalf("expected expensive under target, got %+v", d)
	}
	if d.Demoted {
		t.Fatal("non-demoted pick shouldn't flag demoted")
	}
}

func TestAdaptLatencyPickDemotesWhenExpensiveBreachesSLO(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1},
		{Model: "expensive", Cost: 10},
	}, 60*time.Second, 3)
	// Expensive breaches the 500ms target; cheap is fine
	for i := 0; i < 5; i++ {
		a.Observe("p", "cheap", 50)
		a.Observe("p", "expensive", 900)
	}
	d, _ := a.Pick("p", 500)
	if d.Model != "cheap" {
		t.Fatalf("expected cheap fallback, got %+v", d)
	}
	if !d.Demoted {
		t.Fatal("demoted pick should flag demoted=true")
	}
}

func TestAdaptLatencyPickFallsBackWhenAllBreach(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1},
		{Model: "expensive", Cost: 10},
	}, 60*time.Second, 3)
	for i := 0; i < 5; i++ {
		a.Observe("p", "cheap", 600)
		a.Observe("p", "expensive", 1500)
	}
	d, _ := a.Pick("p", 500)
	// All breach → fall back to cheapest with explicit reason
	if d.Model != "cheap" {
		t.Fatalf("expected cheap fallback: %+v", d)
	}
	if d.Reason == "" || d.Reason == "p99 within target" {
		t.Fatalf("reason should indicate breach fallback: %s", d.Reason)
	}
}

func TestAdaptLatencyPickOptimisticOnInsufficientSamples(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1},
		{Model: "expensive", Cost: 10},
	}, 60*time.Second, 50)
	// Only 2 samples for expensive — below MIN_SAMPLES=50
	a.Observe("p", "expensive", 100)
	a.Observe("p", "expensive", 110)
	d, _ := a.Pick("p", 500)
	if d.Model != "expensive" {
		t.Fatalf("optimistic pick should choose expensive: %+v", d)
	}
	if d.Reason == "" {
		t.Fatal("reason should be set")
	}
}

func TestAdaptLatencyWindowFiltersOldSamples(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{{Model: "x", Cost: 1}},
		1*time.Millisecond, 1)
	// One stale slow sample
	a.Observe("p", "x", 10000)
	time.Sleep(5 * time.Millisecond)
	// Three recent fast samples
	a.Observe("p", "x", 50)
	a.Observe("p", "x", 60)
	a.Observe("p", "x", 55)
	d, _ := a.Pick("p", 500)
	// Stale 10000ms shouldn't count; p99 of {50,55,60} is ~60 < 500
	if d.Model != "x" || d.P99MS > 100 {
		t.Fatalf("window filter broken: %+v", d)
	}
}

func TestAdaptLatencyStatusReportsPercentiles(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{{Model: "x", Cost: 1}},
		60*time.Second, 5)
	for i := int64(100); i <= 200; i += 10 {
		a.Observe("p", "x", i)
	}
	rows, _ := a.Status("p")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].P50 < 140 || rows[0].P50 > 160 {
		t.Fatalf("p50 = %f", rows[0].P50)
	}
	if rows[0].P99 < 190 {
		t.Fatalf("p99 = %f", rows[0].P99)
	}
}

func TestAdaptLatencyListAndReset(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("zeta", []AdaptLatencyTarget{{Model: "x", Cost: 1}}, 60*time.Second, 1)
	a.ConfigurePublic("alpha", []AdaptLatencyTarget{{Model: "x", Cost: 1}}, 60*time.Second, 1)
	l := a.List()
	if l[0] != "alpha" || l[1] != "zeta" {
		t.Fatalf("list = %v", l)
	}
	if a.Reset("alpha") != 1 {
		t.Fatal("reset alpha should drop 1")
	}
	if a.Reset("ALL") != 1 {
		t.Fatal("ALL reset should drop the remaining 1")
	}
}

func TestAdaptLatencyRejectsBadInputs(t *testing.T) {
	a := NewAdaptLatency()
	if err := a.Configure("", nil, 60*time.Second, 1); err == nil {
		t.Fatal("empty policy_id should fail")
	}
	if err := a.Observe("p", "", 100); err == nil {
		t.Fatal("empty model should fail")
	}
	if err := a.Observe("p", "m", -1); err == nil {
		t.Fatal("negative latency should fail")
	}
	if _, err := a.Pick("p", 0); err == nil {
		t.Fatal("zero target should fail")
	}
}

func TestAdaptLatencyStatsAdvance(t *testing.T) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1}, {Model: "expensive", Cost: 10},
	}, 60*time.Second, 3)
	for i := 0; i < 5; i++ {
		a.Observe("p", "expensive", 900)
	}
	a.Pick("p", 500) // demotes
	st := a.Stats()
	if st.Policies != 1 || st.TotalObserves != 5 || st.TotalPicks != 1 || st.TotalDemotes != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkAdaptLatencyObserve(b *testing.B) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{{Model: "m", Cost: 1}}, 60*time.Second, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Observe("p", "m", 100)
	}
}

func BenchmarkAdaptLatencyPick(b *testing.B) {
	a := NewAdaptLatency()
	a.ConfigurePublic("p", []AdaptLatencyTarget{
		{Model: "cheap", Cost: 1}, {Model: "mid", Cost: 5}, {Model: "expensive", Cost: 10},
	}, 60*time.Second, 5)
	for i := 0; i < 200; i++ {
		a.Observe("p", "cheap", 50)
		a.Observe("p", "mid", 200)
		a.Observe("p", "expensive", 450)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Pick("p", 500)
	}
}
