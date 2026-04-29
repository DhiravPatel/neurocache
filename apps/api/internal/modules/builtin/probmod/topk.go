package probmod

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"sort"
)

// TopK is a HeavyKeeper-based top-K estimator. The structure tracks
// the K most-frequent items in a stream using `width × depth` counters
// arranged like a Count-Min Sketch, plus an explicit min-heap of K
// candidates. Hits land in every row's bucket; counters decay on
// collision following the HeavyKeeper rule
// (`pow(decay, current_count)`), which steers the heap toward the
// true heavy hitters far better than naive top-K-from-CMS.
//
// We expose the standard RedisBloom TOPK.* surface:
//
//	TOPK.RESERVE key K [width depth decay]
//	TOPK.ADD key item [item ...]            -> per-item replaced-id (or nil)
//	TOPK.INCRBY key item delta [item delta ...]
//	TOPK.QUERY key item [item ...]          -> per-item bool
//	TOPK.COUNT key item [item ...]          -> per-item estimated count
//	TOPK.LIST key [WITHCOUNT]               -> the K members
//	TOPK.INFO key
//
// Defaults match RedisBloom: width=8, depth=7, decay=0.9.
type TopK struct {
	K       uint32
	Width   uint32
	Depth   uint32
	Decay   float64

	rows []row // depth × width
	heap heapEntries
	idx  map[string]int // item -> heap index
}

type counter struct {
	fingerprint uint64
	count       uint64
}

type row struct {
	cells []counter
}

// heapEntries is a min-heap by count; the heap header always lists
// the smallest-count member, so a fresh insert just compares against
// it. Implemented as a sorted slice for simplicity — at K ≤ 1000 the
// O(K log K) per-add cost is dominated by hashing anyway.
type heapEntries struct {
	items []heapItem
}

type heapItem struct {
	Item  string
	Count uint64
}

// NewTopK allocates a fresh top-K. K is required; width/depth/decay
// fall back to RedisBloom defaults when 0.
func NewTopK(k, width, depth uint32, decay float64) (*TopK, error) {
	if k == 0 {
		return nil, errors.New("K must be > 0")
	}
	if width == 0 {
		width = 8
	}
	if depth == 0 {
		depth = 7
	}
	if decay <= 0 || decay >= 1 {
		decay = 0.9
	}
	t := &TopK{K: k, Width: width, Depth: depth, Decay: decay, idx: map[string]int{}}
	t.rows = make([]row, depth)
	for i := range t.rows {
		t.rows[i].cells = make([]counter, width)
	}
	return t, nil
}

// Add inserts item with weight 1. Returns the displaced item ("" if
// the heap had room or item is now in the top-K).
func (t *TopK) Add(item string) string {
	return t.IncrBy(item, 1)
}

// IncrBy adds `count` to the item's estimated frequency and returns
// the displaced top-K member (or "").
func (t *TopK) IncrBy(item string, count uint64) string {
	fp := hash64([]byte(item))
	maxCount := uint64(0)
	for i := uint32(0); i < t.Depth; i++ {
		col := uint32(fp+uint64(i)*0x9e3779b97f4a7c15) % t.Width
		c := &t.rows[i].cells[col]
		switch {
		case c.count == 0:
			c.fingerprint = fp
			c.count = count
		case c.fingerprint == fp:
			c.count += count
		default:
			// HeavyKeeper decay: each unit of new traffic has a
			// `decay^count` probability of evicting the resident.
			for k := uint64(0); k < count; k++ {
				if rand.Float64() < math.Pow(t.Decay, float64(c.count)) {
					c.count--
					if c.count == 0 {
						c.fingerprint = fp
						c.count = count - k
						break
					}
				}
			}
		}
		if c.count > maxCount {
			maxCount = c.count
		}
	}
	return t.tryAdmit(item, maxCount)
}

// Query returns whether item is currently in the heap.
func (t *TopK) Query(item string) bool {
	_, ok := t.idx[item]
	return ok
}

