package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// CausalLog reconstructs the *causal* order of events emitted by a
// distributed multi-agent run. Redis streams give you *arrival* order
// (when the broker received each event), which is wrong as soon as
// agents on different machines emit out of order due to network
// jitter. Reconstructing what actually happened first in a debugging
// session means staring at clock skew and giving up.
//
// CAUSAL.LOG uses vector clocks (one counter per actor). Each event
// the caller appends:
//
//   - bumps the caller's own counter
//   - includes the dependencies (other events this event "happens-
//     after" — typically the responses to which this is a reply)
//
// READ returns the events in a topological order consistent with the
// happens-before relation. Events with no causal dependency between
// them stay in arrival order (a stable tie-break) — concurrent events
// stay concurrent. The wall-clock timestamp is recorded but never
// used for ordering.
//
// Commands:
//
//   CAUSAL.APPEND log-id actor payload [AFTER e1 e2 ...]
//        AFTER = event ids this event causally depends on.
//        → event-id (server-assigned, monotonic per log)
//   CAUSAL.READ log-id [LIMIT n]
//        Returns events in topological order.
//   CAUSAL.HAPPENS_BEFORE log-id a b
//        → 1 if a happens-before b, 0 otherwise (also reports "concurrent").
//   CAUSAL.CLOCK log-id actor
//        → current vector-clock counter for actor on this log.
//   CAUSAL.FORGET log-id|ALL
//   CAUSAL.LIST
//   CAUSAL.STATS
//
// Hot path: APPEND is one map op + a vector-clock merge bounded by
// #actors. READ does a topo sort (Kahn's algorithm) on demand — fine
// for typical session sizes (hundreds of events).
type CausalLog struct {
	mu   sync.RWMutex
	logs map[string]*causalRun

	totalAppends atomic.Int64
	totalReads   atomic.Int64
}

type causalRun struct {
	mu     sync.Mutex
	nextID int64
	events map[string]*causalEvent
	order  []string // arrival order
	clocks map[string]map[string]int64 // actor → vector clock
}

type causalEvent struct {
	ID      string
	Actor   string
	Payload string
	After   []string
	Clock   map[string]int64 // snapshot at append time
	WallTS  time.Time
}

// NewCausalLog returns an empty registry.
func NewCausalLog() *CausalLog {
	return &CausalLog{logs: map[string]*causalRun{}}
}

// CausalAppendResult is APPEND's return.
type CausalAppendResult struct {
	EventID string `json:"event_id"`
}

// Append records one event. The vector clock for actor is bumped by 1
// and merged with the max of every AFTER event's clock.
func (c *CausalLog) Append(logID, actor, payload string, after []string) (CausalAppendResult, error) {
	if logID == "" {
		return CausalAppendResult{}, errors.New("log_id required")
	}
	if actor == "" {
		return CausalAppendResult{}, errors.New("actor required")
	}
	c.totalAppends.Add(1)
	r := c.runOrCreate(logID)
	r.mu.Lock()
	defer r.mu.Unlock()
	// Merge clocks of every AFTER event into our local clock
	local := r.clocks[actor]
	if local == nil {
		local = map[string]int64{}
	}
	for _, dep := range after {
		ev, ok := r.events[dep]
		if !ok {
			return CausalAppendResult{}, errors.New("unknown AFTER event: " + dep)
		}
		for a, v := range ev.Clock {
			if v > local[a] {
				local[a] = v
			}
		}
	}
	local[actor]++
	r.clocks[actor] = local
	r.nextID++
	id := "e-" + u32x(uint32(r.nextID))
	snap := make(map[string]int64, len(local))
	for k, v := range local {
		snap[k] = v
	}
	r.events[id] = &causalEvent{
		ID: id, Actor: actor, Payload: payload,
		After: append([]string{}, after...),
		Clock: snap, WallTS: time.Now(),
	}
	r.order = append(r.order, id)
	return CausalAppendResult{EventID: id}, nil
}

// CausalReadRow is one row of READ.
type CausalReadRow struct {
	EventID string `json:"event_id"`
	Actor   string `json:"actor"`
	Payload string `json:"payload"`
	After   []string `json:"after,omitempty"`
	WallTSUnix int64 `json:"wall_unix"`
}

