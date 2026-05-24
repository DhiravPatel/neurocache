package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// RAGGap turns a vector index from infra into a product-analytics
// surface. The shape of the problem: every RAG team has a knowledge
// base, and somewhere out in the world there's a steady stream of
// questions users actually ask. The overlap is partial. The
// *non-overlap* — the questions your index can't answer well — is
// what the content team needs to know about, and no cache or vector
// product surfaces it.
//
// DRIFT tells you the input distribution shifted. RAG.GAP.* tells
// you which specific clusters of questions your index is silently
// failing on:
//
//   OBSERVE  every RAG call calls this with (index, query,
//            best_retrieval_score). Cheap, atomic.
//   REPORT   clusters low-coverage queries (best_score < THRESHOLD)
//            in embedding space and surfaces the top-N gaps by
//            (volume × how badly the index missed). Each row is a
//            "ship-list item" for the content team.
//   RESOLVE  mark a cluster handled (the content team shipped the
//            docs); subsequent low-coverage observations on the
//            same cluster get attributed to a re-opened gap.
//
// Commands:
//
//   RAG.GAP.OBSERVE index-id query SCORE f
//        Record one retrieval outcome. Auto-cap per index.
//   RAG.GAP.REPORT index-id [THRESHOLD f] [LIMIT n] [WINDOW seconds]
//        → clustered gaps sorted by (volume × miss-magnitude).
//        Each row: cluster_id, sample_query, n, avg_score,
//        last_seen, resolved.
//   RAG.GAP.QUERIES index-id [THRESHOLD f] [LIMIT n]
//        Raw low-score queries (newest first), pre-clustering.
//   RAG.GAP.RESOLVE index-id cluster-id
//        Mark a cluster addressed. Survives until a new cluster
//        with similar centroid forms.
//   RAG.GAP.INDEXES
//   RAG.GAP.RESET index-id|ALL
//   RAG.GAP.STATS
//
// Hot path: OBSERVE is one embedFallback + slice append. Soft cap
// per index keeps memory bounded. REPORT does single-pass
// agglomerative clustering (cosine ≥ 0.75 → same cluster) over the
// in-window low-score subset, typically ~1 ms on 10k observations.
type RAGGap struct {
	mu      sync.RWMutex
	indexes map[string]*ragGapIndex
	cap     int

	totalObserves atomic.Int64
	totalReports  atomic.Int64
	totalResolves atomic.Int64
}

type ragGapIndex struct {
	mu          sync.RWMutex
	observations []ragGapObs
	resolved    map[string]bool // cluster_id → resolved
}

type ragGapObs struct {
	query string
	vec   []float64
	score float64
	ts    int64
}

// NewRAGGap returns a new gap tracker with a 50k-observation per-index cap.
func NewRAGGap() *RAGGap {
	return &RAGGap{
		indexes: map[string]*ragGapIndex{},
		cap:     50_000,
	}
}

// SetCap adjusts the per-index observation cap.
func (g *RAGGap) SetCap(n int) {
	g.mu.Lock()
	g.cap = n
	g.mu.Unlock()
}

// Observe records one retrieval call.
func (g *RAGGap) Observe(indexID, query string, score float64) error {
	if indexID == "" {
		return errors.New("index_id required")
	}
	if query == "" {
		return errors.New("query required")
	}
	if score < 0 {
		return errors.New("score must be >= 0")
	}
	g.totalObserves.Add(1)
	ix := g.indexOrCreate(indexID)
	vec := embedFallback(query)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if g.cap > 0 && len(ix.observations) >= g.cap {
		// Drop the oldest 10% on overflow
		drop := g.cap / 10
		ix.observations = ix.observations[drop:]
	}
	ix.observations = append(ix.observations, ragGapObs{
		query: query, vec: vec, score: score, ts: time.Now().UnixNano(),
	})
	return nil
}

// RAGGapRow is one row of REPORT output.
type RAGGapRow struct {
	ClusterID   string  `json:"cluster_id"`
	SampleQuery string  `json:"sample_query"`
	N           int     `json:"n"`
	AvgScore    float64 `json:"avg_score"`
	LastSeen    int64   `json:"last_seen"`
	Resolved    bool    `json:"resolved"`
	GapWeight   float64 `json:"gap_weight"` // n × (threshold − avg_score)
}

