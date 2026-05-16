package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// VecQuantMatrix is an int8-quantized embedding matrix. Trades a
// tiny bit of recall for dramatic memory and compute wins:
//
//   - Memory:  float64 = 8 bytes/dim, int8 = 1 byte/dim → 8× smaller
//   - Compute: int8 ops are 2-4× faster than float64 on modern
//              CPUs (SIMD-friendly, smaller cache footprint).
//
// Quantization scheme: per-vector symmetric absolute-max scaling.
// For each input vector, compute scale = max(|vec|) / 127, then
// quantized[i] = round(vec[i] / scale). Cosine similarity uses
// pre-stored norm² (computed at SET time on the original vector).
//
// Empirical recall: typical embedding workloads see <0.5% recall
// loss at top-10 vs. full-precision search — well within noise.
//
// Commands mirror EMBED.MAT.* exactly, just with the int8 backend:
//
//   VEC.QUANT.SET matrix-id row-id v,v,v,...
//   VEC.QUANT.DEL matrix-id row-id
//   VEC.QUANT.TOPK matrix-id query-vec K [FILTER prefix]
//   VEC.QUANT.COSINE matrix-id row-a row-b
//   VEC.QUANT.LEN matrix-id
//   VEC.QUANT.FORGET matrix-id
//   VEC.QUANT.STATS
//
// Storage: per-row {int8 vector, float64 scale, float64 norm}.
// At 768 dims the row is 768 + 16 = 784 bytes vs 6144 for float64.
type VecQuantMatrix struct {
	mu       sync.RWMutex
	matrices map[string]*quantMat

	totalSets  atomic.Int64
	totalTopKs atomic.Int64
	totalRows  atomic.Int64
}

type quantMat struct {
	rows map[string]*quantRow
	dim  int
}

type quantRow struct {
	q     []int8  // dim entries
	scale float64 // per-vector scale (so q[i] * scale ≈ original[i])
	norm  float64 // L2 norm of the ORIGINAL (pre-quant) vector
}

// NewVecQuantMatrix returns an empty registry.
func NewVecQuantMatrix() *VecQuantMatrix {
	return &VecQuantMatrix{matrices: map[string]*quantMat{}}
}

// Set quantizes vec to int8 and stores it. First insert per matrix
// fixes the dim.
func (v *VecQuantMatrix) Set(matrixID, rowID string, vec []float64) error {
	if matrixID == "" || rowID == "" {
		return errors.New("matrix_id and row_id required")
	}
	if len(vec) == 0 {
		return errors.New("vec must be non-empty")
	}
	absMax := 0.0
	for _, x := range vec {
		a := math.Abs(x)
		if a > absMax {
			absMax = a
		}
	}
	if absMax == 0 {
		return errors.New("zero-norm vector")
	}
	scale := absMax / 127
	q := make([]int8, len(vec))
	for i, x := range vec {
		r := math.Round(x / scale)
		if r > 127 {
			r = 127
		} else if r < -127 {
			r = -127
		}
		q[i] = int8(r)
	}
	norm := math.Sqrt(dotProduct(vec, vec))

	v.mu.Lock()
	defer v.mu.Unlock()
	mat, ok := v.matrices[matrixID]
	if !ok {
		mat = &quantMat{rows: map[string]*quantRow{}, dim: len(vec)}
		v.matrices[matrixID] = mat
	}
	if mat.dim != len(vec) {
		return errors.New("vector dim does not match matrix dim")
	}
	if _, existed := mat.rows[rowID]; !existed {
		v.totalRows.Add(1)
	}
	mat.rows[rowID] = &quantRow{q: q, scale: scale, norm: norm}
	v.totalSets.Add(1)
	return nil
}

// Del removes a row.
func (v *VecQuantMatrix) Del(matrixID, rowID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	mat, ok := v.matrices[matrixID]
	if !ok {
		return false
	}
	if _, existed := mat.rows[rowID]; !existed {
		return false
	}
	delete(mat.rows, rowID)
	v.totalRows.Add(-1)
	return true
}

