package llmstack

import (
	"fmt"
	"testing"
)

func TestSemDedupFirstSeenIsNotDup(t *testing.T) {
	d := NewSemDeduper()
	r := d.Seen("bugs", "login page crashes on safari", SeenOpts{})
	if r.IsDup {
		t.Fatal("first item should not be dup")
	}
	if r.NewID == "" {
		t.Fatal("first item should get an ID assigned")
	}
}

func TestSemDedupParaphraseHits(t *testing.T) {
	d := NewSemDeduper()
	d.Seen("bugs", "login page crashes on safari", SeenOpts{Threshold: 0.5})
	r := d.Seen("bugs", "login page crashes on safari browser", SeenOpts{Threshold: 0.5})
	if !r.IsDup {
		t.Fatalf("near-paraphrase should dedup: score=%f", r.Score)
	}
}

func TestSemDedupUnrelatedMisses(t *testing.T) {
	d := NewSemDeduper()
	d.Seen("bugs", "login page crashes on safari", SeenOpts{Threshold: 0.85})
	r := d.Seen("bugs", "today's weather is sunny", SeenOpts{Threshold: 0.85})
	if r.IsDup {
		t.Fatalf("unrelated text should not dedup: score=%f", r.Score)
	}
}

func TestSemDedupHighThresholdRejectsLooseMatches(t *testing.T) {
	d := NewSemDeduper()
	d.Seen("bugs", "the cat sat on the mat", SeenOpts{Threshold: 0.99})
	r := d.Seen("bugs", "the cat is on the mat", SeenOpts{Threshold: 0.99})
	if r.IsDup {
		t.Fatal("threshold 0.99 should only match near-identical")
	}
}

func TestSemDedupPeekDoesNotInsert(t *testing.T) {
	d := NewSemDeduper()
	d.Peek("bugs", "test", SeenOpts{})
	d.Peek("bugs", "test", SeenOpts{})
	rows := d.Recent("bugs", 0)
	if len(rows) != 0 {
		t.Fatalf("peek should not insert; got %d items", len(rows))
	}
}

func TestSemDedupSlidingWindowEviction(t *testing.T) {
	d := NewSemDeduper()
	for i := 0; i < 10; i++ {
		text := fmt.Sprintf("totally distinct item %d xyz%d abc%d", i, i*7, i*13)
		d.Seen("bugs", text, SeenOpts{Window: 5, Threshold: 0.99})
	}
	stats := d.Stats()
	if stats.TotalEvictions == 0 {
		t.Fatal("expected evictions with window=5 and 10 inserts")
	}
}

func TestSemDedupAddExplicitID(t *testing.T) {
	d := NewSemDeduper()
	if err := d.Add("bugs", "BUG-42", "login crashes", nil); err != nil {
		t.Fatal(err)
	}
	rows := d.Recent("bugs", 0)
	if len(rows) != 1 || rows[0].ID != "BUG-42" {
		t.Fatalf("recent = %+v", rows)
	}
}

func TestSemDedupForget(t *testing.T) {
	d := NewSemDeduper()
	d.Seen("bugs", "test", SeenOpts{})
	if !d.Forget("bugs") {
		t.Fatal("forget should return true")
	}
	if d.Forget("bugs") {
		t.Fatal("forget on missing bucket should return false")
	}
}

func TestSemDedupBucketsAndStats(t *testing.T) {
	d := NewSemDeduper()
	d.Seen("bugs", "x", SeenOpts{Threshold: 0.5})
	d.Seen("bugs", "x", SeenOpts{Threshold: 0.5}) // dup
	d.Seen("complaints", "y", SeenOpts{Threshold: 0.5})

	rows := d.Buckets()
	if len(rows) != 2 {
		t.Fatalf("buckets = %d", len(rows))
	}

	s := d.Stats()
	if s.TotalSeens != 3 {
		t.Fatalf("seens = %d", s.TotalSeens)
	}
	if s.TotalHits != 1 {
		t.Fatalf("hits = %d", s.TotalHits)
	}
	if s.HitRate < 0.32 || s.HitRate > 0.34 {
		t.Fatalf("hit_rate = %f, want ~0.333", s.HitRate)
	}
}

func TestSemDedupWithExplicitEmbedding(t *testing.T) {
	d := NewSemDeduper()
	v1 := []float64{1, 0, 0}
	v2 := []float64{0.99, 0.01, 0}
	v3 := []float64{0, 1, 0}
	d.Seen("custom", "a", SeenOpts{Vec: v1, Threshold: 0.9})
	r := d.Seen("custom", "b", SeenOpts{Vec: v2, Threshold: 0.9})
	if !r.IsDup {
		t.Fatalf("near-identical vectors should dedup: %f", r.Score)
	}
	r2 := d.Seen("custom", "c", SeenOpts{Vec: v3, Threshold: 0.9})
	if r2.IsDup {
		t.Fatalf("orthogonal vector should not dedup: %f", r2.Score)
	}
}

func TestSemDedupSetDefaults(t *testing.T) {
	d := NewSemDeduper()
	d.SetDefaults(0.7, 50)
	if d.defaultThreshold != 0.7 || d.defaultWindow != 50 {
		t.Fatal("defaults not updated")
	}
}

func TestSemDedupRecentLimitN(t *testing.T) {
	d := NewSemDeduper()
	for i := 0; i < 10; i++ {
		d.Add("bucket", fmt.Sprintf("id-%d", i), fmt.Sprintf("item %d", i), nil)
	}
	rows := d.Recent("bucket", 3)
	if len(rows) != 3 {
		t.Fatalf("recent N=3 returned %d", len(rows))
	}
	// Newest last → last item should be id-9
	if rows[len(rows)-1].ID != "id-9" {
		t.Fatalf("last = %s, want id-9", rows[len(rows)-1].ID)
	}
}
