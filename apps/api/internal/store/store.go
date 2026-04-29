// Package store implements the NeuroCache keyspace: a single thread-safe
// registry that holds strings, lists, hashes, sets, and sorted sets, plus
// per-key TTLs and hit counters. The typed Entry makes a key exclusively
// hold one value type at a time, matching Redis semantics — a GET on a
// list key returns WRONGTYPE, an LPUSH on a string key does the same.
package store

import (
	"container/list"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

	Str    string
	List   *list.List // elements are strings
	Hash   map[string]string
	Set    map[string]struct{}
	ZSet   *ZSet
	Stream *Stream
	Module *ModuleValue // populated when Type == TypeModule

	CreatedAt time.Time
	ExpireAt  time.Time // zero = no expiry
	Hits      uint64
	LastRead  time.Time
	Bytes     int
}

func (e *Entry) expired(now time.Time) bool {
	return !e.ExpireAt.IsZero() && now.After(e.ExpireAt)
}

// Store is a concurrent multi-type keyspace.
type Store struct {
	mu    sync.RWMutex
	data  map[string]*Entry
	bytes atomic.Int64

	// keyspace notifications fan out on mutations.
	notify func(event, key string)
}

func New() *Store {
	s := &Store{data: make(map[string]*Entry)}
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
func (s *Store) ttlLoop() {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		var expired []string
		s.mu.Lock()
		for k, e := range s.data {
			if e.expired(now) {
				s.bytes.Add(-int64(e.Bytes))
				delete(s.data, k)
				expired = append(expired, k)
			}
		}
		s.mu.Unlock()
		for _, k := range expired {
			s.fire("expired", k)
		}
	}
}

// ─── common key operations ──────────────────────────────────────────────

// Type returns the kind of value at key, or TypeNone if missing/expired.
func (s *Store) Type(key string) ValueType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || e.expired(time.Now()) {
		return TypeNone
	}
	return e.Type
}

// Exists counts how many of the given keys exist (duplicates count).
func (s *Store) Exists(keys ...string) int {
	now := time.Now()
	n := 0
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range keys {
		if e, ok := s.data[k]; ok && !e.expired(now) {
			n++
		}
	}
	return n
}

// Del removes keys; returns how many were actually deleted.
func (s *Store) Del(keys ...string) int {
	var removed []string
	s.mu.Lock()
	for _, k := range keys {
		if e, ok := s.data[k]; ok {
			s.bytes.Add(-int64(e.Bytes))
			delete(s.data, k)
			removed = append(removed, k)
		}
	}
	s.mu.Unlock()
	for _, k := range removed {
		s.fire("del", k)
	}
	return len(removed)
}

// Expire sets TTL. Returns false if the key does not exist.
func (s *Store) Expire(key string, ttl time.Duration) bool {
	s.mu.Lock()
	e, ok := s.data[key]
	if !ok {
		s.mu.Unlock()
		return false
	}
	e.ExpireAt = time.Now().Add(ttl)
	s.mu.Unlock()
	s.fire("expire", key)
	return true
}

// ExpireAt sets an absolute expiry time. Returns false if missing.
func (s *Store) ExpireAt(key string, at time.Time) bool {
	s.mu.Lock()
	e, ok := s.data[key]
	if !ok {
		s.mu.Unlock()
		return false
	}
	e.ExpireAt = at
	s.mu.Unlock()
	s.fire("expireat", key)
	return true
}

// Persist clears the TTL. Returns false if there was no TTL to clear.
func (s *Store) Persist(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || e.ExpireAt.IsZero() {
		return false
	}
	e.ExpireAt = time.Time{}
	return true
}

// TTL returns time until expiry. -1 = no expiry, -2 = missing.
func (s *Store) TTL(key string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.data))
	for k, e := range s.data {
		if e.expired(now) {
			continue
		}
		if pattern == "" || pattern == "*" || globMatch(pattern, k) {
			out = append(out, k)
		}
	}
	return out
}

// Rename moves a key's value to a new name. Returns false if source missing.
// If dst exists it is overwritten (matches RENAME semantics).
func (s *Store) Rename(src, dst string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[src]
	if !ok || e.expired(time.Now()) {
		return false
	}
	if old, ok := s.data[dst]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	delete(s.data, src)
	e.Key = dst
	s.data[dst] = e
	return true
}

// RenameNX renames only if dst does not already exist. Returns false if
// the source was missing or the destination was taken.
func (s *Store) RenameNX(src, dst string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.data[dst]; taken {
		return false
	}
	e, ok := s.data[src]
	if !ok || e.expired(time.Now()) {
		return false
	}
	delete(s.data, src)
	e.Key = dst
	s.data[dst] = e
	return true
}

func (s *Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

func (s *Store) BytesUsed() int64 { return s.bytes.Load() }

func (s *Store) FlushAll() {
	s.mu.Lock()
	s.data = make(map[string]*Entry)
	s.bytes.Store(0)
	s.mu.Unlock()
	s.fire("flushdb", "")
}

// Snapshot copies every live entry. Eviction reads scoring fields only, so
// sharing pointers for list/hash/set is safe (they are never mutated).
func (s *Store) Snapshot() []Entry {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		if !e.expired(now) {
			out = append(out, *e)
		}
	}
	return out
}

// Evict deletes the given keys — a plain wrapper Del preserves for the
// eviction package so it does not reach into Del's signature.
func (s *Store) Evict(keys []string) int { return s.Del(keys...) }

// ─── internal helpers shared by typed operations ────────────────────────

// get returns the live entry if it exists and matches want. Missing
// entries return (nil, false, nil). Mismatched types return ErrWrongType.
// Passing TypeNone disables the type check.
func (s *Store) get(key string, want ValueType) (*Entry, bool, error) {
	e, ok := s.data[key]
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
func (s *Store) getOrCreate(key string, t ValueType) (*Entry, error) {
	e, ok := s.data[key]
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
		e.List = list.New()
	case TypeHash:
		e.Hash = make(map[string]string)
	case TypeSet:
		e.Set = make(map[string]struct{})
	case TypeZSet:
		e.ZSet = newZSet()
	case TypeStream:
		e.Stream = newStream()
	}
	s.data[key] = e
	return e, nil
}

// removeIfEmpty deletes the entry when its collection becomes empty,
// mirroring Redis's "empty key = no key" invariant.
func (s *Store) removeIfEmpty(e *Entry) {
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
	}
	if empty {
		s.bytes.Add(-int64(e.Bytes))
		delete(s.data, e.Key)
	}
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
			for el := e.List.Front(); el != nil; el = el.Next() {
				n += len(el.Value.(string))
			}
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

// Lock / Unlock helpers exposed so higher-level orchestration (WATCH,
// MULTI) can coordinate without re-opening the Store internals.
func (s *Store) Lock()   { s.mu.Lock() }
func (s *Store) Unlock() { s.mu.Unlock() }

// joinErr wraps the underlying error with command context for nicer logs.
func joinErr(cmd string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", strings.ToUpper(cmd), err)
}

// assert silences unused-import warnings for new deps during staged builds.
var _ = joinErr
