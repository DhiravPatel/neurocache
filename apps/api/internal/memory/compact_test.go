package memory

import "testing"

// TestCompactPlanApplyExpand — the full reversible cycle: fold similar
// episodic memories into one semantic summary, then restore them.
func TestCompactPlanApplyExpand(t *testing.T) {
	s := New(384)
	for i := 0; i < 4; i++ {
		if _, _, err := s.AddWithOptions("u", "the user prefers a dark mode interface", AddOptions{Layer: LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCompactor(s)

	// PLAN is read-only: it proposes a fold but mutates nothing.
	plan := c.Plan("u", CompactOptions{MinSize: 3})
	if len(plan.Clusters) == 0 || plan.Entries < 3 {
		t.Fatalf("plan = %+v, want a cluster of the 4 similar entries", plan)
	}
	if len(s.ListByLayer("u", LayerEpisodic)) != 4 {
		t.Fatal("PLAN mutated the store")
	}

	// APPLY writes the summary and (Drop) deletes the originals.
	res := c.Apply("u", CompactOptions{MinSize: 3, Drop: true})
	if len(res.Summaries) == 0 || res.Dropped == 0 {
		t.Fatalf("apply = %+v, want a summary + dropped originals", res)
	}
	if len(s.ListByLayer("u", LayerEpisodic)) != 0 {
		t.Fatal("episodic originals not folded")
	}
	if len(s.ListByLayer("u", LayerSemantic)) == 0 {
		t.Fatal("no semantic summary written")
	}
	sid := res.Summaries[0].SummaryID
	if len(res.Summaries[0].SourceIDs) != 4 {
		t.Fatalf("summary records %d source ids, want 4", len(res.Summaries[0].SourceIDs))
	}

	// EXPAND restores the originals and removes the summary.
	er, ok := c.Expand("u", sid)
	if !ok {
		t.Fatal("expand failed for a known summary")
	}
	if er.Restored != 4 {
		t.Fatalf("restored %d, want 4", er.Restored)
	}
	if len(s.ListByLayer("u", LayerEpisodic)) != 4 {
		t.Fatal("originals not restored to the episodic layer")
	}
	// Expanding an unknown summary is a clean miss.
	if _, ok := c.Expand("u", "nope"); ok {
		t.Fatal("expand of an unknown summary should report not-found")
	}

	st := c.Stats()
	if st.TotalApplied != 1 || st.TotalExpanded != 1 {
		t.Fatalf("stats = %+v, want applied=1 expanded=1", st)
	}
}

// TestCompactNoClustersBelowMinSize — too few similar entries fold nothing.
func TestCompactNoClustersBelowMinSize(t *testing.T) {
	s := New(384)
	_, _, _ = s.AddWithOptions("u", "alpha distinct one", AddOptions{Layer: LayerEpisodic})
	_, _, _ = s.AddWithOptions("u", "beta distinct two", AddOptions{Layer: LayerEpisodic})
	c := NewCompactor(s)
	if res := c.Apply("u", CompactOptions{MinSize: 3}); len(res.Summaries) != 0 {
		t.Fatalf("apply folded %d clusters, want 0 (below MinSize)", len(res.Summaries))
	}
}
