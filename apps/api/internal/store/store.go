// Package store implements the NeuroCache keyspace: a sharded thread-safe
// registry that holds strings, lists, hashes, sets, and sorted sets, plus
// per-key TTLs and hit counters. The typed Entry makes a key exclusively
// hold one value type at a time, matching Redis semantics — a GET on a
// list key returns WRONGTYPE, an LPUSH on a string key does the same.
//
// Concurrency model: 256 shards, each with its own RWMutex + map. A key's
// owning shard is determined by FNV-1a(key) & 255. Single-key operations
// take exactly one shard's lock; cross-key operations (RENAME, COPY,
// MGET-across-shards, etc.) take the involved shards in canonical
// (lowest-index-first) order to avoid deadlock. Range operations (KEYS,
// SCAN, FLUSHALL, eviction snapshot) iterate every shard.
package store

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store/qlist"
)

// ValueType enumerates every kind of value a key can hold.
type ValueType uint8

const (
	TypeNone ValueType = iota
	TypeString
	TypeList
	TypeHash
	TypeSet
	TypeZSet
	TypeStream
)

func (t ValueType) String() string {
	switch t {
	case TypeString:
		return "string"
	case TypeList:
		return "list"
	case TypeHash:
		return "hash"
	case TypeSet:
		return "set"
	case TypeZSet:
		return "zset"
	case TypeStream:
		return "stream"
	case TypeVector:
		return "vectorset"
	default:
		return "none"
	}
}

// ErrWrongType mirrors the Redis WRONGTYPE error string.
var ErrWrongType = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")

// Entry carries the value plus metadata used by metrics, TTL, and eviction.
// Only the field matching Type is populated.
type Entry struct {
	Key  string
	Type ValueType

	Str     string
	List    *qlist.QList // elements are strings
	Hash    map[string]string
	HashTTL map[string]time.Time // optional per-field expiries (Redis 7.4)
	Set     map[string]struct{}
	ZSet    *ZSet
	Stream  *Stream
	Module  *ModuleValue // populated when Type == TypeModule
	Vector  *VectorSet   // populated when Type == TypeVector

	// IntVal + IsInt + IntAtomic are the integer fast-path for the
	// SET/INCR/INCRBY hot path. Redis treats numeric strings specially:
	// an INCR on a numeric value avoids the parse-add-format cycle by
	// keeping the integer in a native field. We go further — IntAtomic
	// is an atomic.Int64 that lets INCR/DECR/INCRBY/DECRBY skip the
	// shard write-lock entirely on the steady state.
	//
	// State machine:
	//   1. Fresh SET of a string that doesn't parse as int → IsInt=false,
	//      Str holds the raw value.
	//   2. SET of a string that parses as int → IsInt=true, IntAtomic
	//      stores the int, Str is the formatted form (kept for GET).
	//   3. INCR on an entry with IsInt=true → atomic.Add on IntAtomic.
	//      Str is left STALE; GET checks IsInt and formats from
	//      IntAtomic on read. This is the lock-free hot path.
	//   4. APPEND / SETRANGE / any write that mutates Str → IsInt=false.
	//
	// IntVal is kept as a non-atomic snapshot of IntAtomic at the last
	// mutex-protected write — used by code paths that already hold the
	// shard lock to avoid the atomic load.
	IntVal    int64
	IsInt     bool
	IntAtomic atomic.Int64

	CreatedAt time.Time
	ExpireAt  time.Time // zero = no expiry
	Hits      uint64
	LastRead  time.Time
	Bytes     int
}

func (e *Entry) expired(now time.Time) bool {
	return !e.ExpireAt.IsZero() && now.After(e.ExpireAt)
}

// Store is a sharded multi-type keyspace. The 256 shards each own a
// disjoint slice of the keyspace; concurrent operations on different
// keys typically don't contend.
type Store struct {
	shards [numShards]*shard
	bytes  atomic.Int64

	// keyspace notifications fan out on mutations.
	notify func(event, key string)
}

func New() *Store {
	s := &Store{}
	for i := 0; i < numShards; i++ {
		s.shards[i] = &shard{data: make(map[string]*Entry), idx: i}
	}
	go s.ttlLoop()
	return s
}

// SetNotifier wires a keyspace callback (used by pub/sub's __keyspace__).
// Call at most once during engine bootstrap. Callers must not block.
func (s *Store) SetNotifier(fn func(event, key string)) { s.notify = fn }

func (s *Store) fire(event, key string) {
	if s.notify != nil {
		s.notify(event, key)
	}
}

