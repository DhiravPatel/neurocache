package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SemanticInvalidator scans tracked cache entries for semantic
// matches against a "this fact changed" query and returns the keys
// that should be evicted. Solves the production failure mode the
// reviewer flagged: you ship 8 kinds of cache (semantic, llm, op,
// rerank, translate, rewrite, layered, fact-stamped), and NONE of
// them have an answer for "the refund policy just changed — kill
// every cached answer downstream of that fact."
//
// Design choice: apps explicitly TRACK cache entries with their
// semantic content. SEMANTIC then computes cosine against the
// invalidation query over the tracked set and returns matches above
// THRESHOLD. Apps are responsible for actually evicting from their
// cache (single round-trip: SEMANTIC → DEL list).
//
// Why explicit TRACK instead of reaching into existing caches: keeps
// the semantic invalidator independent of any cache implementation
// (SEMANTIC_SET, LLM_SET, OP.SET, etc.). Apps register the entries
// they want to be invalidatable; everything else is opt-out.
//
// Commands:
//
//   CACHE.INVALIDATE.TRACK layer key text [EMBED v,v,...]
//        Register a cache key with its semantic content.
//   CACHE.INVALIDATE.UNTRACK layer key
//        Drop registration (e.g. after the app evicted the key).
//   CACHE.INVALIDATE.SEMANTIC query [THRESHOLD 0.80]
//        [LAYERS layer1,layer2,...] [EMBED v,v,...]
//        Scan tracked entries; return matches above threshold,
//        grouped by layer. Apps DEL each returned key.
//   CACHE.INVALIDATE.STAMP layer key  → re-stamp (refresh timestamp)
//   CACHE.STALE.LIST [LAYER l] [LIMIT n]
//        Every tracked key. App pairs with FACT.* to identify
//        which are stale due to fact-version drift.
//   CACHE.INVALIDATE.STATS
//
// Storage: per-layer map of key → {embedding, text, tracked_at}.
// SEMANTIC scan is O(N) per layer × O(dim) cosine. At 100k tracked
// keys × 128 dims it's ~13 ms — fast enough for ad-hoc operator
// invalidations.
type SemanticInvalidator struct {
	mu     sync.RWMutex
	layers map[string]*invalLayer

	totalTracks       atomic.Int64
	totalScans        atomic.Int64
	totalInvalidations atomic.Int64
}

type invalLayer struct {
	mu      sync.RWMutex
	entries map[string]*invalEntry
}

type invalEntry struct {
	text      string
	vec       []float64
	trackedAt int64
}

// NewSemanticInvalidator returns an empty invalidator.
func NewSemanticInvalidator() *SemanticInvalidator {
	return &SemanticInvalidator{layers: map[string]*invalLayer{}}
}

// Track registers a cache key for potential semantic invalidation.
// vec may be nil — falls back to hashed-BoW.
func (s *SemanticInvalidator) Track(layer, key, text string, vec []float64) error {
	if layer == "" || key == "" {
		return errors.New("layer and key required")
	}
	if text == "" {
		return errors.New("text required")
	}
	s.totalTracks.Add(1)
	if vec == nil {
		vec = embedFallback(text)
	}
	s.mu.Lock()
	l, ok := s.layers[layer]
	if !ok {
		l = &invalLayer{entries: map[string]*invalEntry{}}
		s.layers[layer] = l
	}
	s.mu.Unlock()
	l.mu.Lock()
	l.entries[key] = &invalEntry{text: text, vec: vec, trackedAt: invalNowNS()}
	l.mu.Unlock()
	return nil
}

// Untrack drops a tracked entry. Returns true if it existed.
func (s *SemanticInvalidator) Untrack(layer, key string) bool {
	s.mu.RLock()
	l, ok := s.layers[layer]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	l.mu.Lock()
	_, was := l.entries[key]
	delete(l.entries, key)
	l.mu.Unlock()
	return was
}

