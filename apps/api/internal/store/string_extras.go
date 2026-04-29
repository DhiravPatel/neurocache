package store

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// LCS computes the longest common subsequence between the values at
// keyA and keyB. mode controls what to return:
//
//   "string" — the LCS as a string (Redis default)
//   "len"    — just the length
//   "idx"    — every matching range pair (with optional MINMATCHLEN
//              and WITHMATCHLEN flags resolved at the RESP layer)
//
// Returns (lcsString, length, matches). matches is non-nil only when
// idx mode is requested.
func (s *Store) LCS(keyA, keyB string, mode string, minMatchLen int) (string, int, []LCSMatch, error) {
	_, _, unlock := s.lockTwoR(keyA, keyB)
	defer unlock()
	a, _ := s.stringValue(keyA)
	b, _ := s.stringValue(keyB)
	if a == "" && b == "" {
		return "", 0, nil, nil
	}
	dp := lcsTable(a, b)
	length := dp[len(a)][len(b)]
	if mode == "len" {
		return "", length, nil, nil
	}
	lcs := lcsBuild(a, b, dp)
	if mode == "idx" {
		return lcs, length, lcsMatches(a, b, dp, minMatchLen), nil
	}
	return lcs, length, nil, nil
}

// LCSMatch is one (rangeA, rangeB, length) triple LCS IDX surfaces.
type LCSMatch struct {
	StartA, EndA int
	StartB, EndB int
	Length       int
}

// stringValue reads the raw string at key. Caller must hold a read
// or write lock on the shard owning `key`.
func (s *Store) stringValue(key string) (string, bool) {
	sh := s.shardForKey(key)
	e, ok := sh.data[key]
	if !ok {
		return "", false
	}
	if e.Type != TypeString {
		return "", false
	}
	return e.Str, true
}

func lcsTable(a, b string) [][]int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
				continue
			}
			dp[i][j] = dp[i-1][j]
			if dp[i][j-1] > dp[i][j] {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	return dp
}

func lcsBuild(a, b string, dp [][]int) string {
	i, j := len(a), len(b)
	out := []byte{}
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			out = append([]byte{a[i-1]}, out...)
			i--
			j--
			continue
		}
		if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	return string(out)
}

func lcsMatches(a, b string, dp [][]int, minMatchLen int) []LCSMatch {
	out := []LCSMatch{}
	i, j := len(a), len(b)
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			endA, endB := i-1, j-1
			for i > 0 && j > 0 && a[i-1] == b[j-1] {
				i--
				j--
			}
			startA, startB := i, j
			length := endA - startA + 1
			if length >= minMatchLen {
				out = append(out, LCSMatch{StartA: startA, EndA: endA, StartB: startB, EndB: endB, Length: length})
			}
			continue
		}
		if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	return out
}

// ── BITFIELD ───────────────────────────────────────────────────────
//
// BITFIELD is Redis's bit-level "struct" — read/write arbitrary
// integer fields at any bit offset, with three overflow strategies
// (WRAP / SAT / FAIL). We model it on top of the existing string
// storage: operations read/modify a contiguous byte slice and write
// back via Set.

type BitFieldOp struct {
	Op       string // GET | SET | INCRBY
	Type     string // u<N> or i<N>
	Offset   string // raw offset token (may be #N)
	Value    int64  // SET / INCRBY operand
	Overflow string // current OVERFLOW mode for this op
}

