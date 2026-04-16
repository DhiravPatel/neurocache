package store

import (
	"sync"
	"sync/atomic"
	"time"
)

type Entry struct {
	Key       string
	Value     string
	CreatedAt time.Time
	ExpireAt  time.Time // zero = no expiry
	Hits      uint64
	LastRead  time.Time
	Bytes     int
}

func (e *Entry) expired(now time.Time) bool {
	return !e.ExpireAt.IsZero() && now.After(e.ExpireAt)
}

// Store is a thread-safe in-memory KV with TTL and hit counters.
type Store struct {
	mu    sync.RWMutex
	data  map[string]*Entry
	bytes atomic.Int64
}

func New() *Store {
	s := &Store{data: make(map[string]*Entry)}
	go s.ttlLoop()
	return s
}

func (s *Store) ttlLoop() {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		s.mu.Lock()
		for k, e := range s.data {
			if e.expired(now) {
				s.bytes.Add(-int64(e.Bytes))
				delete(s.data, k)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Store) Set(key, value string, ttl time.Duration) {
	now := time.Now()
	e := &Entry{
		Key:       key,
		Value:     value,
		CreatedAt: now,
		LastRead:  now,
		Bytes:     len(key) + len(value),
	}
	if ttl > 0 {
		e.ExpireAt = now.Add(ttl)
	}
	s.mu.Lock()
	if old, ok := s.data[key]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	s.data[key] = e
	s.bytes.Add(int64(e.Bytes))
	s.mu.Unlock()
}

func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	e, ok := s.data[key]
	s.mu.RUnlock()
	if !ok || e.expired(time.Now()) {
		return "", false
	}
	atomic.AddUint64(&e.Hits, 1)
	e.LastRead = time.Now()
	return e.Value, true
}

func (s *Store) Del(keys ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range keys {
		if e, ok := s.data[k]; ok {
			s.bytes.Add(-int64(e.Bytes))
			delete(s.data, k)
			n++
		}
	}
	return n
}

func (s *Store) Exists(key string) bool {
	s.mu.RLock()
	e, ok := s.data[key]
	s.mu.RUnlock()
	return ok && !e.expired(time.Now())
}

func (s *Store) Expire(key string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok {
		return false
	}
	e.ExpireAt = time.Now().Add(ttl)
	return true
}

func (s *Store) Persist(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok {
		return false
	}
	e.ExpireAt = time.Time{}
	return true
}

func (s *Store) TTL(key string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok {
		return -2 // key does not exist
	}
	if e.ExpireAt.IsZero() {
		return -1 // no expiry
	}
	return time.Until(e.ExpireAt)
}

func (s *Store) Incr(key string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	var cur int64
	if ok {
		_, err := fmtSscanInt(e.Value, &cur)
		if err != nil {
			return 0, err
		}
	}
	cur += delta
	v := fmtItoa(cur)
	if !ok {
		e = &Entry{Key: key, CreatedAt: time.Now(), LastRead: time.Now()}
		s.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Value = v
	e.Bytes = len(key) + len(v)
	s.bytes.Add(int64(e.Bytes))
	return cur, nil
}

func (s *Store) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	return keys
}

func (s *Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

func (s *Store) BytesUsed() int64 {
	return s.bytes.Load()
}

func (s *Store) FlushAll() {
	s.mu.Lock()
	s.data = make(map[string]*Entry)
	s.bytes.Store(0)
	s.mu.Unlock()
}

// Snapshot copies of all non-expired entries (for eviction scoring / admin).
func (s *Store) Snapshot() []Entry {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		if !e.expired(now) {
			out = append(out, *e)
		}
	}
	return out
}

func (s *Store) Evict(keys []string) int {
	return s.Del(keys...)
}
