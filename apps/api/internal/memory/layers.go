package memory

// Layered memory: episodic / semantic / procedural tiers, importance
// weighting, recency-weighted ranking, near-duplicate dedup on write,
// adaptive decay, and bulk consolidation. Built on top of the existing
// Store keeping the legacy `Add` / `Query` / `Delete` API source-compat.
//
// Why this exists: a flat "list of strings keyed by user" memory is
// what every Redis-on-top RAG demo builds, and it falls over in three
// places — duplicates pile up, old chatter buries facts, and there's
// no way to ask "give me the user's preferences" vs "give me what we
// said yesterday." Layers + decay + consolidation are the production
// answer.

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
)

// AddOptions controls a layered write. Layer defaults to episodic;
// Importance defaults to 0.5. DedupThreshold of 0 disables the dedup
// check; values in (0, 1] enable cosine-similarity dedup against
// existing entries in the same layer.
type AddOptions struct {
	Layer           Layer
	Importance      float64
	DedupThreshold  float64
	Meta            map[string]string
	SourceIDs       []string
}

// AddWithOptions records a memory at the requested layer. When
// DedupThreshold > 0, similar existing entries are NOT duplicated —
// the matching entry is touched (LastAccessedAt + AccessCount) and
// returned in place of a new write. Returns (entry, isNew).
func (s *Store) AddWithOptions(userID, text string, opts AddOptions) (*Entry, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, errors.New("text required")
	}
	layer := opts.Layer
	if layer == "" {
		layer = LayerEpisodic
	}
	if !layer.IsValid() {
		return nil, false, errors.New("invalid layer: " + string(layer))
	}
	imp := opts.Importance
	if imp < 0 {
		imp = 0
	}
	if imp > 1 {
		imp = 1
	}

	// Dedup pass — if any existing entry in the same layer crosses the
	// threshold, touch it and return early.
	if opts.DedupThreshold > 0 {
		hits := s.ix.Search(text, 0, float32(opts.DedupThreshold))
		for _, h := range hits {
			if h.Meta["user_id"] != userID {
				continue
			}
			if Layer(h.Meta["layer"]) != layer {
				continue
			}
			s.mu.Lock()
			if existing, ok := s.byID[h.ID]; ok {
				existing.LastAccessedAt = time.Now()
				existing.AccessCount++
				// Pull importance up to the new write's value if higher;
				// "this came up again as important" is real signal.
				if imp > existing.Importance {
					existing.Importance = imp
				}
				ret := *existing
				s.mu.Unlock()
				return &ret, false, nil
			}
			s.mu.Unlock()
		}
	}

	meta := map[string]string{}
	for k, v := range opts.Meta {
		meta[k] = v
	}
	meta["user_id"] = userID
	meta["layer"] = string(layer)

	e := &Entry{
		ID:             newID(),
		UserID:         userID,
		Text:           text,
		CreatedAt:      time.Now(),
		LastAccessedAt: time.Now(),
		Meta:           meta,
		Layer:          layer,
		Importance:     imp,
		SourceIDs:      append([]string(nil), opts.SourceIDs...),
	}

	s.mu.Lock()
	s.byID[e.ID] = e
	if _, ok := s.byUser[userID]; !ok {
		s.byUser[userID] = make(map[string]struct{})
	}
	s.byUser[userID][e.ID] = struct{}{}
	s.mu.Unlock()

	s.ix.Upsert(e.ID, e.Text, e.Meta)
	return e, true, nil
}

// LayerQueryOptions tune a layer-scoped semantic query.
type LayerQueryOptions struct {
	Layer       Layer
	K           int
	Threshold   float32
	RecencyBias float64 // 0=ignore recency, 1=heavy recency weighting
	TouchHits   bool    // update LastAccessedAt/AccessCount on returned hits
}

