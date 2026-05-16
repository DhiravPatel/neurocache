package llmstack

import (
	"testing"
)

func TestEscalateConfigureValidatesExpression(t *testing.T) {
	e := NewEscalationLadder()
	if err := e.Configure("p1", map[string]string{
		"cache": "cache_score >= 0.9",
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Configure("p1", map[string]string{
		"cheap": "novelty BAD 0.4",
	}); err == nil {
		t.Fatal("invalid op should fail")
	}
	if err := e.Configure("p1", map[string]string{
		"cheap": "novelty >= notanumber",
	}); err == nil {
		t.Fatal("non-numeric value should fail")
	}
	if err := e.Configure("p1", map[string]string{
		"unknown_tier": "x >= 1",
	}); err == nil {
		t.Fatal("invalid tier should fail")
	}
}

func TestEscalateDecideCacheTierWins(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("support", map[string]string{
		"cache": "cache_score >= 0.9",
		"cheap": "novelty < 0.4 AND confidence >= 0.7",
	})
	d, err := e.Decide("support", map[string]float64{
		"cache_score": 0.95,
		"novelty":     0.2,
		"confidence":  0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != "cache" {
		t.Fatalf("tier = %s (expected cache, the highest-priority match)", d.Tier)
	}
}

func TestEscalateDecideCheapTierMatches(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("support", map[string]string{
		"cache": "cache_score >= 0.9",
		"cheap": "novelty < 0.4 AND confidence >= 0.7",
		"human": "novelty > 0.85 OR confidence < 0.3",
	})
	d, _ := e.Decide("support", map[string]float64{
		"cache_score": 0.5,
		"novelty":     0.3,
		"confidence":  0.8,
	})
	if d.Tier != "cheap" {
		t.Fatalf("tier = %s", d.Tier)
	}
}

func TestEscalateDecideHumanFires(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("support", map[string]string{
		"cache": "cache_score >= 0.9",
		"cheap": "novelty < 0.4 AND confidence >= 0.7",
		"human": "novelty > 0.85 OR confidence < 0.3",
	})
	d, _ := e.Decide("support", map[string]float64{
		"cache_score": 0.4,
		"novelty":     0.91,
		"confidence":  0.4,
	})
	if d.Tier != "human" {
		t.Fatalf("tier = %s", d.Tier)
	}
}

func TestEscalateDefaultExpensiveWhenNoMatch(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{
		"cache": "cache_score >= 0.95",
		"cheap": "novelty < 0.1",
	})
	d, _ := e.Decide("p", map[string]float64{
		"cache_score": 0.3,
		"novelty":     0.5,
	})
	if d.Tier != "expensive" {
		t.Fatalf("default tier = %s", d.Tier)
	}
}

func TestEscalateMissingSignalDoesNotMatch(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{
		"cache": "cache_score >= 0.9",
	})
	// No cache_score → clause is false → fall through to expensive
	d, _ := e.Decide("p", map[string]float64{"novelty": 0.5})
	if d.Tier != "expensive" {
		t.Fatalf("missing signal: %s", d.Tier)
	}
}

func TestEscalatePolicyClearsTierWithDash(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{"cache": "cache_score >= 0.9"})
	e.Configure("p", map[string]string{"cache": "-"})
	rows, _ := e.Policy("p")
	for _, r := range rows {
		if r.Tier == "cache" {
			t.Fatalf("cache tier should be cleared: %+v", r)
		}
	}
}

func TestEscalatePolicyReturnsConfiguredExprs(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{
		"cache":     "cache_score >= 0.9",
		"expensive": "novelty < 0.8",
	})
	rows, ok := e.Policy("p")
	if !ok || len(rows) != 2 {
		t.Fatalf("policy rows = %d", len(rows))
	}
	// Returned in priority order
	if rows[0].Tier != "cache" || rows[1].Tier != "expensive" {
		t.Fatalf("priority order broken: %+v", rows)
	}
}

func TestEscalateRecordAndReport(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{"cache": "x >= 1"})
	e.Record("p", "cache", "win", 0.9)
	e.Record("p", "cache", "win", 0.85)
	e.Record("p", "cheap", "lose", 0.3)
	rows, _ := e.Report("p")
	byTier := map[string]EscalateReportRow{}
	for _, r := range rows {
		byTier[r.Tier] = r
	}
	if byTier["cache"].Count != 2 || byTier["cache"].OutcomeWin != 2 {
		t.Fatalf("cache row: %+v", byTier["cache"])
	}
	if byTier["cache"].MeanQuality < 0.86 {
		t.Fatalf("mean quality = %f", byTier["cache"].MeanQuality)
	}
	if byTier["cheap"].OutcomeLose != 1 {
		t.Fatalf("cheap row: %+v", byTier["cheap"])
	}
}

func TestEscalateRecordRejectsBadInput(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{"cache": "x >= 0"})
	if err := e.Record("p", "bogus", "win", 0.5); err == nil {
		t.Fatal("bad tier should fail")
	}
	if err := e.Record("p", "cache", "win", 1.5); err == nil {
		t.Fatal("quality > 1 should fail")
	}
}

func TestEscalateListSorted(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("zeta", map[string]string{"cache": "x >= 1"})
	e.Configure("alpha", map[string]string{"cache": "x >= 1"})
	e.Configure("mid", map[string]string{"cache": "x >= 1"})
	l := e.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestEscalateResetOne(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("a", map[string]string{"cache": "x >= 1"})
	e.Configure("b", map[string]string{"cache": "x >= 1"})
	if e.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestEscalateResetAll(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("a", map[string]string{"cache": "x >= 1"})
	e.Configure("b", map[string]string{"cache": "x >= 1"})
	if e.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestEscalateDecideUnknownPolicy(t *testing.T) {
	e := NewEscalationLadder()
	if _, err := e.Decide("ghost", nil); err == nil {
		t.Fatal("unknown policy should fail")
	}
}

func TestEscalateORSemantics(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{
		"human": "novelty > 0.85 OR confidence < 0.3",
	})
	// Only novelty matches
	d, _ := e.Decide("p", map[string]float64{"novelty": 0.9, "confidence": 0.7})
	if d.Tier != "human" {
		t.Fatalf("OR-left should fire: %s", d.Tier)
	}
	// Only confidence matches
	d2, _ := e.Decide("p", map[string]float64{"novelty": 0.3, "confidence": 0.1})
	if d2.Tier != "human" {
		t.Fatalf("OR-right should fire: %s", d2.Tier)
	}
}

func TestEscalateStatsAdvance(t *testing.T) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{"cache": "x >= 1"})
	e.Decide("p", map[string]float64{"x": 0.5})
	e.Record("p", "cache", "win", 0.8)
	st := e.Stats()
	if st.Policies != 1 || st.TotalDecisions != 1 || st.TotalRecords != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkEscalateDecide(b *testing.B) {
	e := NewEscalationLadder()
	e.Configure("p", map[string]string{
		"cache":     "cache_score >= 0.9",
		"cheap":     "novelty < 0.4 AND confidence >= 0.7",
		"expensive": "novelty < 0.8 AND confidence >= 0.5",
		"human":     "novelty > 0.85 OR confidence < 0.3",
	})
	sig := map[string]float64{
		"cache_score": 0.5,
		"novelty":     0.6,
		"confidence":  0.6,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Decide("p", sig)
	}
}
