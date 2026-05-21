package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// MoERouter is a Mixture-of-Experts router. Modern LLM apps don't
// just use one model — they fan out to specialized experts (math
// expert, code expert, creative-writing expert, vision expert).
// Routing decisions today are usually hand-coded rules or a single
// classifier — both fragile.
//
// MOE.* gives the cache a smart router that combines two signals:
//
//   1. CAPABILITY MATCH — cosine between the query's embedding and
//      the expert's description embedding. Captures "this expert is
//      good at THIS kind of question."
//
//   2. LIVE HEALTH — recent success-rate + average latency from
//      MOE.RECORD calls. Captures "this expert is actually working
//      right now, not currently rate-limited or failing."
//
// The routing score is `cosine × (success_rate + α)` where α is a
// small smoothing constant so new experts aren't permanently
// blacklisted by an early failure. ROUTE returns top-K experts
// ranked by this combined score.
//
// Commands:
//
//   MOE.EXPERT.REGISTER expert-id name description
//        [TAGS t1,t2,...] [EMBED v,v,...]
//   MOE.ROUTE query [K n] [TAGS t1,t2,...]
//        → array of {expert_id, score, capability, success_rate}
//   MOE.RECORD expert-id success [LATENCY_MS n]
//   MOE.EXPERTS [TAGS ...]              → list with stats
//   MOE.FORGET expert-id
//   MOE.STATS
//
// Atomic counters for success-rate updates → lock-free hot path on
// RECORD. ROUTE is O(N) cosine — sub-microsecond per expert at
// 128-dim, so 100 experts × 128 dims ≈ 10-15 µs.
type MoERouter struct {
	mu      sync.RWMutex
	experts map[string]*moeExpert
	dim     int

	totalRoutes  atomic.Int64
	totalReturns atomic.Int64
	totalRecords atomic.Int64
}

type moeExpert struct {
	id          string
	name        string
	description string
	tags        map[string]bool
	vec         []float64

	calls      atomic.Int64
	successes  atomic.Int64
	totalLatMS atomic.Int64
}

// NewMoERouter returns an empty registry.
func NewMoERouter() *MoERouter {
	return &MoERouter{experts: map[string]*moeExpert{}}
}

// ExpertOpts configures REGISTER.
type ExpertOpts struct {
	Tags []string
	Vec  []float64
}

// RegisterExpert stores or replaces an expert. The first registration
// fixes the embedding dim.
func (m *MoERouter) RegisterExpert(id, name, description string, opts ExpertOpts) error {
	if id == "" || name == "" {
		return errors.New("id and name required")
	}
	tags := map[string]bool{}
	for _, t := range opts.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			tags[t] = true
		}
	}
	vec := opts.Vec
	if vec == nil {
		vec = embedFallback(name + " " + description)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dim == 0 {
		m.dim = len(vec)
	} else if len(vec) != m.dim {
		return errors.New("embedding dim mismatch with existing experts")
	}
	// Preserve atomic counters on replace.
	if existing, ok := m.experts[id]; ok {
		existing.name = name
		existing.description = description
		existing.tags = tags
		existing.vec = normaliseInPlace(vec)
		return nil
	}
	m.experts[id] = &moeExpert{
		id: id, name: name, description: description,
		tags: tags, vec: normaliseInPlace(vec),
	}
	return nil
}

// RouteHit is one row of ROUTE output.
type RouteHit struct {
	ExpertID    string  `json:"expert_id"`
	Name        string  `json:"name"`
	Score       float64 `json:"score"`        // capability × (success_rate + α)
	Capability  float64 `json:"capability"`   // raw cosine
	SuccessRate float64 `json:"success_rate"` // 0..1
	Calls       int64   `json:"calls"`
}

// RouteOpts narrows the routing query.
type RouteOpts struct {
	K    int
	Tags []string
	Vec  []float64
}

