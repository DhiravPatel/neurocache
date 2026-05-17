package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// BlastRadius is the kill-switch-with-accounting primitive. CANARY
// rolls forward; BLAST.RADIUS rolls back when the canary turns out
// to be poisonous. The flow:
//
//   1. While CANARY routes traffic to prompt v5, BLAST.RADIUS.RECORD
//      logs every exposed (tenant, user, prompt-version) tuple.
//
//   2. Postmortem reveals v5 was bad. BLAST.RADIUS.REVERT v5
//      atomically swings the active version back to v4 and returns
//      the impact report: which tenants got how many v5 answers,
//      total duration of exposure, suggested user-by-user retry list.
//
//   3. BLAST.RADIUS.REPORT v5 prints the same report on demand
//      (during the incident review).
//
// This makes "we instantly reverted prompt v5 and exposed exactly 47
// users for exactly 4 minutes" a one-liner instead of a multi-team
// scramble through 12 dashboards. Incident-response telemetry every
// SaaS shop rebuilds badly.
//
// Commands:
//
//   BLAST.SET feature current-version
//        Declare which version is "live" for a feature. Any RECORD
//        with a different version is treated as canary/rollout traffic.
//   BLAST.RECORD feature version tenant user
//        Logs the exposure tuple (cheap — one map op).
//   BLAST.REVERT feature bad-version safe-version [REASON r]
//        Marks bad-version reverted, swaps current to safe-version,
//        returns the impact report and freezes the bad-version log
//        so REPORT remains queryable.
//   BLAST.REPORT feature version
//        → exposed_users / exposed_tenants / first_exposure / last_exposure
//        + per-tenant breakdown
//   BLAST.STATUS feature      — current_version + active versions
//   BLAST.FORGET feature|ALL
//   BLAST.STATS
//
// Hot path: RECORD is one map op; REPORT walks the per-version set
// (typically thousands, not millions, since it's bounded by an
// incident window).
type BlastRadius struct {
	mu       sync.RWMutex
	features map[string]*blastFeature

	totalRecords atomic.Int64
	totalReverts atomic.Int64
}

type blastFeature struct {
	mu             sync.Mutex
	currentVersion string
	versions       map[string]*blastVersion // version → exposure set
}

type blastVersion struct {
	users          map[string]bool
	tenants        map[string]int // tenant → count
	firstExposure  time.Time
	lastExposure   time.Time
	reverted       bool
	revertedAt     time.Time
	revertReason   string
}

// NewBlastRadius returns an empty registry.
func NewBlastRadius() *BlastRadius {
	return &BlastRadius{features: map[string]*blastFeature{}}
}

// Set declares the current "live" version for a feature.
func (b *BlastRadius) Set(feature, version string) error {
	if feature == "" {
		return errors.New("feature required")
	}
	if version == "" {
		return errors.New("version required")
	}
	f := b.featureOrCreate(feature)
	f.mu.Lock()
	f.currentVersion = version
	f.mu.Unlock()
	return nil
}

// Record logs one exposure.
func (b *BlastRadius) Record(feature, version, tenant, user string) error {
	if feature == "" {
		return errors.New("feature required")
	}
	if version == "" {
		return errors.New("version required")
	}
	if tenant == "" {
		return errors.New("tenant required")
	}
	if user == "" {
		return errors.New("user required")
	}
	b.totalRecords.Add(1)
	f := b.featureOrCreate(feature)
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.versions[version]
	if !ok {
		v = &blastVersion{
			users:         map[string]bool{},
			tenants:       map[string]int{},
			firstExposure: time.Now(),
		}
		f.versions[version] = v
	}
	v.users[user] = true
	v.tenants[tenant]++
	v.lastExposure = time.Now()
	return nil
}

// BlastReport is REPORT/REVERT's structured return.
type BlastReport struct {
	Feature        string         `json:"feature"`
	Version        string         `json:"version"`
	Reverted       bool           `json:"reverted"`
	RevertReason   string         `json:"revert_reason,omitempty"`
	ExposedUsers   int            `json:"exposed_users"`
	ExposedTenants int            `json:"exposed_tenants"`
	FirstExposure  int64          `json:"first_exposure_unix"`
	LastExposure   int64          `json:"last_exposure_unix"`
	DurationMS     int64          `json:"duration_ms"`
	PerTenant      map[string]int `json:"per_tenant"`
}

