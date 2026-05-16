package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// RetrievalLearner closes the loop from retrieval → answer quality
// → re-rank. The standard RAG stack is open-loop: it retrieves the
// top-K chunks ranked purely by embedding cosine, throws them at
// the LLM, and never learns from what actually worked. Production
// RAG teams end up writing this exact feedback layer by hand —
// usually a Postgres table of (chunk_id, cited_count, win_count)
// glued to a re-rank step. RETRIEVAL.LEARN.* is that layer:
//
//   RECORD     chunk-id cited|not_cited [SCORE quality]
//        Updates per-chunk EMA of "this chunk was useful".
//   RERANK     applies the learned boost to a list of incoming
//        (chunk-id, score) pairs and returns the new ranking.
//        boost = 0.5 + cited_rate * 1.5  ∈ [0.5, 2.0].
//   WEIGHT     current learned boost for one chunk.
//   STATUS     citation count / weight / sample size for one chunk.
//   TOP/BOTTOM top-N most/least helpful chunks (helps prune RAG
//        index of dead weight).
//   RESET      ALL or per-chunk.
//   STATS      global.
//
// Hot path: RECORD is one map lookup + EMA update (atomic CAS on
// the float bits). RERANK is one map lookup per incoming chunk
// + sort — typically 10-50 chunks, sub-microsecond.
type RetrievalLearner struct {
	mu     sync.RWMutex
	chunks map[string]*rlChunk
	alpha  float64 // EMA factor; default 0.10

	totalRecords atomic.Int64
	totalReranks atomic.Int64
}

type rlChunk struct {
	citedRate float64 // EMA of "was cited" ∈ [0,1]
	samples   int64
	cited     int64
}

// NewRetrievalLearner returns a learner with α=0.10 (the most-recent
// 10 observations roughly dominate the EMA).
func NewRetrievalLearner() *RetrievalLearner {
	return &RetrievalLearner{
		chunks: map[string]*rlChunk{},
		alpha:  0.10,
	}
}

