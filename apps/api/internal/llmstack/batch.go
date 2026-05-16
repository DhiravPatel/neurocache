package llmstack

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// BatchAccumulator is the micro-batch coalescer for embedding /
// inference / any rate-limited bulk-friendly upstream.
//
// Pricing reality: embeddings APIs (OpenAI, Voyage, Cohere) and
// most batch-inference endpoints are 5-10× cheaper per item when
// called in bulk. App code almost never batches because the
// request boundaries don't line up — request A wants doc 12,
// request B wants doc 47, and they arrive 8 ms apart. The cache
// engine sees both. BATCH.* gives the engine the lever:
//
//   ADD          places the item in the active batch for that
//                bucket and returns (batch_id, slot, ready). ready=1
//                tells the caller this ADD just filled MAXSIZE — fire
//                FLUSH now.
//   FLUSH        atomically rolls the active batch forward, returns
//                the item set so the caller makes ONE provider call.
//   PEEK         current age + size without flushing — useful for
//                background flushers that want "anything over MAXWAIT
//                ms old?".
//   RESOLVE      app calls back with the per-item results so STATS
//                can report cost savings, hit rate, and per-item
//                latency.
//
// Commands:
//
//   BATCH.CONFIG bucket-id MAXWAIT_MS n MAXSIZE n
//        [COST_PER_CALL f] [COST_PER_ITEM f]
//   BATCH.ADD bucket-id item-id payload
//        → batch_id, slot, ready, age_ms
//   BATCH.FLUSH bucket-id
//        → batch_id, items:[{item_id, payload}, ...]
//   BATCH.PEEK bucket-id
//        → batch_id, size, age_ms, ready
//   BATCH.RESOLVE bucket-id batch-id [RESULT item-id result]...
//   BATCH.BUCKETS
//   BATCH.RESET bucket-id|ALL
//   BATCH.STATS
//
// Hot path: ADD is one lock + slice append + nanosecond comparison.
// FLUSH is one slice swap. Bookkeeping uses atomic counters so STATS
// is contention-free.
type BatchAccumulator struct {
	mu      sync.RWMutex
	buckets map[string]*batchBucket

	totalAdds     atomic.Int64
	totalFlushes  atomic.Int64
	totalResolves atomic.Int64
}

type batchBucket struct {
	mu           sync.Mutex
	cfg          batchConfig
	active       *batchInFlight
	batchSeq     int
	totalItems   int64
	totalCalls   int64
	savedCalls   int64 // items batched − calls fired
}

type batchInFlight struct {
	id        string
	items     []batchItem
	createdAt int64
}

type batchItem struct {
	ItemID  string
	Payload string
	TS      int64
}

type batchConfig struct {
	MaxWait      time.Duration
	MaxSize      int
	CostPerCall  float64
	CostPerItem  float64
}

// NewBatchAccumulator returns an empty accumulator.
func NewBatchAccumulator() *BatchAccumulator {
	return &BatchAccumulator{buckets: map[string]*batchBucket{}}
}

// Configure creates or updates a bucket. Zero values keep prior.
func (a *BatchAccumulator) Configure(bucketID string, maxWait time.Duration, maxSize int, costPerCall, costPerItem float64) error {
	if bucketID == "" {
		return errors.New("bucket_id required")
	}
	if maxWait < 0 || maxSize < 0 {
		return errors.New("maxWait and maxSize must be non-negative")
	}
	if costPerCall < 0 || costPerItem < 0 {
		return errors.New("cost values must be non-negative")
	}
	a.mu.Lock()
	b, ok := a.buckets[bucketID]
	if !ok {
		b = &batchBucket{cfg: batchConfig{
			MaxWait: 50 * time.Millisecond,
			MaxSize: 64,
		}}
		a.buckets[bucketID] = b
	}
	a.mu.Unlock()
	b.mu.Lock()
	if maxWait > 0 {
		b.cfg.MaxWait = maxWait
	}
	if maxSize > 0 {
		b.cfg.MaxSize = maxSize
	}
	if costPerCall > 0 {
		b.cfg.CostPerCall = costPerCall
	}
	if costPerItem > 0 {
		b.cfg.CostPerItem = costPerItem
	}
	b.mu.Unlock()
	return nil
}

