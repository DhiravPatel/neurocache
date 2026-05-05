package store

import (
	"errors"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store/clist"
)

// LPush prepends one or more values to a list. Creates the list if absent.
// Returns the new length.
func (s *Store) LPush(key string, values ...string) (int, error) {
	if len(values) == 0 {
		return 0, errors.New("LPUSH requires at least one value")
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	e, err := s.getOrCreate(sh, key, TypeList)
	if err != nil {
		sh.mu.Unlock()
		return 0, err
	}
	delta := 0
	for _, v := range values {
		e.List.PushFront(v)
		delta += len(v)
	}
	s.addBytes(e, delta)
	n := e.List.Len()
	sh.mu.Unlock()
	s.fire("lpush", key)
	return n, nil
}

// RPush appends values to a list. Returns the new length.
func (s *Store) RPush(key string, values ...string) (int, error) {
	if len(values) == 0 {
		return 0, errors.New("RPUSH requires at least one value")
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	e, err := s.getOrCreate(sh, key, TypeList)
	if err != nil {
		sh.mu.Unlock()
		return 0, err
	}
	delta := 0
	for _, v := range values {
		e.List.PushBack(v)
		delta += len(v)
	}
	s.addBytes(e, delta)
	n := e.List.Len()
	sh.mu.Unlock()
	s.fire("rpush", key)
	return n, nil
}

// LPushX prepends only if the key already exists. Returns 0 when missing.
func (s *Store) LPushX(key, value string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return 0, err
	}
	e.List.PushFront(value)
	s.addBytes(e, len(value))
	return e.List.Len(), nil
}

// RPushX appends only if the key already exists.
func (s *Store) RPushX(key, value string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return 0, err
	}
	e.List.PushBack(value)
	s.addBytes(e, len(value))
	return e.List.Len(), nil
}

// LPop removes and returns the head. (value, hit, err).
func (s *Store) LPop(key string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return "", false, err
	}
	front := e.List.Front()
	if front == nil {
		return "", false, nil
	}
	v := front.Value
	e.List.Remove(front)
	s.addBytes(e, -len(v))
	s.removeIfEmpty(sh, e)
	return v, true, nil
}

// RPop removes and returns the tail.
func (s *Store) RPop(key string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return "", false, err
	}
	back := e.List.Back()
	if back == nil {
		return "", false, nil
	}
	v := back.Value
	e.List.Remove(back)
	s.addBytes(e, -len(v))
	s.removeIfEmpty(sh, e)
	return v, true, nil
}

// LLen returns the list length, 0 if missing.
func (s *Store) LLen(key string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return 0, err
	}
	return e.List.Len(), nil
}

// LIndex returns the element at index (supports negatives).
func (s *Store) LIndex(key string, index int) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return "", false, err
	}
	n := e.List.Len()
	if index < 0 {
		index = n + index
	}
	if index < 0 || index >= n {
		return "", false, nil
	}
	el := e.List.Front()
	for i := 0; i < index; i++ {
		el = el.Next()
	}
	return el.Value, true, nil
}

// LRange returns elements in [start,stop] with negative indices supported.
func (s *Store) LRange(key string, start, stop int) ([]string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return nil, err
	}
	n := e.List.Len()
	a, b, empty := normalizeRange(start, stop, n)
	if empty {
		return []string{}, nil
	}
	out := make([]string, 0, b-a+1)
	el := e.List.Front()
	for i := 0; i < a; i++ {
		el = el.Next()
	}
	for i := a; i <= b && el != nil; i++ {
		out = append(out, el.Value)
		el = el.Next()
	}
	return out, nil
}

// LSet overwrites the element at index. Errors when out of range.
func (s *Store) LSet(key string, index int, value string) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("ERR no such key")
	}
	n := e.List.Len()
	if index < 0 {
		index = n + index
	}
	if index < 0 || index >= n {
		return errors.New("ERR index out of range")
	}
	el := e.List.Front()
	for i := 0; i < index; i++ {
		el = el.Next()
	}
	old := el.Value
	el.Value = value
	s.addBytes(e, len(value)-len(old))
	return nil
}

// LRem removes up to |count| occurrences of value. count > 0 from head,
// count < 0 from tail, count == 0 removes all. Returns removed count.
func (s *Store) LRem(key string, count int, value string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return 0, err
	}
	removed := 0
	limit := count
	if count < 0 {
		limit = -count
	}
	bytesRemoved := 0
	walk := func(fwd bool) {
		var next func(*clist.Element) *clist.Element
		var start *clist.Element
		if fwd {
			start = e.List.Front()
			next = (*clist.Element).Next
		} else {
			start = e.List.Back()
			next = (*clist.Element).Prev
		}
		for el := start; el != nil; {
			n := next(el)
			if el.Value == value {
				bytesRemoved += len(value)
				e.List.Remove(el)
				removed++
				if count != 0 && removed >= limit {
					return
				}
			}
			el = n
		}
	}
	if count >= 0 {
		walk(true)
	} else {
		walk(false)
	}
	s.addBytes(e, -bytesRemoved)
	s.removeIfEmpty(sh, e)
	return removed, nil
}

// LTrim trims the list to the inclusive [start,stop] range.
func (s *Store) LTrim(key string, start, stop int) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil || !ok {
		return err
	}
	n := e.List.Len()
	a, b, empty := normalizeRange(start, stop, n)
	if empty {
		e.List.Init()
		s.recomputeBytes(e)
		s.removeIfEmpty(sh, e)
		return nil
	}
	keep := clist.New()
	el := e.List.Front()
	for i := 0; i < a; i++ {
		el = el.Next()
	}
	for i := a; i <= b && el != nil; i++ {
		keep.PushBack(el.Value)
		el = el.Next()
	}
	e.List = keep
	s.recomputeBytes(e)
	s.removeIfEmpty(sh, e)
	return nil
}

// LInsert inserts value before/after pivot. Returns new length, or -1 if
// pivot is not found, or 0 if the key is missing.
func (s *Store) LInsert(key string, before bool, pivot, value string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeList)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	for el := e.List.Front(); el != nil; el = el.Next() {
		if el.Value == pivot {
			if before {
				e.List.InsertBefore(value, el)
			} else {
				e.List.InsertAfter(value, el)
			}
			s.addBytes(e, len(value))
			return e.List.Len(), nil
		}
	}
	return -1, nil
}

// RPopLPush atomically pops from src's tail and pushes onto dst's head.
func (s *Store) RPopLPush(src, dst string) (string, bool, error) {
	shS, shD, unlock := s.lockTwoW(src, dst)
	defer unlock()
	se, ok, err := shS.get(src, TypeList)
	if err != nil || !ok {
		return "", false, err
	}
	back := se.List.Back()
	if back == nil {
		return "", false, nil
	}
	v := back.Value
	se.List.Remove(back)

	de, err := s.getOrCreate(shD, dst, TypeList)
	if err != nil {
		// restore on failure so the pop is observed only when the push succeeds
		se.List.PushBack(v)
		return "", false, err
	}
	de.List.PushFront(v)
	s.addBytes(se, -len(v))
	s.addBytes(de, len(v))
	s.removeIfEmpty(shS, se)
	return v, true, nil
}
