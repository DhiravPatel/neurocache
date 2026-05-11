package llmstack

import (
	"math"
	"testing"
	"time"
)

func TestRerankSetGet(t *testing.T) {
	c := NewRerankCache()
	c.Set("best phone", "doc-1", 0.92, 0)
	s, ok := c.Get("best phone", "doc-1")
	if !ok {
		t.Fatal("get returned false")
	}
	if s != 0.92 {
		t.Fatalf("score = %f, want 0.92", s)
	}
}

func TestRerankMissReturnsFalse(t *testing.T) {
	c := NewRerankCache()
	if _, ok := c.Get("q", "d"); ok {
		t.Fatal("expected miss")
	}
}

func TestRerankExpiryHonored(t *testing.T) {
	c := NewRerankCache()
	c.Set("q", "d", 0.5, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("q", "d"); ok {
		t.Fatal("expected expired entry to miss")
	}
}

func TestRerankBatchScoreOrderPreserved(t *testing.T) {
	c := NewRerankCache()
	c.Set("phones", "iphone", 0.91, 0)
	c.Set("phones", "samsung", 0.87, 0)
	r := c.ScoreBatch("phones", []string{"iphone", "missing", "samsung"})
	if r.HitN != 2 || r.MissN != 1 {
		t.Fatalf("hit_n=%d miss_n=%d", r.HitN, r.MissN)
	}
	if r.Scores[0] != 0.91 {
		t.Fatalf("scores[0]=%f", r.Scores[0])
	}
	if !math.IsNaN(r.Scores[1]) {
		t.Fatalf("scores[1] should be NaN for miss, got %f", r.Scores[1])
	}
	if r.Scores[2] != 0.87 {
		t.Fatalf("scores[2]=%f", r.Scores[2])
	}
	if !r.Hits[0] || r.Hits[1] || !r.Hits[2] {
		t.Fatalf("hits = %v", r.Hits)
	}
}

func TestRerankForgetAndPurge(t *testing.T) {
	c := NewRerankCache()
	c.Set("q", "a", 0.1, 0)
	c.Set("q", "b", 0.2, 0)
	if !c.Forget("q", "a") {
		t.Fatal("forget should return true on hit")
	}
	if c.Forget("q", "a") {
		t.Fatal("forget should return false on miss")
	}
	if c.Purge() != 1 {
		t.Fatal("expected 1 entry purged")
	}
}

func TestRerankCapEviction(t *testing.T) {
	c := NewRerankCache()
	c.SetCap(20)
	for i := 0; i < 25; i++ {
		c.Set("q", string(rune('a'+i)), float64(i), 0)
	}
	st := c.Stats()
	if st.Entries > 20 {
		t.Fatalf("entries=%d, want <=20 after eviction", st.Entries)
	}
	if st.TotalEvicts == 0 {
		t.Fatal("expected eviction counter to advance")
	}
}

func TestRerankSavedUSD(t *testing.T) {
	c := NewRerankCache()
	c.SetCostUSD(0.002) // $0.002 per upstream call
	c.Set("q", "d", 0.5, 0)
	for i := 0; i < 100; i++ {
		c.Get("q", "d")
	}
	st := c.Stats()
	if st.SavedCalls != 100 {
		t.Fatalf("saved_calls=%d", st.SavedCalls)
	}
	want := 0.2 // 100 * 0.002
	if math.Abs(st.SavedUSD-want) > 1e-6 {
		t.Fatalf("saved_usd=%f want %f", st.SavedUSD, want)
	}
}

func TestRerankHitRate(t *testing.T) {
	c := NewRerankCache()
	c.Set("q", "d", 0.5, 0)
	for i := 0; i < 3; i++ {
		c.Get("q", "d") // hits
	}
	for i := 0; i < 7; i++ {
		c.Get("q", "missing") // misses
	}
	st := c.Stats()
	if st.HitRate < 0.29 || st.HitRate > 0.31 {
		t.Fatalf("hit_rate=%f, want ~0.30", st.HitRate)
	}
}

func TestRerankSortedByScore(t *testing.T) {
	c := NewRerankCache()
	c.Set("q", "low", 0.1, 0)
	c.Set("q", "mid", 0.5, 0)
	c.Set("q", "high", 0.9, 0)
	got := c.SortedDocIDsByScore("q", []string{"low", "mid", "high", "missing"})
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (missing should drop)", len(got))
	}
	if got[0] != "high" || got[1] != "mid" || got[2] != "low" {
		t.Fatalf("order = %v, want [high mid low]", got)
	}
}
