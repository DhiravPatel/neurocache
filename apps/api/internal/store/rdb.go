package store

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"
)

// Redis-wire-compatible DUMP/RESTORE serialization.
//
// This produces and consumes the exact opaque payload Redis' DUMP emits and
// RESTORE accepts: [1-byte RDB object type][RDB-encoded value][2-byte RDB
// version, little-endian][8-byte CRC64 footer]. That makes NeuroCache a
// drop-in migration source/target for redis-cli --pipe, RIOT, redis-shake,
// and friends — the "byte-compat gap" from the Known-gaps list.
//
// Scope: the five core Redis types (string/list/set/hash/zset). On DUMP we
// emit forms every modern Redis loads; on RESTORE we decode the compact
// encodings real Redis emits (listpack, intset, ziplist, quicklist v1/v2,
// plus int- and LZF-encoded strings). Streams and the NeuroCache-only vector
// type have no standard RDB representation and keep the internal gob format
// (RESTORE auto-detects which it's looking at via the CRC64 footer).

// ─── CRC64 (Redis variant: reflected, poly 0xad93d23594c935a9, init 0) ────

var crc64Table [256]uint64

func init() {
	// Reflected form of Redis' CRC64 poly 0xad93d23594c935a9 (refin/refout).
	const poly = uint64(0x95ac9329ac4bc9b5)
	for i := range crc64Table {
		crc := uint64(i)
		for j := 0; j < 8; j++ {
			if crc&1 == 1 {
				crc = (crc >> 1) ^ poly
			} else {
				crc >>= 1
			}
		}
		crc64Table[i] = crc
	}
}

// crc64 computes the Redis CRC64 over data (continuing from a prior crc).
func crc64(crc uint64, data []byte) uint64 {
	for _, b := range data {
		crc = crc64Table[byte(crc)^b] ^ (crc >> 8)
	}
	return crc
}

// rdbVersion is the version stamped in the DUMP footer. Redis RESTORE rejects
// a payload whose version exceeds the server's own RDB_VERSION, so we stamp a
// conservative value that every Redis ≥ 5 accepts; the encodings we emit
// (plain string/list/set/hash + ZSET_2) are all far older than this.
const rdbVersion = 9

// RDB object type bytes.
const (
	rdbTypeString        = 0
	rdbTypeList          = 1  // legacy plain list (still loaded by modern Redis)
	rdbTypeSet           = 2  // hashtable set
	rdbTypeZSet          = 3  // legacy zset (string doubles)
	rdbTypeHash          = 4  // hashtable hash
	rdbTypeZSet2         = 5  // zset with binary doubles
	rdbTypeHashZipmap    = 9  // very old; unsupported on read
	rdbTypeListZiplist   = 10
	rdbTypeSetIntset     = 11
	rdbTypeZSetZiplist   = 12
	rdbTypeHashZiplist   = 13
	rdbTypeListQuicklist = 14 // v1: nodes are ziplists
	rdbTypeStreamListpks = 15
	rdbTypeHashListpack  = 16
	rdbTypeZSetListpack  = 17
	rdbTypeListQuick2    = 18 // v2: nodes are listpacks (Redis 7+/8 default)
	rdbTypeSetListpack   = 20
)

// ─── writers (DUMP encode) ────────────────────────────────────────────────

// wLen appends an RDB length-encoded integer.
func wLen(b []byte, n uint64) []byte {
	switch {
	case n < 1<<6:
		return append(b, byte(n)) // 00xxxxxx
	case n < 1<<14:
		return append(b, byte(n>>8)|0x40, byte(n)) // 01xxxxxx xxxxxxxx (big-endian 14-bit)
	case n <= 0xffffffff:
		return append(b, 0x80, byte(n>>24), byte(n>>16), byte(n>>8), byte(n)) // 32-bit BE
	default:
		return append(b, 0x81, byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n)) // 64-bit BE
	}
}

// wString appends an RDB string as length-prefixed raw bytes (no int/LZF
// encoding on write — the plain form is always valid and Redis re-encodes
// internally on load).
func wString(b []byte, s string) []byte {
	b = wLen(b, uint64(len(s)))
	return append(b, s...)
}

