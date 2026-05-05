package store

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ObjectInfo is what OBJECT ENCODING/IDLETIME/FREQ surface to callers.
// We pick a single nominal encoding per type — Redis distinguishes
// ziplist vs listpack vs hashtable, but we use uniform Go containers,
// so the encoding label is informative only.
type ObjectInfo struct {
	Type     string
	Encoding string
	IdleSec  int64
	FreqHits uint64
	Bytes    int64
}

// resolveEncoding returns the canonical Redis encoding label for an
// entry. We follow the same size-heuristic Redis uses: small
// collections use a packed representation (listpack / intset /
// embstr); above the size or value-length threshold they "promote"
// to the open-ended form (hashtable / quicklist / skiplist / raw).
//
// The thresholds here match Redis 7.x defaults — operators
// monitoring NeuroCache via RedisInsight or similar tools see the
// same encoding labels they'd see on real Redis, so memory-tuning
// dashboards don't lie.
func resolveEncoding(e *Entry) string {
	switch e.Type {
	case TypeString:
		// Try integer parsing — Redis tags pure-integer strings as "int".
		if n, err := strconv.ParseInt(e.Str, 10, 64); err == nil {
			_ = n
			return "int"
		}
		// embstr is the "small immutable" form — Redis uses it up to
		// 44 bytes (the threshold has changed across versions; we use
		// the long-standing default).
		if len(e.Str) <= 44 {
			return "embstr"
		}
		return "raw"
	case TypeList:
		// listpack for small lists with short values, otherwise quicklist
		// (Redis no longer ships linkedlist; it was replaced in 3.2).
		if e.List == nil || e.List.Len() <= 128 {
			maxLen := 0
			if e.List != nil {
				for el := e.List.Front(); el != nil; el = el.Next() {
					if l := len(el.Value); l > maxLen {
						maxLen = l
					}
					if maxLen > 64 {
						break
					}
				}
			}
			if maxLen <= 64 {
				return "listpack"
			}
		}
		return "quicklist"
	case TypeHash:
		if len(e.Hash) <= 128 {
			maxLen := 0
			for k, v := range e.Hash {
				if l := len(k); l > maxLen {
					maxLen = l
				}
				if l := len(v); l > maxLen {
					maxLen = l
				}
				if maxLen > 64 {
					break
				}
			}
			if maxLen <= 64 {
				return "listpack"
			}
		}
		return "hashtable"
	case TypeSet:
		// intset when every member parses as int64.
		allInt := len(e.Set) > 0
		for m := range e.Set {
			if _, err := strconv.ParseInt(m, 10, 64); err != nil {
				allInt = false
				break
			}
		}
		if allInt && len(e.Set) <= 512 {
			return "intset"
		}
		if len(e.Set) <= 128 {
			maxLen := 0
			for m := range e.Set {
				if l := len(m); l > maxLen {
					maxLen = l
				}
				if maxLen > 64 {
					break
				}
			}
			if maxLen <= 64 {
				return "listpack"
			}
		}
		return "hashtable"
	case TypeZSet:
		if e.ZSet == nil || e.ZSet.Len() <= 128 {
			maxLen := 0
			if e.ZSet != nil {
				for _, m := range e.ZSet.members() {
					if l := len(m); l > maxLen {
						maxLen = l
					}
					if maxLen > 64 {
						break
					}
				}
			}
			if maxLen <= 64 {
				return "listpack"
			}
		}
		return "skiplist"
	case TypeStream:
		return "stream"
	case TypeVector:
		// V* type isn't a Redis-native type so there's no canonical
		// encoding label; we expose the algorithm choice instead.
		if e.Vector != nil && e.Vector.Index != nil {
			return strings.ToLower(string(e.Vector.Index.Algo()))
		}
		return "vectorset"
	case TypeModule:
		return "module"
	}
	return "raw"
}

// Object returns metadata for one key, (nil, false) when missing.
func (s *Store) Object(key string) (*ObjectInfo, bool) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		return nil, false
	}
	return &ObjectInfo{
		Type:     e.Type.String(),
		Encoding: resolveEncoding(e),
		IdleSec:  int64(time.Since(e.LastRead).Seconds()),
		FreqHits: atomic.LoadUint64(&e.Hits),
		Bytes:    int64(e.Bytes),
	}, true
}

