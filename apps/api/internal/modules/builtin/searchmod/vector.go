package searchmod

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"sync"
)

// vectorIndex stores per-doc vectors for one VECTOR field. Two
// algorithms are supported:
//
//   FLAT  — stores every vector linearly; KNN scans them all. Exact
//           but O(N) per query. Right pick when N is small (≲ 50k)
//           or when you must guarantee recall = 1.
//
//   HNSW  — Hierarchical Navigable Small World graph. Layered graph
//           with logarithmic search and configurable accuracy/latency
//           trade-off via ef_runtime. Standard choice for ≥ 100k
//           vectors with ANN-quality requirements.
//
// Distance metrics: COSINE (1 - cos similarity), L2 (squared
// euclidean), IP (negative inner product so smaller = more similar).
type vectorIndex struct {
	algo    string
	dim     int
	metric  string
	m       int // HNSW: max graph degree
	efC     int // HNSW: ef_construction
	efR     int // HNSW: ef_runtime

	mu      sync.RWMutex
	vectors map[string][]float32 // by docID
	hnsw    *hnswGraph
}

// newVectorIndex builds an empty index from the schema field spec.
func newVectorIndex(f *FieldSpec) *vectorIndex {
	v := &vectorIndex{
		algo:   f.VectorAlgo,
		dim:    f.VectorDim,
		metric: f.VectorMetric,
		m:      f.VectorM,
		efC:    f.VectorEFConstr,
		efR:    f.VectorEFRun,
		vectors: map[string][]float32{},
	}
	if v.algo == "HNSW" {
		v.hnsw = newHNSW(v.m, v.efC, v.efR, v.metric)
	}
	return v
}

// Set inserts (or replaces) a vector. raw is the field value as
// stored on the document — we accept the FLOAT32 binary form Redis
// uses (`dim * 4` little-endian bytes) plus a comma-separated decimal
// fallback for the playground.
func (v *vectorIndex) Set(docID, raw string) error {
	vec, err := parseVector(raw, v.dim)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.vectors[docID] = vec
	if v.hnsw != nil {
		v.hnsw.insert(docID, vec)
	}
	return nil
}

// Del removes a doc's vector.
func (v *vectorIndex) Del(docID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.vectors, docID)
	if v.hnsw != nil {
		v.hnsw.remove(docID)
	}
}

