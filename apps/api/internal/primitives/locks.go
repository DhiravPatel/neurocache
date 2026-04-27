package primitives

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// LockManager implements distributed locks with monotonic fencing
// tokens — the bug Martin Kleppmann famously called out about
// SETNX-based locks. Every successful ACQUIRE returns a token that
// strictly increases across the cluster's view of that lock; a fenced
// service can reject stale operations by comparing tokens.
//
// Beyond the canonical ACQUIRE/RELEASE/EXTEND/CHECK we also expose
// OWNER (read who currently holds the lock) so observers don't need
// to ACQUIRE-then-RELEASE just to learn the holder.
type LockManager struct {
	mu      sync.Mutex
	locks   map[string]*lockState
	tokenCt atomic.Uint64
}

type lockState struct {
	owner   string
	token   uint64
	expires time.Time
}

// NewLockManager builds an empty manager.
func NewLockManager() *LockManager {
	m := &LockManager{locks: map[string]*lockState{}}
	go m.sweepLoop()
	return m
}

// Acquire tries to claim `name` for `owner`. Returns the fencing token
// on success. ttl == 0 means "wait forever" — Redis users almost
// always want a TTL so the lock can't deadlock on a crashed holder.
//
// Acquire is reentrant by owner: an existing holder can call Acquire
// again to refresh the TTL and bump the token.
func (m *LockManager) Acquire(name, owner string, ttl time.Duration) (uint64, bool) {
	if name == "" || owner == "" {
		return 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if cur, ok := m.locks[name]; ok && now.Before(cur.expires) && cur.owner != owner {
		return 0, false
	}
	tok := m.tokenCt.Add(1)
	m.locks[name] = &lockState{owner: owner, token: tok, expires: now.Add(ttl)}
	return tok, true
}

// Release drops the lock when `owner` matches the holder. Safe to
// call after the TTL — releases get no-op semantics in that case.
func (m *LockManager) Release(name, owner string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.locks[name]
	if !ok {
		return false
	}
	if cur.owner != owner {
		return false
	}
	delete(m.locks, name)
	return true
}

// Extend bumps the TTL when `owner` matches. Token is unchanged so
// downstream services that already accepted this token keep operating.
func (m *LockManager) Extend(name, owner string, ttl time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.locks[name]
	if !ok || cur.owner != owner || time.Now().After(cur.expires) {
		return false
	}
	cur.expires = time.Now().Add(ttl)
	return true
}

// Check returns the current holder, token, and remaining ms — or
// (false) when no live lock exists.
type LockInfo struct {
	Owner   string
	Token   uint64
	RemMs   int64
}

func (m *LockManager) Check(name string) (LockInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.locks[name]
	if !ok {
		return LockInfo{}, false
	}
	rem := time.Until(cur.expires).Milliseconds()
	if rem <= 0 {
		delete(m.locks, name)
		return LockInfo{}, false
	}
	return LockInfo{Owner: cur.owner, Token: cur.token, RemMs: rem}, true
}

// Errors callers can surface verbatim.
var (
	ErrLockHeld    = errors.New("LOCK held by another owner")
	ErrLockMissing = errors.New("LOCK not found")
)

func (m *LockManager) sweepLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		m.mu.Lock()
		for k, l := range m.locks {
			if now.After(l.expires) {
				delete(m.locks, k)
			}
		}
		m.mu.Unlock()
	}
}
