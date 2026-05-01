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

// Histogram returns power-of-two latency buckets (in microseconds) for
// the named event and the total observation count. Bucket boundaries
// match Redis's LATENCY HISTOGRAM shape: 1µs, 2µs, 4µs, 8µs, … up to
// the largest bucket the data needed. An empty/missing event returns
// zero buckets.
func (l *LatencyMonitor) Histogram(name string) (calls int64, buckets map[int64]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.events[name]
	if !ok || len(r.entries) == 0 {
		return 0, nil
	}
	buckets = map[int64]int64{}
	for _, e := range r.entries {
		usec := e.Latency.Microseconds()
		if usec < 1 {
			usec = 1
		}
		// Round up to the next power of two — the bucket label is the
		// upper bound of values that fell into it.
		bucket := int64(1)
		for bucket < usec {
			bucket <<= 1
		}
		buckets[bucket]++
		calls++
	}
	return calls, buckets
}

// EventNames returns the set of recorded event/command names in
// insertion-stable order. Used by LATENCY HISTOGRAM with no args.
func (l *LatencyMonitor) EventNames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.events))
	for name := range l.events {
		out = append(out, name)
	}
	return out
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
