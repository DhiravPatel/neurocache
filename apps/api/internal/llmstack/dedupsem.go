package llmstack

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SemDeduper does sliding-window semantic deduplication. Real-world
// pain: high-volume text streams (bug reports, customer complaints,
// news ingest, agent traces) get the same item rephrased 50 ways.
// Hash-based dedup misses every paraphrase. The fix is cosine
// similarity over recent items — but apps reimplement the sliding
// window + thresholding in every project, often with the wrong
// eviction policy (FIFO is correct; LRU is subtly wrong because it
// keeps stale rephrasings alive).
//
// DEDUP.SEM.* gives the cache one command:
//
//   DEDUP.SEM.SEEN bucket text [THRESHOLD f] [WINDOW n] [EMBED vec]
//        -> [is_dup, similar_id, similar_text, score]
//        SEEN both queries AND inserts on miss — single round trip
//        for the common case.
//   DEDUP.SEM.PEEK bucket text [THRESHOLD f] [EMBED vec]
//        -> query-only, no insert.
//   DEDUP.SEM.ADD bucket id text [EMBED vec]
//        -> insert without dedup check.
//   DEDUP.SEM.RECENT bucket [N]   -> the N most-recent items.
//   DEDUP.SEM.FORGET bucket       -> drop the whole bucket.
//   DEDUP.SEM.STATS               -> hit/miss/eviction counters.
//
// Implementation:
//   - Per-bucket FIFO deque, fixed size (default 1000).
//   - 128-dim hashed-BoW vectors (deterministic, no embedding service
//     needed). Apps pass real embeddings via EMBED for better quality.
//   - L2-normalized so cosine = dot product. ~128 multiplies per
//     candidate — 1000-item window = 128k ops, vectorisable, runs
//     in microseconds.
//   - sync.Map for buckets; per-bucket RWMutex for the deque.
//
// Throughput target: SEEN over a 1000-item window in <5 µs.
type SemDeduper struct {
	buckets sync.Map // bucket_name -> *dedupBucket

	defaultThreshold float64
	defaultWindow    int

	totalSeens      atomic.Int64
	totalHits       atomic.Int64
	totalMisses     atomic.Int64
	totalEvictions  atomic.Int64
	totalAdds       atomic.Int64
}

type dedupBucket struct {
	mu     sync.RWMutex
	items  []dedupItem // FIFO; oldest at index 0
	dim    int
	window int
}

type dedupItem struct {
	id    string
	text  string
	vec   []float64
	at    int64 // unix-nano
}

// NewSemDeduper returns a deduper with sensible defaults: 0.85
// cosine threshold and 1000-item windows.
func NewSemDeduper() *SemDeduper {
	return &SemDeduper{
		defaultThreshold: 0.85,
		defaultWindow:    1000,
	}
}

// SetDefaults updates the default threshold + window for buckets
// created later. Existing buckets keep their original window.
func (d *SemDeduper) SetDefaults(threshold float64, window int) {
	if threshold > 0 {
		d.defaultThreshold = threshold
	}
	if window > 0 {
		d.defaultWindow = window
	}
}

// SeenOpts configures a SEEN call.
type SeenOpts struct {
	Threshold float64
	Window    int
	Vec       []float64 // optional pre-computed embedding
}

// SeenResult is what SEEN/PEEK return.
type SeenResult struct {
	IsDup        bool    `json:"is_dup"`
	SimilarID    string  `json:"similar_id,omitempty"`
	SimilarText  string  `json:"similar_text,omitempty"`
	Score        float64 `json:"score"`
	NewID        string  `json:"new_id,omitempty"` // populated when SEEN inserts
}

// Seen does dedup-check-and-insert: returns is_dup=true with the
// matched item, or inserts (assigning a new ID) and returns
// is_dup=false. Single round-trip for the common case.
func (d *SemDeduper) Seen(bucket, text string, opts SeenOpts) SeenResult {
	d.totalSeens.Add(1)
	return d.checkAndMaybeInsert(bucket, text, opts, true)
}

// Peek is the query-only variant: never inserts.
func (d *SemDeduper) Peek(bucket, text string, opts SeenOpts) SeenResult {
	return d.checkAndMaybeInsert(bucket, text, opts, false)
}

func (d *SemDeduper) checkAndMaybeInsert(bucket, text string, opts SeenOpts, insertOnMiss bool) SeenResult {
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = d.defaultThreshold
	}
	window := opts.Window
	if window <= 0 {
		window = d.defaultWindow
	}
	vec := opts.Vec
	if vec == nil {
		vec = embedFallback(text)
	}

	b := d.bucketFor(bucket, window, len(vec))
	if b.dim != len(vec) {
		// Dim mismatch — treat as fresh bucket (rare; usually a
		// caller passed inconsistent embeddings).
		return SeenResult{IsDup: false}
	}

	b.mu.RLock()
	bestScore := 0.0
	bestIdx := -1
	for i := range b.items {
		s := dotProduct(b.items[i].vec, vec)
		if s > bestScore {
			bestScore = s
			bestIdx = i
		}
	}
	b.mu.RUnlock()

	if bestIdx >= 0 && bestScore >= threshold {
		d.totalHits.Add(1)
		b.mu.RLock()
		matched := b.items[bestIdx]
		b.mu.RUnlock()
		return SeenResult{
			IsDup:       true,
			SimilarID:   matched.id,
			SimilarText: matched.text,
			Score:       bestScore,
		}
	}

	d.totalMisses.Add(1)
	res := SeenResult{IsDup: false, Score: bestScore}
	if !insertOnMiss {
		return res
	}

	id := newDedupID()
	d.insertLocked(b, dedupItem{
		id:   id,
		text: text,
		vec:  vec,
		at:   time.Now().UnixNano(),
	})
	d.totalAdds.Add(1)
	res.NewID = id
	return res
}

