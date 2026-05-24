package llmstack

import (
	"math"
	"strconv"
	"testing"
)

func TestShadowConfigure(t *testing.T) {
	s := NewShadowEval()
	if err := s.Configure("e1", "prompt-v2", "prompt-v3", 0.20, 1.0); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.Report("e1", 0)
	if rep.BaselineName != "prompt-v2" || rep.CandidateName != "prompt-v3" {
		t.Fatalf("names = %s / %s", rep.BaselineName, rep.CandidateName)
	}
}

func TestShadowMirrorRequiresConfig(t *testing.T) {
	s := NewShadowEval()
	if _, err := s.Mirror("ghost", "r1", "input"); err == nil {
		t.Fatal("mirror without config should fail")
	}
}

func TestShadowMirrorRespectsSampleRate(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 0.10) // 10%
	hits := 0
	const n = 5000
	for i := 0; i < n; i++ {
		v, _ := s.Mirror("e1", "req-"+strconv.Itoa(i), "x")
		if v == "mirror" {
			hits++
		}
	}
	frac := float64(hits) / float64(n)
	if frac < 0.07 || frac > 0.13 {
		t.Fatalf("sample rate ~10%% missed: %f", frac)
	}
}

func TestShadowRecordUpdatesStats(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	for i := 0; i < 50; i++ {
		s.Record("e1", "r"+strconv.Itoa(i), 0.70, 0.82, 100, 120)
	}
	rep, _ := s.Report("e1", 0)
	if rep.N != 50 {
		t.Fatalf("n = %d", rep.N)
	}
	if math.Abs(rep.BaselineMean-0.70) > 1e-9 {
		t.Fatalf("baseline mean = %f", rep.BaselineMean)
	}
	if math.Abs(rep.CandidateMean-0.82) > 1e-9 {
		t.Fatalf("candidate mean = %f", rep.CandidateMean)
	}
	if math.Abs(rep.MeanLift-0.12) > 1e-9 {
		t.Fatalf("lift = %f", rep.MeanLift)
	}
	if rep.LatencyLiftMS != 20 {
		t.Fatalf("latency lift = %f", rep.LatencyLiftMS)
	}
}

func TestShadowWinRateTracked(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	// 7 wins for candidate, 3 wins for baseline
	for i := 0; i < 7; i++ {
		s.Record("e1", "w"+strconv.Itoa(i), 0.50, 0.80, 0, 0)
	}
	for i := 0; i < 3; i++ {
		s.Record("e1", "l"+strconv.Itoa(i), 0.80, 0.50, 0, 0)
	}
	rep, _ := s.Report("e1", 0)
	if math.Abs(rep.WinRateCandidate-0.70) > 1e-9 {
		t.Fatalf("win rate = %f", rep.WinRateCandidate)
	}
}

func TestShadowDetectsRegressions(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0) // threshold 0.20
	// 5 normal records
	for i := 0; i < 5; i++ {
		s.Record("e1", "n"+strconv.Itoa(i), 0.80, 0.82, 0, 0)
	}
	// 2 clear regressions
	s.Record("e1", "bad-1", 0.90, 0.30, 0, 0)
	s.Record("e1", "bad-2", 0.85, 0.40, 0, 0)
	rep, _ := s.Report("e1", 0)
	if rep.RegressionsCount != 2 {
		t.Fatalf("regressions = %d", rep.RegressionsCount)
	}
	// Worst regression first
	if rep.Regressions[0].ReqID != "bad-1" {
		t.Fatalf("worst regression order: %+v", rep.Regressions)
	}
}

func TestShadowRegressionLimitCaps(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	for i := 0; i < 10; i++ {
		s.Record("e1", "r"+strconv.Itoa(i), 0.90, 0.20, 0, 0)
	}
	rep, _ := s.Report("e1", 3)
	if len(rep.Regressions) != 3 {
		t.Fatalf("limit not respected: %d", len(rep.Regressions))
	}
}

func TestShadowPromoteHoldOnLowN(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	for i := 0; i < 10; i++ {
		s.Record("e1", "r"+strconv.Itoa(i), 0.5, 0.8, 0, 0)
	}
	p, _ := s.Promote("e1", 0)
	if p.Verdict != "hold" {
		t.Fatalf("verdict = %s (expected hold under 100 samples)", p.Verdict)
	}
}

func TestShadowPromoteNotRecommendedOnRegression(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	for i := 0; i < 150; i++ {
		s.Record("e1", "r"+strconv.Itoa(i), 0.80, 0.50, 0, 0)
	}
	p, _ := s.Promote("e1", 0)
	if p.Verdict != "not_recommended" {
		t.Fatalf("verdict = %s", p.Verdict)
	}
}

func TestShadowPromoteReady(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	for i := 0; i < 150; i++ {
		s.Record("e1", "r"+strconv.Itoa(i), 0.70, 0.78, 0, 0)
	}
	p, _ := s.Promote("e1", 0.20)
	if p.Verdict != "ready" {
		t.Fatalf("verdict = %s reason=%s lift=%f", p.Verdict, p.Reason, p.MeanLift)
	}
	if p.SuggestedRate != 0.20 {
		t.Fatalf("rate = %f", p.SuggestedRate)
	}
}

func TestShadowResetPreservesConfig(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e1", "b", "c", 0.20, 1.0)
	s.Record("e1", "r", 0.5, 0.5, 0, 0)
	s.Reset("e1")
	rep, _ := s.Report("e1", 0)
	if rep.N != 0 || rep.BaselineName != "b" {
		t.Fatalf("reset broke config: %+v", rep)
	}
}

func TestShadowListSorted(t *testing.T) {
	s := NewShadowEval()
	s.Configure("z", "b", "c", 0, 0)
	s.Configure("a", "b", "c", 0, 0)
	s.Configure("m", "b", "c", 0, 0)
	l := s.List()
	if l[0] != "a" || l[2] != "z" {
		t.Fatalf("list = %v", l)
	}
}

func TestShadowRejectsBadInput(t *testing.T) {
	s := NewShadowEval()
	if err := s.Configure("", "b", "c", 0, 0); err == nil {
		t.Fatal("empty exp id should fail")
	}
	if err := s.Configure("a", "b", "c", 1.5, 0); err == nil {
		t.Fatal("regression_threshold > 1 should fail")
	}
	s.Configure("a", "b", "c", 0, 0)
	if err := s.Record("a", "r", 1.5, 0.5, 0, 0); err == nil {
		t.Fatal("baseline q > 1 should fail")
	}
	if err := s.Record("a", "r", 0.5, 0.5, -1, 0); err == nil {
		t.Fatal("negative latency should fail")
	}
}

func TestShadowStatsAdvance(t *testing.T) {
	s := NewShadowEval()
	s.Configure("e", "b", "c", 0, 0)
	s.Mirror("e", "r", "x")
	s.Record("e", "r", 0.5, 0.6, 0, 0)
	st := s.Stats()
	if st.Experiments != 1 || st.TotalMirrors != 1 || st.TotalRecords != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkShadowRecord(b *testing.B) {
	s := NewShadowEval()
	s.Configure("e", "b", "c", 0.20, 1.0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Record("e", "r", 0.70, 0.82, 100, 120)
	}
}

func BenchmarkShadowReport(b *testing.B) {
	s := NewShadowEval()
	s.Configure("e", "b", "c", 0.20, 1.0)
	for i := 0; i < 1000; i++ {
		s.Record("e", "r"+strconv.Itoa(i), 0.7, 0.78, 100, 120)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Report("e", 10)
	}
}
