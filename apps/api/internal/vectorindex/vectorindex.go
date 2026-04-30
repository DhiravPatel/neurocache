// Package vectorindex implements the per-key vector store used by the
// VectorSet first-class type (V* commands). Two algorithms ship:
//
//	FLAT — every vector held in a slice; KNN scans them all. Exact
//	       (recall = 1) but O(N) per query. Right pick for ≲ 50k
//	       vectors or when you need ground-truth recall guarantees.
//
//	HNSW — Hierarchical Navigable Small World graph. Layered graph
//	       with logarithmic search and a configurable accuracy /
//	       latency knob via ef_runtime. Standard for ≥ 100k vectors
//	       with ANN-quality requirements.
//
// Distance metrics: COSINE (1 - cos similarity), L2 (squared
// euclidean), IP (negative inner product, so smaller = more similar
// across all metrics — every method returns "smaller is better" so a
// single comparator can serve all three).
//
// Notes on lock discipline: each method takes the per-index mutex
// once. Callers must NOT hold any other lock that the engine notifier
// also acquires (this would deadlock the keyspace-notification
// fan-out path) — the store wrapper releases the per-Entry lock
// before calling into vectorindex methods.
package vectorindex

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"sync"
)

// Algo enumerates the supported index backends.
type Algo string

const (
	AlgoFlat Algo = "FLAT"
	AlgoHNSW Algo = "HNSW"
)

// Metric enumerates the supported distance functions.
type Metric string

const (
	MetricCosine Metric = "COSINE"
	MetricL2     Metric = "L2"
	MetricIP     Metric = "IP"
)

// Options bundles the per-index parameters surfaced to callers.
type Options struct {
	Algo   Algo
	Dim    int
	Metric Metric
	M      int     // HNSW max graph degree (default 16)
	EFC    int     // HNSW ef_construction (default 200)
	EFR    int     // HNSW ef_runtime (default 10)
}

// Index is the per-key vector store.
type Index struct {
	algo   Algo
	dim    int
	metric Metric
	m      int
	efc    int
	efr    int

	mu      sync.RWMutex
	vectors map[string][]float32
	// hnsw is non-nil only when algo == AlgoHNSW. FLAT indexes use
	// the vectors map directly.
	hnsw *hnswGraph

	// attrs holds optional JSON attribute strings keyed by member ID.
	// Created lazily — most insertions don't carry attributes.
	attrs map[string]string
}

// New builds an Index. Defaults: HNSW with M=16, EFC=200, EFR=10,
// metric=COSINE. Dim is required and must be > 0.
func New(opts Options) (*Index, error) {
	if opts.Dim <= 0 {
		return nil, errors.New("DIM must be positive")
	}
	if opts.Algo == "" {
		opts.Algo = AlgoHNSW
	}
	if opts.Metric == "" {
		opts.Metric = MetricCosine
	}
	if opts.M <= 0 {
		opts.M = 16
	}
	if opts.EFC <= 0 {
		opts.EFC = 200
	}
	if opts.EFR <= 0 {
		opts.EFR = 10
	}
	idx := &Index{
		algo:    opts.Algo,
		dim:     opts.Dim,
		metric:  opts.Metric,
		m:       opts.M,
		efc:     opts.EFC,
		efr:     opts.EFR,
		vectors: map[string][]float32{},
	}
	if opts.Algo == AlgoHNSW {
		idx.hnsw = newHNSW(opts.M, opts.EFC, opts.EFR, opts.Metric)
	}
	return idx, nil
}

// Algo returns the configured backend.
func (i *Index) Algo() Algo { return i.algo }

// Dim returns the configured vector dimension.
func (i *Index) Dim() int { return i.dim }

// Metric returns the configured distance metric.
func (i *Index) Metric() Metric { return i.metric }

// M / EFC / EFR expose the HNSW knobs (zero on a FLAT index).
func (i *Index) M() int   { return i.m }
func (i *Index) EFC() int { return i.efc }
func (i *Index) EFR() int { return i.efr }

