// Package probstruct holds probabilistic data structures shared across
// NeuroCache subsystems. The first occupant is HeavyKeeper, used both
// by the TopK module (TOPK.* command surface) and by the runtime
// HOTKEYS tracker (introspect.HotKeys).
//
// Why a shared package: the algorithm is identical, the call sites
// have different storage / serialization needs. Forking the
// implementation between probmod and introspect would invite
// drift. This package owns the algorithm; callers wrap it.
package probstruct

import (
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"sync"
)

// HeavyKeeper is a streaming top-K estimator with O(width × depth)
// counter memory plus an explicit min-heap of K candidates. Increments
// land in every row's bucket — collisions decay the resident counter
// with probability `decay^residentCount`, so true heavy hitters
// quickly displace incidental noise.
//
// All methods are safe for concurrent use. Internal locking is a
// single mutex — fine for the sampling rates the HOTKEYS tracker
// produces (default 1 in 100 ops). High-contention callers should
// shard across multiple instances.
type HeavyKeeper struct {
	mu      sync.Mutex
	k       int
	width   int
	depth   int
	decay   float64
	rows    [][]hkCounter // depth × width
	heap    []hkItem      // length ≤ k
	idx     map[string]int

	// observations is the cumulative number of Add/IncrBy invocations.
	// Surfaced via Stats() so operators can sanity-check sample rate.
	observations uint64
}

type hkCounter struct {
	fingerprint uint64
	count       uint64
}

// HKItem is one (item, count) row in the top-K heap.
type HKItem struct {
	Item  string
	Count uint64
}

// hkItem is the internal mirror — exported HKItem is what callers see.
type hkItem = HKItem

// New builds a HeavyKeeper with the canonical RedisBloom defaults
// (width=8, depth=7, decay=0.9) when zero values are passed.
func New(k, width, depth int, decay float64) *HeavyKeeper {
	if k <= 0 {
		k = 1
	}
	if width <= 0 {
		width = 8
	}
	if depth <= 0 {
		depth = 7
	}
	if decay <= 0 || decay >= 1 {
		decay = 0.9
	}
	hk := &HeavyKeeper{
		k:     k,
		width: width,
		depth: depth,
		decay: decay,
		idx:   map[string]int{},
		rows:  make([][]hkCounter, depth),
	}
	for i := range hk.rows {
		hk.rows[i] = make([]hkCounter, width)
	}
	return hk
}

// Add increments item by one. Convenience wrapper for IncrBy(item, 1).
func (h *HeavyKeeper) Add(item string) {
	h.IncrBy(item, 1)
}

// IncrBy adds delta to the item's frequency estimate and refreshes its
// position in the top-K heap. Returns the displaced item (empty when
// the call didn't displace anyone).
func (h *HeavyKeeper) IncrBy(item string, delta uint64) string {
	if delta == 0 {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observations += delta
	fp := hash64(item)
	maxCount := uint64(0)
	for i := 0; i < h.depth; i++ {
		col := int((fp + uint64(i)*0x9e3779b97f4a7c15) % uint64(h.width))
		c := &h.rows[i][col]
		switch {
		case c.count == 0:
			c.fingerprint = fp
			c.count = delta
		case c.fingerprint == fp:
			c.count += delta
		default:
			// HeavyKeeper decay rule: each unit of new traffic has
			// probability decay^current of evicting the resident.
			for k := uint64(0); k < delta; k++ {
				if rand.Float64() < math.Pow(h.decay, float64(c.count)) {
					c.count--
					if c.count == 0 {
						c.fingerprint = fp
						c.count = delta - k
						break
					}
				}
			}
		}
		if c.count > maxCount {
			maxCount = c.count
		}
	}
	return h.tryAdmit(item, maxCount)
}

// tryAdmit decides whether the freshly-incremented item now belongs in
// the heap. Caller holds h.mu.
func (h *HeavyKeeper) tryAdmit(item string, count uint64) string {
	if pos, ok := h.idx[item]; ok {
		if count > h.heap[pos].Count {
			h.heap[pos].Count = count
		}
		return ""
	}
	if len(h.heap) < h.k {
		h.heap = append(h.heap, hkItem{Item: item, Count: count})
		h.idx[item] = len(h.heap) - 1
		return ""
	}
	smallest := 0
	for i, e := range h.heap {
		if e.Count < h.heap[smallest].Count {
			smallest = i
		}
	}
	if count <= h.heap[smallest].Count {
		return ""
	}
	displaced := h.heap[smallest].Item
	delete(h.idx, displaced)
	h.heap[smallest] = hkItem{Item: item, Count: count}
	h.idx[item] = smallest
	return displaced
}

// Top returns up to n items sorted by descending count. n ≤ 0 returns
// the entire current heap.
func (h *HeavyKeeper) Top(n int) []HKItem {
	h.mu.Lock()
	out := make([]HKItem, len(h.heap))
	copy(out, h.heap)
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Item < out[j].Item
	})
	if n > 0 && n < len(out) {
		out = out[:n]
	}
	return out
}

// Count returns the estimated frequency for item — min over the depth
// rows that remember its fingerprint. Returns 0 when no row remembers
// the item (CMS upper-bound: counts are ≥ truth, never overstate to a
// degree that hides absence).
func (h *HeavyKeeper) Count(item string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := hash64(item)
	min := uint64(math.MaxUint64)
	for i := 0; i < h.depth; i++ {
		col := int((fp + uint64(i)*0x9e3779b97f4a7c15) % uint64(h.width))
		c := h.rows[i][col]
		if c.fingerprint != fp {
			return 0
		}
		if c.count < min {
			min = c.count
		}
	}
	if min == math.MaxUint64 {
		return 0
	}
	return min
}

// Reset clears every counter and the top-K heap. The dimensions
// (k/width/depth/decay) are preserved.
func (h *HeavyKeeper) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.rows {
		for j := range h.rows[i] {
			h.rows[i][j] = hkCounter{}
		}
	}
	h.heap = h.heap[:0]
	h.idx = map[string]int{}
	h.observations = 0
}

// Resize rebuilds the structure with new K. Counters and heap are
// reset (we can't safely shrink without losing counts). Returns false
// if the requested K matches the current value.
func (h *HeavyKeeper) Resize(k int) bool {
	if k <= 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if k == h.k {
		return false
	}
	h.k = k
	for i := range h.rows {
		for j := range h.rows[i] {
			h.rows[i][j] = hkCounter{}
		}
	}
	h.heap = h.heap[:0]
	h.idx = map[string]int{}
	h.observations = 0
	return true
}

// Stats describes the live estimator state — useful for HOTKEYS STATS.
type Stats struct {
	K            int
	Width        int
	Depth        int
	Decay        float64
	Tracked      int    // current heap occupancy
	Observations uint64 // cumulative Add count
	BytesApprox  int64  // back-of-the-envelope memory cost
}

// Stats reports configuration and live counts for observability.
func (h *HeavyKeeper) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		K:            h.k,
		Width:        h.width,
		Depth:        h.depth,
		Decay:        h.decay,
		Tracked:      len(h.heap),
		Observations: h.observations,
		BytesApprox:  int64(h.width*h.depth)*16 + int64(h.k)*32,
	}
}

// hash64 is FNV-1a — stable across goroutines and cheap. We don't need
// crypto strength, only a reasonable distribution across counters.
func hash64(s string) uint64 {
	hsh := fnv.New64a()
	_, _ = hsh.Write([]byte(s))
	return hsh.Sum64()
}
