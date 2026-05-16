package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// VectorAudit guards the back door no one talks about: adversarial
// vectors inserted into a RAG index.
//
// CONTEXT.SCAN catches malicious instructions in retrieved *text*.
// VEC.AUDIT catches the more sophisticated case: a vector inserted
// into the index that is *engineered* to match a wide range of user
// queries. The text inside might look innocuous; the vector's
// geometry is the attack — it sits near the centroid of the embedding
// space, so it scores high on almost every retrieval and silently
// inserts the attacker's content into the LLM's context.
//
// Two complementary signals:
//
//   centroid-distance — embeddings should fall within a "shell" of
//     normal radius around the index centroid. A vector sitting *too
//     close* to the centroid (lower distance than the bottom 5% of
//     normal samples) is suspicious — it's been optimized to match
//     everything.
//
//   query-affinity    — track recent query vectors. A poisoning vector
//     has unusually high mean cosine to many recent queries. If
//     mean_cosine_to_top_queries exceeds threshold, flag.
//
// Commands:
//
//   VEC.AUDIT.BASELINE index-id v1 v2 ...
//        Seed normal baseline samples. Computes centroid + the 5th
//        percentile of cluster-distance as the "too central" floor.
//   VEC.AUDIT.ADDQUERY index-id v
//        Record one recent query vector (rolling cap).
//   VEC.AUDIT.CHECK index-id v
//        → {verdict, anomaly_score, centroid_distance,
//           top_query_affinity, baseline_size, reason}
//        verdict: stable | warning | poison | no_baseline
//   VEC.AUDIT.STATUS index-id
//   VEC.AUDIT.LIST
//   VEC.AUDIT.SETCAP n         per-index query buffer cap (default 500)
//   VEC.AUDIT.RESET index-id|ALL
//   VEC.AUDIT.STATS
//
// Hot path: CHECK is one dot product to centroid + one dot product
// per stored query (cap=500 default). On 128-dim L2-normalised
// vectors that's single-digit microseconds.
type VectorAudit struct {
	mu      sync.RWMutex
	indexes map[string]*vecAuditIndex
	cap     int

	totalChecks    atomic.Int64
	totalPoisons   atomic.Int64
	totalQueries   atomic.Int64
}

type vecAuditIndex struct {
	mu              sync.RWMutex
	centroid        []float64
	baselineSize    int
	minHealthyDist  float64 // 5th-percentile distance to centroid in baseline
	maxHealthyDist  float64 // 95th-percentile
	queries         [][]float64
}

// NewVectorAudit returns an empty auditor.
func NewVectorAudit() *VectorAudit {
	return &VectorAudit{
		indexes: map[string]*vecAuditIndex{},
		cap:     500,
	}
}

// SetCap adjusts the per-index recent-query buffer.
func (a *VectorAudit) SetCap(n int) {
	a.mu.Lock()
	a.cap = n
	a.mu.Unlock()
}

// Baseline seeds the auditor from K known-good vectors. Replaces any
// existing baseline. The centroid is the mean of the L2-normalised
// inputs; the healthy distance band is the 5th–95th percentile of
// distance-to-centroid across the baseline.
func (a *VectorAudit) Baseline(indexID string, vecs [][]float64) error {
	if indexID == "" {
		return errors.New("index_id required")
	}
	if len(vecs) < 5 {
		return errors.New("baseline requires at least 5 vectors")
	}
	dim := len(vecs[0])
	if dim == 0 {
		return errors.New("baseline vectors must be non-empty")
	}
	for _, v := range vecs {
		if len(v) != dim {
			return errors.New("all baseline vectors must share dimension")
		}
	}
	// Compute centroid + L2 normalise it
	centroid := make([]float64, dim)
	for _, v := range vecs {
		for i, x := range v {
			centroid[i] += x
		}
	}
	for i := range centroid {
		centroid[i] /= float64(len(vecs))
	}
	l2NormaliseInPlace(centroid)
	// Per-baseline-vector distances (1 - cosine to centroid)
	dists := make([]float64, len(vecs))
	for i, v := range vecs {
		cos := safeCosine(centroid, v)
		dists[i] = 1.0 - cos
	}
	sort.Float64s(dists)
	idx := a.indexOrCreate(indexID)
	idx.mu.Lock()
	idx.centroid = centroid
	idx.baselineSize = len(vecs)
	idx.minHealthyDist = percentile(dists, 0.05)
	idx.maxHealthyDist = percentile(dists, 0.95)
	idx.mu.Unlock()
	return nil
}

