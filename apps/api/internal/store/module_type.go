package store

import (
	"errors"
	"time"
)

// TypeModule is added to the existing ValueType enum (via SetModuleEntry)
// so module-owned data participates in the same lifecycle (TTL,
// eviction, byte accounting, notifications) as the built-in types.
//
// Keeping TypeModule out of the iota block in store.go avoids churning
// every existing switch — the module path is the only one that reads
// it, and it's far enough away from the hot single-key flows that an
// extra constant has no measurable cost.
const TypeModule ValueType = 100

// ModuleValue lives on Entry when Type==TypeModule. The store treats
// the payload opaquely; the module's CustomType handles marshal/free.
type ModuleValue struct {
	TypeIDLo uint64 // first  8 bytes of modules.TypeID
	TypeIDHi uint64 // unused today; reserved for 16-byte IDs later
	Value    any
	Bytes    int64
}

// SetModule stores a module-owned value at key. If the key already
// holds a different type the operation fails with WRONGTYPE.
func (s *Store) SetModule(key string, typeIDLo, typeIDHi uint64, value any, byteSize int64, ttl time.Duration) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := time.Now()
	old, exists := sh.data[key]
	if exists && !old.expired(now) && old.Type != TypeModule {
		return ErrWrongType
	}
	if exists {
		s.bytes.Add(-int64(old.Bytes))
	}
	e := &Entry{
		Key: key, Type: TypeModule,
		CreatedAt: now, LastRead: now,
		Module: &ModuleValue{TypeIDLo: typeIDLo, TypeIDHi: typeIDHi, Value: value, Bytes: byteSize},
	}
	if ttl > 0 {
		e.ExpireAt = now.Add(ttl)
	}
	e.Bytes = int(byteSize) + len(key)
	sh.data[key] = e
	s.bytes.Add(int64(e.Bytes))
	s.fire("module.set", key)
	return nil
}

// GetModule fetches a module value. typeIDLo guards against mistakes:
// reading from the wrong type returns WRONGTYPE so a JSON command can't
// silently see Bloom data.
func (s *Store) GetModule(key string, typeIDLo, typeIDHi uint64) (any, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok || e.expired(time.Now()) {
		return nil, false, nil
	}
	if e.Type != TypeModule || e.Module == nil {
		return nil, false, ErrWrongType
	}
	if e.Module.TypeIDLo != typeIDLo || e.Module.TypeIDHi != typeIDHi {
		return nil, false, ErrWrongType
	}
	return e.Module.Value, true, nil
}

// DelModule removes a module-typed key (no-op for missing keys).
// Returns true when something was deleted.
func (s *Store) DelModule(key string) bool {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data[key]
	if !ok {
		return false
	}
	if e.Type != TypeModule {
		// Don't accidentally drop a built-in-typed key.
		return false
	}
	s.bytes.Add(-int64(e.Bytes))
	delete(sh.data, key)
	s.fire("module.del", key)
	return true
}

// ErrModuleType is returned when a caller passes a wrong type-id pair.
var ErrModuleType = errors.New("WRONGTYPE module type id mismatch")
