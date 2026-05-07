package aiops

import (
	"sync"
	"time"
)

// Audit is an append-only structured event log for compliance use
// cases (SOC2, HIPAA, GDPR access logs). Each entry is immutable —
// records can be queried by actor / resource / action / time range,
// but never modified or deleted (except by retention sweep).
//
// Indexes by actor, resource, and action so the typical "who did
// what to X this week?" query is fast without scanning every row.
type Audit struct {
	mu      sync.RWMutex
	events  []*AuditEvent
	byActor map[string][]int // actor → entry indices
	byRes   map[string][]int // resource → entry indices
	byAct   map[string][]int // action → entry indices

	maxEntries int // ring cap (default 1M); older events drop
}

// AuditEvent is one immutable audit record.
type AuditEvent struct {
	ID        int64             `json:"id"`
	Actor     string            `json:"actor"`
	Action    string            `json:"action"`
	Resource  string            `json:"resource"`
	Outcome   string            `json:"outcome,omitempty"` // "success" | "deny" | "error" | ""
	Attrs     map[string]string `json:"attrs,omitempty"`
	At        time.Time         `json:"at"`
}

// NewAudit returns a manager with a 1M-entry ring.
func NewAudit() *Audit {
	return &Audit{
		byActor:    map[string][]int{},
		byRes:      map[string][]int{},
		byAct:      map[string][]int{},
		maxEntries: 1_000_000,
	}
}

// SetMaxEntries adjusts the ring size at runtime. Older events are
// dropped if shrinking; growing just allows more headroom.
func (a *Audit) SetMaxEntries(n int) {
	if n <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maxEntries = n
	// Trim if we're over.
	if len(a.events) > n {
		drop := len(a.events) - n
		a.events = a.events[drop:]
		// Rebuild indexes (cheap relative to the size of the cap).
		a.rebuildIndexes()
	}
}

// Log appends a record. Returns the assigned entry ID.
func (a *Audit) Log(actor, action, resource, outcome string, attrs map[string]string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := int64(len(a.events) + 1)
	if len(a.events) > 0 {
		id = a.events[len(a.events)-1].ID + 1
	}
	ev := &AuditEvent{
		ID:       id,
		Actor:    actor,
		Action:   action,
		Resource: resource,
		Outcome:  outcome,
		Attrs:    cloneAttrs(attrs),
		At:       time.Now(),
	}
	idx := len(a.events)
	a.events = append(a.events, ev)
	a.byActor[actor] = append(a.byActor[actor], idx)
	a.byRes[resource] = append(a.byRes[resource], idx)
	a.byAct[action] = append(a.byAct[action], idx)

	// Ring eviction: drop the oldest when over cap. We collapse the
	// indexes by rebuilding because the underlying slice shifts.
	if len(a.events) > a.maxEntries {
		drop := len(a.events) - a.maxEntries
		a.events = a.events[drop:]
		a.rebuildIndexes()
	}
	return id
}

// Query returns every event matching the (optional) filters. An empty
// filter selects everything in that dimension. `since` and `until`
// are inclusive; pass time.Time{} to disable. Result is reverse-
// chronological (newest first), capped at limit (0 = no cap).
type AuditQuery struct {
	Actor    string
	Action   string
	Resource string
	Since    time.Time
	Until    time.Time
	Limit    int
}

// Query runs a search. Combines the appropriate index for the most
// selective filter, then walks the result with the others as masks.
func (a *Audit) Query(q AuditQuery) []*AuditEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Pick the most selective index to drive the iteration.
	var idxs []int
	switch {
	case q.Actor != "":
		idxs = a.byActor[q.Actor]
	case q.Resource != "":
		idxs = a.byRes[q.Resource]
	case q.Action != "":
		idxs = a.byAct[q.Action]
	default:
		idxs = nil // sentinel: walk every event
	}

	out := []*AuditEvent{}
	walk := func(ev *AuditEvent) bool {
		if q.Actor != "" && ev.Actor != q.Actor {
			return false
		}
		if q.Action != "" && ev.Action != q.Action {
			return false
		}
		if q.Resource != "" && ev.Resource != q.Resource {
			return false
		}
		if !q.Since.IsZero() && ev.At.Before(q.Since) {
			return false
		}
		if !q.Until.IsZero() && ev.At.After(q.Until) {
			return false
		}
		return true
	}
	if idxs != nil {
		// Walk in reverse-chronological order.
		for i := len(idxs) - 1; i >= 0; i-- {
			ev := a.events[idxs[i]]
			if walk(ev) {
				out = append(out, ev)
				if q.Limit > 0 && len(out) >= q.Limit {
					break
				}
			}
		}
	} else {
		for i := len(a.events) - 1; i >= 0; i-- {
			ev := a.events[i]
			if walk(ev) {
				out = append(out, ev)
				if q.Limit > 0 && len(out) >= q.Limit {
					break
				}
			}
		}
	}
	return out
}

// Count returns the number of stored events.
func (a *Audit) Count() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.events)
}

// AuditStats snapshots the manager.
type AuditStats struct {
	Entries    int `json:"entries"`
	MaxEntries int `json:"max_entries"`
	Actors     int `json:"unique_actors"`
	Resources  int `json:"unique_resources"`
	Actions    int `json:"unique_actions"`
}

// Stats returns a snapshot.
func (a *Audit) Stats() AuditStats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return AuditStats{
		Entries:    len(a.events),
		MaxEntries: a.maxEntries,
		Actors:     len(a.byActor),
		Resources:  len(a.byRes),
		Actions:    len(a.byAct),
	}
}

func (a *Audit) rebuildIndexes() {
	a.byActor = map[string][]int{}
	a.byRes = map[string][]int{}
	a.byAct = map[string][]int{}
	for i, ev := range a.events {
		a.byActor[ev.Actor] = append(a.byActor[ev.Actor], i)
		a.byRes[ev.Resource] = append(a.byRes[ev.Resource], i)
		a.byAct[ev.Action] = append(a.byAct[ev.Action], i)
	}
}

func cloneAttrs(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
