package store

import (
	"hash/fnv"
	"sync"
)

// numShards is the keyspace partition count. 256 was chosen as the
// sweet spot:
//
//   - Powers of two let us mask with `hash & 255` (one AND, no MOD).
//   - Big enough that ~10k concurrent goroutines hashing to keys
//     uniformly distribute → contention is rare on real workloads.
//   - Small enough that the per-shard memory overhead (an empty map
//     header + an RWMutex) totals ~12 KiB at startup — negligible.
//
// Tune by recompiling; we don't expose a runtime knob because every
// downstream component (TTL loop, eviction, persistence) reads the
// constant directly and a runtime resize would require a full pause.
const numShards = 256

// shard owns one slice of the keyspace. Every key whose FNV-1a hash
// masks to a given shard's index lives in that shard's `data` map and
// is protected by that shard's lock.
//
// Per-shard state intentionally mirrors what used to be Store-global:
// the lock, the data map, and a local byte counter (so we don't
// hammer one global atomic on every mutation).
type shard struct {
	mu    sync.RWMutex
	data  map[string]*Entry
	bytes int64 // local byte tally; rolled into Store.bytes via Store.addBytes
}

// shardForKey returns the shard owning `key`. FNV-1a is fast and
// well-distributed for short keys (the common case for cache lookups).
func (s *Store) shardForKey(key string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return s.shards[h.Sum32()&(numShards-1)]
}

// shardIndex returns the canonical index of a shard, used for
// deadlock-free lock ordering when a multi-key op spans two shards.
func (s *Store) shardIndex(sh *shard) int {
	for i, x := range s.shards {
		if x == sh {
			return i
		}
	}
	return -1
}

// lockTwoW takes write locks on the shards for keys a and b in canonical
// (lowest-index-first) order. Returns both shard pointers (which may be
// equal when both keys hash to the same shard) and a single unlock
// function. Critical for two-key atomics: RENAME, COPY, RPOPLPUSH, SMOVE,
// LMOVE — anything that mutates two keys without a global lock.
func (s *Store) lockTwoW(a, b string) (*shard, *shard, func()) {
	shA := s.shardForKey(a)
	shB := s.shardForKey(b)
	if shA == shB {
		shA.mu.Lock()
		return shA, shB, shA.mu.Unlock
	}
	// Deterministic order eliminates the AB/BA deadlock between two
	// concurrent goroutines doing RENAME(x, y) and RENAME(y, x).
	idxA := s.shardIndex(shA)
	idxB := s.shardIndex(shB)
	first, second := shA, shB
	if idxA > idxB {
		first, second = shB, shA
	}
	first.mu.Lock()
	second.mu.Lock()
	return shA, shB, func() {
		second.mu.Unlock()
		first.mu.Unlock()
	}
}

// lockTwoR is the read-lock variant of lockTwoW.
func (s *Store) lockTwoR(a, b string) (*shard, *shard, func()) {
	shA := s.shardForKey(a)
	shB := s.shardForKey(b)
	if shA == shB {
		shA.mu.RLock()
		return shA, shB, shA.mu.RUnlock
	}
	idxA := s.shardIndex(shA)
	idxB := s.shardIndex(shB)
	first, second := shA, shB
	if idxA > idxB {
		first, second = shB, shA
	}
	first.mu.RLock()
	second.mu.RLock()
	return shA, shB, func() {
		second.mu.RUnlock()
		first.mu.RUnlock()
	}
}

// lockAllW grabs every shard's write lock in canonical order. Used by
// FLUSHALL and the eviction snapshot. Expensive — avoid on the hot path.
func (s *Store) lockAllW() func() {
	for _, sh := range s.shards {
		sh.mu.Lock()
	}
	return func() {
		// Unlock in reverse order — not strictly required for correctness
		// (Go's mutex isn't recursive) but matches the "stack discipline"
		// that helps reasoning when reading lock traces.
		for i := len(s.shards) - 1; i >= 0; i-- {
			s.shards[i].mu.Unlock()
		}
	}
}

// lockAllR is the read-lock variant. Used by KEYS, SCAN, Snapshot.
func (s *Store) lockAllR() func() {
	for _, sh := range s.shards {
		sh.mu.RLock()
	}
	return func() {
		for i := len(s.shards) - 1; i >= 0; i-- {
			s.shards[i].mu.RUnlock()
		}
	}
}

// bucketKeysByShard groups a list of keys by their owning shard. Used
// by multi-key ops (DEL, MGET, MSET, EXISTS) to take one lock per
// shard rather than one per key.
func (s *Store) bucketKeysByShard(keys []string) map[*shard][]string {
	out := map[*shard][]string{}
	for _, k := range keys {
		sh := s.shardForKey(k)
		out[sh] = append(out[sh], k)
	}
	return out
}

// shardsFor returns the unique shards owning the given keys. Used by
// multi-key ops (BITOP, ZUNIONSTORE, etc.) that must lock every
// involved shard in canonical order.
func (s *Store) shardsFor(keys []string) []*shard {
	seen := map[*shard]bool{}
	for _, k := range keys {
		sh := s.shardForKey(k)
		seen[sh] = true
	}
	out := make([]*shard, 0, len(seen))
	for _, sh := range s.shards {
		if seen[sh] {
			out = append(out, sh)
		}
	}
	return out
}

// lockShardsW locks each shard in `shards` (must be in canonical /
// ascending-index order — pass output of shardsFor, which guarantees
// this) and returns a single unlocker.
func (s *Store) lockShardsW(shards []*shard) func() {
	for _, sh := range shards {
		sh.mu.Lock()
	}
	return func() {
		for i := len(shards) - 1; i >= 0; i-- {
			shards[i].mu.Unlock()
		}
	}
}

// lockShardsR is the read-lock variant.
func (s *Store) lockShardsR(shards []*shard) func() {
	for _, sh := range shards {
		sh.mu.RLock()
	}
	return func() {
		for i := len(shards) - 1; i >= 0; i-- {
			shards[i].mu.RUnlock()
		}
	}
}
