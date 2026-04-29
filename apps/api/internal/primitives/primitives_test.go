package primitives

import (
	"sync"
	"testing"
	"time"
)

func TestIdempotentLeaderAndDuplicate(t *testing.T) {
	s := NewIdempotencyStore()
	cached, hit, err := s.Acquire("k1", time.Second)
	if hit || err != nil {
		t.Fatalf("first call should be leader: hit=%v err=%v", hit, err)
	}
	_ = cached
	s.Complete("k1", "result-A", time.Second)
	cached, hit, err = s.Acquire("k1", time.Second)
	if !hit || err != nil || cached != "result-A" {
		t.Fatalf("second call should hit cache: hit=%v cached=%v err=%v", hit, cached, err)
	}
}

func TestLockExclusivityAndExtend(t *testing.T) {
	m := NewLockManager()
	tok1, ok := m.Acquire("res", "alice", time.Second)
	if !ok || tok1 == 0 {
		t.Fatal("alice should acquire")
	}
	if _, ok := m.Acquire("res", "bob", time.Second); ok {
		t.Fatal("bob should not acquire while alice holds")
	}
	if !m.Extend("res", "alice", 2*time.Second) {
		t.Fatal("extend should succeed for current owner")
	}
	if m.Extend("res", "bob", time.Second) {
		t.Fatal("extend should fail for non-owner")
	}
	if !m.Release("res", "alice") {
		t.Fatal("alice should release")
	}
	if _, ok := m.Acquire("res", "bob", time.Second); !ok {
		t.Fatal("bob should acquire after release")
	}
}

func TestLockTokenMonotonic(t *testing.T) {
	m := NewLockManager()
	t1, _ := m.Acquire("k", "a", time.Second)
	m.Release("k", "a")
	t2, _ := m.Acquire("k", "b", time.Second)
	if t2 <= t1 {
		t.Fatalf("token went backwards: %d -> %d", t1, t2)
	}
}

func TestRateLimitGCRA(t *testing.T) {
	r := NewRateLimiter()
	// 5 ops per second
	allowed, _, _, _ := r.Allow("u", time.Second, 5, 1)
	if !allowed {
		t.Fatal("first call should pass")
	}
	for i := 0; i < 4; i++ {
		r.Allow("u", time.Second, 5, 1)
	}
	allowed, _, retry, _ := r.Allow("u", time.Second, 5, 1)
	if allowed {
		t.Fatal("6th call should be rejected")
	}
	if retry <= 0 {
		t.Fatal("retry-after should be positive when rejected")
	}
}

func TestDedupSeenOrMark(t *testing.T) {
	d := NewDeduper()
	if d.SeenOrMark("orders", "o-1", time.Second) {
		t.Fatal("first sighting should not be seen")
	}
	if !d.SeenOrMark("orders", "o-1", time.Second) {
		t.Fatal("second sighting should be seen")
	}
	if d.SeenOrMark("orders", "o-2", time.Second) {
		t.Fatal("different id should not be seen")
	}
}

func TestCostTableScoreAndStats(t *testing.T) {
	c := NewCostTable()
	c.Weigh("k1", 10)
	c.Weigh("k2", 100)
	c.RecordHit("k1")
	c.RecordHit("k1")
	c.RecordHit("k2")
	if c.Score("k1") != 30 { // 10 * (1+2)
		t.Fatalf("k1 score = %v, want 30", c.Score("k1"))
	}
	if c.Score("k2") != 200 { // 100 * (1+1)
		t.Fatalf("k2 score = %v, want 200", c.Score("k2"))
	}
	s := c.Stats()
	if s.HitsServed != 3 || s.TotalSaved != 120 {
		t.Fatalf("stats wrong: %+v", s)
	}
}

func TestHistoryAt(t *testing.T) {
	h := NewHistoryStore(0, 0)
	h.Track("flag")
	h.Snapshot("flag", "v1")
	t1 := time.Now()
	time.Sleep(15 * time.Millisecond)
	h.Snapshot("flag", "v2")
	time.Sleep(15 * time.Millisecond)
	h.Snapshot("flag", "v3")
	v, ok := h.At("flag", t1)
	if !ok || v != "v1" {
		t.Fatalf("at t1 should be v1, got %q ok=%v", v, ok)
	}
	v, ok = h.At("flag", time.Now())
	if !ok || v != "v3" {
		t.Fatalf("at now should be v3, got %q ok=%v", v, ok)
	}
}

func TestRecommendCollaborativeFiltering(t *testing.T) {
	r := NewRecommender()
	// alice + bob both like "go" — bob also likes "rust"; carol likes
	// only "rust". Recommending for alice should surface "rust".
	r.Like("alice", "go", 1)
	r.Like("bob", "go", 1)
	r.Like("bob", "rust", 1)
	r.Like("carol", "rust", 1)
	recs := r.Recommend("alice", 5)
	if len(recs) == 0 || recs[0].Item != "rust" {
		t.Fatalf("expected 'rust' top recommendation, got %+v", recs)
	}
}

func TestIdempotentConcurrentLeader(t *testing.T) {
	// Two goroutines hitting the same idempotency key — one should be
	// the leader, the other should wait + receive the leader's result.
	s := NewIdempotencyStore()
	var wg sync.WaitGroup
	results := make(chan any, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, hit, _ := s.Acquire("race", time.Second)
		if !hit {
			time.Sleep(20 * time.Millisecond)
			s.Complete("race", "leader-result", time.Second)
		}
		v, _, _ := s.Acquire("race", time.Second)
		results <- v
	}()
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		v, _, _ := s.Acquire("race", time.Second)
		results <- v
	}()
	wg.Wait()
	close(results)
	for v := range results {
		if v != "leader-result" {
			t.Fatalf("expected leader-result, got %v", v)
		}
	}
}
