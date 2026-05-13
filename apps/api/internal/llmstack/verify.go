package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// VerifyManager implements self-consistency consensus over N model
// samples. For high-stakes outputs — medical, legal, code — running
// the same query 5 times and returning the consensus is dramatically
// more reliable than trusting any single sample. The technique is
// well-known in the literature ("Self-Consistency Improves Chain of
// Thought Reasoning", Wang et al. 2022) but every team rebuilds the
// voting + confidence-scoring machinery from scratch.
//
// VERIFY.* gives the cache a single command set:
//
//   VERIFY.SAMPLE query-id sample [TAGS ...] — record one sample
//   VERIFY.CONSENSUS query-id [STRATEGY exact|medoid|cluster]
//                                          -> consensus + confidence
//   VERIFY.SAMPLES query-id                -> all samples
//   VERIFY.FORGET query-id
//   VERIFY.STATS
//
// Three strategies, each O(n²) on sample count which is fine because
// n is typically 5-15:
//
//   - exact:    bucket by exact string match. Confidence = max/total.
//               Use for short structured outputs (yes/no, JSON, math).
//   - medoid:   tokenize each sample, find the one with highest
//               average token-Jaccard to all others (the "medoid").
//               Confidence = avg-Jaccard-to-medoid. Use for prose.
//   - cluster:  hierarchical-ish bucketing by cosine over hashed-BoW
//               vectors. Returns the largest cluster's medoid +
//               cluster_share as confidence. Use when samples vary
//               widely in surface form but share semantics.
//
// All three return: chosen sample text, confidence ∈ [0,1], a
// breakdown of bucket/cluster sizes for telemetry.
type VerifyManager struct {
	mu      sync.RWMutex
	queries map[string]*verifyQuery

	totalSamples atomic.Int64
	totalConsensus atomic.Int64
}

type verifyQuery struct {
	samples []verifySample
}

type verifySample struct {
	text string
	tags []string
}

// NewVerifyManager returns an empty manager.
func NewVerifyManager() *VerifyManager {
	return &VerifyManager{queries: map[string]*verifyQuery{}}
}

// AddSample records one sample under query_id. Append-only — same
// text twice is recorded twice (that's the point: counts matter).
func (v *VerifyManager) AddSample(queryID, text string, tags []string) error {
	if queryID == "" {
		return errors.New("query_id required")
	}
	if text == "" {
		return errors.New("sample text required")
	}
	v.totalSamples.Add(1)
	v.mu.Lock()
	defer v.mu.Unlock()
	q, ok := v.queries[queryID]
	if !ok {
		q = &verifyQuery{}
		v.queries[queryID] = q
	}
	q.samples = append(q.samples, verifySample{text: text, tags: tags})
	return nil
}

// BucketRow is one bucket in the consensus breakdown.
type BucketRow struct {
	Sample string `json:"sample"`
	Count  int    `json:"count"`
	Share  float64 `json:"share"`
}

// ConsensusResult is VERIFY.CONSENSUS return.
type ConsensusResult struct {
	QueryID    string      `json:"query_id"`
	Strategy   string      `json:"strategy"`
	Chosen     string      `json:"chosen"`
	Confidence float64     `json:"confidence"`
	SampleN    int         `json:"sample_n"`
	Buckets    []BucketRow `json:"buckets,omitempty"`
}

// Consensus returns the consensus + confidence for a query. Strategy
// defaults to "exact".
func (v *VerifyManager) Consensus(queryID, strategy string) (ConsensusResult, bool) {
	v.mu.RLock()
	q, ok := v.queries[queryID]
	v.mu.RUnlock()
	if !ok {
		return ConsensusResult{}, false
	}
	if strategy == "" {
		strategy = "exact"
	}
	v.totalConsensus.Add(1)

	switch strings.ToLower(strategy) {
	case "exact":
		return consensusExact(queryID, q.samples), true
	case "medoid":
		return consensusMedoid(queryID, q.samples), true
	case "cluster":
		return consensusCluster(queryID, q.samples), true
	default:
		return ConsensusResult{
			QueryID: queryID, Strategy: strategy,
			Confidence: 0,
		}, true
	}
}

// Samples returns every recorded sample for a query, in insertion order.
func (v *VerifyManager) Samples(queryID string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	q, ok := v.queries[queryID]
	if !ok {
		return nil
	}
	out := make([]string, len(q.samples))
	for i, s := range q.samples {
		out[i] = s.text
	}
	return out
}

// Forget drops a query (samples + everything).
func (v *VerifyManager) Forget(queryID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.queries[queryID]
	delete(v.queries, queryID)
	return ok
}

// VerifyStats is the global counters snapshot.
type VerifyStats struct {
	TotalSamples  int64 `json:"total_samples"`
	TotalConsensus int64 `json:"total_consensus"`
	Queries       int   `json:"queries"`
}

