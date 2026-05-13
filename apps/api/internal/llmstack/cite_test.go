package llmstack

import (
	"testing"
)

func TestCiteExtractDefaultPattern(t *testing.T) {
	c := NewCitationExtractor()
	cites, err := c.Extract("Per [1] and [2], the answer is 42. [Source-A] disagrees.", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cites) != 3 {
		t.Fatalf("got %d cites, want 3: %s", len(cites), CitationsToText(cites))
	}
	if cites[0].Label != "1" || cites[1].Label != "2" || cites[2].Label != "Source-A" {
		t.Fatalf("labels = %s", CitationsToText(cites))
	}
}

func TestCiteExtractCustomPattern(t *testing.T) {
	c := NewCitationExtractor()
	cites, err := c.Extract("See <cite:doc1/> and <cite:doc2/>", `<cite:([a-z0-9]+)/>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cites) != 2 {
		t.Fatalf("got %d, want 2", len(cites))
	}
	if cites[0].Label != "doc1" {
		t.Fatalf("label = %s", cites[0].Label)
	}
}

func TestCiteExtractBadPattern(t *testing.T) {
	c := NewCitationExtractor()
	if _, err := c.Extract("x", "[unclosed"); err == nil {
		t.Fatal("expected error for bad regex")
	}
}

func TestCiteResolveByID(t *testing.T) {
	c := NewCitationExtractor()
	resolved, err := c.Resolve("As [wiki] notes, Paris is the capital. [missing] is invalid.",
		"",
		map[string]string{"wiki": "Wikipedia article on Paris"},
		[]string{"wiki"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Fatalf("got %d, want 2", len(resolved))
	}
	if !resolved[0].Valid || resolved[0].SourceText != "Wikipedia article on Paris" {
		t.Fatalf("first should resolve: %+v", resolved[0])
	}
	if resolved[1].Valid {
		t.Fatalf("'[missing]' should be invalid: %+v", resolved[1])
	}
}

func TestCiteResolveByPosition(t *testing.T) {
	c := NewCitationExtractor()
	resolved, err := c.Resolve("Per [1] and [2], it works.",
		"",
		map[string]string{"src-a": "first source", "src-b": "second source"},
		[]string{"src-a", "src-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved[0].Valid || resolved[0].SourceText != "first source" {
		t.Fatalf("[1] should resolve to first source: %+v", resolved[0])
	}
	if !resolved[1].Valid || resolved[1].SourceText != "second source" {
		t.Fatalf("[2] should resolve to second source: %+v", resolved[1])
	}
}

func TestCiteValidateAllPass(t *testing.T) {
	c := NewCitationExtractor()
	r, err := c.Validate("Per [1] and [src-b], it works.",
		"",
		map[string]string{"src-a": "first", "src-b": "second"},
		[]string{"src-a", "src-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Fatalf("should be valid: %+v", r)
	}
	if r.ValidN != 2 || r.InvalidN != 0 {
		t.Fatalf("counts: %+v", r)
	}
}

func TestCiteValidateInvalidLabels(t *testing.T) {
	c := NewCitationExtractor()
	r, _ := c.Validate("Per [1] and [imaginary], it works.",
		"",
		map[string]string{"a": "first"},
		[]string{"a"})
	if r.Valid {
		t.Fatal("should be invalid (imaginary label)")
	}
	if r.InvalidN != 1 {
		t.Fatalf("invalid_n = %d", r.InvalidN)
	}
	if r.InvalidLabels[0] != "[imaginary]" {
		t.Fatalf("invalid_labels = %v", r.InvalidLabels)
	}
}

func TestCiteValidateUnreferencedSources(t *testing.T) {
	c := NewCitationExtractor()
	r, _ := c.Validate("Only [1] is used.",
		"",
		map[string]string{"a": "first", "b": "second", "c": "third"},
		[]string{"a", "b", "c"})
	if !r.Valid {
		t.Fatal("should be valid (citations resolve)")
	}
	if len(r.UnreferencedIDs) != 2 {
		t.Fatalf("unreferenced = %v, want 2", r.UnreferencedIDs)
	}
	if r.UnreferencedIDs[0] != "b" || r.UnreferencedIDs[1] != "c" {
		t.Fatalf("unreferenced = %v", r.UnreferencedIDs)
	}
}

func TestCiteStatsAdvance(t *testing.T) {
	c := NewCitationExtractor()
	c.Extract("Per [1] and [2], it works.", "")
	c.Resolve("Per [1] and [bad], it works.", "",
		map[string]string{"a": "x"}, []string{"a"})
	s := c.Stats()
	if s.TotalExtracts != 2 { // Resolve also calls Extract internally
		t.Fatalf("extracts = %d", s.TotalExtracts)
	}
	if s.TotalCitations < 4 {
		t.Fatalf("total citations = %d", s.TotalCitations)
	}
	if s.TotalInvalid < 1 {
		t.Fatalf("total invalid = %d", s.TotalInvalid)
	}
}

func TestCiteExtractEmpty(t *testing.T) {
	c := NewCitationExtractor()
	cites, err := c.Extract("no citations here", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cites) != 0 {
		t.Fatalf("expected 0 cites, got %d", len(cites))
	}
}
