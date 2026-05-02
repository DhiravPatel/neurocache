package store

import (
	"errors"
	"strconv"
	"time"
)

// Set overwrites key with the given value. ttl == 0 clears any expiry,
// ttl > 0 sets a new one. Any existing non-string value is replaced.
func (s *Store) Set(key, value string, ttl time.Duration) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	if old, ok := sh.data[key]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	now := time.Now()
	e := &Entry{
		Key:       key,
		Type:      TypeString,
		Str:       value,
		CreatedAt: now,
		LastRead:  now,
		Bytes:     len(key) + len(value),
	}
	if ttl > 0 {
		e.ExpireAt = now.Add(ttl)
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
	if e, ok := sh.data[key]; ok && !e.expired(time.Now()) {
		sh.mu.Unlock()
		return false
	}
	now := time.Now()
	e := &Entry{
		Key:       key,
		Type:      TypeString,
		Str:       value,
		CreatedAt: now,
		LastRead:  now,
		Bytes:     len(key) + len(value),
	}
	if ttl > 0 {
		e.ExpireAt = now.Add(ttl)
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
	e.LastRead = time.Now()
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
	e.LastRead = time.Now()
	return e.Str, true, nil
}

// GetSet atomically swaps the value and returns the previous one.
func (s *Store) GetSet(key, value string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	prev := ""
	had := false
	if e, ok := sh.data[key]; ok && !e.expired(time.Now()) {
		if e.Type != TypeString {
			return "", false, ErrWrongType
		}
		prev = e.Str
		had = true
		s.bytes.Add(-int64(e.Bytes))
	}
	now := time.Now()
	e := &Entry{
		Key:       key,
		Type:      TypeString,
		Str:       value,
		CreatedAt: now,
		LastRead:  now,
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
	now := time.Now()
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
				CreatedAt: now, LastRead: now,
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
	now := time.Now()
	keys := make([]string, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		keys = append(keys, pairs[i])
	}
	involved := s.shardsFor(keys)
	unlock := s.lockShardsW(involved)
	defer unlock()
	for i := 0; i < len(pairs); i += 2 {
		sh := s.shardForKey(pairs[i])
		if e, ok := sh.data[pairs[i]]; ok && !e.expired(now) {
			return false, nil
		}
	}
	for i := 0; i < len(pairs); i += 2 {
		sh := s.shardForKey(pairs[i])
		k, v := pairs[i], pairs[i+1]
		e := &Entry{
			Key: k, Type: TypeString, Str: v,
			CreatedAt: now, LastRead: now,
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
	now := time.Now()
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
			if !ok || e.expired(now) {
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
		now := time.Now()
		e = &Entry{Key: key, Type: TypeString, Str: value, CreatedAt: now, LastRead: now, Bytes: len(key) + len(value)}
		sh.data[key] = e
		s.bytes.Add(int64(e.Bytes))
		return len(value), nil
	}
	s.bytes.Add(-int64(e.Bytes))
	e.Str += value
	e.Bytes = len(key) + len(e.Str)
	s.bytes.Add(int64(e.Bytes))
	return len(e.Str), nil
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
		now := time.Now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now, LastRead: now}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = newStr
	e.Bytes = len(key) + len(newStr)
	s.bytes.Add(int64(e.Bytes))
	return len(newStr), nil
}

// Incr adds delta to a numeric string value and returns the new total.
// Creates the key at 0 if missing, errors if existing value isn't numeric.
func (s *Store) Incr(key string, delta int64) (int64, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
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
		now := time.Now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now, LastRead: now}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = v
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
		now := time.Now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now, LastRead: now}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = v
	e.Bytes = len(key) + len(v)
	s.bytes.Add(int64(e.Bytes))
	return cur, nil
}
