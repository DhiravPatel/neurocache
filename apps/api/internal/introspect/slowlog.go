// Package introspect implements the operational surface RESP exposes
// for monitoring: SLOWLOG, LATENCY, CLIENT registry, OBJECT introspection
// helpers. Each component is a small thread-safe ring buffer / map so
// queries are O(1) and writers can stay on the hot path.
package introspect

import (
	"sync"
	"sync/atomic"
	"time"
)

// SlowLog is a fixed-size ring buffer of slow command executions.
// Writes are O(1) and never block the caller; reads copy the slice.
type SlowLog struct {
	threshold time.Duration
	mu        sync.Mutex
	entries   []SlowEntry
	maxLen    int
	nextID    atomic.Uint64
}

// SlowEntry captures one slow execution.
type SlowEntry struct {
	ID       uint64
	At       time.Time
	Duration time.Duration
	Command  []string
	Client   string
}

// NewSlowLog returns an empty log capped at maxLen entries. Threshold
// of 0 means "log everything", which is mostly useful for debugging.
func NewSlowLog(maxLen int, threshold time.Duration) *SlowLog {
	if maxLen <= 0 {
		maxLen = 128
	}
	return &SlowLog{maxLen: maxLen, threshold: threshold, entries: make([]SlowEntry, 0, maxLen)}
}

// Maybe appends an entry if d exceeds the threshold. Cheap when the
// command isn't slow — one comparison and a return.
func (s *SlowLog) Maybe(d time.Duration, parts []string, client string) {
	if d < s.threshold {
		return
	}
	s.nextID.Add(1)
	e := SlowEntry{ID: s.nextID.Load(), At: time.Now(), Duration: d, Command: parts, Client: client}
	s.mu.Lock()
	if len(s.entries) == s.maxLen {
		s.entries = s.entries[1:]
	}
	s.entries = append(s.entries, e)
	s.mu.Unlock()
}

// Get returns up to count entries (most-recent first). count <= 0
// returns the entire buffer.
func (s *SlowLog) Get(count int) []SlowEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SlowEntry, len(s.entries))
	for i, e := range s.entries {
		out[len(s.entries)-1-i] = e
	}
	if count > 0 && count < len(out) {
		out = out[:count]
	}
	return out
}

// Len returns the current entry count.
func (s *SlowLog) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Reset wipes the buffer.
func (s *SlowLog) Reset() {
	s.mu.Lock()
	s.entries = s.entries[:0]
	s.mu.Unlock()
}

// Threshold reports the current slow threshold (so CONFIG GET can echo).
func (s *SlowLog) Threshold() time.Duration { return s.threshold }

// SetThreshold lets CONFIG SET retune at runtime.
func (s *SlowLog) SetThreshold(d time.Duration) { s.threshold = d }