// Set inserts or replaces a (id, vec) pair. The vector must be exactly
// Dim() floats long.
func (i *Index) Set(id string, vec []float32) error {
	if len(vec) != i.dim {
		return errors.New("vector dimension mismatch")
	}
	cp := make([]float32, len(vec))
	copy(cp, vec)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.vectors[id] = cp
	if i.hnsw != nil {
		i.hnsw.insert(id, cp)
	}
	return nil
}

// Del removes (id) from both the vector table and any HNSW links.
// Returns whether the id was present.
func (i *Index) Del(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.vectors[id]; !ok {
		return false
	}
	delete(i.vectors, id)
	if i.attrs != nil {
		delete(i.attrs, id)
	}
	if i.hnsw != nil {
		i.hnsw.remove(id)
	}
	return true
}

// Get returns the stored vector for id (zero-copy returned slice; do
// not mutate). ok=false when missing.
func (i *Index) Get(id string) ([]float32, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	v, ok := i.vectors[id]
	return v, ok
}

// SetAttr / GetAttr / DelAttr manage the optional JSON attribute
// blob carried alongside each vector. The store treats the value as
// opaque — callers serialize whatever JSON they like.
func (i *Index) SetAttr(id, json string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.vectors[id]; !ok {
		return false
	}
	if i.attrs == nil {
		i.attrs = map[string]string{}
	}
	i.attrs[id] = json
	return true
}

func (i *Index) GetAttr(id string) (string, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.attrs == nil {
		return "", false
	}
	v, ok := i.attrs[id]
	return v, ok
}

func (i *Index) DelAttr(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.attrs == nil {
		return false
	}
	if _, ok := i.attrs[id]; !ok {
		return false
	}
	delete(i.attrs, id)
	return true
}

// Card returns the vector count.
func (i *Index) Card() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.vectors)
}

// IDs returns every member id in unspecified order. Snapshot — safe
// to iterate without holding the index lock.
func (i *Index) IDs() []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]string, 0, len(i.vectors))
	for id := range i.vectors {
		out = append(out, id)
	}
	return out
}

// Result is one (id, distance) pair from a KNN query. Distance is
// always "smaller = better" regardless of metric (see distance()).
type Result struct {
	ID       string
	Distance float64
}

