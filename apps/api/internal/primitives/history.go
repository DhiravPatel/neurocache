package primitives

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// HistoryStore captures every write to a versioned key so callers can
// time-travel through past values. Operators opt in per-key via
// KEY.TRACK; once tracked, every SET / INCR / HSET / etc. that
// touches the key snapshots the previous value alongside its
// timestamp. KEY.AT lets a reader query the as-of value at any
// instant in the retained window.
//
// Use cases this primitive really is good at:
//   - audit trails ("what was this user's tier at the moment they
//     hit our service?")
//   - debugging ("show me the value of feature_flag:checkout right
//     before the incident")
//   - undo ("restore yesterday's config")
//
// Storage: per-key bounded ring of (ts, value) pairs. The ring caps
// at MaxVersions to keep memory predictable; older entries roll off.
type HistoryStore struct {
	mu       sync.RWMutex
	tracked  map[string]bool
	versions map[string][]Version
	maxVers  int
	maxAge   time.Duration

	// trackedCount mirrors len(tracked) so the engine notifier can
	// short-circuit IsTracked() on every write without taking mu.
	// Zero in the steady-state (nobody opted any key into KEY.TRACK).
	trackedCount atomic.Int64
}

// Version is one snapshot.
type Version struct {
	At    time.Time
	Value string
}

// NewHistoryStore returns an empty store with the supplied retention
// limits. maxVers ≤ 0 keeps everything; maxAge ≤ 0 disables age-based
// pruning.
func NewHistoryStore(maxVers int, maxAge time.Duration) *HistoryStore {
	return &HistoryStore{
		tracked:  map[string]bool{},
		versions: map[string][]Version{},
		maxVers:  maxVers,
		maxAge:   maxAge,
	}
}

// Track opts a key into versioning. Idempotent.
func (h *HistoryStore) Track(key string) {
	h.mu.Lock()
	if !h.tracked[key] {
		h.tracked[key] = true
		h.trackedCount.Add(1)
	}
	h.mu.Unlock()
}

// Untrack removes a key from versioning + drops its history.
func (h *HistoryStore) Untrack(key string) {
	h.mu.Lock()
	if h.tracked[key] {
		h.trackedCount.Add(-1)
	}
	delete(h.tracked, key)
	delete(h.versions, key)
	h.mu.Unlock()
}

// HasAny reports whether any key has been opted into versioning. Lock-
// free; called from the engine notifier on every write to skip the
// per-write IsTracked RWLock when nobody's tracking anything.
func (h *HistoryStore) HasAny() bool { return h.trackedCount.Load() > 0 }

// IsTracked reports whether the engine should snapshot writes to key.
func (h *HistoryStore) IsTracked(key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.tracked[key]
}

// Snapshot pushes a new version. Caller invokes this from the
// keyspace notifier after every write that touched a tracked key.
func (h *HistoryStore) Snapshot(key, value string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.tracked[key] {
		return
	}
	v := Version{At: time.Now(), Value: value}
	h.versions[key] = append(h.versions[key], v)
	if h.maxVers > 0 && len(h.versions[key]) > h.maxVers {
		h.versions[key] = h.versions[key][len(h.versions[key])-h.maxVers:]
	}
	if h.maxAge > 0 {
		cutoff := time.Now().Add(-h.maxAge)
		for i, ver := range h.versions[key] {
			if ver.At.After(cutoff) {
				h.versions[key] = h.versions[key][i:]
				break
			}
		}
	}
}

// At returns the value that was current at instant `t` (or "", false
// if no version exists at-or-before t). Binary search keeps it O(log n).
func (h *HistoryStore) At(key string, t time.Time) (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	versions := h.versions[key]
	if len(versions) == 0 {
		return "", false
	}
	idx := sort.Search(len(versions), func(i int) bool { return versions[i].At.After(t) })
	if idx == 0 {
		return "", false
	}
	return versions[idx-1].Value, true
}

// History returns every retained version, oldest-first.
func (h *HistoryStore) History(key string, max int) []Version {
	h.mu.RLock()
	defer h.mu.RUnlock()
	versions := h.versions[key]
	if max > 0 && len(versions) > max {
		versions = versions[len(versions)-max:]
	}
	out := make([]Version, len(versions))
	copy(out, versions)
	return out
}