// wBinaryDouble appends an 8-byte little-endian IEEE-754 score (ZSET_2).
func wBinaryDouble(b []byte, f float64) []byte {
	bits := math.Float64bits(f)
	return append(b, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24),
		byte(bits>>32), byte(bits>>40), byte(bits>>48), byte(bits>>56))
}

// rdbSerialize produces the Redis DUMP payload for an ExportEntry, or
// ok=false when the type has no standard RDB form (stream/vector) and the
// caller should fall back to the internal gob encoding.
func rdbSerialize(exp ExportEntry) (out []byte, ok bool) {
	var b []byte
	switch exp.Type {
	case "string":
		b = append(b, rdbTypeString)
		b = wString(b, exp.Str)
	case "list":
		b = append(b, rdbTypeList)
		b = wLen(b, uint64(len(exp.List)))
		for _, v := range exp.List {
			b = wString(b, v)
		}
	case "set":
		b = append(b, rdbTypeSet)
		b = wLen(b, uint64(len(exp.Set)))
		for _, m := range exp.Set {
			b = wString(b, m)
		}
	case "hash":
		b = append(b, rdbTypeHash)
		b = wLen(b, uint64(len(exp.Hash)))
		for k, v := range exp.Hash {
			b = wString(b, k)
			b = wString(b, v)
		}
	case "zset":
		b = append(b, rdbTypeZSet2)
		b = wLen(b, uint64(len(exp.ZSet)))
		for _, zm := range exp.ZSet {
			b = wString(b, zm.Member)
			b = wBinaryDouble(b, zm.Score)
		}
	default:
		return nil, false // stream / vectorset — not standard RDB
	}
	// Footer: 2-byte version (LE) + 8-byte CRC64 (LE) over type+value+version.
	b = append(b, byte(rdbVersion), byte(rdbVersion>>8))
	c := crc64(0, b)
	b = append(b, byte(c), byte(c>>8), byte(c>>16), byte(c>>24),
		byte(c>>32), byte(c>>40), byte(c>>48), byte(c>>56))
	return b, true
}

// ─── readers (RESTORE decode) ─────────────────────────────────────────────

var (
	errRDB            = errors.New("Bad data format")
	errRDBChecksum    = errors.New("DUMP payload version or checksum are wrong")
	errRDBUnsupported = errors.New("unsupported RDB object type")
)

type rdbReader struct {
	b   []byte
	pos int
}

func (r *rdbReader) u8() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, errRDB
	}
	v := r.b[r.pos]
	r.pos++
	return v, nil
}

func (r *rdbReader) take(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.b) {
		return nil, errRDB
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v, nil
}

// loadLen decodes an RDB length. When encoded is true the value is specially
// encoded and enc holds the encoding type (0=INT8,1=INT16,2=INT32,3=LZF).
func (r *rdbReader) loadLen() (n uint64, encoded bool, enc int, err error) {
	b0, err := r.u8()
	if err != nil {
		return
	}
	switch (b0 & 0xC0) >> 6 {
	case 0: // 6-bit
		return uint64(b0 & 0x3f), false, 0, nil
	case 1: // 14-bit big-endian
		b1, e := r.u8()
		if e != nil {
			return 0, false, 0, e
		}
		return uint64(b0&0x3f)<<8 | uint64(b1), false, 0, nil
	case 3: // RDB_ENCVAL
		return 0, true, int(b0 & 0x3f), nil
	default: // 10xxxxxx → 32 or 64 bit
		switch b0 {
		case 0x80:
			bs, e := r.take(4)
			if e != nil {
				return 0, false, 0, e
			}
			return uint64(binary.BigEndian.Uint32(bs)), false, 0, nil
		case 0x81:
			bs, e := r.take(8)
			if e != nil {
				return 0, false, 0, e
			}
			return binary.BigEndian.Uint64(bs), false, 0, nil
		}
		return 0, false, 0, errRDB
	}
}

