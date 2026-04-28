package store

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"math/bits"
	"time"
)

// HyperLogLog with 14-bit precision (same as Redis) — 2^14 = 16384
// 6-bit registers, ~12 KiB per key, ~0.81% standard error on cardinality
// estimates.
const (
	hllP         = 14
	hllM         = 1 << hllP // 16384
	hllRegBits   = 6
	hllRegMask   = (1 << hllRegBits) - 1
	hllBytesDens = (hllM * hllRegBits) / 8 // 12288
)

// hllMagic prefixes the serialized HLL. The fixed 6 bytes make the
// string recognizable so ordinary string commands still round-trip it.
var hllMagic = []byte{'H', 'Y', 'L', 'L', 0x00, 0x00}

// newHLL allocates an empty dense HLL as a byte buffer: magic + 16384
// packed 6-bit registers.
func newHLL() []byte {
	buf := make([]byte, len(hllMagic)+hllBytesDens)
	copy(buf, hllMagic)
	return buf
}

// isHLL reports whether bytes start with the HLL magic.
func isHLL(b []byte) bool {
	if len(b) < len(hllMagic) {
		return false
	}
	for i, m := range hllMagic {
		if b[i] != m {
			return false
		}
	}
	return true
}

// getReg / setReg pack/unpack 6-bit registers into the dense buffer.
func getReg(buf []byte, idx int) uint8 {
	bitOff := idx * hllRegBits
	byteOff := len(hllMagic) + bitOff/8
	shift := bitOff % 8
	var v uint16
	v = uint16(buf[byteOff])
	if byteOff+1 < len(buf) {
		v |= uint16(buf[byteOff+1]) << 8
	}
	return uint8((v >> shift) & hllRegMask)
}

func setReg(buf []byte, idx int, val uint8) {
	bitOff := idx * hllRegBits
	byteOff := len(hllMagic) + bitOff/8
	shift := bitOff % 8
	mask := uint16(hllRegMask) << shift
	var v uint16
	v = uint16(buf[byteOff])
	if byteOff+1 < len(buf) {
		v |= uint16(buf[byteOff+1]) << 8
	}
	v = (v &^ mask) | (uint16(val) << shift)
	buf[byteOff] = byte(v & 0xff)
	if byteOff+1 < len(buf) {
		buf[byteOff+1] = byte(v >> 8)
	}
}

// hllHash produces a 64-bit hash with avalanche. FNV-1a alone leaves
// correlated bit patterns for sequential inputs ("item-1", "item-2", …),
// which biases HLL register distribution. Running the FNV output
// through the splitmix64 finalizer smooths the bits out and keeps the
// cardinality estimate honest.
func hllHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// hllRegAndCount splits a 64-bit hash into (register index, rank):
// - index: top P bits of the hash
// - rank:  1 + number of leading zeros in the remaining (64-P) bits,
//          capped at (64-P+1) when those bits are all zero
func hllRegAndCount(h uint64) (int, uint8) {
	idx := int(h >> (64 - hllP))
	remaining := h << hllP
	if remaining == 0 {
		return idx, uint8(64 - hllP + 1)
	}
	return idx, uint8(bits.LeadingZeros64(remaining) + 1)
}

// PFAdd inserts members into the HLL at key. Returns 1 if the internal
// register state changed (cardinality estimate moved), else 0.
func (s *Store) PFAdd(key string, members ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeString)
	if err != nil {
		return 0, err
	}
	var buf []byte
	if ok && isHLL([]byte(e.Str)) {
		buf = []byte(e.Str)
	} else if ok {
		return 0, errors.New("WRONGTYPE Key is not a valid HyperLogLog string value")
	} else {
		buf = newHLL()
	}
	changed := 0
	for _, m := range members {
		idx, count := hllRegAndCount(hllHash(m))
		if cur := getReg(buf, idx); count > cur {
			setReg(buf, idx, count)
			changed = 1
		}
	}
	if !ok {
		now := time.Now()
		e = &Entry{Key: key, Type: TypeString, CreatedAt: now, LastRead: now}
		s.data[key] = e
	} else {
		s.bytes.Add(-int64(e.Bytes))
	}
	e.Str = string(buf)
	e.Bytes = len(key) + len(buf)
	s.bytes.Add(int64(e.Bytes))
	return changed, nil
}

// PFCount estimates cardinality. When given multiple keys, it estimates
// the cardinality of their union without modifying any of them.
func (s *Store) PFCount(keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, errors.New("PFCOUNT requires at least one key")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	merged := newHLL()
	for _, k := range keys {
		e, ok, err := s.get(k, TypeString)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		buf := []byte(e.Str)
		if !isHLL(buf) {
			return 0, errors.New("WRONGTYPE Key is not a valid HyperLogLog string value")
		}
		for i := 0; i < hllM; i++ {
			a := getReg(merged, i)
			b := getReg(buf, i)
			if b > a {
				setReg(merged, i, b)
			}
		}
	}
	return hllEstimate(merged), nil
}

// PFMerge computes the union of srcs and stores it at dst.
func (s *Store) PFMerge(dst string, srcs ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf []byte
	if e, ok, err := s.get(dst, TypeString); err != nil {
		return err
	} else if ok && isHLL([]byte(e.Str)) {
		buf = []byte(e.Str)
	} else {
		buf = newHLL()
	}
	for _, k := range srcs {
		e, ok, err := s.get(k, TypeString)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		src := []byte(e.Str)
		if !isHLL(src) {
			return errors.New("WRONGTYPE Key is not a valid HyperLogLog string value")
		}
		for i := 0; i < hllM; i++ {
			a := getReg(buf, i)
			b := getReg(src, i)
			if b > a {
				setReg(buf, i, b)
			}
		}
	}
	if old, ok := s.data[dst]; ok {
		s.bytes.Add(-int64(old.Bytes))
	}
	now := time.Now()
	e := &Entry{Key: dst, Type: TypeString, Str: string(buf), CreatedAt: now, LastRead: now, Bytes: len(dst) + len(buf)}
	s.data[dst] = e
	s.bytes.Add(int64(e.Bytes))
	return nil
}

// PFRegisters returns the 16384 register values backing the HLL at key,
// or false when the key is missing or not an HLL. Used by PFDEBUG so
// monitoring tools can inspect dense-encoding state without reaching
// past the public store API.
func (s *Store) PFRegisters(key string) ([]uint8, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeString)
	if err != nil || !ok {
		return nil, false
	}
	buf := []byte(e.Str)
	if !isHLL(buf) {
		return nil, false
	}
	out := make([]uint8, hllM)
	for i := 0; i < hllM; i++ {
		out[i] = getReg(buf, i)
	}
	return out, true
}

// hllEstimate applies the HyperLogLog formula with small- and
// large-range corrections. Good enough to match Redis's estimates for
// the dense encoding within single-digit percent error.
func hllEstimate(buf []byte) int64 {
	m := float64(hllM)
	var sum float64
	zeros := 0
	for i := 0; i < hllM; i++ {
		r := getReg(buf, i)
		sum += 1.0 / float64(uint64(1)<<r)
		if r == 0 {
			zeros++
		}
	}
	// bias-corrected constant for m=16384 (Flajolet et al.)
	alpha := 0.7213 / (1 + 1.079/m)
	est := alpha * m * m / sum
	if est <= 2.5*m && zeros != 0 {
		est = m * math.Log(m/float64(zeros))
	}
	return int64(est + 0.5)
}

// _ keeps binary imported if it becomes necessary to persist the HLL
// with an explicit format header in the future.
var _ = binary.LittleEndian
