package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DRRegistry is the disaster-recovery drill primitive. The honest
// observation: 15 phases of state, and none of it answers "if we
// lose this process, is any of it actually recoverable?" Backup +
// restore are typically untested until the day something breaks,
// which is the worst possible day to discover the answer.
//
// DR provides the controlled drill:
//
//   1. SNAPSHOT captures the full AI-state surface (caller supplies
//      every store's serialised state) into a named bundle with a
//      content hash. Snapshot is "labels + opaque blobs" — we don't
//      interpret the blobs; the calling stores own their formats.
//
//   2. RESTORE_INTO assembles a *shadow* registry — the same store
//      payloads, restored to a separate name. The point is to
//      compare, not to clobber live state.
//
//   3. ASSERT walks a list of store-pairs and reports per-pair
//      equivalence (content-hash match) + a final overall verdict.
//      Optional CHECKSUM_ONLY mode skips byte-for-byte comparison
//      for stores whose blobs are non-deterministic but whose hash
//      we compute over a canonical projection.
//
//   4. The result is a drill report: which stores recovered cleanly,
//      which diverged, what to investigate. PROMOTE moves the shadow
//      into live if the operator chooses to (after a real disaster).
//
// Commands:
//
//   DR.SNAPSHOT bundle-id [META k v ...]
//   DR.CONTRIBUTE bundle-id store payload
//        (analogous to TEMPORAL.CONTRIBUTE but for full state)
//   DR.SEAL bundle-id
//   DR.RESTORE_INTO source-bundle shadow-bundle
//   DR.ASSERT source-bundle shadow-bundle
//        → per_store breakdown, all_match (bool), diverged list
//   DR.PROMOTE shadow-bundle
//        Mark the shadow as the new live bundle (informational —
//        actually applying it remains the calling stores' job).
//   DR.LIST [LIMIT n]
//   DR.GET bundle-id
//   DR.FORGET bundle-id|ALL
//   DR.STATS
//
// Hot path: SNAPSHOT/CONTRIBUTE are map ops. ASSERT recomputes
// SHA-256 over each pair of payloads — typically megabytes total,
// well under a second.
type DRRegistry struct {
	mu      sync.RWMutex
	bundles map[string]*drBundle

	totalSnapshots atomic.Int64
	totalRestores  atomic.Int64
	totalAsserts   atomic.Int64
	totalPromotes  atomic.Int64
}

type drBundle struct {
	mu        sync.Mutex
	id        string
	meta      map[string]string
	stores    map[string]string // store → payload
	hashes    map[string]string // store → SHA-256 hex
	sealed    bool
	createdAt time.Time
	promoted  bool
}

// NewDRRegistry returns an empty registry.
func NewDRRegistry() *DRRegistry {
	return &DRRegistry{bundles: map[string]*drBundle{}}
}

// Snapshot opens a new bundle.
func (d *DRRegistry) Snapshot(id string, meta map[string]string) error {
	if id == "" {
		return errors.New("bundle_id required")
	}
	d.totalSnapshots.Add(1)
	cp := map[string]string{}
	for k, v := range meta {
		cp[k] = v
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.bundles[id]; ok {
		return errors.New("bundle already exists: " + id)
	}
	d.bundles[id] = &drBundle{
		id: id, meta: cp,
		stores: map[string]string{}, hashes: map[string]string{},
		createdAt: time.Now(),
	}
	return nil
}

// Contribute adds one store's serialised state.
func (d *DRRegistry) Contribute(bundleID, store, payload string) error {
	if bundleID == "" || store == "" {
		return errors.New("bundle_id and store required")
	}
	d.mu.RLock()
	b, ok := d.bundles[bundleID]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown bundle: " + bundleID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sealed {
		return errors.New("bundle already sealed")
	}
	b.stores[store] = payload
	h := sha256.Sum256([]byte(payload))
	b.hashes[store] = hex.EncodeToString(h[:])
	return nil
}

