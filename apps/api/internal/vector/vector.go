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

	// shadow + dualRead implement REMBED's dual-read cutover. During an
	// embedding migration a shadow index is built at the new dimension
	// from the same source texts (AttachShadow); while dualRead is on,
	// Search consults BOTH spaces and unions the results so retrieval
	// quality never dips mid-migration. SwapShadow promotes the shadow
	// to primary (commit); DropShadow discards it (rollback). The shadow
	// itself is an ordinary *Index with no shadow of its own, so Search
	// recursion terminates after one level.
	shadow   *Index
	dualRead bool
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
//
// When a dual-read migration is active (AttachShadow + SetDualRead), the
// query is scored against both this index and the shadow (each at its own
// dimension), and the two result sets are unioned by id (keeping the
// higher score) before the top-k cut. This is what lets REMBED rebuild the
// vector space underneath live traffic without a retrieval gap.
func (ix *Index) Search(query string, k int, threshold float32) []Hit {
	hits := ix.scanSelf(query, threshold)

	ix.mu.RLock()
	sh := ix.shadow
	dual := ix.dualRead
	ix.mu.RUnlock()
	if dual && sh != nil {
		hits = mergeHits(hits, sh.scanSelf(query, threshold))
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

// scanSelf scores the query against this index's own items only (never the
// shadow), embedding at this index's own dimension. Unsorted, uncapped.
func (ix *Index) scanSelf(query string, threshold float32) []Hit {
	ix.mu.RLock()
	q := Embed(query, ix.dim)
	hits := make([]Hit, 0, len(ix.items))
	for _, it := range ix.items {
		s := Cosine(q, it.Vec)
		if s >= threshold {
			hits = append(hits, Hit{ID: it.ID, Score: s, Text: it.Text, Meta: it.Meta})
		}
	}
	ix.mu.RUnlock()
	return hits
}

// mergeHits unions two hit slices by id, keeping the higher score for ids
// present in both (the same id is expected on both sides during dual-read
// since the shadow is built from identical source texts).
func mergeHits(a, b []Hit) []Hit {
	if len(b) == 0 {
		return a
	}
	byID := make(map[string]Hit, len(a)+len(b))
	for _, h := range a {
		byID[h.ID] = h
	}
	for _, h := range b {
		if cur, ok := byID[h.ID]; !ok || h.Score > cur.Score {
			byID[h.ID] = h
		}
	}
	out := make([]Hit, 0, len(byID))
	for _, h := range byID {
		out = append(out, h)
	}
	return out
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

// Dim returns the current embedding dimension of the live (primary) space.
func (ix *Index) Dim() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.dim
}

// ─── REMBED dual-read staging ───────────────────────────────────────────
//
// These primitives drive an embedding migration. The orchestrator (the
// rembed package) snapshots the source texts, builds a fresh shadow index
// at the target dimension off the hot path, attaches it here, optionally
// flips on dual-read, then commits (SwapShadow) or aborts (DropShadow).

// SnapshotEntry is a minimal (id, text, meta) tuple — everything needed to
// re-embed an item into a new space.
type SnapshotEntry struct {
	ID   string
	Text string
	Meta map[string]string
}

// Snapshot returns a copy of every item's (id, text, meta), suitable for
// re-embedding into a shadow index. Vectors are intentionally omitted —
// the whole point is to recompute them.
func (ix *Index) Snapshot() []SnapshotEntry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]SnapshotEntry, 0, len(ix.items))
	for _, it := range ix.items {
		var meta map[string]string
		if it.Meta != nil {
			meta = make(map[string]string, len(it.Meta))
			for k, v := range it.Meta {
				meta[k] = v
			}
		}
		out = append(out, SnapshotEntry{ID: it.ID, Text: it.Text, Meta: meta})
	}
	return out
}

// ApproxBytes estimates the in-memory footprint of the live space: each
// item carries its float vector (dim×4 bytes) plus its source text.
func (ix *Index) ApproxBytes() int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var b int64
	for _, it := range ix.items {
		b += int64(len(it.Vec))*4 + int64(len(it.Text)) + int64(len(it.ID))
	}
	return b
}

// AttachShadow installs sh as the pending replacement space. Any prior
// shadow is discarded. dualRead is reset to false — the caller turns it on
// explicitly once the shadow is fully built.
func (ix *Index) AttachShadow(sh *Index) {
	ix.mu.Lock()
	ix.shadow = sh
	ix.dualRead = false
	ix.mu.Unlock()
}

// SetDualRead toggles whether Search unions the shadow space. A no-op when
// no shadow is attached.
func (ix *Index) SetDualRead(on bool) {
	ix.mu.Lock()
	if ix.shadow != nil || !on {
		ix.dualRead = on
	}
	ix.mu.Unlock()
}

// ShadowInfo reports whether a shadow is staged, plus its dimension and
// size. present=false means no migration is in flight.
func (ix *Index) ShadowInfo() (present bool, dim, size int, dualRead bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.shadow == nil {
		return false, 0, 0, false
	}
	return true, ix.shadow.Dim(), ix.shadow.Size(), ix.dualRead
}

// SwapShadow commits the migration: the shadow's items and dimension
// become primary, the old space is dropped, dual-read clears. Returns
// false if no shadow was staged.
//
// Two correctness properties this guarantees:
//
//   - Writes during the migration. Entries Upsert'd/Delete'd after the
//     shadow was built went only to the live map. Before promoting, the
//     shadow is reconciled against the live space (re-embedding new/changed
//     ids at the shadow dim, dropping ids deleted since) so those writes are
//     not silently lost by the wholesale swap.
//   - No map aliasing. The promoted map is a FRESH map owned by ix, never
//     sh.items. Aliasing would leave a detached shadow (still reachable to an
//     in-flight dual-read Search) sharing one map with the live index under
//     two different mutexes — a fatal "concurrent map read and map write".
//
// Lock order is always ix.mu → sh.mu (the same order Search and the other
// staging methods use), so this never deadlocks.
func (ix *Index) SwapShadow() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.shadow == nil {
		return false
	}
	sh := ix.shadow
	sh.mu.Lock()
	// Reconcile shadow ← live delta.
	for id, it := range ix.items {
		if sit, ok := sh.items[id]; !ok || sit.Text != it.Text {
			sh.items[id] = &Item{ID: id, Vec: Embed(it.Text, sh.dim), Text: it.Text, Meta: it.Meta}
		}
	}
	for id := range sh.items {
		if _, ok := ix.items[id]; !ok {
			delete(sh.items, id)
		}
	}
	// Promote into a fresh map (never alias sh.items).
	m := make(map[string]*Item, len(sh.items))
	for id, it := range sh.items {
		m[id] = it
	}
	ix.items = m
	ix.dim = sh.dim
	sh.mu.Unlock()
	ix.shadow = nil
	ix.dualRead = false
	return true
}

// DropShadow aborts the migration, leaving the live space untouched.
// Returns false if no shadow was staged.
func (ix *Index) DropShadow() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.shadow == nil {
		return false
	}
	ix.shadow = nil
	ix.dualRead = false
	return true
}
