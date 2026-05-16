package llmstack

import (
	"testing"
	"time"
)

func TestDocFreshRegisterAndCheck(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "https://docs/billing/cancel", "h1", 0)
	r := d.Check("doc-1")
	if r.Status != "fresh" {
		t.Fatalf("status = %s", r.Status)
	}
	if r.Source != "https://docs/billing/cancel" {
		t.Fatalf("source = %s", r.Source)
	}
}

func TestDocFreshCheckMissing(t *testing.T) {
	d := NewDocFreshTracker()
	r := d.Check("ghost")
	if r.Status != "missing" {
		t.Fatalf("missing status = %s", r.Status)
	}
}

func TestDocFreshHashChangeMarksStale(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "old-hash", 0)
	d.Stamp("doc-1", "new-hash")
	r := d.Check("doc-1")
	if r.Status != "stale" {
		t.Fatalf("hash mismatch should be stale: %+v", r)
	}
}

func TestDocFreshReRegisterClearsStale(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "old", 0)
	d.Stamp("doc-1", "new")
	if d.Check("doc-1").Status != "stale" {
		t.Fatal("setup precondition")
	}
	// Re-register with the new hash acknowledges the change
	d.Register("doc-1", "url", "new", 0)
	if d.Check("doc-1").Status != "fresh" {
		t.Fatalf("re-register should clear stale: %+v", d.Check("doc-1"))
	}
}

func TestDocFreshTTLExpires(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "h", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	r := d.Check("doc-1")
	if r.Status != "expired" {
		t.Fatalf("ttl should expire: %+v", r)
	}
}

func TestDocFreshTTLZeroNeverExpires(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "h", 0)
	time.Sleep(5 * time.Millisecond)
	r := d.Check("doc-1")
	if r.Status != "fresh" {
		t.Fatalf("ttl=0 should never expire: %+v", r)
	}
}

func TestDocFreshInvalidate(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "h", 0)
	d.Invalidate("doc-1", "webhook fired")
	r := d.Check("doc-1")
	if r.Status != "stale" {
		t.Fatalf("invalidate should make stale: %+v", r)
	}
	// Reason should mention the invalidation cause
	if r.Reason == "" {
		t.Fatalf("reason missing: %+v", r)
	}
}

func TestDocFreshSameHashStampClears(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "h", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if d.Check("doc-1").Status != "expired" {
		t.Fatal("setup precondition")
	}
	// Re-stamp with same hash should clear expiry
	d.Stamp("doc-1", "h")
	if d.Check("doc-1").Status != "fresh" {
		t.Fatalf("same-hash re-stamp should clear: %+v", d.Check("doc-1"))
	}
}

func TestDocFreshBulkCheck(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("a", "url", "h", 0)
	d.Register("b", "url", "h", 0)
	d.Invalidate("b", "")
	rows := d.BulkCheck([]string{"a", "b", "c"})
	if len(rows) != 3 {
		t.Fatalf("bulk = %d", len(rows))
	}
	if rows[0].Status != "fresh" || rows[1].Status != "stale" || rows[2].Status != "missing" {
		t.Fatalf("statuses = %s/%s/%s", rows[0].Status, rows[1].Status, rows[2].Status)
	}
}

func TestDocFreshStaleListSortedNewestFirst(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("a", "url", "h", 0)
	d.Register("b", "url", "h", 0)
	d.Register("c", "url", "h", 0)
	d.Invalidate("a", "")
	time.Sleep(2 * time.Millisecond)
	d.Invalidate("b", "")
	rows := d.Stale(0)
	// b was invalidated more recently → first
	if len(rows) != 2 || rows[0].DocID != "b" {
		t.Fatalf("stale = %+v", rows)
	}
}

func TestDocFreshStaleLimit(t *testing.T) {
	d := NewDocFreshTracker()
	for i := 0; i < 5; i++ {
		id := "doc-" + itoaBench(i)
		d.Register(id, "url", "h", 0)
		d.Invalidate(id, "")
	}
	rows := d.Stale(2)
	if len(rows) != 2 {
		t.Fatalf("limit not respected: %d", len(rows))
	}
}

func TestDocFreshListSorted(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("zeta", "u", "h", 0)
	d.Register("alpha", "u", "h", 0)
	d.Register("mid", "u", "h", 0)
	l := d.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestDocFreshDropOne(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("a", "u", "h", 0)
	d.Register("b", "u", "h", 0)
	if d.Drop("a") != 1 {
		t.Fatal("drop a should remove 1")
	}
}

func TestDocFreshDropAll(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("a", "u", "h", 0)
	d.Register("b", "u", "h", 0)
	if d.Drop("ALL") != 2 {
		t.Fatal("ALL drop should remove 2")
	}
}

func TestDocFreshRejectsBadInput(t *testing.T) {
	d := NewDocFreshTracker()
	if err := d.Register("", "u", "h", 0); err == nil {
		t.Fatal("empty doc_id should fail")
	}
	if err := d.Register("a", "", "h", 0); err == nil {
		t.Fatal("empty source should fail")
	}
	if err := d.Register("a", "u", "h", -1); err == nil {
		t.Fatal("negative ttl should fail")
	}
	if err := d.Stamp("ghost", "h"); err == nil {
		t.Fatal("unknown doc_id should fail")
	}
	if err := d.Invalidate("ghost", ""); err == nil {
		t.Fatal("unknown doc_id should fail")
	}
}

func TestDocFreshStatsAdvance(t *testing.T) {
	d := NewDocFreshTracker()
	d.Register("a", "u", "h", 0)
	d.Stamp("a", "h2")     // hash change → stale
	d.Check("a")
	d.Invalidate("a", "")
	st := d.Stats()
	if st.Docs != 1 || st.StaleDocs != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalRegisters != 1 || st.TotalStamps != 1 || st.TotalChecks != 1 || st.TotalInvalidates != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkDocFreshCheck(b *testing.B) {
	d := NewDocFreshTracker()
	d.Register("doc-1", "url", "h", 60*time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Check("doc-1")
	}
}
