package aiops

import (
	"sync"
	"time"
)

// ShadowFetcher is supplied by the engine wiring to fetch a key from
// the declared backing source on cache miss. The function signature
// keeps backing-source integration application-defined: a real
// deployment plugs in an HTTP/Postgres/S3 fetcher.
type ShadowFetcher func(key string) (string, error)

// Shadow is a stale-while-revalidate cache fronting a slower backing
// source (Postgres, an HTTP API, etc.). On cache miss the previous
// value (if any) returns immediately and a background goroutine fetches
// the fresh one. Stops thundering herds without app-side double-locking.
type Shadow struct {
	mu      sync.RWMutex
	values  map[string]*shadowEntry
	pending map[string]bool
	fetcher ShadowFetcher

	hits     uint64
	misses   uint64
	stale    uint64
	refreshes uint64
}

type shadowEntry struct {
	value     string
	storedAt  time.Time
	staleAt   time.Time // time after which the value is considered stale
}

// NewShadow constructs a manager. fetcher may be nil at boot; the
// engine sets it after wiring.
func NewShadow(fetcher ShadowFetcher) *Shadow {
	return &Shadow{
		values:  map[string]*shadowEntry{},
		pending: map[string]bool{},
		fetcher: fetcher,
	}
}

// SetFetcher swaps the backing-source fetcher. Used during dynamic
// reconfiguration.
func (s *Shadow) SetFetcher(f ShadowFetcher) {
	s.mu.Lock()
	s.fetcher = f
	s.mu.Unlock()
}

// Put stores a value with a freshness window. After staleAfter elapses
// the value is returned as stale and a background refresh is kicked
// off on the next Get.
func (s *Shadow) Put(key, value string, staleAfter time.Duration) {
	s.mu.Lock()
	s.values[key] = &shadowEntry{
		value:    value,
		storedAt: time.Now(),
		staleAt:  time.Now().Add(staleAfter),
	}
	s.mu.Unlock()
}

// Get returns (value, isFresh, hadValue). On a stale value we return
// it immediately and trigger an async refresh — at most one refresh
// per key in flight. On a complete miss with a fetcher configured we
// fetch synchronously.
func (s *Shadow) Get(key string) (string, bool, bool) {
	s.mu.RLock()
	e, ok := s.values[key]
	fetcher := s.fetcher
	s.mu.RUnlock()
	if ok {
		fresh := time.Now().Before(e.staleAt)
		if !fresh {
			s.scheduleRefresh(key, fetcher)
			s.mu.Lock()
			s.stale++
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.hits++
			s.mu.Unlock()
		}
		return e.value, fresh, true
	}
	s.mu.Lock()
	s.misses++
	s.mu.Unlock()
	if fetcher == nil {
		return "", false, false
	}
	v, err := fetcher(key)
	if err != nil {
		return "", false, false
	}
	// Default 5 min staleness on the first miss-fetch — operators
	// override per key via Put.
	s.Put(key, v, 5*time.Minute)
	return v, true, true
}

// Forget drops a key.
func (s *Shadow) Forget(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.values[key]
	delete(s.values, key)
	return ok
}

// scheduleRefresh kicks off a background fetch if one isn't already
// running for the key.
func (s *Shadow) scheduleRefresh(key string, fetcher ShadowFetcher) {
	if fetcher == nil {
		return
	}
	s.mu.Lock()
	if s.pending[key] {
		s.mu.Unlock()
		return
	}
	s.pending[key] = true
	s.refreshes++
	s.mu.Unlock()
	go func() {
		v, err := fetcher(key)
		s.mu.Lock()
		delete(s.pending, key)
		if err == nil {
			s.values[key] = &shadowEntry{
				value:    v,
				storedAt: time.Now(),
				staleAt:  time.Now().Add(5 * time.Minute),
			}
		}
		s.mu.Unlock()
	}()
}

// ShadowStats snapshots the manager.
type ShadowStats struct {
	Entries   int    `json:"entries"`
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Stale     uint64 `json:"stale_serves"`
	Refreshes uint64 `json:"background_refreshes"`
}

// Stats returns a snapshot.
func (s *Shadow) Stats() ShadowStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ShadowStats{
		Entries:   len(s.values),
		Hits:      s.hits,
		Misses:    s.misses,
		Stale:     s.stale,
		Refreshes: s.refreshes,
	}
}
