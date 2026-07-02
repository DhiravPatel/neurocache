package store

import (
	"errors"
	"math/bits"
	"strings"
	"time"
)

// Bitmaps are strings treated as packed bits. Offsets are big-endian —
// bit 0 is the high bit of byte 0 — matching Redis's wire semantics.

// SetBit writes bit at offset. Extends the string with zero bytes as
// needed. Returns the previous bit value.
func (s *Store) SetBit(key string, offset int64, value int) (int, error) {
	if offset < 0 {
		return 0, errors.New("bit offset is not an integer or out of range")
	}
	if value != 0 && value != 1 {
		return 0, errors.New("bit is not an integer or out of range")
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok, err := sh.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	byteIdx := offset / 8
	// Cap the bitmap at Redis's 512 MiB ceiling so a huge offset can't
	// pre-grow a multi-gigabyte buffer and OOM the server.
	if byteIdx >= maxStringBytes {
		return 0, errors.New("bit offset is not an integer or out of range")
	}
	var cur []byte
	if ok {
		cur = []byte(e.Str)
	}
	if int64(len(cur)) <= byteIdx {
		grown := make([]byte, byteIdx+1)
		copy(grown, cur)
		cur = grown
	}
	bitIdx := uint(7 - offset%8)
	prev := int((cur[byteIdx] >> bitIdx) & 1)
	if value == 1 {
		cur[byteIdx] |= 1 << bitIdx
	} else {
		cur[byteIdx] &^= 1 << bitIdx
	}
	if !ok {
		now := time.Now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now.UnixNano(), LastRead: now.UnixNano()}
		sh.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = string(cur)
	e.Bytes = len(key) + len(cur)
	s.bytes.Add(int64(e.Bytes))
	return prev, nil
}

// GetBit reads bit at offset (0 if key missing or offset past end).
func (s *Store) GetBit(key string, offset int64) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil || !ok {
		return 0, err
	}
	byteIdx := offset / 8
	if byteIdx < 0 || int64(len(e.Str)) <= byteIdx {
		return 0, nil
	}
	bitIdx := uint(7 - offset%8)
	return int((e.Str[byteIdx] >> bitIdx) & 1), nil
}

// BitCount counts set bits in [start,end] (byte indices). Negative
// indices count from the end. end < start returns 0.
// BitCount counts set bits in key, optionally within [start,end]. When
// bitRange is false the range is measured in bytes (the default); when
// true, start/end are bit offsets (BITCOUNT ... BIT, Redis 7.0). Both
// endpoints accept negative indices counting back from the end.
func (s *Store) BitCount(key string, start, end int, hasRange, bitRange bool) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil || !ok {
		return 0, err
	}
	data := []byte(e.Str)
	n := len(data)
	if n == 0 {
		return 0, nil
	}
	if !hasRange {
		start, end = 0, n-1
	}
	if bitRange {
		a, b, empty := normalizeRange(start, end, n*8)
		if empty {
			return 0, nil
		}
		return countBitsInRange(data, a, b), nil
	}
	a, b, empty := normalizeRange(start, end, n)
	if empty {
		return 0, nil
	}
	count := 0
	for i := a; i <= b; i++ {
		count += bits.OnesCount8(data[i])
	}
	return count, nil
}

// BitPos returns the bit-index of the first bit set to `bit` in the key,
// optionally limited to [start,end]. When bitRange is false start/end are
// byte offsets (the default); when true they are bit offsets (BITPOS ...
// BIT, Redis 7.0). Returns -1 if absent. Matches Redis's rule that a
// search for a clear bit (bit==0) with no explicit end returns the first
// bit past the string when every scanned bit is set.
func (s *Store) BitPos(key string, bit int, start, end int, hasEnd, bitRange bool) (int, error) {
	if bit != 0 && bit != 1 {
		return 0, errors.New("bit must be 0 or 1")
	}
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeString)
	if err != nil || !ok {
		return -1, err
	}
	data := []byte(e.Str)
	n := len(data)
	if n == 0 {
		return -1, nil
	}
	var a, b int
	var empty bool
	if bitRange {
		if !hasEnd {
			end = n*8 - 1
		}
		a, b, empty = normalizeRange(start, end, n*8)
	} else {
		if !hasEnd {
			end = n - 1
		}
		a, b, empty = normalizeRange(start, end, n)
		// Widen the byte range to its covered bit range.
		a, b = a*8, b*8+7
	}
	if empty {
		return -1, nil
	}
	for i := a; i <= b; i++ {
		if bitAt(data, i) == bit {
			return i, nil
		}
	}
	// Redis quirk: hunting for a clear bit across a string that is all
	// 1s, with no caller-supplied end, reports the first bit off the
	// right edge rather than -1 (the string is logically zero-padded).
	if bit == 0 && !hasEnd {
		return n * 8, nil
	}
	return -1, nil
}

