package llmstack

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalesceFirstCallerOwns(t *testing.T) {
	c := NewCoalescer()
	r := c.Lock("k1", 1000)
	if !r.Owner {
		t.Fatal("first caller should own")
	}
	if r.Token == "" {
		t.Fatal("owner should get a token")
	}
}

func TestCoalesceSecondCallerWaits(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 1000)
	r2 := c.Lock("k1", 1000)
	if r2.Owner {
		t.Fatal("second caller should NOT own")
	}
}

func TestCoalescePublishWakesWaiter(t *testing.T) {
	c := NewCoalescer()
	r := c.Lock("k1", 5000)
	done := make(chan WaitResult, 1)
	go func() {
		done <- c.Wait("k1", 2*time.Second)
	}()
	time.Sleep(10 * time.Millisecond) // let waiter park
	if !c.Publish("k1", r.Token, "the answer") {
		t.Fatal("publish failed")
	}
	w := <-done
	if !w.Got || w.Result != "the answer" {
		t.Fatalf("waiter got = %+v", w)
	}
}

func TestCoalescePublishRejectsBadToken(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 1000)
	if c.Publish("k1", "wrong-token", "x") {
		t.Fatal("publish with wrong token should fail")
	}
}

func TestCoalescePublishUnknownKey(t *testing.T) {
	c := NewCoalescer()
	if c.Publish("nope", "tok", "x") {
		t.Fatal("publish on unknown key should fail")
	}
}

func TestCoalesceWaitTimesOut(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 5000)
	w := c.Wait("k1", 30*time.Millisecond)
	if w.Got {
		t.Fatal("wait should time out without publish")
	}
}

func TestCoalesceWaitOnUnknownKeyReturnsImmediately(t *testing.T) {
	c := NewCoalescer()
	start := time.Now()
	w := c.Wait("never-existed", 5*time.Second)
	if w.Got {
		t.Fatal("unknown key should return got=false")
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("wait on unknown key should return immediately")
	}
}

func TestCoalesceAlreadyPublishedReturnsImmediately(t *testing.T) {
	c := NewCoalescer()
	r := c.Lock("k1", 1000)
	c.Publish("k1", r.Token, "result")
	start := time.Now()
	w := c.Wait("k1", 5*time.Second)
	if !w.Got || w.Result != "result" {
		t.Fatalf("wait after publish = %+v", w)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("wait on published key should return immediately")
	}
}

func TestCoalesceStaleLockReclaimable(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 30) // 30ms timeout
	time.Sleep(60 * time.Millisecond)
	r := c.Lock("k1", 1000)
	if !r.Owner {
		t.Fatal("stale lock should be reclaimable by next caller")
	}
}

func TestCoalesceManyWaitersOneOwner(t *testing.T) {
	// Real thundering-herd: 100 goroutines all hit the cache for the
	// same key. One wins LOCK, the other 99 WAIT. After PUBLISH, all
	// 99 should wake up with the same answer.
	c := NewCoalescer()
	const N = 100
	var owners atomic.Int32
	var receivers atomic.Int32
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]string, N)

	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			r := c.Lock("herd-key", 5000)
			if r.Owner {
				owners.Add(1)
				time.Sleep(50 * time.Millisecond) // simulate upstream call
				c.Publish("herd-key", r.Token, "the truth")
				results[i] = "the truth"
			} else {
				w := c.Wait("herd-key", 2*time.Second)
				if w.Got {
					receivers.Add(1)
					results[i] = w.Result
				}
			}
		}()
	}
	wg.Wait()
	if owners.Load() != 1 {
		t.Fatalf("expected exactly 1 owner, got %d", owners.Load())
	}
	if receivers.Load() != N-1 {
		t.Fatalf("expected %d receivers, got %d", N-1, receivers.Load())
	}
	for i, r := range results {
		if r != "the truth" {
			t.Fatalf("waiter %d got %q", i, r)
		}
	}
}

func TestCoalesceStatusStates(t *testing.T) {
	c := NewCoalescer()
	if _, ok := c.Status("nope"); ok {
		t.Fatal("status on unknown key should be false")
	}
	r := c.Lock("k1", 1000)
	st, _ := c.Status("k1")
	if st.State != "locked" {
		t.Fatalf("state = %q, want locked", st.State)
	}
	c.Publish("k1", r.Token, "x")
	st2, _ := c.Status("k1")
	if st2.State != "published" {
		t.Fatalf("state = %q, want published", st2.State)
	}
}

func TestCoalesceForget(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 1000)
	if !c.Forget("k1") {
		t.Fatal("forget should return true")
	}
	if c.Forget("k1") {
		t.Fatal("forget on missing should return false")
	}
}

func TestCoalesceForgetWakesWaiter(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 5000)
	done := make(chan WaitResult, 1)
	go func() {
		done <- c.Wait("k1", 2*time.Second)
	}()
	time.Sleep(10 * time.Millisecond)
	c.Forget("k1")
	w := <-done
	if w.Got {
		t.Fatal("waiter should not get a result on forget")
	}
}

func TestCoalesceStatsTrackContention(t *testing.T) {
	c := NewCoalescer()
	c.Lock("k1", 1000)
	c.Lock("k1", 1000) // contended
	c.Lock("k1", 1000) // contended
	s := c.Stats()
	if s.TotalLocks != 3 || s.TotalAcquires != 1 || s.TotalContended != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if s.SaveRate < 0.66 || s.SaveRate > 0.67 {
		t.Fatalf("save_rate = %f, want ~0.667", s.SaveRate)
	}
}