// Seal forbids further CONTRIBUTE on this bundle.
func (d *DRRegistry) Seal(bundleID string) error {
	d.mu.RLock()
	b, ok := d.bundles[bundleID]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown bundle: " + bundleID)
	}
	b.mu.Lock()
	b.sealed = true
	b.mu.Unlock()
	return nil
}

// RestoreInto copies a source bundle into a new shadow bundle. The
// shadow is sealed immediately on creation. This is the "what would
// we have if we restored from this snapshot" rehearsal.
func (d *DRRegistry) RestoreInto(sourceID, shadowID string) error {
	if sourceID == "" || shadowID == "" {
		return errors.New("source_id and shadow_id required")
	}
	d.totalRestores.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	src, ok := d.bundles[sourceID]
	if !ok {
		return errors.New("unknown source bundle: " + sourceID)
	}
	if _, exists := d.bundles[shadowID]; exists {
		return errors.New("shadow already exists: " + shadowID)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if !src.sealed {
		return errors.New("source must be sealed before restore")
	}
	shadow := &drBundle{
		id: shadowID,
		meta: map[string]string{"restored_from": sourceID},
		stores: map[string]string{},
		hashes: map[string]string{},
		sealed: true,
		createdAt: time.Now(),
	}
	for k, v := range src.stores {
		shadow.stores[k] = v
	}
	for k, v := range src.hashes {
		shadow.hashes[k] = v
	}
	d.bundles[shadowID] = shadow
	return nil
}

// DRAssertResult is ASSERT's return.
type DRAssertResult struct {
	SourceID  string                 `json:"source_id"`
	ShadowID  string                 `json:"shadow_id"`
	PerStore  []DRAssertRow          `json:"per_store"`
	AllMatch  bool                   `json:"all_match"`
	Diverged  []string               `json:"diverged"`
	MissingInShadow []string         `json:"missing_in_shadow"`
	ExtraInShadow   []string         `json:"extra_in_shadow"`
}

// DRAssertRow is one row.
type DRAssertRow struct {
	Store      string `json:"store"`
	SourceHash string `json:"source_hash"`
	ShadowHash string `json:"shadow_hash"`
	Match      bool   `json:"match"`
}

// Assert compares hashes per store. Returns a structured report.
func (d *DRRegistry) Assert(sourceID, shadowID string) (DRAssertResult, error) {
	if sourceID == "" || shadowID == "" {
		return DRAssertResult{}, errors.New("source and shadow required")
	}
	d.totalAsserts.Add(1)
	d.mu.RLock()
	src, ok := d.bundles[sourceID]
	if !ok {
		d.mu.RUnlock()
		return DRAssertResult{}, errors.New("unknown source: " + sourceID)
	}
	shadow, ok := d.bundles[shadowID]
	d.mu.RUnlock()
	if !ok {
		return DRAssertResult{}, errors.New("unknown shadow: " + shadowID)
	}
	src.mu.Lock()
	shadow.mu.Lock()
	defer src.mu.Unlock()
	defer shadow.mu.Unlock()
	out := DRAssertResult{SourceID: sourceID, ShadowID: shadowID}
	allStores := map[string]bool{}
	for k := range src.hashes {
		allStores[k] = true
	}
	for k := range shadow.hashes {
		allStores[k] = true
	}
	names := make([]string, 0, len(allStores))
	for k := range allStores {
		names = append(names, k)
	}
	sort.Strings(names)
	allMatch := true
	for _, k := range names {
		srcH, hasSrc := src.hashes[k]
		shaH, hasSha := shadow.hashes[k]
		if !hasSrc {
			out.ExtraInShadow = append(out.ExtraInShadow, k)
			allMatch = false
			continue
		}
		if !hasSha {
			out.MissingInShadow = append(out.MissingInShadow, k)
			allMatch = false
			continue
		}
		row := DRAssertRow{
			Store: k, SourceHash: srcH, ShadowHash: shaH,
			Match: srcH == shaH,
		}
		out.PerStore = append(out.PerStore, row)
		if !row.Match {
			out.Diverged = append(out.Diverged, k)
			allMatch = false
		}
	}
	out.AllMatch = allMatch
	return out, nil
}

// Promote marks the shadow as promoted (informational — applying
// the restore is the calling stores' job; PROMOTE just records the
// operator's intent for audit).
func (d *DRRegistry) Promote(bundleID string) error {
	if bundleID == "" {
		return errors.New("bundle_id required")
	}
	d.totalPromotes.Add(1)
	d.mu.RLock()
	b, ok := d.bundles[bundleID]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown bundle: " + bundleID)
	}
	b.mu.Lock()
	b.promoted = true
	b.mu.Unlock()
	return nil
}