// InvalidationHit is one match from the SEMANTIC scan.
type InvalidationHit struct {
	Layer string  `json:"layer"`
	Key   string  `json:"key"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

// InvalidationResult is SEMANTIC's return.
type InvalidationResult struct {
	Total      int                `json:"total"`
	PerLayer   map[string]int     `json:"per_layer"`
	Hits       []InvalidationHit  `json:"hits"`
}

// SemanticOpts narrows SEMANTIC's scan.
type SemanticOpts struct {
	Threshold float64
	Layers    []string // empty = all
	Vec       []float64
}

// Semantic scans tracked entries for matches above THRESHOLD.
// Returns the hits + per-layer counts so apps can issue bulk DELs.
func (s *SemanticInvalidator) Semantic(query string, opts SemanticOpts) InvalidationResult {
	s.totalScans.Add(1)
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = 0.80
	}
	queryVec := opts.Vec
	if queryVec == nil {
		queryVec = embedFallback(query)
	}
	wantLayers := map[string]bool{}
	for _, l := range opts.Layers {
		wantLayers[l] = true
	}
	allLayers := len(wantLayers) == 0

	out := InvalidationResult{PerLayer: map[string]int{}}
	s.mu.RLock()
	for layerName, layer := range s.layers {
		if !allLayers && !wantLayers[layerName] {
			continue
		}
		layer.mu.RLock()
		for key, entry := range layer.entries {
			if len(entry.vec) != len(queryVec) {
				continue
			}
			score := dotProduct(entry.vec, queryVec)
			if score >= threshold {
				out.Hits = append(out.Hits, InvalidationHit{
					Layer: layerName, Key: key,
					Text: entry.text, Score: score,
				})
				out.PerLayer[layerName]++
				out.Total++
			}
		}
		layer.mu.RUnlock()
	}
	s.mu.RUnlock()
	// Sort hits by score desc so operators see strongest matches first
	sort.Slice(out.Hits, func(i, j int) bool { return out.Hits[i].Score > out.Hits[j].Score })
	if out.Total > 0 {
		s.totalInvalidations.Add(int64(out.Total))
	}
	return out
}

// StampedRow is one row of STALE.LIST.
type StampedRow struct {
	Layer     string `json:"layer"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	TrackedAt int64  `json:"tracked_at_unix"`
}

// List returns every tracked entry, optionally filtered by layer.
func (s *SemanticInvalidator) List(layerFilter string, limit int) []StampedRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []StampedRow{}
	for layerName, layer := range s.layers {
		if layerFilter != "" && layerName != layerFilter {
			continue
		}
		layer.mu.RLock()
		for k, e := range layer.entries {
			out = append(out, StampedRow{
				Layer: layerName, Key: k, Text: e.text,
				TrackedAt: e.trackedAt / 1_000_000_000,
			})
			if limit > 0 && len(out) >= limit {
				layer.mu.RUnlock()
				return out
			}
		}
		layer.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Layer != out[j].Layer {
			return out[i].Layer < out[j].Layer
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// PurgeLayer wipes a whole layer. Returns the entry count dropped.
func (s *SemanticInvalidator) PurgeLayer(layer string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.layers[layer]
	if !ok {
		return 0
	}
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	delete(s.layers, layer)
	return n
}

// Stats is the global snapshot.
type InvalidationStats struct {
	Layers             int   `json:"layers"`
	TotalTracked       int   `json:"total_tracked"`
	TotalTracks        int64 `json:"total_tracks"`
	TotalScans         int64 `json:"total_scans"`
	TotalInvalidations int64 `json:"total_invalidations"`
}

func (s *SemanticInvalidator) Stats() InvalidationStats {
	s.mu.RLock()
	n := len(s.layers)
	total := 0
	for _, l := range s.layers {
		l.mu.RLock()
		total += len(l.entries)
		l.mu.RUnlock()
	}
	s.mu.RUnlock()
	return InvalidationStats{
		Layers:             n,
		TotalTracked:       total,
		TotalTracks:        s.totalTracks.Load(),
		TotalScans:         s.totalScans.Load(),
		TotalInvalidations: s.totalInvalidations.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func invalNowNS() int64 {
	return time.Now().UnixNano()
}
