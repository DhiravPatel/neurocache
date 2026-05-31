package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ReplayShadow is the always-on shadow-replay primitive. SANDBOX
// replays *historical* traffic against a proposed config diff;
// ReplayShadow runs *live*: every production request is mirrored to
// a candidate, the two outputs are compared, divergence is recorded.
// This is what catches "the diff looked fine in SANDBOX but is
// silently degrading on traffic shapes we didn't replay" — the
// always-on safety net for the SANDBOX promote.
//
// Lifecycle per (live, shadow) pair:
//
//   ENABLE → for every RECORD(live), engine also accepts an
//            RECORD(shadow) with the same request_id. We track both
//            outputs and compute divergence on-the-fly.
//   DIVERGENCE → cumulative agree rate, mean similarity, top
//            divergent recent samples.
//   ALERT → fires when agree rate drops below a configurable floor
//            (returned in DIVERGENCE; caller dispatches the alert).
//   DISABLE → end the shadow.
//
// Commands:
//
//   REPLAY.SHADOW.ENABLE pair-id live-route shadow-route [MIN_AGREE f]
//   REPLAY.SHADOW.RECORD pair-id request-id LIVE "live-output" SHADOW "shadow-output"
//   REPLAY.SHADOW.DIVERGENCE pair-id [LIMIT n]
//        → cumulative agree rate + top-N divergent samples + alert (bool)
//   REPLAY.SHADOW.DISABLE pair-id
//   REPLAY.SHADOW.LIST
//   REPLAY.SHADOW.STATS
//
// Hot path: RECORD is O(1) (append + cheap line-overlap similarity).
// DIVERGENCE returns the rolling window — typically last few thousand.
type ReplayShadow struct {
	mu    sync.RWMutex
	pairs map[string]*replayShadowPair

	totalEnables atomic.Int64
	totalRecords atomic.Int64
	totalAlerts  atomic.Int64
}

type replayShadowPair struct {
	mu        sync.Mutex
	id        string
	liveRoute string
	shadow    string
	minAgree  float64
	samples   []replaySample
	max       int
	disabled  bool
	createdAt time.Time
}

type replaySample struct {
	RequestID string
	Live      string
	Shadow    string
	Agree     bool
	Similarity float64
	At        time.Time
}

const replayShadowMax = 5000

// NewReplayShadow returns an empty registry.
func NewReplayShadow() *ReplayShadow {
	return &ReplayShadow{pairs: map[string]*replayShadowPair{}}
}

// Enable opens a shadow pair.
func (r *ReplayShadow) Enable(pairID, liveRoute, shadowRoute string, minAgree float64) error {
	if pairID == "" {
		return errors.New("pair_id required")
	}
	if liveRoute == "" || shadowRoute == "" {
		return errors.New("live and shadow routes required")
	}
	if minAgree < 0 || minAgree > 1 {
		return errors.New("min_agree must be in [0,1]")
	}
	if minAgree == 0 {
		minAgree = 0.9
	}
	r.totalEnables.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pairs[pairID]; ok {
		return errors.New("pair already exists: " + pairID)
	}
	r.pairs[pairID] = &replayShadowPair{
		id: pairID, liveRoute: liveRoute, shadow: shadowRoute,
		minAgree: minAgree, max: replayShadowMax, createdAt: time.Now(),
	}
	return nil
}

