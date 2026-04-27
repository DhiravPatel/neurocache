package store

import "time"

// LMove atomically pops from one end of src and pushes onto one end of
// dst. Direction flags use Redis's LEFT/RIGHT vocabulary mapped to
// boolean ends: srcRight=true → tail-pop, dstRight=true → tail-push.
//
// Returns ("", false) when src is missing or empty. Returns
// ErrWrongType if either key holds a non-list value.
//
// Atomicity: a single Store.mu critical section spans the pop and the
// push so concurrent observers never see the value in neither list.
// src == dst is supported (rotation): LMove("a", "a", true, false)
// behaves like a single-element rotate from tail to head.
func (s *Store) LMove(src, dst string, srcRight, dstRight bool) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok, err := s.get(src, TypeList)
	if err != nil || !ok {
		return "", false, err
	}
	if se.List.Len() == 0 {
		return "", false, nil
	}
	// pop the source side
	var v string
	if srcRight {
		back := se.List.Back()
		v = back.Value.(string)
		se.List.Remove(back)
	} else {
		front := se.List.Front()
		v = front.Value.(string)
		se.List.Remove(front)
	}
	// push to destination — must succeed before we settle src bookkeeping;
	// otherwise a wrong-type dst would silently swallow the popped value.
	de, err := s.getOrCreate(dst, TypeList)
	if err != nil {
		// restore the popped value so callers don't see a half-applied op
		if srcRight {
			se.List.PushBack(v)
		} else {
			se.List.PushFront(v)
		}
		return "", false, err
	}
	if dstRight {
		de.List.PushBack(v)
	} else {
		de.List.PushFront(v)
	}
	s.recomputeBytes(se)
	if se != de {
		s.recomputeBytes(de)
	}
	s.removeIfEmpty(se)
	return v, true, nil
}

// Touch refreshes the LastRead timestamp for each existing key and
// returns the count actually touched. Mirrors Redis TOUCH — useful when
// a key's idle time matters for an LRU policy and the caller wants to
// say "this key is hot" without reading its value.
func (s *Store) Touch(keys ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	n := 0
	for _, k := range keys {
		e, ok := s.data[k]
		if !ok || e.expired(now) {
			continue
		}
		e.LastRead = now
		n++
	}
	return n
}

// ExpireTime returns the absolute expiry as a Unix-second timestamp.
//
//   -2 → key does not exist
//   -1 → key exists, no TTL
//    n → expiry as Unix epoch seconds
func (s *Store) ExpireTime(key string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || e.expired(time.Now()) {
		return -2
	}
	if e.ExpireAt.IsZero() {
		return -1
	}
	return e.ExpireAt.Unix()
}

// PExpireTime is ExpireTime in milliseconds.
func (s *Store) PExpireTime(key string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || e.expired(time.Now()) {
		return -2
	}
	if e.ExpireAt.IsZero() {
		return -1
	}
	return e.ExpireAt.UnixMilli()
}