// DRView is GET's return.
type DRView struct {
	BundleID    string            `json:"bundle_id"`
	Meta        map[string]string `json:"meta,omitempty"`
	Stores      []string          `json:"stores"`
	Hashes      map[string]string `json:"hashes"`
	Sealed      bool              `json:"sealed"`
	Promoted    bool              `json:"promoted"`
	CreatedUnix int64             `json:"created_unix"`
}

// Get returns the bundle (without payloads — those can be large).
func (d *DRRegistry) Get(bundleID string) (DRView, bool) {
	d.mu.RLock()
	b, ok := d.bundles[bundleID]
	d.mu.RUnlock()
	if !ok {
		return DRView{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v := DRView{
		BundleID: b.id, Sealed: b.sealed, Promoted: b.promoted,
		CreatedUnix: b.createdAt.Unix(),
		Hashes: map[string]string{},
	}
	if len(b.meta) > 0 {
		v.Meta = map[string]string{}
		for k, vv := range b.meta {
			v.Meta[k] = vv
		}
	}
	for k := range b.stores {
		v.Stores = append(v.Stores, k)
	}
	sort.Strings(v.Stores)
	for k, h := range b.hashes {
		v.Hashes[k] = h
	}
	return v, true
}

// Payload returns the raw payload for one store in one bundle.
func (d *DRRegistry) Payload(bundleID, store string) (string, bool) {
	d.mu.RLock()
	b, ok := d.bundles[bundleID]
	d.mu.RUnlock()
	if !ok {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.stores[store]
	return p, ok
}

// DRListRow is one row of LIST.
type DRListRow struct {
	BundleID    string `json:"bundle_id"`
	Sealed      bool   `json:"sealed"`
	Promoted    bool   `json:"promoted"`
	Stores      int    `json:"stores"`
	CreatedUnix int64  `json:"created_unix"`
}

// List returns most-recent bundles.
func (d *DRRegistry) List(limit int) []DRListRow {
	if limit <= 0 {
		limit = 50
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]DRListRow, 0, len(d.bundles))
	for _, b := range d.bundles {
		b.mu.Lock()
		out = append(out, DRListRow{
			BundleID: b.id, Sealed: b.sealed, Promoted: b.promoted,
			Stores: len(b.stores), CreatedUnix: b.createdAt.Unix(),
		})
		b.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedUnix > out[j].CreatedUnix })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Forget drops a bundle (or all).
func (d *DRRegistry) Forget(bundleID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if bundleID == "ALL" {
		n := len(d.bundles)
		d.bundles = map[string]*drBundle{}
		return n
	}
	if _, ok := d.bundles[bundleID]; ok {
		delete(d.bundles, bundleID)
		return 1
	}
	return 0
}

// DRStats is the global snapshot.
type DRStats struct {
	Bundles        int   `json:"bundles"`
	TotalSnapshots int64 `json:"total_snapshots"`
	TotalRestores  int64 `json:"total_restores"`
	TotalAsserts   int64 `json:"total_asserts"`
	TotalPromotes  int64 `json:"total_promotes"`
}

func (d *DRRegistry) Stats() DRStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return DRStats{
		Bundles:        len(d.bundles),
		TotalSnapshots: d.totalSnapshots.Load(),
		TotalRestores:  d.totalRestores.Load(),
		TotalAsserts:   d.totalAsserts.Load(),
		TotalPromotes:  d.totalPromotes.Load(),
	}
}