// ttlLoop is a lazy expirer. It sweeps once per second; reads also check
// ExpireAt so callers never see an expired value even between sweeps.
// Per-field hash TTLs (Redis 7.4 HEXPIRE) ride on the same tick.
func (s *Store) ttlLoop() {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		var expired []string
		// Sweep each shard independently — never holds more than one
		// shard lock at a time, so the loop doesn't block writers on
		// the other 255 shards.
		for _, sh := range s.shards {
			sh.mu.Lock()
			for k, e := range sh.data {
				if e.expired(now) {
					s.bytes.Add(-int64(e.Bytes))
					delete(sh.data, k)
					expired = append(expired, k)
				}
			}
			s.sweepHashFieldsShard(sh, now)
			sh.mu.Unlock()
		}
		for _, k := range expired {
			s.fire("expired", k)
		}
	}
}

// ─── common key operations ──────────────────────────────────────────────

// Type returns the kind of value at key, or TypeNone if missing/expired.
func (s *Store) Type(key string) ValueType {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		return TypeNone
	}
	return e.Type
}

// Exists counts how many of the given keys exist (duplicates count).
// Buckets keys by shard so we take one lock per shard, not one per key.
//
// Single-key fast path skips the map allocation in bucketKeysByShard —
// EXISTS is one of the highest-frequency commands (ioredis emits it
// before every DEL/EXPIRE on a missing-key check), so eliminating the
// map[*shard][]string allocation matters.
func (s *Store) Exists(keys ...string) int {
	now := time.Now()
	if len(keys) == 1 {
		sh := s.shardForKey(keys[0])
		sh.mu.RLock()
		defer sh.mu.RUnlock()
		if e, ok := sh.data[keys[0]]; ok && !e.expired(now) {
			return 1
		}
		return 0
	}
	n := 0
	buckets := s.bucketKeysByShard(keys)
	for sh, ks := range buckets {
		sh.mu.RLock()
		for _, k := range ks {
			if e, ok := sh.data[k]; ok && !e.expired(now) {
				n++
			}
		}
		sh.mu.RUnlock()
	}
	return n
}

// Del removes keys; returns how many were actually deleted.
//
// Single-key fast path avoids the bucketKeysByShard map alloc and the
// `removed []string` slice growth — DEL key is the second-most-common
// write next to SET, so the inlined path is worth it.
func (s *Store) Del(keys ...string) int {
	if len(keys) == 1 {
		k := keys[0]
		sh := s.shardForKey(k)
		sh.mu.Lock()
		e, ok := sh.data[k]
		if !ok {
			sh.mu.Unlock()
			return 0
		}
		s.bytes.Add(-int64(e.Bytes))
		delete(sh.data, k)
		sh.mu.Unlock()
		s.fire("del", k)
		return 1
	}
	var removed []string
	buckets := s.bucketKeysByShard(keys)
	for sh, ks := range buckets {
		sh.mu.Lock()
		for _, k := range ks {
			if e, ok := sh.data[k]; ok {
				s.bytes.Add(-int64(e.Bytes))
				delete(sh.data, k)
				removed = append(removed, k)
			}
		}
		sh.mu.Unlock()
	}
	for _, k := range removed {
		s.fire("del", k)
	}
	return len(removed)
}

// Expire sets TTL. Returns false if the key does not exist.
func (s *Store) Expire(key string, ttl time.Duration) bool {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	e, ok := sh.data[key]
	if !ok {
		sh.mu.Unlock()
		return false
	}
	e.ExpireAt = time.Now().Add(ttl)
	sh.mu.Unlock()
	s.fire("expire", key)
	return true
}

// ExpireAt sets an absolute expiry time. Returns false if missing.
func (s *Store) ExpireAt(key string, at time.Time) bool {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	e, ok := sh.data[key]
	if !ok {
		sh.mu.Unlock()
		return false
	}
	e.ExpireAt = at
	sh.mu.Unlock()
	s.fire("expireat", key)
	return true
}

// Persist clears the TTL. Returns false if there was no TTL to clear.
func (s *Store) Persist(key string) bool {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data[key]
	if !ok || e.ExpireAt.IsZero() {
		return false
	}
	e.ExpireAt = time.Time{}
	return true
}

// TTL returns time until expiry. -1 = no expiry, -2 = missing.
func (s *Store) TTL(key string) time.Duration {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok {
		return -2
	}
	if e.ExpireAt.IsZero() {
		return -1
	}
	d := time.Until(e.ExpireAt)
	if d < 0 {
		return -2
	}
	return d
}

// Keys returns all live, non-expired keys matching an optional glob
// pattern ("*" matches everything). Pass "" or "*" for a full list.
func (s *Store) Keys(pattern string) []string {
	now := time.Now()
	out := []string{}
	for _, sh := range s.shards {
		sh.mu.RLock()
		for k, e := range sh.data {
			if e.expired(now) {
				continue
			}
			if pattern == "" || pattern == "*" || globMatch(pattern, k) {
				out = append(out, k)
			}
		}
		sh.mu.RUnlock()
	}
	return out
}

