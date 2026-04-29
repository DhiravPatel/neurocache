package store

import (
	"errors"
	"strings"
	"time"
)

// SMIsMember reports membership for several elements at once. Returns
// a parallel bool slice — true at index i means members[i] was found.
// Missing keys behave as empty sets (every result false).
func (s *Store) SMIsMember(key string, members ...string) ([]bool, error) {
	out := make([]bool, len(members))
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeSet)
	if err != nil || !ok {
		return out, err
	}
	for i, m := range members {
		_, found := e.Set[m]
		out[i] = found
	}
	return out, nil
}

// SInterCard returns the intersection cardinality without materialising
// the result. limit > 0 caps the count — useful for "do these sets
// share at least N members?" queries.
func (s *Store) SInterCard(keys []string, limit int) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sets, err := s.loadSets(keys)
	if err != nil {
		return 0, err
	}
	if len(sets) == 0 {
		return 0, nil
	}
	small := 0
	for i, st := range sets {
		if len(st) < len(sets[small]) {
			small = i
		}
	}
	count := 0
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
		count++
		if limit > 0 && count >= limit {
			return count, nil
		}
	}
	return count, nil
}

// GetDel atomically reads + deletes a key. Returns (value, true) on
// hit, ("", false) when missing.
func (s *Store) GetDel(key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeString)
	if err != nil || !ok {
		return "", false, err
	}
	v := e.Str
	s.bytes.Add(-int64(e.Bytes))
	delete(s.data, key)
	s.fire("del", key)
	return v, true, nil
}

// GetEx reads a key and optionally adjusts the TTL atomically.
// Modes:
//
//   "" / "KEEP"  — leave TTL alone
//   "EX seconds" / "PX millis" — set new TTL
//   "EXAT unix-sec" / "PXAT unix-ms" — set absolute expiry
//   "PERSIST"    — clear TTL
func (s *Store) GetEx(key string, mode string, value int64) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeString)
	if err != nil || !ok {
		return "", false, err
	}
	switch strings.ToUpper(mode) {
	case "", "KEEP":
		// no-op
	case "PERSIST":
		e.ExpireAt = time.Time{}
	case "EX":
		e.ExpireAt = time.Now().Add(time.Duration(value) * time.Second)
	case "PX":
		e.ExpireAt = time.Now().Add(time.Duration(value) * time.Millisecond)
	case "EXAT":
		e.ExpireAt = time.Unix(value, 0)
	case "PXAT":
		e.ExpireAt = time.UnixMilli(value)
	default:
		return "", false, errors.New("syntax error")
	}
	return e.Str, true, nil
}

// LPos searches a list for the value, returning its 0-based index.
// rank chooses which match (1 = first, -1 = last); count > 0 returns
// up to count match indices. maxlen caps the scan distance.
func (s *Store) LPos(key, value string, rank, count, maxlen int) ([]int, bool, error) {
	if rank == 0 {
		return nil, false, errors.New("RANK can't be zero")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeList)
	if err != nil || !ok {
		return nil, false, err
	}
	out := []int{}
	scanned := 0
	if rank > 0 {
		idx := 0
		hits := 0
		for el := e.List.Front(); el != nil; el = el.Next() {
			if maxlen > 0 && scanned >= maxlen {
				break
			}
			scanned++
			if el.Value.(string) == value {
				hits++
				if hits >= rank {
					out = append(out, idx)
					if count == 0 {
						return out, true, nil
					}
					if count > 0 && len(out) >= count {
						return out, true, nil
					}
				}
			}
			idx++
		}
	} else {
		// scan from tail; rank is negative = nth match from the end
		want := -rank
		hits := 0
		idx := e.List.Len() - 1
		for el := e.List.Back(); el != nil; el = el.Prev() {
			if maxlen > 0 && scanned >= maxlen {
				break
			}
			scanned++
			if el.Value.(string) == value {
				hits++
				if hits >= want {
					out = append(out, idx)
					if count == 0 {
						return out, true, nil
					}
					if count > 0 && len(out) >= count {
						return out, true, nil
					}
				}
			}
			idx--
		}
	}
	return out, len(out) > 0, nil
}
