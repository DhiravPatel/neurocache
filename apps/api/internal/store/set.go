package store

import (
	"errors"
	"math/rand"
)

// SAdd inserts members into the set; returns the number of new members.
func (s *Store) SAdd(key string, members ...string) (int, error) {
	if len(members) == 0 {
		return 0, errors.New("SADD requires at least one member")
	}
	s.mu.Lock()
	e, err := s.getOrCreate(key, TypeSet)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	added := 0
	delta := 0
	for _, m := range members {
		if _, exists := e.Set[m]; !exists {
			e.Set[m] = struct{}{}
			added++
			delta += len(m)
		}
	}
	s.addBytes(e, delta)
	s.mu.Unlock()
	s.fire("sadd", key)
	return added, nil
}

// SRem deletes members; returns the count actually removed.
func (s *Store) SRem(key string, members ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return 0, err
	}
	removed := 0
	delta := 0
	for _, m := range members {
		if _, exists := e.Set[m]; exists {
			delete(e.Set, m)
			removed++
			delta -= len(m)
		}
	}
	s.addBytes(e, delta)
	s.removeIfEmpty(e)
	return removed, nil
}

// SIsMember reports membership.
func (s *Store) SIsMember(key, member string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return false, err
	}
	_, exists := e.Set[member]
	return exists, nil
}

// SMembers returns every member.
func (s *Store) SMembers(key string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return []string{}, err
	}
	out := make([]string, 0, len(e.Set))
	for m := range e.Set {
		out = append(out, m)
	}
	return out, nil
}

// SCard returns set cardinality, 0 when missing.
func (s *Store) SCard(key string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return 0, err
	}
	return len(e.Set), nil
}

// SPop removes and returns a random member.
func (s *Store) SPop(key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return "", false, err
	}
	for m := range e.Set {
		delete(e.Set, m)
		s.addBytes(e, -len(m))
		s.removeIfEmpty(e)
		return m, true, nil
	}
	return "", false, nil
}

// SRandMember returns |count| random members without removing them.
// count > 0 returns unique members (clamped to size); count < 0 allows
// repetition. count == 0 returns a single random member as a one-element
// slice (caller passes 1 for "just one").
func (s *Store) SRandMember(key string, count int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return []string{}, err
	}
	members := make([]string, 0, len(e.Set))
	for m := range e.Set {
		members = append(members, m)
	}
	if count == 0 || len(members) == 0 {
		return []string{}, nil
	}
	if count > 0 {
		if count >= len(members) {
			return members, nil
		}
		rand.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
		return members[:count], nil
	}
	n := -count
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = members[rand.Intn(len(members))]
	}
	return out, nil
}

// SMove atomically moves a member from src to dst.
func (s *Store) SMove(src, dst, member string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok, err := s.get(src, TypeSet)
	if err != nil || !ok {
		return false, err
	}
	if _, exists := se.Set[member]; !exists {
		return false, nil
	}
	de, err := s.getOrCreate(dst, TypeSet)
	if err != nil {
		return false, err
	}
	delete(se.Set, member)
	s.addBytes(se, -len(member))
	if _, dup := de.Set[member]; !dup {
		de.Set[member] = struct{}{}
		s.addBytes(de, len(member))
	}
	s.removeIfEmpty(se)
	return true, nil
}

// SInter returns the intersection. Missing keys behave as empty sets.
func (s *Store) SInter(keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return []string{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sets, err := s.loadSets(keys)
	if err != nil {
		return nil, err
	}
	if len(sets) == 0 {
		return []string{}, nil
	}
	// iterate the smallest set for speed
	small := 0
	for i, st := range sets {
		if len(st) < len(sets[small]) {
			small = i
		}
	}
	out := []string{}
next:
	for m := range sets[small] {
		for i, st := range sets {
			if i == small {
				continue
			}
			if _, ok := st[m]; !ok {
				continue next
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// SUnion returns the union of all sets.
func (s *Store) SUnion(keys ...string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sets, err := s.loadSets(keys)
	if err != nil {
		return nil, err
	}
	acc := map[string]struct{}{}
	for _, st := range sets {
		for m := range st {
			acc[m] = struct{}{}
		}
	}
	out := make([]string, 0, len(acc))
	for m := range acc {
		out = append(out, m)
	}
	return out, nil
}

// SDiff returns members in the first set that are absent from the rest.
func (s *Store) SDiff(keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return []string{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sets, err := s.loadSets(keys)
	if err != nil {
		return nil, err
	}
	if len(sets) == 0 {
		return []string{}, nil
	}
	out := []string{}
next:
	for m := range sets[0] {
		for i := 1; i < len(sets); i++ {
			if _, ok := sets[i][m]; ok {
				continue next
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// SInterStore stores the intersection into dst, returning the size.
func (s *Store) SInterStore(dst string, keys ...string) (int, error) {
	out, err := s.SInter(keys...)
	if err != nil {
		return 0, err
	}
	return s.storeSetResult(dst, out)
}

// SUnionStore stores the union into dst, returning the size.
func (s *Store) SUnionStore(dst string, keys ...string) (int, error) {
	out, err := s.SUnion(keys...)
	if err != nil {
		return 0, err
	}
	return s.storeSetResult(dst, out)
}

// SDiffStore stores the diff into dst, returning the size.
func (s *Store) SDiffStore(dst string, keys ...string) (int, error) {
	out, err := s.SDiff(keys...)
	if err != nil {
		return 0, err
	}
	return s.storeSetResult(dst, out)
}

// loadSets dereferences keys into raw member maps. Missing keys are
// treated as empty, wrong-typed keys raise WRONGTYPE.
func (s *Store) loadSets(keys []string) ([]map[string]struct{}, error) {
	out := make([]map[string]struct{}, 0, len(keys))
	for _, k := range keys {
		e, ok, err := s.get(k, TypeSet)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, map[string]struct{}{})
			continue
		}
		out = append(out, e.Set)
	}
	return out, nil
}

// storeSetResult writes a computed member slice into a destination key,
// replacing anything already there. Empty results delete the key.
func (s *Store) storeSetResult(dst string, members []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.data[dst]; ok {
		s.bytes.Add(-int64(old.Bytes))
		delete(s.data, dst)
	}
	if len(members) == 0 {
		return 0, nil
	}
	e, err := s.getOrCreate(dst, TypeSet)
	if err != nil {
		return 0, err
	}
	for _, m := range members {
		e.Set[m] = struct{}{}
	}
	s.recomputeBytes(e)
	return len(members), nil
}
