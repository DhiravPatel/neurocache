package llmstack

import (
	"strings"
	"testing"
)

func TestGraphExtractFoundedPattern(t *testing.T) {
	g := NewGraphExtractor()
	trips, err := g.Run("kg", "Sam Altman founded OpenAI in 2015.", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tr := range trips {
		if strings.Contains(tr.Subject, "Sam Altman") && tr.Relation == "founded" && strings.Contains(tr.Object, "OpenAI") {
			found = true
		}
	}
	if !found {
		t.Fatalf("triples = %+v", trips)
	}
}

func TestGraphExtractWorksAtPattern(t *testing.T) {
	g := NewGraphExtractor()
	trips, _ := g.Run("kg", "Alice works at Acme Corp.", "")
	found := false
	for _, tr := range trips {
		if tr.Relation == "works_at" {
			found = true
		}
	}
	if !found {
		t.Fatalf("works_at not extracted: %+v", trips)
	}
}

func TestGraphExtractIsOfPattern(t *testing.T) {
	g := NewGraphExtractor()
	trips, _ := g.Run("kg", "Sundar Pichai is the CEO of Google.", "")
	found := false
	for _, tr := range trips {
		if strings.HasPrefix(tr.Relation, "is_ceo") && strings.Contains(tr.Object, "Google") {
			found = true
		}
	}
	if !found {
		t.Fatalf("is_X_of not extracted: %+v", trips)
	}
}

func TestGraphExtractMemoizes(t *testing.T) {
	g := NewGraphExtractor()
	g.Run("kg", "Alice founded Beta Co.", "")
	trips2, _ := g.Run("kg", "Alice founded Beta Co.", "")
	if len(trips2) != 0 {
		t.Fatalf("memoize failed: %+v", trips2)
	}
	s := g.Stats()
	if s.TotalDupes != 1 {
		t.Fatalf("dupes = %d", s.TotalDupes)
	}
}

func TestGraphExtractDedupesWithinSentence(t *testing.T) {
	g := NewGraphExtractor()
	// Same triple expressed twice in the same text
	trips, _ := g.Run("kg", "Bob founded Alpha. Bob founded Alpha.", "")
	count := 0
	for _, tr := range trips {
		if tr.Subject == "Bob" && tr.Relation == "founded" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("within-text dedup failed: %d", count)
	}
}

func TestGraphExtractListMostRecent(t *testing.T) {
	g := NewGraphExtractor()
	g.Run("kg", "Alice founded Acme.", "")
	g.Run("kg", "Bob founded Beta.", "")
	g.Run("kg", "Carol founded Gamma.", "")
	rows, _ := g.List("kg", 0)
	if len(rows) < 3 {
		t.Fatalf("list short: %+v", rows)
	}
}

func TestGraphExtractListLimit(t *testing.T) {
	g := NewGraphExtractor()
	for i := 0; i < 10; i++ {
		g.Run("kg", "Person"+itoaBench(i)+" founded Co"+itoaBench(i)+".", "")
	}
	rows, _ := g.List("kg", 3)
	if len(rows) != 3 {
		t.Fatalf("limit = %d", len(rows))
	}
}

func TestGraphExtractSourcesTracked(t *testing.T) {
	g := NewGraphExtractor()
	g.Run("kg", "Alice founded A.", "doc-1")
	g.Run("kg", "Bob founded B.", "doc-2")
	srcs := g.Sources("kg")
	if len(srcs) != 2 {
		t.Fatalf("sources = %v", srcs)
	}
}

func TestGraphExtractForget(t *testing.T) {
	g := NewGraphExtractor()
	g.Run("a", "Alice founded X.", "")
	g.Run("b", "Bob founded Y.", "")
	if g.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if g.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestGraphExtractStats(t *testing.T) {
	g := NewGraphExtractor()
	g.Run("kg", "Alice founded Acme.", "")
	g.Run("kg", "Alice founded Acme.", "") // dupe
	s := g.Stats()
	if s.TotalRuns != 2 || s.TotalDupes != 1 || s.TotalTriples < 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestGraphExtractRejectsBadInput(t *testing.T) {
	g := NewGraphExtractor()
	if _, err := g.Run("", "x", ""); err == nil {
		t.Fatal("empty graph should fail")
	}
	if _, err := g.Run("g", "", ""); err == nil {
		t.Fatal("empty text should fail")
	}
}
