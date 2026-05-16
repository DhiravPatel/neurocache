package llmstack

import (
	"testing"
)

func TestNoveltyBaselineAndScoreInDistribution(t *testing.T) {
	n := NewNoveltyDetector()
	n.Baseline("support", []string{
		"customer can't log in to Safari",
		"login broken on Safari browser",
		"users see login errors authenticating",
		"login fails on Safari for premium tier",
	})
	r, ok := n.Score("support", "Safari login broken for our users")
	if !ok {
		t.Fatal("score returned false")
	}
	if r.Verdict == "novel" {
		t.Fatalf("on-topic query should not be novel: score=%.4f", r.Score)
	}
}

func TestNoveltyDetectsOOD(t *testing.T) {
	n := NewNoveltyDetector()
	n.Baseline("support", []string{
		"login issue with Safari browser",
		"password reset not working",
		"checkout button broken",
	})
	r, _ := n.Score("support", "quantum entanglement and antimatter physics")
	if r.Verdict == "in_distribution" {
		t.Fatalf("totally OOD query should not be in-distribution: %+v", r)
	}
}

func TestNoveltyAddAcceptsNewExample(t *testing.T) {
	n := NewNoveltyDetector()
	n.Baseline("support", []string{"safari login"})
	r1, _ := n.Score("support", "weather report")
	if r1.Verdict == "in_distribution" {
		t.Fatal("weather should be novel initially")
	}
	// Teach: weather queries are now seen often
	for i := 0; i < 5; i++ {
		n.Add("support", "weather report and forecast")
	}
	r2, _ := n.Score("support", "weather report")
	if r2.Score >= r1.Score {
		t.Fatalf("after ADD, weather should be more in-distribution: r1=%.4f r2=%.4f",
			r1.Score, r2.Score)
	}
}

func TestNoveltyThresholdConfig(t *testing.T) {
	n := NewNoveltyDetector()
	n.Baseline("d", []string{"baseline text here"})
	if err := n.SetThresholds("d", 0.1, 0.5); err != nil {
		t.Fatal(err)
	}
	// Bad must be > ok
	if err := n.SetThresholds("d", 0.8, 0.5); err == nil {
		t.Fatal("bad <= ok should fail")
	}
}

func TestNoveltyUnknownDetector(t *testing.T) {
	n := NewNoveltyDetector()
	if _, ok := n.Score("nope", "x"); ok {
		t.Fatal("score on unknown should fail")
	}
}

func TestNoveltyRejectsEmpty(t *testing.T) {
	n := NewNoveltyDetector()
	if err := n.Baseline("", []string{"x"}); err == nil {
		t.Fatal("empty detector_id should fail")
	}
	if err := n.Baseline("d", nil); err == nil {
		t.Fatal("empty baseline should fail")
	}
}

func TestNoveltyForget(t *testing.T) {
	n := NewNoveltyDetector()
	n.Baseline("d", []string{"x"})
	if !n.Forget("d") {
		t.Fatal("forget should return true")
	}
	if n.Forget("d") {
		t.Fatal("forget on missing should return false")
	}
}

func TestNoveltyStatsAdvance(t *testing.T) {
	n := NewNoveltyDetector()
	n.Baseline("d", []string{"normal traffic"})
	n.Score("d", "normal traffic similar")
	n.Score("d", "quantum entanglement")
	s := n.Stats()
	if s.TotalScores != 2 {
		t.Fatalf("scores = %d", s.TotalScores)
	}
}
