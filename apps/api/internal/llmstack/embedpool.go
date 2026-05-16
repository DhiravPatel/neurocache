package llmstack

import (
	"errors"
	"math"
	"sync/atomic"
)

// EmbedPooler is a stateless bulk-pool operation set: given a list
// of per-chunk embeddings, produce a single doc-level embedding
// via mean / max / weighted-mean / normalised-sum aggregation.
// Apps building RAG indexes commonly need this — "I have 14 chunk
// embeddings, give me the doc embedding" — and ship the matrix
// across the network just to compute one cheap mean.
//
// EMBED.POOL.* gives the cache one-roundtrip pooling:
//
//   EMBED.POOL.MEAN v1,...|v1,...|v1,...
//        → comma-separated pooled vector
//   EMBED.POOL.MAX v1,...|v1,...
//   EMBED.POOL.WEIGHTED w1,w2,w3 v1,...|v1,...|v1,...
//   EMBED.POOL.NORM_SUM v1,...|v1,...
//        (sum then L2-normalise — useful when chunk count varies
//         across docs and you want directional similarity)
//   EMBED.POOL.STATS
//
// All operations are O(N·dim), pure compute, no state. Atomic
// counters track per-strategy usage.
type EmbedPooler struct {
	totalMeans    atomic.Int64
	totalMaxes    atomic.Int64
	totalWeighted atomic.Int64
	totalNormSum  atomic.Int64
	totalVecsIn   atomic.Int64
}

// NewEmbedPooler returns a fresh pooler.
func NewEmbedPooler() *EmbedPooler { return &EmbedPooler{} }

// Mean returns the element-wise mean. Empty / dim-mismatched input
// returns an error.
func (e *EmbedPooler) Mean(vecs [][]float64) ([]float64, error) {
	e.totalMeans.Add(1)
	if err := validateVecs(vecs); err != nil {
		return nil, err
	}
	e.totalVecsIn.Add(int64(len(vecs)))
	out := make([]float64, len(vecs[0]))
	for _, v := range vecs {
		for i, x := range v {
			out[i] += x
		}
	}
	n := float64(len(vecs))
	for i := range out {
		out[i] /= n
	}
	return out, nil
}

// Max returns the element-wise max (max pooling — keeps the
// strongest signal per dimension).
func (e *EmbedPooler) Max(vecs [][]float64) ([]float64, error) {
	e.totalMaxes.Add(1)
	if err := validateVecs(vecs); err != nil {
		return nil, err
	}
	e.totalVecsIn.Add(int64(len(vecs)))
	out := make([]float64, len(vecs[0]))
	copy(out, vecs[0])
	for _, v := range vecs[1:] {
		for i, x := range v {
			if x > out[i] {
				out[i] = x
			}
		}
	}
	return out, nil
}

// Weighted returns sum(weights[i] * vecs[i]) / sum(weights).
// Weight count must equal vec count.
func (e *EmbedPooler) Weighted(weights []float64, vecs [][]float64) ([]float64, error) {
	e.totalWeighted.Add(1)
	if err := validateVecs(vecs); err != nil {
		return nil, err
	}
	if len(weights) != len(vecs) {
		return nil, errors.New("weights and vecs length mismatch")
	}
	e.totalVecsIn.Add(int64(len(vecs)))
	out := make([]float64, len(vecs[0]))
	wsum := 0.0
	for i, w := range weights {
		wsum += w
		for j, x := range vecs[i] {
			out[j] += w * x
		}
	}
	if wsum == 0 {
		return nil, errors.New("sum of weights is zero")
	}
	for i := range out {
		out[i] /= wsum
	}
	return out, nil
}

// NormSum returns sum(vecs) / |sum(vecs)| — useful when you want
// the resultant direction without averaging dilution.
func (e *EmbedPooler) NormSum(vecs [][]float64) ([]float64, error) {
	e.totalNormSum.Add(1)
	if err := validateVecs(vecs); err != nil {
		return nil, err
	}
	e.totalVecsIn.Add(int64(len(vecs)))
	out := make([]float64, len(vecs[0]))
	for _, v := range vecs {
		for i, x := range v {
			out[i] += x
		}
	}
	norm := math.Sqrt(dotProduct(out, out))
	if norm == 0 {
		return nil, errors.New("zero-norm result")
	}
	for i := range out {
		out[i] /= norm
	}
	return out, nil
}

// EmbedPoolStats is the global counters snapshot.
type EmbedPoolStats struct {
	TotalMeans    int64 `json:"total_means"`
	TotalMaxes    int64 `json:"total_maxes"`
	TotalWeighted int64 `json:"total_weighted"`
	TotalNormSum  int64 `json:"total_norm_sum"`
	TotalVecsIn   int64 `json:"total_vecs_in"`
}

func (e *EmbedPooler) Stats() EmbedPoolStats {
	return EmbedPoolStats{
		TotalMeans:    e.totalMeans.Load(),
		TotalMaxes:    e.totalMaxes.Load(),
		TotalWeighted: e.totalWeighted.Load(),
		TotalNormSum:  e.totalNormSum.Load(),
		TotalVecsIn:   e.totalVecsIn.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func validateVecs(vecs [][]float64) error {
	if len(vecs) == 0 {
		return errors.New("at least one vector required")
	}
	dim := len(vecs[0])
	if dim == 0 {
		return errors.New("vectors must be non-empty")
	}
	for i, v := range vecs[1:] {
		if len(v) != dim {
			return errors.New("dim mismatch at index " + itoa(i+1))
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
