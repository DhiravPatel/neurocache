package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// TemporalSnapshots is the unified point-in-time query across the
// engine's belief stores. Individual primitives (KEY.AT, DOC.FRESH,
// MEMORY) each have their own time-travel — TEMPORAL composes them
// into a coherent "what did the system believe at T" view, which is
// what postmortems actually need.
//
// The flow:
//
//   1. App calls SNAPSHOT periodically (every few minutes during
//      normal ops, more often during an incident). Each snapshot
//      captures a labeled bundle of (timestamp + opaque-payload
//      slices contributed by each store).
//
//   2. AT-T returns the snapshot bundle nearest to T (≤ T).
//
//   3. DIFF compares two snapshots and reports which stores changed.
//
// The engine doesn't enforce any structure on the contributed
// payloads — stores hand TEMPORAL whatever blob they want. The point
// of this primitive is alignment: one timestamp across stores.
//
// Commands:
//
//   TEMPORAL.SNAPSHOT snap-id [META k v ...]
//        Open a new empty snapshot. Stores then call CONTRIBUTE.
//   TEMPORAL.CONTRIBUTE snap-id store payload
//        Add one store's contribution.
//   TEMPORAL.CLOSE snap-id          — seal the snapshot (read-only).
//   TEMPORAL.AT unix-ms             → nearest closed snapshot ≤ T
//   TEMPORAL.GET snap-id            → full bundle dump
//   TEMPORAL.DIFF snap-a snap-b     → which stores have different payloads
//   TEMPORAL.LIST [LIMIT n]         → reverse-chronological
//   TEMPORAL.FORGET snap-id|ALL
//   TEMPORAL.STATS
//
// Hot path: SNAPSHOT/CONTRIBUTE/CLOSE are map ops. AT is a sort over
// known snapshots (typically dozens-to-hundreds, not millions).
type TemporalSnapshots struct {
	mu        sync.RWMutex
	snapshots map[string]*temporalSnap

	totalSnaps   atomic.Int64
	totalContrib atomic.Int64
	totalQueries atomic.Int64
}

type temporalSnap struct {
	mu        sync.RWMutex
	id        string
	at        time.Time
	closed    bool
	meta      map[string]string
	stores    map[string]string // store name → payload
}

// NewTemporalSnapshots returns an empty registry.
func NewTemporalSnapshots() *TemporalSnapshots {
	return &TemporalSnapshots{snapshots: map[string]*temporalSnap{}}
}

