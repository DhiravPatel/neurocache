package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// SandboxReplay holds historical traffic snapshots and replays them
// against a candidate configuration to project the impact of a
// change BEFORE shipping it. WHATIF projects one route's metrics;
// SANDBOX replays the whole system across thousands of real requests.
//
// The flow:
//
//   1. App calls RECORD on every real request, capturing inputs +
//      observed outcome (selected route, latency, cost, quality).
//      We keep a rolling window (default 100k requests).
//
//   2. App calls REPLAY with a candidate "rerouter" function — a
//      mapping (input → projected_route) that represents the diff
//      being evaluated. The engine walks the window and reports how
//      many requests would have been routed differently, and what
//      the projected aggregate metrics (cost delta, quality delta,
//      latency delta) would be.
//
// The rerouter is supplied as a JSON rule set (we ship a basic
// matcher; for richer policy, the app can register routes manually
// via REPLAY.MANUAL).
//
// Commands:
//
//   SANDBOX.RECORD sandbox-id request-id input route quality cost latency
//   SANDBOX.SET_ROUTE sandbox-id input-substring new-route
//        Add a rerouting rule (substring match).
//   SANDBOX.UNSET_ROUTE sandbox-id input-substring
//   SANDBOX.RULES sandbox-id           — list active rules
//   SANDBOX.REPLAY sandbox-id [PROJECTION-METRICS quality:0.X,cost:0.Y,latency:Z]
//        Apply the rerouter to every recorded request. Returns:
//          changed_count, cost_delta_total, quality_delta_avg,
//          latency_delta_avg, per_route_breakdown.
//   SANDBOX.SIZE sandbox-id            — recorded request count
//   SANDBOX.FORGET sandbox-id|ALL
//   SANDBOX.LIST
//   SANDBOX.STATS
//
// The rule format is intentionally minimal — it's a "string contains"
// matcher because that covers 80% of realistic prompt-routing diffs.
// For more complex policies the app can feed pre-computed routes via
// REPLAY's optional ROUTES arg.
type SandboxReplay struct {
	mu       sync.RWMutex
	sandboxes map[string]*sandbox

	totalRecords atomic.Int64
	totalReplays atomic.Int64
}

type sandbox struct {
	mu       sync.RWMutex
	requests []sandboxReq
	max      int
	rules    []sandboxRule // ordered; first match wins

	// Optional per-route projection metrics ("model:gpt-4o" → factor).
	// Used by REPLAY to project the impact of moving traffic to a route
	// without past observations.
	projection map[string]sandboxProjection
}

type sandboxReq struct {
	ID       string
	Input    string
	Route    string
	Quality  float64
	CostUSD  float64
	LatMS    float64
}

type sandboxRule struct {
	Match   string
	NewRoute string
}

type sandboxProjection struct {
	QualityScale float64
	CostScale    float64
	LatencyScale float64
}

const sandboxMaxBuf = 100000

// NewSandboxReplay returns an empty registry.
func NewSandboxReplay() *SandboxReplay {
	return &SandboxReplay{sandboxes: map[string]*sandbox{}}
}

// Record appends one observed request.
func (s *SandboxReplay) Record(sandboxID, requestID, input, route string, quality, cost, latency float64) error {
	if sandboxID == "" {
		return errors.New("sandbox_id required")
	}
	if requestID == "" {
		return errors.New("request_id required")
	}
	if input == "" {
		return errors.New("input required")
	}
	if route == "" {
		return errors.New("route required")
	}
	if quality < 0 || quality > 1 {
		return errors.New("quality must be in [0,1]")
	}
	if cost < 0 || latency < 0 {
		return errors.New("cost and latency must be non-negative")
	}
	s.totalRecords.Add(1)
	b := s.sandboxOrCreate(sandboxID)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = append(b.requests, sandboxReq{
		ID: requestID, Input: input, Route: route,
		Quality: quality, CostUSD: cost, LatMS: latency,
	})
	if len(b.requests) > b.max {
		b.requests = b.requests[len(b.requests)-b.max:]
	}
	return nil
}