// BitField runs a sequence of ops on key. Returns one reply per op
// (nil for OVERFLOW switches; integer for GET/SET/INCRBY).
func (s *Store) BitField(key string, ops []BitFieldOp, readOnly bool) ([]any, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data[key]
	var raw []byte
	if ok && e.Type == TypeString {
		raw = []byte(e.Str)
	} else if ok && e.Type != TypeString {
		return nil, ErrWrongType
	}
	out := make([]any, 0, len(ops))
	overflow := "WRAP"
	for _, op := range ops {
		switch op.Op {
		case "OVERFLOW":
			overflow = strings.ToUpper(op.Overflow)
			out = append(out, nil)
		case "GET":
			signed, width, err := parseBitFieldType(op.Type)
			if err != nil {
				return nil, err
			}
			off, err := parseBitFieldOffset(op.Offset, width)
			if err != nil {
				return nil, err
			}
			out = append(out, readBits(raw, off, width, signed))
		case "SET":
			if readOnly {
				return nil, errors.New("BITFIELD_RO is read-only")
			}
			signed, width, err := parseBitFieldType(op.Type)
			if err != nil {
				return nil, err
			}
			off, err := parseBitFieldOffset(op.Offset, width)
			if err != nil {
				return nil, err
			}
			prev := readBits(raw, off, width, signed)
			raw = ensureBitCapacity(raw, off+width)
			writeBits(raw, off, width, op.Value)
			out = append(out, prev)
		case "INCRBY":
			if readOnly {
				return nil, errors.New("BITFIELD_RO is read-only")
			}
			signed, width, err := parseBitFieldType(op.Type)
			if err != nil {
				return nil, err
			}
			off, err := parseBitFieldOffset(op.Offset, width)
			if err != nil {
				return nil, err
			}
			cur := readBits(raw, off, width, signed)
			next, ok := bitFieldOverflow(cur+op.Value, width, signed, overflow)
			if !ok {
				out = append(out, nil)
				continue
			}
			raw = ensureBitCapacity(raw, off+width)
			writeBits(raw, off, width, next)
			out = append(out, next)
		}
	}
	if !readOnly && raw != nil {
		newE, err := s.getOrCreate(sh, key, TypeString)
		if err != nil {
			return nil, err
		}
		newE.Str = string(raw)
		s.recomputeBytes(newE)
	}
	return out, nil
}

func parseBitFieldType(t string) (signed bool, width int, err error) {
	if len(t) < 2 {
		return false, 0, errors.New("invalid type")
	}
	switch t[0] {
	case 'u', 'U':
	case 'i', 'I':
		signed = true
	default:
		return false, 0, errors.New("type must start with u or i")
	}
	w, err := strconv.Atoi(t[1:])
	if err != nil || w <= 0 {
		return false, 0, errors.New("invalid width")
	}
	if signed && w > 64 {
		return false, 0, errors.New("signed width must be <= 64")
	}
	if !signed && w > 63 {
		return false, 0, errors.New("unsigned width must be <= 63")
	}
	return signed, w, nil
}

func parseBitFieldOffset(off string, width int) (int, error) {
	if strings.HasPrefix(off, "#") {
		multiplier, err := strconv.Atoi(off[1:])
		if err != nil {
			return 0, errors.New("invalid offset multiplier")
		}
		return multiplier * width, nil
	}
	return strconv.Atoi(off)
}

func ensureBitCapacity(buf []byte, bits int) []byte {
	need := (bits + 7) / 8
	if len(buf) >= need {
		return buf
	}
	out := make([]byte, need)
	copy(out, buf)
	return out
}

// readBits pulls `width` bits at `off`, sign-extending when signed.
func readBits(buf []byte, off, width int, signed bool) int64 {
	var value uint64
	for i := 0; i < width; i++ {
		bit := off + i
		bytePos := bit / 8
		bitInByte := uint(7 - bit%8)
		var b byte
		if bytePos < len(buf) {
			b = buf[bytePos]
		}
		v := uint64((b >> bitInByte) & 1)
		value = (value << 1) | v
	}
	if signed && width < 64 {
		mask := uint64(1) << (width - 1)
		if value&mask != 0 {
			value |= ^((uint64(1) << width) - 1)
		}
	}
	return int64(value)
}

// writeBits stores the low `width` bits of value at `off`.
func writeBits(buf []byte, off, width int, value int64) {
	uv := uint64(value)
	for i := 0; i < width; i++ {
		bit := off + i
		bytePos := bit / 8
		bitInByte := uint(7 - bit%8)
		v := byte((uv >> uint(width-1-i)) & 1)
		buf[bytePos] = (buf[bytePos] &^ (1 << bitInByte)) | (v << bitInByte)
	}
}

// bitFieldOverflow applies the SET/INCRBY overflow strategy.
func bitFieldOverflow(value int64, width int, signed bool, mode string) (int64, bool) {
	var min, max int64
	if signed {
		max = (int64(1) << (width - 1)) - 1
		min = -(int64(1) << (width - 1))
	} else {
		max = (int64(1) << width) - 1
	}
	if value >= min && value <= max {
		return value, true
	}
	switch strings.ToUpper(mode) {
	case "SAT":
		if value > max {
			return max, true
		}
		return min, true
	case "FAIL":
		return 0, false
	}
	// WRAP — mask the low `width` bits.
	uv := uint64(value) & ((uint64(1) << width) - 1)
	if signed {
		mask := uint64(1) << (width - 1)
		if uv&mask != 0 {
			return int64(uv | ^((uint64(1) << width) - 1)), true
		}
	}
	return int64(uv), true
}