// RAGGapFilter narrows REPORT.
type RAGGapFilter struct {
	Threshold      float64       // retrieval score below which a hit counts as a gap
	Window         time.Duration // 0 = no time filter
	Limit          int           // 0 = no limit
	ClusterMinSim  float64       // cosine for merging into the same cluster; 0 = default 0.50
}

// Report clusters low-score queries and returns gap rows sorted by
// (n × miss-magnitude). Resolved clusters are still surfaced but
// marked, so callers can see whether ship-list items have re-opened.
func (g *RAGGap) Report(indexID string, f RAGGapFilter) ([]RAGGapRow, error) {
	g.totalReports.Add(1)
	if indexID == "" {
		return nil, errors.New("index_id required")
	}
	g.mu.RLock()
	ix, ok := g.indexes[indexID]
	g.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	if f.Threshold <= 0 {
		f.Threshold = 0.40
	}
	cutoff := int64(0)
	if f.Window > 0 {
		cutoff = time.Now().UnixNano() - f.Window.Nanoseconds()
	}

	ix.mu.RLock()
	lowScores := make([]ragGapObs, 0, 64)
	for _, o := range ix.observations {
		if cutoff > 0 && o.ts < cutoff {
			continue
		}
		if o.score < f.Threshold {
			lowScores = append(lowScores, o)
		}
	}
	resolvedCopy := make(map[string]bool, len(ix.resolved))
	for k, v := range ix.resolved {
		resolvedCopy[k] = v
	}
	ix.mu.RUnlock()

	minSim := f.ClusterMinSim
	if minSim <= 0 {
		// Default tuned for hashed-BoW (the fallback embedder). When
		// callers wire a real sentence-transformer this can climb to ~0.80.
		minSim = 0.50
	}
	clusters := clusterObservations(lowScores, minSim)

	rows := make([]RAGGapRow, 0, len(clusters))
	for _, c := range clusters {
		avg := c.scoreSum / float64(c.n)
		row := RAGGapRow{
			ClusterID:   c.id,
			SampleQuery: c.sampleQuery,
			N:           c.n,
			AvgScore:    avg,
			LastSeen:    c.lastSeen / int64(time.Second),
			Resolved:    resolvedCopy[c.id],
			GapWeight:   float64(c.n) * (f.Threshold - avg),
		}
		rows = append(rows, row)
	}
	// Sort: unresolved first (ship-list priority), then by gap weight desc.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Resolved != rows[j].Resolved {
			return !rows[i].Resolved
		}
		return rows[i].GapWeight > rows[j].GapWeight
	})
	if f.Limit > 0 && len(rows) > f.Limit {
		rows = rows[:f.Limit]
	}
	return rows, nil
}

// RAGGapQueryRow is one row of QUERIES output.
type RAGGapQueryRow struct {
	Query string  `json:"query"`
	Score float64 `json:"score"`
	TS    int64   `json:"ts"`
}