// TopK runs int8-vs-int8 dot products. Returns top K by cosine.
func (v *VecQuantMatrix) TopK(matrixID string, query []float64, opts TopKOpts) []TopKHit {
	v.totalTopKs.Add(1)
	k := opts.K
	if k <= 0 {
		k = 10
	}
	v.mu.RLock()
	mat, ok := v.matrices[matrixID]
	if !ok || mat.dim != len(query) {
		v.mu.RUnlock()
		return nil
	}
	// Quantize the query the same way (per-query scale)
	qAbsMax := 0.0
	for _, x := range query {
		a := math.Abs(x)
		if a > qAbsMax {
			qAbsMax = a
		}
	}
	if qAbsMax == 0 {
		v.mu.RUnlock()
		return nil
	}
	qScale := qAbsMax / 127
	qq := make([]int8, len(query))
	for i, x := range query {
		r := math.Round(x / qScale)
		if r > 127 {
			r = 127
		} else if r < -127 {
			r = -127
		}
		qq[i] = int8(r)
	}
	qNorm := math.Sqrt(dotProduct(query, query))
	if qNorm == 0 {
		v.mu.RUnlock()
		return nil
	}
	hits := make([]TopKHit, 0, len(mat.rows))
	for id, row := range mat.rows {
		if opts.Filter != "" && !strings.HasPrefix(id, opts.Filter) {
			continue
		}
		// int8 dot product into int32 (avoid overflow at 768 dims)
		var dot int32
		for i := range qq {
			dot += int32(qq[i]) * int32(row.q[i])
		}
		// Reconstruct cosine: (dot * scale_a * scale_b) / (norm_a * norm_b)
		cos := float64(dot) * qScale * row.scale / (qNorm * row.norm)
		hits = append(hits, TopKHit{RowID: id, Score: cos})
	}
	v.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Cosine returns cosine of two stored rows.
func (v *VecQuantMatrix) Cosine(matrixID, a, b string) (float64, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	mat, ok := v.matrices[matrixID]
	if !ok {
		return 0, false
	}
	ra, ok := mat.rows[a]
	if !ok {
		return 0, false
	}
	rb, ok := mat.rows[b]
	if !ok {
		return 0, false
	}
	var dot int32
	for i := range ra.q {
		dot += int32(ra.q[i]) * int32(rb.q[i])
	}
	return float64(dot) * ra.scale * rb.scale / (ra.norm * rb.norm), true
}

// Len returns the row count of a matrix.
func (v *VecQuantMatrix) Len(matrixID string) (int, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	mat, ok := v.matrices[matrixID]
	if !ok {
		return 0, false
	}
	return len(mat.rows), true
}

// Forget drops a whole matrix. Returns rows removed.
func (v *VecQuantMatrix) Forget(matrixID string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	mat, ok := v.matrices[matrixID]
	if !ok {
		return 0
	}
	n := len(mat.rows)
	delete(v.matrices, matrixID)
	v.totalRows.Add(-int64(n))
	return n
}

// VecQuantStats is the global snapshot.
type VecQuantStats struct {
	Matrices   []MatrixRow `json:"matrices"`
	TotalSets  int64       `json:"total_sets"`
	TotalTopKs int64       `json:"total_topks"`
	TotalRows  int64       `json:"total_rows"`
	BytesPerRowSample int  `json:"bytes_per_row_sample"` // for the largest matrix
}

func (v *VecQuantMatrix) Stats() VecQuantStats {
	v.mu.RLock()
	rows := make([]MatrixRow, 0, len(v.matrices))
	largestDim := 0
	for id, mat := range v.matrices {
		rows = append(rows, MatrixRow{MatrixID: id, Rows: len(mat.rows), Dim: mat.dim})
		if mat.dim > largestDim {
			largestDim = mat.dim
		}
	}
	v.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].MatrixID < rows[j].MatrixID })
	return VecQuantStats{
		Matrices:          rows,
		TotalSets:         v.totalSets.Load(),
		TotalTopKs:        v.totalTopKs.Load(),
		TotalRows:         v.totalRows.Load(),
		BytesPerRowSample: largestDim + 16, // int8 array + scale + norm
	}
}
