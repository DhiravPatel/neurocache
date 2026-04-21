package store

import (
	"container/list"
	"time"
)

// ExportEntry is a type-agnostic wire record used by snapshot/restore
// paths. It mirrors persistence.KeySnapshot but is declared here so the
// store has no dependency on the persistence package — the engine
// bridges the two formats.
type ExportEntry struct {
	Key      string
	Type     string
	ExpireAt int64 // unix-milli, 0 = no expiry
	Str      string
	List     []string
	Hash     map[string]string
	Set      []string
	ZSet     []ExportZMember
	Stream   []ExportStreamEntry
}

// ExportZMember and ExportStreamEntry carry the minimal payload for
// reconstructing a sorted set / stream from a snapshot.
type ExportZMember struct {
	Member string
	Score  float64
}

type ExportStreamEntry struct {
	ID     string
	Fields []string
}

// Export takes a consistent snapshot of every non-expired key. Safe to
// call concurrently with reads / writes — we only hold the read lock
// while copying pointer-y containers, so new writes after we return are
// simply not in the snapshot (that's the normal snapshot semantics).
func (s *Store) Export() []ExportEntry {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExportEntry, 0, len(s.data))
	for _, e := range s.data {
		if e.expired(now) {
			continue
		}
		ent := ExportEntry{Key: e.Key, Type: e.Type.String()}
		if !e.ExpireAt.IsZero() {
			ent.ExpireAt = e.ExpireAt.UnixMilli()
		}
		switch e.Type {
		case TypeString:
			ent.Str = e.Str
		case TypeList:
			if e.List != nil {
				items := make([]string, 0, e.List.Len())
				for el := e.List.Front(); el != nil; el = el.Next() {
					items = append(items, el.Value.(string))
				}
				ent.List = items
			}
		case TypeHash:
			cpy := make(map[string]string, len(e.Hash))
			for k, v := range e.Hash {
				cpy[k] = v
			}
			ent.Hash = cpy
		case TypeSet:
			members := make([]string, 0, len(e.Set))
			for m := range e.Set {
				members = append(members, m)
			}
			ent.Set = members
		case TypeZSet:
			if e.ZSet != nil {
				z := make([]ExportZMember, 0, e.ZSet.Len())
				for _, m := range e.ZSet.members() {
					sc, _ := e.ZSet.Score(m)
					z = append(z, ExportZMember{Member: m, Score: sc})
				}
				ent.ZSet = z
			}
		case TypeStream:
			if e.Stream != nil {
				e.Stream.mu.Lock()
				xs := make([]ExportStreamEntry, 0, len(e.Stream.entries))
				for _, se := range e.Stream.entries {
					fields := make([]string, len(se.Fields))
					copy(fields, se.Fields)
					xs = append(xs, ExportStreamEntry{ID: se.ID.String(), Fields: fields})
				}
				e.Stream.mu.Unlock()
				ent.Stream = xs
			}
		}
		out = append(out, ent)
	}
	return out
}

// Restore rebuilds the keyspace from a set of exported entries. It
// replaces whatever is currently in the store — used at startup when a
// snapshot is loaded. Callers should invoke before accepting clients.
func (s *Store) Restore(entries []ExportEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]*Entry, len(entries))
	s.bytes.Store(0)
	now := time.Now()
	for _, ent := range entries {
		e := &Entry{Key: ent.Key, CreatedAt: now, LastRead: now}
		if ent.ExpireAt > 0 {
			e.ExpireAt = time.UnixMilli(ent.ExpireAt)
			if e.expired(now) {
				continue
			}
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
		default:
			continue
		}
		s.recomputeBytes(e)
		s.data[e.Key] = e
	}
}

// getOrCreateInline allocates the type-specific container for an Entry
// that's already been partially populated (used only by Restore).
func (s *Store) getOrCreateInline(e *Entry) (*Entry, error) {
	switch e.Type {
	case TypeList:
		e.List = list.New()
	case TypeHash:
		e.Hash = map[string]string{}
	case TypeSet:
		e.Set = map[string]struct{}{}
	case TypeZSet:
		e.ZSet = newZSet()
	case TypeStream:
		e.Stream = newStream()
	}
	return e, nil
}