func (r *rdbReader) loadString() (string, error) {
	n, encoded, enc, err := r.loadLen()
	if err != nil {
		return "", err
	}
	if encoded {
		switch enc {
		case 0: // INT8
			b, e := r.u8()
			return strconv.FormatInt(int64(int8(b)), 10), e
		case 1: // INT16 LE
			bs, e := r.take(2)
			if e != nil {
				return "", e
			}
			return strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(bs))), 10), nil
		case 2: // INT32 LE
			bs, e := r.take(4)
			if e != nil {
				return "", e
			}
			return strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(bs))), 10), nil
		case 3: // LZF
			clen, _, _, e := r.loadLen()
			if e != nil {
				return "", e
			}
			ulen, _, _, e := r.loadLen()
			if e != nil {
				return "", e
			}
			cb, e := r.take(int(clen))
			if e != nil {
				return "", e
			}
			out, e := lzfDecompress(cb, int(ulen))
			return string(out), e
		default:
			return "", errRDB
		}
	}
	bs, err := r.take(int(n))
	return string(bs), err
}

func (r *rdbReader) loadBinaryDouble() (float64, error) {
	bs, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(bs)), nil
}

func (r *rdbReader) loadStringDouble() (float64, error) {
	n, err := r.u8()
	if err != nil {
		return 0, err
	}
	switch n {
	case 255:
		return math.Inf(-1), nil
	case 254:
		return math.Inf(1), nil
	case 253:
		return math.NaN(), nil
	default:
		bs, e := r.take(int(n))
		if e != nil {
			return 0, e
		}
		return strconv.ParseFloat(string(bs), 64)
	}
}

// lzfDecompress expands an LZF-compressed buffer to ulen bytes.
func lzfDecompress(in []byte, ulen int) ([]byte, error) {
	// ulen is the attacker-controlled decompressed-length header from a
	// RESTORE payload. Bound it before pre-allocating, or a tiny
	// compressed blob claiming a 2 GB output would OOM the server
	// (decompression bomb).
	if ulen < 0 || ulen > maxStringBytes {
		return nil, errRDB
	}
	out := make([]byte, 0, ulen)
	i := 0
	for i < len(in) {
		ctrl := int(in[i])
		i++
		if ctrl < 32 { // literal run of ctrl+1 bytes
			n := ctrl + 1
			if i+n > len(in) {
				return nil, errRDB
			}
			out = append(out, in[i:i+n]...)
			i += n
		} else { // back reference
			length := ctrl >> 5
			if length == 7 {
				if i >= len(in) {
					return nil, errRDB
				}
				length += int(in[i])
				i++
			}
			if i >= len(in) {
				return nil, errRDB
			}
			ref := len(out) - ((ctrl & 0x1f) << 8) - int(in[i]) - 1
			i++
			if ref < 0 {
				return nil, errRDB
			}
			for j := 0; j < length+2; j++ {
				out = append(out, out[ref])
				ref++
			}
		}
	}
	return out, nil
}

// ─── listpack / intset / ziplist decoders ─────────────────────────────────

func lpBacklenSize(l int) int {
	switch {
	case l <= 127:
		return 1
	case l <= 16383:
		return 2
	case l <= 2097151:
		return 3
	case l <= 268435455:
		return 4
	default:
		return 5
	}
}

