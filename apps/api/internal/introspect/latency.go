package introspect

import (
	"sync"
	"time"
)

// LatencyMonitor implements LATENCY HISTORY/LATEST/RESET/DOCTOR/GRAPH.
// We bucket events by name (e.g. "command", "fork", "rdb-fsync") and
// keep a per-name ring buffer so high-volume events don't crowd out
// rare-but-important ones.
type LatencyMonitor struct {
	maxLen int
	mu     sync.Mutex
	events map[string]*latencyRing // name -> ring
}

// LatencyEvent is a single latency observation.
type LatencyEvent struct {
	Name      string
	At        time.Time
	Latency   time.Duration
}

type latencyRing struct {
	entries []LatencyEvent
	max     int
}

// NewLatencyMonitor returns an empty monitor with at most maxLen entries
// per event name (default 160 — Redis' default).
func NewLatencyMonitor(maxLen int) *LatencyMonitor {
	if maxLen <= 0 {
		maxLen = 160
	}
	return &LatencyMonitor{maxLen: maxLen, events: map[string]*latencyRing{}}
}

// Record adds an observation. Cheap (one map lookup + slice append).
func (l *LatencyMonitor) Record(name string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.events[name]
	if !ok {
		r = &latencyRing{max: l.maxLen}
		l.events[name] = r
	}
	if len(r.entries) == r.max {
		r.entries = r.entries[1:]
	}
	r.entries = append(r.entries, LatencyEvent{Name: name, At: time.Now(), Latency: d})
}

// History returns every observation for an event (oldest-first).
func (l *LatencyMonitor) History(name string) []LatencyEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.events[name]
	if !ok {
		return nil
	}
	out := make([]LatencyEvent, len(r.entries))
	copy(out, r.entries)
	return out
}

// Latest returns one row per known event with its most recent value.
func (l *LatencyMonitor) Latest() []LatencyEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []LatencyEvent{}
	for _, r := range l.events {
		if len(r.entries) > 0 {
			out = append(out, r.entries[len(r.entries)-1])
		}
	}
	return out
}

// Reset clears one or all event buckets. names=nil → reset everything.
// Returns the number of buckets cleared.
func (l *LatencyMonitor) Reset(names ...string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(names) == 0 {
		n := len(l.events)
		l.events = map[string]*latencyRing{}
		return n
	}
	n := 0
	for _, name := range names {
		if _, ok := l.events[name]; ok {
			delete(l.events, name)
			n++
		}
	}
	return n
}

// Doctor returns a one-shot diagnostic summary string. Mirrors Redis'
// human-readable LATENCY DOCTOR output style.
func (l *LatencyMonitor) Doctor() string {
	latest := l.Latest()
	if len(latest) == 0 {
		return "Dave, I have observed the system to be quiet. Smooth sailing.\n"
	}
	out := "Dave, I have observed the following latency events:\n"
	for _, e := range latest {
		out += "  - " + e.Name + ": " + e.Latency.String() + " (last seen " + e.At.Format(time.RFC3339) + ")\n"
	}
	return out
}