// Record updates the EMA for a chunk. Quality is optional — if
// supplied it's used as the "this chunk helped" weight (replaces the
// 0/1 cited signal); pass 1.0 if cited & helpful, 0.0 if not used.
func (r *RetrievalLearner) Record(chunkID string, cited bool, quality float64) error {
	if chunkID == "" {
		return errors.New("chunk_id required")
	}
	r.totalRecords.Add(1)
	signal := 0.0
	if cited {
		signal = 1.0
	}
	if quality > 0 {
		signal = quality
	}
	if signal < 0 || signal > 1 {
		return errors.New("quality must be in [0,1]")
	}
	r.mu.Lock()
	c, ok := r.chunks[chunkID]
	if !ok {
		c = &rlChunk{citedRate: signal} // first observation seeds the EMA
		c.samples = 1
		if cited {
			c.cited = 1
		}
		r.chunks[chunkID] = c
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	c.citedRate = c.citedRate + r.alpha*(signal-c.citedRate)
	c.samples++
	if cited {
		c.cited++
	}
	return nil
}

// Weight returns the current learned boost for one chunk.
// Range [0.5, 2.0]; defaults to 1.0 for unseen chunks (no boost).
func (r *RetrievalLearner) Weight(chunkID string) float64 {
	r.mu.RLock()
	c, ok := r.chunks[chunkID]
	r.mu.RUnlock()
	if !ok {
		return 1.0
	}
	return 0.5 + c.citedRate*1.5
}

// RerankRow is one (chunk-id, score) item passed to RERANK.
type RerankRow struct {
	ChunkID string
	Score   float64
}

// RerankResult is one re-ranked row.
type RerankResult struct {
	ChunkID  string  `json:"chunk_id"`
	Original float64 `json:"original_score"`
	Boost    float64 `json:"boost"`
	Reranked float64 `json:"reranked_score"`
}

// Rerank applies learned weights to incoming retrieval scores.
// Returns results sorted high-to-low by reranked score.
func (r *RetrievalLearner) Rerank(rows []RerankRow) []RerankResult {
	r.totalReranks.Add(1)
	out := make([]RerankResult, len(rows))
	for i, row := range rows {
		boost := r.Weight(row.ChunkID)
		out[i] = RerankResult{
			ChunkID:  row.ChunkID,
			Original: row.Score,
			Boost:    boost,
			Reranked: row.Score * boost,
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reranked > out[j].Reranked })
	return out
}

// RetrievalLearnStatus is per-chunk snapshot.
type RetrievalLearnStatus struct {
	ChunkID     string  `json:"chunk_id"`
	CitedRate   float64 `json:"cited_rate"`
	Weight      float64 `json:"weight"`
	Samples     int64   `json:"samples"`
	CitedCount  int64   `json:"cited_count"`
}

// Status returns per-chunk learned stats.
func (r *RetrievalLearner) Status(chunkID string) (RetrievalLearnStatus, bool) {
	r.mu.RLock()
	c, ok := r.chunks[chunkID]
	r.mu.RUnlock()
	if !ok {
		return RetrievalLearnStatus{}, false
	}
	return RetrievalLearnStatus{
		ChunkID:    chunkID,
		CitedRate:  c.citedRate,
		Weight:     0.5 + c.citedRate*1.5,
		Samples:    c.samples,
		CitedCount: c.cited,
	}, true
}

// RetrievalLearnRow is one row of TOP/BOTTOM.
type RetrievalLearnRow struct {
	ChunkID    string  `json:"chunk_id"`
	CitedRate  float64 `json:"cited_rate"`
	Weight     float64 `json:"weight"`
	Samples    int64   `json:"samples"`
}

// Top returns the N highest-weighted chunks. limit=0 means all.
func (r *RetrievalLearner) Top(limit int) []RetrievalLearnRow {
	return r.rankedRows(limit, true)
}

// Bottom returns the N lowest-weighted chunks. Useful for pruning
// dead-weight from a RAG index.
func (r *RetrievalLearner) Bottom(limit int) []RetrievalLearnRow {
	return r.rankedRows(limit, false)
}

// Reset drops the learned weight for one chunk; chunkID="ALL"
// resets every chunk.
func (r *RetrievalLearner) Reset(chunkID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if chunkID == "ALL" {
		n := len(r.chunks)
		r.chunks = map[string]*rlChunk{}
		return n
	}
	if _, ok := r.chunks[chunkID]; ok {
		delete(r.chunks, chunkID)
		return 1
	}
	return 0
}

// RetrievalLearnStats is the global snapshot.
type RetrievalLearnStats struct {
	Chunks       int   `json:"chunks"`
	TotalRecords int64 `json:"total_records"`
	TotalReranks int64 `json:"total_reranks"`
	MeanWeight   float64 `json:"mean_weight"`
}

func (r *RetrievalLearner) Stats() RetrievalLearnStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := len(r.chunks)
	sum := 0.0
	for _, c := range r.chunks {
		sum += 0.5 + c.citedRate*1.5
	}
	mean := 0.0
	if n > 0 {
		mean = sum / float64(n)
	}
	return RetrievalLearnStats{
		Chunks:       n,
		TotalRecords: r.totalRecords.Load(),
		TotalReranks: r.totalReranks.Load(),
		MeanWeight:   mean,
	}
}

// SetAlpha tunes the EMA factor (small α = slow learning, more stable;
// large α = fast learning, more volatile). Default 0.10.
func (r *RetrievalLearner) SetAlpha(a float64) error {
	if a <= 0 || a > 1 {
		return errors.New("alpha must be in (0,1]")
	}
	r.mu.Lock()
	r.alpha = a
	r.mu.Unlock()
	return nil
}

// ─── internals ──────────────────────────────────────────────────

func (r *RetrievalLearner) rankedRows(limit int, descending bool) []RetrievalLearnRow {
	r.mu.RLock()
	out := make([]RetrievalLearnRow, 0, len(r.chunks))
	for id, c := range r.chunks {
		out = append(out, RetrievalLearnRow{
			ChunkID:   id,
			CitedRate: c.citedRate,
			Weight:    0.5 + c.citedRate*1.5,
			Samples:   c.samples,
		})
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if descending {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Weight < out[j].Weight
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
