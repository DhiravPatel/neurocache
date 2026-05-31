package llmstack

import (
	"testing"
	"time"
)

func TestCFCachePutAndGet(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "ctx-1", "A", []string{"doc:44"}, 0)
	r := c.Get("Q", "ctx-1")
	if !r.Hit || r.Answer != "A" {
		t.Fatalf("get: %+v", r)
	}
}

func TestCFCacheDistinguishesByContext(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "ctx-old", "A-old", nil, 0)
	c.Put("Q", "ctx-new", "A-new", nil, 0)
	if c.Get("Q", "ctx-old").Answer != "A-old" {
		t.Fatal("ctx-old wrong answer")
	}
	if c.Get("Q", "ctx-new").Answer != "A-new" {
		t.Fatal("ctx-new wrong answer")
	}
}

func TestCFCacheVariants(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "ctx-a", "A", nil, 0)
	c.Put("Q", "ctx-b", "B", nil, 0)
	c.Put("Q", "ctx-c", "C", nil, 0)
	rows, ok := c.Variants("Q", 0)
	if !ok || len(rows) != 3 {
		t.Fatalf("variants = %+v", rows)
	}
}

func TestCFCacheVariantsLimit(t *testing.T) {
	c := NewCounterfactualCache()
	for i := 0; i < 10; i++ {
		c.Put("Q", "ctx-"+itoaBench(i), "A", nil, 0)
	}
	rows, _ := c.Variants("Q", 3)
	if len(rows) != 3 {
		t.Fatalf("limit = %d", len(rows))
	}
}

func TestCFCacheDiff(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "a", "line1\nline2\nline3", nil, 0)
	c.Put("Q", "b", "line1\nline2X\nline3", nil, 0)
	d, ok := c.Diff("Q", "a", "b")
	if !ok {
		t.Fatal("diff missing")
	}
	if d.Identical {
		t.Fatal("should not be identical")
	}
	if len(d.OnlyInA) != 1 || d.OnlyInA[0] != "line2" {
		t.Fatalf("only_in_a = %v", d.OnlyInA)
	}
	if len(d.OnlyInB) != 1 || d.OnlyInB[0] != "line2X" {
		t.Fatalf("only_in_b = %v", d.OnlyInB)
	}
}

func TestCFCacheDiffIdentical(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "a", "same", nil, 0)
	c.Put("Q", "b", "same", nil, 0)
	d, _ := c.Diff("Q", "a", "b")
	if !d.Identical {
		t.Fatal("should be identical")
	}
}

func TestCFCacheTTLExpiry(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "ctx", "A", nil, 5*time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	r := c.Get("Q", "ctx")
	if r.Hit {
		t.Fatal("expired entry should miss")
	}
}

func TestCFCacheForgetVariant(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "a", "A", nil, 0)
	c.Put("Q", "b", "B", nil, 0)
	if c.Forget("Q", "a") != 1 {
		t.Fatal("forget variant")
	}
	if c.Get("Q", "a").Hit {
		t.Fatal("a should be gone")
	}
	if !c.Get("Q", "b").Hit {
		t.Fatal("b should remain")
	}
}

func TestCFCacheForgetQuery(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "a", "A", nil, 0)
	c.Put("Q", "b", "B", nil, 0)
	if c.Forget("Q", "") != 2 {
		t.Fatal("forget query")
	}
}

func TestCFCacheForgetAll(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q1", "a", "A", nil, 0)
	c.Put("Q2", "a", "A", nil, 0)
	if c.Forget("ALL", "") != 2 {
		t.Fatal("forget ALL should drop both")
	}
}

func TestCFCacheStats(t *testing.T) {
	c := NewCounterfactualCache()
	c.Put("Q", "a", "A", nil, 0)
	c.Get("Q", "a")
	c.Get("Q", "b") // miss
	s := c.Stats()
	if s.TotalPuts != 1 || s.TotalHits != 1 || s.TotalMisses != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.HitRate < 0.49 || s.HitRate > 0.51 {
		t.Fatalf("hit rate = %f", s.HitRate)
	}
}

func TestCFCacheRejectsBadInput(t *testing.T) {
	c := NewCounterfactualCache()
	if err := c.Put("", "a", "x", nil, 0); err == nil {
		t.Fatal("empty query should fail")
	}
	if err := c.Put("q", "", "x", nil, 0); err == nil {
		t.Fatal("empty ctx should fail")
	}
	if err := c.Put("q", "a", "", nil, 0); err == nil {
		t.Fatal("empty answer should fail")
	}
	if err := c.Put("q", "a", "x", nil, -1); err == nil {
		t.Fatal("negative ttl should fail")
	}
}
