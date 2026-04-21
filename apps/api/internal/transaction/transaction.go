// Package transaction implements the MULTI / EXEC / DISCARD / WATCH
// command group. It is a passive data structure — the RESP and HTTP
// layers own the actual execution and replay queued commands against
// the engine once EXEC is received.
package transaction

import (
	"errors"
	"sync"
)

// Queued is a single command held inside a MULTI block.
type Queued struct {
	Cmd  string
	Args []string
}

// State tracks one connection's transaction lifecycle.
type State struct {
	mu      sync.Mutex
	active  bool
	queue   []Queued
	watched map[string]uint64 // key -> version at WATCH time
	dirty   bool              // a watched key was touched -> abort on EXEC
}

// New returns a fresh per-connection transaction state.
func New() *State { return &State{watched: map[string]uint64{}} }

// Begin starts a MULTI block. Returns an error if one is already active
// (matches Redis's "MULTI calls can not be nested" response).
func (s *State) Begin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return errors.New("MULTI calls can not be nested")
	}
	s.active = true
	s.queue = nil
	return nil
}

// InProgress reports whether MULTI has been called without EXEC/DISCARD.
func (s *State) InProgress() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Queue appends a command to the pending transaction.
func (s *State) Queue(cmd string, args []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return errors.New("not in a MULTI block")
	}
	s.queue = append(s.queue, Queued{Cmd: cmd, Args: args})
	return nil
}

// Commit returns the queued commands and resets state.
// aborted == true when a WATCH-ed key was mutated since WATCH ran —
// callers must return a nil array per Redis protocol instead of running.
func (s *State) Commit() (cmds []Queued, aborted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmds = s.queue
	aborted = s.dirty
	s.active = false
	s.queue = nil
	s.watched = map[string]uint64{}
	s.dirty = false
	return
}

// Discard cancels the transaction.
func (s *State) Discard() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return errors.New("DISCARD without MULTI")
	}
	s.active = false
	s.queue = nil
	s.watched = map[string]uint64{}
	s.dirty = false
	return nil
}

// Watch adds keys to the watched set along with their current version.
// The RESP layer bumps KeyVersion on every write so EXEC can detect races.
func (s *State) Watch(key string, version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return errors.New("WATCH inside MULTI is not allowed")
	}
	s.watched[key] = version
	return nil
}

// Unwatch clears every watched key.
func (s *State) Unwatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watched = map[string]uint64{}
	s.dirty = false
}

// CheckDirty reports whether the watched key's version has advanced.
// The RESP-side versions source is passed in so this package stays
// decoupled from engine internals.
func (s *State) CheckDirty(currentVersion func(string) uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.watched {
		if currentVersion(k) != v {
			s.dirty = true
			return
		}
	}
}
