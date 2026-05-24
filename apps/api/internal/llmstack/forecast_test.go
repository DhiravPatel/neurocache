package llmstack

import (
	"testing"
	"time"
)

func TestForecastObserveAccumulates(t *testing.T) {
	f := NewCostForecast()
	for i := 0; i < 5; i++ {
		if err := f.Observe("acme", 1.0); err != nil {
			t.Fatal(err)
		}
	}
	st := f.Stats()
	if st.TotalTicks != 5 || st.Tenants != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestForecastProjectInsufficientData(t *testing.T) {
	f := NewCostForecast()
	p, err := f.Project("ghost", 60*time.Second, 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verdict != "insufficient_data" {
		t.Fatalf("verdict = %s", p.Verdict)
	}
}

func TestForecastProjectBreachDetected(t *testing.T) {
	f := NewCostForecast()
	// 10 ticks at $1 each spread over the window — high burn rate
	for i := 0; i < 10; i++ {
		f.Observe("acme", 1.0)
		time.Sleep(2 * time.Millisecond)
	}
	// Set a deliberately tiny cap so projection exceeds it
	p, err := f.Project("acme", 100*time.Millisecond, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Spent < 9.0 {
		t.Fatalf("spent = %f", p.Spent)
	}
	if p.Verdict != "breach" {
		t.Fatalf("verdict = %s (spent=%f, cap=%f, projected=%f)", p.Verdict, p.Spent, p.Cap, p.ProjectedEnd)
	}
}

func TestForecastProjectOK(t *testing.T) {
	f := NewCostForecast()
	for i := 0; i < 5; i++ {
		f.Observe("acme", 0.10)
		time.Sleep(1 * time.Millisecond)
	}
	p, _ := f.Project("acme", 60*time.Second, 100.0)
	if p.Verdict != "ok" && p.Verdict != "insufficient_data" {
		t.Fatalf("verdict = %s (spent=%f)", p.Verdict, p.Spent)
	}
}

func TestForecastSamplesCounted(t *testing.T) {
	f := NewCostForecast()
	for i := 0; i < 7; i++ {
		f.Observe("acme", 0.5)
	}
	p, _ := f.Project("acme", 60*time.Second, 100.0)
	if p.Samples != 7 {
		t.Fatalf("samples = %d", p.Samples)
	}
}

func TestForecastRateUSDPerDay(t *testing.T) {
	f := NewCostForecast()
	// 10 ticks at $1, ~1ms apart → slope ≈ huge → rate_per_day is huge
	for i := 0; i < 10; i++ {
		f.Observe("acme", 1.0)
		time.Sleep(1 * time.Millisecond)
	}
	p, _ := f.Project("acme", 50*time.Millisecond, 1000.0)
	if p.RateUSDPerDay <= 0 {
		t.Fatalf("rate not positive: %f", p.RateUSDPerDay)
	}
}

func TestForecastWindowFiltersOld(t *testing.T) {
	f := NewCostForecast()
	// Old tick
	f.Observe("acme", 100.0)
	time.Sleep(5 * time.Millisecond)
	// Recent tick
	f.Observe("acme", 1.0)
	f.Observe("acme", 1.0)
	p, _ := f.Project("acme", 2*time.Millisecond, 50.0)
	// Old $100 should be excluded by 2ms window
	if p.Spent > 10 {
		t.Fatalf("window filter let old spend through: spent=%f", p.Spent)
	}
}

func TestForecastAlertIdempotent(t *testing.T) {
	f := NewCostForecast()
	f.Alert("acme", 0.80)
	f.Alert("acme", 0.80) // duplicate
	rows, _ := f.Alerts("acme")
	if len(rows) != 1 {
		t.Fatalf("duplicate alert created: %d", len(rows))
	}
}

func TestForecastMultipleAlertThresholds(t *testing.T) {
	f := NewCostForecast()
	f.Alert("acme", 0.50)
	f.Alert("acme", 0.80)
	f.Alert("acme", 0.95)
	rows, _ := f.Alerts("acme")
	if len(rows) != 3 {
		t.Fatalf("alerts = %d", len(rows))
	}
}

func TestForecastTenantsSorted(t *testing.T) {
	f := NewCostForecast()
	f.Observe("zeta", 1)
	f.Observe("alpha", 1)
	f.Observe("mid", 1)
	t1 := f.Tenants()
	if t1[0] != "alpha" || t1[2] != "zeta" {
		t.Fatalf("tenants = %v", t1)
	}
}

func TestForecastResetOne(t *testing.T) {
	f := NewCostForecast()
	f.Observe("a", 1)
	f.Observe("b", 1)
	if f.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestForecastResetAll(t *testing.T) {
	f := NewCostForecast()
	f.Observe("a", 1)
	f.Observe("b", 1)
	if f.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestForecastRejectsBadInput(t *testing.T) {
	f := NewCostForecast()
	if err := f.Observe("", 1); err == nil {
		t.Fatal("empty tenant should fail")
	}
	if err := f.Observe("a", -1); err == nil {
		t.Fatal("negative spend should fail")
	}
	if _, err := f.Project("a", 0, 100); err == nil {
		t.Fatal("zero window should fail")
	}
	if _, err := f.Project("a", time.Second, 0); err == nil {
		t.Fatal("zero cap should fail")
	}
	if err := f.Alert("a", 0); err == nil {
		t.Fatal("zero fraction should fail")
	}
	if err := f.Alert("a", 1.5); err == nil {
		t.Fatal("fraction > 1 should fail")
	}
}

func TestForecastStatsAdvance(t *testing.T) {
	f := NewCostForecast()
	f.Observe("a", 1)
	f.Observe("a", 1)
	f.Project("a", time.Second, 100)
	st := f.Stats()
	if st.Tenants != 1 || st.TotalObserves != 2 || st.TotalProjects != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkForecastObserve(b *testing.B) {
	f := NewCostForecast()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Observe("acme", 0.10)
	}
}

func BenchmarkForecastProject(b *testing.B) {
	f := NewCostForecast()
	for i := 0; i < 500; i++ {
		f.Observe("acme", 0.10)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Project("acme", 60*time.Second, 100.0)
	}
}