func (v *VerifyManager) Stats() VerifyStats {
	v.mu.RLock()
	n := len(v.queries)
	v.mu.RUnlock()
	return VerifyStats{
		TotalSamples:  v.totalSamples.Load(),
		TotalConsensus: v.totalConsensus.Load(),
		Queries:       n,
	}
}

// ─── strategies ────────────────────────────────────────────────

func consensusExact(qid string, samples []verifySample) ConsensusResult {
	res := ConsensusResult{
		QueryID: qid, Strategy: "exact", SampleN: len(samples),
	}
	if len(samples) == 0 {
		return res
	}
	counts := map[string]int{}
	for _, s := range samples {
		counts[strings.TrimSpace(s.text)]++
	}
	max := 0
	chosen := ""
	for s, c := range counts {
		if c > max {
			max = c
			chosen = s
		}
	}
	res.Chosen = chosen
	res.Confidence = float64(max) / float64(len(samples))
	// Buckets sorted desc by count for telemetry
	type kv struct {
		s string
		c int
	}
	pairs := make([]kv, 0, len(counts))
	for s, c := range counts {
		pairs = append(pairs, kv{s, c})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].c > pairs[j].c })
	for _, p := range pairs {
		res.Buckets = append(res.Buckets, BucketRow{
			Sample: p.s, Count: p.c,
			Share: float64(p.c) / float64(len(samples)),
		})
	}
	return res
}

func consensusMedoid(qid string, samples []verifySample) ConsensusResult {
	res := ConsensusResult{
		QueryID: qid, Strategy: "medoid", SampleN: len(samples),
	}
	n := len(samples)
	if n == 0 {
		return res
	}
	if n == 1 {
		res.Chosen = samples[0].text
		res.Confidence = 1.0
		return res
	}
	bags := make([]map[string]struct{}, n)
	for i, s := range samples {
		bags[i] = ngramBag(s.text)
	}
	bestIdx := 0
	bestAvg := -1.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			sum += jaccard(bags[i], bags[j])
		}
		avg := sum / float64(n-1)
		if avg > bestAvg {
			bestAvg = avg
			bestIdx = i
		}
	}
	res.Chosen = samples[bestIdx].text
	res.Confidence = bestAvg
	return res
}

func consensusCluster(qid string, samples []verifySample) ConsensusResult {
	res := ConsensusResult{
		QueryID: qid, Strategy: "cluster", SampleN: len(samples),
	}
	n := len(samples)
	if n == 0 {
		return res
	}
	// Embed each sample (hashed-BoW fallback).
	vecs := make([][]float64, n)
	for i, s := range samples {
		vecs[i] = embedFallback(s.text)
	}
	// Greedy single-link clustering: walk samples, each joins the
	// existing cluster whose centroid has cosine ≥ 0.6, else starts
	// a new cluster.
	type cluster struct {
		members []int
		centroid []float64
	}
	clusters := []*cluster{}
	const threshold = 0.6
	for i, v := range vecs {
		best := -1
		bestSim := threshold
		for ci, c := range clusters {
			s := cosine(v, c.centroid)
			if s >= bestSim {
				bestSim = s
				best = ci
			}
		}
		if best == -1 {
			clusters = append(clusters, &cluster{
				members:  []int{i},
				centroid: copySlice(v),
			})
		} else {
			c := clusters[best]
			c.members = append(c.members, i)
			// Update centroid (mean)
			for k := range c.centroid {
				c.centroid[k] = (c.centroid[k]*float64(len(c.members)-1) + v[k]) / float64(len(c.members))
			}
		}
	}
	// Pick the largest cluster.
	bestC := clusters[0]
	for _, c := range clusters {
		if len(c.members) > len(bestC.members) {
			bestC = c
		}
	}
	// Inside the winning cluster, pick the medoid (sample with
	// highest avg cosine to others in the cluster).
	bestIdx := bestC.members[0]
	if len(bestC.members) > 1 {
		bestAvg := -1.0
		for _, mi := range bestC.members {
			sum := 0.0
			for _, mj := range bestC.members {
				if mi == mj {
					continue
				}
				sum += cosine(vecs[mi], vecs[mj])
			}
			avg := sum / float64(len(bestC.members)-1)
			if avg > bestAvg {
				bestAvg = avg
				bestIdx = mi
			}
		}
	}
	res.Chosen = samples[bestIdx].text
	res.Confidence = float64(len(bestC.members)) / float64(n)
	// Cluster-size breakdown for telemetry
	for _, c := range clusters {
		sample := ""
		if len(c.members) > 0 {
			sample = samples[c.members[0]].text
		}
		res.Buckets = append(res.Buckets, BucketRow{
			Sample: sample,
			Count:  len(c.members),
			Share:  float64(len(c.members)) / float64(n),
		})
	}
	sort.Slice(res.Buckets, func(i, j int) bool {
		return res.Buckets[i].Count > res.Buckets[j].Count
	})
	return res
}

func copySlice(s []float64) []float64 {
	out := make([]float64, len(s))
	copy(out, s)
	return out
}
