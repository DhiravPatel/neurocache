package probmod

import (
	"encoding/binary"
	"errors"
	"math/rand"
)

// Cuckoo is a cuckoo filter — supports deletion and counting (which
// classic Bloom filters don't), at the cost of slightly higher memory
// per element. Each bucket holds a fixed number of fingerprints; on
// collision we evict and re-insert until either we settle or we hit
// MaxIterations and the insert fails.
type Cuckoo struct {
	Buckets       []bucket
	BucketSize    uint8
	NumBuckets    uint64
	MaxIterations uint16
	Expansion     uint8
	Count         uint64
}

type bucket struct {
	Fingerprints []uint16 // 0 == empty slot
}

// NewCuckoo allocates a cuckoo filter with the given capacity. The
// load factor sweet spot is around 0.95 with 4-slot buckets, so we
// round numBuckets up to capacity / bucketSize / 0.95.
func NewCuckoo(capacity uint64, bucketSize uint8, maxIter uint16, expansion uint8) (*Cuckoo, error) {
	if capacity == 0 {
		return nil, errors.New("capacity must be > 0")
	}
	if bucketSize == 0 {
		bucketSize = 4
	}
	if maxIter == 0 {
		maxIter = 500
	}
	if expansion == 0 {
		expansion = 1
	}
	num := uint64(float64(capacity) / float64(bucketSize) / 0.95)
	if num < 2 {
		num = 2
	}
	num = nextPow2(num)
	c := &Cuckoo{
		BucketSize: bucketSize, NumBuckets: num,
		MaxIterations: maxIter, Expansion: expansion,
		Buckets: make([]bucket, num),
	}
	for i := range c.Buckets {
		c.Buckets[i] = bucket{Fingerprints: make([]uint16, bucketSize)}
	}
	return c, nil
}

// Add inserts an item. Returns ok=false when MaxIterations was hit
// (filter effectively full). A duplicate insertion is allowed — use
// AddNX to reject duplicates.
func (c *Cuckoo) Add(item []byte) bool {
	fp, i1, i2 := c.fingerprintAndIndices(item)
	if c.tryInsert(i1, fp) || c.tryInsert(i2, fp) {
		c.Count++
		return true
	}
	// kick a random fingerprint until we settle or hit MaxIterations.
	idx := i1
	if rand.Intn(2) == 0 {
		idx = i2
	}
	for n := uint16(0); n < c.MaxIterations; n++ {
		slot := rand.Intn(int(c.BucketSize))
		fp, c.Buckets[idx].Fingerprints[slot] = c.Buckets[idx].Fingerprints[slot], fp
		idx = c.altIndex(idx, fp)
		if c.tryInsert(idx, fp) {
			c.Count++
			return true
		}
	}
	return false
}

// AddNX inserts only if no fingerprint match is present. False
// positives are possible (cuckoo's accuracy story).
func (c *Cuckoo) AddNX(item []byte) bool {
	if c.Contains(item) {
		return false
	}
	return c.Add(item)
}

// Contains tests for the item.
func (c *Cuckoo) Contains(item []byte) bool {
	fp, i1, i2 := c.fingerprintAndIndices(item)
	return c.bucketHas(i1, fp) || c.bucketHas(i2, fp)
}

// Count returns how many times the item was inserted (matching
// fingerprint count).
func (c *Cuckoo) CountItem(item []byte) uint64 {
	fp, i1, i2 := c.fingerprintAndIndices(item)
	return c.bucketCount(i1, fp) + c.bucketCount(i2, fp)
}

// Del removes one matching fingerprint. Returns true on success.
// Cuckoo filters can over-delete due to fingerprint collisions; this
// matches Redis's documented behaviour.
func (c *Cuckoo) Del(item []byte) bool {
	fp, i1, i2 := c.fingerprintAndIndices(item)
	if c.bucketDelete(i1, fp) || c.bucketDelete(i2, fp) {
		c.Count--
		return true
	}
	return false
}