// Rename moves a key's value to a new name. Returns false if source missing.
// If dst exists it is overwritten (matches RENAME semantics).
func (s *Store) Rename(src, dst string) bool {
	shS, shD, unlock := s.lockTwoW(src, dst)
	defer unlock()
	e, ok := shS.data[src]
	if !ok || e.expired(time.Now()) {
		return false
	}
	if old, ok := shD.data[dst]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	delete(shS.data, src)
	e.Key = dst
	shD.data[dst] = e
	return true
}

// RenameNX renames only if dst does not already exist. Returns false if
// the source was missing or the destination was taken.
func (s *Store) RenameNX(src, dst string) bool {
	shS, shD, unlock := s.lockTwoW(src, dst)
	defer unlock()
	if _, taken := shD.data[dst]; taken {
		return false
	}
	e, ok := shS.data[src]
	if !ok || e.expired(time.Now()) {
		return false
	}
	delete(shS.data, src)
	e.Key = dst
	shD.data[dst] = e
	return true
}

// Size returns the number of live keys across every shard. Walks all
// 256 shards under read locks; cheap relative to a typical traffic
// pattern (DBSIZE is a low-frequency observation command).
func (s *Store) Size() int {
	n := 0
	for _, sh := range s.shards {
		sh.mu.RLock()
		n += len(sh.data)
		sh.mu.RUnlock()
	}
	return n
}

func (s *Store) BytesUsed() int64 { return s.bytes.Load() }

func (s *Store) FlushAll() {
	unlock := s.lockAllW()
	for _, sh := range s.shards {
		sh.data = make(map[string]*Entry)
	}
	s.bytes.Store(0)
	unlock()
	s.fire("flushdb", "")
}

// Snapshot copies every live entry. Eviction reads scoring fields only, so
// sharing pointers for list/hash/set is safe (they are never mutated).
func (s *Store) Snapshot() []Entry {
	now := time.Now()
	out := []Entry{}
	for _, sh := range s.shards {
		sh.mu.RLock()
		for _, e := range sh.data {
			if !e.expired(now) {
				out = append(out, *e)
			}
		}
		sh.mu.RUnlock()
	}
	return out
}

// Evict deletes the given keys — a plain wrapper Del preserves for the
// eviction package so it does not reach into Del's signature.
func (s *Store) Evict(keys []string) int { return s.Del(keys...) }

// ─── internal helpers shared by typed operations ────────────────────────
//
// `get` and `getOrCreate` are methods on *shard so they operate on the
// right map without re-deriving the shard. Callers that already hold
// the shard's lock invoke them directly.

// get returns the live entry if it exists and matches want. Missing
// entries return (nil, false, nil). Mismatched types return ErrWrongType.
// Passing TypeNone disables the type check.
func (sh *shard) get(key string, want ValueType) (*Entry, bool, error) {
	e, ok := sh.data[key]
	if !ok {
		return nil, false, nil
	}
	if e.expired(time.Now()) {
		return nil, false, nil
	}
	if want != TypeNone && e.Type != want {
		return nil, true, ErrWrongType
	}
	return e, true, nil
}

// getOrCreate returns the entry at key, allocating a new one of the given
// type when missing. A type mismatch on an existing entry is an error.
// Callers must already hold sh.mu.Lock(); the function itself doesn't
// touch the global byte counter (the caller orchestrates via Store.addBytes).
func (s *Store) getOrCreate(sh *shard, key string, t ValueType) (*Entry, error) {
	e, ok := sh.data[key]
	if ok && !e.expired(time.Now()) {
		if e.Type != t {
			return nil, ErrWrongType
		}
		return e, nil
	}
	if ok {
		s.bytes.Add(-int64(e.Bytes))
	}
	e = &Entry{Key: key, Type: t, CreatedAt: time.Now(), LastRead: time.Now()}
	switch t {
	case TypeList:
		e.List = qlist.New()
	case TypeHash:
		// Pre-size the bucket array. Go's map starts at 0 buckets and
		// grows in 2× steps, paying a rehash on every threshold. Most
		// hashes settle around 8–32 fields; sizing for 8 avoids the
		// first 3 grow operations (which together copy 0+1+2+4 = 7
		// bucket loads worth of work). For workloads with larger
		// hashes the steady-state growth is unchanged.
		e.Hash = make(map[string]string, 8)
	case TypeSet:
		e.Set = make(map[string]struct{}, 8)
	case TypeZSet:
		e.ZSet = newZSet()
	case TypeStream:
		e.Stream = newStream()
	}
	sh.data[key] = e
	return e, nil
}

