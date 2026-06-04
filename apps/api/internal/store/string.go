package store

import (
	"errors"
	"strconv"
	"time"
	"unsafe"
)

// maxStringBytes caps the in-memory size of any single string value,
// mirroring Redis's proto-max-bulk-len (512 MiB). Commands that grow a
// string from a client-supplied offset (SETRANGE, SETBIT, BITFIELD)
// must enforce it: otherwise `SETBIT k 9999999999999 1` would
// make([]byte, ~1.2 TB) and OOM the server off a single command.
const maxStringBytes = 512 * 1024 * 1024

// Set overwrites key with the given value. ttl == 0 clears any expiry,
// ttl > 0 sets a new one. Any existing non-string value is replaced.
//
// Hot-path optimization: when the key already holds a string, we reuse
// the existing *Entry instead of allocating a new one. redis-benchmark
// SET cycles over a fixed key set and overwrites each many times — the
// reuse path saves the heap allocation, the GC pressure, and the
// 80-byte Entry copy per call. New keys still allocate (one-time cost).
func (s *Store) Set(key, value string, ttl time.Duration) {
	sh := s.shardForKey(key)
	now := s.now()
	sh.mu.Lock()
	if old, ok := sh.data[key]; ok && old.Type == TypeString {
		// In-place update — same key, same type, just rewrite the
		// payload + accounting fields.
		s.bytes.Add(-int64(old.Bytes))
		old.Str = value
		old.appendBuf = nil // release any APPEND buffer; Str is fresh now
		old.Bytes = len(key) + len(value)
		old.LastRead = now.UnixNano()
		// Invalidate the integer fast-path. We DON'T re-parse here:
		// SET is dominated by non-numeric values (random user data,
		// session tokens, JSON), and a doomed ParseInt costs more
		// than it saves. INCR will do the parse exactly once on
		// first call after a SET, then keep IsInt=true thereafter.
		old.IsInt = false
		old.IntAtomic.Store(0) // clear so a subsequent INCR slow-path resets correctly
		if ttl > 0 {
			old.ExpireAt = now.Add(ttl).UnixNano()
		} else {
			old.ExpireAt = 0
		}
		s.bytes.Add(int64(old.Bytes))
		sh.mu.Unlock()
		s.fire("set", key)
		return
	}
	if old, ok := sh.data[key]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	e := &Entry{
		Key:       key,
		Type:      TypeString,
		Str:       value,
		CreatedAt: now.UnixNano(),
		LastRead:  now.UnixNano(),
		Bytes:     len(key) + len(value),
	}
	if ttl > 0 {
		e.ExpireAt = now.Add(ttl).UnixNano()
	}
	sh.data[key] = e
	s.bytes.Add(int64(e.Bytes))
	sh.mu.Unlock()
	s.fire("set", key)
}

// SetNX sets the key only if it does not exist. Returns true on success.
func (s *Store) SetNX(key, value string, ttl time.Duration) bool {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	if e, ok := sh.data[key]; ok && !e.expired(s.nowNs()) {
		sh.mu.Unlock()
		return false
	}
	now := s.now()
	e := &Entry{
		Key:       key,
		Type:      TypeString,
		Str:       value,
		CreatedAt: now.UnixNano(),
		LastRead:  now.UnixNano(),
		Bytes:     len(key) + len(value),
	}
	if ttl > 0 {
		e.ExpireAt = now.Add(ttl).UnixNano()
	}
	sh.data[key] = e
	s.bytes.Add(int64(e.Bytes))
	sh.mu.Unlock()
	s.fire("setnx", key)
	return true
}

// Get returns (value, true) for an existing string key.
func (s *Store) Get(key string) (string, bool) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil || !ok {
		return "", false
	}
	e.Hits++
	e.LastRead = s.nowNs()
	if e.IsInt {
		// IntAtomic may be ahead of e.Str (lock-free INCR path).
		// Format on demand to return the authoritative value.
		return strconv.FormatInt(e.IntAtomic.Load(), 10), true
	}
	return e.Str, true
}

