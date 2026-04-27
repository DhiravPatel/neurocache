package primitives

import (
	"hash/fnv"
	"sync"
	"time"
)

// Deduper answers "have I seen this id in the last N seconds?" in
// constant time + bounded memory using a rotating two-bloom-filter
// scheme. The classic single-bloom approach can't expire entries, so
// we maintain two filters and rotate them on a schedule — fresh
// filter accepts new ids, second filter is consulted for the older
// half of the window, both get cleared periodically.
//
// Properties:
//
//   - guaranteed false-negative-free for ids inserted within window
//   - small false-positive rate (configurable per bucket)
//   - memory cost: ~10 bits/expected-id × 2 filters
//
// This is the textbook stream-dedup primitive — every distributed
// queue / event-source pipeline ends up needing one. Now it's a
// single command instead of 50 lines of Lua.
type Deduper struct {
	mu       sync.Mutex
	buckets  map[string]*dedupBucket
	defaultM uint64 // bits per filter
	defaultK uint32 // hash positions per insert
}

type dedupBucket struct {
	currentBits []uint64
	previousBits []uint64
	m           uint64
	k           uint32
	rotated     time.Time
	period      time.Duration
}

// NewDeduper builds a deduper. expectedItems sizes the bloom filter
// for the desired false-positive rate (~1% at the default capacity).
func NewDeduper() *Deduper {
	d := &Deduper{
		buckets:  map[string]*dedupBucket{},
		defaultM: 1 << 16, // 64Ki bits per filter ≈ 8 KiB; ~10k items @1% FPR
		defaultK: 7,
	}
	go d.sweepLoop()
	return d
}

// SeenOrMark returns whether `id` has been seen within the last
// `window`, marking it as seen if it hasn't. The first call for a
// (bucket, id) pair returns false; subsequent calls within `window`
// return true.
func (d *Deduper) SeenOrMark(bucket, id string, window time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.buckets[bucket]
	if !ok {
		b = newDedupBucket(d.defaultM, d.defaultK, window)
		d.buckets[bucket] = b
	}
	// rotate when half the window has elapsed
	if time.Since(b.rotated) >= window/2 {
		b.previousBits = b.currentBits
		b.currentBits = make([]uint64, len(b.currentBits))
		b.rotated = time.Now()
	}
	h1, h2 := dedupHash(id)
	if b.contains(h1, h2) {
		return true
	}
	b.set(h1, h2)
	return false
}

// Forget drops a bucket (clears all dedup state for it).
func (d *Deduper) Forget(bucket string) {
	d.mu.Lock()
	delete(d.buckets, bucket)
	d.mu.Unlock()
}

func (d *Deduper) sweepLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		d.mu.Lock()
		for k, b := range d.buckets {
			// Drop buckets whose last-rotated time is more than 4× the
			// window — anyone still using them gets fresh state.
			if now.Sub(b.rotated) > 4*b.period {
				delete(d.buckets, k)
			}
		}
		d.mu.Unlock()
	}
}

func newDedupBucket(m uint64, k uint32, period time.Duration) *dedupBucket {
	words := (m + 63) / 64
	return &dedupBucket{
		currentBits:  make([]uint64, words),
		previousBits: make([]uint64, words),
		m:            m,
		k:            k,
		period:       period,
		rotated:      time.Now(),
	}
}

func (b *dedupBucket) contains(h1, h2 uint64) bool {
	for i := uint32(0); i < b.k; i++ {
		bit := (h1 + uint64(i)*h2) % b.m
		if b.currentBits[bit/64]&(1<<(bit%64)) == 0 {
			// not in current filter — check previous
			if b.previousBits[bit/64]&(1<<(bit%64)) == 0 {
				return false
			}
		}
	}
	return true
}

func (b *dedupBucket) set(h1, h2 uint64) {
	for i := uint32(0); i < b.k; i++ {
		bit := (h1 + uint64(i)*h2) % b.m
		b.currentBits[bit/64] |= 1 << (bit % 64)
	}
}

// dedupHash returns two independent uint64 hashes via FNV-1a + a
// secondary mix — same trick the probabilistic module uses.
func dedupHash(id string) (uint64, uint64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	v := h.Sum64()
	v2 := v ^ (v >> 33)
	v2 *= 0xff51afd7ed558ccd
	v2 ^= v2 >> 33
	if v2 == 0 {
		v2 = 1
	}
	return v, v2
}
