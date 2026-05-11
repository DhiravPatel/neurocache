package llmstack

import (
	"testing"
)

func TestFewShotAddAndQuery(t *testing.T) {
	f := NewFewShotBank()
	f.Add("support", "ex1", "How do I reset my password?",
		"Click 'forgot password' on the login page.", AddOpts{})
	f.Add("support", "ex2", "What's the refund policy?",
		"30-day refund for all annual plans.", AddOpts{})
	f.Add("support", "ex3", "Where do I download the app?",
		"App store / Play store.", AddOpts{})

	hits, ok := f.Query("support", "i forgot my password", QueryOpts{K: 1})
	if !ok {
		t.Fatal("query returned false")
	}
	if len(hits) != 1 {
		t.Fatalf("hits=%d", len(hits))
	}
	if hits[0].ID != "ex1" {
		t.Fatalf("expected top hit ex1 (password question), got %s", hits[0].ID)
	}
}

func TestFewShotUnknownBank(t *testing.T) {
	f := NewFewShotBank()
	if _, ok := f.Query("nope", "anything", QueryOpts{}); ok {
		t.Fatal("expected false for unknown bank")
	}
}

func TestFewShotKDefaultsTo3(t *testing.T) {
	f := NewFewShotBank()
	for i := 0; i < 5; i++ {
		f.Add("b1", string(rune('a'+i)), "input "+string(rune('a'+i)), "out", AddOpts{})
	}
	hits, _ := f.Query("b1", "input", QueryOpts{})
	if len(hits) != 3 {
		t.Fatalf("default K should be 3, got %d", len(hits))
	}
}

func TestFewShotTagFilter(t *testing.T) {
	f := NewFewShotBank()
	f.Add("b1", "billing1", "refund question", "30 days", AddOpts{Tags: []string{"billing"}})
	f.Add("b1", "tech1", "login bug", "clear cookies", AddOpts{Tags: []string{"tech"}})
	f.Add("b1", "tech2", "404 error", "check the URL", AddOpts{Tags: []string{"tech"}})

	hits, _ := f.Query("b1", "anything", QueryOpts{K: 5, Tags: []string{"tech"}})
	if len(hits) != 2 {
		t.Fatalf("tag filter should return 2 tech hits, got %d", len(hits))
	}
	for _, h := range hits {
		found := false
		for _, tag := range h.Tags {
			if tag == "tech" {
				found = true
			}
		}
		if !found {
			t.Fatalf("hit %s missing tech tag", h.ID)
		}
	}
}

func TestFewShotEmbedExplicit(t *testing.T) {
	f := NewFewShotBank()
	v1 := []float64{1, 0, 0}
	v2 := []float64{0, 1, 0}
	v3 := []float64{0.9, 0.1, 0}
	f.Add("b1", "ex1", "alpha", "out1", AddOpts{Vec: v1})
	f.Add("b1", "ex2", "beta", "out2", AddOpts{Vec: v2})
	f.Add("b1", "ex3", "gamma", "out3", AddOpts{Vec: v3})

	hits, _ := f.Query("b1", "anything", QueryOpts{K: 1, Vec: []float64{1, 0, 0}})
	if hits[0].ID != "ex1" {
		t.Fatalf("expected top hit ex1 (most similar to query vec), got %s", hits[0].ID)
	}
}

func TestFewShotDimMismatchRejected(t *testing.T) {
	f := NewFewShotBank()
	f.Add("b1", "ex1", "i", "o", AddOpts{Vec: []float64{1, 0, 0}})
	if err := f.Add("b1", "ex2", "i", "o", AddOpts{Vec: []float64{1, 0}}); err == nil {
		t.Fatal("expected dim mismatch error")
	}
}

func TestFewShotGetAndDel(t *testing.T) {
	f := NewFewShotBank()
	f.Add("b1", "ex1", "in", "out", AddOpts{Tags: []string{"x"}})
	got, ok := f.Get("b1", "ex1")
	if !ok || got.Output != "out" {
		t.Fatalf("get failed: %+v", got)
	}
	if !f.Del("b1", "ex1") {
		t.Fatal("del should return true")
	}
	if _, ok := f.Get("b1", "ex1"); ok {
		t.Fatal("get should fail after del")
	}
}

func TestFewShotForgetBank(t *testing.T) {
	f := NewFewShotBank()
	f.Add("b1", "ex1", "in", "out", AddOpts{})
	if !f.Forget("b1") {
		t.Fatal("forget should return true")
	}
	if _, ok := f.Query("b1", "x", QueryOpts{}); ok {
		t.Fatal("query should fail after forget")
	}
}

func TestFewShotBanksAndStats(t *testing.T) {
	f := NewFewShotBank()
	f.Add("alpha", "ex1", "i", "o", AddOpts{})
	f.Add("alpha", "ex2", "i2", "o2", AddOpts{})
	f.Add("beta", "ex1", "i", "o", AddOpts{})
	rows := f.Banks()
	if len(rows) != 2 {
		t.Fatalf("banks=%d", len(rows))
	}
	if rows[0].BankID != "alpha" || rows[0].Examples != 2 {
		t.Fatalf("rows[0]=%+v", rows[0])
	}
	s := f.Stats()
	if s.Banks != 2 || s.Examples != 3 || s.TotalAdds != 3 {
		t.Fatalf("stats=%+v", s)
	}
}

func TestFewShotEmptyInputRejected(t *testing.T) {
	f := NewFewShotBank()
	if err := f.Add("b1", "ex1", "", "o", AddOpts{}); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestFewShotEmbedFallbackTopical(t *testing.T) {
	f := NewFewShotBank()
	// Same topic words → high similarity
	f.Add("b1", "ex1", "Python is a great language for data science",
		"Yes, with pandas and numpy", AddOpts{})
	f.Add("b1", "ex2", "JavaScript is the language of the web",
		"Yes, especially with React", AddOpts{})
	f.Add("b1", "ex3", "What's the weather today?",
		"Sunny, 72F", AddOpts{})

	hits, _ := f.Query("b1", "Python data analysis", QueryOpts{K: 1})
	if hits[0].ID != "ex1" {
		t.Fatalf("expected ex1 (Python topic), got %s", hits[0].ID)
	}
}