// Read returns events in topological order. Concurrent events stay in
// arrival order (stable tie-break).
func (c *CausalLog) Read(logID string, limit int) ([]CausalReadRow, bool) {
	if logID == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 1000
	}
	c.totalReads.Add(1)
	c.mu.RLock()
	r, ok := c.logs[logID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Kahn's algorithm
	indeg := make(map[string]int, len(r.events))
	for _, e := range r.events {
		indeg[e.ID] = len(e.After)
	}
	// Reverse adjacency: parent → []children
	children := make(map[string][]string, len(r.events))
	for _, e := range r.events {
		for _, p := range e.After {
			children[p] = append(children[p], e.ID)
		}
	}
	// Queue in arrival order, only picking up nodes whose indeg=0
	ready := make([]string, 0, len(r.order))
	for _, id := range r.order {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	out := make([]CausalReadRow, 0, len(r.events))
	for len(ready) > 0 && len(out) < limit {
		next := ready[0]
		ready = ready[1:]
		e := r.events[next]
		out = append(out, CausalReadRow{
			EventID: e.ID, Actor: e.Actor, Payload: e.Payload,
			After: append([]string{}, e.After...),
			WallTSUnix: e.WallTS.Unix(),
		})
		// release children
		for _, ch := range children[next] {
			indeg[ch]--
			if indeg[ch] == 0 {
				ready = append(ready, ch)
			}
		}
	}
	return out, true
}

// CausalHB is HAPPENS_BEFORE's return.
type CausalHB struct {
	A          string `json:"a"`
	B          string `json:"b"`
	HappensBefore bool `json:"happens_before"`
	Concurrent bool   `json:"concurrent"`
	Reason     string `json:"reason"`
}

// HappensBefore answers a < b? Result is one of:
//   - happens_before=true (a < b)
//   - happens_before=false with concurrent=true (incomparable)
//   - happens_before=false with concurrent=false (b < a, opposite)
func (c *CausalLog) HappensBefore(logID, a, b string) (CausalHB, bool) {
	c.mu.RLock()
	r, ok := c.logs[logID]
	c.mu.RUnlock()
	if !ok {
		return CausalHB{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ea, oka := r.events[a]
	eb, okb := r.events[b]
	if !oka || !okb {
		return CausalHB{}, false
	}
	out := CausalHB{A: a, B: b}
	switch compareClocks(ea.Clock, eb.Clock) {
	case -1:
		out.HappensBefore = true
		out.Reason = "a's clock <= b's clock everywhere, strictly less in at least one actor"
	case 1:
		out.HappensBefore = false
		out.Reason = "b's clock <= a's clock everywhere → b happens-before a"
	case 0:
		out.HappensBefore = false
		out.Reason = "clocks are equal — a and b are the same event"
	case 2:
		out.HappensBefore = false
		out.Concurrent = true
		out.Reason = "clocks are incomparable — a and b are concurrent"
	}
	return out, true
}

// Clock returns the actor's current vector-clock counter on this log.
func (c *CausalLog) Clock(logID, actor string) (int64, bool) {
	c.mu.RLock()
	r, ok := c.logs[logID]
	c.mu.RUnlock()
	if !ok {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	local := r.clocks[actor]
	if local == nil {
		return 0, true
	}
	return local[actor], true
}

// Forget drops a log (or all).
func (c *CausalLog) Forget(logID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if logID == "ALL" {
		n := len(c.logs)
		c.logs = map[string]*causalRun{}
		return n
	}
	if _, ok := c.logs[logID]; ok {
		delete(c.logs, logID)
		return 1
	}
	return 0
}

// List returns every known log id.
func (c *CausalLog) List() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.logs))
	for k := range c.logs {
		out = append(out, k)
	}
	c.mu.RUnlock()
	sort.Strings(out)
	return out
}

// CausalStats is the global snapshot.
type CausalStats struct {
	Logs         int   `json:"logs"`
	TotalAppends int64 `json:"total_appends"`
	TotalReads   int64 `json:"total_reads"`
	TotalEvents  int   `json:"total_events"`
}

func (c *CausalLog) Stats() CausalStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ev := 0
	for _, r := range c.logs {
		r.mu.Lock()
		ev += len(r.events)
		r.mu.Unlock()
	}
	return CausalStats{
		Logs:         len(c.logs),
		TotalAppends: c.totalAppends.Load(),
		TotalReads:   c.totalReads.Load(),
		TotalEvents:  ev,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (c *CausalLog) runOrCreate(id string) *causalRun {
	c.mu.RLock()
	r, ok := c.logs[id]
	c.mu.RUnlock()
	if ok {
		return r
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.logs[id]; ok {
		return r
	}
	r = &causalRun{
		events: map[string]*causalEvent{},
		clocks: map[string]map[string]int64{},
	}
	c.logs[id] = r
	return r
}

// compareClocks returns:
//   -1 if a < b (a happens-before b)
//    0 if a == b
//    1 if a > b (b happens-before a)
//    2 if incomparable (concurrent)
func compareClocks(a, b map[string]int64) int {
	allKeys := map[string]bool{}
	for k := range a {
		allKeys[k] = true
	}
	for k := range b {
		allKeys[k] = true
	}
	aLess, bLess := false, false
	for k := range allKeys {
		va := a[k]
		vb := b[k]
		if va < vb {
			aLess = true
		}
		if vb < va {
			bLess = true
		}
	}
	switch {
	case aLess && bLess:
		return 2
	case aLess:
		return -1
	case bLess:
		return 1
	default:
		return 0
	}
}
