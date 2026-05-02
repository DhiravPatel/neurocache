// Package retrieval implements hybrid lexical+semantic search — the
// production-grade upgrade over a pure cosine `SEMANTIC_GET`. Each
// index pairs a BM25 inverted index (lexical) with the HNSW vector
// index (dense). Queries hit both arms in parallel, then fuse with
// Reciprocal Rank Fusion (RRF) so a single score reflects "this doc
// matched on terms AND/OR meaning" — the consensus pattern from the
// retrieval literature (Cormack et al, 2009; widely deployed in
// production search stacks).
//
// Why hybrid: pure-vector misses exact strings (model numbers, names,
// rare terms), while pure-BM25 misses paraphrases. Real RAG pipelines
// always do both and fuse.
//
// We deliberately ship Okapi BM25 + RRF + an optional cross-encoder
// rerank hook (caller supplies the model) rather than a single dense
// score — this is the industry default for retrieval quality.
package retrieval

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// Document is one indexed item — caller-supplied id, the text we
// tokenize for BM25 + embed for vectors, and an opaque metadata map
// passed back on hits (filtering by tag, tenant, etc.).
type Document struct {
	ID       string            `json:"id"`
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata,omitempty"`
	AddedAt  time.Time         `json:"added_at"`
}

// Hit is one ranked document with its fused score plus the
// contributing lexical / dense ranks (useful for debugging "why did
// this match?" in the dashboard).
type Hit struct {
	ID         string            `json:"id"`
	Text       string            `json:"text"`
	Score      float64           `json:"score"`
	BM25Rank   int               `json:"bm25_rank,omitempty"` // 1-indexed; 0 = absent
	VectorRank int               `json:"vector_rank,omitempty"`
	BM25Score  float64           `json:"bm25_score,omitempty"`
	VectorDist float64           `json:"vector_dist,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Options bundles per-index tuning knobs.
type Options struct {
	Dim       int
	K1        float64 // BM25 term-frequency saturation (default 1.2)
	B         float64 // BM25 length-normalization (default 0.75)
	HNSW      bool    // dense backend uses HNSW; falls back to FLAT scan if false
	Stopwords []string
}

// Index is one named hybrid retrieval index.
type Index struct {
	mu        sync.RWMutex
	dim       int
	k1, b     float64
	stopwords map[string]struct{}

	docs map[string]*Document // id → doc

	// BM25 inverted index: term → posting list of (docID, term-freq).
	postings map[string]map[string]int
	// docLen / avgLen are needed for BM25's length-normalization term.
	docLen map[string]int
	totLen int64

	dense *vectorindex.Index
}

var defaultStopwords = []string{
	"a", "an", "and", "are", "as", "at", "be", "but", "by", "for",
	"if", "in", "into", "is", "it", "no", "not", "of", "on", "or",
	"such", "that", "the", "their", "then", "there", "these", "they",
	"this", "to", "was", "will", "with",
}

// New creates an index. Dim is required for the dense arm; everything
// else has reasonable defaults.
func New(opts Options) (*Index, error) {
	if opts.Dim <= 0 {
		opts.Dim = 384
	}
	if opts.K1 <= 0 {
		opts.K1 = 1.2
	}
	if opts.B < 0 || opts.B > 1 {
		opts.B = 0.75
	}
	algo := vectorindex.AlgoHNSW
	if !opts.HNSW {
		algo = vectorindex.AlgoFlat
	}
	dense, err := vectorindex.New(vectorindex.Options{
		Algo:   algo,
		Dim:    opts.Dim,
		Metric: vectorindex.MetricCosine,
	})
	if err != nil {
		return nil, err
	}
	stop := make(map[string]struct{}, len(defaultStopwords)+len(opts.Stopwords))
	for _, w := range defaultStopwords {
		stop[w] = struct{}{}
	}
	for _, w := range opts.Stopwords {
		stop[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
	return &Index{
		dim:       opts.Dim,
		k1:        opts.K1,
		b:         opts.B,
		stopwords: stop,
		docs:      map[string]*Document{},
		postings:  map[string]map[string]int{},
		docLen:    map[string]int{},
		dense:     dense,
	}, nil
}

// Add upserts a document. Re-adding the same id replaces the prior
// version cleanly (postings + vector both refreshed).
func (ix *Index) Add(doc Document) error {
	if doc.ID == "" {
		return errors.New("document id is required")
	}
	doc.Text = strings.TrimSpace(doc.Text)
	if doc.Text == "" {
		return errors.New("document text is required")
	}
	if doc.AddedAt.IsZero() {
		doc.AddedAt = time.Now()
	}
	terms := tokenize(doc.Text, ix.stopwords)

	ix.mu.Lock()
	if old, exists := ix.docs[doc.ID]; exists {
		// Remove prior postings before re-indexing.
		oldTerms := tokenize(old.Text, ix.stopwords)
		for _, t := range oldTerms {
			if pl, ok := ix.postings[t]; ok {
				delete(pl, doc.ID)
				if len(pl) == 0 {
					delete(ix.postings, t)
				}
			}
		}
		ix.totLen -= int64(ix.docLen[doc.ID])
	}
	ix.docs[doc.ID] = &doc
	tf := termFreq(terms)
	for term, n := range tf {
		pl, ok := ix.postings[term]
		if !ok {
			pl = map[string]int{}
			ix.postings[term] = pl
		}
		pl[doc.ID] = n
	}
	ix.docLen[doc.ID] = len(terms)
	ix.totLen += int64(len(terms))
	ix.mu.Unlock()

	// Embed + insert into dense index. Use the project's feature-hashing
	// embed when no external embedder is plumbed — the hybrid score is
	// dominated by BM25 for exact terms anyway, and the embedder is
	// pluggable at a later phase (see vector.Embed callsite for swap).
	vec := vector.Embed(doc.Text, ix.dim)
	return ix.dense.Set(doc.ID, vec)
}

// Delete removes a document. Returns false if the id was not present.
func (ix *Index) Delete(id string) bool {
	ix.mu.Lock()
	doc, ok := ix.docs[id]
	if !ok {
		ix.mu.Unlock()
		return false
	}
	for _, t := range tokenize(doc.Text, ix.stopwords) {
		if pl, ok := ix.postings[t]; ok {
			delete(pl, id)
			if len(pl) == 0 {
				delete(ix.postings, t)
			}
		}
	}
	ix.totLen -= int64(ix.docLen[id])
	delete(ix.docLen, id)
	delete(ix.docs, id)
	ix.mu.Unlock()

	ix.dense.Del(id)
	return true
}

// Get returns one indexed document, ok=false if absent.
func (ix *Index) Get(id string) (*Document, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	d, ok := ix.docs[id]
	if !ok {
		return nil, false
	}
	cp := *d
	return &cp, true
}

// Size reports the indexed document count.
func (ix *Index) Size() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}

// QueryOptions tune one search call.
type QueryOptions struct {
	K       int     // top-k after fusion (default 10)
	Alpha   float64 // 0=BM25-only, 1=vector-only, 0.5=balanced (default 0.5)
	RRFK    float64 // RRF damping constant (default 60 — Cormack et al.)
	Filter  func(meta map[string]string) bool
	Rerank  Reranker // optional cross-encoder
	UseBM25 bool     // when both UseBM25 and UseVector are false, both are enabled
	UseVect bool
}

// Reranker is a pluggable second-stage scorer applied to the top
// candidates after fusion. Score is "higher is better" — we sort
// descending. Returning an error falls back to the fused order.
type Reranker func(query string, hits []Hit) ([]Hit, error)

// Query runs the hybrid search and returns ranked Hits.
func (ix *Index) Query(query string, opts QueryOptions) []Hit {
	if opts.K <= 0 {
		opts.K = 10
	}
	if opts.Alpha < 0 || opts.Alpha > 1 {
		opts.Alpha = 0.5
	}
	if opts.RRFK <= 0 {
		opts.RRFK = 60
	}
	useBM25 := opts.UseBM25 || (!opts.UseBM25 && !opts.UseVect)
	useVect := opts.UseVect || (!opts.UseBM25 && !opts.UseVect)

	// Run both arms with a generous oversample so the fused list has
	// candidates that didn't make either individual top-K.
	oversample := opts.K * 4
	if oversample < 32 {
		oversample = 32
	}

	var bm25Hits, vecHits []Hit
	if useBM25 {
		bm25Hits = ix.bm25Search(query, oversample)
	}
	if useVect {
		vecHits = ix.vectorSearch(query, oversample)
	}

	fused := ix.rrfFuse(bm25Hits, vecHits, opts)

	// Apply caller filter on metadata.
	if opts.Filter != nil {
		filtered := fused[:0]
		for _, h := range fused {
			if opts.Filter(h.Metadata) {
				filtered = append(filtered, h)
			}
		}
		fused = filtered
	}

	// Rerank (cross-encoder or any caller-supplied scorer) on top
	// candidates only — re-ranking is expensive, so we cap it.
	if opts.Rerank != nil && len(fused) > 0 {
		topN := fused
		if len(topN) > opts.K*3 {
			topN = topN[:opts.K*3]
		}
		if rer, err := opts.Rerank(query, topN); err == nil {
			fused = rer
		}
	}

	if len(fused) > opts.K {
		fused = fused[:opts.K]
	}
	return fused
}

func (ix *Index) bm25Search(query string, k int) []Hit {
	terms := tokenize(query, ix.stopwords)
	if len(terms) == 0 {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	N := float64(len(ix.docs))
	if N == 0 {
		return nil
	}
	avgLen := 1.0
	if N > 0 {
		avgLen = float64(ix.totLen) / N
	}

	scores := map[string]float64{}
	for _, term := range terms {
		pl, ok := ix.postings[term]
		if !ok {
			continue
		}
		df := float64(len(pl))
		// Standard Okapi BM25 IDF, smoothed +1 to avoid negatives on
		// terms appearing in >half the corpus (which is rare with
		// stopword filtering but cheap insurance).
		idf := math.Log((N-df+0.5)/(df+0.5) + 1)
		for docID, tf := range pl {
			dl := float64(ix.docLen[docID])
			tfFloat := float64(tf)
			denom := tfFloat + ix.k1*(1-ix.b+ix.b*dl/avgLen)
			scores[docID] += idf * (tfFloat * (ix.k1 + 1)) / denom
		}
	}

	hits := make([]Hit, 0, len(scores))
	for id, s := range scores {
		d := ix.docs[id]
		hits = append(hits, Hit{
			ID:        id,
			Text:      d.Text,
			Metadata:  cloneMeta(d.Metadata),
			BM25Score: s,
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].BM25Score > hits[j].BM25Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	for i := range hits {
		hits[i].BM25Rank = i + 1
	}
	return hits
}

func (ix *Index) vectorSearch(query string, k int) []Hit {
	if query == "" {
		return nil
	}
	q := vector.Embed(query, ix.dim)
	results := ix.dense.KNN(q, k)
	if len(results) == 0 {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	hits := make([]Hit, 0, len(results))
	for i, r := range results {
		d, ok := ix.docs[r.ID]
		if !ok {
			continue
		}
		hits = append(hits, Hit{
			ID:         r.ID,
			Text:       d.Text,
			Metadata:   cloneMeta(d.Metadata),
			VectorDist: r.Distance,
			VectorRank: i + 1,
		})
	}
	return hits
}

// rrfFuse implements weighted Reciprocal Rank Fusion. Each hit's
// contribution is alpha/(rrfK+vectorRank) + (1-alpha)/(rrfK+bm25Rank).
// Missing-arm ranks contribute zero. RRF is rank-only — we don't have
// to normalize the heterogeneous BM25 and cosine scales. Standard
// trick from Cormack et al. that survives a decade later as the
// production retrieval-fusion default.
func (ix *Index) rrfFuse(bm25, vect []Hit, opts QueryOptions) []Hit {
	merged := map[string]*Hit{}

	put := func(id string, src *Hit) *Hit {
		if h, ok := merged[id]; ok {
			return h
		}
		h := &Hit{
			ID:       id,
			Text:     src.Text,
			Metadata: src.Metadata,
		}
		merged[id] = h
		return h
	}

	for i := range bm25 {
		h := put(bm25[i].ID, &bm25[i])
		h.BM25Rank = bm25[i].BM25Rank
		h.BM25Score = bm25[i].BM25Score
	}
	for i := range vect {
		h := put(vect[i].ID, &vect[i])
		h.VectorRank = vect[i].VectorRank
		h.VectorDist = vect[i].VectorDist
	}

	for _, h := range merged {
		var s float64
		if h.BM25Rank > 0 {
			s += (1 - opts.Alpha) / (opts.RRFK + float64(h.BM25Rank))
		}
		if h.VectorRank > 0 {
			s += opts.Alpha / (opts.RRFK + float64(h.VectorRank))
		}
		h.Score = s
	}

	out := make([]Hit, 0, len(merged))
	for _, h := range merged {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Stats reports per-index size + posting list cardinality. Useful for
// "vector index" panels in the dashboard.
type Stats struct {
	Documents int   `json:"documents"`
	Terms     int   `json:"terms"`
	TotalLen  int64 `json:"total_length"`
	AvgLen    int64 `json:"avg_length"`
}

func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	avg := int64(0)
	if len(ix.docs) > 0 {
		avg = ix.totLen / int64(len(ix.docs))
	}
	return Stats{
		Documents: len(ix.docs),
		Terms:     len(ix.postings),
		TotalLen:  ix.totLen,
		AvgLen:    avg,
	}
}

// Manager is the per-engine registry of named indexes. RESP commands
// look up an index by name; the manager creates them lazily so callers
// can `RETRIEVE.ADD docs hello "..."` without a separate CREATE step
// once defaults are acceptable. Explicit `RETRIEVE.CREATE` lets callers
// pin tuning knobs upfront.
type Manager struct {
	mu      sync.RWMutex
	defDim  int
	indexes map[string]*Index
}

// NewManager returns a fresh manager with the given default vector
// dimension (used when callers don't specify one).
func NewManager(defaultDim int) *Manager {
	if defaultDim <= 0 {
		defaultDim = 384
	}
	return &Manager{defDim: defaultDim, indexes: map[string]*Index{}}
}

// Create makes an index. Existing index returns ErrIndexExists.
func (m *Manager) Create(name string, opts Options) (*Index, error) {
	if name == "" {
		return nil, errors.New("index name required")
	}
	if opts.Dim == 0 {
		opts.Dim = m.defDim
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.indexes[name]; ok {
		return nil, ErrIndexExists
	}
	ix, err := New(opts)
	if err != nil {
		return nil, err
	}
	m.indexes[name] = ix
	return ix, nil
}

// GetOrCreate returns an existing index or creates one with defaults.
// This is what most ergonomic SDK callers want.
func (m *Manager) GetOrCreate(name string) *Index {
	m.mu.RLock()
	if ix, ok := m.indexes[name]; ok {
		m.mu.RUnlock()
		return ix
	}
	m.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if ix, ok := m.indexes[name]; ok {
		return ix
	}
	ix, _ := New(Options{Dim: m.defDim})
	m.indexes[name] = ix
	return ix
}

// Get returns an existing index by name.
func (m *Manager) Get(name string) (*Index, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ix, ok := m.indexes[name]
	return ix, ok
}

// Drop deletes an index.
func (m *Manager) Drop(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.indexes[name]; !ok {
		return false
	}
	delete(m.indexes, name)
	return true
}

// Names returns every index name.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.indexes))
	for k := range m.indexes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrIndexExists is returned when create is called on an existing name.
var ErrIndexExists = errors.New("index already exists")

// ErrNoSuchIndex is the lookup-failure sentinel.
var ErrNoSuchIndex = errors.New("no such retrieval index")

// ─── tokenization ────────────────────────────────────────────────

// tokenize lowercases, strips non-alphanumeric runes, and removes
// stopwords. Deliberately simple: production deployments often want
// stemming or language-specific analyzers, but those belong in a
// pluggable analyzer interface, not the tokenizer. Adding stemming
// blindly hurts non-English queries.
func tokenize(text string, stop map[string]struct{}) []string {
	text = strings.ToLower(text)
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if len(t) < 2 {
			continue
		}
		if _, skip := stop[t]; skip {
			continue
		}
		out = append(out, t)
	}
	return out
}

func termFreq(tokens []string) map[string]int {
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	return tf
}

func cloneMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
