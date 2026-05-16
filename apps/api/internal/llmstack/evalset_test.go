package llmstack

import (
	"testing"
)

func TestEvalSetCreateAndAdd(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("summarizer")
	if err := e.AddCase("summarizer", "c1", "summarize this", "expected output"); err != nil {
		t.Fatal(err)
	}
	st, ok := e.Status("summarizer")
	if !ok || st.DraftCases != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestEvalSetFreezeMakesImmutable(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("eval-1")
	e.AddCase("eval-1", "c1", "x", "y")
	e.AddCase("eval-1", "c2", "a", "b")
	if err := e.Freeze("eval-1", "v1"); err != nil {
		t.Fatal(err)
	}
	// Adding a new case after freeze affects the next version, not v1
	e.AddCase("eval-1", "c3", "p", "q")
	e.Freeze("eval-1", "v2")
	st, _ := e.Status("eval-1")
	if len(st.Versions) != 2 {
		t.Fatalf("versions = %d", len(st.Versions))
	}
	var v1Cases, v2Cases int
	for _, v := range st.Versions {
		if v.Version == "v1" {
			v1Cases = v.Cases
		} else {
			v2Cases = v.Cases
		}
	}
	if v1Cases != 2 || v2Cases != 3 {
		t.Fatalf("v1=%d v2=%d", v1Cases, v2Cases)
	}
}

func TestEvalSetFreezeRejectsDuplicateVersion(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.Freeze("e", "v1")
	if err := e.Freeze("e", "v1"); err == nil {
		t.Fatal("duplicate version should fail")
	}
}

func TestEvalSetFreezeRejectsEmptyDraft(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	if err := e.Freeze("e", "v1"); err == nil {
		t.Fatal("empty draft freeze should fail")
	}
}

func TestEvalSetRecordRequiresFrozenCase(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.Freeze("e", "v1")
	if err := e.Record("e", "v1", "ghost", "gpt", 0.5, ""); err == nil {
		t.Fatal("unknown case_id should fail")
	}
}

func TestEvalSetDiffRegressionAndImprovement(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	for i := 1; i <= 5; i++ {
		e.AddCase("e", "c"+itoaBench(i), "input", "")
	}
	e.Freeze("e", "v1")
	// modelA scores
	e.Record("e", "v1", "c1", "gpt-4", 0.90, "")
	e.Record("e", "v1", "c2", "gpt-4", 0.85, "")
	e.Record("e", "v1", "c3", "gpt-4", 0.50, "")
	e.Record("e", "v1", "c4", "gpt-4", 0.30, "")
	e.Record("e", "v1", "c5", "gpt-4", 0.70, "")
	// modelB scores: c1 regressed badly, c4 improved a lot, c5 same
	e.Record("e", "v1", "c1", "gpt-4o", 0.20, "")
	e.Record("e", "v1", "c2", "gpt-4o", 0.80, "")
	e.Record("e", "v1", "c3", "gpt-4o", 0.55, "")
	e.Record("e", "v1", "c4", "gpt-4o", 0.85, "")
	e.Record("e", "v1", "c5", "gpt-4o", 0.70, "")

	d, err := e.Diff("e", "v1", "gpt-4", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Regressions) == 0 || d.Regressions[0].CaseID != "c1" {
		t.Fatalf("worst regression should be c1: %+v", d.Regressions)
	}
	if len(d.Improvements) == 0 || d.Improvements[0].CaseID != "c4" {
		t.Fatalf("best improvement should be c4: %+v", d.Improvements)
	}
	// c5 unchanged
	if d.NoChange < 1 {
		t.Fatalf("no_change = %d", d.NoChange)
	}
	// New failures: c1 (passed @ 0.9 → failed @ 0.2)
	found := false
	for _, id := range d.NewFailures {
		if id == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("c1 should be in new_failures: %+v", d.NewFailures)
	}
	// Newly passing: c4 (failed @ 0.3 → passed @ 0.85)
	foundPass := false
	for _, id := range d.NewlyPassing {
		if id == "c4" {
			foundPass = true
		}
	}
	if !foundPass {
		t.Fatalf("c4 should be in newly_passing: %+v", d.NewlyPassing)
	}
}

func TestEvalSetDiffDeltaMean(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	for i := 1; i <= 4; i++ {
		e.AddCase("e", "c"+itoaBench(i), "x", "")
	}
	e.Freeze("e", "v1")
	// All cases: model B is +0.10 on every case
	for i := 1; i <= 4; i++ {
		id := "c" + itoaBench(i)
		e.Record("e", "v1", id, "a", 0.50, "")
		e.Record("e", "v1", id, "b", 0.60, "")
	}
	d, _ := e.Diff("e", "v1", "a", "b")
	if d.DeltaMean < 0.09 || d.DeltaMean > 0.11 {
		t.Fatalf("delta_mean = %f", d.DeltaMean)
	}
}

func TestEvalSetDiffRequiresBothModelsRun(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.Freeze("e", "v1")
	e.Record("e", "v1", "c1", "a", 0.5, "")
	if _, err := e.Diff("e", "v1", "a", "b"); err == nil {
		t.Fatal("diff with model B unrun should fail")
	}
}

func TestEvalSetDiffSkipsCasesMissingInEitherRun(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.AddCase("e", "c2", "x", "")
	e.Freeze("e", "v1")
	// Model A runs both cases; model B only c1
	e.Record("e", "v1", "c1", "a", 0.5, "")
	e.Record("e", "v1", "c2", "a", 0.5, "")
	e.Record("e", "v1", "c1", "b", 0.5, "")
	d, _ := e.Diff("e", "v1", "a", "b")
	// c2 has no B-side; should be ignored, not counted as regression
	if d.TotalA != 2 || d.TotalB != 1 {
		t.Fatalf("totals = %d / %d", d.TotalA, d.TotalB)
	}
	if d.NoChange != 1 {
		t.Fatalf("no_change = %d (only c1 has both sides)", d.NoChange)
	}
}

func TestEvalSetAddCaseReplacesOnDuplicateID(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "input v1", "")
	e.AddCase("e", "c1", "input v2", "")
	st, _ := e.Status("e")
	if st.DraftCases != 1 {
		t.Fatalf("duplicate id should overwrite: %d cases", st.DraftCases)
	}
}