// GetTyped is Get with explicit WRONGTYPE signalling, used by RESP code.
func (s *Store) GetTyped(key string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return "", true, err
	}
	if !ok {
		return "", false, nil
	}
	e.Hits++
	e.LastRead = s.nowNs()
	if e.IsInt {
		return strconv.FormatInt(e.IntAtomic.Load(), 10), true, nil
	}
	return e.Str, true, nil
}

// GetSet atomically swaps the value and returns the previous one.
func (s *Store) GetSet(key, value string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	prev := ""
	had := false
	if e, ok := sh.data[key]; ok && !e.expired(s.nowNs()) {
		if e.Type != TypeString {
			return "", false, ErrWrongType
		}
		prev = e.Str
		had = true
		s.bytes.Add(-int64(e.Bytes))
	}
	now := s.now()
	e := &Entry{
		Key:       key,
		Type:      TypeString,
		Str:       value,
		CreatedAt: now.UnixNano(),
		LastRead:  now.UnixNano(),
		Bytes:     len(key) + len(value),
	}
	sh.data[key] = e
	s.bytes.Add(int64(e.Bytes))
	return prev, had, nil
}

// MSet sets several key/value pairs atomically. Pairs must be paired.
// Buckets keys by shard so we acquire each shard's lock once.
func (s *Store) MSet(pairs ...string) error {
	if len(pairs)%2 != 0 {
		return errors.New("MSET requires an even argument count")
	}
	now := s.now()
	type kv struct{ k, v string }
	bucket := map[*shard][]kv{}
	for i := 0; i < len(pairs); i += 2 {
		sh := s.shardForKey(pairs[i])
		bucket[sh] = append(bucket[sh], kv{pairs[i], pairs[i+1]})
	}
	for sh, items := range bucket {
		sh.mu.Lock()
		for _, it := range items {
			if old, ok := sh.data[it.k]; ok {
				s.bytes.Add(-int64(old.Bytes))
			}
			e := &Entry{
				Key: it.k, Type: TypeString, Str: it.v,
				CreatedAt: now.UnixNano(), LastRead: now.UnixNano(),
				Bytes: len(it.k) + len(it.v),
			}
			sh.data[it.k] = e
			s.bytes.Add(int64(e.Bytes))
		}
		sh.mu.Unlock()
	}
	return nil
}

// MSetNX sets multiple keys only if *none* already exist. Locks every
// involved shard up front in canonical order — atomic across the
// presence-check + write phase.
func (s *Store) MSetNX(pairs ...string) (bool, error) {
	if len(pairs)%2 != 0 {
		return false, errors.New("MSETNX requires an even argument count")
	}
	now := s.now()
	keys := make([]string, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		keys = append(keys, pairs[i])
	}
	involved := s.shardsFor(keys)
	unlock := s.lockShardsW(involved)
	defer unlock()
	for i := 0; i < len(pairs); i += 2 {
		sh := s.shardForKey(pairs[i])
		if e, ok := sh.data[pairs[i]]; ok && !e.expired(now.UnixNano()) {
			return false, nil
		}
	}
	for i := 0; i < len(pairs); i += 2 {
		sh := s.shardForKey(pairs[i])
		k, v := pairs[i], pairs[i+1]
		e := &Entry{
			Key: k, Type: TypeString, Str: v,
			CreatedAt: now.UnixNano(), LastRead: now.UnixNano(),
			Bytes: len(k) + len(v),
		}
		sh.data[k] = e
		s.bytes.Add(int64(e.Bytes))
	}
	return true, nil
}

