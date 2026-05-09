package llmstack

import (
	"testing"
)

func TestFingerprintCaseAndWhitespaceRobust(t *testing.T) {
	cases := [][2]string{
		{"Hello world", "  hello   WORLD  "},
		{"How does this work?", "how does this work"},
		{"List the top 5 items.", "list the top 5 items"},
		{"Multiple   spaces", "Multiple spaces"},
		{"Mixed\tCASE\nhere", "mixed case here"},
	}
	for _, c := range cases {
		a, b := Fingerprint(c[0]), Fingerprint(c[1])
		if a != b {
			t.Fatalf("expected match: %q -> %s vs %q -> %s", c[0], a, c[1], b)
		}
	}
}

func TestFingerprintDigitRunCollapse(t *testing.T) {
	// Different ID numbers should hash the same so the analytics
	// layer can cluster "lookup user 12345" with "lookup user 99999".
	a := Fingerprint("Lookup user 12345 please")
	b := Fingerprint("Lookup user 99999 please")
	if a != b {
		t.Fatalf("digit-run collapse failed: %s vs %s", a, b)
	}
	// But fingerprints for entirely different prompts must differ.
	c := Fingerprint("Delete record 12345")
	if a == c {
		t.Fatalf("unrelated prompts hashed identically: %s", a)
	}
}

func TestFingerprintURLCollapse(t *testing.T) {
	a := Fingerprint("Summarize https://example.com/foo for me")
	b := Fingerprint("Summarize http://other.io/bar for me")
	if a != b {
		t.Fatalf("URL collapse failed: %s vs %s", a, b)
	}
}

func TestFingerprintDistinctPromptsDistinctHash(t *testing.T) {
	a := Fingerprint("translate this text to French")
	b := Fingerprint("summarize this text in three bullets")
	if a == b {
		t.Fatalf("distinct prompts collided: %s", a)
	}
}

func TestPromptAnalyticsRecordAndCount(t *testing.T) {
	p := NewPromptAnalytics()
	p.Record("How does X work?")
	p.Record("HOW DOES X WORK?")
	p.Record("how does x work")
	p.Record("Different prompt entirely")
	groups := p.Groups(0)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	// First group should be the 3-count "how does x work" cluster.
	if groups[0].Count != 3 {
		t.Fatalf("top group count=%d want=3", groups[0].Count)
	}
	if groups[1].Count != 1 {
		t.Fatalf("second group count=%d want=1", groups[1].Count)
	}
}

func TestPromptAnalyticsTopNLimit(t *testing.T) {
	// Use prompts whose canonical forms differ — "prompt N" all
	// collapse to a single fingerprint due to digit-run normalization
	// (which is correct behavior; the test just needs distinct text).
	p := NewPromptAnalytics()
	prompts := []string{
		"summarize the document",
		"translate to french",
		"extract entities",
		"classify sentiment",
		"answer the question",
		"draft an email",
		"write a story",
		"explain this concept",
		"compare these options",
		"plan a trip",
	}
	for _, q := range prompts {
		p.Record(q)
	}
	rows := p.Groups(3)
	if len(rows) != 3 {
		t.Fatalf("limit=3 but got %d rows", len(rows))
	}
}

func TestPromptAnalyticsSamplePreserved(t *testing.T) {
	p := NewPromptAnalytics()
	original := "Plot revenue for Q3 2026"
	p.Record(original)
	p.Record("PLOT REVENUE FOR Q3 2026") // hits same group
	fp := Fingerprint(original)
	if got := p.Sample(fp); got != original {
		t.Fatalf("sample=%q want=%q", got, original)
	}
}

func TestPromptAnalyticsStats(t *testing.T) {
	p := NewPromptAnalytics()
	p.Record("a")
	p.Record("a")
	p.Record("b")
	st := p.Stats()
	if st.TotalRecords != 3 {
		t.Fatalf("totalRecords=%d want=3", st.TotalRecords)
	}
	if st.UniqueGroups != 2 {
		t.Fatalf("uniqueGroups=%d want=2", st.UniqueGroups)
	}
}

func TestPromptAnalyticsReset(t *testing.T) {
	p := NewPromptAnalytics()
	p.Record("x")
	p.Reset()
	st := p.Stats()
	if st.TotalRecords != 0 || st.UniqueGroups != 0 {
		t.Fatalf("post-reset stats=%+v", st)
	}
}

func BenchmarkFingerprint(b *testing.B) {
	prompts := []string{
		"Translate this paragraph to Spanish please",
		"Show me my AWS billing for the last 3 months",
		"What's the weather in NYC tomorrow?",
		"Summarize https://example.com/long/article for me",
		"Find user 12345 and update their tier",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Fingerprint(prompts[i%len(prompts)])
	}
}

func BenchmarkPromptAnalyticsRecord(b *testing.B) {
	p := NewPromptAnalytics()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Record("Find user 12345 in the system please")
	}
}
