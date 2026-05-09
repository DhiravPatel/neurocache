package llmstack

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// LLMRouter is a lock-free provider-failover ladder. Apps configure
// an ordered list of providers per route ("chat-fast", "embed-cheap",
// etc.); when one fails, Next() returns the first healthy one in the
// ladder. Health flips are atomic — both Mark and Next read/write
// `healthy` without taking the route's mutex.
//
// Why this exists: every production LLM app builds this in
// application code. Doing it at the cache layer means:
//
//   - One source of truth across worker processes — when the engine
//     marks OpenAI down, every worker sees it instantly via atomic
//     load (no per-worker stale state)
//   - Atomic counters per provider for "how often did we fall back?"
//     surfaced via LLM.ROUTE.STATS for the dashboard
//   - Auto-recovery: a Mark(provider, true) anywhere flips the bit
//     and the next Next() picks it up — useful for circuit-breaker
//     style tests that probe the upstream every N seconds
//
// Hot path:
//   Next: sync.Map.Load(routeName) → walk providers, atomic.Load
//         on each `healthy` flag → return first true. Zero allocations.
//   Mark: sync.Map.Load(provider key) → atomic.Store on `healthy`.
//
// Sub-microsecond per call. Safe to wrap every LLM request.
type LLMRouter struct {
	routes    sync.Map // route name → *route
	providers sync.Map // provider name → *provider (cross-route shared)

	// Process-wide observability
	totalNexts     atomic.Int64
	totalFailovers atomic.Int64 // count where Next had to skip a downed provider
}

// route is the ordered ladder for one logical purpose.
type route struct {
	name      string
	providers []*provider // ordered from preferred to fallback
	mu        sync.RWMutex // guards `providers` slice replacement; reads are bare loads

	picks    atomic.Int64
	rotations atomic.Int64
}

// provider is the per-provider health + counter state. Shared across
// every route that includes this provider (a name is global so that
// "OpenAI is down" propagates to all routes naming it).
type provider struct {
	name    string
	healthy atomic.Bool // true == ok to route to

	// Optional last-checked timestamp for circuit-breaker patterns.
	lastMarkNS atomic.Int64

	// Counters surfaced via LLM.ROUTE.STATS.
	picks   atomic.Int64
	skips   atomic.Int64 // times Next skipped this provider because !healthy
}

// NewLLMRouter returns an empty router.
func NewLLMRouter() *LLMRouter { return &LLMRouter{} }

// SetRoute defines (or replaces) a route. Providers are listed in
// preferred-to-fallback order. Each provider name is shared across
// routes — calling MarkDown for "openai" flips the bit for every
// route that references it.
func (r *LLMRouter) SetRoute(name string, providerNames []string) {
	provs := make([]*provider, 0, len(providerNames))
	for _, p := range providerNames {
		v, loaded := r.providers.LoadOrStore(p, &provider{name: p})
		pp := v.(*provider)
		// Newly-registered providers default to healthy. Existing
		// providers (loaded == true) preserve their current health
		// state — operators routinely redefine routes after marking
		// providers down for testing, and we shouldn't undo that.
		if !loaded {
			pp.healthy.Store(true)
			pp.lastMarkNS.Store(time.Now().UnixNano())
		}
		provs = append(provs, pp)
	}
	rt := &route{name: name, providers: provs}
	r.routes.Store(name, rt)
}

// Next returns the name of the first healthy provider in the named
// route, or ("", ErrNoHealthyProvider) if every provider is down.
//
// Zero-allocation hot path: bare slice read + atomic.Bool.Load per
// candidate. The route's RWMutex.RLock guards against a concurrent
// SetRoute swapping the slice — uncontended, ~5 ns.
func (r *LLMRouter) Next(routeName string) (string, error) {
	r.totalNexts.Add(1)
	v, ok := r.routes.Load(routeName)
	if !ok {
		return "", ErrUnknownRoute
	}
	rt := v.(*route)
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	skipped := false
	for _, p := range rt.providers {
		if p.healthy.Load() {
			p.picks.Add(1)
			rt.picks.Add(1)
			if skipped {
				rt.rotations.Add(1)
				r.totalFailovers.Add(1)
			}
			return p.name, nil
		}
		p.skips.Add(1)
		skipped = true
	}
	return "", ErrNoHealthyProvider
}

