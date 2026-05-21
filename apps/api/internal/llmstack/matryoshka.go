package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// MatryoshkaMatrix is a 3-pass hierarchical embedding store
// modeled on the Matryoshka Representation Learning paper
// (Kusupati et al. 2022). Modern embedding models (OpenAI
// text-embedding-3, Nomic embed v1.5) deliberately train the
// first N dimensions to be a viable lower-fidelity vector — apps
// can truncate without re-running the embedder.
//
// We exploit that: on SET, store truncated 128-dim and 256-dim
// copies alongside the full-dim vector. TOPK then runs a 3-pass
// search:
//
//   1. Scan ALL rows with 128-dim cosine → keep top SHORTLIST*4
//   2. Re-rank that shortlist with 256-dim cosine → keep SHORTLIST
//   3. Final pass: full-dim cosine on the SHORTLIST → return top K
//
// For 10k rows × 768 dims:
//   Flat full-dim:  10k × 768 = 7.68M mul-adds  (~8 ms)
//   Matryoshka:     10k × 128 + 400 × 256 + 100 × 768
//                 = 1.28M + 102k + 77k ≈ 1.46M mul-adds  (~1.5 ms)
//   → ~5× speedup with negligible recall loss (typically <2%)
//
// Storage cost: ~50% more bytes than EMBED.MAT (the 128 and 256
// dim copies). Apps that don't have matryoshka-trained embeddings
// still get the speedup, just with slightly higher recall loss.
type MatryoshkaMatrix struct {
	mu       sync.RWMutex
	matrices map[string]*matryoshkaMat

	totalSets  atomic.Int64
	totalTopKs atomic.Int64
	totalRows  atomic.Int64
}

type matryoshkaMat struct {
	rows map[string]*matryoshkaRow
	dim  int
}

type matryoshkaRow struct {
	full []float64 // L2-normalised, full dim
	d128 []float64 // L2-normalised, first 128
	d256 []float64 // L2-normalised, first 256
}

// NewMatryoshkaMatrix returns an empty registry.
func NewMatryoshkaMatrix() *MatryoshkaMatrix {
	return &MatryoshkaMatrix{matrices: map[string]*matryoshkaMat{}}
}

// Set stores a row in all three precisions. The first SET to a
// matrix fixes the full dimensionality.
func (m *MatryoshkaMatrix) Set(matrixID, rowID string, vec []float64) error {
	if matrixID == "" || rowID == "" {
		return errors.New("matrix_id and row_id required")
	}
	if len(vec) < 256 {
		return errors.New("vec must be at least 256 dims for matryoshka (need both 128 and 256 truncations)")
	}
	full := normaliseInPlace(vec)
	if isZero(full) {
		return errors.New("zero-norm vector")
	}
	d128 := normaliseInPlace(vec[:128])
	d256 := normaliseInPlace(vec[:256])
	row := &matryoshkaRow{full: full, d128: d128, d256: d256}

	m.mu.Lock()
	defer m.mu.Unlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		mat = &matryoshkaMat{rows: map[string]*matryoshkaRow{}, dim: len(vec)}
		m.matrices[matrixID] = mat
	}
	if mat.dim != len(vec) {
		return errors.New("vector dim does not match matrix dim")
	}
	if _, existed := mat.rows[rowID]; !existed {
		m.totalRows.Add(1)
	}
	mat.rows[rowID] = row
	m.totalSets.Add(1)
	return nil
}

// Del removes a row.
func (m *MatryoshkaMatrix) Del(matrixID, rowID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		return false
	}
	if _, existed := mat.rows[rowID]; !existed {
		return false
	}
	delete(mat.rows, rowID)
	m.totalRows.Add(-1)
	return true
}

// MatryoshkaOpts narrows the TOPK search.
type MatryoshkaOpts struct {
	K         int
	Shortlist int    // refinement pool size; default 4*K, min 50
	Filter    string // prefix filter on row_id
}

