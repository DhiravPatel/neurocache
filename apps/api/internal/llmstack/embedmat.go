package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// EmbedMatrix is an inline embedding matrix with top-K cosine
// search. Lots of teams hit "I want to do top-K cosine over a few
// thousand vectors" — for which spinning up a vector DB (Pinecone /
// Weaviate / Qdrant) is overkill, but rolling cosine in client code
// is slow because you ship every vector across the network for
// every query.
//
// EMBED.MAT.* keeps the matrix in the cache and runs cosine
// server-side:
//
//   EMBED.MAT.SET matrix-id row-id v1,v2,v3,...
//   EMBED.MAT.DEL matrix-id row-id
//   EMBED.MAT.TOPK matrix-id query-vec K [FILTER prefix]
//        -> array of {row_id, score} ordered by score desc
//   EMBED.MAT.DOT matrix-id row-a row-b      -> float64
//   EMBED.MAT.COSINE matrix-id row-a row-b   -> float64
//   EMBED.MAT.LEN matrix-id                  -> int
//   EMBED.MAT.LIST matrix-id [PREFIX p]      -> row-ids
//   EMBED.MAT.FORGET matrix-id
//   EMBED.MAT.STATS                          -> matrices + totals
//
// Vectors are stored L2-normalised on SET so the TOPK hot path
// reduces to a single dot product per row. For 10k rows × 768 dims
// that's 7.6M mul-adds — runs in ~3-5 ms on modern hardware,
// fast enough for interactive search.
//
// Storage: per-matrix RWMutex around a row map. Compaction-free —
// DEL just removes the map entry. Apps that need persistence beyond
// restart use AOF (matrix.SET is in the writeset).
type EmbedMatrix struct {
	mu       sync.RWMutex
	matrices map[string]*embMatrix

	totalSets   atomic.Int64
	totalTopKs  atomic.Int64
	totalRows   atomic.Int64
}

type embMatrix struct {
	rows map[string][]float64 // normalised on insert
	dim  int
}

// NewEmbedMatrix returns an empty matrix registry.
func NewEmbedMatrix() *EmbedMatrix {
	return &EmbedMatrix{matrices: map[string]*embMatrix{}}
}

// Set inserts or replaces a row. First insert into a matrix fixes
// the dimensionality; subsequent inserts must match. Vector is
// L2-normalised in place.
func (m *EmbedMatrix) Set(matrixID, rowID string, vec []float64) error {
	if matrixID == "" || rowID == "" {
		return errors.New("matrix_id and row_id required")
	}
	if len(vec) == 0 {
		return errors.New("vec must be non-empty")
	}
	norm := math.Sqrt(dotProduct(vec, vec))
	if norm == 0 {
		return errors.New("zero-norm vector")
	}
	normVec := make([]float64, len(vec))
	for i, v := range vec {
		normVec[i] = v / norm
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		mat = &embMatrix{rows: map[string][]float64{}, dim: len(vec)}
		m.matrices[matrixID] = mat
	}
	if mat.dim != len(vec) {
		return errors.New("vector dim does not match matrix dim")
	}
	if _, existed := mat.rows[rowID]; !existed {
		m.totalRows.Add(1)
	}
	mat.rows[rowID] = normVec
	m.totalSets.Add(1)
	return nil
}

// Del removes a row.
func (m *EmbedMatrix) Del(matrixID, rowID string) bool {
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

// TopKHit is one row of TOPK output.
type TopKHit struct {
	RowID string  `json:"row_id"`
	Score float64 `json:"score"`
}

// TopKOpts narrows the search.
type TopKOpts struct {
	K      int
	Filter string // row_ids must have this prefix
}

// TopK returns the K rows with highest cosine vs query. Query is
// L2-normalised before the loop so cosine reduces to dot product.
// Empty matrix or dim mismatch returns nil.
func (m *EmbedMatrix) TopK(matrixID string, query []float64, opts TopKOpts) []TopKHit {
	m.totalTopKs.Add(1)
	k := opts.K
	if k <= 0 {
		k = 10
	}
	m.mu.RLock()
	mat, ok := m.matrices[matrixID]
	if !ok || mat.dim != len(query) {
		m.mu.RUnlock()
		return nil
	}
	norm := math.Sqrt(dotProduct(query, query))
	if norm == 0 {
		m.mu.RUnlock()
		return nil
	}
	normQuery := make([]float64, len(query))
	for i, v := range query {
		normQuery[i] = v / norm
	}
	hits := make([]TopKHit, 0, len(mat.rows))
	for id, v := range mat.rows {
		if opts.Filter != "" && !strings.HasPrefix(id, opts.Filter) {
			continue
		}
		hits = append(hits, TopKHit{
			RowID: id,
			Score: dotProduct(normQuery, v),
		})
	}
	m.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Dot returns the dot product of two rows. Both vectors are stored
// normalised so Dot equals Cosine — provided here for API symmetry.
func (m *EmbedMatrix) Dot(matrixID, a, b string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		return 0, false
	}
	va, ok := mat.rows[a]
	if !ok {
		return 0, false
	}
	vb, ok := mat.rows[b]
	if !ok {
		return 0, false
	}
	return dotProduct(va, vb), true
}

// Cosine is the same as Dot (vectors stored normalised). Kept as a
// named entry point for clarity in user-facing examples.
func (m *EmbedMatrix) Cosine(matrixID, a, b string) (float64, bool) {
	return m.Dot(matrixID, a, b)
}

// Len returns the row count of a matrix.
func (m *EmbedMatrix) Len(matrixID string) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		return 0, false
	}
	return len(mat.rows), true
}

// List returns every row_id in a matrix, optionally filtered by prefix.
func (m *EmbedMatrix) List(matrixID, prefix string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mat, ok := m.matrices[matrixID]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(mat.rows))
	for id := range mat.rows {
		if prefix == "" || strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Forget drops a whole matrix. Returns the number of rows removed.
func (m *EmbedMatrix) Forget(matrixID string) int {
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

// MatrixRow is one row of EMBED.MAT.STATS matrices list.
type MatrixRow struct {
	MatrixID string `json:"matrix_id"`
	Rows     int    `json:"rows"`
	Dim      int    `json:"dim"`
}

// EmbedMatStats is the global snapshot.
type EmbedMatStats struct {
	Matrices    []MatrixRow `json:"matrices"`
	TotalSets   int64       `json:"total_sets"`
	TotalTopKs  int64       `json:"total_topks"`
	TotalRows   int64       `json:"total_rows"`
}

func (m *EmbedMatrix) Stats() EmbedMatStats {
	m.mu.RLock()
	rows := make([]MatrixRow, 0, len(m.matrices))
	for id, mat := range m.matrices {
		rows = append(rows, MatrixRow{MatrixID: id, Rows: len(mat.rows), Dim: mat.dim})
	}
	m.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].MatrixID < rows[j].MatrixID })
	return EmbedMatStats{
		Matrices:    rows,
		TotalSets:   m.totalSets.Load(),
		TotalTopKs:  m.totalTopKs.Load(),
		TotalRows:   m.totalRows.Load(),
	}
}
