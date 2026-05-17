package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// TrustRegistry maintains Bayesian trust scores per source/tool. The
// inverse of RETRIEVAL.LEARN: RL boosts chunks that get cited; TRUST
// down-weights *sources* that keep producing ungrounded answers. With
// both in place, retrieval becomes a closed loop — bad sources fade
// without anyone manually pruning the index.
//
// The model is a Beta posterior: each entity has α (successes) and β
// (failures), starting at α=1, β=1 (Jeffreys-style uniform-ish prior
// so a brand-new source isn't ranked above an established one with a
// few mistakes). The mean is α/(α+β); we also report a 95% credible
// interval from the Beta CDF approximation so a small-n score
// (5 outcomes) isn't presented with the same authority as a 5,000-n
// score.
//
// Outcome buckets the caller posts:
//
//   grounded     — GROUND.CHECK passed using this source
//   hallucinated — an answer that used this source got rejected
//   citation_used — CITE picked this source on a successful answer
//   contradicted  — MEMORY.CONFLICT flagged this source vs. another
//   neutral       — used but no outcome signal (no score effect)
//
// Each is mapped to +/- weight; admins can tune via WEIGHT.
//
// Commands:
//
//   TRUST.RECORD entity outcome [WEIGHT w]
//        entity is an opaque ID (typically "source:url" or "tool:name").
//        weight defaults to 1.
//   TRUST.SCORE entity
//        → trust, n, ci_low, ci_high
//   TRUST.RANK SOURCES|TOOLS [TOP n|BOTTOM n] [MIN_N k]
//        Default SOURCES, TOP 10, MIN_N=10 (don't bottom-rank a 2-outcome source).
//   TRUST.DECAY half_life_seconds
//        Optional time-decay of historical counts so trust adapts to model swaps.
//   TRUST.RESET entity|ALL
//   TRUST.LIST
//   TRUST.STATS
//
// Hot path: RECORD is one map lookup + two atomic adds. SCORE is one
// posterior closed-form. RANK is O(n log n) sort over registered
// entities (typically thousands, not millions).
type TrustRegistry struct {
	mu       sync.RWMutex
	entities map[string]*trustRow

	totalRecords atomic.Int64
	totalScores  atomic.Int64
	totalRanks   atomic.Int64
}

type trustRow struct {
	mu       sync.Mutex
	entity   string
	kind     string // "sources" or "tools" (parsed from entity prefix)
	alpha    float64
	beta     float64
	n        int64
	// breakdown counters for explainability
	grounded, halluc, cited, contradicted, neutral int64
}

// NewTrustRegistry returns an empty trust store.
func NewTrustRegistry() *TrustRegistry {
	return &TrustRegistry{entities: map[string]*trustRow{}}
}

// outcomeDelta maps an outcome label to (Δα, Δβ).
func outcomeDelta(outcome string, weight float64) (float64, float64, error) {
	if weight < 0 {
		return 0, 0, errors.New("weight must be non-negative")
	}
	if weight == 0 {
		weight = 1
	}
	switch strings.ToLower(outcome) {
	case "grounded":
		return weight, 0, nil
	case "hallucinated":
		return 0, weight, nil
	case "citation_used", "cited":
		return weight, 0, nil
	case "contradicted":
		return 0, weight, nil
	case "neutral":
		return 0, 0, nil
	default:
		return 0, 0, errors.New("unknown outcome: " + outcome)
	}
}

// Record posts one outcome for an entity.
func (t *TrustRegistry) Record(entity, outcome string, weight float64) error {
	if entity == "" {
		return errors.New("entity required")
	}
	da, db, err := outcomeDelta(outcome, weight)
	if err != nil {
		return err
	}
	t.totalRecords.Add(1)
	r := t.rowOrCreate(entity)
	r.mu.Lock()
	r.alpha += da
	r.beta += db
	r.n++
	switch strings.ToLower(outcome) {
	case "grounded":
		r.grounded++
	case "hallucinated":
		r.halluc++
	case "citation_used", "cited":
		r.cited++
	case "contradicted":
		r.contradicted++
	case "neutral":
		r.neutral++
	}
	r.mu.Unlock()
	return nil
}

// TrustScore is the SCORE result.
type TrustScore struct {
	Entity       string  `json:"entity"`
	Trust        float64 `json:"trust"`
	N            int64   `json:"n"`
	CILow        float64 `json:"ci_low"`
	CIHigh       float64 `json:"ci_high"`
	Grounded     int64   `json:"grounded"`
	Hallucinated int64   `json:"hallucinated"`
	Cited        int64   `json:"cited"`
	Contradicted int64   `json:"contradicted"`
	Neutral      int64   `json:"neutral"`
}

