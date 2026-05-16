package llmstack

import (
	"fmt"
	"testing"
	"time"
)

func TestTimelineAppendAndRange(t *testing.T) {
	tl := NewTimelineLog()
	tl.Append("user-42", "logged in", 1000, "auth")
	tl.Append("user-42", "viewed product", 2000, "view")
	tl.Append("user-42", "added to cart", 3000, "cart")
	rows := tl.Range("user-42", RangeOpts{SinceMS: 1500, UntilMS: 2500})
	if len(rows) != 1 || rows[0].Event != "viewed product" {
		t.Fatalf("range = %+v", rows)
	}
}

func TestTimelineAppendSortedInsertion(t *testing.T) {
	tl := NewTimelineLog()
	// Insert out of order
	tl.Append("k", "third", 3000, "")
	tl.Append("k", "first", 1000, "")
	tl.Append("k", "second", 2000, "")
	rows := tl.Range("k", RangeOpts{})
	if rows[0].TS != 1000 || rows[1].TS != 2000 || rows[2].TS != 3000 {
		t.Fatalf("not sorted by ts: %+v", rows)
	}
}

func TestTimelineKindFilter(t *testing.T) {
	tl := NewTimelineLog()
	tl.Append("k", "login", 1000, "auth")
	tl.Append("k", "page", 2000, "view")
	tl.Append("k", "logout", 3000, "auth")
	rows := tl.Range("k", RangeOpts{Kind: "auth"})
	if len(rows) != 2 {
		t.Fatalf("kind filter = %d", len(rows))
	}
}

func TestTimelineRecent(t *testing.T) {
	tl := NewTimelineLog()
	now := time.Now().UnixMilli()
	tl.Append("k", "fresh", now-1000, "")
	tl.Append("k", "old", now-10*60*1000, "")
	rows := tl.Recent("k", 60, "", 0)
	if len(rows) != 1 || rows[0].Event != "fresh" {
		t.Fatalf("recent = %+v", rows)
	}
}

func TestTimelineLimit(t *testing.T) {
	tl := NewTimelineLog()
	for i := 0; i < 100; i++ {
		tl.Append("k", "e", int64(i+1)*1000, "")
	}
	rows := tl.Range("k", RangeOpts{Limit: 5})
	if len(rows) != 5 {
		t.Fatalf("limit = %d", len(rows))
	}
}

func TestTimelineLen(t *testing.T) {
	tl := NewTimelineLog()
	tl.Append("k", "e", 100, "")
	tl.Append("k", "e2", 200, "")
	n, _ := tl.Len("k")
	if n != 2 {
		t.Fatalf("len = %d", n)
	}
}

func TestTimelineForget(t *testing.T) {
	tl := NewTimelineLog()
	tl.Append("k", "e", 100, "")
	if !tl.Forget("k") {
		t.Fatal("forget should return true")
	}
	if tl.Forget("k") {
		t.Fatal("forget on missing should return false")
	}
}

func TestTimelineRejectsEmpty(t *testing.T) {
	tl := NewTimelineLog()
	if err := tl.Append("", "e", 100, ""); err == nil {
		t.Fatal("empty key should fail")
	}
	if err := tl.Append("k", "", 100, ""); err == nil {
		t.Fatal("empty event should fail")
	}
}

func TestTimelineCapEviction(t *testing.T) {
	tl := NewTimelineLog()
	tl.SetDefaultCap(50)
	for i := 0; i < 100; i++ {
		tl.Append("k", fmt.Sprintf("e%d", i), int64(i+1)*1000, "")
	}
	n, _ := tl.Len("k")
	if n > 50 {
		t.Fatalf("cap eviction failed: len = %d", n)
	}
	// Oldest events should be evicted
	rows := tl.Range("k", RangeOpts{})
	if rows[0].TS == 1000 {
		t.Fatal("oldest event should have been evicted")
	}
}

func TestTimelineStatsAdvance(t *testing.T) {
	tl := NewTimelineLog()
	tl.Append("k", "e", 100, "")
	tl.Range("k", RangeOpts{})
	s := tl.Stats()
	if s.TotalAppends != 1 || s.TotalRanges != 1 || s.Keys != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestTimelineKeys(t *testing.T) {
	tl := NewTimelineLog()
	tl.Append("alpha", "e", 100, "")
	tl.Append("beta", "e", 100, "")
	keys := tl.Keys()
	if len(keys) != 2 || keys[0] != "alpha" || keys[1] != "beta" {
		t.Fatalf("keys = %v", keys)
	}
}
