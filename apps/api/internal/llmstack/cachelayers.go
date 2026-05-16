package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// CacheLayers is a 3-layer cache (exact → semantic → negative) that
// resolves to the first hit in ONE round-trip. Today, RAG apps do
// 3 sequential GETs: check exact-match cache, then semantic cache,
// then a negative-cache to see if we already know there's no good
// answer. That's 3 round-trips per request, every request.
//
// CACHE.LAYERS.LOOKUP collapses all three to a single call:
//
//   1. exact:    sha256(key) → value
//   2. semantic: cosine over stored (embedding, value) pairs
//                ≥ threshold (default 0.85) → value
//   3. negative: did we previously mark this key/text as
//                "no answer was found"? Apps short-circuit on
//                this to avoid re-running expensive RAG that
//                won't help.
//
// Returns {hit_layer, value, score} or {hit_layer: "miss"}.
//
// Commands:
//
//   CACHE.LAYERS.SET layer key value [EX sec] [EMBED v,v,...]
//        Layer = exact | semantic | negative.
//        For semantic, EMBED is the embedding of `key`; if
//        omitted, the hashed-BoW fallback is computed from
//        `key`.
//   CACHE.LAYERS.LOOKUP key [TEXT semantic-text] [EMBED v,v,...]
//        Walks all three layers in order. Returns first hit.
//   CACHE.LAYERS.FORGET key [LAYER l]
//   CACHE.LAYERS.PURGE [LAYER l]
//   CACHE.LAYERS.STATS
//
// Storage: three internal maps. exact is hash-keyed; semantic is
// a flat (embedding, value) list with linear cosine scan; negative
// is hash-keyed with TTL. Apps that need millions of semantic
// entries graduate to RETRIEVE.* — this is the fast-path for
// "first 100k common queries" hot cache.
type CacheLayers struct {
	mu             sync.RWMutex
	exact          map[string]*layeredEntry
	semantic       []semanticLayer
	negative       map[string]*layeredEntry
	semThreshold   float64

	totalLookups atomic.Int64
	exactHits    atomic.Int64
	semanticHits atomic.Int64
	negativeHits atomic.Int64
	misses       atomic.Int64
}

type layeredEntry struct {
	value     string
	expiresAt int64
}

type semanticLayer struct {
	keyHash   string
	vec       []float64
	value     string
	expiresAt int64
}

// NewCacheLayers returns an empty registry with 0.85 semantic
// similarity threshold.
func NewCacheLayers() *CacheLayers {
	return &CacheLayers{
		exact:        map[string]*layeredEntry{},
		negative:     map[string]*layeredEntry{},
		semThreshold: 0.85,
	}
}

// SetThreshold updates the semantic similarity gate.
func (c *CacheLayers) SetThreshold(t float64) {
	c.mu.Lock()
	c.semThreshold = t
	c.mu.Unlock()
}

// Set stores in the specified layer.
type LayerSetOpts struct {
	TTL time.Duration
	Vec []float64
}