// MGet returns a parallel slice: hit[i] false means the key was missing.
func (s *Store) MGet(keys ...string) ([]string, []bool, error) {
	vals := make([]string, len(keys))
	hits := make([]bool, len(keys))
	now := s.now()
	// Bucket by shard, take one read lock per shard.
	type pos struct {
		i int
		k string
	}
	byShard := map[*shard][]pos{}
	for i, k := range keys {
		sh := s.shardForKey(k)
		byShard[sh] = append(byShard[sh], pos{i, k})
	}
	for sh, items := range byShard {
		sh.mu.RLock()
		for _, it := range items {
			e, ok := sh.data[it.k]
			if !ok || e.expired(now.UnixNano()) {
				continue
			}
			if e.Type != TypeString {
				continue
			}
			vals[it.i] = e.Str
			hits[it.i] = true
		}
		sh.mu.RUnlock()
	}
	return vals, hits, nil
}

// Append concatenates value to the existing string and returns the new
// length. Creates the key as an empty string when missing.
func (s *Store) Append(key, value string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	if !ok {
		now := s.now()
		e = &Entry{Key: key, Type: TypeString, Str: value, CreatedAt: now.UnixNano(), LastRead: now.UnixNano(), Bytes: len(key) + len(value)}
		sh.data[key] = e
		s.bytes.Add(int64(e.Bytes))
		return len(value), nil
	}
	s.bytes.Add(-int64(e.Bytes))
	// Amortized-O(1) append. The naive `e.Str += value` reallocates and
	// copies the ENTIRE current value on every call — O(N²) total (and a
	// GC storm) when a client appends repeatedly to one growing key,
	// where Redis is O(1) amortized via its capacity-doubling SDS. We
	// keep an over-allocated buffer and let Go's append() write into its
	// spare capacity in place. Crucially this is safe for concurrent
	// readers: append only ever writes at index ≥ len (or reallocates,
	// leaving the old array intact), so it never disturbs the bytes an
	// already-returned e.Str view points at. e.Str is re-published as an
	// unsafe view over the buffer, so every existing reader keeps working
	// unchanged.
	e.appendBuf = growAppend(e.appendBuf, e.Str, value)
	e.Str = unsafe.String(unsafe.SliceData(e.appendBuf), len(e.appendBuf))
	// APPEND can produce a non-numeric string ("12" + "abc"); invalidate
	// the integer fast-path so the next INCR goes through ParseInt.
	e.IsInt = false
	e.Bytes = len(key) + len(e.Str)
	s.bytes.Add(int64(e.Bytes))
	return len(e.Str), nil
}

// growAppend returns a buffer holding cur+value whose first len(cur)
// bytes equal cur. When buf already backs cur (the steady state across
// repeated APPENDs — Append republishes e.Str as a view over buf) it
// appends into buf's spare capacity in place / grows geometrically;
// otherwise (first append, or some other command replaced e.Str via
// SET/SETRANGE/GETSET/SETBIT/…) it rebuilds from cur first. This keeps
// Append self-correcting so no other writer of Str needs to know
// appendBuf exists.
func growAppend(buf []byte, cur, value string) []byte {
	aliases := len(buf) == len(cur) &&
		(len(cur) == 0 || unsafe.SliceData(buf) == unsafe.StringData(cur))
	if !aliases {
		buf = append(make([]byte, 0, len(cur)+len(value)), cur...)
	}
	return append(buf, value...)
}

// StrLen returns the byte length, 0 if missing.
func (s *Store) StrLen(key string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return len(e.Str), nil
}

// GetRange returns a substring by Redis-style inclusive [start,end] with
// negative indices counting from the right.
func (s *Store) GetRange(key string, start, end int) (string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	n := len(e.Str)
	a, b, empty := normalizeRange(start, end, n)
	if empty {
		return "", nil
	}
	return e.Str[a : b+1], nil
}