// AddQuery records one recent query vector (rolling window).
func (a *VectorAudit) AddQuery(indexID string, vec []float64) error {
	if indexID == "" {
		return errors.New("index_id required")
	}
	if len(vec) == 0 {
		return errors.New("query vector required")
	}
	a.totalQueries.Add(1)
	idx := a.indexOrCreate(indexID)
	cp := make([]float64, len(vec))
	copy(cp, vec)
	l2NormaliseInPlace(cp)
	idx.mu.Lock()
	idx.queries = append(idx.queries, cp)
	if a.cap > 0 && len(idx.queries) > a.cap {
		idx.queries = idx.queries[1:]
	}
	idx.mu.Unlock()
	return nil
}

// VecAuditResult is CHECK's return.
type VecAuditResult struct {
	IndexID            string  `json:"index_id"`
	Verdict            string  `json:"verdict"`               // stable | warning | poison | no_baseline
	AnomalyScore       float64 `json:"anomaly_score"`         // 0..1
	CentroidDistance   float64 `json:"centroid_distance"`     // 1 - cosine
	TopQueryAffinity   float64 `json:"top_query_affinity"`    // mean cosine of top-K queries
	BaselineSize       int     `json:"baseline_size"`
	Reason             string  `json:"reason,omitempty"`
}

// Check scores a candidate vector against the index's baseline +
// recent queries.
func (a *VectorAudit) Check(indexID string, vec []float64) (VecAuditResult, error) {
	if indexID == "" {
		return VecAuditResult{}, errors.New("index_id required")
	}
	if len(vec) == 0 {
		return VecAuditResult{}, errors.New("vector required")
	}
	a.totalChecks.Add(1)
	a.mu.RLock()
	idx, ok := a.indexes[indexID]
	a.mu.RUnlock()
	if !ok {
		return VecAuditResult{IndexID: indexID, Verdict: "no_baseline"}, nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := VecAuditResult{IndexID: indexID, BaselineSize: idx.baselineSize}
	if idx.centroid == nil {
		out.Verdict = "no_baseline"
		return out, nil
	}
	if len(vec) != len(idx.centroid) {
		out.Verdict = "warning"
		out.Reason = "dimension mismatch with baseline"
		out.AnomalyScore = 1.0
		return out, nil
	}
	cp := make([]float64, len(vec))
	copy(cp, vec)
	l2NormaliseInPlace(cp)
	cos := safeCosine(idx.centroid, cp)
	out.CentroidDistance = 1.0 - cos

	// Signal 1: distance too low → suspiciously central
	tooCentral := out.CentroidDistance < idx.minHealthyDist*0.5
	// Signal 2: distance too high → outlier (not poisoning per se,
	// but worth a warning — caller can decide). Additive margin
	// of 0.3 beyond the 95th-percentile, since multiplicative
	// breaks near the natural upper bound of cosine distance.
	tooFar := out.CentroidDistance > idx.maxHealthyDist+0.3

	// Signal 3: query affinity — mean cosine to top-K queries.
	const topK = 10
	queryAffinity := 0.0
	if len(idx.queries) > 0 {
		sims := make([]float64, len(idx.queries))
		for i, q := range idx.queries {
			sims[i] = safeCosine(cp, q)
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(sims)))
		k := topK
		if k > len(sims) {
			k = len(sims)
		}
		var sum float64
		for i := 0; i < k; i++ {
			sum += sims[i]
		}
		queryAffinity = sum / float64(k)
	}
	out.TopQueryAffinity = queryAffinity

	// Composite anomaly score and verdict
	score := 0.0
	reasons := make([]string, 0, 3)
	if tooCentral {
		score += 0.50
		reasons = append(reasons, "vector sits suspiciously close to index centroid")
	}
	if tooFar {
		score += 0.35
		reasons = append(reasons, "vector is an outlier beyond healthy distance shell")
	}
	if queryAffinity > 0.80 {
		score += 0.40
		reasons = append(reasons, "high mean cosine to top recent queries")
	} else if queryAffinity > 0.65 {
		score += 0.20
		reasons = append(reasons, "elevated mean cosine to top recent queries")
	}
	if score > 1 {
		score = 1
	}
	out.AnomalyScore = score
	out.Reason = joinReasons(reasons)
	switch {
	case score >= 0.60:
		out.Verdict = "poison"
		a.totalPoisons.Add(1)
	case score >= 0.30:
		out.Verdict = "warning"
	default:
		out.Verdict = "stable"
	}
	return out, nil
}

