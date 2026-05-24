package llmstack

import (
	"testing"
	"time"
)

func TestBatchAddFillsSlots(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 50*time.Millisecond, 96, 0, 0)
	r1, err := a.Add("emb", "item-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Slot != 0 || r1.Ready {
		t.Fatalf("first add = %+v", r1)
	}
	r2, _ := a.Add("emb", "item-2", "second")
	if r2.Slot != 1 || r2.BatchID != r1.BatchID {
		t.Fatalf("second add doesn't share batch: %+v vs %+v", r1, r2)
	}
}

func TestBatchAddReadyOnMaxSize(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 60*time.Second, 3, 0, 0)
	a.Add("emb", "i1", "x")
	a.Add("emb", "i2", "x")
	r3, _ := a.Add("emb", "i3", "x")
	if !r3.Ready {
		t.Fatalf("3rd add should fire ready: %+v", r3)
	}
}

func TestBatchAddReadyOnMaxWait(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 1*time.Millisecond, 1000, 0, 0)
	a.Add("emb", "i1", "x")
	time.Sleep(3 * time.Millisecond)
	r2, _ := a.Add("emb", "i2", "x")
	if !r2.Ready {
		t.Fatalf("2nd add past MAXWAIT should fire ready: %+v", r2)
	}
}

func TestBatchFlushRollsForward(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 60*time.Second, 100, 0, 0)
	a.Add("emb", "i1", "x")
	a.Add("emb", "i2", "y")
	out, ok := a.Flush("emb")
	if !ok || len(out.Items) != 2 {
		t.Fatalf("flush = %+v", out)
	}
	// Active batch is now empty
	r3, _ := a.Add("emb", "i3", "z")
	if r3.BatchID == out.BatchID {
		t.Fatal("after flush, new batch id should differ")
	}
}

func TestBatchFlushEmptyBucket(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 60*time.Second, 100, 0, 0)
	out, ok := a.Flush("emb")
	if !ok {
		t.Fatal("flush should succeed even when empty")
	}
	if len(out.Items) != 0 {
		t.Fatalf("empty flush has items: %+v", out)
	}
}

func TestBatchPeekReportsAge(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 1*time.Millisecond, 100, 0, 0)
	a.Add("emb", "i1", "x")
	time.Sleep(3 * time.Millisecond)
	p, ok := a.Peek("emb")
	if !ok || p.Size != 1 {
		t.Fatalf("peek = %+v", p)
	}
	if !p.Ready {
		t.Fatalf("peek past MAXWAIT should be ready: %+v", p)
	}
	if p.AgeMS < 2 {
		t.Fatalf("age too small: %d", p.AgeMS)
	}
}

func TestBatchBucketsSorted(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("zeta", 50*time.Millisecond, 10, 0, 0)
	a.Configure("alpha", 50*time.Millisecond, 10, 0, 0)
	a.Configure("mid", 50*time.Millisecond, 10, 0, 0)
	b := a.Buckets()
	if b[0] != "alpha" || b[2] != "zeta" {
		t.Fatalf("buckets = %v", b)
	}
}

func TestBatchResetOne(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("a", 50*time.Millisecond, 10, 0, 0)
	a.Configure("b", 50*time.Millisecond, 10, 0, 0)
	if a.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestBatchResetAll(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("a", 50*time.Millisecond, 10, 0, 0)
	a.Configure("b", 50*time.Millisecond, 10, 0, 0)
	if a.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestBatchStatsTrackSavings(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("emb", 60*time.Second, 100, 0.0001, 0)
	for i := 0; i < 50; i++ {
		a.Add("emb", "i"+itoaBench(i), "x")
	}
	a.Flush("emb") // 50 items, 1 call → saved 49 calls
	st := a.Stats()
	if len(st.PerBucket) != 1 {
		t.Fatalf("per-bucket = %d", len(st.PerBucket))
	}
	row := st.PerBucket[0]
	if row.TotalItems != 50 || row.TotalCalls != 1 {
		t.Fatalf("counters = %+v", row)
	}
	if row.SavedCalls != 49 {
		t.Fatalf("saved = %d, want 49", row.SavedCalls)
	}
	if row.AvgBatch != 50 {
		t.Fatalf("avg batch = %f", row.AvgBatch)
	}
	if row.SavedUSD < 0.004 || row.SavedUSD > 0.005 {
		t.Fatalf("saved usd = %f", row.SavedUSD)
	}
}

func TestBatchRejectsBadInput(t *testing.T) {
	a := NewBatchAccumulator()
	if err := a.Configure("", 1*time.Millisecond, 1, 0, 0); err == nil {
		t.Fatal("empty bucket should fail")
	}
	if err := a.Configure("a", -1, 1, 0, 0); err == nil {
		t.Fatal("negative maxWait should fail")
	}
	if err := a.Configure("a", 1, 1, -1, 0); err == nil {
		t.Fatal("negative cost should fail")
	}
	a.Configure("a", 50*time.Millisecond, 10, 0, 0)
	if _, err := a.Add("", "i", "x"); err == nil {
		t.Fatal("empty bucket id should fail")
	}
	if _, err := a.Add("a", "", "x"); err == nil {
		t.Fatal("empty item id should fail")
	}
	if err := a.Resolve("ghost", "b", 0); err == nil {
		t.Fatal("unknown bucket should fail")
	}
}

func TestBatchStatsAdvance(t *testing.T) {
	a := NewBatchAccumulator()
	a.Configure("a", 50*time.Millisecond, 10, 0, 0)
	a.Add("a", "i1", "x")
	a.Flush("a")
	a.Resolve("a", "b1", 1)
	st := a.Stats()
	if st.Buckets != 1 || st.TotalAdds != 1 || st.TotalFlushes != 1 || st.TotalResolves != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkBatchAdd(b *testing.B) {
	a := NewBatchAccumulator()
	a.Configure("emb", 60*time.Second, 1000000, 0, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Add("emb", "item", "payload")
	}
}

func BenchmarkBatchFlush(b *testing.B) {
	a := NewBatchAccumulator()
	a.Configure("emb", 60*time.Second, 100, 0, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 50; j++ {
			a.Add("emb", "item", "payload")
		}
		a.Flush("emb")
	}
}