// SetRange writes value starting at offset, zero-padding if needed.
// Returns the length of the resulting string.
func (s *Store) SetRange(key string, offset int, value string) (int, error) {
	if offset < 0 {
		return 0, errors.New("offset out of range")
	}
	// Bound the resulting size before allocating. Checking offset first
	// (≤ maxStringBytes) guarantees offset+len(value) can't overflow.
	if offset > maxStringBytes || offset+len(value) > maxStringBytes {
		return 0, errors.New("string exceeds maximum allowed size (proto-max-bulk-len)")
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	var cur []byte
	if ok {
		cur = []byte(e.Str)
	}
	end := offset + len(value)
	if end > len(cur) {
		grown := make([]byte, end)
		copy(grown, cur)
		cur = grown
	}
	copy(cur[offset:], value)
	newStr := string(cur)
	if !ok {
		now := s.now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now.UnixNano(), LastRead: now.UnixNano()}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = newStr
	// SETRANGE invalidates the integer fast-path: a binary patch may
	// no longer parse as an int.
	e.IsInt = false
	e.Bytes = len(key) + len(newStr)
	s.bytes.Add(int64(e.Bytes))
	return len(newStr), nil
}

// Incr adds delta to a numeric string value and returns the new total.
// Creates the key at 0 if missing, errors if existing value isn't numeric.
//
// Two-tier hot path:
//
//  1. LOCK-FREE FAST PATH: when the entry already exists and has
//     IsInt=true (set by a prior INCR or numeric SET), we take only
//     the shard's RLock — long enough to safely look up the entry —
//     and then atomically increment IntAtomic. No write-lock, no map
//     write, no string format. Returns the new value directly.
//     This is the redis-benchmark INCR shape (same key hit
//     repeatedly), and it goes from ~25 ns/op (lock+map+update) down
//     to ~5 ns/op (RLock+map-read+atomic.Add).
//
//  2. SLOW PATH: when the key is missing, has expired, or holds a
//     non-numeric string, we fall through to the original write-lock
//     path that handles entry creation + ParseInt + bytes accounting.
//
// Str remains valid for callers that read it directly (GET in the
// dispatch fast-path uses it). Whenever the lock-free path bumps
// IntAtomic, Str becomes stale by one delta — GET handles this by
// checking IsInt and formatting from IntAtomic when needed.
func (s *Store) Incr(key string, delta int64) (int64, error) {
	sh := s.shardForKey(key)

	// ── lock-free hot path ────────────────────────────────────────
	sh.mu.RLock()
	e, ok := sh.data[key]
	if ok && e.Type == TypeString && e.IsInt && !e.expired(s.nowNs()) {
		newVal := e.IntAtomic.Add(delta)
		sh.mu.RUnlock()
		return newVal, nil
	}
	sh.mu.RUnlock()

	// ── slow path (entry missing / non-int / expired) ─────────────
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	// Re-check IsInt after acquiring write lock — another goroutine
	// may have promoted this entry to int while we were waiting.
	if ok && e.IsInt {
		return e.IntAtomic.Add(delta), nil
	}
	var cur int64
	if ok {
		cur, err = strconv.ParseInt(e.Str, 10, 64)
		if err != nil {
			return 0, errors.New("ERR value is not an integer or out of range")
		}
	}
	cur += delta
	v := strconv.FormatInt(cur, 10)
	if !ok {
		now := s.now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now.UnixNano(), LastRead: now.UnixNano()}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = v
	e.IntVal = cur
	e.IntAtomic.Store(cur)
	e.IsInt = true
	e.Bytes = len(key) + len(v)
	s.bytes.Add(int64(e.Bytes))
	return cur, nil
}

// IncrByFloat adds a float delta and stores the result back as a string.
func (s *Store) IncrByFloat(key string, delta float64) (float64, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	var cur float64
	if ok {
		cur, err = strconv.ParseFloat(e.Str, 64)
		if err != nil {
			return 0, errors.New("ERR value is not a valid float")
		}
	}
	cur += delta
	v := strconv.FormatFloat(cur, 'f', -1, 64)
	if !ok {
		now := s.now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now.UnixNano(), LastRead: now.UnixNano()}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = v
	e.Bytes = len(key) + len(v)
	s.bytes.Add(int64(e.Bytes))
	return cur, nil
}