func (c *Cuckoo) tryInsert(idx uint64, fp uint16) bool {
	for i, slot := range c.Buckets[idx].Fingerprints {
		if slot == 0 {
			c.Buckets[idx].Fingerprints[i] = fp
			return true
		}
	}
	return false
}
func (c *Cuckoo) bucketHas(idx uint64, fp uint16) bool {
	for _, slot := range c.Buckets[idx].Fingerprints {
		if slot == fp {
			return true
		}
	}
	return false
}
func (c *Cuckoo) bucketCount(idx uint64, fp uint16) uint64 {
	var n uint64
	for _, slot := range c.Buckets[idx].Fingerprints {
		if slot == fp {
			n++
		}
	}
	return n
}
func (c *Cuckoo) bucketDelete(idx uint64, fp uint16) bool {
	for i, slot := range c.Buckets[idx].Fingerprints {
		if slot == fp {
			c.Buckets[idx].Fingerprints[i] = 0
			return true
		}
	}
	return false
}

// fingerprintAndIndices derives the 16-bit fingerprint + the two
// candidate buckets from the item's hash. Fingerprint of 0 is reserved
// as "empty", so we map it to 1.
func (c *Cuckoo) fingerprintAndIndices(item []byte) (uint16, uint64, uint64) {
	h := hash64(item)
	fp := uint16(h)
	if fp == 0 {
		fp = 1
	}
	mask := c.NumBuckets - 1
	i1 := h & mask
	i2 := c.altIndex(i1, fp)
	return fp, i1, i2
}

func (c *Cuckoo) altIndex(idx uint64, fp uint16) uint64 {
	mask := c.NumBuckets - 1
	return (idx ^ hash64(uint16ToBytes(fp))) & mask
}

func uint16ToBytes(v uint16) []byte {
	return []byte{byte(v), byte(v >> 8)}
}

func nextPow2(n uint64) uint64 {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}

// Marshal/Unmarshal for AOF + DUMP/RESTORE.
const cuckooVersion = 1

func (c *Cuckoo) Marshal() ([]byte, error) {
	out := make([]byte, 0, 64+int(c.NumBuckets)*int(c.BucketSize)*2)
	out = append(out, cuckooVersion)
	out = append(out, c.BucketSize)
	out = appendU64(out, c.NumBuckets)
	out = append(out, byte(c.MaxIterations), byte(c.MaxIterations>>8))
	out = append(out, c.Expansion)
	out = appendU64(out, c.Count)
	for _, b := range c.Buckets {
		for _, fp := range b.Fingerprints {
			out = append(out, byte(fp), byte(fp>>8))
		}
	}
	return out, nil
}

func UnmarshalCuckoo(in []byte) (*Cuckoo, error) {
	r := newReader(in)
	v, err := r.u8()
	if err != nil {
		return nil, err
	}
	if v != cuckooVersion {
		return nil, errors.New("unsupported cuckoo version")
	}
	bs, err := r.u8()
	if err != nil {
		return nil, err
	}
	nb, err := r.u64()
	if err != nil {
		return nil, err
	}
	miLo, err := r.u8()
	if err != nil {
		return nil, err
	}
	miHi, err := r.u8()
	if err != nil {
		return nil, err
	}
	exp, err := r.u8()
	if err != nil {
		return nil, err
	}
	count, err := r.u64()
	if err != nil {
		return nil, err
	}
	c := &Cuckoo{
		BucketSize: bs, NumBuckets: nb,
		MaxIterations: uint16(miLo) | uint16(miHi)<<8,
		Expansion:     exp, Count: count,
	}
	c.Buckets = make([]bucket, nb)
	for i := range c.Buckets {
		c.Buckets[i] = bucket{Fingerprints: make([]uint16, bs)}
		for j := range c.Buckets[i].Fingerprints {
			lo, err := r.u8()
			if err != nil {
				return nil, err
			}
			hi, err := r.u8()
			if err != nil {
				return nil, err
			}
			c.Buckets[i].Fingerprints[j] = uint16(lo) | uint16(hi)<<8
		}
	}
	return c, nil
}

// silence the binary import on older toolchains where appendU64 isn't
// inlined enough to trigger the use-check.
var _ = binary.LittleEndian