// bitAt returns bit i of data using Redis bit numbering: bit 0 is the
// most-significant bit of byte 0.
func bitAt(data []byte, i int) int {
	return int((data[i>>3] >> (7 - uint(i&7))) & 1)
}

// countBitsInRange counts set bits in the inclusive bit range [a,b] of
// data, using Redis bit numbering. Partial edge bytes are masked; whole
// interior bytes use a hardware popcount.
func countBitsInRange(data []byte, a, b int) int {
	firstByte, lastByte := a>>3, b>>3
	if firstByte == lastByte {
		// leading bits before a and trailing bits after b are masked off.
		mask := byte(0xff>>uint(a&7)) & byte(0xff<<uint(7-(b&7)))
		return bits.OnesCount8(data[firstByte] & mask)
	}
	count := bits.OnesCount8(data[firstByte] & byte(0xff>>uint(a&7)))
	for i := firstByte + 1; i < lastByte; i++ {
		count += bits.OnesCount8(data[i])
	}
	count += bits.OnesCount8(data[lastByte] & byte(0xff<<uint(7-(b&7))))
	return count
}

// BitOp performs AND / OR / XOR / NOT across source keys, storing the
// result (padded to the longest source) into dst. Locks every involved
// shard up front (in canonical order) so the read of every src and the
// write of dst happen atomically with respect to other writers.
func (s *Store) BitOp(op, dst string, keys []string) (int, error) {
	op = strings.ToUpper(op)
	if op == "NOT" && len(keys) != 1 {
		return 0, errors.New("BITOP NOT must be against a single source key")
	}
	if len(keys) == 0 {
		return 0, errors.New("BITOP requires at least one source key")
	}
	// Collect every shard the operation touches and lock them in
	// canonical order — same mechanic as lockTwoW, generalized.
	allKeys := append([]string{dst}, keys...)
	involved := s.shardsFor(allKeys)
	unlock := s.lockShardsW(involved)
	defer unlock()
	shD := s.shardForKey(dst)

	srcs := make([][]byte, len(keys))
	maxLen := 0
	for i, k := range keys {
		shS := s.shardForKey(k)
		e, ok, err := shS.get(k, TypeString)
		if err != nil {
			return 0, err
		}
		if ok {
			srcs[i] = []byte(e.Str)
		}
		if len(srcs[i]) > maxLen {
			maxLen = len(srcs[i])
		}
	}
	out := make([]byte, maxLen)
	switch op {
	case "AND":
		for i := 0; i < maxLen; i++ {
			b := byte(0xff)
			for _, src := range srcs {
				if i >= len(src) {
					b = 0
					break
				}
				b &= src[i]
			}
			out[i] = b
		}
	case "OR":
		for i := 0; i < maxLen; i++ {
			var b byte
			for _, src := range srcs {
				if i < len(src) {
					b |= src[i]
				}
			}
			out[i] = b
		}
	case "XOR":
		for i := 0; i < maxLen; i++ {
			var b byte
			for _, src := range srcs {
				if i < len(src) {
					b ^= src[i]
				}
			}
			out[i] = b
		}
	case "NOT":
		for i := 0; i < maxLen; i++ {
			out[i] = ^srcs[0][i]
		}
	default:
		return 0, errors.New("unknown BITOP operation")
	}
	// write dst
	if old, ok := shD.data[dst]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	now := time.Now()
	e := &Entry{Key: dst, Type: TypeString, Str: string(out), CreatedAt: now.UnixNano(), LastRead: now.UnixNano(), Bytes: len(dst) + len(out)}
	shD.data[dst] = e
	s.bytes.Add(int64(e.Bytes))
	return maxLen, nil
}
