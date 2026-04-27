package introspect

import (
	"sync"
)

// TrackingTable powers Redis 6+ server-assisted client-side caching.
// When a client opts in via `CLIENT TRACKING ON`, every key it reads
// is recorded; subsequent writes to those keys publish an
// invalidation message back to the client (on the same connection in
// RESP3, or on a redirected pub/sub channel in BCAST/REDIRECT mode).
//
// We support both modes:
//
//   default  — per-key invalidation: track exact keys touched
//   bcast    — broadcast: invalidate any key matching one of the
//              client's PREFIX subscriptions (no per-key state)
//
// The NOLOOP flag suppresses invalidations triggered by the same
// client's own writes — used by clients that already know they
// invalidated their cached value.
type TrackingTable struct {
	mu sync.RWMutex

	// per-key reverse index: key -> set of client IDs tracking it
	keys map[string]map[uint64]struct{}

	// per-client state: clientID -> tracking flags + prefix list
	clients map[uint64]*trackingClient
}

type trackingClient struct {
	on        bool
	bcast     bool
	noloop    bool
	redirect  uint64   // client ID to forward invalidations to (0 = self)
	prefixes  []string // for BCAST mode
}

// NewTrackingTable returns an empty tracking registry.
func NewTrackingTable() *TrackingTable {
	return &TrackingTable{
		keys:    map[string]map[uint64]struct{}{},
		clients: map[uint64]*trackingClient{},
	}
}

// Enable turns tracking on for clientID with the supplied options.
func (t *TrackingTable) Enable(clientID uint64, bcast, noloop bool, redirect uint64, prefixes []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clients[clientID] = &trackingClient{
		on: true, bcast: bcast, noloop: noloop,
		redirect: redirect, prefixes: append([]string(nil), prefixes...),
	}
}

// Disable removes a client from tracking and drops its key set.
func (t *TrackingTable) Disable(clientID uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.clients, clientID)
	for key, set := range t.keys {
		delete(set, clientID)
		if len(set) == 0 {
			delete(t.keys, key)
		}
	}
}

// IsEnabled reports whether the client opted in.
func (t *TrackingTable) IsEnabled(clientID uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	c, ok := t.clients[clientID]
	return ok && c.on
}

// RecordRead notes that clientID read keys. Default-mode clients have
// the keys reverse-indexed; BCAST clients track nothing per-key.
func (t *TrackingTable) RecordRead(clientID uint64, keys []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.clients[clientID]
	if !ok || !c.on || c.bcast {
		return
	}
	for _, k := range keys {
		set, ok := t.keys[k]
		if !ok {
			set = map[uint64]struct{}{}
			t.keys[k] = set
		}
		set[clientID] = struct{}{}
	}
}

// Invalidations returns the client IDs that should receive an
// invalidation for `key`. excludeClientID is the writer (used to honour
// NOLOOP).
//
// For default-mode clients the lookup is direct; for BCAST clients we
// scan their prefix list. The result also tells callers whether to
// drop the key from the reverse index after dispatch (Redis only fires
// once per key per client).
type InvalidationTarget struct {
	ClientID uint64
	Redirect uint64 // 0 = same conn
}

func (t *TrackingTable) Invalidations(key string, writer uint64) []InvalidationTarget {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []InvalidationTarget{}
	// default-mode hits
	if set, ok := t.keys[key]; ok {
		for cid := range set {
			c := t.clients[cid]
			if c == nil || (c.noloop && cid == writer) {
				continue
			}
			out = append(out, InvalidationTarget{ClientID: cid, Redirect: c.redirect})
		}
		// Per-key invalidations only fire once.
		delete(t.keys, key)
	}
	// bcast clients
	for cid, c := range t.clients {
		if !c.bcast || (c.noloop && cid == writer) {
			continue
		}
		matches := len(c.prefixes) == 0
		for _, p := range c.prefixes {
			if hasPrefix(key, p) {
				matches = true
				break
			}
		}
		if matches {
			out = append(out, InvalidationTarget{ClientID: cid, Redirect: c.redirect})
		}
	}
	return out
}

// Info reports the tracking state for `CLIENT TRACKINGINFO`.
type TrackingInfo struct {
	On       bool
	Bcast    bool
	NoLoop   bool
	Redirect uint64
	Prefixes []string
	NumKeys  int // tracked keys (default mode)
}

func (t *TrackingTable) Info(clientID uint64) TrackingInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	c, ok := t.clients[clientID]
	if !ok {
		return TrackingInfo{}
	}
	out := TrackingInfo{
		On: c.on, Bcast: c.bcast, NoLoop: c.noloop,
		Redirect: c.redirect, Prefixes: append([]string(nil), c.prefixes...),
	}
	for _, set := range t.keys {
		if _, ok := set[clientID]; ok {
			out.NumKeys++
		}
	}
	return out
}

func hasPrefix(s, p string) bool {
	if len(p) > len(s) {
		return false
	}
	return s[:len(p)] == p
}
