package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FactRegistry stores versioned facts + tracks which cache entries
// were derived from which fact-version. Solves a real production
// failure mode: you update the refund policy from "30 days" to
// "14 days", but every semantically-cached answer derived from the
// old policy keeps serving stale "30 days" answers forever. No
// other cache product ships a primitive for this.
//
// Two-part design:
//
//   1. FACT.* — the registry. A fact has an id, a current version,
//      and current content. Apps bump the version when the
//      underlying fact changes (refund policy revised, pricing
//      changed, etc.).
//
//   2. STAMP — when an app caches an LLM-generated answer derived
//      from a fact, it stamps the cache-key with the fact-id +
//      version it was generated under. A later STALE check returns
//      true if the stamped version != the fact's current version
//      — apps treat stale = miss → regenerate.
//
// Commands:
//
//   FACT.SET fact-id content                 → version=1 on first set
//   FACT.BUMP fact-id new-content            → version++, content swap
//   FACT.GET fact-id                         → [version, content]
//   FACT.STAMP cache-key fact-id [fact-id …] → mark a cache key as
//                                              derived from N facts
//   FACT.STALE cache-key                     → 1 if any stamped fact's
//                                              version drifted, else 0
//   FACT.STALE_KEYS [LIMIT n]                → all known-stale keys
//   FACT.UNSTAMP cache-key                   → drop the stamps
//   FACT.LIST                                → every fact id
//   FACT.FORGET fact-id                      → unregister
//   FACT.STATS
//
// Storage: per-fact atomic version counter; per-stamped-key list of
// (fact_id, stamped_version). STALE is O(stamps-on-this-key) which
// is typically 1-3.
type FactRegistry struct {
	mu    sync.RWMutex
	facts map[string]*factEntry
	stamps map[string]*stampEntry // cache_key -> stamps

	totalSets   atomic.Int64
	totalBumps  atomic.Int64
	totalStamps atomic.Int64
	totalChecks atomic.Int64
	totalStale  atomic.Int64
}

type factEntry struct {
	version   atomic.Int64
	content   string
	updatedAt int64
	mu        sync.RWMutex // protects content
}

type stampEntry struct {
	mu     sync.RWMutex
	stamps []factStamp
}

type factStamp struct {
	factID  string
	version int64
}

// NewFactRegistry returns an empty registry.
func NewFactRegistry() *FactRegistry {
	return &FactRegistry{
		facts:  map[string]*factEntry{},
		stamps: map[string]*stampEntry{},
	}
}

// Set creates a new fact at version 1 OR replaces the content of
// an existing fact at its current version (does NOT bump). Use
// BUMP when the fact's MEANING changed.
func (f *FactRegistry) Set(factID, content string) error {
	if factID == "" {
		return errors.New("fact_id required")
	}
	f.totalSets.Add(1)
	now := time.Now().Unix()
	f.mu.Lock()
	e, ok := f.facts[factID]
	if !ok {
		e = &factEntry{}
		e.version.Store(1)
		f.facts[factID] = e
	}
	f.mu.Unlock()
	e.mu.Lock()
	e.content = content
	e.updatedAt = now
	e.mu.Unlock()
	return nil
}

// Bump increments the version + swaps content atomically. This is
// what invalidates every stamped cache entry derived from this fact.
func (f *FactRegistry) Bump(factID, content string) (int64, error) {
	if factID == "" {
		return 0, errors.New("fact_id required")
	}
	f.totalBumps.Add(1)
	now := time.Now().Unix()
	f.mu.Lock()
	e, ok := f.facts[factID]
	if !ok {
		// BUMP on a new fact creates it at version 1 (no-op increment)
		e = &factEntry{}
		e.version.Store(1)
		f.facts[factID] = e
		f.mu.Unlock()
		e.mu.Lock()
		e.content = content
		e.updatedAt = now
		e.mu.Unlock()
		return 1, nil
	}
	f.mu.Unlock()
	newV := e.version.Add(1)
	e.mu.Lock()
	e.content = content
	e.updatedAt = now
	e.mu.Unlock()
	return newV, nil
}

// FactGetResult is FACT.GET's return.
type FactGetResult struct {
	Version   int64  `json:"version"`
	Content   string `json:"content"`
	UpdatedAt int64  `json:"updated_at_unix"`
}

