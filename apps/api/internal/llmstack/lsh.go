package llmstack

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
)

// LSHIndex is a random-hyperplane Locality-Sensitive Hashing index.
// For datasets of 100k+ vectors, even EMBED.MAT.TOPK (linear cosine
// scan) gets slow. LSH buckets vectors by their signature so
// near-duplicate detection becomes O(1 + bucket size) instead of
// O(N).
//
// How it works: at index creation, we generate K random hyperplanes
// in the embedding space. A vector's "signature" is a K-bit string
// where bit i = sign(dot(vector, hyperplane_i)). Vectors that are
// close in cosine space land in the same or nearby buckets with
// high probability.
//
// Commands:
//
//   HASH.LSH.CREATE bucket-id dim [BITS k]
//        Initialise an LSH bucket with k random hyperplanes
//        (default 16 → 65k possible signatures).
//   HASH.LSH.SET bucket-id row-id v,v,v,...
//   HASH.LSH.DEL bucket-id row-id
//   HASH.LSH.SIGN bucket-id v,v,v,...      → signature as hex
//   HASH.LSH.NEIGHBORS bucket-id v,v,v,... [RADIUS r] [K k]
//        → top-K cosine-ranked hits from candidate buckets within
//          Hamming radius r (default 1) of the query's signature.
//   HASH.LSH.LEN bucket-id
//   HASH.LSH.FORGET bucket-id
//   HASH.LSH.STATS
//
// Storage: per-bucket {hyperplanes [K][dim]float64,
//                       rows: signature → list of (row_id, vec)}.
// Bucket count: 2^K (16 bits = 65k slots, sparse).
//
// Recall-vs-speed knob: higher BITS = better selectivity but
// smaller buckets (faster scan, lower recall); lower BITS =
// larger buckets (slower scan, higher recall). Default 16 is a
// solid trade for typical embedding workloads.
type LSHIndex struct {
	mu      sync.RWMutex
	buckets map[string]*lshBucket

	totalSets      atomic.Int64
	totalNeighbors atomic.Int64
	totalRows      atomic.Int64
}

type lshBucket struct {
	mu          sync.RWMutex
	dim         int
	bits        int
	hyperplanes [][]float64       // [bits][dim], pre-normalised
	rows        map[string]*lshRow
	bySignature map[uint64][]string // signature → row_ids
}

type lshRow struct {
	vec       []float64 // L2-normalised
	signature uint64
}

// NewLSHIndex returns an empty index registry.
func NewLSHIndex() *LSHIndex {
	return &LSHIndex{buckets: map[string]*lshBucket{}}
}

// Create initialises a new bucket. Bits must be 1..64 (uint64
// signature). Default 16.
func (l *LSHIndex) Create(bucketID string, dim, bits int) error {
	if bucketID == "" {
		return errors.New("bucket_id required")
	}
	if dim < 1 {
		return errors.New("dim must be positive")
	}
	if bits <= 0 {
		bits = 16
	}
	if bits > 64 {
		return errors.New("bits must be <= 64")
	}
	rng := rand.New(rand.NewSource(int64(len(bucketID)) ^ int64(dim*1000)))
	hyperplanes := make([][]float64, bits)
	for i := 0; i < bits; i++ {
		h := make([]float64, dim)
		for j := range h {
			h[j] = rng.NormFloat64()
		}
		// Normalize so sign(dot) is consistent
		norm := math.Sqrt(dotProduct(h, h))
		if norm > 0 {
			for j := range h {
				h[j] /= norm
			}
		}
		hyperplanes[i] = h
	}
	l.mu.Lock()
	l.buckets[bucketID] = &lshBucket{
		dim:         dim,
		bits:        bits,
		hyperplanes: hyperplanes,
		rows:        map[string]*lshRow{},
		bySignature: map[uint64][]string{},
	}
	l.mu.Unlock()
	return nil
}

// Set inserts (or replaces) a row. Vector dim must match the bucket.
func (l *LSHIndex) Set(bucketID, rowID string, vec []float64) error {
	if rowID == "" {
		return errors.New("row_id required")
	}
	l.mu.RLock()
	b, ok := l.buckets[bucketID]
	l.mu.RUnlock()
	if !ok {
		return errors.New("unknown bucket_id: " + bucketID)
	}
	if len(vec) != b.dim {
		return errors.New("vector dim does not match bucket dim")
	}
	norm := math.Sqrt(dotProduct(vec, vec))
	if norm == 0 {
		return errors.New("zero-norm vector")
	}
	normVec := make([]float64, len(vec))
	for i, v := range vec {
		normVec[i] = v / norm
	}
	sig := b.computeSig(normVec)
	b.mu.Lock()
	defer b.mu.Unlock()
	// If row already exists, remove from old signature bucket first
	if existing, ok := b.rows[rowID]; ok {
		old := b.bySignature[existing.signature]
		for i, id := range old {
			if id == rowID {
				b.bySignature[existing.signature] = append(old[:i], old[i+1:]...)
				break
			}
		}
	} else {
		l.totalRows.Add(1)
	}
	b.rows[rowID] = &lshRow{vec: normVec, signature: sig}
	b.bySignature[sig] = append(b.bySignature[sig], rowID)
	l.totalSets.Add(1)
	return nil
}

