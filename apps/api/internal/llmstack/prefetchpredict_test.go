package llmstack

import (
	"testing"
)

func TestPrefetchPredictTooLittleHistory(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("s1", "hello")
	c, ok := p.Predict("s1", 5)
	if !ok || len(c) != 0 {
		t.Fatalf("with 1 event, predict should return empty: c=%v ok=%v", c, ok)
	}
}

func TestPrefetchPredictBigramRecall(t *testing.T) {
	p := NewPrefetchPredictor()
	// Pattern: every time user asks about "pricing", they then ask about "billing"
	p.Observe("s1", "what is the pricing model")
	p.Observe("s1", "how does billing work")
	p.Observe("s1", "what is the pricing model again")
	// Now predict; "billing" should be top candidate
	c, ok := p.Predict("s1", 5)
	if !ok || len(c) == 0 {
		t.Fatalf("predict empty: %+v", c)
	}
	if c[0].Text == "" {
		t.Fatalf("top candidate empty: %+v", c)
	}
	// The successor of the most-similar prior event should win
	found := false
	for _, cand := range c {
		if cand.Text == "how does billing work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected billing successor in candidates, got %+v", c)
	}
}

func TestPrefetchPredictRankByAccumulatedSimilarity(t *testing.T) {
	p := NewPrefetchPredictor()
	// Three pricing→billing transitions, one weather→sunny transition
	for i := 0; i < 3; i++ {
		p.Observe("s1", "pricing question")
		p.Observe("s1", "billing follow up")
	}
	p.Observe("s1", "weather today")
	p.Observe("s1", "sunny")
	p.Observe("s1", "pricing question again")
	c, _ := p.Predict("s1", 5)
	// Top candidate should be billing — three matching prefixes outweigh one weather match
	if len(c) == 0 || c[0].Text != "billing follow up" {
		t.Fatalf("expected billing top, got %+v", c)
	}
}

func TestPrefetchPredictExcludesEcho(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("s1", "same question")
	p.Observe("s1", "different reply")
	p.Observe("s1", "same question")
	c, _ := p.Predict("s1", 5)
	for _, cand := range c {
		if cand.Text == "same question" {
			t.Fatal("self-echo should be excluded from candidates")
		}
	}
}

func TestPrefetchPredictRespectLimit(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("s1", "a")
	p.Observe("s1", "b")
	p.Observe("s1", "c")
	p.Observe("s1", "d")
	p.Observe("s1", "e")
	p.Observe("s1", "a") // trigger predict from prefix "a"
	c, _ := p.Predict("s1", 2)
	if len(c) > 2 {
		t.Fatalf("limit not respected: got %d", len(c))
	}
}

func TestPrefetchPredictPerSessionIsolation(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("s1", "alpha")
	p.Observe("s1", "beta")
	p.Observe("s1", "alpha")
	c1, _ := p.Predict("s1", 5)
	c2, _ := p.Predict("s2", 5)
	if len(c1) == 0 {
		t.Fatal("s1 should have candidates")
	}
	if c2 != nil {
		t.Fatalf("s2 had no observations; got %v", c2)
	}
}

func TestPrefetchPredictHorizonClipsLookback(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Horizon("s1", 2)
	// Far-back transition for "alpha" → "beta"
	p.Observe("s1", "alpha")
	p.Observe("s1", "beta")
	// Padding
	for i := 0; i < 10; i++ {
		p.Observe("s1", "padding")
	}
	p.Observe("s1", "alpha")
	// With horizon=2 we don't look back far enough to see the alpha→beta transition
	c, _ := p.Predict("s1", 5)
	for _, cand := range c {
		if cand.Text == "beta" {
			t.Fatalf("horizon=2 should clip; beta in candidates: %+v", c)
		}
	}
}

func TestPrefetchHitUpdatesEMA(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("s1", "x")
	for i := 0; i < 10; i++ {
		p.Hit("s1", "anything")
	}
	st, _ := p.Status("s1")
	if st.HitRate < 0.7 {
		t.Fatalf("EMA should rise toward 1.0 after many hits: %f", st.HitRate)
	}
	if st.TotalHits != 10 {
		t.Fatalf("total hits = %d", st.TotalHits)
	}
}

func TestPrefetchSessionsSorted(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("zeta", "x")
	p.Observe("alpha", "x")
	p.Observe("mid", "x")
	l := p.Sessions()
	if len(l) != 3 || l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("sessions = %v", l)
	}
}

func TestPrefetchResetOne(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("a", "x")
	p.Observe("b", "x")
	if p.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
	if _, ok := p.Status("a"); ok {
		t.Fatal("a still present")
	}
}

func TestPrefetchResetAll(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("a", "x")
	p.Observe("b", "x")
	if p.Reset("ALL") != 2 {
		t.Fatal("reset ALL should drop 2")
	}
}

func TestPrefetchRejectsBadInput(t *testing.T) {
	p := NewPrefetchPredictor()
	if err := p.Observe("", "x"); err == nil {
		t.Fatal("empty session_id should fail")
	}
	if err := p.Observe("s", ""); err == nil {
		t.Fatal("empty text should fail")
	}
	if err := p.Hit("", "x"); err == nil {
		t.Fatal("empty session_id should fail")
	}
	if err := p.Hit("ghost", "x"); err == nil {
		t.Fatal("unknown session should fail")
	}
}

func TestPrefetchStatsAdvance(t *testing.T) {
	p := NewPrefetchPredictor()
	p.Observe("s", "a")
	p.Observe("s", "b")
	p.Predict("s", 5)
	p.Hit("s", "anything")
	st := p.Stats()
	if st.Sessions != 1 || st.TotalObserves != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalPredicts != 1 || st.TotalHits != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkPrefetchObserve(b *testing.B) {
	p := NewPrefetchPredictor()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Observe("session-x", "user question goes here")
	}
}

func BenchmarkPrefetchPredict(b *testing.B) {
	p := NewPrefetchPredictor()
	for i := 0; i < 8; i++ {
		p.Observe("s", "pricing question")
		p.Observe("s", "billing answer")
	}
	p.Observe("s", "pricing question now")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Predict("s", 5)
	}
}
