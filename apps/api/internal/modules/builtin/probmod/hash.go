// Package probmod implements the Redis Stack probabilistic data
// structures — Bloom filters (BF.*), Cuckoo filters (CF.*), and
// Count-Min Sketches (CMS.*) — as a single NeuroCache module so the
// shared hash + bit-twiddling code lives in one place.
//
// All three structures rely on a fast, well-distributed 64-bit hash.
// We use the FNV-1a 64-bit polynomial (no external dependency, no
// allocations on the hot path) and split the result into two 32-bit
// halves so we can derive k pseudo-independent hash positions via
// double-hashing without re-hashing the input.
package probmod

const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

// hash64 computes FNV-1a over the raw bytes.
func hash64(b []byte) uint64 {
	h := fnvOffset
	for i := 0; i < len(b); i++ {
		h ^= uint64(b[i])
		h *= fnvPrime
	}
	return h
}

// pair returns (h1, h2) — two 32-bit hashes derived from one FNV-1a
// pass. Bloom + Cuckoo + CMS use these to derive their k positions:
//
//	pos[i] = (h1 + i*h2) mod m
//
// Double hashing with two independent halves is asymptotically as good
// as k independent hashes, with one-Nth the work.
func pair(b []byte) (uint64, uint64) {
	h := hash64(b)
	// Mix the two halves so they're independent enough for double-hashing.
	h1 := h
	h2 := h ^ (h >> 33)
	h2 *= 0xff51afd7ed558ccd
	h2 ^= h2 >> 33
	if h2 == 0 {
		h2 = 1
	}
	return h1, h2
}