// KNN returns the k nearest members to query, sorted ascending by
// distance. Empty slice when the index is empty or the query
// dimension is wrong.
func (i *Index) KNN(query []float32, k int) []Result {
	if len(query) != i.dim {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.algo == AlgoHNSW && i.hnsw != nil {
		return i.hnsw.search(query, k)
	}
	out := make([]Result, 0, len(i.vectors))
	for id, vec := range i.vectors {
		out = append(out, Result{ID: id, Distance: distance(query, vec, i.metric)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Distance < out[b].Distance })
	if k > 0 && k < len(out) {
		out = out[:k]
	}
	return out
}

// Links returns the HNSW neighbour ids of (id) at every layer. Empty
// slice on FLAT indexes or when id is not present. The result is
// stable across the call; callers may iterate without holding any
// index lock.
func (i *Index) Links(id string) [][]string {
	if i.hnsw == nil {
		return nil
	}
	return i.hnsw.links(id)
}

// MemUsage approximates the byte cost. Used by VINFO.
func (i *Index) MemUsage() int64 {
	i.mu.RLock()
	defer i.mu.RUnlock()
	per := int64(i.dim*4 + 64) // vector + map overhead
	cost := int64(len(i.vectors)) * per
	if i.attrs != nil {
		for id, v := range i.attrs {
			cost += int64(len(id) + len(v) + 32)
		}
	}
	if i.hnsw != nil {
		cost += int64(len(i.vectors)) * int64(i.m) * 16 // graph links
	}
	return cost
}

// ─── parsers ──────────────────────────────────────────────────────

// ParseVector accepts either the binary FLOAT32 form (`dim * 4`
// little-endian bytes) or the comma-separated decimal form
// ("1.0,2.0,3.0"). The wire form most drivers send is the binary
// FP32; the CSV form is friendly for the playground and tests.
func ParseVector(raw string, dim int) ([]float32, error) {
	if dim <= 0 {
		return nil, errors.New("vector index has no DIM declared")
	}
	if len(raw) == dim*4 {
		out := make([]float32, dim)
		for i := 0; i < dim; i++ {
			bits := binary.LittleEndian.Uint32([]byte(raw[i*4 : (i+1)*4]))
			out[i] = math.Float32frombits(bits)
		}
		return out, nil
	}
	parts := splitCSV(raw)
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

// EncodeVector serialises a vector back to the FP32 binary wire form
// — used by VEMB and the persistence layer.
func EncodeVector(vec []float32) string {
	buf := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return string(buf)
}

func splitCSV(s string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ',':
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		case c == ' ' || c == '\t':
			// skip
		default:
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// distance computes the configured metric. All branches return
// "smaller = better" so the KNN sort needs only one comparator.
func distance(a, b []float32, metric Metric) float64 {
	switch metric {
	case MetricL2:
		var sum float64
		for i := range a {
			d := float64(a[i] - b[i])
			sum += d * d
		}
		return sum
	case MetricIP:
		var sum float64
		for i := range a {
			sum += float64(a[i]) * float64(b[i])
		}
		return -sum
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

// ─── HNSW ─────────────────────────────────────────────────────────

type hnswGraph struct {
	m       int
	efc     int
	efr     int
	metric  Metric
	mL      float64

	mu       sync.RWMutex
	nodes    map[string]*hnswNode
	entry    string
	maxLayer int
	rng      *rand.Rand
}

type hnswNode struct {
	id     string
	vec    []float32
	layers [][]string
}

func newHNSW(m, efc, efr int, metric Metric) *hnswGraph {
	return &hnswGraph{
		m: m, efc: efc, efr: efr, metric: metric,
		mL:    1.0 / math.Log(float64(m)),
		nodes: map[string]*hnswNode{},
		rng:   rand.New(rand.NewSource(1)),
	}
}

func (g *hnswGraph) pickLayer() int {
	r := g.rng.Float64()
	if r == 0 {
		r = 1e-9
	}
	return int(math.Floor(-math.Log(r) * g.mL))
}

func (g *hnswGraph) insert(id string, vec []float32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.nodes[id]; ok {
		// drop old links + reinsert cleanly
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
	cur := g.entry
	for l := g.maxLayer; l > layer; l-- {
		cur = g.greedyDescend(cur, vec, l)
	}
	for l := minInt(layer, g.maxLayer); l >= 0; l-- {
		neighbours := g.searchLayer(cur, vec, g.efc, l)
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
				cands := make([]heapNode, 0, len(n.layers[l]))
				for _, nid := range n.layers[l] {
					cands = append(cands, heapNode{id: nid, dist: distance(g.nodes[nid].vec, n.vec, g.metric)})
				}
				sort.Slice(cands, func(a, b int) bool { return cands[a].dist < cands[b].dist })
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

func (g *hnswGraph) search(query []float32, k int) []Result {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.entry == "" {
		return nil
	}
	cur := g.entry
	for l := g.maxLayer; l > 0; l-- {
		cur = g.greedyDescend(cur, query, l)
	}
	results := g.searchLayer(cur, query, maxInt(g.efr, k), 0)
	picked := selectTopK(results, k)
	out := make([]Result, len(picked))
	for i, p := range picked {
		out[i] = Result{ID: p.id, Distance: p.dist}
	}
	return out
}

func (g *hnswGraph) links(id string) [][]string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	if !ok {
		return nil
	}
	out := make([][]string, len(n.layers))
	for li, layer := range n.layers {
		out[li] = append([]string(nil), layer...)
	}
	return out
}

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

func (g *hnswGraph) searchLayer(entry string, q []float32, ef, layer int) []heapNode {
	visited := map[string]struct{}{entry: {}}
	cand := []heapNode{{id: entry, dist: distance(g.nodes[entry].vec, q, g.metric)}}
	results := []heapNode{cand[0]}
	for len(cand) > 0 {
		sort.Slice(cand, func(i, j int) bool { return cand[i].dist < cand[j].dist })
		top := cand[0]
		cand = cand[1:]
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
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
