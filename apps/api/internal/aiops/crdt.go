package aiops

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

// CRDTRegistry implements four conflict-free replicated data types:
//
//	G-Counter    — grow-only counter; each actor owns a slot
//	PN-Counter   — increment+decrement counter (P − N)
//	OR-Set       — observed-remove set; remove only erases tags the
//	                caller has *seen*, so concurrent add+remove of the
//	                same element resolves to "added" (the safe default)
//	LWW-Register — last-writer-wins register keyed on timestamp + actor
//
// OSS Redis ships none of these. Redis Enterprise / CRDB does, but
// only as a paid add-on tied to active-active replication. NeuroCache's
// replication fan-out plus these CRDT operations gives you eventual
// consistency for multi-region writes without paying for Enterprise.
//
// Each key holds exactly one CRDT type; mixing types per key returns
// an error. The MERGE command is the central CRDT primitive — it
// joins two replicas' state without conflict regardless of message
// order or duplicates.
type CRDTRegistry struct {
	mu      sync.Mutex
	entries map[string]*crdtEntry
}

// CRDTKind labels the CRDT type for a key.
type CRDTKind string

const (
	CRDTGCounter CRDTKind = "g_counter"
	CRDTPNCounter CRDTKind = "pn_counter"
	CRDTORSet     CRDTKind = "or_set"
	CRDTLWW       CRDTKind = "lww_register"
)

// crdtEntry is the union over the four CRDT shapes. Only the field
// matching kind is meaningful; the others stay nil.
type crdtEntry struct {
	kind CRDTKind

	// G-Counter / PN-Counter state
	pos map[string]int64 // actor → positive count
	neg map[string]int64 // actor → negative count (PN only)

	// OR-Set state: element → set of unique tags. Each ADD mints a
	// fresh tag (we use "<actor>-<incrementing-counter>-<unix-ns>");
	// REMOVE erases every tag currently observed for the element. A
	// concurrent ADD on another replica produces a tag the remover
	// never saw, so the element survives the merge — that's the
	// observed-remove semantics.
	orset    map[string]map[string]bool
	orseqMu  sync.Mutex // protects orseq while we increment per-actor
	orseq    map[string]uint64

	// LWW-Register state
	lwwValue string
	lwwTS    int64  // unix-ns; ties broken by actor lex order
	lwwActor string

	createdAt time.Time
	updatedAt time.Time
}

// NewCRDTRegistry returns an empty registry.
func NewCRDTRegistry() *CRDTRegistry {
	return &CRDTRegistry{entries: map[string]*crdtEntry{}}
}

var (
	errCRDTUnknown      = errors.New("crdt key not found")
	errCRDTKindMismatch = errors.New("crdt kind mismatch for key")
)

// CRDTErrCode classifies the typed errors for the RESP handler.
func CRDTErrCode(err error) string {
	switch err {
	case errCRDTUnknown:
		return "NOCRDT"
	case errCRDTKindMismatch:
		return "WRONGTYPE"
	}
	return ""
}

// getOrCreate returns the entry, creating it with the requested kind
// if absent. Returns errCRDTKindMismatch when the existing kind doesn't
// match. Caller holds c.mu.
func (c *CRDTRegistry) getOrCreate(key string, kind CRDTKind) (*crdtEntry, error) {
	e, ok := c.entries[key]
	if ok {
		if e.kind != kind {
			return nil, errCRDTKindMismatch
		}
		e.updatedAt = time.Now()
		return e, nil
	}
	now := time.Now()
	e = &crdtEntry{
		kind:      kind,
		createdAt: now,
		updatedAt: now,
	}
	switch kind {
	case CRDTGCounter:
		e.pos = map[string]int64{}
	case CRDTPNCounter:
		e.pos = map[string]int64{}
		e.neg = map[string]int64{}
	case CRDTORSet:
		e.orset = map[string]map[string]bool{}
		e.orseq = map[string]uint64{}
	case CRDTLWW:
		// nothing to init
	}
	c.entries[key] = e
	return e, nil
}

// GIncr advances a G-Counter slot. Negative deltas are rejected — the
// "grow-only" property is what makes the merge a max-per-actor.
func (c *CRDTRegistry) GIncr(key, actor string, delta int64) (int64, error) {
	if delta < 0 {
		return 0, errors.New("g-counter delta must be non-negative")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.getOrCreate(key, CRDTGCounter)
	if err != nil {
		return 0, err
	}
	e.pos[actor] += delta
	return e.gValue(), nil
}

// GValue returns the sum of every actor's slot.
func (c *CRDTRegistry) GValue(key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return 0, errCRDTUnknown
	}
	if e.kind != CRDTGCounter {
		return 0, errCRDTKindMismatch
	}
	return e.gValue(), nil
}

