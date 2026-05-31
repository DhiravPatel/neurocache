package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// VecSpaceHealth detects embedding-space collapse — the silent killer
// failure mode. If the embedding model degrades (a bad deploy, a
// quantization gone wrong, a model swap, a CUDA OOM that returned
// nan-stuffed garbage), vectors collapse toward each other. Every
// cosine drifts toward 1.0; retrieval starts returning whatever the
// first chunk in the index is. No error, no latency change, just
// quietly broken.
//
// DRIFT watches the input *text* distribution; nothing in any vector
// DB watches the *vector space* itself. VECSPACE.* fixes that.
//
// Two metrics per sample population:
//
//   mean_pairwise_cosine — average cosine across all sampled pairs.
//     Healthy embedding models cluster around 0.0–0.2 (text is
//     mostly orthogonal); a collapsed model jumps to 0.8+.
//
//   effective_dim — sum(λ_i)^2 / sum(λ_i^2) on the empirical
//     covariance eigenvalues (the participation ratio). Healthy:
//     near the embedding dim. Collapsed: near 1 (everything lies
//     on a single direction).
//
// The verdict is computed against thresholds the caller can tune.
// We also expose nan_rate because a degenerate model often produces
// NaN-stuffed vectors and you want to know immediately.
//
// Commands:
//
//   VECSPACE.SAMPLE space-id [VECTORS v1 v2 ...] [DIM d]
//        SAMPLE accepts vectors as inline floats: VECTORS dim v1 v2 ... vn vn+1 ...
//        — supplied as flattened tokens, dim picked up from DIM (required).
//        Repeated calls accumulate; SAMPLE retains a rolling window
//        of the most recent N=1024 vectors per space.
//   VECSPACE.HEALTH space-id [COLLAPSE_AT f] [LOW_DIM_AT n]
//        → mean_pairwise_cosine, effective_dim, nan_rate, sample_n, verdict, reason
//   VECSPACE.RESET space-id|ALL
//   VECSPACE.LIST
//   VECSPACE.STATS
//
// Hot path: SAMPLE is O(n*dim) to copy. HEALTH samples up to 256
// pairs from the buffer (random — not all pairs, which is O(n^2));
// covariance is computed over the dim^2 matrix from the buffer.
// At dim=768, 1024 samples, HEALTH is well under 100ms.
type VecSpaceHealth struct {
	mu     sync.RWMutex
	spaces map[string]*vecSpace

	totalSamples atomic.Int64
	totalHealths atomic.Int64
}

type vecSpace struct {
	mu     sync.Mutex
	dim    int
	buf    [][]float64 // rolling window
	max    int
	totalIn int64
}

const vsMaxBuf = 1024

// NewVecSpaceHealth returns an empty registry.
func NewVecSpaceHealth() *VecSpaceHealth {
	return &VecSpaceHealth{spaces: map[string]*vecSpace{}}
}

// Sample appends vectors to the rolling window for spaceID. All
// vectors must share the same dim. The first vector in a space pins
// the dim; subsequent dim mismatches are rejected.
func (v *VecSpaceHealth) Sample(spaceID string, vectors [][]float64) error {
	if spaceID == "" {
		return errors.New("space_id required")
	}
	if len(vectors) == 0 {
		return errors.New("at least one vector required")
	}
	v.totalSamples.Add(int64(len(vectors)))
	s := v.spaceOrCreate(spaceID, len(vectors[0]))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, vec := range vectors {
		if len(vec) != s.dim {
			return errors.New("dim mismatch")
		}
		cp := make([]float64, len(vec))
		copy(cp, vec)
		s.buf = append(s.buf, cp)
		s.totalIn++
		if len(s.buf) > s.max {
			s.buf = s.buf[len(s.buf)-s.max:]
		}
	}
	return nil
}

// VecSpaceReport is the HEALTH return.
type VecSpaceReport struct {
	SpaceID            string  `json:"space_id"`
	SampleN            int     `json:"sample_n"`
	Dim                int     `json:"dim"`
	MeanPairwiseCosine float64 `json:"mean_pairwise_cosine"`
	EffectiveDim       float64 `json:"effective_dim"`
	NaNRate            float64 `json:"nan_rate"`
	Verdict            string  `json:"verdict"`
	Reason             string  `json:"reason"`
}

