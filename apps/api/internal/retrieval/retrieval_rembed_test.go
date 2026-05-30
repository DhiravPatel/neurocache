package retrieval

import "testing"

// TestRetrievalRembedSwapRollback exercises the dense-arm rebuild: stage a
// replacement index at a new dim, commit it, then stage + roll back another.
func TestRetrievalRembedSwapRollback(t *testing.T) {
	ix, err := New(Options{Dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Add(Document{ID: "d1", Text: "hello world"}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Add(Document{ID: "d2", Text: "foo bar baz"}); err != nil {
		t.Fatal(err)
	}

	docs, dim := ix.RembedStat()
	if docs != 2 || dim != 8 {
		t.Fatalf("RembedStat = (%d,%d), want (2,8)", docs, dim)
	}

	var lastDone, lastTotal int
	if err := ix.StageRembed(16, 1, func(done, total int) { lastDone, lastTotal = done, total }, nil); err != nil {
		t.Fatal(err)
	}
	if lastDone != 2 || lastTotal != 2 {
		t.Fatalf("progress ended at %d/%d, want 2/2", lastDone, lastTotal)
	}
	if staged, sdim := ix.RembedStaged(); !staged || sdim != 16 {
		t.Fatalf("RembedStaged = (%v,%d), want (true,16)", staged, sdim)
	}

	if !ix.SwapRembed() {
		t.Fatal("SwapRembed returned false")
	}
	if _, dim := ix.RembedStat(); dim != 16 {
		t.Fatalf("post-swap dim = %d, want 16", dim)
	}
	// The index keeps serving after the swap.
	if hits := ix.Query("hello world", QueryOptions{K: 1}); len(hits) == 0 {
		t.Fatal("no hits after swap")
	}

	// Rollback path leaves the live dim untouched.
	if err := ix.StageRembed(32, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !ix.RollbackRembed() {
		t.Fatal("RollbackRembed returned false")
	}
	if _, dim := ix.RembedStat(); dim != 16 {
		t.Fatalf("post-rollback dim = %d, want 16 (unchanged)", dim)
	}
	if staged, _ := ix.RembedStaged(); staged {
		t.Fatal("shadow still staged after rollback")
	}
}

// TestRetrievalRembedPreservesDocsAddedDuringStaging — a doc added after the
// shadow was built (so it reached only the live dense arm) must be reconciled
// into the shadow at swap, not left vector-unsearchable.
func TestRetrievalRembedPreservesDocsAddedDuringStaging(t *testing.T) {
	ix, err := New(Options{Dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Add(Document{ID: "d1", Text: "hello world"}); err != nil {
		t.Fatal(err)
	}
	// Stage at a new dim from the snapshot {d1}.
	if err := ix.StageRembed(16, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Add d2 DURING staging — only the live arm sees it.
	if err := ix.Add(Document{ID: "d2", Text: "foo bar baz qux"}); err != nil {
		t.Fatal(err)
	}
	if !ix.SwapRembed() {
		t.Fatal("SwapRembed returned false")
	}
	// d2 must be vector-searchable from the promoted dense arm.
	hits := ix.Query("foo bar baz qux", QueryOptions{K: 5, Alpha: 1, UseVect: true})
	found := false
	for _, h := range hits {
		if h.ID == "d2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("d2 (added during staging) not vector-searchable after swap: %v", hits)
	}
}