// Score returns the entity's posterior mean + a 95% credible interval.
// Unknown entities are reported as the prior (trust=0.5, n=0).
func (t *TrustRegistry) Score(entity string) TrustScore {
	t.totalScores.Add(1)
	t.mu.RLock()
	r, ok := t.entities[entity]
	t.mu.RUnlock()
	if !ok {
		return TrustScore{Entity: entity, Trust: 0.5, N: 0, CILow: 0.025, CIHigh: 0.975}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, b := r.alpha+1, r.beta+1 // Jeffreys prior
	mean := a / (a + b)
	low, high := betaCI(a, b)
	return TrustScore{
		Entity:       entity,
		Trust:        mean,
		N:            r.n,
		CILow:        low,
		CIHigh:       high,
		Grounded:     r.grounded,
		Hallucinated: r.halluc,
		Cited:        r.cited,
		Contradicted: r.contradicted,
		Neutral:      r.neutral,
	}
}

// TrustRankRow is one row of RANK.
type TrustRankRow struct {
	Entity string  `json:"entity"`
	Trust  float64 `json:"trust"`
	N      int64   `json:"n"`
}

// Rank returns entities sorted by trust. kind="" matches everything;
// otherwise it is matched against the prefix before ":" (e.g. "source").
// direction is "top" or "bottom"; default top. minN filters out
// low-evidence entities so a 2-outcome source isn't bottom-ranked
// alongside a 5,000-outcome one.
func (t *TrustRegistry) Rank(kind, direction string, n, minN int) []TrustRankRow {
	if n <= 0 {
		n = 10
	}
	if minN < 0 {
		minN = 0
	}
	if direction == "" {
		direction = "top"
	}
	t.totalRanks.Add(1)
	kind = strings.ToLower(kind)
	t.mu.RLock()
	cand := make([]TrustRankRow, 0, len(t.entities))
	for _, r := range t.entities {
		if kind != "" && kind != "all" && r.kind != kind {
			continue
		}
		r.mu.Lock()
		if r.n < int64(minN) {
			r.mu.Unlock()
			continue
		}
		a, b := r.alpha+1, r.beta+1
		cand = append(cand, TrustRankRow{
			Entity: r.entity, Trust: a / (a + b), N: r.n,
		})
		r.mu.Unlock()
	}
	t.mu.RUnlock()
	if strings.ToLower(direction) == "bottom" {
		sort.Slice(cand, func(i, j int) bool { return cand[i].Trust < cand[j].Trust })
	} else {
		sort.Slice(cand, func(i, j int) bool { return cand[i].Trust > cand[j].Trust })
	}
	if len(cand) > n {
		cand = cand[:n]
	}
	return cand
}

// Decay shrinks α and β toward the prior by half_life. Callers run
// this periodically (e.g. nightly) so old outcomes from a previous
// model version don't poison the score forever.
func (t *TrustRegistry) Decay(halfLifeSeconds float64) error {
	if halfLifeSeconds <= 0 {
		return errors.New("half_life_seconds must be positive")
	}
	// A practical implementation: shrink toward the prior by 0.5 per
	// call. Callers schedule the cadence; we don't track elapsed time
	// to keep the primitive deterministic + idempotent-per-call.
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, r := range t.entities {
		r.mu.Lock()
		r.alpha *= 0.5
		r.beta *= 0.5
		// n is unchanged so cumulative observation count is preserved
		r.mu.Unlock()
	}
	return nil
}

// Reset drops an entity (or all). entity="ALL" wipes everything.
func (t *TrustRegistry) Reset(entity string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entity == "ALL" {
		n := len(t.entities)
		t.entities = map[string]*trustRow{}
		return n
	}
	if _, ok := t.entities[entity]; ok {
		delete(t.entities, entity)
		return 1
	}
	return 0
}

// List returns every known entity (sorted).
func (t *TrustRegistry) List() []string {
	t.mu.RLock()
	out := make([]string, 0, len(t.entities))
	for k := range t.entities {
		out = append(out, k)
	}
	t.mu.RUnlock()
	sort.Strings(out)
	return out
}

// TrustStats is the global snapshot.
type TrustStats struct {
	Entities     int   `json:"entities"`
	TotalRecords int64 `json:"total_records"`
	TotalScores  int64 `json:"total_scores"`
	TotalRanks   int64 `json:"total_ranks"`
}

func (t *TrustRegistry) Stats() TrustStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TrustStats{
		Entities:     len(t.entities),
		TotalRecords: t.totalRecords.Load(),
		TotalScores:  t.totalScores.Load(),
		TotalRanks:   t.totalRanks.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (t *TrustRegistry) rowOrCreate(entity string) *trustRow {
	t.mu.RLock()
	r, ok := t.entities[entity]
	t.mu.RUnlock()
	if ok {
		return r
	}
	kind := ""
	if i := strings.Index(entity, ":"); i > 0 {
		kind = strings.ToLower(entity[:i])
		// Allow both "source" and "sources" — normalise to plural for
		// RANK's user-facing language.
		if !strings.HasSuffix(kind, "s") {
			kind += "s"
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.entities[entity]; ok {
		return r
	}
	r = &trustRow{entity: entity, kind: kind}
	t.entities[entity] = r
	return r
}

// betaCI returns an approximate 95% credible interval for Beta(α, β)
// using the Wilson-score correction. For modest α + β this is within
// a percentage point of the exact Beta inverse CDF; for α + β > 50
// it's effectively exact. We don't pull a stats library for this.
func betaCI(a, b float64) (low, high float64) {
	n := a + b
	if n <= 0 {
		return 0, 1
	}
	p := a / n
	z := 1.96
	denom := 1 + (z*z)/n
	center := (p + (z*z)/(2*n)) / denom
	margin := (z * math.Sqrt((p*(1-p)+(z*z)/(4*n))/n)) / denom
	low = center - margin
	high = center + margin
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return
}