func TestEvalSetStatusListsModelsRun(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.Freeze("e", "v1")
	e.Record("e", "v1", "c1", "gpt-4", 0.5, "")
	e.Record("e", "v1", "c1", "claude", 0.6, "")
	st, _ := e.Status("e")
	models := st.Versions[0].Models
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
}

func TestEvalSetRecordRejectsBadScore(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.Freeze("e", "v1")
	if err := e.Record("e", "v1", "c1", "m", 1.5, ""); err == nil {
		t.Fatal("score > 1 should fail")
	}
}

func TestEvalSetListSorted(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("zeta")
	e.Create("alpha")
	e.Create("mid")
	l := e.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestEvalSetDropOne(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("a")
	e.Create("b")
	if e.Drop("a") != 1 {
		t.Fatal("drop a should remove 1")
	}
}

func TestEvalSetDropAll(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("a")
	e.Create("b")
	if e.Drop("ALL") != 2 {
		t.Fatal("ALL drop should remove 2")
	}
}

func TestEvalSetRejectsBadInputs(t *testing.T) {
	e := NewEvalSetStore()
	if err := e.Create(""); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := e.AddCase("", "c", "x", ""); err == nil {
		t.Fatal("empty eval_id should fail")
	}
	if err := e.AddCase("ghost", "c", "x", ""); err == nil {
		t.Fatal("unknown eval_id should fail")
	}
	if err := e.Freeze("", "v"); err == nil {
		t.Fatal("empty eval_id should fail")
	}
	if err := e.Freeze("ghost", "v"); err == nil {
		t.Fatal("unknown eval_id should fail")
	}
}

func TestEvalSetStatsAdvance(t *testing.T) {
	e := NewEvalSetStore()
	e.Create("e")
	e.AddCase("e", "c1", "x", "")
	e.Freeze("e", "v1")
	e.Record("e", "v1", "c1", "a", 0.5, "")
	e.Record("e", "v1", "c1", "b", 0.7, "")
	e.Diff("e", "v1", "a", "b")
	st := e.Stats()
	if st.Sets != 1 || st.TotalAdds != 1 || st.TotalFreezes != 1 || st.TotalRecords != 2 || st.TotalDiffs != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkEvalSetDiff(b *testing.B) {
	e := NewEvalSetStore()
	e.Create("e")
	for i := 0; i < 200; i++ {
		e.AddCase("e", "c"+itoaBench(i), "x", "")
	}
	e.Freeze("e", "v1")
	for i := 0; i < 200; i++ {
		id := "c" + itoaBench(i)
		e.Record("e", "v1", id, "a", 0.5, "")
		e.Record("e", "v1", id, "b", 0.6, "")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Diff("e", "v1", "a", "b")
	}
}
