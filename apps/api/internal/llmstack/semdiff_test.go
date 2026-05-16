package llmstack

import (
	"strings"
	"testing"
)

func TestSemDiffCheckIdentical(t *testing.T) {
	s := NewSemDiffStore()
	r := s.Check("summarize this document carefully", "summarize this document carefully")
	if !r.Identical {
		t.Fatalf("identical text not flagged: cos=%f verdict=%s", r.Cosine, r.Verdict)
	}
	if r.Verdict != "identical" {
		t.Fatalf("verdict = %s", r.Verdict)
	}
}

func TestSemDiffCheckEquivalent(t *testing.T) {
	s := NewSemDiffStore()
	// Same lexical content, slight rewording: hashed-BoW will keep high cosine
	r := s.Check(
		"summarize this document carefully document",
		"summarize document carefully document this",
	)
	if r.Cosine < 0.90 {
		t.Fatalf("expected equivalent, got %f", r.Cosine)
	}
}

func TestSemDiffCheckDivergent(t *testing.T) {
	s := NewSemDiffStore()
	r := s.Check(
		"explain quantum physics to a child",
		"write a recipe for chocolate cake",
	)
	if r.Verdict == "identical" || r.Verdict == "equivalent" {
		t.Fatalf("very different text scored as %s (cos=%f)", r.Verdict, r.Cosine)
	}
}

func TestSemDiffPutAndGetLatest(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("prompt-v", "v1", "summarize the doc")
	s.Put("prompt-v", "v2", "summarize the document in 3 sentences")
	label, text, ok := s.Latest("prompt-v")
	if !ok || label != "v2" {
		t.Fatalf("latest label = %s, ok=%v", label, ok)
	}
	if !strings.Contains(text, "3 sentences") {
		t.Fatalf("latest text = %s", text)
	}
}

func TestSemDiffGetByVersion(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("p", "v1", "hello")
	s.Put("p", "v2", "world")
	_, text, ok := s.Get("p", "v1")
	if !ok || text != "hello" {
		t.Fatalf("v1 = %s, ok=%v", text, ok)
	}
}

func TestSemDiffCompareReturnsVerdict(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("p", "v1", "summarize this document carefully")
	s.Put("p", "v2", "summarize this document carefully")
	r, err := s.Compare("p", "v1", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Identical {
		t.Fatalf("matching versions should compare identical: cos=%f", r.Cosine)
	}
}

func TestSemDiffHistoryComputesDrift(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("p", "v1", "summarize document")
	s.Put("p", "v2", "summarize document briefly")
	s.Put("p", "v3", "write recipe for soup")
	rows, ok := s.History("p")
	if !ok || len(rows) != 3 {
		t.Fatalf("history len = %d", len(rows))
	}
	if rows[0].VsPrev != 0 {
		t.Fatalf("first row should have 0 vs_prev: %f", rows[0].VsPrev)
	}
	if rows[1].VsPrev < 0.5 {
		t.Fatalf("v1→v2 cosine too low: %f", rows[1].VsPrev)
	}
	// v3 wildly different from v2
	if rows[2].VsPrev > 0.5 {
		t.Fatalf("v2→v3 cosine should be low: %f", rows[2].VsPrev)
	}
}

func TestSemDiffPutOverwrites(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("p", "v1", "old text")
	s.Put("p", "v1", "new text")
	_, text, _ := s.Get("p", "v1")
	if text != "new text" {
		t.Fatalf("overwrite failed: %s", text)
	}
}

func TestSemDiffDelete(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("p", "v1", "x")
	if !s.Delete("p") {
		t.Fatal("delete should report success")
	}
	if _, _, ok := s.Get("p", "v1"); ok {
		t.Fatal("name still exists after delete")
	}
}

func TestSemDiffNamesSorted(t *testing.T) {
	s := NewSemDiffStore()
	s.Put("c", "v", "x")
	s.Put("a", "v", "x")
	s.Put("b", "v", "x")
	names := s.Names()
	if len(names) != 3 || names[0] != "a" || names[2] != "c" {
		t.Fatalf("names = %v", names)
	}
}

func TestSemDiffRejectsBadInput(t *testing.T) {
	s := NewSemDiffStore()
	if err := s.Put("", "v", "x"); err == nil {
		t.Fatal("empty name should fail")
	}
	if err := s.Put("p", "", "x"); err == nil {
		t.Fatal("empty version should fail")
	}
	if _, err := s.Compare("ghost", "v1", "v2"); err == nil {
		t.Fatal("compare on missing name should fail")
	}
	s.Put("p", "v1", "x")
	if _, err := s.Compare("p", "v1", "missing"); err == nil {
		t.Fatal("compare on missing version should fail")
	}
}

func TestSemDiffStatsAdvance(t *testing.T) {
	s := NewSemDiffStore()
	s.Check("a", "b")
	s.Put("p", "v1", "x")
	s.Put("p", "v2", "y")
	s.Compare("p", "v1", "v2")
	st := s.Stats()
	if st.Names != 1 || st.TotalVersions != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalChecks != 1 || st.TotalPuts != 2 || st.TotalCompares != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkSemDiffCheck(b *testing.B) {
	s := NewSemDiffStore()
	a := "summarize the document briefly with citations"
	c := "summarize this document briefly please"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Check(a, c)
	}
}

func BenchmarkSemDiffCompareCached(b *testing.B) {
	s := NewSemDiffStore()
	s.Put("p", "v1", "summarize the document briefly")
	s.Put("p", "v2", "summarize this document briefly please")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Compare("p", "v1", "v2")
	}
}
