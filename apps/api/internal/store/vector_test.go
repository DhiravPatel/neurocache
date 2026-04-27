package store

import (
	"testing"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

func defaultOpts(dim int) vectorindex.Options {
	return vectorindex.Options{Algo: vectorindex.AlgoFlat, Dim: dim, Metric: vectorindex.MetricL2}
}

// ── VAdd / VRem / VCard / VDim ────────────────────────────────────

func TestVAddCreatesAndCounts(t *testing.T) {
	s := New()
	n, err := s.VAdd("v", "a", []float32{1, 0, 0}, defaultOpts(3))
	if err != nil || n != 1 {
		t.Fatalf("VAdd: n=%d err=%v", n, err)
	}
	// Re-add same id replaces vector → returns 0.
	n, _ = s.VAdd("v", "a", []float32{2, 0, 0}, defaultOpts(3))
	if n != 0 {
		t.Fatalf("re-add should return 0, got %d", n)
	}
	if c, _ := s.VCard("v"); c != 1 {
		t.Fatalf("card after re-add = %d, want 1", c)
	}
}

func TestVAddOnExistingKeyIgnoresOpts(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 0}, defaultOpts(2))
	// Second call with a different metric — opts are ignored on the
	// existing key; the original L2 metric should remain.
	cosOpts := vectorindex.Options{Algo: vectorindex.AlgoFlat, Dim: 2, Metric: vectorindex.MetricCosine}
	if _, err := s.VAdd("v", "b", []float32{0, 1}, cosOpts); err != nil {
		t.Fatal(err)
	}
	info, _, _ := s.VInfo("v")
	if info.Metric != "L2" {
		t.Fatalf("metric should be unchanged after second VADD, got %s", info.Metric)
	}
}

func TestVAddDimMismatchRejected(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 0, 0}, defaultOpts(3))
	if _, err := s.VAdd("v", "b", []float32{1, 0}, vectorindex.Options{Dim: 2}); err == nil {
		t.Fatal("VADD with mismatched dim should error")
	}
}

func TestVRemPartial(t *testing.T) {
	s := New()
	for _, id := range []string{"a", "b", "c"} {
		s.VAdd("v", id, []float32{1, 0}, defaultOpts(2))
	}
	n, _ := s.VRem("v", "b", "ghost", "c")
	if n != 2 {
		t.Fatalf("VRem should report 2 removals, got %d", n)
	}
	if c, _ := s.VCard("v"); c != 1 {
		t.Fatalf("card after VRem = %d, want 1", c)
	}
}

func TestVCardOnMissingKey(t *testing.T) {
	s := New()
	if c, err := s.VCard("nope"); err != nil || c != 0 {
		t.Fatalf("missing key: c=%d err=%v", c, err)
	}
}

func TestVDimReturnsConfiguredDim(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 2, 3, 4}, defaultOpts(4))
	d, ok, _ := s.VDim("v")
	if !ok || d != 4 {
		t.Fatalf("VDim: d=%d ok=%v", d, ok)
	}
}

// ── VSim ──────────────────────────────────────────────────────────

func TestVSimRanksByDistance(t *testing.T) {
	s := New()
	s.VAdd("v", "close", []float32{1, 0}, defaultOpts(2))
	s.VAdd("v", "mid", []float32{0.5, 0.5}, defaultOpts(2))
	s.VAdd("v", "far", []float32{-1, 0}, defaultOpts(2))
	res, err := s.VSim("v", []float32{1, 0}, 3)
	if err != nil || len(res) != 3 {
		t.Fatalf("VSim: len=%d err=%v", len(res), err)
	}
	if res[0].ID != "close" {
		t.Fatalf("expected 'close' first, got %s", res[0].ID)
	}
}

func TestVSimEmptyKey(t *testing.T) {
	s := New()
	res, err := s.VSim("nope", []float32{1, 0}, 5)
	if err != nil || res != nil {
		t.Fatalf("missing key: res=%v err=%v", res, err)
	}
}

// ── VEmb ──────────────────────────────────────────────────────────

func TestVEmbReturnsCopy(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 2, 3}, defaultOpts(3))
	got, ok, _ := s.VEmb("v", "a")
	if !ok || got[0] != 1 {
		t.Fatalf("VEmb: got=%v ok=%v", got, ok)
	}
	got[0] = 999 // mutate the copy
	got2, _, _ := s.VEmb("v", "a")
	if got2[0] != 1 {
		t.Fatalf("mutation leaked into stored vector: %v", got2)
	}
}