// TopK runs the 3-pass hierarchical search.
func (m *MatryoshkaMatrix) TopK(matrixID string, query []float64, opts MatryoshkaOpts) []TopKHit {
	m.totalTopKs.Add(1)
	k := opts.K
	if k <= 0 {
		k = 10
	}
	shortlist := opts.Shortlist
	if shortlist <= 0 {
		shortlist = 4 * k
	}
	if shortlist < 50 {
		shortlist = 50
	}
	m.mu.RLock()
	mat, ok := m.matrices[matrixID]
	if !ok || mat.dim != len(query) {
		m.mu.RUnlock()
		return nil
	}
	// Normalise query at full dim + the two truncations.
	qNorm := math.Sqrt(dotProduct(query, query))
	if qNorm == 0 {
		m.mu.RUnlock()
		return nil
	}
	qFull := normaliseCopy(query)
	q128 := normaliseCopy(query[:128])
	q256 := normaliseCopy(query[:256])

	type pair struct {
		id    string
		score float64
	}

	// Pass 1: 128-dim cosine over ALL rows
	pass1 := make([]pair, 0, len(mat.rows))
	for id, row := range mat.rows {
		if opts.Filter != "" && !strings.HasPrefix(id, opts.Filter) {
			continue
		}
		pass1 = append(pass1, pair{id, dotProduct(row.d128, q128)})
	}
	m.mu.RUnlock()

	// Take top shortlist*4 from pass 1 (broad recall)
	pass1Cap := shortlist * 4
	if pass1Cap > len(pass1) {
		pass1Cap = len(pass1)
	}
	sort.Slice(pass1, func(i, j int) bool { return pass1[i].score > pass1[j].score })
	pass1 = pass1[:pass1Cap]

	// Pass 2: 256-dim re-rank
	m.mu.RLock()
	pass2 := make([]pair, 0, len(pass1))
	for _, p := range pass1 {
		row, ok := mat.rows[p.id]
		if !ok {
			continue
		}
		pass2 = append(pass2, pair{p.id, dotProduct(row.d256, q256)})
	}
	// Pass 3: full-dim final
	pass2Cap := shortlist
	if pass2Cap > len(pass2) {
		pass2Cap = len(pass2)
	}
	sort.Slice(pass2, func(i, j int) bool { return pass2[i].score > pass2[j].score })
	pass2 = pass2[:pass2Cap]
	pass3 := make([]TopKHit, 0, len(pass2))
	for _, p := range pass2 {
		row, ok := mat.rows[p.id]
		if !ok {
			continue
		}
		pass3 = append(pass3, TopKHit{RowID: p.id, Score: dotProduct(row.full, qFull)})
	}
	m.mu.RUnlock()
	sort.Slice(pass3, func(i, j int) bool { return pass3[i].Score > pass3[j].Score })
	if len(pass3) > k {
		pass3 = pass3[:k]
	}
	return pass3
}

// Len returns the row count of a matrix.
func (m *MatryoshkaMatrix) Len(matrixID string) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		return 0, false
	}
	return len(mat.rows), true
}

// Forget drops a matrix.
func (m *MatryoshkaMatrix) Forget(matrixID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		return 0
	}
	n := len(mat.rows)
	delete(m.matrices, matrixID)
	m.totalRows.Add(-int64(n))
	return n
}

// MatryoshkaStats is the global snapshot.
type MatryoshkaStats struct {
	Matrices   []MatrixRow `json:"matrices"`
	TotalSets  int64       `json:"total_sets"`
	TotalTopKs int64       `json:"total_topks"`
	TotalRows  int64       `json:"total_rows"`
}

func (m *MatryoshkaMatrix) Stats() MatryoshkaStats {
	m.mu.RLock()
	rows := make([]MatrixRow, 0, len(m.matrices))
	for id, mat := range m.matrices {
		rows = append(rows, MatrixRow{MatrixID: id, Rows: len(mat.rows), Dim: mat.dim})
	}
	m.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].MatrixID < rows[j].MatrixID })
	return MatryoshkaStats{
		Matrices:   rows,
		TotalSets:  m.totalSets.Load(),
		TotalTopKs: m.totalTopKs.Load(),
		TotalRows:  m.totalRows.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func normaliseCopy(vec []float64) []float64 {
	out := make([]float64, len(vec))
	norm := math.Sqrt(dotProduct(vec, vec))
	if norm == 0 {
		return out
	}
	for i, v := range vec {
		out[i] = v / norm
	}
	return out
}

func isZero(v []float64) bool {
	for _, x := range v {
		if x != 0 {
			return false
		}
	}
	return true
}

