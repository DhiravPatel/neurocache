package store

import (
	"errors"
	"math/rand"
	"time"
)

// HExpire sets per-field TTLs on a hash. Mirrors Redis 7.4's HEXPIRE.
// Returns one int per field:
//
//   1   — TTL applied
//   0   — TTL not applied (NX/XX/GT/LT condition not met)
//   -2  — field doesn't exist
//
// Conditions:
//   NX — only set when no existing TTL
//   XX — only set when an existing TTL is present
//   GT — only set when the new TTL is greater than the current one
//   LT — only set when the new TTL is less than the current one
func (s *Store) HExpire(key string, ttl time.Duration, fields []string, cond string) ([]int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeHash)
	if err != nil {
		return nil, err
	}
	out := make([]int, len(fields))
	if !ok {
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	if e.HashTTL == nil {
		e.HashTTL = map[string]time.Time{}
	}
	now := time.Now()
	target := now.Add(ttl)
	for i, f := range fields {
		if _, exists := e.Hash[f]; !exists {
			out[i] = -2
			continue
		}
		cur, hasTTL := e.HashTTL[f]
		switch cond {
		case "NX":
			if hasTTL {
				out[i] = 0
				continue
			}
		case "XX":
			if !hasTTL {
				out[i] = 0
				continue
			}
		case "GT":
			if hasTTL && !target.After(cur) {
				out[i] = 0
				continue
			}
		case "LT":
			if hasTTL && !target.Before(cur) {
				out[i] = 0
				continue
			}
		}
		e.HashTTL[f] = target
		out[i] = 1
	}
	return out, nil
}

// HExpireAt is the absolute-timestamp variant.
func (s *Store) HExpireAt(key string, at time.Time, fields []string, cond string) ([]int, error) {
	return s.HExpire(key, time.Until(at), fields, cond)
}

// HTTL returns the per-field remaining seconds. -2 = field missing,
// -1 = field exists with no TTL.
func (s *Store) HTTL(key string, fields []string, ms bool) ([]int64, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	out := make([]int64, len(fields))
	e, ok, err := sh.get(key, TypeHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	now := time.Now()
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
		rem := exp.Sub(now)
		if rem < 0 {
			out[i] = -2
			continue
		}
		if ms {
			out[i] = rem.Milliseconds()
		} else {
			out[i] = int64(rem.Seconds())
		}
	}
	return out, nil
}

// HPersist clears the TTL on selected fields. Returns 1 per field
// whose TTL was actually cleared, 0 otherwise, -2 when missing.
func (s *Store) HPersist(key string, fields []string) ([]int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	out := make([]int, len(fields))
	e, ok, err := sh.get(key, TypeHash)
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
		if _, had := e.HashTTL[f]; had {
			delete(e.HashTTL, f)
			out[i] = 1
		} else {
			out[i] = 0
		}
	}
	return out, nil
}

// sweepHashFieldsShard is the per-field sweep — called from the TTL loop on
// every tick for one shard. Caller holds sh.mu.Lock().
func (s *Store) sweepHashFieldsShard(sh *shard, now time.Time) {
	for _, e := range sh.data {
		if e.Type != TypeHash || len(e.HashTTL) == 0 {
			continue
		}
		for f, exp := range e.HashTTL {
			if now.After(exp) {
				delete(e.Hash, f)
				delete(e.HashTTL, f)
			}
		}
		if len(e.Hash) == 0 {
			s.bytes.Add(-int64(e.Bytes))
			delete(sh.data, e.Key)
		}
	}
}

// HRandFieldCount returns up to count fields. count > 0 returns
// distinct fields; count < 0 allows duplicates. withValues bundles the
// matching values alongside.
func (s *Store) HRandFieldCount(key string, count int, withValues bool) ([]string, []string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeHash)
	if err != nil || !ok {
		return nil, nil, err
	}
	keys := make([]string, 0, len(e.Hash))
	for k := range e.Hash {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, nil, nil
	}
	out := []string{}
	vals := []string{}
	if count >= 0 {
		// distinct
		if count > len(keys) {
			count = len(keys)
		}
		// shuffle and take the first `count`
		shuffled := append([]string(nil), keys...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		out = shuffled[:count]
	} else {
		n := -count
		for i := 0; i < n; i++ {
			out = append(out, keys[rand.Intn(len(keys))])
		}
	}
	if withValues {
		vals = make([]string, len(out))
		for i, k := range out {
			vals[i] = e.Hash[k]
		}
	}
	return out, vals, nil
}

// silence unused-error for constructed errors when a future refactor
// surfaces them.
var _ = errors.New