// VecAuditStatus is the per-index snapshot.
type VecAuditStatus struct {
	IndexID         string  `json:"index_id"`
	BaselineSize    int     `json:"baseline_size"`
	MinHealthyDist  float64 `json:"min_healthy_dist"`
	MaxHealthyDist  float64 `json:"max_healthy_dist"`
	QueryBufferSize int     `json:"query_buffer_size"`
}

// Status returns the per-index snapshot.
func (a *VectorAudit) Status(indexID string) (VecAuditStatus, bool) {
	a.mu.RLock()
	idx, ok := a.indexes[indexID]
	a.mu.RUnlock()
	if !ok {
		return VecAuditStatus{}, false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return VecAuditStatus{
		IndexID:         indexID,
		BaselineSize:    idx.baselineSize,
		MinHealthyDist:  idx.minHealthyDist,
		MaxHealthyDist:  idx.maxHealthyDist,
		QueryBufferSize: len(idx.queries),
	}, true
}

// List returns every index id, sorted.
func (a *VectorAudit) List() []string {
	a.mu.RLock()
	out := make([]string, 0, len(a.indexes))
	for k := range a.indexes {
		out = append(out, k)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops an index. indexID="ALL" wipes everything.
func (a *VectorAudit) Reset(indexID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if indexID == "ALL" {
		n := len(a.indexes)
		a.indexes = map[string]*vecAuditIndex{}
		return n
	}
	if _, ok := a.indexes[indexID]; ok {
		delete(a.indexes, indexID)
		return 1
	}
	return 0
}

// VecAuditStats is the global snapshot.
type VecAuditStats struct {
	Indexes       int   `json:"indexes"`
	TotalChecks   int64 `json:"total_checks"`
	TotalPoisons  int64 `json:"total_poisons_detected"`
	TotalQueries  int64 `json:"total_queries"`
	Cap           int   `json:"cap"`
}

func (a *VectorAudit) Stats() VecAuditStats {
	a.mu.RLock()
	n := len(a.indexes)
	cap := a.cap
	a.mu.RUnlock()
	return VecAuditStats{
		Indexes:      n,
		TotalChecks:  a.totalChecks.Load(),
		TotalPoisons: a.totalPoisons.Load(),
		TotalQueries: a.totalQueries.Load(),
		Cap:          cap,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (a *VectorAudit) indexOrCreate(id string) *vecAuditIndex {
	a.mu.RLock()
	idx, ok := a.indexes[id]
	a.mu.RUnlock()
	if ok {
		return idx
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx, ok := a.indexes[id]; ok {
		return idx
	}
	idx = &vecAuditIndex{}
	a.indexes[id] = idx
	return idx
}

func l2NormaliseInPlace(v []float64) {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return
	}
	norm := math.Sqrt(sum)
	for i := range v {
		v[i] /= norm
	}
}

func safeCosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// percentile returns p-quantile of a *sorted* slice. p in [0,1].
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func joinReasons(rs []string) string {
	if len(rs) == 0 {
		return ""
	}
	var b []byte
	for i, r := range rs {
		if i > 0 {
			b = append(b, '|')
		}
		b = append(b, r...)
	}
	return string(b)
}
