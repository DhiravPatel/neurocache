package aiops

import (
	"sort"
	"sync"
	"time"
)

// Policies caches RBAC + ABAC verdicts. The actual policy engine
// (OPA, Cedar, your own evaluator) is supplied as a callback; this
// layer caches its decisions so a high-throughput read path doesn't
// re-evaluate the same (user, resource, action) tuple thousands of
// times per second.
type Policies struct {
	mu      sync.RWMutex
	cache   map[string]policyEntry
	eval    PolicyEvaluator

	hits    uint64
	misses  uint64
}

// PolicyEvaluator is plugged in by the caller. It returns (allow,
// reason). The reason string is cached alongside the verdict — useful
// for audit logs.
type PolicyEvaluator func(user, resource, action string, ctx map[string]string) (bool, string)

type policyEntry struct {
	allow    bool
	reason   string
	expireAt time.Time
}

// NewPolicies returns an empty manager.
func NewPolicies(eval PolicyEvaluator) *Policies {
	return &Policies{cache: map[string]policyEntry{}, eval: eval}
}

// SetEvaluator swaps the underlying evaluator. Used during dynamic
// policy reload — combined with Purge() this gives a clean cutover.
func (p *Policies) SetEvaluator(e PolicyEvaluator) {
	p.mu.Lock()
	p.eval = e
	p.mu.Unlock()
}

// Allow returns (allow, reason) for the (user, resource, action) tuple,
// using the cache when fresh. ctx is hashed into the cache key so
// different attribute sets don't collide.
func (p *Policies) Allow(user, resource, action string, ctx map[string]string, ttl time.Duration) (bool, string) {
	key := policyKey(user, resource, action, ctx)
	p.mu.RLock()
	e, ok := p.cache[key]
	p.mu.RUnlock()
	now := time.Now()
	if ok && (e.expireAt.IsZero() || now.Before(e.expireAt)) {
		p.mu.Lock()
		p.hits++
		p.mu.Unlock()
		return e.allow, e.reason
	}
	p.mu.Lock()
	p.misses++
	eval := p.eval
	p.mu.Unlock()
	if eval == nil {
		// Fail-closed when no evaluator is wired — surface that loudly.
		return false, "no policy evaluator configured"
	}
	allow, reason := eval(user, resource, action, ctx)
	exp := time.Time{}
	if ttl > 0 {
		exp = now.Add(ttl)
	}
	p.mu.Lock()
	p.cache[key] = policyEntry{allow: allow, reason: reason, expireAt: exp}
	p.mu.Unlock()
	return allow, reason
}

// Set records a verdict directly without going through the evaluator.
// Used for "static rule overrides" and tests.
func (p *Policies) Set(user, resource, action string, ctx map[string]string, allow bool, reason string, ttl time.Duration) {
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	p.mu.Lock()
	p.cache[policyKey(user, resource, action, ctx)] = policyEntry{allow: allow, reason: reason, expireAt: exp}
	p.mu.Unlock()
}

// Purge wipes the verdict cache. Returns dropped count.
func (p *Policies) Purge() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.cache)
	p.cache = map[string]policyEntry{}
	return n
}

// PolicyStats snapshots state.
type PolicyStats struct {
	Entries int    `json:"entries"`
	Hits    uint64 `json:"hits"`
	Misses  uint64 `json:"misses"`
}

// Stats snapshots the cache.
func (p *Policies) Stats() PolicyStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return PolicyStats{Entries: len(p.cache), Hits: p.hits, Misses: p.misses}
}

// policyKey canonicalizes (user, resource, action, ctx) into a stable
// cache key. ctx keys are sorted so map-iteration randomness doesn't
// fragment the cache.
func policyKey(user, resource, action string, ctx map[string]string) string {
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb []byte
	sb = append(sb, user...)
	sb = append(sb, '|')
	sb = append(sb, resource...)
	sb = append(sb, '|')
	sb = append(sb, action...)
	sb = append(sb, '|')
	for _, k := range keys {
		sb = append(sb, k...)
		sb = append(sb, '=')
		sb = append(sb, ctx[k]...)
		sb = append(sb, ',')
	}
	return string(sb)
}
