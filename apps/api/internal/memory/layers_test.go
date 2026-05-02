package memory

import (
	"testing"
	"time"
)

func TestAddWithOptionsRoutesToLayer(t *testing.T) {
	s := New(384)
	e, isNew, err := s.AddWithOptions("u1", "I prefer dark mode", AddOptions{
		Layer:      LayerSemantic,
		Importance: 0.8,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !isNew {
		t.Errorf("expected isNew=true on first write")
	}
	if e.Layer != LayerSemantic {
		t.Errorf("layer mismatch: %q", e.Layer)
	}
	if e.Importance != 0.8 {
		t.Errorf("importance not preserved: %f", e.Importance)
	}
	if e.Meta["layer"] != "semantic" {
		t.Errorf("meta['layer'] mismatch: %q", e.Meta["layer"])
	}
}

func TestDedupOnSimilarText(t *testing.T) {
	s := New(384)
	first, _, _ := s.AddWithOptions("u1", "the user prefers dark mode", AddOptions{
		Layer:          LayerSemantic,
		DedupThreshold: 0.5,
	})
	second, isNew, _ := s.AddWithOptions("u1", "the user prefers dark mode", AddOptions{
		Layer:          LayerSemantic,
		DedupThreshold: 0.5,
		Importance:     0.9,
	})
	if isNew {
		t.Errorf("expected dedup hit, got new write (id=%s vs %s)", first.ID, second.ID)
	}
	if second.ID != first.ID {
		t.Errorf("dedup returned different id: %s vs %s", first.ID, second.ID)
	}
	// Importance should have been pulled up to the higher write.
	if second.Importance < 0.9 {
		t.Errorf("importance not promoted on dedup hit: %f", second.Importance)
	}
}

func TestQueryLayeredScopedToLayer(t *testing.T) {
	s := New(384)
	s.AddWithOptions("u1", "today we shipped the auth refactor", AddOptions{Layer: LayerEpisodic})
	s.AddWithOptions("u1", "the user prefers terse explanations", AddOptions{Layer: LayerSemantic, Importance: 0.9})

	episodic := s.QueryLayered("u1", "auth refactor", LayerQueryOptions{Layer: LayerEpisodic, K: 5})
	if len(episodic) != 1 || episodic[0].Entry.Layer != LayerEpisodic {
		t.Errorf("episodic query leaked or missed: %+v", episodic)
	}

	semantic := s.QueryLayered("u1", "user prefers", LayerQueryOptions{Layer: LayerSemantic, K: 5})
	if len(semantic) != 1 || semantic[0].Entry.Layer != LayerSemantic {
		t.Errorf("semantic query leaked or missed: %+v", semantic)
	}
}

func TestQueryTouchesAccessTracking(t *testing.T) {
	s := New(384)
	e, _, _ := s.AddWithOptions("u1", "the api returns json", AddOptions{Layer: LayerSemantic})
	hits := s.QueryLayered("u1", "api json", LayerQueryOptions{
		Layer: LayerSemantic, K: 5, TouchHits: true,
	})
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	// Re-read to confirm AccessCount was bumped under the lock.
	got, ok := s.byID[e.ID]
	if !ok {
		t.Fatal("entry vanished")
	}
	if got.AccessCount != 1 {
		t.Errorf("expected AccessCount=1, got %d", got.AccessCount)
	}
}

func TestDecayDropsOldUntouchedEntries(t *testing.T) {
	s := New(384)
	old, _, _ := s.AddWithOptions("u1", "stale chatter", AddOptions{Layer: LayerEpisodic})
	// Backdate.
	s.byID[old.ID].CreatedAt = time.Now().Add(-90 * 24 * time.Hour)

	res := s.Decay("u1", DecayOptions{
		Layer:  LayerEpisodic,
		MaxAge: 30 * 24 * time.Hour,
	})
	if res.Dropped != 1 {
		t.Errorf("expected 1 drop, got %d (scanned=%d)", res.Dropped, res.Scanned)
	}
	if _, ok := s.byID[old.ID]; ok {
		t.Errorf("decayed entry still present")
	}
}

func TestDecayDryRunDoesNotDelete(t *testing.T) {
	s := New(384)
	old, _, _ := s.AddWithOptions("u1", "old thing", AddOptions{Layer: LayerEpisodic})
	s.byID[old.ID].CreatedAt = time.Now().Add(-90 * 24 * time.Hour)

	res := s.Decay("u1", DecayOptions{
		Layer:  LayerEpisodic,
		MaxAge: 30 * 24 * time.Hour,
		DryRun: true,
	})
	if res.Dropped != 1 {
		t.Errorf("dry run should report 1 drop, got %d", res.Dropped)
	}
	if _, ok := s.byID[old.ID]; !ok {
		t.Errorf("dry run actually deleted entry")
	}
}

func TestConsolidateClustersAndWritesSemantic(t *testing.T) {
	s := New(384)
	for _, line := range []string{
		"the user prefers concise answers",
		"the user prefers concise answers without preamble",
		"the user prefers concise responses",
		"the user prefers brief answers",
	} {
		s.AddWithOptions("u1", line, AddOptions{Layer: LayerEpisodic})
	}
	// Add one outlier that shouldn't cluster.
	s.AddWithOptions("u1", "completely different thing about gardening", AddOptions{Layer: LayerEpisodic})

	res := s.Consolidate(ConsolidateOptions{
		UserID:    "u1",
		Threshold: 0.6,
		MinSize:   3,
		Drop:      true,
	})
	if res.Written < 1 {
		t.Errorf("expected ≥1 cluster written, got %d (clusters=%d)", res.Written, res.Clusters)
	}
	if res.Dropped < 3 {
		t.Errorf("expected ≥3 episodic drops, got %d", res.Dropped)
	}
	semantic := s.ListByLayer("u1", LayerSemantic)
	if len(semantic) < 1 {
		t.Errorf("expected ≥1 semantic entry post-consolidate, got %d", len(semantic))
	}
	if len(semantic) > 0 && len(semantic[0].SourceIDs) < 3 {
		t.Errorf("expected SourceIDs to record cluster members, got %v", semantic[0].SourceIDs)
	}
}

func TestLayerStatsReportsBreakdown(t *testing.T) {
	s := New(384)
	s.AddWithOptions("u1", "a", AddOptions{Layer: LayerEpisodic})
	s.AddWithOptions("u1", "b", AddOptions{Layer: LayerSemantic})
	s.AddWithOptions("u1", "c", AddOptions{Layer: LayerProcedural})
	st := s.LayerStats("u1")
	if st.Episodic != 1 || st.Semantic != 1 || st.Procedural != 1 {
		t.Errorf("LayerStats wrong: %+v", st)
	}
}

func TestInvalidLayerRejected(t *testing.T) {
	s := New(384)
	_, _, err := s.AddWithOptions("u1", "x", AddOptions{Layer: Layer("nonsense")})
	if err == nil {
		t.Error("expected error on invalid layer")
	}
}
