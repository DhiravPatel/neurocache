package llmstack

import (
	"strings"
	"testing"
)

func TestShrinkWhitespaceCollapses(t *testing.T) {
	s := NewPromptShrinker(nil)
	r := s.Shrink("hello   world\n\n\nfoo\tbar", ShrinkOpts{Strategy: "whitespace"})
	if r.Text != "hello world foo bar" {
		t.Fatalf("got %q", r.Text)
	}
	if r.ShrunkChars >= r.OriginalChars {
		t.Fatalf("whitespace should shrink: %d -> %d", r.OriginalChars, r.ShrunkChars)
	}
}

func TestShrinkStopwordsRemoved(t *testing.T) {
	s := NewPromptShrinker(nil)
	r := s.Shrink("The cat is on the mat", ShrinkOpts{Strategy: "stopwords"})
	// Should drop: the, is, on, the
	if strings.Contains(strings.ToLower(r.Text), "the ") || strings.Contains(r.Text, " on ") {
		t.Fatalf("stopwords leaked: %q", r.Text)
	}
	if !strings.Contains(r.Text, "cat") || !strings.Contains(r.Text, "mat") {
		t.Fatalf("content words lost: %q", r.Text)
	}
}

func TestShrinkStopwordsPreservesIdentifiers(t *testing.T) {
	s := NewPromptShrinker(nil)
	// "is_admin" looks like an identifier; should not be stripped of "is_"
	r := s.Shrink("Check the is_admin flag", ShrinkOpts{Strategy: "stopwords"})
	if !strings.Contains(r.Text, "is_admin") {
		t.Fatalf("identifier lost: %q", r.Text)
	}
}

func TestShrinkStopwordsPreservesNegation(t *testing.T) {
	// "not" / "no" must NOT be in the stopwords list — they flip meaning.
	s := NewPromptShrinker(nil)
	r := s.Shrink("This is not the answer", ShrinkOpts{Strategy: "stopwords"})
	if !strings.Contains(strings.ToLower(r.Text), "not") {
		t.Fatalf("negation lost: %q", r.Text)
	}
}

func TestShrinkTruncate(t *testing.T) {
	s := NewPromptShrinker(nil)
	long := strings.Repeat("word ", 200) // ~1000 chars / ~250 tokens by 4-char approx
	r := s.Shrink(long, ShrinkOpts{Strategy: "truncate", Target: 50})
	if r.ShrunkToks > 50 {
		t.Fatalf("shrunk_toks = %d, want <=50", r.ShrunkToks)
	}
}

func TestShrinkTruncateFromEnd(t *testing.T) {
	s := NewPromptShrinker(nil)
	long := "AAA " + strings.Repeat("word ", 200) + " ZZZ"
	r := s.Shrink(long, ShrinkOpts{Strategy: "truncate", Target: 5, FromEnd: true})
	if !strings.Contains(r.Text, "ZZZ") {
		t.Fatalf("from_end should keep tail: %q", r.Text)
	}
	if strings.Contains(r.Text, "AAA") {
		t.Fatalf("from_end should drop head: %q", r.Text)
	}
}

func TestShrinkAllStrategy(t *testing.T) {
	s := NewPromptShrinker(nil)
	in := "  The  cat is on    the\n\nmat  "
	r := s.Shrink(in, ShrinkOpts{Strategy: "all"})
	if r.Saved == 0 {
		t.Fatalf("all strategy should save tokens: %+v", r)
	}
	if strings.Contains(r.Text, "  ") {
		t.Fatalf("double space leaked: %q", r.Text)
	}
}

func TestShrinkRatioCalculated(t *testing.T) {
	s := NewPromptShrinker(nil)
	r := s.Shrink("the cat is the dog", ShrinkOpts{Strategy: "stopwords"})
	if r.Ratio >= 1.0 {
		t.Fatalf("ratio should be < 1 after stopword removal: %f", r.Ratio)
	}
	if r.Ratio == 0 {
		t.Fatalf("ratio shouldn't be zero")
	}
}

func TestShrinkStatsAdvance(t *testing.T) {
	s := NewPromptShrinker(nil)
	s.Shrink("  hello   world ", ShrinkOpts{Strategy: "whitespace"})
	s.Shrink("the cat is here", ShrinkOpts{Strategy: "stopwords"})
	stats := s.Stats()
	if stats.TotalRuns != 2 {
		t.Fatalf("total_runs = %d", stats.TotalRuns)
	}
	if stats.TotalTokensSaved == 0 {
		t.Fatalf("should have saved some tokens")
	}
}

func TestShrinkEmptyText(t *testing.T) {
	s := NewPromptShrinker(nil)
	r := s.Shrink("", ShrinkOpts{Strategy: "all"})
	if r.Text != "" {
		t.Fatalf("empty in → empty out: %q", r.Text)
	}
}

func TestShrinkNoTokensIncreaseAfterCompression(t *testing.T) {
	// Sanity: shrinkers must never make text LONGER.
	s := NewPromptShrinker(nil)
	inputs := []string{
		"normal sentence",
		"the the the the",
		"AAA BBB CCC",
		"   leading spaces",
		"trailing spaces   ",
	}
	for _, in := range inputs {
		r := s.Shrink(in, ShrinkOpts{Strategy: "all"})
		if r.ShrunkChars > r.OriginalChars {
			t.Errorf("shrink grew input %q -> %q", in, r.Text)
		}
	}
}