// SetRoute adds (or replaces) a substring → route rule.
func (s *SandboxReplay) SetRoute(sandboxID, match, newRoute string) error {
	if sandboxID == "" || match == "" || newRoute == "" {
		return errors.New("sandbox_id, match, route required")
	}
	b := s.sandboxOrCreate(sandboxID)
	b.mu.Lock()
	defer b.mu.Unlock()
	// Replace if exists, else append (preserve order)
	for i, r := range b.rules {
		if r.Match == match {
			b.rules[i].NewRoute = newRoute
			return nil
		}
	}
	b.rules = append(b.rules, sandboxRule{Match: match, NewRoute: newRoute})
	return nil
}

// UnsetRoute drops a rule.
func (s *SandboxReplay) UnsetRoute(sandboxID, match string) int {
	s.mu.RLock()
	b, ok := s.sandboxes[sandboxID]
	s.mu.RUnlock()
	if !ok {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, r := range b.rules {
		if r.Match == match {
			b.rules = append(b.rules[:i], b.rules[i+1:]...)
			return 1
		}
	}
	return 0
}

// SetProjection sets per-route scaling factors. A request routed to a
// new route during REPLAY gets its metrics multiplied by the new
// route's projection factors. Default if unset: 1.0 (no change).
func (s *SandboxReplay) SetProjection(sandboxID, route string, qualityScale, costScale, latencyScale float64) error {
	if sandboxID == "" || route == "" {
		return errors.New("sandbox_id and route required")
	}
	b := s.sandboxOrCreate(sandboxID)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.projection == nil {
		b.projection = map[string]sandboxProjection{}
	}
	b.projection[route] = sandboxProjection{
		QualityScale: qualityScale, CostScale: costScale, LatencyScale: latencyScale,
	}
	return nil
}

// SandboxReplayResult is REPLAY's return.
type SandboxReplayResult struct {
	SandboxID        string                       `json:"sandbox_id"`
	RequestsReplayed int                          `json:"requests_replayed"`
	ChangedCount     int                          `json:"changed_count"`
	CostDeltaTotal   float64                      `json:"cost_delta_total_usd"`
	QualityDeltaAvg  float64                      `json:"quality_delta_avg"`
	LatencyDeltaAvg  float64                      `json:"latency_delta_avg_ms"`
	PerRoute         map[string]SandboxRouteStats `json:"per_route_breakdown"`
}

// SandboxRouteStats is one row in the per-route breakdown.
type SandboxRouteStats struct {
	BeforeCount int     `json:"before_count"`
	AfterCount  int     `json:"after_count"`
	CostBefore  float64 `json:"cost_before_total_usd"`
	CostAfter   float64 `json:"cost_after_total_usd"`
}

// Replay walks the recorded buffer applying the rerouting rules and
// projections; returns the aggregate impact.
func (s *SandboxReplay) Replay(sandboxID string) (SandboxReplayResult, bool) {
	if sandboxID == "" {
		return SandboxReplayResult{}, false
	}
	s.totalReplays.Add(1)
	s.mu.RLock()
	b, ok := s.sandboxes[sandboxID]
	s.mu.RUnlock()
	if !ok {
		return SandboxReplayResult{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := SandboxReplayResult{
		SandboxID: sandboxID,
		RequestsReplayed: len(b.requests),
		PerRoute: map[string]SandboxRouteStats{},
	}
	var sumQDelta, sumLatDelta float64
	for _, r := range b.requests {
		newRoute := r.Route
		for _, rule := range b.rules {
			if containsSubstr(r.Input, rule.Match) {
				newRoute = rule.NewRoute
				break
			}
		}
		changed := newRoute != r.Route
		newQ, newC, newL := r.Quality, r.CostUSD, r.LatMS
		if changed {
			out.ChangedCount++
			if p, ok := b.projection[newRoute]; ok {
				if p.QualityScale > 0 {
					newQ = r.Quality * p.QualityScale
					if newQ > 1 {
						newQ = 1
					}
				}
				if p.CostScale > 0 {
					newC = r.CostUSD * p.CostScale
				}
				if p.LatencyScale > 0 {
					newL = r.LatMS * p.LatencyScale
				}
			}
		}
		// Aggregate
		out.CostDeltaTotal += newC - r.CostUSD
		sumQDelta += newQ - r.Quality
		sumLatDelta += newL - r.LatMS
		// Per-route
		bs := out.PerRoute[r.Route]
		bs.BeforeCount++
		bs.CostBefore += r.CostUSD
		out.PerRoute[r.Route] = bs
		as := out.PerRoute[newRoute]
		as.AfterCount++
		as.CostAfter += newC
		out.PerRoute[newRoute] = as
	}
	if len(b.requests) > 0 {
		out.QualityDeltaAvg = sumQDelta / float64(len(b.requests))
		out.LatencyDeltaAvg = sumLatDelta / float64(len(b.requests))
	}
	return out, true
}

// SandboxRulesRow is one row of RULES.
type SandboxRulesRow struct {
	Match    string `json:"match"`
	NewRoute string `json:"new_route"`
}

func (s *SandboxReplay) Rules(sandboxID string) []SandboxRulesRow {
	s.mu.RLock()
	b, ok := s.sandboxes[sandboxID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]SandboxRulesRow, 0, len(b.rules))
	for _, r := range b.rules {
		out = append(out, SandboxRulesRow{Match: r.Match, NewRoute: r.NewRoute})
	}
	return out
}

// Size returns the number of recorded requests.
func (s *SandboxReplay) Size(sandboxID string) (int, bool) {
	s.mu.RLock()
	b, ok := s.sandboxes[sandboxID]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.requests), true
}

// Forget drops a sandbox (or all).
func (s *SandboxReplay) Forget(sandboxID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sandboxID == "ALL" {
		n := len(s.sandboxes)
		s.sandboxes = map[string]*sandbox{}
		return n
	}
	if _, ok := s.sandboxes[sandboxID]; ok {
		delete(s.sandboxes, sandboxID)
		return 1
	}
	return 0
}

// List returns every sandbox id.
func (s *SandboxReplay) List() []string {
	s.mu.RLock()
	out := make([]string, 0, len(s.sandboxes))
	for k := range s.sandboxes {
		out = append(out, k)
	}
	s.mu.RUnlock()
	sort.Strings(out)
	return out
}

// SandboxStats is the global snapshot.
type SandboxStats struct {
	Sandboxes    int   `json:"sandboxes"`
	TotalRecords int64 `json:"total_records"`
	TotalReplays int64 `json:"total_replays"`
	BufferedReqs int   `json:"buffered_requests"`
}

func (s *SandboxReplay) Stats() SandboxStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	buf := 0
	for _, b := range s.sandboxes {
		b.mu.RLock()
		buf += len(b.requests)
		b.mu.RUnlock()
	}
	return SandboxStats{
		Sandboxes:    len(s.sandboxes),
		TotalRecords: s.totalRecords.Load(),
		TotalReplays: s.totalReplays.Load(),
		BufferedReqs: buf,
	}
}

func (s *SandboxReplay) sandboxOrCreate(id string) *sandbox {
	s.mu.RLock()
	b, ok := s.sandboxes[id]
	s.mu.RUnlock()
	if ok {
		return b
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.sandboxes[id]; ok {
		return b
	}
	b = &sandbox{max: sandboxMaxBuf}
	s.sandboxes[id] = b
	return b
}

// containsSubstr is a case-insensitive substring match. (We do not
// import strings.Contains everywhere; one inline implementation keeps
// the dependency surface tiny.)
func containsSubstr(s, sub string) bool {
	if sub == "" {
		return true
	}
	// Cheap case-insensitive scan
	sl, subL := len(s), len(sub)
	if subL > sl {
		return false
	}
	for i := 0; i+subL <= sl; i++ {
		match := true
		for j := 0; j < subL; j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