// Snapshot opens a new empty snapshot.
func (t *TemporalSnapshots) Snapshot(id string, meta map[string]string) error {
	if id == "" {
		return errors.New("snap_id required")
	}
	t.totalSnaps.Add(1)
	cp := map[string]string{}
	for k, v := range meta {
		cp[k] = v
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.snapshots[id]; ok {
		return errors.New("snapshot already exists: " + id)
	}
	// Truncate to millisecond resolution so AT(unix-ms) can find this
	// snapshot exactly (without nanosecond-precision skew making the
	// snapshot appear to be in the "future" relative to a ms-floored
	// target time).
	now := time.UnixMilli(time.Now().UnixMilli())
	t.snapshots[id] = &temporalSnap{
		id: id, at: now, meta: cp,
		stores: map[string]string{},
	}
	return nil
}

// Contribute attaches a store's payload to an open snapshot.
func (t *TemporalSnapshots) Contribute(snapID, store, payload string) error {
	if snapID == "" || store == "" {
		return errors.New("snap_id and store required")
	}
	t.totalContrib.Add(1)
	t.mu.RLock()
	s, ok := t.snapshots[snapID]
	t.mu.RUnlock()
	if !ok {
		return errors.New("unknown snap_id: " + snapID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("snapshot already closed")
	}
	s.stores[store] = payload
	return nil
}

// Close seals a snapshot for further contributions.
func (t *TemporalSnapshots) Close(snapID string) error {
	if snapID == "" {
		return errors.New("snap_id required")
	}
	t.mu.RLock()
	s, ok := t.snapshots[snapID]
	t.mu.RUnlock()
	if !ok {
		return errors.New("unknown snap_id: " + snapID)
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// TemporalView is GET / AT's return.
type TemporalView struct {
	SnapID   string            `json:"snap_id"`
	AtUnix   int64             `json:"at_unix_ms"`
	Closed   bool              `json:"closed"`
	Meta     map[string]string `json:"meta,omitempty"`
	Stores   map[string]string `json:"stores"`
}

// Get returns the full snapshot.
func (t *TemporalSnapshots) Get(snapID string) (TemporalView, bool) {
	t.totalQueries.Add(1)
	t.mu.RLock()
	s, ok := t.snapshots[snapID]
	t.mu.RUnlock()
	if !ok {
		return TemporalView{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := TemporalView{
		SnapID: s.id, AtUnix: s.at.UnixMilli(),
		Closed: s.closed,
		Meta:   copyMetaProv(s.meta),
		Stores: map[string]string{},
	}
	for k, v := range s.stores {
		out.Stores[k] = v
	}
	return out, true
}

// At returns the most-recent closed snapshot at or before atUnixMS.
func (t *TemporalSnapshots) At(atUnixMS int64) (TemporalView, bool) {
	if atUnixMS <= 0 {
		return TemporalView{}, false
	}
	t.totalQueries.Add(1)
	target := time.UnixMilli(atUnixMS)
	t.mu.RLock()
	var best *temporalSnap
	for _, s := range t.snapshots {
		s.mu.RLock()
		closed := s.closed
		at := s.at
		s.mu.RUnlock()
		if !closed {
			continue
		}
		if at.After(target) {
			continue
		}
		if best == nil || at.After(best.at) {
			best = s
		}
	}
	t.mu.RUnlock()
	if best == nil {
		return TemporalView{}, false
	}
	return t.Get(best.id)
}

// TemporalDiff is DIFF's return.
type TemporalDiff struct {
	SnapA       string   `json:"snap_a"`
	SnapB       string   `json:"snap_b"`
	Identical   bool     `json:"identical"`
	OnlyInA     []string `json:"only_in_a"`
	OnlyInB     []string `json:"only_in_b"`
	Changed     []string `json:"changed"`
	Same        []string `json:"same"`
}

// Diff compares two snapshots, returning which stores changed.
func (t *TemporalSnapshots) Diff(snapA, snapB string) (TemporalDiff, bool) {
	a, ok := t.Get(snapA)
	if !ok {
		return TemporalDiff{}, false
	}
	b, ok := t.Get(snapB)
	if !ok {
		return TemporalDiff{}, false
	}
	out := TemporalDiff{SnapA: snapA, SnapB: snapB}
	for k, va := range a.Stores {
		vb, hasB := b.Stores[k]
		if !hasB {
			out.OnlyInA = append(out.OnlyInA, k)
			continue
		}
		if va != vb {
			out.Changed = append(out.Changed, k)
		} else {
			out.Same = append(out.Same, k)
		}
	}
	for k := range b.Stores {
		if _, hasA := a.Stores[k]; !hasA {
			out.OnlyInB = append(out.OnlyInB, k)
		}
	}
	out.Identical = len(out.OnlyInA) == 0 && len(out.OnlyInB) == 0 && len(out.Changed) == 0
	sort.Strings(out.OnlyInA)
	sort.Strings(out.OnlyInB)
	sort.Strings(out.Changed)
	sort.Strings(out.Same)
	return out, true
}

// TemporalListRow is one row of LIST.
type TemporalListRow struct {
	SnapID string `json:"snap_id"`
	AtUnix int64  `json:"at_unix"`
	Closed bool   `json:"closed"`
	Stores int    `json:"stores"`
}

// List returns the most-recent snapshots.
func (t *TemporalSnapshots) List(limit int) []TemporalListRow {
	if limit <= 0 {
		limit = 50
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]TemporalListRow, 0, len(t.snapshots))
	for _, s := range t.snapshots {
		s.mu.RLock()
		out = append(out, TemporalListRow{
			SnapID: s.id, AtUnix: s.at.UnixMilli(),
			Closed: s.closed, Stores: len(s.stores),
		})
		s.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AtUnix > out[j].AtUnix })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Forget drops a snapshot (or all).
func (t *TemporalSnapshots) Forget(snapID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if snapID == "ALL" {
		n := len(t.snapshots)
		t.snapshots = map[string]*temporalSnap{}
		return n
	}
	if _, ok := t.snapshots[snapID]; ok {
		delete(t.snapshots, snapID)
		return 1
	}
	return 0
}

// TemporalStats is the global snapshot.
type TemporalStats struct {
	Snapshots     int   `json:"snapshots"`
	TotalSnaps    int64 `json:"total_snaps"`
	TotalContrib  int64 `json:"total_contributions"`
	TotalQueries  int64 `json:"total_queries"`
}

func (t *TemporalSnapshots) Stats() TemporalStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TemporalStats{
		Snapshots:    len(t.snapshots),
		TotalSnaps:   t.totalSnaps.Load(),
		TotalContrib: t.totalContrib.Load(),
		TotalQueries: t.totalQueries.Load(),
	}
}
