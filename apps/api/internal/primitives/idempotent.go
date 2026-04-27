// Package primitives implements NeuroCache-only commands that don't
// exist in vanilla Redis: idempotency keys, distributed locks with
// fencing tokens, GCRA rate limiting, bloom-backed deduplication,
// cost-aware cache weights, time-travel versioned keys, and a
// collaborative-filtering recommendation surface that re-uses the
// existing semantic infrastructure.
//
// Each primitive lives in its own file so the dispatcher hooks stay
// trivial (one function per command). Storage is via the engine's
// CustomValue mechanism for cross-restart durability — these
// primitives participate in AOF/RDB just like any built-in type.
package primitives

import (
	"errors"
	"sync"
	"time"
)

// IdempotencyStore captures previously-completed (id → result) pairs
// so duplicate calls return the cached result instead of re-running
// the work. Eviction is timer-driven; entries past their TTL are
// reaped on the next Sweep tick.
type IdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*idempotentEntry
}

type idempotentEntry struct {
	result   any
	completedAt time.Time
	expires  time.Time
	inFlight bool
	wait     chan struct{} // closed when the in-flight call finishes
}

// NewIdempotencyStore returns an empty store + starts the sweeper.
func NewIdempotencyStore() *IdempotencyStore {
	s := &IdempotencyStore{entries: map[string]*idempotentEntry{}}
	go s.sweepLoop()
	return s
}

// Acquire is the once-only entry point. The caller passes the id and
// the work to do. Outcomes:
//
//   (cached, true, nil)     — id was seen recently; return the cached result
//   (nil,   false, nil)     — caller is the first runner; do the work, then call Complete
//   (nil,   false, err)     — id is in flight on another goroutine; we waited
//                             then either returned its result or got an error
//
// The TTL determines how long the result is cached after the work
// completes. Subsequent Acquire calls within that window reuse the
// cached result without re-running the work.
func (s *IdempotencyStore) Acquire(id string, ttl time.Duration) (any, bool, error) {
	s.mu.Lock()
	if e, ok := s.entries[id]; ok {
		if time.Now().Before(e.expires) {
			if e.inFlight {
				wait := e.wait
				s.mu.Unlock()
				<-wait
				s.mu.Lock()
				if e, ok := s.entries[id]; ok && !e.inFlight {
					res := e.result
					s.mu.Unlock()
					return res, true, nil
				}
				s.mu.Unlock()
				return nil, false, errors.New("idempotent leader failed")
			}
			res := e.result
			s.mu.Unlock()
			return res, true, nil
		}
		// expired — fall through to fresh acquire
	}
	e := &idempotentEntry{inFlight: true, wait: make(chan struct{}), expires: time.Now().Add(ttl)}
	s.entries[id] = e
	s.mu.Unlock()
	return nil, false, nil
}

// Complete records the result + releases anyone waiting on the same id.
func (s *IdempotencyStore) Complete(id string, result any, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		// caller never Acquired — register from scratch so a subsequent
		// Acquire still hits the cache.
		s.entries[id] = &idempotentEntry{result: result, completedAt: time.Now(), expires: time.Now().Add(ttl)}
		return
	}
	e.result = result
	e.completedAt = time.Now()
	e.expires = time.Now().Add(ttl)
	e.inFlight = false
	if e.wait != nil {
		close(e.wait)
		e.wait = nil
	}
}

// Discard drops an in-flight id so subsequent callers can retry — used
// when the leader hits an unrecoverable error and shouldn't poison the
// idempotency window.
func (s *IdempotencyStore) Discard(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok && e.inFlight {
		close(e.wait)
		delete(s.entries, id)
	}
}

func (s *IdempotencyStore) sweepLoop() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		s.mu.Lock()
		for k, e := range s.entries {
			if !e.inFlight && now.After(e.expires) {
				delete(s.entries, k)
			}
		}
		s.mu.Unlock()
	}
}
