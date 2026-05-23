package llmstack

import (
	"testing"
)

func TestGoalSetAndCheckInitial(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "book a flight NYC to SF under $400 next Friday")
	r, ok := g.Check("sess-1")
	if !ok {
		t.Fatal("check returned false")
	}
	if r.TotalUpdates != 0 {
		t.Fatalf("updates = %d", r.TotalUpdates)
	}
	if r.Hint != "progress" {
		t.Fatalf("initial hint = %s, want progress", r.Hint)
	}
}

func TestGoalProgressIncrementsCount(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "do thing")
	g.Progress("sess-1", "step one done")
	g.Progress("sess-1", "step two done")
	r, _ := g.Check("sess-1")
	if r.TotalUpdates != 2 {
		t.Fatalf("updates = %d", r.TotalUpdates)
	}
}

func TestGoalDetectsLoop(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "do thing")
	// 5 nearly-identical updates → loop
	for i := 0; i < 5; i++ {
		g.Progress("sess-1", "searched again for the same flight option")
	}
	r, _ := g.Check("sess-1")
	if r.Hint != "loop" {
		t.Fatalf("expected loop hint, got %s (stalled=%d)", r.Hint, r.StalledSteps)
	}
	if !r.Stagnation {
		t.Fatal("stagnation should be true")
	}
}

func TestGoalDetectsProgress(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "complete the workflow")
	// Diverse updates → progressing
	g.Progress("sess-1", "fetched the data successfully")
	g.Progress("sess-1", "parsed the response into JSON")
	g.Progress("sess-1", "stored result in the database")
	r, _ := g.Check("sess-1")
	if r.Hint == "loop" || r.Hint == "stalled" {
		t.Fatalf("diverse updates should not be stalled: %+v", r)
	}
}

func TestGoalCompletion(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "the cat sat on the mat")
	g.Progress("sess-1", "the cat sat on the mat completely done")
	r, _ := g.Check("sess-1")
	// High similarity to goal → complete
	if r.Hint != "complete" {
		t.Fatalf("near-identical-to-goal update should be 'complete': %+v", r)
	}
}

func TestGoalProgressOnUnsetSession(t *testing.T) {
	g := NewGoalTracker()
	if err := g.Progress("sess-1", "x"); err == nil {
		t.Fatal("progress without set should fail")
	}
}

func TestGoalUnsetSessionCheck(t *testing.T) {
	g := NewGoalTracker()
	_, ok := g.Check("sess-1")
	if ok {
		t.Fatal("check on unset session should fail")
	}
}

func TestGoalHistoryLimit(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "x")
	for i := 0; i < 10; i++ {
		g.Progress("sess-1", "update")
	}
	rows := g.History("sess-1", 3)
	if len(rows) != 3 {
		t.Fatalf("limit not honored: %d", len(rows))
	}
}

func TestGoalForget(t *testing.T) {
	g := NewGoalTracker()
	g.Set("sess-1", "x")
	if !g.Forget("sess-1") {
		t.Fatal("forget should return true")
	}
	if g.Forget("sess-1") {
		t.Fatal("forget on missing should return false")
	}
}

func TestGoalSessions(t *testing.T) {
	g := NewGoalTracker()
	g.Set("a", "x")
	g.Set("b", "y")
	sessions := g.Sessions()
	if len(sessions) != 2 || sessions[0] != "a" {
		t.Fatalf("sessions = %v", sessions)
	}
}

func TestGoalRejectsEmpty(t *testing.T) {
	g := NewGoalTracker()
	if err := g.Set("", "x"); err == nil {
		t.Fatal("empty session_id should fail")
	}
	if err := g.Set("s", ""); err == nil {
		t.Fatal("empty goal should fail")
	}
}

func TestGoalStatsAdvance(t *testing.T) {
	g := NewGoalTracker()
	g.Set("s", "x")
	g.Progress("s", "y")
	g.Check("s")
	st := g.Stats()
	if st.TotalSets != 1 || st.TotalProgresses != 1 || st.TotalChecks != 1 {
		t.Fatalf("stats = %+v", st)
	}
}