// MarkDown flags a provider as unhealthy. Atomic store; the next Next
// call observing this provider in any route will skip it.
func (r *LLMRouter) MarkDown(providerName string) error {
	v, ok := r.providers.Load(providerName)
	if !ok {
		return ErrUnknownProvider
	}
	p := v.(*provider)
	p.healthy.Store(false)
	p.lastMarkNS.Store(time.Now().UnixNano())
	return nil
}

// MarkUp flips a provider back to healthy. Used by circuit-breaker
// probes that detected the upstream is alive again.
func (r *LLMRouter) MarkUp(providerName string) error {
	v, ok := r.providers.Load(providerName)
	if !ok {
		return ErrUnknownProvider
	}
	p := v.(*provider)
	p.healthy.Store(true)
	p.lastMarkNS.Store(time.Now().UnixNano())
	return nil
}

// IsHealthy is a lock-free read of a provider's flag.
func (r *LLMRouter) IsHealthy(providerName string) (bool, error) {
	v, ok := r.providers.Load(providerName)
	if !ok {
		return false, ErrUnknownProvider
	}
	return v.(*provider).healthy.Load(), nil
}

// ErrUnknownRoute / ErrUnknownProvider are typed errors for the RESP
// command layer to surface as named replies.
var (
	ErrUnknownRoute      = errors.New("UNKNOWNROUTE no such route")
	ErrUnknownProvider   = errors.New("UNKNOWNPROVIDER no such provider")
	ErrNoHealthyProvider = errors.New("NOHEALTHY every provider in the route is marked down")
)

// RouteStatus is one row in LLM.ROUTE.LIST output.
type RouteStatus struct {
	Name      string           `json:"name"`
	Providers []ProviderStatus `json:"providers"`
	Picks     int64            `json:"picks"`
	Rotations int64            `json:"rotations"`
}

// ProviderStatus is one provider's snapshot.
type ProviderStatus struct {
	Name        string `json:"name"`
	Healthy     bool   `json:"healthy"`
	Picks       int64  `json:"picks"`
	Skips       int64  `json:"skips"`
	LastMarkNS  int64  `json:"last_mark_ns"`
}

// List returns a snapshot of every configured route + provider state.
func (r *LLMRouter) List() []RouteStatus {
	var out []RouteStatus
	r.routes.Range(func(k, v any) bool {
		rt := v.(*route)
		rt.mu.RLock()
		ps := make([]ProviderStatus, 0, len(rt.providers))
		for _, p := range rt.providers {
			ps = append(ps, ProviderStatus{
				Name:       p.name,
				Healthy:    p.healthy.Load(),
				Picks:      p.picks.Load(),
				Skips:      p.skips.Load(),
				LastMarkNS: p.lastMarkNS.Load(),
			})
		}
		rt.mu.RUnlock()
		out = append(out, RouteStatus{
			Name:      k.(string),
			Providers: ps,
			Picks:     rt.picks.Load(),
			Rotations: rt.rotations.Load(),
		})
		return true
	})
	return out
}

// RouterStats is the global counters snapshot.
type RouterStats struct {
	TotalNexts     int64 `json:"total_nexts"`
	TotalFailovers int64 `json:"total_failovers"`
	UniqueRoutes   int   `json:"unique_routes"`
	UniqueProviders int  `json:"unique_providers"`
}

func (r *LLMRouter) Stats() RouterStats {
	nRoutes, nProvs := 0, 0
	r.routes.Range(func(_, _ any) bool { nRoutes++; return true })
	r.providers.Range(func(_, _ any) bool { nProvs++; return true })
	return RouterStats{
		TotalNexts:      r.totalNexts.Load(),
		TotalFailovers:  r.totalFailovers.Load(),
		UniqueRoutes:    nRoutes,
		UniqueProviders: nProvs,
	}
}

// Forget drops a route. The underlying providers stay registered so
// other routes referencing them continue to see their health state.
func (r *LLMRouter) Forget(routeName string) bool {
	_, was := r.routes.LoadAndDelete(routeName)
	return was
}