// Get returns the current version + content of a fact.
func (f *FactRegistry) Get(factID string) (FactGetResult, bool) {
	f.mu.RLock()
	e, ok := f.facts[factID]
	f.mu.RUnlock()
	if !ok {
		return FactGetResult{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return FactGetResult{
		Version:   e.version.Load(),
		Content:   e.content,
		UpdatedAt: e.updatedAt,
	}, true
}

// Stamp records that a cache_key was derived from these facts at
// their current versions. Replaces any previous stamps for the
// same key.
func (f *FactRegistry) Stamp(cacheKey string, factIDs []string) error {
	if cacheKey == "" {
		return errors.New("cache_key required")
	}
	if len(factIDs) == 0 {
		return errors.New("at least one fact_id required")
	}
	f.totalStamps.Add(1)
	stamps := make([]factStamp, 0, len(factIDs))
	f.mu.RLock()
	for _, id := range factIDs {
		e, ok := f.facts[id]
		if !ok {
			f.mu.RUnlock()
			return errors.New("unknown fact_id: " + id)
		}
		stamps = append(stamps, factStamp{factID: id, version: e.version.Load()})
	}
	f.mu.RUnlock()

	f.mu.Lock()
	se, ok := f.stamps[cacheKey]
	if !ok {
		se = &stampEntry{}
		f.stamps[cacheKey] = se
	}
	f.mu.Unlock()
	se.mu.Lock()
	se.stamps = stamps
	se.mu.Unlock()
	return nil
}

// Stale returns true if any of the cache_key's stamped facts has a
// version newer than the stamped one. Unstamped keys return false
// (no opinion → not stale).
func (f *FactRegistry) Stale(cacheKey string) bool {
	f.totalChecks.Add(1)
	f.mu.RLock()
	se, ok := f.stamps[cacheKey]
	f.mu.RUnlock()
	if !ok {
		return false
	}
	se.mu.RLock()
	stamps := se.stamps
	se.mu.RUnlock()
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, s := range stamps {
		fe, ok := f.facts[s.factID]
		if !ok {
			// Fact was forgotten — treat as stale (caller should
			// reconsider whether this cache entry still applies)
			f.totalStale.Add(1)
			return true
		}
		if fe.version.Load() > s.version {
			f.totalStale.Add(1)
			return true
		}
	}
	return false
}

// Unstamp drops the stamps for a cache key (e.g. after the app
// evicted the cache entry).
func (f *FactRegistry) Unstamp(cacheKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.stamps[cacheKey]
	delete(f.stamps, cacheKey)
	return ok
}

// StaleKeys returns every stamped cache_key whose stamped version
// drifted from the current. Optionally limit the result count.
func (f *FactRegistry) StaleKeys(limit int) []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0)
	for k, se := range f.stamps {
		se.mu.RLock()
		stamps := se.stamps
		se.mu.RUnlock()
		stale := false
		for _, s := range stamps {
			fe, ok := f.facts[s.factID]
			if !ok || fe.version.Load() > s.version {
				stale = true
				break
			}
		}
		if stale {
			out = append(out, k)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// List returns every registered fact, sorted by id.
type FactRow struct {
	FactID    string `json:"fact_id"`
	Version   int64  `json:"version"`
	UpdatedAt int64  `json:"updated_at_unix"`
}

func (f *FactRegistry) List() []FactRow {
	f.mu.RLock()
	out := make([]FactRow, 0, len(f.facts))
	for id, e := range f.facts {
		e.mu.RLock()
		out = append(out, FactRow{
			FactID: id, Version: e.version.Load(), UpdatedAt: e.updatedAt,
		})
		e.mu.RUnlock()
	}
	f.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].FactID < out[j].FactID })
	return out
}

// Forget unregisters a fact. Stamped cache keys still see "stale"
// on next check (because the fact no longer exists, the stamp
// can't be validated). Returns true if it existed.
func (f *FactRegistry) Forget(factID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.facts[factID]
	delete(f.facts, factID)
	return ok
}

// FactStats is the global snapshot.
type FactStats struct {
	Facts        int   `json:"facts"`
	StampedKeys  int   `json:"stamped_keys"`
	TotalSets    int64 `json:"total_sets"`
	TotalBumps   int64 `json:"total_bumps"`
	TotalStamps  int64 `json:"total_stamps"`
	TotalChecks  int64 `json:"total_checks"`
	TotalStale   int64 `json:"total_stale_detected"`
}

func (f *FactRegistry) Stats() FactStats {
	f.mu.RLock()
	nf := len(f.facts)
	ns := len(f.stamps)
	f.mu.RUnlock()
	return FactStats{
		Facts:       nf,
		StampedKeys: ns,
		TotalSets:   f.totalSets.Load(),
		TotalBumps:  f.totalBumps.Load(),
		TotalStamps: f.totalStamps.Load(),
		TotalChecks: f.totalChecks.Load(),
		TotalStale:  f.totalStale.Load(),
	}
}
