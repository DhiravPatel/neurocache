package store

import (
	"strconv"
	"sync/atomic"
	"testing"
)

// BenchmarkParallelSetRandom drives many goroutines doing SET on well-spread
// keys, to measure shard-lock contention under high parallelism. If the 256
// shards keep contention negligible at GOMAXPROCS goroutines, a thread-
// per-shard execution rewrite cannot improve throughput — the data the
// lever-4 decision needs.
func BenchmarkParallelSetRandom(b *testing.B) {
	s := New()
	var ctr uint64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddUint64(&ctr, 1)
			// Multiply by a large odd constant to spread across shards.
			k := "k" + strconv.FormatUint(i*2654435761, 10)
			s.Set(k, "v", 0)
		}
	})
}

// BenchmarkParallelMixed is a 80/20 read/write mix on a working set, closer
// to a real workload's shard-access pattern.
func BenchmarkParallelMixed(b *testing.B) {
	s := New()
	const workingSet = 100000
	for i := 0; i < workingSet; i++ {
		s.Set("k"+strconv.Itoa(i), "v", 0)
	}
	var ctr uint64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddUint64(&ctr, 1)
			k := "k" + strconv.FormatUint((i*2654435761)%workingSet, 10)
			if i%5 == 0 {
				s.Set(k, "v2", 0)
			} else {
				_, _ = s.Get(k)
			}
		}
	})
}

// BenchmarkParallelHotKey is the adversarial case: every goroutine hammers
// ONE key, so they all collide on a single shard's lock. This is the only
// scenario where thread-per-shard could plausibly help — it shows the upper
// bound on contention cost.
func BenchmarkParallelHotKey(b *testing.B) {
	s := New()
	s.Set("hot", "0", 0)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = s.Incr("hot", 1)
		}
	})
}
