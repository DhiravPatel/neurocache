package llmstack

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPrefixHashStable(t *testing.T) {
	a := HashPrefix("you are a helpful assistant")
	b := HashPrefix("you are a helpful assistant")
	if a != b {
		t.Fatalf("same input should hash identically: %s vs %s", a, b)
	}
	c := HashPrefix("different system prompt")
	if a == c {
		t.Fatal("different inputs should hash differently")
	}
	if len(a) != 16 {
		t.Fatalf("hash should be 16 hex chars, got %d", len(a))
	}
}

func TestPrefixRegisterAndLookup(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("abc123", "worker-1", 0)
	p.Register("abc123", "worker-2", 0)
	rows := p.Lookup("abc123")
	if len(rows) != 2 {
		t.Fatalf("workers = %d", len(rows))
	}
}

func TestPrefixLookupMiss(t *testing.T) {
	p := NewPrefixRouter()
	if rows := p.Lookup("unknown"); len(rows) != 0 {
		t.Fatalf("miss should return empty, got %v", rows)
	}
}

func TestPrefixLookupNewestFirst(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("abc", "worker-old", 0)
	time.Sleep(2 * time.Millisecond)
	p.Register("abc", "worker-new", 0)
	rows := p.Lookup("abc")
	if rows[0].Worker != "worker-new" {
		t.Fatalf("newest should be first, got %v", rows)
	}
}

func TestPrefixForgetSpecificWorker(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("abc", "w1", 0)
	p.Register("abc", "w2", 0)
	if !p.Forget("abc", "w1") {
		t.Fatal("forget should return true")
	}
	rows := p.Lookup("abc")
	if len(rows) != 1 || rows[0].Worker != "w2" {
		t.Fatalf("after forget w1: %v", rows)
	}
}

func TestPrefixForgetAllForPrefix(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("abc", "w1", 0)
	p.Register("abc", "w2", 0)
	if !p.Forget("abc", "") {
		t.Fatal("forget all should return true")
	}
	if rows := p.Lookup("abc"); len(rows) != 0 {
		t.Fatalf("after forget all: %v", rows)
	}
}

func TestPrefixEvictWorker(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("pfx1", "w1", 0)
	p.Register("pfx2", "w1", 0)
	p.Register("pfx3", "w1", 0)
	p.Register("pfx1", "w2", 0)

	dropped := p.EvictWorker("w1")
	if dropped != 3 {
		t.Fatalf("evicted %d, want 3", dropped)
	}
	if rows := p.Lookup("pfx1"); len(rows) != 1 || rows[0].Worker != "w2" {
		t.Fatalf("pfx1 should still have w2: %v", rows)
	}
	if rows := p.Lookup("pfx2"); len(rows) != 0 {
		t.Fatalf("pfx2 should be empty: %v", rows)
	}
}

func TestPrefixTTLExpires(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("abc", "w1", 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if rows := p.Lookup("abc"); len(rows) != 0 {
		t.Fatalf("expired claim should drop, got %v", rows)
	}
}

func TestPrefixListAndStats(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("hot", "w1", 0)
	p.Register("hot", "w2", 0)
	p.Register("hot", "w3", 0)
	p.Register("cold", "w1", 0)

	p.Lookup("hot")
	p.Lookup("hot")
	p.Lookup("missing")

	list := p.Prefixes()
	if len(list) != 2 {
		t.Fatalf("prefixes = %d", len(list))
	}
	if list[0].PrefixHash != "hot" || list[0].Workers != 3 {
		t.Fatalf("hot row = %+v", list[0])
	}

	s := p.Stats()
	if s.TotalLookups != 3 || s.TotalHits != 2 || s.TotalMisses != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestPrefixConcurrentRegisterRaceFree(t *testing.T) {
	// 50 distinct (prefix, worker) pairs (10 prefixes × 5 workers),
	// each fired by 2 goroutines = 100 total. Independent indices so
	// every pair gets hit (avoids i%10 / i%5 stepping in lockstep).
	p := NewPrefixRouter()
	var wg sync.WaitGroup
	wg.Add(100)
	for i := 0; i < 100; i++ {
		i := i
		j := i % 50
		go func() {
			defer wg.Done()
			p.Register(fmt.Sprintf("hash-%d", j/5), fmt.Sprintf("w%d", j%5), 0)
		}()
	}
	wg.Wait()
	list := p.Prefixes()
	if len(list) != 10 {
		t.Fatalf("expected 10 distinct prefixes, got %d", len(list))
	}
	for _, p := range list {
		if p.Workers != 5 {
			t.Errorf("prefix %s has %d workers, want 5", p.PrefixHash, p.Workers)
		}
	}
}

func TestPrefixRegisterRefreshUpdatesTimestamp(t *testing.T) {
	p := NewPrefixRouter()
	p.Register("abc", "w1", 0)
	time.Sleep(2 * time.Millisecond)
	p.Register("abc", "w1", 0) // refresh
	rows := p.Lookup("abc")
	if len(rows) != 1 {
		t.Fatalf("should still be 1 worker after refresh, got %d", len(rows))
	}
}
