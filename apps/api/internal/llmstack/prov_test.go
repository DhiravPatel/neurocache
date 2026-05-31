package llmstack

import (
	"testing"
)

func TestProvBeginAndNode(t *testing.T) {
	p := NewProvenance()
	if err := p.Begin("ans-1", map[string]string{"user": "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Node("ans-1", "q", "query", "what is the refund window", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Node("ans-1", "rw", "rewrite", "hyDE variant", []string{"q"}, nil); err != nil {
		t.Fatal(err)
	}
	v, ok := p.Answer("ans-1")
	if !ok || len(v.Nodes) != 2 {
		t.Fatalf("answer = %+v", v)
	}
}

func TestProvNodeValidatesFrom(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	if err := p.Node("a", "n1", "k", "l", []string{"ghost"}, nil); err == nil {
		t.Fatal("FROM ghost should fail")
	}
}

func TestProvNodeRejectsDuplicate(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	p.Node("a", "n1", "k", "l", nil, nil)
	if err := p.Node("a", "n1", "k", "l", nil, nil); err == nil {
		t.Fatal("duplicate node id should fail")
	}
}

func TestProvBeginRejectsDuplicate(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	if err := p.Begin("a", nil); err == nil {
		t.Fatal("duplicate begin should fail")
	}
}

func TestProvWhyTracesLineage(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	p.Node("a", "q", "query", "Q", nil, nil)
	p.Node("a", "rw", "rewrite", "RW", []string{"q"}, nil)
	p.Node("a", "c1", "chunk", "doc-44 v3", []string{"rw"}, []string{"doc:44@v3"})
	p.Node("a", "llm", "llm", "gpt-4o", []string{"c1"}, []string{"prompt:v5"})
	p.Node("a", "out", "answer", "final", []string{"llm"}, nil)
	path, ok := p.Why("a", "out", 0)
	if !ok || len(path) != 5 {
		t.Fatalf("why = %+v", path)
	}
	// First entry is the target, then ancestors
	if path[0].ID != "out" || path[4].ID != "q" {
		t.Fatalf("unexpected path: %+v", path)
	}
}

func TestProvWhyDefaultsToLastNode(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	p.Node("a", "q", "query", "Q", nil, nil)
	p.Node("a", "ans", "answer", "A", []string{"q"}, nil)
	path, _ := p.Why("a", "", 0)
	if path[0].ID != "ans" {
		t.Fatalf("default target = %s", path[0].ID)
	}
}

func TestProvImpactReturnsAllAnswers(t *testing.T) {
	p := NewProvenance()
	p.Begin("a1", nil)
	p.Node("a1", "c", "chunk", "x", nil, []string{"doc:44"})
	p.Begin("a2", nil)
	p.Node("a2", "c", "chunk", "x", nil, []string{"doc:44"})
	p.Begin("a3", nil)
	p.Node("a3", "c", "chunk", "x", nil, []string{"doc:99"})
	imp := p.Impact("doc:44")
	if len(imp) != 2 {
		t.Fatalf("impact = %v", imp)
	}
}

func TestProvImpactReturnsEmptyForUnknown(t *testing.T) {
	p := NewProvenance()
	if got := p.Impact("nope"); len(got) != 0 {
		t.Fatalf("unknown ref = %v", got)
	}
}

func TestProvForgetCleansRefIndex(t *testing.T) {
	p := NewProvenance()
	p.Begin("a1", nil)
	p.Node("a1", "c", "chunk", "x", nil, []string{"doc:44"})
	p.Forget("a1")
	if got := p.Impact("doc:44"); len(got) != 0 {
		t.Fatalf("ref index should be cleaned: %v", got)
	}
	s := p.Stats()
	if s.IndexedRefs != 0 {
		t.Fatalf("indexed_refs = %d", s.IndexedRefs)
	}
}

func TestProvForgetAll(t *testing.T) {
	p := NewProvenance()
	p.Begin("a1", nil)
	p.Begin("a2", nil)
	if p.Forget("ALL") != 2 {
		t.Fatal("ALL should drop 2")
	}
}

func TestProvListMostRecent(t *testing.T) {
	p := NewProvenance()
	p.Begin("a1", nil)
	p.Begin("a2", nil)
	p.Begin("a3", nil)
	rows := p.List(2)
	if len(rows) != 2 {
		t.Fatalf("list = %d", len(rows))
	}
}

func TestProvStats(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	p.Node("a", "n", "k", "l", nil, []string{"r1"})
	p.Why("a", "", 0)
	p.Impact("r1")
	s := p.Stats()
	if s.TotalBegins != 1 || s.TotalNodes != 1 || s.TotalWhys != 1 || s.TotalImpacts != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.IndexedRefs != 1 {
		t.Fatalf("indexed_refs = %d", s.IndexedRefs)
	}
}

func TestProvRejectsBadInput(t *testing.T) {
	p := NewProvenance()
	if err := p.Begin("", nil); err == nil {
		t.Fatal("empty answer should fail")
	}
	p.Begin("a", nil)
	if err := p.Node("", "n", "k", "l", nil, nil); err == nil {
		t.Fatal("empty answer id should fail")
	}
	if err := p.Node("a", "", "k", "l", nil, nil); err == nil {
		t.Fatal("empty node id should fail")
	}
	if err := p.Node("a", "n", "", "l", nil, nil); err == nil {
		t.Fatal("empty kind should fail")
	}
	if err := p.Node("nope", "n", "k", "l", nil, nil); err == nil {
		t.Fatal("unknown answer should fail")
	}
}

func TestProvWhyDepthBound(t *testing.T) {
	p := NewProvenance()
	p.Begin("a", nil)
	p.Node("a", "n1", "k", "l", nil, nil)
	p.Node("a", "n2", "k", "l", []string{"n1"}, nil)
	p.Node("a", "n3", "k", "l", []string{"n2"}, nil)
	path, _ := p.Why("a", "n3", 2)
	if len(path) != 2 {
		t.Fatalf("depth bound not respected: %d", len(path))
	}
}

func BenchmarkProvNode(b *testing.B) {
	p := NewProvenance()
	p.Begin("a", nil)
	p.Node("a", "root", "k", "l", nil, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Node("a", "n-"+itoaBench(i), "k", "l", []string{"root"}, []string{"doc:" + itoaBench(i%50)})
	}
}
