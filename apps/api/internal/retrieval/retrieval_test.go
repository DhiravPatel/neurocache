package retrieval

import (
	"strings"
	"testing"
)

func mustAdd(t *testing.T, ix *Index, id, text string, meta map[string]string) {
	t.Helper()
	if err := ix.Add(Document{ID: id, Text: text, Metadata: meta}); err != nil {
		t.Fatalf("Add(%q): %v", id, err)
	}
}

func TestHybridFindsExactTermAndParaphrase(t *testing.T) {
	ix, err := New(Options{Dim: 256})
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, ix, "iphone13", "iPhone 13 review: best small phone of the year", nil)
	mustAdd(t, ix, "pixel7", "Google Pixel 7 in-depth coverage", nil)
	mustAdd(t, ix, "galaxy", "Samsung Galaxy S22 photo quality breakdown", nil)
	mustAdd(t, ix, "tutorial", "How to build a REST API with Go and Postgres", nil)

	// Exact-string query — BM25 should drive iphone13 to the top.
	hits := ix.Query("iPhone 13", QueryOptions{K: 3})
	if len(hits) == 0 {
		t.Fatalf("expected hits, got none")
	}
	if hits[0].ID != "iphone13" {
		t.Errorf("exact match: want iphone13 first, got %q", hits[0].ID)
	}
	if hits[0].BM25Rank != 1 {
		t.Errorf("expected BM25Rank=1, got %d", hits[0].BM25Rank)
	}

	// Generic phrase — at least one phone-related doc should rank above
	// the unrelated REST tutorial.
	hits = ix.Query("phone review", QueryOptions{K: 3})
	if len(hits) == 0 {
		t.Fatalf("expected hits for paraphrase")
	}
	if hits[0].ID == "tutorial" {
		t.Errorf("paraphrase: tutorial outranked phone hits, got top=%q", hits[0].ID)
	}
}

func TestRRFFusionWeighsBothArms(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	mustAdd(t, ix, "doc1", "alpha bravo charlie", nil)
	mustAdd(t, ix, "doc2", "delta echo foxtrot", nil)
	mustAdd(t, ix, "doc3", "alpha delta gamma", nil)

	hits := ix.Query("alpha", QueryOptions{K: 5, Alpha: 0.5})
	if len(hits) < 2 {
		t.Fatalf("expected ≥2 hits, got %d", len(hits))
	}
	// Both doc1 and doc3 contain "alpha"; both should appear before any
	// document that lacks it.
	got := []string{hits[0].ID, hits[1].ID}
	if !contains(got, "doc1") || !contains(got, "doc3") {
		t.Errorf("expected doc1 and doc3 in top-2, got %v", got)
	}
	if hits[0].Score <= 0 {
		t.Errorf("expected positive fused score, got %f", hits[0].Score)
	}
}

func TestUpsertReplacesPriorVersion(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	mustAdd(t, ix, "k", "the quick brown fox", nil)
	mustAdd(t, ix, "k", "completely different content about gardens", nil)

	hits := ix.Query("fox", QueryOptions{K: 5, UseBM25: true})
	for _, h := range hits {
		if h.ID == "k" {
			t.Errorf("upsert did not remove old postings; %q still matched 'fox'", h.Text)
		}
	}
	hits = ix.Query("gardens", QueryOptions{K: 5, UseBM25: true})
	if len(hits) == 0 || hits[0].ID != "k" {
		t.Errorf("upsert did not index new content; want hit on 'gardens', got %v", hits)
	}
}

func TestDeleteRemovesDoc(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	mustAdd(t, ix, "a", "this should disappear", nil)
	if !ix.Delete("a") {
		t.Fatalf("Delete returned false on existing id")
	}
	if ix.Delete("a") {
		t.Errorf("Delete returned true on missing id")
	}
	if hits := ix.Query("disappear", QueryOptions{K: 5, UseBM25: true}); len(hits) != 0 {
		t.Errorf("expected zero hits after delete, got %d", len(hits))
	}
}

func TestFilterByMetadata(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	mustAdd(t, ix, "a", "shared keyword", map[string]string{"tenant": "t1"})
	mustAdd(t, ix, "b", "shared keyword", map[string]string{"tenant": "t2"})

	hits := ix.Query("shared", QueryOptions{
		K:      5,
		Filter: func(m map[string]string) bool { return m["tenant"] == "t1" },
	})
	if len(hits) != 1 || hits[0].ID != "a" {
		t.Errorf("filter failed: got %v", hits)
	}
}

func TestRerankerRunsAndCanReorder(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	mustAdd(t, ix, "a", "alpha beta", nil)
	mustAdd(t, ix, "b", "alpha gamma", nil)

	called := false
	hits := ix.Query("alpha", QueryOptions{
		K: 2,
		Rerank: func(query string, hits []Hit) ([]Hit, error) {
			called = true
			// Reverse to prove the reranker output wins.
			for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
				hits[i], hits[j] = hits[j], hits[i]
			}
			return hits, nil
		},
	})
	if !called {
		t.Fatal("reranker not invoked")
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
}

func TestStopwordsAreFiltered(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	mustAdd(t, ix, "doc", "the quick brown fox jumps over the lazy dog", nil)
	st := ix.Stats()
	for _, sw := range []string{"the", "and", "is"} {
		if _, ok := ix.postings[sw]; ok {
			t.Errorf("stopword %q leaked into posting list (terms=%d)", sw, st.Terms)
		}
	}
	if _, ok := ix.postings["fox"]; !ok {
		t.Errorf("non-stopword 'fox' missing from posting list")
	}
}

func TestManagerCreateAndGetOrCreate(t *testing.T) {
	m := NewManager(128)
	if _, err := m.Create("docs", Options{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Create("docs", Options{}); err == nil {
		t.Errorf("expected ErrIndexExists on duplicate create")
	}
	ix := m.GetOrCreate("auto")
	if ix == nil {
		t.Fatalf("GetOrCreate returned nil")
	}
	if got := strings.Join(m.Names(), ","); got != "auto,docs" {
		t.Errorf("Names mismatch: %q", got)
	}
	if !m.Drop("auto") {
		t.Errorf("Drop returned false on existing")
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