// lpEntry parses one listpack entry's encoding+data at p, returning its
// string value and the encoding+data byte length (excluding the backlen).
func lpEntry(b []byte, p int) (string, int, error) {
	if p >= len(b) {
		return "", 0, errRDB
	}
	c := b[p]
	switch {
	case c < 0x80: // 7-bit uint
		return strconv.Itoa(int(c & 0x7f)), 1, nil
	case c < 0xC0: // 10xxxxxx 6-bit string
		l := int(c & 0x3f)
		if p+1+l > len(b) {
			return "", 0, errRDB
		}
		return string(b[p+1 : p+1+l]), 1 + l, nil
	case c < 0xE0: // 110xxxxx 13-bit int
		if p+1 >= len(b) {
			return "", 0, errRDB
		}
		v := int(c&0x1f)<<8 | int(b[p+1])
		if v >= 1<<12 {
			v -= 1 << 13
		}
		return strconv.Itoa(v), 2, nil
	case c == 0xF0: // 32-bit string len LE
		if p+5 > len(b) {
			return "", 0, errRDB
		}
		l := int(binary.LittleEndian.Uint32(b[p+1:]))
		if p+5+l > len(b) {
			return "", 0, errRDB
		}
		return string(b[p+5 : p+5+l]), 5 + l, nil
	case c == 0xF1: // 16-bit int LE
		if p+3 > len(b) {
			return "", 0, errRDB
		}
		return strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(b[p+1:]))), 10), 3, nil
	case c == 0xF2: // 24-bit int LE
		if p+4 > len(b) {
			return "", 0, errRDB
		}
		u := uint32(b[p+1]) | uint32(b[p+2])<<8 | uint32(b[p+3])<<16
		return strconv.FormatInt(int64(int32(u<<8)>>8), 10), 4, nil
	case c == 0xF3: // 32-bit int LE
		if p+5 > len(b) {
			return "", 0, errRDB
		}
		return strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(b[p+1:]))), 10), 5, nil
	case c == 0xF4: // 64-bit int LE
		if p+9 > len(b) {
			return "", 0, errRDB
		}
		return strconv.FormatInt(int64(binary.LittleEndian.Uint64(b[p+1:])), 10), 9, nil
	case c < 0xF0: // 1110xxxx 12-bit string
		if p+1 >= len(b) {
			return "", 0, errRDB
		}
		l := int(c&0x0f)<<8 | int(b[p+1])
		if p+2+l > len(b) {
			return "", 0, errRDB
		}
		return string(b[p+2 : p+2+l]), 2 + l, nil
	default:
		return "", 0, errRDB
	}
}

func lpDecode(b []byte) ([]string, error) {
	if len(b) < 7 { // 4-byte total + 2-byte numele + 0xFF
		return nil, errRDB
	}
	pos := 6
	var out []string
	for pos < len(b) {
		if b[pos] == 0xFF {
			return out, nil
		}
		s, enclen, err := lpEntry(b, pos)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		pos += enclen + lpBacklenSize(enclen)
	}
	return out, nil
}

func intsetDecode(b []byte) ([]string, error) {
	if len(b) < 8 {
		return nil, errRDB
	}
	enc := binary.LittleEndian.Uint32(b[0:4])
	n := binary.LittleEndian.Uint32(b[4:8])
	// n is an untrusted count from the payload. Cap the pre-allocation
	// against the bytes actually present (smallest element is 2 bytes) so
	// a crafted "count = 4 billion" header can't allocate a 64 GB slice
	// before the per-element bounds checks below would bail.
	capHint := int(n)
	if max := (len(b)-8)/2 + 1; capHint > max {
		capHint = max
	}
	out := make([]string, 0, capHint)
	p := 8
	for i := uint32(0); i < n; i++ {
		switch enc {
		case 2:
			if p+2 > len(b) {
				return nil, errRDB
			}
			out = append(out, strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(b[p:]))), 10))
			p += 2
		case 4:
			if p+4 > len(b) {
				return nil, errRDB
			}
			out = append(out, strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(b[p:]))), 10))
			p += 4
		case 8:
			if p+8 > len(b) {
				return nil, errRDB
			}
			out = append(out, strconv.FormatInt(int64(binary.LittleEndian.Uint64(b[p:])), 10))
			p += 8
		default:
			return nil, errRDB
		}
	}
	return out, nil
}

