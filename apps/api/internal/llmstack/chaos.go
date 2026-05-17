package llmstack

import (
	"errors"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ChaosInjector is the built-in fault-injection primitive. The
// honest motivation: NeuroCache ships 15 phases of governance,
// detection, and incident-response primitives (BLAST kill switches,
// AUTO rules, FORECAST burn alarms, VECSPACE health checks). If
// none of them ever fire in production until the day there's a real
// incident, you don't actually know whether they work — you just
// know they compile.
//
// CHAOS lets you synthesize the failure your detectors are supposed
// to catch, in a controlled window, with optional tenant scoping:
//
//   - "Fail every TRUST.SCORE lookup for the next 30 seconds" — does
//     your AUTO rule that downgrades on low trust actually fire?
//   - "Inject VECSPACE.HEALTH=COLLAPSED for these 60 seconds" —
//     does retrieval back off, does the alert reach oncall?
//   - "Starve agent-7 in MARKET for 10 minutes" — does the fairness
//     floor catch it?
//
// The model: a Fault is a (target, kind, rate, expires_at,
// scope) tuple. Other primitives call `chaos.Affects(target, kind)`
// before doing real work and respect the verdict (return an error,
// override a verdict, drop a result). The engine wires the calls;
// CHAOS only owns the registry.
//
// Commands:
//
//   CHAOS.INJECT fault-id TARGET t KIND k [RATE 0..1] [DURATION ms]
//        [SCOPE k=v[,k=v...]] [REASON r]
//        Active faults live until DURATION elapses. RATE defaults to
//        1.0 (every call). RATE 0.3 means "fail 30% of calls".
//   CHAOS.REVOKE fault-id            — end one early
//   CHAOS.ACTIVE [TARGET t] [KIND k] — list currently-active faults
//   CHAOS.HISTORY [LIMIT n]          — past faults (audit trail)
//   CHAOS.CHECK target kind [scope-k1 v1 ...]
//        → injected (bool), fault_id (if injected), kind, reason
//        This is the primary integration point: any primitive that
//        wants to participate calls CHECK and acts on injected=1.
//   CHAOS.STATS
//
// The hot path: CHECK is O(active-faults) — typically zero or a
// handful, never more than dozens. RATE filtering uses a per-CHECK
// rand draw against a seeded RNG, so the fraction is statistically
// honest but every test run can be deterministic with a fixed seed.
type ChaosInjector struct {
	mu      sync.RWMutex
	faults  map[string]*chaosFault
	history []chaosFault
	rng     *rand.Rand

	totalInjects atomic.Int64
	totalChecks  atomic.Int64
	totalHits    atomic.Int64
	totalRevokes atomic.Int64
}

type chaosFault struct {
	ID        string
	Target    string // primitive name: "trust", "vecspace", "market", "cfcache", ...
	Kind      string // failure flavour: "lookup_fail", "collapsed", "starved", ...
	Rate      float64
	Scope     map[string]string // e.g. {"tenant": "acme", "session": "x"}
	Reason    string
	StartedAt time.Time
	ExpiresAt time.Time // zero means no expiry (manual revoke)
	Revoked   bool
	RevokedAt time.Time
}

// NewChaosInjector returns an empty injector.
func NewChaosInjector() *ChaosInjector {
	return &ChaosInjector{
		faults:  map[string]*chaosFault{},
		history: []chaosFault{},
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Inject registers a new fault. Re-injecting the same fault-id is
// rejected — to override, REVOKE first.
func (c *ChaosInjector) Inject(id, target, kind string, rate float64, duration time.Duration, scope map[string]string, reason string) error {
	if id == "" {
		return errors.New("fault_id required")
	}
	if target == "" {
		return errors.New("target required")
	}
	if kind == "" {
		return errors.New("kind required")
	}
	if rate < 0 || rate > 1 {
		return errors.New("rate must be in [0,1]")
	}
	if rate == 0 {
		rate = 1.0
	}
	if duration < 0 {
		return errors.New("duration must be non-negative")
	}
	c.totalInjects.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.faults[id]; ok {
		return errors.New("fault already active: " + id)
	}
	cp := map[string]string{}
	for k, v := range scope {
		cp[k] = v
	}
	f := &chaosFault{
		ID: id, Target: target, Kind: kind,
		Rate: rate, Scope: cp, Reason: reason,
		StartedAt: time.Now(),
	}
	if duration > 0 {
		f.ExpiresAt = f.StartedAt.Add(duration)
	}
	c.faults[id] = f
	return nil
}

// Revoke ends a fault early. Returns 1 if it was active.
func (c *ChaosInjector) Revoke(id string) int {
	c.totalRevokes.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.faults[id]
	if !ok {
		return 0
	}
	f.Revoked = true
	f.RevokedAt = time.Now()
	c.history = append(c.history, *f)
	if len(c.history) > 1000 {
		c.history = c.history[len(c.history)-1000:]
	}
	delete(c.faults, id)
	return 1
}

// ChaosCheckResult is CHECK's return — the verdict primitives consult.
type ChaosCheckResult struct {
	Injected bool   `json:"injected"`
	FaultID  string `json:"fault_id"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
}

// Check is the integration point. A primitive that wants to be
// affected by chaos asks "am I currently injected with this kind of
// fault, given my scope?" The answer respects:
//
//   - target match
//   - kind match (or empty kind = "any fault on this target")
//   - scope intersection (every scope k=v on the fault must be
//     satisfied by the supplied scope)
//   - expiry (lazy — expired faults are reaped on every CHECK)
//   - rate (RNG draw)
//
// The rate gate is the last check, so explicit kind/scope filters
// always win deterministically before the random coin flip.
func (c *ChaosInjector) Check(target, kind string, scope map[string]string) ChaosCheckResult {
	c.totalChecks.Add(1)
	if target == "" {
		return ChaosCheckResult{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	// Reap expired faults
	for id, f := range c.faults {
		if !f.ExpiresAt.IsZero() && now.After(f.ExpiresAt) {
			c.history = append(c.history, *f)
			delete(c.faults, id)
		}
	}
	// Walk active faults
	for _, f := range c.faults {
		if f.Target != target {
			continue
		}
		if kind != "" && f.Kind != "" && f.Kind != kind {
			continue
		}
		if !scopeMatches(f.Scope, scope) {
			continue
		}
		// Rate gate
		if f.Rate < 1.0 {
			if c.rng.Float64() >= f.Rate {
				continue
			}
		}
		c.totalHits.Add(1)
		return ChaosCheckResult{
			Injected: true, FaultID: f.ID, Kind: f.Kind, Reason: f.Reason,
		}
	}
	return ChaosCheckResult{}
}

// scopeMatches: every key in faultScope must equal the matching key
// in callerScope. Extra keys in callerScope are irrelevant. Empty
// faultScope matches everything.
func scopeMatches(faultScope, callerScope map[string]string) bool {
	for k, v := range faultScope {
		got, ok := callerScope[k]
		if !ok || got != v {
			return false
		}
	}
	return true
}

// ChaosFaultView is one row of ACTIVE / HISTORY.
type ChaosFaultView struct {
	ID          string            `json:"fault_id"`
	Target      string            `json:"target"`
	Kind        string            `json:"kind"`
	Rate        float64           `json:"rate"`
	Scope       map[string]string `json:"scope,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	StartedUnix int64             `json:"started_unix"`
	ExpiresUnix int64             `json:"expires_unix"`
	Revoked     bool              `json:"revoked,omitempty"`
}

// Active lists currently-injected faults.
func (c *ChaosInjector) Active(target, kind string) []ChaosFaultView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ChaosFaultView, 0, len(c.faults))
	for _, f := range c.faults {
		if target != "" && f.Target != target {
			continue
		}
		if kind != "" && f.Kind != kind {
			continue
		}
		out = append(out, viewFault(f))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedUnix > out[j].StartedUnix })
	return out
}

