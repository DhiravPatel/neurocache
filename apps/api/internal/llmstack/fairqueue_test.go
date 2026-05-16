package llmstack

import (
	"testing"
)

func TestFairQueueConfigureRequiresPositiveWeight(t *testing.T) {
	q := NewFairQueue()
	if err := q.Configure("api", map[string]float64{"a": -1}); err == nil {
		t.Fatal("negative weight should fail")
	}
}

func TestFairQueueEnqueueRequiresConfiguredTenant(t *testing.T) {
	q := NewFairQueue()
	if _, err := q.Enqueue("api", "ghost", "req-1", ""); err == nil {
		t.Fatal("unconfigured tenant should fail")
	}
}

func TestFairQueueEqualWeightsRoundRobin(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1, "b": 1})
	q.Enqueue("api", "a", "a1", "")
	q.Enqueue("api", "a", "a2", "")
	q.Enqueue("api", "a", "a3", "")
	q.Enqueue("api", "b", "b1", "")
	q.Enqueue("api", "b", "b2", "")
	// Equal weights → strict round-robin a, b, a, b, a
	got := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		r, ok := q.Dequeue("api")
		if !ok {
			t.Fatal("dequeue empty too early")
		}
		got = append(got, r.RequestID)
	}
	want := []string{"a1", "b1", "a2", "b2", "a3"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("at %d: got %s, want %s (full=%v)", i, got[i], w, got)
		}
	}
}

func TestFairQueueWeightedSchedule(t *testing.T) {
	q := NewFairQueue()
	// a has 3× the weight of b → should be scheduled ~3× as often
	q.Configure("api", map[string]float64{"a": 3, "b": 1})
	for i := 1; i <= 20; i++ {
		q.Enqueue("api", "a", "a"+itoaBench(i), "")
	}
	for i := 1; i <= 20; i++ {
		q.Enqueue("api", "b", "b"+itoaBench(i), "")
	}
	aCount, bCount := 0, 0
	// Dequeue 16 — should be ~12 a's and ~4 b's
	for i := 0; i < 16; i++ {
		r, _ := q.Dequeue("api")
		if r.Tenant == "a" {
			aCount++
		} else {
			bCount++
		}
	}
	if aCount < 10 || aCount > 13 {
		t.Fatalf("a count = %d (expected ~12)", aCount)
	}
	if bCount < 3 || bCount > 6 {
		t.Fatalf("b count = %d (expected ~4)", bCount)
	}
}

func TestFairQueueStarvationProtection(t *testing.T) {
	q := NewFairQueue()
	// Even with 10× weight, the underweight tenant still gets served
	q.Configure("api", map[string]float64{"big": 10, "small": 1})
	for i := 1; i <= 50; i++ {
		q.Enqueue("api", "big", "big"+itoaBench(i), "")
	}
	q.Enqueue("api", "small", "small-1", "")
	// Drain enough that "small" definitely gets a turn
	smallSeen := false
	for i := 0; i < 20; i++ {
		r, _ := q.Dequeue("api")
		if r.RequestID == "small-1" {
			smallSeen = true
			break
		}
	}
	if !smallSeen {
		t.Fatal("small tenant starved")
	}
}

func TestFairQueueDequeueEmptyReturnsNotOk(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1})
	if _, ok := q.Dequeue("api"); ok {
		t.Fatal("empty queue should return not-ok")
	}
}

func TestFairQueueEnqueueReturnsDepth(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1, "b": 1})
	d1, _ := q.Enqueue("api", "a", "a1", "")
	d2, _ := q.Enqueue("api", "b", "b1", "")
	d3, _ := q.Enqueue("api", "a", "a2", "")
	if d1 != 1 || d2 != 2 || d3 != 3 {
		t.Fatalf("depths = %d, %d, %d", d1, d2, d3)
	}
}

func TestFairQueueLenOverallAndPerTenant(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1, "b": 1})
	q.Enqueue("api", "a", "a1", "")
	q.Enqueue("api", "a", "a2", "")
	q.Enqueue("api", "b", "b1", "")
	overall, _ := q.Len("api", "")
	if overall != 3 {
		t.Fatalf("overall = %d", overall)
	}
	aLen, _ := q.Len("api", "a")
	if aLen != 2 {
		t.Fatalf("a len = %d", aLen)
	}
}

func TestFairQueuePeekPreservesQueue(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1})
	q.Enqueue("api", "a", "a1", "")
	q.Enqueue("api", "a", "a2", "")
	rows, _ := q.Peek("api", 2)
	if len(rows) != 2 {
		t.Fatalf("peek = %d", len(rows))
	}
	// Queue should still hold both
	overall, _ := q.Len("api", "")
	if overall != 2 {
		t.Fatalf("peek mutated queue: depth=%d", overall)
	}
}

func TestFairQueueDropTenant(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1, "b": 1})
	q.Enqueue("api", "a", "a1", "")
	q.Enqueue("api", "a", "a2", "")
	q.Enqueue("api", "b", "b1", "")
	dropped, ok := q.DropTenant("api", "a")
	if !ok || dropped != 2 {
		t.Fatalf("drop returned %d / %v", dropped, ok)
	}
	overall, _ := q.Len("api", "")
	if overall != 1 {
		t.Fatalf("overall after drop = %d", overall)
	}
}

func TestFairQueueWeightZeroRemovesTenant(t *testing.T) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1})
	q.Configure("api", map[string]float64{"a": 0}) // remove
	if _, err := q.Enqueue("api", "a", "x", ""); err == nil {
		t.Fatal("weight=0 should have removed tenant")
	}
}

func TestFairQueueListSorted(t *testing.T) {
	q := NewFairQueue()
	q.Configure("zeta", map[string]float64{"a": 1})
	q.Configure("alpha", map[string]float64{"a": 1})
	q.Configure("mid", map[string]float64{"a": 1})
	l := q.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestFairQueueResetOne(t *testing.T) {
	q := NewFairQueue()
	q.Configure("a", map[string]float64{"t": 1})
	q.Configure("b", map[string]float64{"t": 1})
	if q.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestFairQueueResetAll(t *testing.T) {
	q := NewFairQueue()
	q.Configure("a", map[string]float64{"t": 1})
	q.Configure("b", map[string]float64{"t": 1})
	if q.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestFairQueueRejectsBadInputs(t *testing.T) {
	q := NewFairQueue()
	if err := q.Configure("", map[string]float64{"a": 1}); err == nil {
		t.Fatal("empty queue_id should fail")
	}
	if err := q.Configure("q", map[string]float64{"": 1}); err == nil {
		t.Fatal("empty tenant should fail")
	}
	q.Configure("q", map[string]float64{"a": 1})
	if _, err := q.Enqueue("q", "a", "", ""); err == nil {
		t.Fatal("empty request_id should fail")
	}
}

func TestFairQueueStatsAdvance(t *testing.T) {
	q := NewFairQueue()
	q.Configure("a", map[string]float64{"t": 1})
	q.Enqueue("a", "t", "r", "")
	q.Dequeue("a")
	st := q.Stats()
	if st.Queues != 1 || st.TotalEnqueues != 1 || st.TotalDequeues != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkFairQueueEnqueueDequeue(b *testing.B) {
	q := NewFairQueue()
	q.Configure("api", map[string]float64{"a": 1, "b": 1, "c": 1})
	tenants := []string{"a", "b", "c"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Enqueue("api", tenants[i%3], "r", "")
		q.Dequeue("api")
	}
}
