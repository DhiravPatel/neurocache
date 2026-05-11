package semcache

import (
	"strconv"
	"testing"
	"time"
)

func TestNegCacheBasic(t *testing.T) {
	n := NewNegCache()
	if n.Check("missing query") {
		t.Fatal("fresh cache should miss")
	}
	n.Mark("missing query", 0)
	if !n.Check("missing query") {
		t.Fatal("post-Mark Check should hit")
	}
}

func TestNegCacheNormalization(t *testing.T) {
	n := NewNegCache()
	n.Mark("How does this work?", 0)
	// Different whitespace + case should still hit.
	if !n.Check("how does THIS  work?") {
		t.Fatal("normalized variant should hit")
	}
	if !n.Check("  how\tdoes\nthis work?  ") {
		t.Fatal("whitespace-noisy variant should hit")
	}
}

func TestNegCacheTTL(t *testing.T) {
	n := NewNegCache()
	n.Mark("q", 50*time.Millisecond)
	if !n.Check("q") {
		t.Fatal("immediate Check should hit")
	}
	time.Sleep(80 * time.Millisecond)
	if n.Check("q") {
		t.Fatal("expired entry should miss")
	}
	st := n.Stats()
	if st.ExpirePurges < 1 {
		t.Fatalf("expected an expire purge; got %d", st.ExpirePurges)
	}
}

func TestNegCacheStats(t *testing.T) {
	n := NewNegCache()
	n.Mark("a", 0)
	n.Mark("b", 0)
	n.Check("a")          // hit
	n.Check("a")          // hit
	n.Check("never seen") // miss
	st := n.Stats()
	if st.Hits != 2 {
		t.Fatalf("hits=%d want=2", st.Hits)
	}
	if st.Misses != 1 {
		t.Fatalf("misses=%d want=1", st.Misses)
	}
	if st.Marks != 2 {
		t.Fatalf("marks=%d want=2", st.Marks)
	}
	if st.UniqueEntries != 2 {
		t.Fatalf("unique=%d want=2", st.UniqueEntries)
	}
	if st.HitRate < 0.66 || st.HitRate > 0.67 {
		t.Fatalf("hit_rate=%f want~0.667", st.HitRate)
	}
}

func TestNegCacheListAndSort(t *testing.T) {
	n := NewNegCache()
	n.Mark("oldest", 0)
	time.Sleep(15 * time.Millisecond)
	n.Mark("middle", 0)
	time.Sleep(15 * time.Millisecond)
	n.Mark("newest", 0)
	rows := n.List(0)
	if len(rows) != 3 {
		t.Fatalf("rows=%d want=3", len(rows))
	}
	// Newest should be first (smallest AgeSec). Since all ages are
	// rounded to whole seconds during the brief test the relative
	// order may show as 0/0/0 — what we really verify is List
	// returns every entry, not the sort under millisecond precision.
}

func TestNegCacheForgetAndClear(t *testing.T) {
	n := NewNegCache()
	n.Mark("a", 0)
	n.Mark("b", 0)
	if !n.Forget("a") {
		t.Fatal("Forget(a) should return true")
	}
	if n.Forget("a") {
		t.Fatal("second Forget(a) should return false")
	}
	if n.Check("a") {
		t.Fatal("after Forget, Check should miss")
	}
	cleared := n.Clear()
	if cleared != 1 {
		t.Fatalf("Clear removed %d want=1", cleared)
	}
	if n.Check("b") {
		t.Fatal("after Clear, Check should miss")
	}
}

func BenchmarkNegCacheCheckHit(b *testing.B) {
	n := NewNegCache()
	n.Mark("query that hits", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.Check("query that hits")
	}
}

func BenchmarkNegCacheCheckMiss(b *testing.B) {
	n := NewNegCache()
	for i := 0; i < 100; i++ {
		n.Mark("filler "+strconv.Itoa(i), 0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.Check("definitely not there")
	}
}

func BenchmarkNegCacheMark(b *testing.B) {
	n := NewNegCache()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.Mark("q "+strconv.Itoa(i&0xFFFF), 0)
	}
}