// Dump serializes one key's value as an opaque blob suitable for
// RESTORE on this engine. We use gob+gzip — the layout is internal and
// not compatible with Redis' DUMP format on purpose: Redis' format is
// versioned and tied to its on-disk encoding. Anyone needing real
// inter-Redis migration should swap this for the rdb-style payload.
func (s *Store) Dump(key string) (string, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		sh.mu.RUnlock()
		return "", false, nil
	}
	exp := ExportEntry{Key: e.Key, Type: e.Type.String()}
	if !e.ExpireAt.IsZero() {
		exp.ExpireAt = e.ExpireAt.UnixMilli()
	}
	switch e.Type {
	case TypeString:
		exp.Str = e.Str
	case TypeList:
		items := []string{}
		for el := e.List.Front(); el != nil; el = el.Next() {
			items = append(items, el.Value)
		}
		exp.List = items
	case TypeHash:
		cpy := make(map[string]string, len(e.Hash))
		for k, v := range e.Hash {
			cpy[k] = v
		}
		exp.Hash = cpy
	case TypeSet:
		ms := []string{}
		for m := range e.Set {
			ms = append(ms, m)
		}
		exp.Set = ms
	case TypeZSet:
		for _, m := range e.ZSet.members() {
			sc, _ := e.ZSet.Score(m)
			exp.ZSet = append(exp.ZSet, ExportZMember{Member: m, Score: sc})
		}
	case TypeStream:
		e.Stream.mu.Lock()
		for _, se := range e.Stream.entries {
			exp.Stream = append(exp.Stream, ExportStreamEntry{ID: se.ID.String(), Fields: se.Fields})
		}
		e.Stream.mu.Unlock()
	case TypeVector:
		if e.Vector != nil && e.Vector.Index != nil {
			idx := e.Vector.Index
			exp.VectorOpts = ExportVectorOpts{
				Algo: string(idx.Algo()), Dim: idx.Dim(), Metric: string(idx.Metric()),
				M: idx.M(), EFC: idx.EFC(), EFR: idx.EFR(),
			}
			for _, id := range idx.IDs() {
				vec, _ := idx.Get(id)
				attr, _ := idx.GetAttr(id)
				exp.VectorMembers = append(exp.VectorMembers, ExportVectorMember{
					ID: id, Vec: encodeVectorString(vec), Attr: attr,
				})
			}
		}
	}
	sh.mu.RUnlock()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := gob.NewEncoder(gz)
	if err := enc.Encode(exp); err != nil {
		return "", false, err
	}
	if err := gz.Close(); err != nil {
		return "", false, err
	}
	return buf.String(), true, nil
}

// RestoreKey writes back a blob produced by Dump. ttlMs > 0 sets the TTL,
// 0 means "no TTL". replace == true overwrites an existing key. (Kept
// distinct from Store.Restore which restores a whole snapshot at boot.)
func (s *Store) RestoreKey(key string, ttlMs int64, blob string, replace bool) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if _, exists := sh.data[key]; exists && !replace {
		return errors.New("BUSYKEY Target key name already exists.")
	}
	gz, err := gzip.NewReader(bytes.NewReader([]byte(blob)))
	if err != nil {
		return errors.New("DUMP payload version or checksum are wrong")
	}
	defer gz.Close()
	var exp ExportEntry
	if err := gob.NewDecoder(gz).Decode(&exp); err != nil {
		return errors.New("DUMP payload version or checksum are wrong")
	}
	exp.Key = key
	if ttlMs > 0 {
		exp.ExpireAt = time.Now().Add(time.Duration(ttlMs) * time.Millisecond).UnixMilli()
	}
	s.restoreOne(exp)
	return nil
}