// History returns past faults (reverse chronological).
func (c *ChaosInjector) History(limit int) []ChaosFaultView {
	if limit <= 0 {
		limit = 100
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ChaosFaultView, 0, limit)
	for i := len(c.history) - 1; i >= 0 && len(out) < limit; i-- {
		f := c.history[i]
		out = append(out, viewFault(&f))
	}
	return out
}

// ChaosStats is the global snapshot.
type ChaosStats struct {
	Active       int   `json:"active"`
	History      int   `json:"history"`
	TotalInjects int64 `json:"total_injects"`
	TotalChecks  int64 `json:"total_checks"`
	TotalHits    int64 `json:"total_hits"`
	TotalRevokes int64 `json:"total_revokes"`
}

func (c *ChaosInjector) Stats() ChaosStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ChaosStats{
		Active: len(c.faults), History: len(c.history),
		TotalInjects: c.totalInjects.Load(),
		TotalChecks:  c.totalChecks.Load(),
		TotalHits:    c.totalHits.Load(),
		TotalRevokes: c.totalRevokes.Load(),
	}
}

func viewFault(f *chaosFault) ChaosFaultView {
	v := ChaosFaultView{
		ID: f.ID, Target: f.Target, Kind: f.Kind, Rate: f.Rate,
		Reason: f.Reason,
		StartedUnix: f.StartedAt.Unix(),
		Revoked: f.Revoked,
	}
	if !f.ExpiresAt.IsZero() {
		v.ExpiresUnix = f.ExpiresAt.Unix()
	}
	if len(f.Scope) > 0 {
		v.Scope = map[string]string{}
		for k, vv := range f.Scope {
			v.Scope[k] = vv
		}
	}
	return v
}

// ForSeed lets tests pin the RNG so chaos draws are deterministic.
func (c *ChaosInjector) ForSeed(seed int64) {
	c.mu.Lock()
	c.rng = rand.New(rand.NewSource(seed))
	c.mu.Unlock()
}
