package llmstack

import (
	"testing"
	"time"
)

func TestToolCacheSetGet(t *testing.T) {
	tc := NewToolCache()
	tc.Set("get_weather", `{"city":"NYC"}`, "sunny 72F", 0, 0)
	v, ok := tc.Get("get_weather", `{"city":"NYC"}`)
	if !ok || v != "sunny 72F" {
		t.Fatalf("Get got=%q ok=%v", v, ok)
	}
	if _, ok := tc.Get("get_weather", `{"city":"LA"}`); ok {
		t.Fatal("LA should miss")
	}
}

func TestToolCacheArgsCanonicalization(t *testing.T) {
	// {"a":1,"b":2} and {"b":2,"a":1} must hash identically.
	tc := NewToolCache()
	tc.Set("tool", `{"a":1,"b":2}`, "result", 0, 0)
	v, ok := tc.Get("tool", `{"b":2,"a":1}`)
	if !ok || v != "result" {
		t.Fatalf("reordered args should hit; got=%q ok=%v", v, ok)
	}
}

func TestToolCacheTTL(t *testing.T) {
	tc := NewToolCache()
	tc.Set("tool", "args", "v", 50*time.Millisecond, 0)
	if _, ok := tc.Get("tool", "args"); !ok {
		t.Fatal("immediate get should hit")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := tc.Get("tool", "args"); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestToolCacheStats(t *testing.T) {
	tc := NewToolCache()
	tc.Set("t1", "a1", "v1", 0, 100_000) // $0.10
	tc.Set("t2", "a2", "v2", 0, 50_000)  // $0.05
	tc.Get("t1", "a1") // hit + $0.10 saved
	tc.Get("t1", "a1") // hit + $0.10 saved
	tc.Get("t1", "miss") // miss
	st := tc.Stats()
	if st.Hits != 2 {
		t.Fatalf("hits=%d want=2", st.Hits)
	}
	if st.Misses != 1 {
		t.Fatalf("misses=%d want=1", st.Misses)
	}
	if st.Stores != 2 {
		t.Fatalf("stores=%d want=2", st.Stores)
	}
	if st.SavedUSD < 0.19 || st.SavedUSD > 0.21 {
		t.Fatalf("saved_usd=%f want~0.20", st.SavedUSD)
	}
	if st.UniqueEntries != 2 {
		t.Fatalf("unique=%d want=2", st.UniqueEntries)
	}
}

func TestToolCachePurgeByTool(t *testing.T) {
	tc := NewToolCache()
	tc.Set("a", "x", "1", 0, 0)
	tc.Set("a", "y", "2", 0, 0)
	tc.Set("b", "x", "3", 0, 0)
	n := tc.Purge("a")
	if n != 2 {
		t.Fatalf("Purge(a) removed=%d want=2", n)
	}
	if _, ok := tc.Get("a", "x"); ok {
		t.Fatal("a/x should be gone")
	}
	if _, ok := tc.Get("b", "x"); !ok {
		t.Fatal("b/x should remain")
	}
}

func TestToolCacheList(t *testing.T) {
	tc := NewToolCache()
	tc.Set("get_user", `{"id":1}`, "alice", time.Hour, 1000)
	tc.Set("get_user", `{"id":2}`, "bob", 0, 1000)
	tc.Set("other", "args", "v", 0, 0)
	rows := tc.List("get_user", 0)
	if len(rows) != 2 {
		t.Fatalf("filtered list=%d want=2", len(rows))
	}
	for _, r := range rows {
		if r.Tool != "get_user" {
			t.Fatalf("unexpected tool=%q", r.Tool)
		}
	}
	all := tc.List("", 0)
	if len(all) != 3 {
		t.Fatalf("all list=%d want=3", len(all))
	}
}

// ─── benchmarks ─────────────────────────────────────────────────

func BenchmarkToolCacheGetHit(b *testing.B) {
	tc := NewToolCache()
	tc.Set("tool", `{"k":"v"}`, "result", 0, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tc.Get("tool", `{"k":"v"}`)
	}
}

func BenchmarkToolCacheSet(b *testing.B) {
	tc := NewToolCache()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tc.Set("tool", `{"k":"v"}`, "r", 0, 0)
	}
}