func (c *CacheLayers) Set(layer, key, value string, opts LayerSetOpts) error {
	if key == "" {
		return errors.New("key required")
	}
	now := time.Now().UnixNano()
	exp := int64(0)
	if opts.TTL > 0 {
		exp = now + opts.TTL.Nanoseconds()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch layer {
	case "exact":
		c.exact[hashKeyCL(key)] = &layeredEntry{value: value, expiresAt: exp}
	case "semantic":
		vec := opts.Vec
		if vec == nil {
			vec = embedFallback(key)
		}
		// Replace any existing entry for the same key (by hash)
		kh := hashKeyCL(key)
		for i, s := range c.semantic {
			if s.keyHash == kh {
				c.semantic[i] = semanticLayer{
					keyHash: kh, vec: vec, value: value, expiresAt: exp,
				}
				return nil
			}
		}
		c.semantic = append(c.semantic, semanticLayer{
			keyHash: kh, vec: vec, value: value, expiresAt: exp,
		})
	case "negative":
		c.negative[hashKeyCL(key)] = &layeredEntry{value: value, expiresAt: exp}
	default:
		return errors.New("unknown layer: " + layer)
	}
	return nil
}

// LookupResult is the LOOKUP return.
type LookupResult struct {
	HitLayer string  `json:"hit_layer"` // exact | semantic | negative | miss
	Value    string  `json:"value,omitempty"`
	Score    float64 `json:"score,omitempty"` // for semantic hits
}

// Lookup walks the three layers in order. Returns the first hit
// or {hit_layer: miss}.
type LookupOpts struct {
	Text string    // text used for semantic-layer matching (defaults to key)
	Vec  []float64 // optional embedding override
}

func (c *CacheLayers) Lookup(key string, opts LookupOpts) LookupResult {
	c.totalLookups.Add(1)
	now := time.Now().UnixNano()
	kh := hashKeyCL(key)

	c.mu.RLock()
	// Layer 1: exact
	if e, ok := c.exact[kh]; ok && (e.expiresAt == 0 || e.expiresAt > now) {
		v := e.value
		c.mu.RUnlock()
		c.exactHits.Add(1)
		return LookupResult{HitLayer: "exact", Value: v}
	}

	// Layer 2: semantic
	semText := opts.Text
	if semText == "" {
		semText = key
	}
	queryVec := opts.Vec
	if queryVec == nil {
		queryVec = embedFallback(semText)
	}
	threshold := c.semThreshold
	bestScore := 0.0
	bestValue := ""
	for _, s := range c.semantic {
		if s.expiresAt != 0 && s.expiresAt <= now {
			continue
		}
		if len(s.vec) != len(queryVec) {
			continue
		}
		score := dotProduct(s.vec, queryVec)
		if score > bestScore {
			bestScore = score
			bestValue = s.value
		}
	}
	if bestScore >= threshold {
		c.mu.RUnlock()
		c.semanticHits.Add(1)
		return LookupResult{HitLayer: "semantic", Value: bestValue, Score: bestScore}
	}

	// Layer 3: negative
	if e, ok := c.negative[kh]; ok && (e.expiresAt == 0 || e.expiresAt > now) {
		v := e.value
		c.mu.RUnlock()
		c.negativeHits.Add(1)
		return LookupResult{HitLayer: "negative", Value: v}
	}
	c.mu.RUnlock()
	c.misses.Add(1)
	return LookupResult{HitLayer: "miss"}
}

// Forget drops one key. Empty layer = all layers.
func (c *CacheLayers) Forget(layer, key string) int {
	kh := hashKeyCL(key)
	dropped := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	if layer == "" || layer == "exact" {
		if _, ok := c.exact[kh]; ok {
			delete(c.exact, kh)
			dropped++
		}
	}
	if layer == "" || layer == "semantic" {
		for i, s := range c.semantic {
			if s.keyHash == kh {
				c.semantic = append(c.semantic[:i], c.semantic[i+1:]...)
				dropped++
				break
			}
		}
	}
	if layer == "" || layer == "negative" {
		if _, ok := c.negative[kh]; ok {
			delete(c.negative, kh)
			dropped++
		}
	}
	return dropped
}

// Purge wipes the specified layer (or all).
func (c *CacheLayers) Purge(layer string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	if layer == "" || layer == "exact" {
		n += len(c.exact)
		c.exact = map[string]*layeredEntry{}
	}
	if layer == "" || layer == "semantic" {
		n += len(c.semantic)
		c.semantic = nil
	}
	if layer == "" || layer == "negative" {
		n += len(c.negative)
		c.negative = map[string]*layeredEntry{}
	}
	return n
}

// CacheLayersStats is the global snapshot.
type CacheLayersStats struct {
	ExactSize    int     `json:"exact_size"`
	SemanticSize int     `json:"semantic_size"`
	NegativeSize int     `json:"negative_size"`
	Threshold    float64 `json:"semantic_threshold"`
	TotalLookups int64   `json:"total_lookups"`
	ExactHits    int64   `json:"exact_hits"`
	SemanticHits int64   `json:"semantic_hits"`
	NegativeHits int64   `json:"negative_hits"`
	Misses       int64   `json:"misses"`
	HitRate      float64 `json:"hit_rate"`
}

func (c *CacheLayers) Stats() CacheLayersStats {
	c.mu.RLock()
	e := len(c.exact)
	s := len(c.semantic)
	n := len(c.negative)
	th := c.semThreshold
	c.mu.RUnlock()
	lookups := c.totalLookups.Load()
	hits := c.exactHits.Load() + c.semanticHits.Load() + c.negativeHits.Load()
	rate := 0.0
	if lookups > 0 {
		rate = float64(hits) / float64(lookups)
	}
	return CacheLayersStats{
		ExactSize:    e,
		SemanticSize: s,
		NegativeSize: n,
		Threshold:    th,
		TotalLookups: lookups,
		ExactHits:    c.exactHits.Load(),
		SemanticHits: c.semanticHits.Load(),
		NegativeHits: c.negativeHits.Load(),
		Misses:       c.misses.Load(),
		HitRate:      rate,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func hashKeyCL(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
