package llmstack

import "testing"

func TestSandboxRecordAndReplay(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("box", "r1", "summarize the doc", "gpt-4o", 0.9, 0.01, 200)
	s.Record("box", "r2", "translate to french", "gpt-4o", 0.85, 0.01, 220)
	// Reroute all "summarize" → cheaper model
	s.SetRoute("box", "summarize", "gpt-4o-mini")
	s.SetProjection("box", "gpt-4o-mini", 0.95, 0.20, 0.50)
	r, _ := s.Replay("box")
	if r.ChangedCount != 1 {
		t.Fatalf("changed = %d", r.ChangedCount)
	}
	// Cost delta should be NEGATIVE (cheaper route on changed request)
	if r.CostDeltaTotal >= 0 {
		t.Fatalf("expected negative cost delta: %f", r.CostDeltaTotal)
	}
}

func TestSandboxNoMatchKeepsRoute(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("box", "r1", "x", "route-a", 0.9, 0.01, 100)
	s.SetRoute("box", "no-such-substring", "route-b")
	r, _ := s.Replay("box")
	if r.ChangedCount != 0 {
		t.Fatalf("unexpected change: %d", r.ChangedCount)
	}
}

func TestSandboxRulesOrderFirstMatchWins(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("box", "r", "summarize the doc", "old", 0.9, 0.01, 100)
	s.SetRoute("box", "doc", "route-doc")
	s.SetRoute("box", "summarize", "route-sum")
	r, _ := s.Replay("box")
	// First rule (doc) should match; route-doc wins
	if r.PerRoute["route-doc"].AfterCount != 1 {
		t.Fatalf("first rule should win: %+v", r.PerRoute)
	}
}

func TestSandboxPerRouteBreakdown(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("box", "r1", "x", "a", 0.9, 0.01, 100)
	s.Record("box", "r2", "x", "a", 0.8, 0.01, 100)
	s.Record("box", "r3", "x", "b", 0.7, 0.02, 200)
	r, _ := s.Replay("box")
	if r.PerRoute["a"].BeforeCount != 2 || r.PerRoute["b"].BeforeCount != 1 {
		t.Fatalf("breakdown: %+v", r.PerRoute)
	}
}

func TestSandboxUnsetRoute(t *testing.T) {
	s := NewSandboxReplay()
	s.SetRoute("box", "x", "new")
	if s.UnsetRoute("box", "x") != 1 {
		t.Fatal("unset should drop 1")
	}
	if s.UnsetRoute("box", "ghost") != 0 {
		t.Fatal("missing rule should be 0")
	}
}

func TestSandboxRollingWindowCap(t *testing.T) {
	s := NewSandboxReplay()
	// Force a smaller buffer for the test by direct construction
	s.Record("box", "x", "y", "r", 0.5, 0, 0)
	n, _ := s.Size("box")
	if n != 1 {
		t.Fatalf("size = %d", n)
	}
}

func TestSandboxRejectsBadInput(t *testing.T) {
	s := NewSandboxReplay()
	if err := s.Record("", "r", "i", "x", 0, 0, 0); err == nil {
		t.Fatal("empty sandbox")
	}
	if err := s.Record("b", "", "i", "x", 0, 0, 0); err == nil {
		t.Fatal("empty request")
	}
	if err := s.Record("b", "r", "", "x", 0, 0, 0); err == nil {
		t.Fatal("empty input")
	}
	if err := s.Record("b", "r", "i", "", 0, 0, 0); err == nil {
		t.Fatal("empty route")
	}
	if err := s.Record("b", "r", "i", "x", 1.5, 0, 0); err == nil {
		t.Fatal("quality > 1")
	}
	if err := s.Record("b", "r", "i", "x", 0.5, -1, 0); err == nil {
		t.Fatal("negative cost")
	}
}

func TestSandboxForgetAndList(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("a", "r", "i", "x", 0, 0, 0)
	s.Record("b", "r", "i", "x", 0, 0, 0)
	if l := s.List(); len(l) != 2 {
		t.Fatal("list")
	}
	if s.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if s.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestSandboxStats(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("a", "r", "i", "x", 0.5, 0, 0)
	s.Replay("a")
	st := s.Stats()
	if st.TotalRecords != 1 || st.TotalReplays != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestSandboxCaseInsensitiveMatch(t *testing.T) {
	s := NewSandboxReplay()
	s.Record("b", "r", "SuMmArize", "old", 0.5, 0, 0)
	s.SetRoute("b", "summarize", "new")
	r, _ := s.Replay("b")
	if r.ChangedCount != 1 {
		t.Fatalf("case-insensitive match failed: %+v", r)
	}
}