// Copy deep-copies src to dst. replace controls whether dst can be
// overwritten. Returns false when src is missing or dst exists without
// replace.
func (s *Store) Copy(src, dst string, replace bool) (bool, error) {
	shS, shD, unlock := s.lockTwoW(src, dst)
	defer unlock()
	se, ok := shS.data[src]
	if !ok || se.expired(time.Now()) {
		return false, nil
	}
	if _, exists := shD.data[dst]; exists && !replace {
		return false, nil
	}
	exp := ExportEntry{Key: dst, Type: se.Type.String()}
	if !se.ExpireAt.IsZero() {
		exp.ExpireAt = se.ExpireAt.UnixMilli()
	}
	switch se.Type {
	case TypeString:
		exp.Str = se.Str
	case TypeList:
		items := []string{}
		for el := se.List.Front(); el != nil; el = el.Next() {
			items = append(items, el.Value)
		}
		exp.List = items
	case TypeHash:
		cpy := make(map[string]string, len(se.Hash))
		for k, v := range se.Hash {
			cpy[k] = v
		}
		exp.Hash = cpy
	case TypeSet:
		ms := []string{}
		for m := range se.Set {
			ms = append(ms, m)
		}
		exp.Set = ms
	case TypeZSet:
		for _, m := range se.ZSet.members() {
			sc, _ := se.ZSet.Score(m)
			exp.ZSet = append(exp.ZSet, ExportZMember{Member: m, Score: sc})
		}
	case TypeStream:
		se.Stream.mu.Lock()
		for _, sm := range se.Stream.entries {
			exp.Stream = append(exp.Stream, ExportStreamEntry{ID: sm.ID.String(), Fields: sm.Fields})
		}
		se.Stream.mu.Unlock()
	case TypeVector:
		if se.Vector != nil && se.Vector.Index != nil {
			idx := se.Vector.Index
			exp.VectorOpts = ExportVectorOpts{
				Algo: string(idx.Algo()), Dim: idx.Dim(), Metric: string(idx.Metric()),
				M: idx.M(), EFC: idx.EFC(), EFR: idx.EFR(),
			}
			for _, id := range idx.IDs() {
				vec, _ := idx.Get(id)
				attr, _ := idx.GetAttr(id)
				exp.VectorMembers = append(exp.VectorMembers, ExportVectorMember{
					ID: id, Vec: encodeVectorString(vec), Attr: attr,
				})
			}
		}
	}
	s.restoreOne(exp)
	return true, nil
}

// restoreOne is the inner reconstruct used by Restore + Copy. Caller
// must hold the write lock on the shard owning ent.Key.
func (s *Store) restoreOne(ent ExportEntry) {
	sh := s.shardForKey(ent.Key)
	if old, ok := sh.data[ent.Key]; ok {
		s.bytes.Add(-int64(old.Bytes))
		delete(sh.data, ent.Key)
	}
	e := &Entry{Key: ent.Key, CreatedAt: time.Now(), LastRead: time.Now()}
	if ent.ExpireAt > 0 {
		e.ExpireAt = time.UnixMilli(ent.ExpireAt)
	}
	switch ent.Type {
	case "string":
		e.Type = TypeString
		e.Str = ent.Str
	case "list":
		e.Type = TypeList
		_, _ = s.getOrCreateInline(e)
		for _, v := range ent.List {
			e.List.PushBack(v)
		}
	case "hash":
		e.Type = TypeHash
		_, _ = s.getOrCreateInline(e)
		for k, v := range ent.Hash {
			e.Hash[k] = v
		}
	case "set":
		e.Type = TypeSet
		_, _ = s.getOrCreateInline(e)
		for _, m := range ent.Set {
			e.Set[m] = struct{}{}
		}
	case "zset":
		e.Type = TypeZSet
		_, _ = s.getOrCreateInline(e)
		for _, zm := range ent.ZSet {
			e.ZSet.AddNew(zm.Score, zm.Member)
		}
	case "stream":
		e.Type = TypeStream
		_, _ = s.getOrCreateInline(e)
		for _, se := range ent.Stream {
			id, err := ParseStreamID(se.ID, false)
			if err != nil {
				continue
			}
			e.Stream.entries = append(e.Stream.entries, StreamEntry{ID: id, Fields: se.Fields})
			if e.Stream.lastID.Less(id) {
				e.Stream.lastID = id
			}
		}
	case "vectorset":
		e.Type = TypeVector
		if !restoreVectorSet(e, ent) {
			return
		}
	}
	s.recomputeBytes(e)
	sh.data[e.Key] = e
}