// ── attributes ────────────────────────────────────────────────────

func TestVAttrLifecycle(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 2, 3}, defaultOpts(3))
	ok, _ := s.VSetAttr("v", "a", `{"label":"x"}`)
	if !ok {
		t.Fatal("VSetAttr should succeed")
	}
	got, present, _ := s.VGetAttr("v", "a")
	if !present || got != `{"label":"x"}` {
		t.Fatalf("VGetAttr: got=%q present=%v", got, present)
	}
	ok, _ = s.VDelAttr("v", "a")
	if !ok {
		t.Fatal("VDelAttr should report true")
	}
}

func TestVRemDropsAttr(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 2, 3}, defaultOpts(3))
	s.VSetAttr("v", "a", `{"x":1}`)
	s.VRem("v", "a")
	if _, present, _ := s.VGetAttr("v", "a"); present {
		t.Fatal("attr must be cleared when its member is removed")
	}
}

// ── snapshot roundtrip ────────────────────────────────────────────

func TestVectorSetExportRestore(t *testing.T) {
	src := New()
	src.VAdd("v", "a", []float32{1, 0, 0}, defaultOpts(3))
	src.VAdd("v", "b", []float32{0, 1, 0}, defaultOpts(3))
	src.VSetAttr("v", "a", `{"k":1}`)
	exported := src.Export()

	dst := New()
	dst.Restore(exported)
	if c, _ := dst.VCard("v"); c != 2 {
		t.Fatalf("card after restore = %d, want 2", c)
	}
	got, ok, _ := dst.VEmb("v", "a")
	if !ok || got[0] != 1 {
		t.Fatalf("VEmb after restore: got=%v ok=%v", got, ok)
	}
	attr, present, _ := dst.VGetAttr("v", "a")
	if !present || attr != `{"k":1}` {
		t.Fatalf("attr after restore: %q present=%v", attr, present)
	}
	info, _, _ := dst.VInfo("v")
	if info.Algo != "FLAT" || info.Dim != 3 {
		t.Fatalf("config after restore: %+v", info)
	}
}

func TestVectorSetCopyRoundtrip(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 2, 3}, defaultOpts(3))
	if ok, _ := s.Copy("v", "v2", false); !ok {
		t.Fatal("Copy should succeed")
	}
	got, ok, _ := s.VEmb("v2", "a")
	if !ok || got[1] != 2 {
		t.Fatalf("VEmb on copied key: got=%v ok=%v", got, ok)
	}
}

// ── lifecycle ─────────────────────────────────────────────────────

func TestVectorSetSurvivesEmpty(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 0}, defaultOpts(2))
	s.VRem("v", "a")
	if s.Type("v") != TypeVector {
		t.Fatal("vector set should remain after last VREM (config is precious)")
	}
}

func TestVectorSetTTLExpires(t *testing.T) {
	s := New()
	s.VAdd("v", "a", []float32{1, 0}, defaultOpts(2))
	s.Expire("v", 50*time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	// Type read with expired entry should report TypeNone.
	if got := s.Type("v"); got != TypeNone {
		t.Fatalf("expected expired vector set, got %s", got)
	}
}

// ── VRandMember + VScan ───────────────────────────────────────────

func TestVRandMemberCountModes(t *testing.T) {
	s := New()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		s.VAdd("v", id, []float32{1, 2}, defaultOpts(2))
	}
	// Single
	out, _ := s.VRandMember("v", 0)
	if len(out) != 1 {
		t.Fatalf("single mode returned %d ids", len(out))
	}
	// Unique cap
	out, _ = s.VRandMember("v", 100)
	if len(out) != 5 {
		t.Fatalf("unique mode returned %d ids", len(out))
	}
	// With replacement
	out, _ = s.VRandMember("v", -10)
	if len(out) != 10 {
		t.Fatalf("replacement mode returned %d ids", len(out))
	}
}

func TestVScanIteratesEverything(t *testing.T) {
	s := New()
	want := map[string]bool{}
	for i := 0; i < 30; i++ {
		id := "id" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		want[id] = true
		s.VAdd("v", id, []float32{1, 2}, defaultOpts(2))
	}
	got := map[string]bool{}
	cursor := 0
	for {
		next, page, err := s.VScan("v", cursor, "", 7)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range page {
			got[id] = true
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	if len(got) != len(want) {
		t.Fatalf("VSCAN missed ids: got %d, want %d", len(got), len(want))
	}
}
