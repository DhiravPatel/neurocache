package llmstack

import (
	"testing"
)

func TestAutocompleteAddAndSuggest(t *testing.T) {
	a := NewAutocomplete()
	a.Add("commands", "kill server", 100)
	a.Add("commands", "kill process", 50)
	a.Add("commands", "killall", 25)
	a.Add("commands", "list files", 75)
	hits := a.Suggest("commands", "kil", 5)
	if len(hits) != 3 {
		t.Fatalf("expected 3 kil* matches, got %d", len(hits))
	}
	// score=100 wins
	if hits[0].Phrase != "kill server" {
		t.Fatalf("top = %s, want 'kill server'", hits[0].Phrase)
	}
}

func TestAutocompleteCaseInsensitive(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "Apple", 10)
	a.Add("c", "apricot", 5)
	hits := a.Suggest("c", "AP", 10)
	if len(hits) != 2 {
		t.Fatalf("case-insensitive prefix should match both, got %d", len(hits))
	}
}

func TestAutocompletePreservesOriginalCasing(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "iPhone", 10)
	hits := a.Suggest("c", "i", 1)
	if hits[0].Phrase != "iPhone" {
		t.Fatalf("original casing lost: %s", hits[0].Phrase)
	}
}

func TestAutocompleteUpdateExisting(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "test", 1)
	a.Add("c", "test", 100) // update score
	hits := a.Suggest("c", "te", 1)
	if hits[0].Score != 100 {
		t.Fatalf("score not updated: %f", hits[0].Score)
	}
	if n, _ := a.Size("c"); n != 1 {
		t.Fatalf("duplicate inserted: size=%d", n)
	}
}

func TestAutocompleteDel(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "alpha", 1)
	a.Add("c", "beta", 1)
	if !a.Del("c", "alpha") {
		t.Fatal("del should return true")
	}
	if a.Del("c", "alpha") {
		t.Fatal("del on missing should return false")
	}
	if n, _ := a.Size("c"); n != 1 {
		t.Fatalf("size after del = %d", n)
	}
}

func TestAutocompleteForget(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "a", 1)
	a.Add("c", "b", 1)
	if n := a.Forget("c"); n != 2 {
		t.Fatalf("forget = %d, want 2", n)
	}
	if _, ok := a.Size("c"); ok {
		t.Fatal("size on forgotten list should be false")
	}
}

func TestAutocompleteEmptyPrefix(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "alpha", 5)
	a.Add("c", "beta", 10)
	hits := a.Suggest("c", "", 10)
	if len(hits) != 2 {
		t.Fatalf("empty prefix should match all, got %d", len(hits))
	}
}

func TestAutocompleteNoMatch(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "alpha", 1)
	hits := a.Suggest("c", "zzz", 10)
	if len(hits) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(hits))
	}
}

func TestAutocompleteK(t *testing.T) {
	a := NewAutocomplete()
	for i := 0; i < 100; i++ {
		a.Add("c", string(rune('a'+i%26))+string(rune('a'+i/26)), float64(i))
	}
	hits := a.Suggest("c", "a", 5)
	if len(hits) > 5 {
		t.Fatalf("K=5 honored: got %d", len(hits))
	}
}

func TestAutocompleteAlphabeticalTieBreak(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "zebra", 10)
	a.Add("c", "apple", 10)
	a.Add("c", "mango", 10)
	hits := a.Suggest("c", "", 10)
	// Same score → alphabetical
	if hits[0].Phrase != "apple" || hits[2].Phrase != "zebra" {
		t.Fatalf("tie-break not alphabetical: %v", hits)
	}
}

func TestAutocompleteStatsAdvance(t *testing.T) {
	a := NewAutocomplete()
	a.Add("c", "x", 1)
	a.Suggest("c", "x", 1)
	a.Suggest("c", "z", 1)
	s := a.Stats()
	if s.TotalAdds != 1 || s.TotalSuggests != 2 || s.TotalHits != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestAutocompleteEmptyPhraseRejected(t *testing.T) {
	a := NewAutocomplete()
	if err := a.Add("c", "", 1); err == nil {
		t.Fatal("empty phrase should fail")
	}
	if err := a.Add("", "x", 1); err == nil {
		t.Fatal("empty list_id should fail")
	}
}