// ziplistDecode handles the pre-7.0 ziplist encoding (still appears in dumps
// taken from older Redis).
func ziplistDecode(b []byte) ([]string, error) {
	if len(b) < 11 {
		return nil, errRDB
	}
	p := 10 // skip zlbytes(4) + zltail(4) + zllen(2)
	var out []string
	for p < len(b) {
		if b[p] == 0xFF {
			return out, nil
		}
		// prevlen
		if b[p] < 254 {
			p++
		} else {
			p += 5
		}
		if p >= len(b) {
			return nil, errRDB
		}
		enc := b[p]
		if enc>>6 != 3 { // string
			var l int
			switch enc >> 6 {
			case 0:
				l = int(enc & 0x3f)
				p++
			case 1:
				if p+2 > len(b) {
					return nil, errRDB
				}
				l = int(enc&0x3f)<<8 | int(b[p+1])
				p += 2
			case 2:
				if p+5 > len(b) {
					return nil, errRDB
				}
				l = int(binary.BigEndian.Uint32(b[p+1 : p+5]))
				p += 5
			}
			if p+l > len(b) {
				return nil, errRDB
			}
			out = append(out, string(b[p:p+l]))
			p += l
		} else { // integer
			switch enc {
			case 0xC0:
				out = append(out, strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(b[p+1:]))), 10))
				p += 3
			case 0xD0:
				out = append(out, strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(b[p+1:]))), 10))
				p += 5
			case 0xE0:
				out = append(out, strconv.FormatInt(int64(binary.LittleEndian.Uint64(b[p+1:])), 10))
				p += 9
			case 0xF0: // 24-bit
				u := uint32(b[p+1]) | uint32(b[p+2])<<8 | uint32(b[p+3])<<16
				out = append(out, strconv.FormatInt(int64(int32(u<<8)>>8), 10))
				p += 4
			case 0xFE: // 8-bit
				out = append(out, strconv.FormatInt(int64(int8(b[p+1])), 10))
				p += 2
			default: // 1111xxxx 4-bit immediate (value-1)
				out = append(out, strconv.Itoa(int(enc&0x0f)-1))
				p++
			}
		}
	}
	return out, nil
}

func (r *rdbReader) loadQuicklist1() ([]string, error) {
	nn, _, _, err := r.loadLen()
	if err != nil {
		return nil, err
	}
	var out []string
	for i := uint64(0); i < nn; i++ {
		zl, err := r.loadString()
		if err != nil {
			return nil, err
		}
		els, err := ziplistDecode([]byte(zl))
		if err != nil {
			return nil, err
		}
		out = append(out, els...)
	}
	return out, nil
}

func (r *rdbReader) loadQuicklist2() ([]string, error) {
	nn, _, _, err := r.loadLen()
	if err != nil {
		return nil, err
	}
	var out []string
	for i := uint64(0); i < nn; i++ {
		container, _, _, err := r.loadLen()
		if err != nil {
			return nil, err
		}
		node, err := r.loadString()
		if err != nil {
			return nil, err
		}
		if container == 1 { // PLAIN: node is a single element
			out = append(out, node)
		} else { // PACKED: node is a listpack
			els, err := lpDecode([]byte(node))
			if err != nil {
				return nil, err
			}
			out = append(out, els...)
		}
	}
	return out, nil
}

func pairsToMap(flat []string) map[string]string {
	m := make(map[string]string, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		m[flat[i]] = flat[i+1]
	}
	return m
}

func pairsToZSet(flat []string) []ExportZMember {
	out := make([]ExportZMember, 0, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		sc, _ := strconv.ParseFloat(flat[i+1], 64)
		out = append(out, ExportZMember{Member: flat[i], Score: sc})
	}
	return out
}

