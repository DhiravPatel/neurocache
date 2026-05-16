package llmstack

import (
	"testing"
)

func TestEmbMigrateStartAndWrite(t *testing.T) {
	e := NewEmbMigrator()
	if err := e.Start("m1", "minilm", "bge-small"); err != nil {
		t.Fatal(err)
	}
	err := e.Write("m1", "doc-1",
		[]float64{1, 0, 0}, []float64{0, 1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := e.Status("m1")
	if !ok {
		t.Fatal("status returned false")
	}
	if s.RowsWritten != 1 {
		t.Fatalf("rows = %d", s.RowsWritten)
	}
	if s.OldDim != 3 || s.NewDim != 5 {
		t.Fatalf("dims = %d / %d", s.OldDim, s.NewDim)
	}
}

func TestEmbMigrateStartRejectsDuplicate(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m1", "a", "b")
	if err := e.Start("m1", "a", "b"); err == nil {
		t.Fatal("duplicate start should fail (must ABORT first)")
	}
}

func TestEmbMigrateWriteRejectsBadInput(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "a", "b")
	if err := e.Write("m", "", []float64{1}, []float64{1}); err == nil {
		t.Fatal("empty row_id should fail")
	}
	if err := e.Write("m", "r", nil, []float64{1}); err == nil {
		t.Fatal("empty oldVec should fail")
	}
	if err := e.Write("m", "r", []float64{0}, []float64{0}); err == nil {
		t.Fatal("zero-norm vec should fail")
	}
}

func TestEmbMigrateWriteDimMismatch(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "a", "b")
	e.Write("m", "r1", []float64{1, 0}, []float64{0, 1})
	if err := e.Write("m", "r2", []float64{1, 0, 0}, []float64{0, 1}); err == nil {
		t.Fatal("dim mismatch should fail")
	}
}

func TestEmbMigrateCompareReturnsBothTopK(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "old", "new")
	// Setup: 5 docs with both old and new vectors
	for i, vals := range [][2][]float64{
		{{1, 0, 0}, {1, 0, 0, 0}},
		{{0.9, 0.1, 0}, {0.9, 0.1, 0, 0}},
		{{0, 1, 0}, {0, 1, 0, 0}},
		{{0, 0, 1}, {0, 0, 1, 0}},
		{{0.5, 0.5, 0}, {0.5, 0.5, 0, 0}},
	} {
		e.Write("m", string(rune('a'+i)), vals[0], vals[1])
	}
	r, ok := e.Compare("m", []float64{1, 0, 0}, []float64{1, 0, 0, 0}, 3)
	if !ok {
		t.Fatal("compare returned false")
	}
	if len(r.OldTopK) != 3 || len(r.NewTopK) != 3 {
		t.Fatalf("topk lens = %d / %d", len(r.OldTopK), len(r.NewTopK))
	}
	// Top hit in both should be "a" (identical-vector match)
	if r.OldTopK[0].RowID != "a" || r.NewTopK[0].RowID != "a" {
		t.Fatalf("top hits = %s / %s", r.OldTopK[0].RowID, r.NewTopK[0].RowID)
	}
	if r.OverlapAtK == 0 {
		t.Fatal("overlap should be > 0 when vectors are similar across models")
	}
}

func TestEmbMigrateCompareDimMismatch(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "old", "new")
	e.Write("m", "r1", []float64{1, 0, 0}, []float64{1, 0, 0, 0})
	r, ok := e.Compare("m", []float64{1, 0}, []float64{1, 0, 0, 0}, 5)
	if ok {
		t.Fatalf("dim mismatch should fail, got %+v", r)
	}
}

func TestEmbMigrateCutover(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "a", "b")
	if !e.Cutover("m") {
		t.Fatal("cutover should return true")
	}
	// Idempotent
	if !e.Cutover("m") {
		t.Fatal("second cutover should also return true")
	}
	s, _ := e.Status("m")
	if !s.CutOver {
		t.Fatal("status should reflect cutover")
	}
}

func TestEmbMigrateAbort(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "a", "b")
	if !e.Abort("m") {
		t.Fatal("abort should return true")
	}
	if e.Abort("m") {
		t.Fatal("abort on missing should return false")
	}
	// After abort, START with same id should succeed
	if err := e.Start("m", "a", "c"); err != nil {
		t.Fatalf("start after abort should succeed: %v", err)
	}
}

func TestEmbMigrateList(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m1", "a", "b")
	e.Start("m2", "c", "d")
	rows := e.List()
	if len(rows) != 2 {
		t.Fatalf("list = %d", len(rows))
	}
}

func TestEmbMigrateRejectsBadStart(t *testing.T) {
	e := NewEmbMigrator()
	if err := e.Start("", "a", "b"); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := e.Start("m", "", "b"); err == nil {
		t.Fatal("empty from_model should fail")
	}
}

func TestEmbMigrateStatsAdvance(t *testing.T) {
	e := NewEmbMigrator()
	e.Start("m", "a", "b")
	e.Write("m", "r", []float64{1, 0}, []float64{1, 0, 0})
	e.Compare("m", []float64{1, 0}, []float64{1, 0, 0}, 1)
	e.Cutover("m")
	s := e.Stats()
	if s.TotalStarts != 1 || s.TotalWrites != 1 || s.TotalCompares != 1 || s.TotalCutovers != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