// ── SORT ───────────────────────────────────────────────────────────
//
// SORT walks a list / set / zset and orders the elements by either
// their natural value (numeric or alpha) or the value of an external
// hash field. STORE captures the result into a list.

type SortOpts struct {
	By       string // "" / "nosort" / hash-pattern (e.g. "weight_*")
	Get      []string
	Limit    [2]int // [offset, count]; -1 means "no limit"
	Order    string // "ASC" / "DESC"
	Alpha    bool
	Store    string
}

// Sort runs the requested sort and returns the result list. When
// opts.Store is set, the result is also written to a list at that key
// and the function returns nil for the slice (caller writes the count
// reply via SortStored).
func (s *Store) Sort(key string, opts SortOpts) ([]string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	src, err := s.collectSortSource(key)
	sh.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(opts.By, "nosort") {
		// preserve source order
	} else {
		// sort with a key function
		keyFn := func(v string) string { return v }
		if opts.By != "" {
			keyFn = func(v string) string { return s.lookupSortBy(opts.By, v) }
		}
		if opts.Alpha {
			sort.SliceStable(src, func(i, j int) bool {
				if opts.Order == "DESC" {
					return keyFn(src[i]) > keyFn(src[j])
				}
				return keyFn(src[i]) < keyFn(src[j])
			})
		} else {
			sort.SliceStable(src, func(i, j int) bool {
				ai, _ := strconv.ParseFloat(keyFn(src[i]), 64)
				bi, _ := strconv.ParseFloat(keyFn(src[j]), 64)
				if opts.Order == "DESC" {
					return ai > bi
				}
				return ai < bi
			})
		}
	}
	off, count := 0, len(src)
	if opts.Limit[1] >= 0 {
		off = opts.Limit[0]
		count = opts.Limit[1]
		if off > len(src) {
			off = len(src)
		}
		if off+count > len(src) {
			count = len(src) - off
		}
		src = src[off : off+count]
	}
	if len(opts.Get) == 0 {
		if opts.Store != "" {
			return nil, s.replaceList(opts.Store, src)
		}
		return src, nil
	}
	// GET pattern resolution
	out := []string{}
	for _, v := range src {
		for _, pat := range opts.Get {
			if pat == "#" {
				out = append(out, v)
				continue
			}
			out = append(out, s.lookupSortBy(pat, v))
		}
	}
	if opts.Store != "" {
		return nil, s.replaceList(opts.Store, out)
	}
	return out, nil
}

// collectSortSource enumerates the elements of a list / set / zset.
// Caller must hold a read lock on the shard owning `key`.
func (s *Store) collectSortSource(key string) ([]string, error) {
	sh := s.shardForKey(key)
	e, ok := sh.data[key]
	if !ok {
		return nil, nil
	}
	switch e.Type {
	case TypeList:
		out := make([]string, 0, e.List.Len())
		for el := e.List.Front(); el != nil; el = el.Next() {
			out = append(out, el.Value.(string))
		}
		return out, nil
	case TypeSet:
		out := make([]string, 0, len(e.Set))
		for m := range e.Set {
			out = append(out, m)
		}
		return out, nil
	case TypeZSet:
		return e.ZSet.members(), nil
	}
	return nil, ErrWrongType
}

// lookupSortBy resolves a `pattern->*` style indirection. The pattern
// may be a hash key with `*` standing in for the element ("weight_*")
// or a hash-field reference ("user_*->name"). Acquires its own read
// lock on the resolved key's shard — Sort holds no global lock.
func (s *Store) lookupSortBy(pattern, element string) string {
	if !strings.Contains(pattern, "*") {
		return pattern
	}
	key := strings.Replace(pattern, "*", element, 1)
	field := ""
	if idx := strings.Index(key, "->"); idx >= 0 {
		field = key[idx+2:]
		key = key[:idx]
	}
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data[key]
	if !ok {
		return ""
	}
	if field != "" {
		if e.Type != TypeHash {
			return ""
		}
		return e.Hash[field]
	}
	if e.Type == TypeString {
		return e.Str
	}
	return ""
}

func (s *Store) replaceList(key string, items []string) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if old, ok := sh.data[key]; ok {
		s.bytes.Add(-int64(old.Bytes))
		delete(sh.data, key)
	}
	if len(items) == 0 {
		return nil
	}
	e, err := s.getOrCreate(sh, key, TypeList)
	if err != nil {
		return err
	}
	for _, v := range items {
		e.List.PushBack(v)
	}
	s.recomputeBytes(e)
	s.fire("rpush", key)
	return nil
}