// BatchAddResult is ADD's return.
type BatchAddResult struct {
	BatchID string `json:"batch_id"`
	Slot    int    `json:"slot"`
	Ready   bool   `json:"ready"`
	AgeMS   int64  `json:"age_ms"`
}

// Add places one item into the active batch. ready=true tells the
// caller this ADD just hit MAXSIZE (or this is the only item past
// MAXWAIT) — they should call FLUSH right away.
func (a *BatchAccumulator) Add(bucketID, itemID, payload string) (BatchAddResult, error) {
	if bucketID == "" {
		return BatchAddResult{}, errors.New("bucket_id required")
	}
	if itemID == "" {
		return BatchAddResult{}, errors.New("item_id required")
	}
	a.totalAdds.Add(1)
	b := a.bucketOrCreate(bucketID)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		b.batchSeq++
		b.active = &batchInFlight{
			id:        "b" + strconv.Itoa(b.batchSeq),
			createdAt: time.Now().UnixNano(),
		}
	}
	slot := len(b.active.items)
	b.active.items = append(b.active.items, batchItem{
		ItemID: itemID, Payload: payload, TS: time.Now().UnixNano(),
	})
	b.totalItems++
	age := (time.Now().UnixNano() - b.active.createdAt) / int64(time.Millisecond)
	ready := false
	if b.cfg.MaxSize > 0 && len(b.active.items) >= b.cfg.MaxSize {
		ready = true
	}
	if b.cfg.MaxWait > 0 && age >= int64(b.cfg.MaxWait/time.Millisecond) {
		ready = true
	}
	return BatchAddResult{BatchID: b.active.id, Slot: slot, Ready: ready, AgeMS: age}, nil
}

// BatchFlushResult is FLUSH's return.
type BatchFlushResult struct {
	BatchID string         `json:"batch_id"`
	Items   []BatchItemRow `json:"items"`
}

// BatchItemRow is one row of the flushed items.
type BatchItemRow struct {
	ItemID  string `json:"item_id"`
	Payload string `json:"payload"`
}

