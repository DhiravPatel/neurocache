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

// ReproSeeds is the deterministic seed bundle primitive. Complements
// REPLAY / SANDBOX. The problem: any non-determinism in the run
// (BANDIT random draw, MARKET tie-break, sampling temperature)
// breaks bit-reproduction, so debugging "why did this run answer
// differently the second time?" becomes a hunt.
//
// REPRO is the registry of named seed bundles. A bundle pins every
// stochastic decision in a run:
//
//   BUNDLE create the bundle, set N named seeds inside
//   USE consume one seed by name (deterministic — returns the
//       same value for the same name within a bundle)
//   REPLAY-MODE: USE with the same bundle id replays the same draws
//   VERIFY: a run's seed-trace produces the same content hash as the
//          original
//
// Commands:
//
//   REPRO.BUNDLE bundle-id [SEED u64] [META k v ...]
//   REPRO.USE bundle-id name
//        → 64-bit seed value (deterministic per bundle+name)
//   REPRO.TRACE bundle-id     — list every USE so far
//   REPRO.HASH bundle-id      → content hash of the trace
//   REPRO.LIST [LIMIT n]
//   REPRO.GET bundle-id
//   REPRO.FORGET bundle-id|ALL
//   REPRO.STATS
type ReproSeeds struct {
	mu      sync.RWMutex
	bundles map[string]*reproBundle

	totalBundles atomic.Int64
	totalUses    atomic.Int64
}

type reproBundle struct {
	mu       sync.Mutex
	id       string
	rootSeed uint64
	uses     []reproUse
	cache    map[string]uint64
	meta     map[string]string
	createdAt time.Time
}

type reproUse struct {
	Name  string
	Value uint64
	At    time.Time
}

// NewReproSeeds returns an empty registry.
func NewReproSeeds() *ReproSeeds {
	return &ReproSeeds{bundles: map[string]*reproBundle{}}
}

// Bundle creates a new bundle. seed=0 → use current nanos.
func (r *ReproSeeds) Bundle(id string, seed uint64, meta map[string]string) error {
	if id == "" {
		return errors.New("bundle_id required")
	}
	r.totalBundles.Add(1)
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	cp := map[string]string{}
	for k, v := range meta {
		cp[k] = v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bundles[id]; ok {
		return errors.New("bundle already exists: " + id)
	}
	r.bundles[id] = &reproBundle{
		id: id, rootSeed: seed,
		cache: map[string]uint64{}, meta: cp,
		createdAt: time.Now(),
	}
	return nil
}

// Use returns a deterministic 64-bit seed for (bundle, name). The
// same (bundle, name) always returns the same value — that's the
// reproducibility guarantee.
func (r *ReproSeeds) Use(bundleID, name string) (uint64, error) {
	if bundleID == "" || name == "" {
		return 0, errors.New("bundle_id and name required")
	}
	r.totalUses.Add(1)
	r.mu.RLock()
	b, ok := r.bundles[bundleID]
	r.mu.RUnlock()
	if !ok {
		return 0, errors.New("unknown bundle: " + bundleID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.cache[name]; ok {
		return v, nil
	}
	// Derive: SHA256(root || name) → take first 8 bytes as uint64
	h := sha256.New()
	var rootBytes [8]byte
	for i := 0; i < 8; i++ {
		rootBytes[i] = byte(b.rootSeed >> (8 * i))
	}
	h.Write(rootBytes[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	var v uint64
	for i := 0; i < 8; i++ {
		v |= uint64(sum[i]) << (8 * i)
	}
	b.cache[name] = v
	b.uses = append(b.uses, reproUse{Name: name, Value: v, At: time.Now()})
	return v, nil
}

// ReproTraceRow is one entry in TRACE.
type ReproTraceRow struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
	AtUnix int64 `json:"at_unix"`
}

// Trace returns every USE in insertion order.
func (r *ReproSeeds) Trace(bundleID string) ([]ReproTraceRow, bool) {
	r.mu.RLock()
	b, ok := r.bundles[bundleID]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ReproTraceRow, len(b.uses))
	for i, u := range b.uses {
		out[i] = ReproTraceRow{Name: u.Name, Value: u.Value, AtUnix: u.At.Unix()}
	}
	return out, true
}

// Hash returns a content hash over the (name, value) pairs in trace
// order — a single hex blob the caller can post alongside the run's
// answer to prove the same seed-trace produced it.
func (r *ReproSeeds) Hash(bundleID string) (string, bool) {
	rows, ok := r.Trace(bundleID)
	if !ok {
		return "", false
	}
	h := sha256.New()
	for _, row := range rows {
		h.Write([]byte(row.Name))
		var vb [8]byte
		v := row.Value
		for i := 0; i < 8; i++ {
			vb[i] = byte(v >> (8 * i))
		}
		h.Write(vb[:])
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// ReproView is GET's return.
type ReproView struct {
	BundleID    string            `json:"bundle_id"`
	RootSeed    uint64            `json:"root_seed"`
	Uses        int               `json:"uses"`
	Meta        map[string]string `json:"meta,omitempty"`
	CreatedUnix int64             `json:"created_unix"`
}

// Get returns one bundle's metadata.
func (r *ReproSeeds) Get(bundleID string) (ReproView, bool) {
	r.mu.RLock()
	b, ok := r.bundles[bundleID]
	r.mu.RUnlock()
	if !ok {
		return ReproView{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v := ReproView{
		BundleID: b.id, RootSeed: b.rootSeed,
		Uses: len(b.uses), CreatedUnix: b.createdAt.Unix(),
	}
	if len(b.meta) > 0 {
		v.Meta = map[string]string{}
		for k, vv := range b.meta {
			v.Meta[k] = vv
		}
	}
	return v, true
}

// ReproListRow is one row of LIST.
type ReproListRow struct {
	BundleID    string `json:"bundle_id"`
	Uses        int    `json:"uses"`
	CreatedUnix int64  `json:"created_unix"`
}

// List returns recent bundles.
func (r *ReproSeeds) List(limit int) []ReproListRow {
	if limit <= 0 {
		limit = 50
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ReproListRow, 0, len(r.bundles))
	for _, b := range r.bundles {
		b.mu.Lock()
		out = append(out, ReproListRow{
			BundleID: b.id, Uses: len(b.uses), CreatedUnix: b.createdAt.Unix(),
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
func (r *ReproSeeds) Forget(bundleID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if bundleID == "ALL" {
		n := len(r.bundles)
		r.bundles = map[string]*reproBundle{}
		return n
	}
	if _, ok := r.bundles[bundleID]; ok {
		delete(r.bundles, bundleID)
		return 1
	}
	return 0
}

// ReproStats is the global snapshot.
type ReproStats struct {
	Bundles      int   `json:"bundles"`
	TotalBundles int64 `json:"total_bundles"`
	TotalUses    int64 `json:"total_uses"`
}

func (r *ReproSeeds) Stats() ReproStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return ReproStats{
		Bundles: len(r.bundles),
		TotalBundles: r.totalBundles.Load(),
		TotalUses: r.totalUses.Load(),
	}
}