// QueryLayered semantic-searches one layer for a user. Score is the
// cosine similarity blended with recency: final = (1-bias)*sim +
// bias*recency_decay. Recency decay is exp(-age_days/30) — a mild
// "newer is better" tilt that stops without going off a cliff.
func (s *Store) QueryLayered(userID, q string, opts LayerQueryOptions) []QueryHit {
	if opts.K <= 0 {
		opts.K = 10
	}
	hits := s.ix.Search(q, 0, opts.Threshold)
	out := make([]QueryHit, 0, opts.K)
	now := time.Now()
	bias := opts.RecencyBias
	if bias < 0 {
		bias = 0
	}
	if bias > 1 {
		bias = 1
	}

	type ranked struct {
		hit   QueryHit
		blend float64
	}
	candidates := make([]ranked, 0, len(hits))

	s.mu.RLock()
	for _, h := range hits {
		if h.Meta["user_id"] != userID {
			continue
		}
		if opts.Layer != "" && Layer(h.Meta["layer"]) != opts.Layer {
			continue
		}
		e, ok := s.byID[h.ID]
		if !ok {
			continue
		}
		ageDays := now.Sub(e.CreatedAt).Hours() / 24
		recency := expDecay(ageDays, 30)
		// Importance acts as a floor — important memories don't decay
		// in ranking even if they're old.
		if e.Importance > recency {
			recency = e.Importance
		}
		blend := (1-bias)*float64(h.Score) + bias*recency
		candidates = append(candidates, ranked{
			hit:   QueryHit{Entry: e, Score: float32(blend)},
			blend: blend,
		})
	}
	s.mu.RUnlock()

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].blend > candidates[j].blend })
	if len(candidates) > opts.K {
		candidates = candidates[:opts.K]
	}
	for _, c := range candidates {
		out = append(out, c.hit)
	}
	if opts.TouchHits {
		s.mu.Lock()
		for _, h := range out {
			if e, ok := s.byID[h.Entry.ID]; ok {
				e.LastAccessedAt = now
				e.AccessCount++
			}
		}
		s.mu.Unlock()
	}
	return out
}

// DecayOptions parameterize a decay sweep.
type DecayOptions struct {
	Layer        Layer         // only this layer (default: episodic only)
	MaxAge       time.Duration // hard cutoff regardless of importance
	HalfLife     time.Duration // exponential weight; 0 disables soft decay
	MinScore     float64       // entries falling below this composite are dropped
	DryRun       bool          // count what would be dropped, don't actually drop
	UntouchedFor time.Duration // require LastAccessedAt older than this; 0 = ignore
}

// DecayResult reports what Decay touched.
type DecayResult struct {
	Scanned int `json:"scanned"`
	Dropped int `json:"dropped"`
}

// Decay sweeps entries in a layer and removes ones whose age,
// importance, and last-access pattern put them below MinScore. Use a
// soft sweep (HalfLife + MinScore) for routine cleanup; use MaxAge
// for hard retention policies.
func (s *Store) Decay(userID string, opts DecayOptions) DecayResult {
	if opts.Layer == "" {
		opts.Layer = LayerEpisodic
	}
	if opts.MinScore <= 0 {
		opts.MinScore = 0.05
	}
	now := time.Now()

	s.mu.RLock()
	candidates := []*Entry{}
	if userID == "" {
		for _, e := range s.byID {
			if e.Layer == opts.Layer {
				candidates = append(candidates, e)
			}
		}
	} else if set, ok := s.byUser[userID]; ok {
		for id := range set {
			if e, ok := s.byID[id]; ok && e.Layer == opts.Layer {
				candidates = append(candidates, e)
			}
		}
	}
	s.mu.RUnlock()

	res := DecayResult{Scanned: len(candidates)}
	for _, e := range candidates {
		drop := false
		if opts.MaxAge > 0 && now.Sub(e.CreatedAt) > opts.MaxAge {
			drop = true
		}
		if !drop && opts.UntouchedFor > 0 && !e.LastAccessedAt.IsZero() &&
			now.Sub(e.LastAccessedAt) > opts.UntouchedFor {
			drop = true
		}
		if !drop && opts.HalfLife > 0 {
			ageHL := now.Sub(e.CreatedAt).Hours() / opts.HalfLife.Hours()
			weight := expDecay(ageHL, 1) * (0.5 + 0.5*e.Importance) *
				(1 + 0.05*float64(e.AccessCount))
			if weight < opts.MinScore {
				drop = true
			}
		}
		if drop {
			res.Dropped++
			if !opts.DryRun {
				s.Delete(e.UserID, e.ID)
			}
		}
	}
	return res
}

// ConsolidateOptions tune the consolidation pass.
type ConsolidateOptions struct {
	UserID    string  // required
	Threshold float64 // cosine similarity to cluster (default 0.85)
	MinSize   int     // minimum cluster size to consolidate (default 3)
	Drop      bool    // delete clustered episodic entries after writing summary
	Importance float64
}

// ConsolidateResult reports what consolidation produced.
type ConsolidateResult struct {
	Clusters int      `json:"clusters"`
	Written  int      `json:"written"`
	Dropped  int      `json:"dropped"`
	NewIDs   []string `json:"new_ids"`
}