// Queries returns the raw low-score queries (newest first), with no
// clustering. Useful when callers want to write their own grouping.
func (g *RAGGap) Queries(indexID string, threshold float64, limit int) ([]RAGGapQueryRow, bool) {
	if threshold <= 0 {
		threshold = 0.40
	}
	g.mu.RLock()
	ix, ok := g.indexes[indexID]
	g.mu.RUnlock()
	if !ok {
		return nil, false
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]RAGGapQueryRow, 0, 64)
	// Iterate newest-first
	for i := len(ix.observations) - 1; i >= 0; i-- {
		o := ix.observations[i]
		if o.score < threshold {
			out = append(out, RAGGapQueryRow{Query: o.query, Score: o.score, TS: o.ts / int64(time.Second)})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, true
}

// Resolve marks one cluster id as addressed.
func (g *RAGGap) Resolve(indexID, clusterID string) error {
	if indexID == "" || clusterID == "" {
		return errors.New("index_id and cluster_id required")
	}
	g.totalResolves.Add(1)
	ix := g.indexOrCreate(indexID)
	ix.mu.Lock()
	if ix.resolved == nil {
		ix.resolved = map[string]bool{}
	}
	ix.resolved[clusterID] = true
	ix.mu.Unlock()
	return nil
}

// Indexes returns every index id known, sorted.
func (g *RAGGap) Indexes() []string {
	g.mu.RLock()
	out := make([]string, 0, len(g.indexes))
	for k := range g.indexes {
		out = append(out, k)
	}
	g.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops one index's observations + resolved set. indexID="ALL"
// wipes everything.
func (g *RAGGap) Reset(indexID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if indexID == "ALL" {
		n := len(g.indexes)
		g.indexes = map[string]*ragGapIndex{}
		return n
	}
	if _, ok := g.indexes[indexID]; ok {
		delete(g.indexes, indexID)
		return 1
	}
	return 0
}

// RAGGapStats is the global snapshot.
type RAGGapStats struct {
	Indexes       int   `json:"indexes"`
	Observations  int   `json:"observations"`
	TotalObserves int64 `json:"total_observes"`
	TotalReports  int64 `json:"total_reports"`
	TotalResolves int64 `json:"total_resolves"`
	Cap           int   `json:"cap"`
}

func (g *RAGGap) Stats() RAGGapStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	obs := 0
	for _, ix := range g.indexes {
		ix.mu.RLock()
		obs += len(ix.observations)
		ix.mu.RUnlock()
	}
	return RAGGapStats{
		Indexes:       len(g.indexes),
		Observations:  obs,
		TotalObserves: g.totalObserves.Load(),
		TotalReports:  g.totalReports.Load(),
		TotalResolves: g.totalResolves.Load(),
		Cap:           g.cap,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (g *RAGGap) indexOrCreate(id string) *ragGapIndex {
	g.mu.RLock()
	ix, ok := g.indexes[id]
	g.mu.RUnlock()
	if ok {
		return ix
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if ix, ok := g.indexes[id]; ok {
		return ix
	}
	ix = &ragGapIndex{resolved: map[string]bool{}}
	g.indexes[id] = ix
	return ix
}

// ragGapCluster is one in-flight cluster during REPORT.
type ragGapCluster struct {
	id          string
	centroid    []float64
	sampleQuery string
	scoreSum    float64
	n           int
	lastSeen    int64
}

// clusterObservations does a single-pass agglomerative cluster of
// observations: for each obs, assign to the first cluster whose
// centroid has cosine ≥ minSim; create a new cluster otherwise.
// Centroids are running averages (cheap, no normalization required
// since embedFallback already L2-normalises each input).
func clusterObservations(obs []ragGapObs, minSim float64) []ragGapCluster {
	clusters := make([]ragGapCluster, 0, 16)
	for _, o := range obs {
		assigned := false
		for i := range clusters {
			if dotProduct(clusters[i].centroid, o.vec) >= minSim {
				// Update centroid as running average
				inv := 1.0 / float64(clusters[i].n+1)
				for d := range clusters[i].centroid {
					clusters[i].centroid[d] = (clusters[i].centroid[d]*float64(clusters[i].n) + o.vec[d]) * inv
				}
				clusters[i].n++
				clusters[i].scoreSum += o.score
				if o.ts > clusters[i].lastSeen {
					clusters[i].lastSeen = o.ts
				}
				assigned = true
				break
			}
		}
		if assigned {
			continue
		}
		centroid := make([]float64, len(o.vec))
		copy(centroid, o.vec)
		clusters = append(clusters, ragGapCluster{
			id:          clusterID(o.query),
			centroid:    centroid,
			sampleQuery: o.query,
			scoreSum:    o.score,
			n:           1,
			lastSeen:    o.ts,
		})
	}
	return clusters
}

// clusterID is a stable 12-char hash of the cluster's seed query.
// Same first-query → same cluster id, so RESOLVE survives across
// REPORT calls as long as the cluster's seed query is still present.
func clusterID(query string) string {
	h := fnv1a32(query)
	// 8 hex chars
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hex[h&0xF]
		h >>= 4
	}
	return "gap-" + string(out)
}
