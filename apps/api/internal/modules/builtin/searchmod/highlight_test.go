package searchmod

import (
	"strings"
	"testing"
)

func TestWrapTermsBasic(t *testing.T) {
	got := wrapTerms("the quick brown fox", []string{"quick"}, "<b>", "</b>")
	want := "the <b>quick</b> brown fox"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWrapTermsCaseInsensitive(t *testing.T) {
	got := wrapTerms("REDIS rocks", []string{"redis"}, "<b>", "</b>")
	want := "<b>REDIS</b> rocks"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWrapTermsWordBoundary(t *testing.T) {
	// "go" inside "google" must not be wrapped — whole-word only.
	got := wrapTerms("go to google", []string{"go"}, "<b>", "</b>")
	want := "<b>go</b> to google"
	if got != want {
		t.Fatalf("boundary check failed: got %q, want %q", got, want)
	}
}

func TestWrapTermsMultiple(t *testing.T) {
	got := wrapTerms("redis is fast", []string{"redis", "fast"}, "[", "]")
	want := "[redis] is [fast]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWrapTermsEmptyTermsPasses(t *testing.T) {
	got := wrapTerms("hello world", nil, "<b>", "</b>")
	if got != "hello world" {
		t.Fatalf("nil terms should pass through unchanged, got %q", got)
	}
}

func TestSummarizeReturnsSnippet(t *testing.T) {
	body := "the quick brown fox jumps over the lazy dog and then runs away from the chasing wolf in the deep dark forest"
	got := summarizeText(body, []string{"fox"}, 1, 8, "... ")
	if !strings.Contains(got, "fox") {
		t.Fatalf("snippet should include the matched term: %q", got)
	}
	if !strings.HasSuffix(got, "... ") {
		t.Fatalf("snippet should end with separator: %q", got)
	}
}

func TestSummarizeFallbackWhenNoMatch(t *testing.T) {
	body := "alpha beta gamma delta epsilon"
	got := summarizeText(body, []string{"missing"}, 1, 3, "... ")
	if !strings.Contains(got, "alpha") {
		t.Fatalf("fallback should return a head slice: %q", got)
	}
}

func TestApplyHighlightFiltersByField(t *testing.T) {
	doc := &Document{
		ID:    "d1",
		Score: 1,
		Fields: map[string]string{
			"title": "redis is fast",
			"body":  "redis runs in memory",
		},
	}
	applyHighlight(doc, HighlightOpts{Fields: []string{"title"}, Open: "<b>", Close: "</b>"}, []string{"redis"})
	if !strings.Contains(doc.Fields["title"], "<b>redis</b>") {
		t.Errorf("title should be highlighted: %q", doc.Fields["title"])
	}
	if strings.Contains(doc.Fields["body"], "<b>") {
		t.Errorf("body should NOT be highlighted (FIELDS restricted to title): %q", doc.Fields["body"])
	}
}

func TestApplySummarizeShortensContent(t *testing.T) {
	long := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa ", 10)
	doc := &Document{
		ID:    "d",
		Score: 1,
		Fields: map[string]string{
			"body": long + " match-here " + long,
		},
	}
	applySummarize(doc, SummarizeOpts{Frags: 1, Len: 6, Separator: "/"}, []string{"match-here"})
	if got := len(doc.Fields["body"]); got > len(long) {
		t.Fatalf("summarize should shrink body, got len=%d (orig=%d)", got, len(long))
	}
}

func TestExtractQueryTermsCollectsTermsAndPhrases(t *testing.T) {
	q, err := ParseQuery("redis @title:fast \"hello world\"")
	if err != nil {
		t.Fatal(err)
	}
	got := extractQueryTerms(q)
	want := map[string]bool{"redis": true, "fast": true, "hello": true, "world": true}
	for _, term := range got {
		delete(want, strings.ToLower(term))
	}
	if len(want) > 0 {
		t.Fatalf("missed terms: %v (got %v)", want, got)
	}
}

func TestParseSummarizeOptsFullClause(t *testing.T) {
	opts := &SummarizeOpts{}
	args := []string{"FIELDS", "1", "title", "FRAGS", "5", "LEN", "12", "SEPARATOR", "//", "WITHSCORES"}
	end := parseSummarizeOpts(args, 0, opts)
	if end != 9 {
		t.Fatalf("end index = %d, want 9 (right before WITHSCORES)", end)
	}
	if len(opts.Fields) != 1 || opts.Fields[0] != "title" {
		t.Errorf("fields = %v", opts.Fields)
	}
	if opts.Frags != 5 || opts.Len != 12 || opts.Separator != "//" {
		t.Errorf("opts = %+v", opts)
	}
}

func TestParseHighlightOptsTags(t *testing.T) {
	opts := &HighlightOpts{}
	args := []string{"FIELDS", "1", "body", "TAGS", "<em>", "</em>", "RETURN"}
	end := parseHighlightOpts(args, 0, opts)
	if end != 6 {
		t.Fatalf("end index = %d, want 6 (right before RETURN)", end)
	}
	if opts.Open != "<em>" || opts.Close != "</em>" {
		t.Errorf("tags = %q / %q", opts.Open, opts.Close)
	}
}
