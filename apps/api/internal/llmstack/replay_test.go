package llmstack

import (
	"testing"
)

func TestReplayRecordAndGet(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "prompt-A", "completion-A")
	r.Record("s1", 2, "tool", "weather NYC", "72F")
	rows, ok := r.Get("s1", -1)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Kind != "llm" || rows[1].Out != "72F" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestReplayRecordRejectsBackwardsStep(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 5, "llm", "x", "y")
	if err := r.Record("s1", 3, "llm", "x", "y"); err == nil {
		t.Fatal("backwards step should fail")
	}
}

func TestReplayRecordReplacesSameStep(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "a", "b")
	r.Record("s1", 1, "llm", "a", "B-new") // replaces
	rows, _ := r.Get("s1", -1)
	if len(rows) != 1 || rows[0].Out != "B-new" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestReplayOpenNextCloseRoundtrip(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "prompt", "completion")
	r.Record("s1", 2, "tool", "weather NYC", "72F")
	r.Open("s1")
	step, err := r.Next("s1", "llm", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if step.Out != "completion" {
		t.Fatalf("recorded out = %s", step.Out)
	}
	step2, _ := r.Next("s1", "tool", "weather NYC")
	if step2.Out != "72F" {
		t.Fatalf("step2 = %+v", step2)
	}
	r.Close("s1")
}

func TestReplayNextRequiresOpen(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "x", "y")
	if _, err := r.Next("s1", "llm", "x"); err == nil {
		t.Fatal("NEXT without OPEN should fail")
	}
}

func TestReplayDriftDetected(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "original prompt", "answer")
	r.Open("s1")
	_, err := r.Next("s1", "llm", "DIFFERENT prompt")
	if err == nil {
		t.Fatal("input divergence should error")
	}
	if !IsReplayDrift(err) {
		t.Fatalf("expected REPLAYDRIFT, got %v", err)
	}
}

func TestReplayNextSkipsNonMatchingKinds(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "p1", "c1")
	r.Record("s1", 2, "tool", "t1", "t-out")
	r.Record("s1", 3, "llm", "p2", "c2")
	r.Open("s1")
	// Skip the LLM step at index 0 and 2; consume the tool step at 1
	step, err := r.Next("s1", "tool", "t1")
	if err != nil || step.Out != "t-out" {
		t.Fatalf("tool next = %+v, err=%v", step, err)
	}
}

func TestReplayDiffFindsDivergence(t *testing.T) {
	r := NewReplayStore()
	r.Record("a", 1, "llm", "p", "good")
	r.Record("a", 2, "tool", "t", "ok")
	r.Record("b", 1, "llm", "p", "good")
	r.Record("b", 2, "tool", "t", "BROKEN")
	rows, err := r.Diff("a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected divergence rows")
	}
	if rows[0].Step != 2 || rows[0].Field != "out" {
		t.Fatalf("first divergence wrong: %+v", rows[0])
	}
}

func TestReplayDiffReportsLengthMismatch(t *testing.T) {
	r := NewReplayStore()
	r.Record("a", 1, "llm", "p", "c")
	r.Record("a", 2, "llm", "p", "c")
	r.Record("b", 1, "llm", "p", "c")
	rows, _ := r.Diff("a", "b")
	found := false
	for _, row := range rows {
		if row.Field == "n_steps" {
			found = true
		}
	}
	if !found {
		t.Fatalf("length mismatch row missing: %+v", rows)
	}
}

func TestReplayExportBundle(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "p", "c")
	r.Record("s1", 2, "tool", "t", "o")
	b, ok := r.Export("s1")
	if !ok || b.NSteps != 2 {
		t.Fatalf("export = %+v", b)
	}
	if b.SessionID != "s1" {
		t.Fatalf("sid = %s", b.SessionID)
	}
}

func TestReplayGetByStep(t *testing.T) {
	r := NewReplayStore()
	r.Record("s1", 1, "llm", "p1", "c1")
	r.Record("s1", 2, "llm", "p2", "c2")
	rows, _ := r.Get("s1", 2)
	if len(rows) != 1 || rows[0].In != "p2" {
		t.Fatalf("step filter broken: %+v", rows)
	}
}

func TestReplayRejectsBadInput(t *testing.T) {
	r := NewReplayStore()
	if err := r.Record("", 1, "llm", "x", "y"); err == nil {
		t.Fatal("empty sid should fail")
	}
	if err := r.Record("s", -1, "llm", "x", "y"); err == nil {
		t.Fatal("negative step should fail")
	}
	if err := r.Record("s", 1, "bogus", "x", "y"); err == nil {
		t.Fatal("bad kind should fail")
	}
	if err := r.Open(""); err == nil {
		t.Fatal("empty sid for open should fail")
	}
	if err := r.Open("ghost"); err == nil {
		t.Fatal("unknown sid for open should fail")
	}
	if _, err := r.Diff("ghost", "x"); err == nil {
		t.Fatal("diff with unknown sid should fail")
	}
}

func TestReplaySessionsSorted(t *testing.T) {
	r := NewReplayStore()
	r.Record("zeta", 1, "llm", "x", "y")
	r.Record("alpha", 1, "llm", "x", "y")
	r.Record("mid", 1, "llm", "x", "y")
	sess := r.Sessions()
	if sess[0] != "alpha" || sess[2] != "zeta" {
		t.Fatalf("sessions = %v", sess)
	}
}

func TestReplayResetAll(t *testing.T) {
	r := NewReplayStore()
	r.Record("a", 1, "llm", "x", "y")
	r.Record("b", 1, "llm", "x", "y")
	if r.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestReplayStatsAdvance(t *testing.T) {
	r := NewReplayStore()
	r.Record("s", 1, "llm", "x", "y")
	r.Open("s")
	r.Next("s", "llm", "x")
	r.Record("s2", 1, "llm", "x", "y")
	r.Diff("s", "s2")
	st := r.Stats()
	if st.Sessions != 2 || st.TotalRecords < 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalNexts != 1 || st.TotalDiffs != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkReplayRecord(b *testing.B) {
	r := NewReplayStore()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Record("s", i, "llm", "prompt", "completion")
	}
}

func BenchmarkReplayNext(b *testing.B) {
	r := NewReplayStore()
	for i := 0; i < 1000; i++ {
		r.Record("s", i, "llm", "p", "c")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Open("s")
		r.Next("s", "llm", "p")
		r.Close("s")
	}
}