func (e *crdtEntry) gValue() int64 {
	var sum int64
	for _, v := range e.pos {
		sum += v
	}
	return sum
}

// PNIncr advances a PN-Counter. Positive deltas land in P, negative
// deltas land in N (after taking abs).
func (c *CRDTRegistry) PNIncr(key, actor string, delta int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.getOrCreate(key, CRDTPNCounter)
	if err != nil {
		return 0, err
	}
	if delta >= 0 {
		e.pos[actor] += delta
	} else {
		e.neg[actor] += -delta
	}
	return e.pnValue(), nil
}

// PNValue is the P − N sum.
func (c *CRDTRegistry) PNValue(key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return 0, errCRDTUnknown
	}
	if e.kind != CRDTPNCounter {
		return 0, errCRDTKindMismatch
	}
	return e.pnValue(), nil
}

func (e *crdtEntry) pnValue() int64 {
	var p, n int64
	for _, v := range e.pos {
		p += v
	}
	for _, v := range e.neg {
		n += v
	}
	return p - n
}

// SAdd adds a member to an OR-Set. Each call mints a fresh unique tag
// for the (actor, member) pair so concurrent removes from peers can't
// erase what they didn't observe.
func (c *CRDTRegistry) SAdd(key, actor, member string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.getOrCreate(key, CRDTORSet)
	if err != nil {
		return 0, err
	}
	tags, ok := e.orset[member]
	if !ok {
		tags = map[string]bool{}
		e.orset[member] = tags
	}
	e.orseq[actor]++
	tag := actor + "-" + strconv.FormatUint(e.orseq[actor], 10) + "-" +
		strconv.FormatInt(time.Now().UnixNano(), 10)
	tags[tag] = true
	return len(tags), nil
}

// SRem removes every tag currently observed for member. Concurrent
// adds on a peer that haven't replicated will produce a fresh tag the
// remover never saw, so the element survives the merge — that's the
// observed-remove behaviour.
func (c *CRDTRegistry) SRem(key, member string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return 0, errCRDTUnknown
	}
	if e.kind != CRDTORSet {
		return 0, errCRDTKindMismatch
	}
	tags, ok := e.orset[member]
	if !ok {
		return 0, nil
	}
	n := len(tags)
	delete(e.orset, member)
	e.updatedAt = time.Now()
	return n, nil
}

// SMembers returns every present element in lexicographic order.
func (c *CRDTRegistry) SMembers(key string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, errCRDTUnknown
	}
	if e.kind != CRDTORSet {
		return nil, errCRDTKindMismatch
	}
	out := make([]string, 0, len(e.orset))
	for k, tags := range e.orset {
		if len(tags) > 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SIsMember returns 1 if member is present, 0 otherwise.
func (c *CRDTRegistry) SIsMember(key, member string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return false, errCRDTUnknown
	}
	if e.kind != CRDTORSet {
		return false, errCRDTKindMismatch
	}
	tags, ok := e.orset[member]
	return ok && len(tags) > 0, nil
}

// LWWSet stores value with a (timestamp, actor) tiebreaker. If ts is 0
// the manager fills in the current unix-ns. The new value wins iff its
// (ts, actor) tuple is strictly greater than the stored one — equal
// timestamps fall back to lexicographic actor comparison so divergent
// replicas converge on the same answer.
func (c *CRDTRegistry) LWWSet(key, actor, value string, ts int64) error {
	if ts == 0 {
		ts = time.Now().UnixNano()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.getOrCreate(key, CRDTLWW)
	if err != nil {
		return err
	}
	if ts > e.lwwTS || (ts == e.lwwTS && actor > e.lwwActor) {
		e.lwwValue = value
		e.lwwTS = ts
		e.lwwActor = actor
	}
	return nil
}

// LWWGet returns the stored value, its timestamp and the actor that
// wrote it.
func (c *CRDTRegistry) LWWGet(key string) (string, int64, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return "", 0, "", errCRDTUnknown
	}
	if e.kind != CRDTLWW {
		return "", 0, "", errCRDTKindMismatch
	}
	return e.lwwValue, e.lwwTS, e.lwwActor, nil
}

