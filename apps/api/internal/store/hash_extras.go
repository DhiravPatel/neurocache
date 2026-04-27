package store

import (
	"errors"
	"strings"
	"time"
)

// HGetDel atomically reads + deletes the given fields. Returns one
// reply per field — the value when present, ("", false) when absent.
// The hash key itself is removed when the last field is deleted.
//
// Mirrors Redis 8.0 HGETDEL.
func (s *Store) HGetDel(key string, fields []string) ([]string, []bool, error) {
	values := make([]string, len(fields))
	hits := make([]bool, len(fields))
	if len(fields) == 0 {
		return values, hits, errors.New("HGETDEL requires at least one field")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return values, hits, err
	}
	for i, f := range fields {
		v, exists := e.Hash[f]
		if !exists {
			continue
		}
		values[i] = v
		hits[i] = true
		delete(e.Hash, f)
		if e.HashTTL != nil {
			delete(e.HashTTL, f)
		}
	}
	s.recomputeBytes(e)
	s.removeIfEmpty(e)
	s.fire("hdel", key)
	return values, hits, nil
}

// HGetEx atomically reads fields and adjusts their per-field TTL. mode
// controls the TTL operation, applied uniformly to every field that
// exists. Modes (case-insensitive):
//
//   "" / "KEEP"  — leave TTL alone
//   "EX" / "PX"  — set new TTL with value (seconds / milliseconds)
//   "EXAT"       — absolute Unix-second expiry
//   "PXAT"       — absolute Unix-millisecond expiry
//   "PERSIST"    — clear TTL on every read field
//
// Returns one reply per field as for HGetDel.
func (s *Store) HGetEx(key string, fields []string, mode string, value int64) ([]string, []bool, error) {
	values := make([]string, len(fields))
	hits := make([]bool, len(fields))
	if len(fields) == 0 {
		return values, hits, errors.New("HGETEX requires at least one field")
	}
	mode = strings.ToUpper(mode)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil || !ok {
		return values, hits, err
	}
	now := time.Now()
	var newExp time.Time
	persist := false
	switch mode {
	case "", "KEEP":
		// no TTL change
	case "PERSIST":
		persist = true
	case "EX":
		newExp = now.Add(time.Duration(value) * time.Second)
	case "PX":
		newExp = now.Add(time.Duration(value) * time.Millisecond)
	case "EXAT":
		newExp = time.Unix(value, 0)
	case "PXAT":
		newExp = time.UnixMilli(value)
	default:
		return values, hits, errors.New("syntax error")
	}
	for i, f := range fields {
		v, exists := e.Hash[f]
		if !exists {
			continue
		}
		values[i] = v
		hits[i] = true
		switch {
		case persist:
			if e.HashTTL != nil {
				delete(e.HashTTL, f)
			}
		case !newExp.IsZero():
			if e.HashTTL == nil {
				e.HashTTL = map[string]time.Time{}
			}
			e.HashTTL[f] = newExp
		}
	}
	return values, hits, nil
}

// HSetEx writes one or more field/value pairs *and* assigns a per-field
// TTL in a single atomic operation. Mirrors Redis 8.0 HSETEX with the
// shared TTL form (every field gets the same expiry).
//
// pairs is the flat (field, value, field, value, …) layout; the caller
// is responsible for parity. Conditional flags:
//
//   "FNX" — only write fields that don't yet exist
//   "FXX" — only write fields that already exist
//   ""    — unconditional (default)
//
// Returns 1 when every requested field was written, 0 when at least one
// was rejected by the condition (Redis semantics — atomicity per call).
func (s *Store) HSetEx(key string, ttl time.Duration, cond string, pairs []string) (int, error) {
	if len(pairs) == 0 || len(pairs)%2 != 0 {
		return 0, errors.New("HSETEX requires field/value pairs")
	}
	cond = strings.ToUpper(cond)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.getOrCreate(key, TypeHash)
	if err != nil {
		return 0, err
	}
	// First pass — verify the condition holds for every field. If any
	// field fails, write nothing (matches Redis HSETEX atomicity).
	for i := 0; i < len(pairs); i += 2 {
		_, exists := e.Hash[pairs[i]]
		switch cond {
		case "FNX":
			if exists {
				return 0, nil
			}
		case "FXX":
			if !exists {
				return 0, nil
			}
		}
	}
	if e.HashTTL == nil && ttl > 0 {
		e.HashTTL = map[string]time.Time{}
	}
	exp := time.Now().Add(ttl)
	for i := 0; i < len(pairs); i += 2 {
		f, v := pairs[i], pairs[i+1]
		e.Hash[f] = v
		if ttl > 0 {
			e.HashTTL[f] = exp
		} else if e.HashTTL != nil {
			delete(e.HashTTL, f)
		}
	}
	s.recomputeBytes(e)
	s.fire("hset", key)
	return 1, nil
}

// HExpireTime returns the absolute expiry of each field in Unix
// seconds. -2 = field missing, -1 = field exists with no TTL.
// Mirrors Redis 7.4's HEXPIRETIME.
func (s *Store) HExpireTime(key string, fields []string, ms bool) ([]int64, error) {
	out := make([]int64, len(fields))
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	for i, f := range fields {
		if _, exists := e.Hash[f]; !exists {
			out[i] = -2
			continue
		}
		exp, hasTTL := e.HashTTL[f]
		if !hasTTL {
			out[i] = -1
			continue
		}
		if ms {
			out[i] = exp.UnixMilli()
		} else {
			out[i] = exp.Unix()
		}
	}
	return out, nil
}
