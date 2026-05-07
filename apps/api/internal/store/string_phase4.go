package store

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
)

// DelEx is a compare-and-delete: it removes the key only when the
// caller-supplied value matches the current string. Returns:
//
//   1  — key existed, value matched, key was removed
//   0  — key existed but value did not match (no-op)
//  -1  — key did not exist
//
// The CAS makes safe "delete only if you still own the lease" patterns
// trivial — without it, callers race the standard "GET then DEL" pair
// against any other writer.
//
// Type rules: only string values are eligible. Calling DELEX on a list/
// hash/set/zset key returns ErrWrongType so callers don't accidentally
// nuke a different data type.
func (s *Store) DelEx(key, value string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		return -1, nil
	}
	if e.Type != TypeString {
		return 0, ErrWrongType
	}
	if e.Str != value {
		return 0, nil
	}
	s.bytes.Add(-int64(e.Bytes))
	delete(sh.data, key)
	s.fire("del", key)
	return 1, nil
}

// Digest returns a stable 40-char hex SHA1 of the key's content. Used
// for ETag-style change detection, replication consistency probes, and
// "did this change?" cache validation. Returns ("", false) for missing
// keys.
//
// The hash domain captures the canonical serialization of each value
// type — for collections, that means a sorted enumeration so a hash is
// stable across insertion order. This matches the property real
// operators want: identical content → identical digest, regardless of
// how it got there.
func (s *Store) Digest(key string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		return "", false, nil
	}
	h := sha1.New()
	switch e.Type {
	case TypeString:
		h.Write([]byte("S:"))
		h.Write([]byte(e.Str))
	case TypeList:
		h.Write([]byte("L:"))
		e.List.ForEach(func(v string) bool {
			h.Write([]byte(v))
			h.Write([]byte{0})
			return true
		})
	case TypeHash:
		h.Write([]byte("H:"))
		fields := make([]string, 0, len(e.Hash))
		for f := range e.Hash {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, f := range fields {
			h.Write([]byte(f))
			h.Write([]byte{0})
			h.Write([]byte(e.Hash[f]))
			h.Write([]byte{0})
		}
	case TypeSet:
		h.Write([]byte("X:"))
		members := make([]string, 0, len(e.Set))
		for m := range e.Set {
			members = append(members, m)
		}
		sort.Strings(members)
		for _, m := range members {
			h.Write([]byte(m))
			h.Write([]byte{0})
		}
	case TypeZSet:
		h.Write([]byte("Z:"))
		members := e.ZSet.members()
		sort.Strings(members)
		for _, m := range members {
			sc, _ := e.ZSet.Score(m)
			h.Write([]byte(fmt.Sprintf("%s\x00%g\x00", m, sc)))
		}
	default:
		// Streams + module types fall back to the entry-bytes count —
		// good enough for a "did anything change" probe without
		// forcing a deep walk over every payload.
		h.Write([]byte(fmt.Sprintf("O:%d", e.Bytes)))
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

// MSetEx is MSET with a shared TTL applied to every key. Mirrors the
// MSET atomicity guarantee — either every (key, value) pair lands with
// the TTL applied, or the call errors out and nothing changes.
//
// ttl == 0 is rejected so callers don't accidentally overwrite without
// the expiry semantics they asked for; use plain MSET for the
// no-expiry form.
func (s *Store) MSetEx(ttl time.Duration, pairs ...string) error {
	if ttl <= 0 {
		return errors.New("MSETEX requires a positive TTL")
	}
	if len(pairs) == 0 || len(pairs)%2 != 0 {
		return errors.New("MSETEX requires key/value pairs")
	}
	keys := make([]string, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		keys = append(keys, pairs[i])
	}
	involved := s.shardsFor(keys)
	unlock := s.lockShardsW(involved)
	defer unlock()
	now := time.Now()
	exp := now.Add(ttl)
	for i := 0; i < len(pairs); i += 2 {
		k, v := pairs[i], pairs[i+1]
		sh := s.shardForKey(k)
		if old, ok := sh.data[k]; ok {
			s.bytes.Add(-int64(old.Bytes))
		}
		e := &Entry{
			Key:       k,
			Type:      TypeString,
			Str:       v,
			CreatedAt: now,
			LastRead:  now,
			ExpireAt:  exp,
			Bytes:     len(k) + len(v),
		}
		sh.data[k] = e
		s.bytes.Add(int64(e.Bytes))
	}
	return nil
}
