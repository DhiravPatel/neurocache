package llmstack

import (
	"testing"
)

func TestMoERegisterAndRoute(t *testing.T) {
	// Use explicit embeddings so the test is deterministic — the
	// fallback hash-BoW can produce ties between unrelated experts
	// for short descriptions.
	m := NewMoERouter()
	m.RegisterExpert("math", "MathGPT", "math",
		ExpertOpts{Tags: []string{"math"}, Vec: []float64{1, 0, 0}})
	m.RegisterExpert("code", "CodeGPT", "code",
		ExpertOpts{Tags: []string{"code"}, Vec: []float64{0, 1, 0}})
	m.RegisterExpert("creative", "CreativeGPT", "writing",
		ExpertOpts{Tags: []string{"writing"}, Vec: []float64{0, 0, 1}})

	hits := m.Route("anything", RouteOpts{K: 1, Vec: []float64{1, 0, 0}})
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].ExpertID != "math" {
		t.Fatalf("expected math expert, got %s", hits[0].ExpertID)
	}
}

func TestMoEEmptyRoutingReturnsTopK(t *testing.T) {
	m := NewMoERouter()
	for i := 0; i < 5; i++ {
		m.RegisterExpert(string(rune('a'+i)), "name", "description", ExpertOpts{})
	}
	hits := m.Route("anything", RouteOpts{K: 3})
	if len(hits) != 3 {
		t.Fatalf("len = %d, want 3", len(hits))
	}
}

func TestMoEHealthDegradesScore(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("good", "Good", "math expert", ExpertOpts{})
	m.RegisterExpert("bad", "Bad", "math expert", ExpertOpts{})

	// Both start with capability ~1 and success_rate=1
	// Now degrade "bad" with 100 failures
	for i := 0; i < 100; i++ {
		m.Record("bad", false, 0)
	}
	// Good still has success_rate=1 (no calls); should win.
	hits := m.Route("math problem", RouteOpts{K: 2})
	if hits[0].ExpertID != "good" {
		t.Fatalf("bad expert with 0%% success should rank below good: %+v", hits)
	}
	if hits[1].SuccessRate >= 0.05 {
		t.Fatalf("bad success_rate = %f, want ~0", hits[1].SuccessRate)
	}
}

func TestMoEDimMismatchRejected(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("a", "n", "d", ExpertOpts{Vec: []float64{1, 0, 0}})
	if err := m.RegisterExpert("b", "n", "d", ExpertOpts{Vec: []float64{1, 0}}); err == nil {
		t.Fatal("expected dim mismatch error")
	}
}

func TestMoEReplacePreservesCounters(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("a", "Old name", "desc", ExpertOpts{})
	for i := 0; i < 10; i++ {
		m.Record("a", true, 100)
	}
	m.RegisterExpert("a", "New name", "new desc", ExpertOpts{})
	rows := m.Experts(nil)
	if rows[0].Calls != 10 || rows[0].Successes != 10 {
		t.Fatalf("counters lost on replace: %+v", rows[0])
	}
	if rows[0].Name != "New name" {
		t.Fatalf("name not updated: %s", rows[0].Name)
	}
}

func TestMoERecordUnknownReturnsFalse(t *testing.T) {
	m := NewMoERouter()
	if m.Record("nope", true, 0) {
		t.Fatal("record on unknown should return false")
	}
}

func TestMoERouteTagFilter(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("math", "Math", "math", ExpertOpts{Tags: []string{"science"}})
	m.RegisterExpert("code", "Code", "code", ExpertOpts{Tags: []string{"engineering"}})
	hits := m.Route("anything", RouteOpts{K: 5, Tags: []string{"engineering"}})
	if len(hits) != 1 || hits[0].ExpertID != "code" {
		t.Fatalf("tag filter failed: %+v", hits)
	}
}

func TestMoERouteAvgLatency(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("a", "A", "d", ExpertOpts{})
	m.Record("a", true, 200)
	m.Record("a", true, 400)
	rows := m.Experts(nil)
	if rows[0].AvgLatencyMS != 300 {
		t.Fatalf("avg = %d, want 300", rows[0].AvgLatencyMS)
	}
}

func TestMoEForget(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("a", "A", "d", ExpertOpts{})
	if !m.Forget("a") {
		t.Fatal("forget should return true")
	}
	if m.Forget("a") {
		t.Fatal("forget on missing should return false")
	}
}

func TestMoEStatsAdvance(t *testing.T) {
	m := NewMoERouter()
	m.RegisterExpert("a", "A", "d", ExpertOpts{})
	m.Route("x", RouteOpts{K: 1})
	m.Record("a", true, 0)
	s := m.Stats()
	if s.Experts != 1 || s.TotalRoutes != 1 || s.TotalRecords != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestMoERejectsBadRegister(t *testing.T) {
	m := NewMoERouter()
	if err := m.RegisterExpert("", "n", "d", ExpertOpts{}); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := m.RegisterExpert("a", "", "d", ExpertOpts{}); err == nil {
		t.Fatal("empty name should fail")
	}
}