// Merge joins src's state into dst. Both keys must hold the same CRDT
// kind (otherwise errCRDTKindMismatch). Result lands in dst; src is
// untouched. Use this to converge two replicas during read repair or
// after a partition heals.
func (c *CRDTRegistry) Merge(dst, src string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.entries[dst]
	if !ok {
		return errCRDTUnknown
	}
	s, ok := c.entries[src]
	if !ok {
		return errCRDTUnknown
	}
	if d.kind != s.kind {
		return errCRDTKindMismatch
	}
	switch d.kind {
	case CRDTGCounter:
		for actor, v := range s.pos {
			if v > d.pos[actor] {
				d.pos[actor] = v
			}
		}
	case CRDTPNCounter:
		for actor, v := range s.pos {
			if v > d.pos[actor] {
				d.pos[actor] = v
			}
		}
		for actor, v := range s.neg {
			if v > d.neg[actor] {
				d.neg[actor] = v
			}
		}
	case CRDTORSet:
		for member, srcTags := range s.orset {
			dstTags, ok := d.orset[member]
			if !ok {
				dstTags = map[string]bool{}
				d.orset[member] = dstTags
			}
			for tag := range srcTags {
				dstTags[tag] = true
			}
		}
	case CRDTLWW:
		if s.lwwTS > d.lwwTS || (s.lwwTS == d.lwwTS && s.lwwActor > d.lwwActor) {
			d.lwwValue = s.lwwValue
			d.lwwTS = s.lwwTS
			d.lwwActor = s.lwwActor
		}
	}
	d.updatedAt = time.Now()
	return nil
}

// CRDTSnapshot is a debug-friendly view of the entry's full state.
// Slices/maps are copied so callers may mutate freely.
type CRDTSnapshot struct {
	Key       string            `json:"key"`
	Kind      CRDTKind          `json:"kind"`
	GValue    int64             `json:"value,omitempty"`
	Actors    map[string]int64  `json:"actors,omitempty"`
	NegActors map[string]int64  `json:"neg_actors,omitempty"`
	Members   []string          `json:"members,omitempty"`
	LWWValue  string            `json:"lww_value,omitempty"`
	LWWTS     int64             `json:"lww_ts,omitempty"`
	LWWActor  string            `json:"lww_actor,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// State returns the full debug snapshot for a key.
func (c *CRDTRegistry) State(key string) (CRDTSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return CRDTSnapshot{}, false
	}
	snap := CRDTSnapshot{
		Key:       key,
		Kind:      e.kind,
		CreatedAt: e.createdAt,
		UpdatedAt: e.updatedAt,
	}
	switch e.kind {
	case CRDTGCounter:
		snap.GValue = e.gValue()
		snap.Actors = copyInt64Map(e.pos)
	case CRDTPNCounter:
		snap.GValue = e.pnValue()
		snap.Actors = copyInt64Map(e.pos)
		snap.NegActors = copyInt64Map(e.neg)
	case CRDTORSet:
		snap.Members = make([]string, 0, len(e.orset))
		for k, tags := range e.orset {
			if len(tags) > 0 {
				snap.Members = append(snap.Members, k)
			}
		}
		sort.Strings(snap.Members)
	case CRDTLWW:
		snap.LWWValue = e.lwwValue
		snap.LWWTS = e.lwwTS
		snap.LWWActor = e.lwwActor
	}
	return snap, true
}

// Type returns the kind for a key, or empty string if the key is
// unknown.
func (c *CRDTRegistry) Type(key string) CRDTKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return ""
	}
	return e.kind
}

// List returns every known CRDT key, optionally filtered by kind.
// Output is sorted for stable display.
func (c *CRDTRegistry) List(kind CRDTKind) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.entries))
	for k, e := range c.entries {
		if kind != "" && e.kind != kind {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Forget drops a key.
func (c *CRDTRegistry) Forget(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	delete(c.entries, key)
	return ok
}

// CRDTStats rolls up the registry by type.
type CRDTStats struct {
	Total       int `json:"total"`
	GCounters   int `json:"g_counters"`
	PNCounters  int `json:"pn_counters"`
	ORSets      int `json:"or_sets"`
	LWWRegisters int `json:"lww_registers"`
}

// Stats returns the per-kind counts.
func (c *CRDTRegistry) Stats() CRDTStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := CRDTStats{Total: len(c.entries)}
	for _, e := range c.entries {
		switch e.kind {
		case CRDTGCounter:
			st.GCounters++
		case CRDTPNCounter:
			st.PNCounters++
		case CRDTORSet:
			st.ORSets++
		case CRDTLWW:
			st.LWWRegisters++
		}
	}
	return st
}

func copyInt64Map(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