// Add explicitly inserts an item without a dedup check. Used by apps
// that want to bypass dedup for known-distinct content (e.g. user-
// assigned IDs).
func (d *SemDeduper) Add(bucket, id, text string, vec []float64) error {
	if id == "" {
		return errors.New("id required")
	}
	if vec == nil {
		vec = embedFallback(text)
	}
	b := d.bucketFor(bucket, d.defaultWindow, len(vec))
	if b.dim != len(vec) {
		return errors.New("embedding dim mismatch with bucket")
	}
	d.insertLocked(b, dedupItem{
		id:   id,
		text: text,
		vec:  vec,
		at:   time.Now().UnixNano(),
	})
	d.totalAdds.Add(1)
	return nil
}

func (d *SemDeduper) insertLocked(b *dedupBucket, it dedupItem) {
	b.mu.Lock()
	b.items = append(b.items, it)
	if len(b.items) > b.window {
		drop := len(b.items) - b.window
		b.items = b.items[drop:]
		d.totalEvictions.Add(int64(drop))
	}
	b.mu.Unlock()
}

// RecentRow is one row of DEDUP.SEM.RECENT.
type RecentRow struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	AtMS int64  `json:"at_ms"`
}

// Recent returns the last n items in insertion order, newest last.
// Pass 0 for "all".
func (d *SemDeduper) Recent(bucket string, n int) []RecentRow {
	v, ok := d.buckets.Load(bucket)
	if !ok {
		return nil
	}
	b := v.(*dedupBucket)
	b.mu.RLock()
	items := b.items
	if n > 0 && n < len(items) {
		items = items[len(items)-n:]
	}
	out := make([]RecentRow, len(items))
	for i, it := range items {
		out[i] = RecentRow{ID: it.id, Text: it.text, AtMS: it.at / int64(time.Millisecond)}
	}
	b.mu.RUnlock()
	return out
}

// Forget drops a bucket entirely.
func (d *SemDeduper) Forget(bucket string) bool {
	_, was := d.buckets.LoadAndDelete(bucket)
	return was
}

// Buckets returns every active bucket with size + window.
type DedupBucketRow struct {
	Bucket string `json:"bucket"`
	Size   int    `json:"size"`
	Window int    `json:"window"`
}

func (d *SemDeduper) Buckets() []DedupBucketRow {
	out := []DedupBucketRow{}
	d.buckets.Range(func(k, v any) bool {
		b := v.(*dedupBucket)
		b.mu.RLock()
		out = append(out, DedupBucketRow{Bucket: k.(string), Size: len(b.items), Window: b.window})
		b.mu.RUnlock()
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out
}

// SemDedupStats is the global counters snapshot.
type SemDedupStats struct {
	Buckets        int     `json:"buckets"`
	TotalSeens     int64   `json:"total_seens"`
	TotalHits      int64   `json:"total_hits"`
	TotalMisses    int64   `json:"total_misses"`
	TotalAdds      int64   `json:"total_adds"`
	TotalEvictions int64   `json:"total_evictions"`
	HitRate        float64 `json:"hit_rate"`
}

func (d *SemDeduper) Stats() SemDedupStats {
	n := 0
	d.buckets.Range(func(_, _ any) bool { n++; return true })
	seens := d.totalSeens.Load()
	hits := d.totalHits.Load()
	rate := 0.0
	if seens > 0 {
		rate = float64(hits) / float64(seens)
	}
	return SemDedupStats{
		Buckets:        n,
		TotalSeens:     seens,
		TotalHits:      hits,
		TotalMisses:    d.totalMisses.Load(),
		TotalAdds:      d.totalAdds.Load(),
		TotalEvictions: d.totalEvictions.Load(),
		HitRate:        rate,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (d *SemDeduper) bucketFor(name string, window, dim int) *dedupBucket {
	if v, ok := d.buckets.Load(name); ok {
		return v.(*dedupBucket)
	}
	fresh := &dedupBucket{
		items:  make([]dedupItem, 0, window),
		dim:    dim,
		window: window,
	}
	actual, _ := d.buckets.LoadOrStore(name, fresh)
	return actual.(*dedupBucket)
}

// dotProduct is cosine for L2-normalised vectors. embedFallback
// already returns normalised vectors so we skip the divide-by-norm
// step on the hot path.
func dotProduct(a, b []float64) float64 {
	var s float64
	for i, v := range a {
		s += v * b[i]
	}
	return s
}

func newDedupID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
