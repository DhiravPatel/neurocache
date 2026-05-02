package store

import (
	"strconv"
	"testing"
)

// Hot-path benchmarks. These exist to catch O(N) regressions in the
// per-command byte accounting (recomputeBytes vs addBytes deltas) —
// the kind of bug that ships as "compiles + tests pass + 65× slower
// than Redis under real load". Run before merging anything that
// touches the Store hot path:
//
//	go test ./internal/store/ -run=NONE -bench=BenchmarkHot -benchmem
//
// A single LPUSH should take ~hundreds of ns. If you see µs, you've
// regressed. Compare new against `git stash; go test -bench=…`.

func BenchmarkHotLPush(b *testing.B) {
	s := New()
	defer func() { _ = s }()
	const key = "bench:list"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.LPush(key, "v"+strconv.Itoa(i))
	}
}

func BenchmarkHotRPush(b *testing.B) {
	s := New()
	const key = "bench:list"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.RPush(key, "v"+strconv.Itoa(i))
	}
}

func BenchmarkHotLPopFromLong(b *testing.B) {
	// Pre-load 100k entries so each LPop sees a long list. Pre-fix this
	// would have been quadratic (each pop's recomputeBytes walked the
	// list); now it's flat.
	s := New()
	const key = "bench:list"
	for i := 0; i < 100_000; i++ {
		_, _ = s.RPush(key, "v"+strconv.Itoa(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = s.LPop(key)
		// Refill so we don't exhaust the list mid-bench.
		if i%50_000 == 0 {
			for j := 0; j < 50_000; j++ {
				_, _ = s.RPush(key, "v"+strconv.Itoa(j))
			}
		}
	}
}

func BenchmarkHotHSet(b *testing.B) {
	s := New()
	const key = "bench:hash"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.HSet(key, "f"+strconv.Itoa(i), "v")
	}
}

func BenchmarkHotSAdd(b *testing.B) {
	s := New()
	const key = "bench:set"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.SAdd(key, "m"+strconv.Itoa(i))
	}
}

func BenchmarkHotZAdd(b *testing.B) {
	s := New()
	const key = "bench:zset"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.ZAdd(key, ZPair{Score: float64(i), Member: "m" + strconv.Itoa(i)})
	}
}

func BenchmarkHotSetGet(b *testing.B) {
	s := New()
	const key = "bench:str"
	s.Set(key, "value", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = s.GetTyped(key)
	}
}

func BenchmarkHotIncr(b *testing.B) {
	s := New()
	const key = "bench:counter"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Incr(key, 1)
	}
}
