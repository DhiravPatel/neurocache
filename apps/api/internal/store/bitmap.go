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
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok, err := s.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	byteIdx := offset / 8
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
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now, LastRead: now}
		s.data[key] = e
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeString)
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
func (s *Store) BitCount(key string, start, end int, hasRange bool) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeString)
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

// BitPos returns the byte-index of the first bit set to `bit` in the
// key, optionally limited to [start,end]. Returns -1 if absent.
func (s *Store) BitPos(key string, bit int, start, end int, hasEnd bool) (int, error) {
	if bit != 0 && bit != 1 {
		return 0, errors.New("bit must be 0 or 1")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeString)
	if err != nil || !ok {
		return -1, err
	}
	data := []byte(e.Str)
	n := len(data)
	if n == 0 {
		return -1, nil
	}
	if !hasEnd {
		end = n - 1
	}
	a, b, empty := normalizeRange(start, end, n)
	if empty {
		return -1, nil
	}
	for i := a; i <= b; i++ {
		for j := 7; j >= 0; j-- {
			if int((data[i]>>j)&1) == bit {
				return i*8 + (7 - j), nil
			}
		}
	}
	return -1, nil
}

// BitOp performs AND / OR / XOR / NOT across source keys, storing the
// result (padded to the longest source) into dst. Returns dst length.
func (s *Store) BitOp(op, dst string, keys []string) (int, error) {
	op = strings.ToUpper(op)
	if op == "NOT" && len(keys) != 1 {
		return 0, errors.New("BITOP NOT must be against a single source key")
	}
	if len(keys) == 0 {
		return 0, errors.New("BITOP requires at least one source key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	srcs := make([][]byte, len(keys))
	maxLen := 0
	for i, k := range keys {
		e, ok, err := s.get(k, TypeString)
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
	if old, ok := s.data[dst]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	now := time.Now()
	e := &Entry{Key: dst, Type: TypeString, Str: string(out), CreatedAt: now, LastRead: now, Bytes: len(dst) + len(out)}
	s.data[dst] = e
	s.bytes.Add(int64(e.Bytes))
	return maxLen, nil
}