// Count returns the estimated frequency for item (min over rows; 0 if
// no row remembers the item).
func (t *TopK) Count(item string) uint64 {
	fp := hash64([]byte(item))
	min := uint64(math.MaxUint64)
	for i := uint32(0); i < t.Depth; i++ {
		col := uint32(fp+uint64(i)*0x9e3779b97f4a7c15) % t.Width
		c := t.rows[i].cells[col]
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

// List returns the current top-K, sorted by descending count.
func (t *TopK) List() []heapItem {
	out := make([]heapItem, len(t.heap.items))
	copy(out, t.heap.items)
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// MemUsage approximates byte cost (counters + heap + index).
func (t *TopK) MemUsage() int64 {
	return int64(uint64(t.Width)*uint64(t.Depth)*16 + uint64(t.K)*32)
}

// tryAdmit decides whether the freshly-incremented item now belongs in
// the top-K heap. Returns the displaced member's name (or "").
func (t *TopK) tryAdmit(item string, count uint64) string {
	if pos, ok := t.idx[item]; ok {
		// Already a member — refresh count (HeavyKeeper uses min over rows
		// as the estimate; here we use the latest observed peak).
		if count > t.heap.items[pos].Count {
			t.heap.items[pos].Count = count
		}
		return ""
	}
	if uint32(len(t.heap.items)) < t.K {
		t.heap.items = append(t.heap.items, heapItem{Item: item, Count: count})
		t.idx[item] = len(t.heap.items) - 1
		return ""
	}
	// Find the smallest member; if our count beats it, displace.
	smallest := 0
	for i, e := range t.heap.items {
		if e.Count < t.heap.items[smallest].Count {
			smallest = i
		}
	}
	if count <= t.heap.items[smallest].Count {
		return ""
	}
	displaced := t.heap.items[smallest].Item
	delete(t.idx, displaced)
	t.heap.items[smallest] = heapItem{Item: item, Count: count}
	t.idx[item] = smallest
	return displaced
}

// Marshal/Unmarshal — version-tagged binary for AOF/RDB round-trip.

const topkVersion = 1

func (t *TopK) Marshal() ([]byte, error) {
	out := make([]byte, 0, 64+int(t.Width*t.Depth)*16+len(t.heap.items)*32)
	out = append(out, topkVersion)
	out = appendU32(out, t.K)
	out = appendU32(out, t.Width)
	out = appendU32(out, t.Depth)
	out = appendF64(out, t.Decay)
	for _, r := range t.rows {
		for _, c := range r.cells {
			out = appendU64(out, c.fingerprint)
			out = appendU64(out, c.count)
		}
	}
	out = appendU32(out, uint32(len(t.heap.items)))
	for _, h := range t.heap.items {
		out = appendU32(out, uint32(len(h.Item)))
		out = append(out, h.Item...)
		out = appendU64(out, h.Count)
	}
	return out, nil
}

func UnmarshalTopK(in []byte) (*TopK, error) {
	r := newReader(in)
	v, err := r.u8()
	if err != nil || v != topkVersion {
		return nil, errors.New("unsupported topk version")
	}
	t := &TopK{idx: map[string]int{}}
	if t.K, err = r.u32(); err != nil {
		return nil, err
	}
	if t.Width, err = r.u32(); err != nil {
		return nil, err
	}
	if t.Depth, err = r.u32(); err != nil {
		return nil, err
	}
	if t.Decay, err = r.f64(); err != nil {
		return nil, err
	}
	t.rows = make([]row, t.Depth)
	for i := range t.rows {
		t.rows[i].cells = make([]counter, t.Width)
		for j := range t.rows[i].cells {
			fp, err := r.u64()
			if err != nil {
				return nil, err
			}
			cnt, err := r.u64()
			if err != nil {
				return nil, err
			}
			t.rows[i].cells[j] = counter{fingerprint: fp, count: cnt}
		}
	}
	hn, err := r.u32()
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < hn; i++ {
		nlen, err := r.u32()
		if err != nil {
			return nil, err
		}
		name, err := r.bytes(int(nlen))
		if err != nil {
			return nil, err
		}
		cnt, err := r.u64()
		if err != nil {
			return nil, err
		}
		t.heap.items = append(t.heap.items, heapItem{Item: string(name), Count: cnt})
		t.idx[string(name)] = int(i)
	}
	return t, nil
}

// reader.bytes already exists in bloom.go; we just need a small string
// reader on top of u32-prefixed payloads.
var _ = binary.LittleEndian // keep import alive when subset codecs change