// Flush returns the active batch's items and rolls the bucket
// forward to an empty active batch. Returns nil items when the
// bucket has nothing.
func (a *BatchAccumulator) Flush(bucketID string) (BatchFlushResult, bool) {
	a.totalFlushes.Add(1)
	a.mu.RLock()
	b, ok := a.buckets[bucketID]
	a.mu.RUnlock()
	if !ok {
		return BatchFlushResult{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil || len(b.active.items) == 0 {
		return BatchFlushResult{}, true
	}
	items := make([]BatchItemRow, len(b.active.items))
	for i, it := range b.active.items {
		items[i] = BatchItemRow{ItemID: it.ItemID, Payload: it.Payload}
	}
	out := BatchFlushResult{BatchID: b.active.id, Items: items}
	b.totalCalls++
	if len(items) > 1 {
		b.savedCalls += int64(len(items) - 1)
	}
	b.active = nil
	return out, true
}

// BatchPeekResult is PEEK's return.
type BatchPeekResult struct {
	BatchID string `json:"batch_id"`
	Size    int    `json:"size"`
	AgeMS   int64  `json:"age_ms"`
	Ready   bool   `json:"ready"`
}

// Peek returns the active batch's metadata without rolling it forward.
func (a *BatchAccumulator) Peek(bucketID string) (BatchPeekResult, bool) {
	a.mu.RLock()
	b, ok := a.buckets[bucketID]
	a.mu.RUnlock()
	if !ok {
		return BatchPeekResult{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		return BatchPeekResult{}, true
	}
	age := (time.Now().UnixNano() - b.active.createdAt) / int64(time.Millisecond)
	ready := false
	if b.cfg.MaxSize > 0 && len(b.active.items) >= b.cfg.MaxSize {
		ready = true
	}
	if b.cfg.MaxWait > 0 && age >= int64(b.cfg.MaxWait/time.Millisecond) {
		ready = true
	}
	return BatchPeekResult{
		BatchID: b.active.id, Size: len(b.active.items), AgeMS: age, Ready: ready,
	}, true
}

// Resolve increments the bucket's per-item counters. The actual
// per-item results are not stored — they're consumed by the caller's
// downstream pipeline; this call just bumps the telemetry.
func (a *BatchAccumulator) Resolve(bucketID, _ string, resultsCount int) error {
	if bucketID == "" {
		return errors.New("bucket_id required")
	}
	if resultsCount < 0 {
		return errors.New("results_count must be non-negative")
	}
	a.totalResolves.Add(1)
	a.mu.RLock()
	_, ok := a.buckets[bucketID]
	a.mu.RUnlock()
	if !ok {
		return errors.New("unknown bucket_id: " + bucketID)
	}
	return nil
}

// Buckets returns every known bucket id, sorted.
func (a *BatchAccumulator) Buckets() []string {
	a.mu.RLock()
	out := make([]string, 0, len(a.buckets))
	for k := range a.buckets {
		out = append(out, k)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a bucket. bucketID="ALL" wipes everything.
func (a *BatchAccumulator) Reset(bucketID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if bucketID == "ALL" {
		n := len(a.buckets)
		a.buckets = map[string]*batchBucket{}
		return n
	}
	if _, ok := a.buckets[bucketID]; ok {
		delete(a.buckets, bucketID)
		return 1
	}
	return 0
}

// BatchBucketStat is per-bucket telemetry inside Stats.
type BatchBucketStat struct {
	BucketID      string  `json:"bucket_id"`
	TotalItems    int64   `json:"total_items"`
	TotalCalls    int64   `json:"total_calls"`
	SavedCalls    int64   `json:"calls_saved"`
	AvgBatch      float64 `json:"avg_batch"`
	SavedUSD      float64 `json:"saved_usd"`
}

// BatchStats is the global snapshot with per-bucket savings.
type BatchStats struct {
	Buckets       int               `json:"buckets"`
	TotalAdds     int64             `json:"total_adds"`
	TotalFlushes  int64             `json:"total_flushes"`
	TotalResolves int64             `json:"total_resolves"`
	PerBucket     []BatchBucketStat `json:"per_bucket"`
}

func (a *BatchAccumulator) Stats() BatchStats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	per := make([]BatchBucketStat, 0, len(a.buckets))
	for id, b := range a.buckets {
		b.mu.Lock()
		row := BatchBucketStat{
			BucketID:   id,
			TotalItems: b.totalItems,
			TotalCalls: b.totalCalls,
			SavedCalls: b.savedCalls,
		}
		if b.totalCalls > 0 {
			row.AvgBatch = float64(b.totalItems) / float64(b.totalCalls)
		}
		// Saved USD = saved_calls × (cost_per_call - cost_per_item)
		row.SavedUSD = float64(b.savedCalls) * b.cfg.CostPerCall
		b.mu.Unlock()
		per = append(per, row)
	}
	sort.Slice(per, func(i, j int) bool { return per[i].BucketID < per[j].BucketID })
	return BatchStats{
		Buckets:       len(a.buckets),
		TotalAdds:     a.totalAdds.Load(),
		TotalFlushes:  a.totalFlushes.Load(),
		TotalResolves: a.totalResolves.Load(),
		PerBucket:     per,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (a *BatchAccumulator) bucketOrCreate(id string) *batchBucket {
	a.mu.RLock()
	b, ok := a.buckets[id]
	a.mu.RUnlock()
	if ok {
		return b
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.buckets[id]; ok {
		return b
	}
	b = &batchBucket{cfg: batchConfig{
		MaxWait: 50 * time.Millisecond,
		MaxSize: 64,
	}}
	a.buckets[id] = b
	return b
}