// KNN returns the k closest docIDs to query, with their distances.
func (v *vectorIndex) KNN(query []float32, k int) []KNNResult {
	if len(query) != v.dim {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.algo == "HNSW" && v.hnsw != nil {
		return v.hnsw.search(query, k)
	}
	// FLAT: brute-force over the entire vector table.
	out := make([]KNNResult, 0, len(v.vectors))
	for id, vec := range v.vectors {
		out = append(out, KNNResult{DocID: id, Distance: distance(query, vec, v.metric)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	if k > 0 && k < len(out) {
		out = out[:k]
	}
	return out
}

// KNNResult is one (docID, distance) pair returned by KNN.
type KNNResult struct {
	DocID    string
	Distance float64
}

// parseVector accepts either the binary FLOAT32 form Redis uses on the
// wire (`dim * 4` little-endian bytes) or the comma-separated decimal
// form ("1.0,2.0,3.0") which is friendlier for the playground.
func parseVector(raw string, dim int) ([]float32, error) {
	if dim <= 0 {
		return nil, errors.New("vector field has no DIM declared")
	}
	if len(raw) == dim*4 {
		out := make([]float32, dim)
		for i := 0; i < dim; i++ {
			bits := binary.LittleEndian.Uint32([]byte(raw[i*4 : (i+1)*4]))
			out[i] = math.Float32frombits(bits)
		}
		return out, nil
	}
	// CSV fallback
	parts := splitVectorCSV(raw)
	if len(parts) != dim {
		return nil, errors.New("vector dimension mismatch")
	}
	out := make([]float32, dim)
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

func splitVectorCSV(s string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		if s[i] == ' ' || s[i] == '\t' {
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// distance computes the appropriate distance metric. We deliberately
// return "smaller is better" for every metric so callers (FLAT, HNSW,
// KNN sort) can use a single comparator.
func distance(a, b []float32, metric string) float64 {
	switch metric {
	case "L2":
		var sum float64
		for i := range a {
			d := float64(a[i] - b[i])
			sum += d * d
		}
		return sum
	case "IP":
		var sum float64
		for i := range a {
			sum += float64(a[i]) * float64(b[i])
		}
		return -sum // negate so smaller = better
	default: // COSINE
		var dot, na, nb float64
		for i := range a {
			dot += float64(a[i]) * float64(b[i])
			na += float64(a[i]) * float64(a[i])
			nb += float64(b[i]) * float64(b[i])
		}
		if na == 0 || nb == 0 {
			return 1
		}
		return 1 - dot/(math.Sqrt(na)*math.Sqrt(nb))
	}
}

// ── HNSW ──────────────────────────────────────────────────────────
//
// Minimal HNSW: each node has a randomly-chosen max layer (geometric
// distribution with parameter `1/ln(M)`); upper layers form a coarse
// graph the search descends. We keep the implementation compact —
// production-grade HNSW has more knobs (mL, heuristic neighbour
// selection, layer-aware connection pruning) but this is the same
// algorithm at the same time/space class.

type hnswGraph struct {
	m       int
	efC     int
	efR     int
	metric  string
	mL      float64

	mu      sync.RWMutex
	nodes   map[string]*hnswNode
	entry   string
	maxLayer int
	rng     *rand.Rand
}

type hnswNode struct {
	id     string
	vec    []float32
	layers [][]string // per-layer neighbour list
}

func newHNSW(m, efC, efR int, metric string) *hnswGraph {
	if m <= 0 {
		m = 16
	}
	if efC <= 0 {
		efC = 200
	}
	if efR <= 0 {
		efR = 10
	}
	return &hnswGraph{
		m: m, efC: efC, efR: efR, metric: metric,
		mL: 1.0 / math.Log(float64(m)),
		nodes: map[string]*hnswNode{},
		rng: rand.New(rand.NewSource(1)),
	}
}

// pickLayer samples the new node's max layer from a geometric
// distribution. Higher M ⇒ shallower expected depth.
func (g *hnswGraph) pickLayer() int {
	r := g.rng.Float64()
	if r == 0 {
		r = 1e-9
	}
	return int(math.Floor(-math.Log(r) * g.mL))
}

// insert adds (id, vec) to the graph. Replaces if id exists.
func (g *hnswGraph) insert(id string, vec []float32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.nodes[id]; ok {
		// Replace: drop old links, re-insert cleanly.
		for _, layer := range old.layers {
			for _, neighbourID := range layer {
				if n, ok := g.nodes[neighbourID]; ok {
					for li, l := range n.layers {
						n.layers[li] = removeStr(l, id)
					}
				}
			}
		}
		delete(g.nodes, id)
		if g.entry == id {
			g.entry = ""
		}
	}
	layer := g.pickLayer()
	node := &hnswNode{id: id, vec: vec, layers: make([][]string, layer+1)}
	g.nodes[id] = node
	if g.entry == "" {
		g.entry = id
		g.maxLayer = layer
		return
	}
	// Search down from the current entry to `layer`.
	cur := g.entry
	for l := g.maxLayer; l > layer; l-- {
		cur = g.greedyDescend(cur, vec, l)
	}
	// At each layer ≤ insertion layer, link to the M closest neighbours.
	for l := min(layer, g.maxLayer); l >= 0; l-- {
		neighbours := g.searchLayer(cur, vec, g.efC, l)
		picked := selectTopK(neighbours, g.m)
		ids := make([]string, len(picked))
		for i, p := range picked {
			ids[i] = p.id
		}
		node.layers[l] = ids
		for _, p := range picked {
			n := g.nodes[p.id]
			n.layers[l] = append(n.layers[l], id)
			if len(n.layers[l]) > g.m {
				// Prune to M nearest.
				cands := make([]heapNode, 0, len(n.layers[l]))
				for _, nid := range n.layers[l] {
					cands = append(cands, heapNode{id: nid, dist: distance(g.nodes[nid].vec, n.vec, g.metric)})
				}
				sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
				kept := make([]string, 0, g.m)
				for i := 0; i < g.m && i < len(cands); i++ {
					kept = append(kept, cands[i].id)
				}
				n.layers[l] = kept
			}
		}
		if len(picked) > 0 {
			cur = picked[0].id
		}
	}
	if layer > g.maxLayer {
		g.maxLayer = layer
		g.entry = id
	}
}

// remove drops (id) from the graph and clears every backlink. Cheap
// because graphs degrade gracefully — neighbours just lose a link.
func (g *hnswGraph) remove(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	old, ok := g.nodes[id]
	if !ok {
		return
	}
	for _, layer := range old.layers {
		for _, nid := range layer {
			if n, ok := g.nodes[nid]; ok {
				for li, l := range n.layers {
					n.layers[li] = removeStr(l, id)
				}
			}
		}
	}
	delete(g.nodes, id)
	if g.entry == id {
		g.entry = ""
		for nid := range g.nodes {
			g.entry = nid
			break
		}
	}
}

// search runs k-NN against the graph.
func (g *hnswGraph) search(query []float32, k int) []KNNResult {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.entry == "" {
		return nil
	}
	cur := g.entry
	for l := g.maxLayer; l > 0; l-- {
		cur = g.greedyDescend(cur, query, l)
	}
	results := g.searchLayer(cur, query, max(g.efR, k), 0)
	picked := selectTopK(results, k)
	out := make([]KNNResult, len(picked))
	for i, p := range picked {
		out[i] = KNNResult{DocID: p.id, Distance: p.dist}
	}
	return out
}

// greedyDescend is the per-layer one-step descent: keep moving to the
// closer neighbour until none beats the current node.
func (g *hnswGraph) greedyDescend(start string, q []float32, layer int) string {
	cur := start
	curDist := distance(g.nodes[cur].vec, q, g.metric)
	for {
		moved := false
		for _, nid := range g.nodes[cur].layers[layer] {
			d := distance(g.nodes[nid].vec, q, g.metric)
			if d < curDist {
				cur = nid
				curDist = d
				moved = true
			}
		}
		if !moved {
			return cur
		}
	}
}

// searchLayer is the standard ef-bounded best-first traversal at a
// single graph layer. Returns the visited set sorted by ascending dist.
func (g *hnswGraph) searchLayer(entry string, q []float32, ef, layer int) []heapNode {
	visited := map[string]struct{}{entry: {}}
	cand := []heapNode{{id: entry, dist: distance(g.nodes[entry].vec, q, g.metric)}}
	results := []heapNode{cand[0]}
	for len(cand) > 0 {
		// pop closest
		sort.Slice(cand, func(i, j int) bool { return cand[i].dist < cand[j].dist })
		top := cand[0]
		cand = cand[1:]
		// stop when the best candidate is worse than the worst kept result.
		if len(results) >= ef && top.dist > worst(results).dist {
			break
		}
		for _, nid := range g.nodes[top.id].layers[layer] {
			if _, seen := visited[nid]; seen {
				continue
			}
			visited[nid] = struct{}{}
			d := distance(g.nodes[nid].vec, q, g.metric)
			cand = append(cand, heapNode{id: nid, dist: d})
			results = append(results, heapNode{id: nid, dist: d})
			if len(results) > ef {
				sort.Slice(results, func(i, j int) bool { return results[i].dist < results[j].dist })
				results = results[:ef]
			}
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].dist < results[j].dist })
	return results
}

func worst(rs []heapNode) heapNode {
	m := rs[0]
	for _, r := range rs[1:] {
		if r.dist > m.dist {
			m = r
		}
	}
	return m
}

func selectTopK(rs []heapNode, k int) []heapNode {
	sort.Slice(rs, func(i, j int) bool { return rs[i].dist < rs[j].dist })
	if k > 0 && k < len(rs) {
		rs = rs[:k]
	}
	return rs
}

type heapNode struct {
	id   string
	dist float64
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func removeStr(xs []string, v string) []string {
	for i, x := range xs {
		if x == v {
			return append(xs[:i], xs[i+1:]...)
		}
	}
	return xs
}
