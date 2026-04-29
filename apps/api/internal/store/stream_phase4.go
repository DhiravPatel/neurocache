package store

import (
	"errors"
	"strings"
	"time"
)

// ── XACKDEL ───────────────────────────────────────────────────────
//
// XAckDel acknowledges entries to a consumer group AND removes them
// from the stream itself in a single atomic operation. Workflow most
// teams hand-roll today via two separate calls (XACK + XDEL) raced
// against late-arriving consumers; doing it atomically prevents the
// race where a second consumer grabs the entry between the ACK and
// the DEL.
//
// Returns the count of entries that were both ACKed AND deleted.
// IDs that weren't in the group's PEL are silently skipped — this
// matches XACK's "the count is what we processed, not what you asked
// for" convention.
func (s *Store) XAckDel(key, group string, ids ...string) (int, error) {
	if len(ids) == 0 {
		return 0, errors.New("XACKDEL requires at least one ID")
	}
	parsed := make([]StreamID, 0, len(ids))
	for _, raw := range ids {
		id, err := ParseStreamID(raw, false)
		if err != nil {
			return 0, err
		}
		parsed = append(parsed, id)
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	acked, err := xackDelLocked(e.Stream, group, parsed)
	if err != nil {
		return 0, err
	}
	if acked > 0 {
		// recomputeBytes -> approxBytes acquires Stream.mu — must
		// happen after the locked critical section above released it.
		s.recomputeBytes(e)
		s.fire("xack", key)
	}
	return acked, nil
}

// xackDelLocked acks + deletes within a single Stream lock. Pulled out
// so the public XAckDel can release Stream.mu before recomputeBytes
// (which re-locks it via approxBytes).
func xackDelLocked(stream *Stream, group string, ids []StreamID) (int, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	g, ok := stream.groups[group]
	if !ok {
		return 0, errors.New("NOGROUP No such consumer group")
	}
	acked := 0
	victims := map[StreamID]struct{}{}
	for _, id := range ids {
		if pe, ok := g.Pending[id]; ok {
			delete(g.Pending, id)
			if c, ok := g.Consumers[pe.Consumer]; ok {
				delete(c.Pending, id)
			}
			acked++
			victims[id] = struct{}{}
		}
	}
	if len(victims) > 0 {
		kept := stream.entries[:0]
		for _, ent := range stream.entries {
			if _, drop := victims[ent.ID]; drop {
				continue
			}
			kept = append(kept, ent)
		}
		stream.entries = kept
	}
	return acked, nil
}

// ── XDELEX ────────────────────────────────────────────────────────
//
// XDelExMode controls how XDELEX handles entries that are still
// referenced by a consumer-group PEL.
type XDelExMode int

const (
	// XDelExKeepRef is the legacy XDEL behaviour — drop entries from
	// the stream regardless of PEL state. PEL entries that pointed at
	// the deleted IDs become "orphaned" references; XPENDING continues
	// to surface them but XCLAIM / XAUTOCLAIM can no longer materialize
	// the underlying record.
	XDelExKeepRef XDelExMode = iota
	// XDelExRef refuses to delete an entry while any consumer group
	// holds a pending reference to it. The op returns the count of
	// entries actually removed; refused IDs are silently skipped.
	XDelExRef
	// XDelExAcked deletes entries only when every consumer group has
	// already ACKed them — i.e. the entry is unreferenced everywhere.
	XDelExAcked
)

// ParseXDelExMode decodes the mode token. Defaults to KEEPREF when the
// token is empty so callers omitting the flag get classic XDEL semantics.
func ParseXDelExMode(s string) (XDelExMode, error) {
	switch strings.ToUpper(s) {
	case "", "KEEPREF":
		return XDelExKeepRef, nil
	case "REF":
		return XDelExRef, nil
	case "ACKED":
		return XDelExAcked, nil
	}
	return 0, errors.New("syntax error: mode must be KEEPREF | REF | ACKED")
}

// XDelEx is XDEL with reference-aware deletion. Returns the count of
// entries actually removed.
func (s *Store) XDelEx(key string, mode XDelExMode, ids ...string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	parsed := make([]StreamID, 0, len(ids))
	for _, raw := range ids {
		id, err := ParseStreamID(raw, false)
		if err != nil {
			return 0, err
		}
		parsed = append(parsed, id)
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	removed := xdelExLocked(e.Stream, mode, parsed)
	if removed > 0 {
		s.recomputeBytes(e)
		s.fire("xdel", key)
	}
	return removed, nil
}

// xdelExLocked applies the mode filter + slice rebuild under a single
// Stream lock so the caller can recomputeBytes after release.
func xdelExLocked(stream *Stream, mode XDelExMode, ids []StreamID) int {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	victims := map[StreamID]struct{}{}
	for _, id := range ids {
		switch mode {
		case XDelExKeepRef:
			victims[id] = struct{}{}
		case XDelExRef:
			referenced := false
			for _, g := range stream.groups {
				if _, has := g.Pending[id]; has {
					referenced = true
					break
				}
			}
			if !referenced {
				victims[id] = struct{}{}
			}
		case XDelExAcked:
			pending := false
			for _, g := range stream.groups {
				if _, has := g.Pending[id]; has {
					pending = true
					break
				}
			}
			if !pending {
				victims[id] = struct{}{}
			}
		}
	}
	if len(victims) == 0 {
		return 0
	}
	kept := stream.entries[:0]
	removed := 0
	for _, ent := range stream.entries {
		if _, drop := victims[ent.ID]; drop {
			removed++
			continue
		}
		kept = append(kept, ent)
	}
	stream.entries = kept
	return removed
}

// ── XCFGSET ───────────────────────────────────────────────────────
//
// XCfgSet adjusts per-group runtime limits without dropping the
// group. Today it surfaces two knobs that operators ask for after
// they've shipped a queue and noticed misbehaving consumers:
//
//   MAXDELIVERIES n  — soft cap on how many times a single ID can be
//                      claimed before XPENDING starts flagging it as
//                      "poison". Stored on the group; the read paths
//                      surface it via XINFO GROUPS.
//   MINIDLE ms       — minimum idle window before XAUTOCLAIM is
//                      allowed to reassign an entry. Lets ops tune
//                      claim aggressiveness per group.
//
// Both knobs default to "no limit" (zero) so legacy groups behave
// identically until an operator explicitly tunes them.
type GroupConfig struct {
	MaxDeliveries int64
	MinIdleMs     int64
}

// XCfgSet applies the requested changes to the named group. Returns
// the post-change GroupConfig so callers can confirm what landed.
//
// Group-config writes don't change byte accounting (no entry data
// shifts), so we don't need recomputeBytes — meaning we can keep both
// locks for the brief read-modify-write without the deadlock the
// XAckDel / XDelEx paths had to dodge.
func (s *Store) XCfgSet(key, group string, cfg GroupConfig) (GroupConfig, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return GroupConfig{}, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	g, ok := e.Stream.groups[group]
	if !ok {
		return GroupConfig{}, errors.New("NOGROUP No such consumer group")
	}
	if cfg.MaxDeliveries >= 0 {
		g.MaxDeliveries = cfg.MaxDeliveries
	}
	if cfg.MinIdleMs >= 0 {
		g.MinIdleMs = cfg.MinIdleMs
	}
	return GroupConfig{
		MaxDeliveries: g.MaxDeliveries,
		MinIdleMs:     g.MinIdleMs,
	}, nil
}

// XCfgGet reports the current per-group config (for XINFO + tests).
func (s *Store) XCfgGet(key, group string) (GroupConfig, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return GroupConfig{}, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	g, ok := e.Stream.groups[group]
	if !ok {
		return GroupConfig{}, errors.New("NOGROUP No such consumer group")
	}
	return GroupConfig{
		MaxDeliveries: g.MaxDeliveries,
		MinIdleMs:     g.MinIdleMs,
	}, nil
}

// silence unused "time" if a future refactor drops the import
var _ = time.Now