// Route returns the top-K experts for `query`. K defaults to 1.
func (m *MoERouter) Route(query string, opts RouteOpts) []RouteHit {
	m.totalRoutes.Add(1)
	k := opts.K
	if k <= 0 {
		k = 1
	}
	queryVec := opts.Vec
	if queryVec == nil {
		queryVec = embedFallback(query)
	}
	want := map[string]bool{}
	for _, t := range opts.Tags {
		want[strings.ToLower(strings.TrimSpace(t))] = true
	}
	m.mu.RLock()
	if m.dim == 0 || len(queryVec) != m.dim {
		m.mu.RUnlock()
		return nil
	}
	// Normalise the query so dot product == cosine.
	normQuery := make([]float64, len(queryVec))
	qNorm := math.Sqrt(dotProduct(queryVec, queryVec))
	if qNorm == 0 {
		m.mu.RUnlock()
		return nil
	}
	for i, v := range queryVec {
		normQuery[i] = v / qNorm
	}
	const alpha = 0.05 // smoothing — new experts aren't dead-zero
	hits := make([]RouteHit, 0, len(m.experts))
	for _, e := range m.experts {
		if !tagsMatch(e.tags, want) {
			continue
		}
		calls := e.calls.Load()
		succ := e.successes.Load()
		rate := 1.0
		if calls > 0 {
			rate = float64(succ) / float64(calls)
		}
		capability := dotProduct(normQuery, e.vec)
		hits = append(hits, RouteHit{
			ExpertID:    e.id,
			Name:        e.name,
			Capability:  capability,
			SuccessRate: rate,
			Score:       capability * (rate + alpha),
			Calls:       calls,
		})
	}
	m.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	m.totalReturns.Add(int64(len(hits)))
	return hits
}

// Record updates the live health stats for an expert. Apps call
// this after the upstream completes (success=true on 2xx, false
// on error/rate-limit/timeout).
func (m *MoERouter) Record(expertID string, success bool, latencyMS int64) bool {
	m.mu.RLock()
	e, ok := m.experts[expertID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	e.calls.Add(1)
	if success {
		e.successes.Add(1)
	}
	if latencyMS > 0 {
		e.totalLatMS.Add(latencyMS)
	}
	m.totalRecords.Add(1)
	return true
}

// ExpertRow is one row of EXPERTS.
type ExpertRow struct {
	ExpertID     string   `json:"expert_id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Tags         []string `json:"tags,omitempty"`
	Calls        int64    `json:"calls"`
	Successes    int64    `json:"successes"`
	SuccessRate  float64  `json:"success_rate"`
	AvgLatencyMS int64    `json:"avg_latency_ms"`
}

// Experts returns every registered expert with live stats, sorted by id.
func (m *MoERouter) Experts(filterTags []string) []ExpertRow {
	want := map[string]bool{}
	for _, t := range filterTags {
		want[strings.ToLower(strings.TrimSpace(t))] = true
	}
	m.mu.RLock()
	out := make([]ExpertRow, 0, len(m.experts))
	for _, e := range m.experts {
		if !tagsMatch(e.tags, want) {
			continue
		}
		calls := e.calls.Load()
		succ := e.successes.Load()
		rate := 1.0
		if calls > 0 {
			rate = float64(succ) / float64(calls)
		}
		avgMS := int64(0)
		if calls > 0 {
			avgMS = e.totalLatMS.Load() / calls
		}
		out = append(out, ExpertRow{
			ExpertID: e.id, Name: e.name, Description: e.description,
			Tags: tagsList(e.tags), Calls: calls, Successes: succ,
			SuccessRate: rate, AvgLatencyMS: avgMS,
		})
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ExpertID < out[j].ExpertID })
	return out
}

// Forget drops an expert. Returns true if it existed.
func (m *MoERouter) Forget(expertID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.experts[expertID]
	delete(m.experts, expertID)
	return ok
}

// MoEStats is the global snapshot.
type MoEStats struct {
	Experts      int   `json:"experts"`
	TotalRoutes  int64 `json:"total_routes"`
	TotalReturns int64 `json:"total_returns"`
	TotalRecords int64 `json:"total_records"`
}

func (m *MoERouter) Stats() MoEStats {
	m.mu.RLock()
	n := len(m.experts)
	m.mu.RUnlock()
	return MoEStats{
		Experts:      n,
		TotalRoutes:  m.totalRoutes.Load(),
		TotalReturns: m.totalReturns.Load(),
		TotalRecords: m.totalRecords.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func normaliseInPlace(vec []float64) []float64 {
	out := make([]float64, len(vec))
	norm := math.Sqrt(dotProduct(vec, vec))
	if norm == 0 {
		return out
	}
	for i, v := range vec {
		out[i] = v / norm
	}
	return out
}