// rdbDeserialize parses a Redis DUMP payload into an ExportEntry. It validates
// the CRC64 footer (which also serves to distinguish a real RDB payload from
// the internal gob format on the RESTORE path).
func rdbDeserialize(blob []byte) (ExportEntry, error) {
	var exp ExportEntry
	if len(blob) < 11 { // 1 type + 2 version + 8 crc, minimum
		return exp, errRDBChecksum
	}
	body := blob[:len(blob)-8]
	want := binary.LittleEndian.Uint64(blob[len(blob)-8:])
	if crc64(0, body) != want {
		return exp, errRDBChecksum
	}
	typeByte := body[0]
	r := &rdbReader{b: body[1 : len(body)-2]} // strip type byte + 2-byte version
	switch typeByte {
	case rdbTypeString:
		s, err := r.loadString()
		if err != nil {
			return exp, err
		}
		exp.Type, exp.Str = "string", s
	case rdbTypeList:
		n, _, _, err := r.loadLen()
		if err != nil {
			return exp, err
		}
		for i := uint64(0); i < n; i++ {
			s, e := r.loadString()
			if e != nil {
				return exp, e
			}
			exp.List = append(exp.List, s)
		}
		exp.Type = "list"
	case rdbTypeListZiplist:
		zl, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := ziplistDecode([]byte(zl))
		if e != nil {
			return exp, e
		}
		exp.List, exp.Type = els, "list"
	case rdbTypeListQuicklist:
		els, err := r.loadQuicklist1()
		if err != nil {
			return exp, err
		}
		exp.List, exp.Type = els, "list"
	case rdbTypeListQuick2:
		els, err := r.loadQuicklist2()
		if err != nil {
			return exp, err
		}
		exp.List, exp.Type = els, "list"
	case rdbTypeSet:
		n, _, _, err := r.loadLen()
		if err != nil {
			return exp, err
		}
		for i := uint64(0); i < n; i++ {
			s, e := r.loadString()
			if e != nil {
				return exp, e
			}
			exp.Set = append(exp.Set, s)
		}
		exp.Type = "set"
	case rdbTypeSetIntset:
		is, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := intsetDecode([]byte(is))
		if e != nil {
			return exp, e
		}
		exp.Set, exp.Type = els, "set"
	case rdbTypeSetListpack:
		lp, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := lpDecode([]byte(lp))
		if e != nil {
			return exp, e
		}
		exp.Set, exp.Type = els, "set"
	case rdbTypeHash:
		n, _, _, err := r.loadLen()
		if err != nil {
			return exp, err
		}
		exp.Hash = make(map[string]string, n)
		for i := uint64(0); i < n; i++ {
			f, e := r.loadString()
			if e != nil {
				return exp, e
			}
			v, e2 := r.loadString()
			if e2 != nil {
				return exp, e2
			}
			exp.Hash[f] = v
		}
		exp.Type = "hash"
	case rdbTypeHashZiplist:
		zl, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := ziplistDecode([]byte(zl))
		if e != nil {
			return exp, e
		}
		exp.Hash, exp.Type = pairsToMap(els), "hash"
	case rdbTypeHashListpack:
		lp, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := lpDecode([]byte(lp))
		if e != nil {
			return exp, e
		}
		exp.Hash, exp.Type = pairsToMap(els), "hash"
	case rdbTypeZSet2:
		n, _, _, err := r.loadLen()
		if err != nil {
			return exp, err
		}
		for i := uint64(0); i < n; i++ {
			m, e := r.loadString()
			if e != nil {
				return exp, e
			}
			sc, e2 := r.loadBinaryDouble()
			if e2 != nil {
				return exp, e2
			}
			exp.ZSet = append(exp.ZSet, ExportZMember{Member: m, Score: sc})
		}
		exp.Type = "zset"
	case rdbTypeZSet:
		n, _, _, err := r.loadLen()
		if err != nil {
			return exp, err
		}
		for i := uint64(0); i < n; i++ {
			m, e := r.loadString()
			if e != nil {
				return exp, e
			}
			sc, e2 := r.loadStringDouble()
			if e2 != nil {
				return exp, e2
			}
			exp.ZSet = append(exp.ZSet, ExportZMember{Member: m, Score: sc})
		}
		exp.Type = "zset"
	case rdbTypeZSetZiplist:
		zl, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := ziplistDecode([]byte(zl))
		if e != nil {
			return exp, e
		}
		exp.ZSet, exp.Type = pairsToZSet(els), "zset"
	case rdbTypeZSetListpack:
		lp, err := r.loadString()
		if err != nil {
			return exp, err
		}
		els, e := lpDecode([]byte(lp))
		if e != nil {
			return exp, e
		}
		exp.ZSet, exp.Type = pairsToZSet(els), "zset"
	default:
		return exp, errRDBUnsupported
	}
	return exp, nil
}
