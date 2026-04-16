// Package vector provides a tiny, dependency-free "embedding" and cosine
// similarity index suitable for demos and scaffolding. It maps text to a
// fixed-dimensional vector using feature hashing over words + character
// trigrams, then L2-normalizes. Swap in real ONNX / OpenAI embeddings later
// without touching callers.
package vector

import (
	"hash/fnv"
	"math"
	"strings"
	"sync"
)

// Embed returns an L2-normalized feature-hashed vector for text.
func Embed(text string, dim int) []float32 {
	if dim <= 0 {
		dim = 384
	}
	vec := make([]float32, dim)
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return vec
	}

	// Word unigrams (weight 1.0)
	for _, w := range strings.Fields(text) {
		vec[hash(w)%uint32(dim)] += 1.0
	}
	// Character trigrams with space padding (weight 0.5) — gives fuzziness
	padded := " " + text + " "
	runes := []rune(padded)
	for i := 0; i+3 <= len(runes); i++ {
		vec[hash(string(runes[i:i+3]))%uint32(dim)] += 0.5
	}

	// L2 normalize
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sum))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// Cosine returns similarity between two L2-normalized vectors (= dot product).
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func hash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// Index is a thread-safe in-memory linear-scan vector index.
// Fine up to tens of thousands of entries; swap for HNSW later.
type Index struct {
	mu    sync.RWMutex
	dim   int
	items map[string]*Item
}

type Item struct {
	ID     string
	Vec    []float32
	Text   string
	Meta   map[string]string
}

type Hit struct {
	ID    string
	Score float32
	Text  string
	Meta  map[string]string
}

func NewIndex(dim int) *Index {
	return &Index{dim: dim, items: make(map[string]*Item)}
}

func (ix *Index) Upsert(id, text string, meta map[string]string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.items[id] = &Item{
		ID:   id,
		Vec:  Embed(text, ix.dim),
		Text: text,
		Meta: meta,
	}
}

func (ix *Index) Delete(id string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, ok := ix.items[id]; ok {
		delete(ix.items, id)
		return true
	}
	return false
}

func (ix *Index) Size() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.items)
}

// Search returns top-k items whose score >= threshold, sorted desc.
func (ix *Index) Search(query string, k int, threshold float32) []Hit {
	q := Embed(query, ix.dim)
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	hits := make([]Hit, 0, len(ix.items))
	for _, it := range ix.items {
		s := Cosine(q, it.Vec)
		if s >= threshold {
			hits = append(hits, Hit{ID: it.ID, Score: s, Text: it.Text, Meta: it.Meta})
		}
	}
	// partial insertion sort for small k
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].Score > hits[j-1].Score; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Filter returns items where filter(meta) is true.
func (ix *Index) Filter(filter func(meta map[string]string) bool) []*Item {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]*Item, 0)
	for _, it := range ix.items {
		if filter == nil || filter(it.Meta) {
			out = append(out, it)
		}
	}
	return out
}