// Consolidate clusters a user's episodic memories by cosine
// similarity, writes one synthetic semantic-layer entry per cluster
// (text = "; "-joined cluster sample), and optionally drops the
// originals. The synthesized text is intentionally raw; production
// deployments wire an LLM summarizer on top — this method gives the
// cluster + the SourceIDs, and the caller can re-write Text after the
// fact via MEMORY.CONSOLIDATE_REPLACE (future work) or simply by
// reading SourceIDs and computing their own summary.
//
// We deliberately *don't* call out to an LLM here. NeuroCache's
// promise is "infrastructure primitives." Calling OpenAI from the
// data-plane would couple every deployment to outbound HTTP and a
// secret, and would block AOF replay on a model API.
func (s *Store) Consolidate(opts ConsolidateOptions) ConsolidateResult {
	if opts.Threshold <= 0 {
		opts.Threshold = 0.85
	}
	if opts.MinSize <= 0 {
		opts.MinSize = 3
	}
	if opts.Importance == 0 {
		opts.Importance = 0.7
	}

	s.mu.RLock()
	episodic := []*Entry{}
	if set, ok := s.byUser[opts.UserID]; ok {
		for id := range set {
			if e, ok := s.byID[id]; ok && e.Layer == LayerEpisodic {
				episodic = append(episodic, e)
			}
		}
	}
	s.mu.RUnlock()

	if len(episodic) < opts.MinSize {
		return ConsolidateResult{}
	}

	// Cheap O(N²) clustering — fine up to a few thousand entries per
	// user, which is the realistic per-user volume. Larger volumes
	// belong in a periodic offline job, not the data-plane.
	dim := s.ix.Size()
	_ = dim
	embeds := make([][]float32, len(episodic))
	for i, e := range episodic {
		embeds[i] = vector.Embed(e.Text, 384)
	}

	assigned := make([]int, len(episodic))
	for i := range assigned {
		assigned[i] = -1
	}
	clusterID := 0
	clusters := [][]int{}
	for i := range episodic {
		if assigned[i] >= 0 {
			continue
		}
		cluster := []int{i}
		assigned[i] = clusterID
		for j := i + 1; j < len(episodic); j++ {
			if assigned[j] >= 0 {
				continue
			}
			if float64(vector.Cosine(embeds[i], embeds[j])) >= opts.Threshold {
				cluster = append(cluster, j)
				assigned[j] = clusterID
			}
		}
		if len(cluster) >= opts.MinSize {
			clusters = append(clusters, cluster)
		}
		clusterID++
	}

	res := ConsolidateResult{Clusters: len(clusters)}
	for _, cluster := range clusters {
		// Build a representative summary text. Sort by importance, then
		// recency, take up to 5 lines as the seed; callers can rewrite.
		rows := make([]*Entry, len(cluster))
		for i, idx := range cluster {
			rows[i] = episodic[idx]
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Importance != rows[j].Importance {
				return rows[i].Importance > rows[j].Importance
			}
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		})
		take := rows
		if len(take) > 5 {
			take = take[:5]
		}
		bits := make([]string, 0, len(take))
		ids := make([]string, 0, len(rows))
		for _, e := range rows {
			ids = append(ids, e.ID)
		}
		for _, e := range take {
			bits = append(bits, e.Text)
		}
		summary, _, err := s.AddWithOptions(opts.UserID, strings.Join(bits, "; "), AddOptions{
			Layer:      LayerSemantic,
			Importance: opts.Importance,
			Meta:       map[string]string{"derived": "consolidate"},
			SourceIDs:  ids,
		})
		if err != nil {
			continue
		}
		res.Written++
		res.NewIDs = append(res.NewIDs, summary.ID)
		if opts.Drop {
			for _, id := range ids {
				if s.Delete(opts.UserID, id) {
					res.Dropped++
				}
			}
		}
	}
	return res
}

// ListByLayer returns one user's entries, optionally filtered by layer.
func (s *Store) ListByLayer(userID string, layer Layer) []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.byUser[userID]
	out := make([]*Entry, 0, len(set))
	for id := range set {
		e, ok := s.byID[id]
		if !ok {
			continue
		}
		if layer != "" && e.Layer != layer {
			continue
		}
		out = append(out, e)
	}
	return out
}

// LayerStats reports entries-per-layer for a user.
type LayerStats struct {
	Episodic   int `json:"episodic"`
	Semantic   int `json:"semantic"`
	Procedural int `json:"procedural"`
	Other      int `json:"other"`
}

// LayerStats reports a per-user layer breakdown. Empty UserID returns
// the global breakdown across all users — useful for the dashboard.
func (s *Store) LayerStats(userID string) LayerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := LayerStats{}
	visit := func(e *Entry) {
		switch e.Layer {
		case LayerEpisodic, "":
			st.Episodic++
		case LayerSemantic:
			st.Semantic++
		case LayerProcedural:
			st.Procedural++
		default:
			st.Other++
		}
	}
	if userID == "" {
		for _, e := range s.byID {
			visit(e)
		}
		return st
	}
	if set, ok := s.byUser[userID]; ok {
		for id := range set {
			if e, ok := s.byID[id]; ok {
				visit(e)
			}
		}
	}
	return st
}

// expDecay returns exp(-x/scale) clamped to [0,1].
func expDecay(x, scale float64) float64 {
	if scale <= 0 || x <= 0 {
		return 1
	}
	return math.Exp(-x / scale)
}