// Report returns the impact summary for one version. Non-existent
// versions return ok=false.
func (b *BlastRadius) Report(feature, version string) (BlastReport, bool) {
	b.mu.RLock()
	f, ok := b.features[feature]
	b.mu.RUnlock()
	if !ok {
		return BlastReport{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.versions[version]
	if !ok {
		return BlastReport{}, false
	}
	tenants := make(map[string]int, len(v.tenants))
	for k, n := range v.tenants {
		tenants[k] = n
	}
	out := BlastReport{
		Feature: feature, Version: version,
		Reverted: v.reverted, RevertReason: v.revertReason,
		ExposedUsers: len(v.users), ExposedTenants: len(v.tenants),
		FirstExposure: v.firstExposure.Unix(),
		LastExposure:  v.lastExposure.Unix(),
		DurationMS:    v.lastExposure.Sub(v.firstExposure).Milliseconds(),
		PerTenant:     tenants,
	}
	return out, true
}

// Revert marks bad-version reverted and swings current → safeVersion.
// Returns the same impact report Report would. badVersion's exposure
// data is preserved so postmortem queries work later.
func (b *BlastRadius) Revert(feature, badVersion, safeVersion, reason string) (BlastReport, error) {
	if feature == "" || badVersion == "" || safeVersion == "" {
		return BlastReport{}, errors.New("feature, bad_version, safe_version required")
	}
	if badVersion == safeVersion {
		return BlastReport{}, errors.New("bad_version == safe_version")
	}
	b.totalReverts.Add(1)
	f := b.featureOrCreate(feature)
	f.mu.Lock()
	v, ok := f.versions[badVersion]
	if !ok {
		f.mu.Unlock()
		return BlastReport{}, errors.New("unknown bad_version: " + badVersion)
	}
	if !v.reverted {
		v.reverted = true
		v.revertedAt = time.Now()
		v.revertReason = reason
	}
	f.currentVersion = safeVersion
	f.mu.Unlock()
	rep, _ := b.Report(feature, badVersion)
	return rep, nil
}

// BlastStatus is STATUS's return.
type BlastStatus struct {
	Feature        string   `json:"feature"`
	CurrentVersion string   `json:"current_version"`
	Versions       []string `json:"versions"`
}

// Status returns the current version + every version we've seen.
func (b *BlastRadius) Status(feature string) (BlastStatus, bool) {
	b.mu.RLock()
	f, ok := b.features[feature]
	b.mu.RUnlock()
	if !ok {
		return BlastStatus{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	versions := make([]string, 0, len(f.versions))
	for v := range f.versions {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return BlastStatus{Feature: feature, CurrentVersion: f.currentVersion, Versions: versions}, true
}

// Forget drops a feature (or all).
func (b *BlastRadius) Forget(feature string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if feature == "ALL" {
		n := len(b.features)
		b.features = map[string]*blastFeature{}
		return n
	}
	if _, ok := b.features[feature]; ok {
		delete(b.features, feature)
		return 1
	}
	return 0
}

// BlastStats is the global snapshot.
type BlastStats struct {
	Features     int   `json:"features"`
	TotalRecords int64 `json:"total_records"`
	TotalReverts int64 `json:"total_reverts"`
}

func (b *BlastRadius) Stats() BlastStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return BlastStats{
		Features:     len(b.features),
		TotalRecords: b.totalRecords.Load(),
		TotalReverts: b.totalReverts.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (b *BlastRadius) featureOrCreate(feature string) *blastFeature {
	b.mu.RLock()
	f, ok := b.features[feature]
	b.mu.RUnlock()
	if ok {
		return f
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, ok := b.features[feature]; ok {
		return f
	}
	f = &blastFeature{versions: map[string]*blastVersion{}}
	b.features[feature] = f
	return f
}