// Del removes a row.
func (l *LSHIndex) Del(bucketID, rowID string) bool {
	l.mu.RLock()
	b, ok := l.buckets[bucketID]
	l.mu.RUnlock()
	if !ok {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	row, ok := b.rows[rowID]
	if !ok {
		return false
	}
	delete(b.rows, rowID)
	old := b.bySignature[row.signature]
	for i, id := range old {
		if id == rowID {
			b.bySignature[row.signature] = append(old[:i], old[i+1:]...)
			break
		}
	}
	if len(b.bySignature[row.signature]) == 0 {
		delete(b.bySignature, row.signature)
	}
	l.totalRows.Add(-1)
	return true
}

// Sign returns the signature for a vector (hex-encoded uint64).
func (l *LSHIndex) Sign(bucketID string, vec []float64) (uint64, bool) {
	l.mu.RLock()
	b, ok := l.buckets[bucketID]
	l.mu.RUnlock()
	if !ok {
		return 0, false
	}
	if len(vec) != b.dim {
		return 0, false
	}
	norm := math.Sqrt(dotProduct(vec, vec))
	if norm == 0 {
		return 0, false
	}
	normVec := make([]float64, len(vec))
	for i, v := range vec {
		normVec[i] = v / norm
	}
	return b.computeSig(normVec), true
}

// NeighborsOpts configures NEIGHBORS.
type NeighborsOpts struct {
	K      int
	Radius int // Hamming distance; default 1
}

// Neighbors returns the top-K cosine-ranked rows from any bucket
// within Hamming distance Radius of the query signature. Default
// K=10, Radius=1.
func (l *LSHIndex) Neighbors(bucketID string, query []float64, opts NeighborsOpts) []TopKHit {
	l.totalNeighbors.Add(1)
	k := opts.K
	if k <= 0 {
		k = 10
	}
	radius := opts.Radius
	if radius < 0 {
		radius = 1
	}
	l.mu.RLock()
	b, ok := l.buckets[bucketID]
	l.mu.RUnlock()
	if !ok {
		return nil
	}
	if len(query) != b.dim {
		return nil
	}
	norm := math.Sqrt(dotProduct(query, query))
	if norm == 0 {
		return nil
	}
	normQuery := make([]float64, len(query))
	for i, v := range query {
		normQuery[i] = v / norm
	}
	qSig := b.computeSig(normQuery)

	// Gather candidate row_ids from buckets within Hamming radius
	candidates := make([]string, 0, 64)
	b.mu.RLock()
	for sig, ids := range b.bySignature {
		if hammingDist(sig, qSig) <= uint8(radius) {
			candidates = append(candidates, ids...)
		}
	}
	// Compute exact cosine for each candidate
	hits := make([]TopKHit, 0, len(candidates))
	for _, id := range candidates {
		row, ok := b.rows[id]
		if !ok {
			continue
		}
		hits = append(hits, TopKHit{RowID: id, Score: dotProduct(row.vec, normQuery)})
	}
	b.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Len returns the row count in a bucket.
func (l *LSHIndex) Len(bucketID string) (int, bool) {
	l.mu.RLock()
	b, ok := l.buckets[bucketID]
	l.mu.RUnlock()
	if !ok {
		return 0, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.rows), true
}

// Forget drops a bucket entirely.
func (l *LSHIndex) Forget(bucketID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[bucketID]
	if !ok {
		return 0
	}
	n := len(b.rows)
	delete(l.buckets, bucketID)
	l.totalRows.Add(-int64(n))
	return n
}

// LSHBucketRow is one row of HASH.LSH.STATS buckets.
type LSHBucketRow struct {
	BucketID       string  `json:"bucket_id"`
	Rows           int     `json:"rows"`
	Dim            int     `json:"dim"`
	Bits           int     `json:"bits"`
	OccupiedSigs   int     `json:"occupied_signatures"`
	AvgPerBucket   float64 `json:"avg_rows_per_bucket"`
}

// LSHStats is the global snapshot.
type LSHStats struct {
	Buckets        []LSHBucketRow `json:"buckets"`
	TotalSets      int64          `json:"total_sets"`
	TotalNeighbors int64          `json:"total_neighbors"`
	TotalRows      int64          `json:"total_rows"`
}

func (l *LSHIndex) Stats() LSHStats {
	l.mu.RLock()
	rows := make([]LSHBucketRow, 0, len(l.buckets))
	for id, b := range l.buckets {
		b.mu.RLock()
		avgPer := 0.0
		if len(b.bySignature) > 0 {
			avgPer = float64(len(b.rows)) / float64(len(b.bySignature))
		}
		rows = append(rows, LSHBucketRow{
			BucketID:     id,
			Rows:         len(b.rows),
			Dim:          b.dim,
			Bits:         b.bits,
			OccupiedSigs: len(b.bySignature),
			AvgPerBucket: avgPer,
		})
		b.mu.RUnlock()
	}
	l.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].BucketID < rows[j].BucketID })
	return LSHStats{
		Buckets:        rows,
		TotalSets:      l.totalSets.Load(),
		TotalNeighbors: l.totalNeighbors.Load(),
		TotalRows:      l.totalRows.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (b *lshBucket) computeSig(normVec []float64) uint64 {
	var sig uint64
	for i, h := range b.hyperplanes {
		if dotProduct(normVec, h) >= 0 {
			sig |= 1 << uint(i)
		}
	}
	return sig
}

// hammingDist returns the number of differing bits.
func hammingDist(a, b uint64) uint8 {
	return uint8(popcount64(a ^ b))
}

// popcount64 counts set bits using SWAR.
func popcount64(x uint64) int {
	x = x - ((x >> 1) & 0x5555555555555555)
	x = (x & 0x3333333333333333) + ((x >> 2) & 0x3333333333333333)
	x = (x + (x >> 4)) & 0x0f0f0f0f0f0f0f0f
	return int((x * 0x0101010101010101) >> 56)
}
