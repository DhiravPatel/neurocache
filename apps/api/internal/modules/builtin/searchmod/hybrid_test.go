package searchmod

import "testing"

func TestBlendRRFFusesBothLegs(t *testing.T) {
	sparse := []SearchHit{
		{DocID: "a", Score: 10, Doc: &Document{ID: "a"}},
		{DocID: "b", Score: 7, Doc: &Document{ID: "b"}},
		{DocID: "c", Score: 3, Doc: &Document{ID: "c"}},
	}
	dense := []SearchHit{
		{DocID: "c", Score: 0.95, Doc: &Document{ID: "c"}},
		{DocID: "d", Score: 0.80, Doc: &Document{ID: "d"}},
		{DocID: "a", Score: 0.40, Doc: &Document{ID: "a"}},
	}
	got := blendHybridScores(sparse, dense, 0.5, 0.5, "rrf")
	if len(got) != 4 {
		t.Fatalf("expected 4 unique docs, got %d", len(got))
	}
	// Doc "a" appears at rank 1 sparse + rank 3 dense → top result.
	// Doc "c" appears at rank 3 sparse + rank 1 dense → second.
	if got[0].DocID != "a" {
		t.Fatalf("rank 1 should be 'a' (top of sparse), got %s", got[0].DocID)
	}
}

func TestBlendMinMaxRespectsWeights(t *testing.T) {
	sparse := []SearchHit{
		{DocID: "a", Score: 10, Doc: &Document{ID: "a"}},
		{DocID: "b", Score: 5, Doc: &Document{ID: "b"}},
	}
	dense := []SearchHit{
		{DocID: "b", Score: 1.0, Doc: &Document{ID: "b"}},
		{DocID: "a", Score: 0.0, Doc: &Document{ID: "a"}},
	}
	// Heavy bias toward dense — "b" wins because dense ranks it first
	// after normalisation (1.0 vs 0.0).
	got := blendHybridScores(sparse, dense, 0.1, 0.9, "minmax")
	if got[0].DocID != "b" {
		t.Fatalf("dense-weighted minmax should rank 'b' first, got %s", got[0].DocID)
	}
}

func TestBlendNoneIsRawWeightedSum(t *testing.T) {
	sparse := []SearchHit{{DocID: "a", Score: 100, Doc: &Document{ID: "a"}}}
	dense := []SearchHit{{DocID: "a", Score: 0.5, Doc: &Document{ID: "a"}}}
	got := blendHybridScores(sparse, dense, 0.5, 0.5, "none")
	if len(got) != 1 {
		t.Fatalf("got %d rows", len(got))
	}
	want := 0.5*100 + 0.5*0.5
	if got[0].Score != want {
		t.Fatalf("none mode: got %v, want %v", got[0].Score, want)
	}
}

func TestMinMaxNormalizeEdgeCases(t *testing.T) {
	// Empty input → empty output.
	if got := minMaxNormalize(nil); len(got) != 0 {
		t.Fatalf("empty input should produce empty map, got %v", got)
	}
	// All-equal scores → 0 for everyone (no spread to normalise across).
	hits := []SearchHit{{DocID: "a", Score: 5}, {DocID: "b", Score: 5}}
	out := minMaxNormalize(hits)
	if out["a"] != 0 || out["b"] != 0 {
		t.Fatalf("zero-spread should normalize to 0, got %v", out)
	}
}

func TestBlendRRFEmptyLegsAreSafe(t *testing.T) {
	got := blendHybridScores(nil, nil, 0.5, 0.5, "rrf")
	if len(got) != 0 {
		t.Fatalf("two empty legs should produce empty result, got %v", got)
	}
}