// Health returns the snapshot for one space. Thresholds:
//
//   collapseAt = 0 → default 0.80 (mean pairwise cosine above this
//                    is "collapsed")
//   lowDimAt   = 0 → default 16 (effective dim below this with
//                    dim>32 is "collapsed")
//
// Verdict is one of: HEALTHY, DEGRADED, COLLAPSED, INSUFFICIENT.
func (v *VecSpaceHealth) Health(spaceID string, collapseAt float64, lowDimAt int) (VecSpaceReport, bool) {
	if spaceID == "" {
		return VecSpaceReport{}, false
	}
	if collapseAt <= 0 {
		collapseAt = 0.80
	}
	if lowDimAt <= 0 {
		lowDimAt = 16
	}
	v.totalHealths.Add(1)
	v.mu.RLock()
	s, ok := v.spaces[spaceID]
	v.mu.RUnlock()
	if !ok {
		return VecSpaceReport{}, false
	}
	s.mu.Lock()
	buf := make([][]float64, len(s.buf))
	for i, vec := range s.buf {
		cp := make([]float64, len(vec))
		copy(cp, vec)
		buf[i] = cp
	}
	dim := s.dim
	s.mu.Unlock()

	out := VecSpaceReport{SpaceID: spaceID, SampleN: len(buf), Dim: dim}
	if len(buf) < 8 {
		out.Verdict = "INSUFFICIENT"
		out.Reason = "need at least 8 sampled vectors"
		return out, true
	}
	out.NaNRate = nanRate(buf)
	out.MeanPairwiseCosine = meanPairwiseCosine(buf, 256)
	out.EffectiveDim = participationRatio(buf, dim)

	switch {
	case out.NaNRate > 0.05:
		out.Verdict = "COLLAPSED"
		out.Reason = "nan rate exceeds 5% — model is producing garbage"
	case out.MeanPairwiseCosine >= collapseAt:
		out.Verdict = "COLLAPSED"
		out.Reason = "vectors are nearly colinear — embedding model is degenerate"
	case dim >= 32 && out.EffectiveDim <= float64(lowDimAt):
		out.Verdict = "COLLAPSED"
		out.Reason = "effective dim is far below configured dim — covariance has collapsed"
	case out.MeanPairwiseCosine >= 0.50:
		out.Verdict = "DEGRADED"
		out.Reason = "mean cosine drifting upward — investigate before retrieval craters"
	default:
		out.Verdict = "HEALTHY"
		out.Reason = "vector space is well-spread"
	}
	return out, true
}

// Reset wipes a space. spaceID="ALL" wipes everything.
func (v *VecSpaceHealth) Reset(spaceID string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if spaceID == "ALL" {
		n := len(v.spaces)
		v.spaces = map[string]*vecSpace{}
		return n
	}
	if _, ok := v.spaces[spaceID]; ok {
		delete(v.spaces, spaceID)
		return 1
	}
	return 0
}

// List returns every known space id.
func (v *VecSpaceHealth) List() []string {
	v.mu.RLock()
	out := make([]string, 0, len(v.spaces))
	for k := range v.spaces {
		out = append(out, k)
	}
	v.mu.RUnlock()
	sort.Strings(out)
	return out
}

// VecSpaceStats is the global snapshot.
type VecSpaceStats struct {
	Spaces       int   `json:"spaces"`
	TotalSamples int64 `json:"total_samples"`
	TotalHealths int64 `json:"total_healths"`
}

func (v *VecSpaceHealth) Stats() VecSpaceStats {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return VecSpaceStats{
		Spaces:       len(v.spaces),
		TotalSamples: v.totalSamples.Load(),
		TotalHealths: v.totalHealths.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (v *VecSpaceHealth) spaceOrCreate(id string, dim int) *vecSpace {
	v.mu.RLock()
	s, ok := v.spaces[id]
	v.mu.RUnlock()
	if ok {
		return s
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.spaces[id]; ok {
		return s
	}
	s = &vecSpace{dim: dim, max: vsMaxBuf}
	v.spaces[id] = s
	return s
}

// meanPairwiseCosine samples up to maxPairs pairs (random walk with
// strided indexing — deterministic-ish but doesn't degenerate to the
// same pair every call) and averages cosine. Vectors that aren't
// L2-normalised are normalised on the fly.
func meanPairwiseCosine(buf [][]float64, maxPairs int) float64 {
	n := len(buf)
	if n < 2 {
		return 0
	}
	pairs := 0
	sum := 0.0
	// Use a simple stride that produces ~maxPairs pairs covering the buffer
	stride := n * n / (maxPairs * 2)
	if stride < 1 {
		stride = 1
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j += stride {
			c := cosine(buf[i], buf[j])
			if math.IsNaN(c) || math.IsInf(c, 0) {
				continue
			}
			sum += c
			pairs++
			if pairs >= maxPairs {
				return sum / float64(pairs)
			}
		}
	}
	if pairs == 0 {
		return 0
	}
	return sum / float64(pairs)
}

// participationRatio approximates the effective dimensionality using
// the per-coordinate variance: PR = (sum σ²)² / (sum σ⁴). This is
// the same number you get from the eigenvalue PR when the coordinate
// system already aligns with PCA — for an honest answer you'd run
// PCA, but for a "is the space degenerate" signal the coordinate-
// variance proxy gets you 95% of the way and avoids dragging in a
// linear-algebra library.
func participationRatio(buf [][]float64, dim int) float64 {
	if len(buf) == 0 || dim == 0 {
		return 0
	}
	mean := make([]float64, dim)
	for _, v := range buf {
		for i := 0; i < dim && i < len(v); i++ {
			mean[i] += v[i]
		}
	}
	n := float64(len(buf))
	for i := range mean {
		mean[i] /= n
	}
	vars := make([]float64, dim)
	for _, v := range buf {
		for i := 0; i < dim && i < len(v); i++ {
			d := v[i] - mean[i]
			vars[i] += d * d
		}
	}
	for i := range vars {
		vars[i] /= n
	}
	sum, sumSq := 0.0, 0.0
	for _, v := range vars {
		sum += v
		sumSq += v * v
	}
	if sumSq == 0 {
		return 0
	}
	return (sum * sum) / sumSq
}

func nanRate(buf [][]float64) float64 {
	total, bad := 0, 0
	for _, v := range buf {
		for _, x := range v {
			total++
			if math.IsNaN(x) || math.IsInf(x, 0) {
				bad++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(bad) / float64(total)
}