// removeIfEmpty deletes the entry from its shard when its collection is
// empty, mirroring Redis's "empty key = no key" invariant.
func (s *Store) removeIfEmpty(sh *shard, e *Entry) {
	empty := false
	switch e.Type {
	case TypeList:
		empty = e.List == nil || e.List.Len() == 0
	case TypeHash:
		empty = len(e.Hash) == 0
	case TypeSet:
		empty = len(e.Set) == 0
	case TypeZSet:
		empty = e.ZSet == nil || e.ZSet.Len() == 0
	case TypeStream:
		// Streams keep the key even at length 0 — they have metadata
		// (last-ID, consumer groups) that must persist. Match Redis.
		empty = false
	case TypeVector:
		// Vector sets keep the key when emptied — the index dimension
		// + algorithm choice are configuration the caller chose at
		// VADD time and shouldn't lose to the last VREM.
		empty = false
	}
	if empty {
		s.bytes.Add(-int64(e.Bytes))
		delete(sh.data, e.Key)
	}
}

// addBytes adjusts an entry's byte count by a signed delta and mirrors
// the change into the global byte counter. O(1) — preferred over
// recomputeBytes on the hot path (every list/hash/set/zset push is
// otherwise O(N) because recomputeBytes walks the whole collection).
func (s *Store) addBytes(e *Entry, delta int) {
	if delta == 0 {
		return
	}
	e.Bytes += delta
	s.bytes.Add(int64(delta))
}

// recomputeBytes is a best-effort byte-size recalculation. Kept cheap —
// metrics and eviction only need ballpark numbers, not exact cardinality.
func (s *Store) recomputeBytes(e *Entry) {
	old := e.Bytes
	var n int
	switch e.Type {
	case TypeString:
		n = len(e.Key) + len(e.Str)
	case TypeList:
		n = len(e.Key)
		if e.List != nil {
			e.List.ForEach(func(v string) bool {
				n += len(v)
				return true
			})
		}
	case TypeHash:
		n = len(e.Key)
		for f, v := range e.Hash {
			n += len(f) + len(v)
		}
	case TypeSet:
		n = len(e.Key)
		for m := range e.Set {
			n += len(m)
		}
	case TypeZSet:
		n = len(e.Key)
		if e.ZSet != nil {
			for _, m := range e.ZSet.members() {
				n += len(m) + 8
			}
		}
	case TypeStream:
		n = len(e.Key)
		if e.Stream != nil {
			n += e.Stream.approxBytes()
		}
	case TypeVector:
		n = len(e.Key)
		if e.Vector != nil && e.Vector.Index != nil {
			n += int(e.Vector.Index.MemUsage())
		}
	}
	e.Bytes = n
	s.bytes.Add(int64(n - old))
}

// globMatch is a tiny glob matcher supporting *, ?, and [abc] — enough
// for KEYS pattern support without pulling in a full regex.
func globMatch(pattern, s string) bool {
	return matchRunes([]rune(pattern), []rune(s))
}

func matchRunes(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchRunes(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		case '[':
			close := -1
			for i := 1; i < len(p); i++ {
				if p[i] == ']' {
					close = i
					break
				}
			}
			if close == -1 || len(s) == 0 {
				return false
			}
			ok := false
			for _, r := range p[1:close] {
				if r == s[0] {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
			p, s = p[close+1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

// wrongTypeMsg is a helper used by command handlers to format errors.
func wrongTypeMsg(e *Entry) string {
	return fmt.Sprintf("WRONGTYPE key holds %s, expected different type", e.Type)
}

// normalizeRange clamps Redis-style [start,stop] indices (negatives count
// from the end, inclusive stop) against a length n. Returns empty = true
// when the clamped range is empty.
func normalizeRange(start, stop, n int) (int, int, bool) {
	if n == 0 {
		return 0, 0, true
	}
	if start < 0 {
		start = n + start
	}
	if stop < 0 {
		stop = n + stop
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}
	if start > stop || start >= n {
		return 0, 0, true
	}
	return start, stop, false
}

// PeekTouchState reads (Hits, LastRead) without bumping either —
// used by CLIENT NO-TOUCH to snapshot per-key touch state before a
// read so it can be restored afterward. ok=false when the key is
// missing (or expired) at peek time.
func (s *Store) PeekTouchState(key string) (uint64, time.Time, bool) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		return 0, time.Time{}, false
	}
	return atomic.LoadUint64(&e.Hits), e.LastRead, true
}

// RestoreTouchState writes (hits, lastRead) back onto an entry —
// the inverse of PeekTouchState. Silently no-ops on missing or
// type-changed entries (a concurrent writer may have replaced the
// value between the snapshot and the restore).
func (s *Store) RestoreTouchState(key string, hits uint64, lastRead time.Time) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data[key]
	if !ok {
		return
	}
	atomic.StoreUint64(&e.Hits, hits)
	e.LastRead = lastRead
}

// joinErr wraps the underlying error with command context for nicer logs.
func joinErr(cmd string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", strings.ToUpper(cmd), err)
}

// assert silences unused-import warnings for new deps during staged builds.
var _ = joinErr
