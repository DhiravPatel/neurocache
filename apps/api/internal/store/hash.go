package store

import (
	"errors"
	"strconv"
)

// HSet writes one or more field/value pairs. Returns the number of *new*
// fields (matching Redis semantics — overwrites don't count).
func (s *Store) HSet(key string, pairs ...string) (int, error) {
	if len(pairs) == 0 || len(pairs)%2 != 0 {
		return 0, errors.New("HSET requires field/value pairs")
	}
	s.mu.Lock()
	e, err := s.getOrCreate(key, TypeHash)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	added := 0
	for i := 0; i < len(pairs); i += 2 {
		f, v := pairs[i], pairs[i+1]
		if _, exists := e.Hash[f]; !exists {
			added++
		}
		e.Hash[f] = v
	}
	s.recomputeBytes(e)
	s.mu.Unlock()
	s.fire("hset", key)
	return added, nil
}

// HSetNX sets a field only if it does not exist.
func (s *Store) HSetNX(key, field, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.getOrCreate(key, TypeHash)
	if err != nil {
		return false, err
	}
	if _, exists := e.Hash[field]; exists {
		return false, nil
	}
	e.Hash[field] = value
	s.recomputeBytes(e)
	return true, nil
}

// HGet fetches a single field.
func (s *Store) HGet(key, field string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return "", false, err
	}
	v, exists := e.Hash[field]
	return v, exists, nil
}

// HMGet returns values for a list of fields; miss[i] is zero-value.
func (s *Store) HMGet(key string, fields ...string) ([]string, []bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals := make([]string, len(fields))
	hits := make([]bool, len(fields))
	e, ok, err := s.get(key, TypeHash)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return vals, hits, nil
	}
	for i, f := range fields {
		v, exists := e.Hash[f]
		if exists {
			vals[i] = v
			hits[i] = true
		}
	}
	return vals, hits, nil
}

// HGetAll returns all fields as alternating field/value pairs — callers
// can pair them up or flatten depending on wire protocol needs.
func (s *Store) HGetAll(key string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return []string{}, err
	}
	out := make([]string, 0, len(e.Hash)*2)
	for f, v := range e.Hash {
		out = append(out, f, v)
	}
	return out, nil
}

// HDel removes fields; returns the number actually removed.
func (s *Store) HDel(key string, fields ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return 0, err
	}
	removed := 0
	for _, f := range fields {
		if _, exists := e.Hash[f]; exists {
			delete(e.Hash, f)
			removed++
		}
	}
	s.recomputeBytes(e)
	s.removeIfEmpty(e)
	return removed, nil
}

// HExists reports whether the field is present.
func (s *Store) HExists(key, field string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return false, err
	}
	_, exists := e.Hash[field]
	return exists, nil
}

// HLen returns the field count, 0 if missing.
func (s *Store) HLen(key string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return 0, err
	}
	return len(e.Hash), nil
}

// HKeys returns all field names.
func (s *Store) HKeys(key string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return []string{}, err
	}
	out := make([]string, 0, len(e.Hash))
	for f := range e.Hash {
		out = append(out, f)
	}
	return out, nil
}

// HVals returns all field values.
func (s *Store) HVals(key string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return []string{}, err
	}
	out := make([]string, 0, len(e.Hash))
	for _, v := range e.Hash {
		out = append(out, v)
	}
	return out, nil
}

// HIncrBy adds delta to a numeric field; creates it at 0 if missing.
func (s *Store) HIncrBy(key, field string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.getOrCreate(key, TypeHash)
	if err != nil {
		return 0, err
	}
	var cur int64
	if v, ok := e.Hash[field]; ok {
		cur, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, errors.New("ERR hash value is not an integer")
		}
	}
	cur += delta
	e.Hash[field] = strconv.FormatInt(cur, 10)
	s.recomputeBytes(e)
	return cur, nil
}

// HIncrByFloat adds a float delta; mirrors HIncrBy for float semantics.
func (s *Store) HIncrByFloat(key, field string, delta float64) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.getOrCreate(key, TypeHash)
	if err != nil {
		return 0, err
	}
	var cur float64
	if v, ok := e.Hash[field]; ok {
		cur, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, errors.New("ERR hash value is not a float")
		}
	}
	cur += delta
	e.Hash[field] = strconv.FormatFloat(cur, 'f', -1, 64)
	s.recomputeBytes(e)
	return cur, nil
}

// HStrLen returns the byte length of the field's value.
func (s *Store) HStrLen(key, field string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return 0, err
	}
	return len(e.Hash[field]), nil
}