// Record posts one (live, shadow) output pair for one request_id.
func (r *ReplayShadow) Record(pairID, requestID, live, shadow string) error {
	if pairID == "" || requestID == "" {
		return errors.New("pair_id and request_id required")
	}
	r.totalRecords.Add(1)
	r.mu.RLock()
	p, ok := r.pairs[pairID]
	r.mu.RUnlock()
	if !ok {
		return errors.New("unknown pair: " + pairID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disabled {
		return errors.New("pair is disabled")
	}
	sim := outputSimilarity(live, shadow)
	p.samples = append(p.samples, replaySample{
		RequestID: requestID, Live: live, Shadow: shadow,
		Agree: sim >= 0.95, Similarity: sim, At: time.Now(),
	})
	if len(p.samples) > p.max {
		p.samples = p.samples[len(p.samples)-p.max:]
	}
	return nil
}

// outputSimilarity is a cheap line-overlap Jaccard. For real
// production teams using LLM-judge or BLEU/ROUGE, register a
// downstream worker — this primitive owns the bookkeeping, not the
// quality model.
func outputSimilarity(a, b string) float64 {
	la := strings.Split(a, "\n")
	lb := strings.Split(b, "\n")
	setA := map[string]bool{}
	for _, l := range la {
		setA[l] = true
	}
	inter := 0
	for _, l := range lb {
		if setA[l] {
			inter++
			delete(setA, l)
		}
	}
	union := len(la) + len(lb) - inter
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

// ReplayShadowDivergence is DIVERGENCE's return.
type ReplayShadowDivergence struct {
	PairID         string                   `json:"pair_id"`
	N              int                      `json:"n"`
	AgreeRate      float64                  `json:"agree_rate"`
	MeanSimilarity float64                  `json:"mean_similarity"`
	MinAgree       float64                  `json:"min_agree"`
	Alert          bool                     `json:"alert"`
	TopDivergent   []ReplayShadowSampleRow  `json:"top_divergent"`
}

// ReplayShadowSampleRow is one row of top_divergent.
type ReplayShadowSampleRow struct {
	RequestID  string  `json:"request_id"`
	Live       string  `json:"live"`
	Shadow     string  `json:"shadow"`
	Similarity float64 `json:"similarity"`
	AtUnix     int64   `json:"at_unix"`
}

// Divergence summarises the pair's recent samples. Alert is true if
// agree_rate < min_agree.
func (r *ReplayShadow) Divergence(pairID string, limit int) (ReplayShadowDivergence, bool) {
	if limit <= 0 {
		limit = 5
	}
	r.mu.RLock()
	p, ok := r.pairs[pairID]
	r.mu.RUnlock()
	if !ok {
		return ReplayShadowDivergence{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := ReplayShadowDivergence{
		PairID: p.id, N: len(p.samples), MinAgree: p.minAgree,
	}
	if len(p.samples) == 0 {
		return out, true
	}
	agree := 0
	simSum := 0.0
	for _, s := range p.samples {
		if s.Agree {
			agree++
		}
		simSum += s.Similarity
	}
	out.AgreeRate = float64(agree) / float64(len(p.samples))
	out.MeanSimilarity = simSum / float64(len(p.samples))
	if out.AgreeRate < p.minAgree {
		out.Alert = true
		r.totalAlerts.Add(1)
	}
	// Top divergent: sort recent samples by similarity asc
	div := make([]replaySample, len(p.samples))
	copy(div, p.samples)
	sort.SliceStable(div, func(i, j int) bool { return div[i].Similarity < div[j].Similarity })
	if len(div) > limit {
		div = div[:limit]
	}
	for _, s := range div {
		out.TopDivergent = append(out.TopDivergent, ReplayShadowSampleRow{
			RequestID: s.RequestID, Live: s.Live, Shadow: s.Shadow,
			Similarity: s.Similarity, AtUnix: s.At.Unix(),
		})
	}
	return out, true
}

// Disable ends a shadow pair (samples retained for queries).
func (r *ReplayShadow) Disable(pairID string) error {
	r.mu.RLock()
	p, ok := r.pairs[pairID]
	r.mu.RUnlock()
	if !ok {
		return errors.New("unknown pair: " + pairID)
	}
	p.mu.Lock()
	p.disabled = true
	p.mu.Unlock()
	return nil
}

// ReplayShadowListRow is one row of LIST.
type ReplayShadowListRow struct {
	PairID      string  `json:"pair_id"`
	LiveRoute   string  `json:"live_route"`
	ShadowRoute string  `json:"shadow_route"`
	Samples     int     `json:"samples"`
	Disabled    bool    `json:"disabled"`
}

// List returns all pairs.
func (r *ReplayShadow) List() []ReplayShadowListRow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ReplayShadowListRow, 0, len(r.pairs))
	for _, p := range r.pairs {
		p.mu.Lock()
		out = append(out, ReplayShadowListRow{
			PairID: p.id, LiveRoute: p.liveRoute, ShadowRoute: p.shadow,
			Samples: len(p.samples), Disabled: p.disabled,
		})
		p.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PairID < out[j].PairID })
	return out
}

// Forget drops a pair (or all).
func (r *ReplayShadow) Forget(pairID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pairID == "ALL" {
		n := len(r.pairs)
		r.pairs = map[string]*replayShadowPair{}
		return n
	}
	if _, ok := r.pairs[pairID]; ok {
		delete(r.pairs, pairID)
		return 1
	}
	return 0
}

// ReplayShadowStats is the global snapshot.
type ReplayShadowStats struct {
	Pairs        int   `json:"pairs"`
	TotalEnables int64 `json:"total_enables"`
	TotalRecords int64 `json:"total_records"`
	TotalAlerts  int64 `json:"total_alerts"`
}

func (r *ReplayShadow) Stats() ReplayShadowStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return ReplayShadowStats{
		Pairs: len(r.pairs),
		TotalEnables: r.totalEnables.Load(),
		TotalRecords: r.totalRecords.Load(),
		TotalAlerts: r.totalAlerts.Load(),
	}
}
