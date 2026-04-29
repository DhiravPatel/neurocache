package probmod

import (
	"encoding/binary"
	"errors"
	"math"
)

// Bloom is a (potentially scaling) bloom filter. A "filter" here is
// actually a list of underlying tables — when one fills up we add a
// new tighter one and EXISTS scans across all of them. This matches
// the Redis Bloom (RedisBloom) behaviour.
type Bloom struct {
	Layers      []*bloomLayer
	ErrorRate   float64
	Capacity    uint64
	Expansion   uint64 // multiplier for each new layer's capacity
	NonScaling  bool
	Inserted    uint64
}

type bloomLayer struct {
	Bits     []uint64 // bit array, 64 bits per word
	M        uint64   // number of bits
	K        uint32   // number of hash functions
	Capacity uint64
	Count    uint64
}

// NewBloom constructs a single-layer filter sized for `capacity` items
// at `errorRate` false-positive probability. expansion=0 picks the
// canonical default of 2.
func NewBloom(errorRate float64, capacity uint64, expansion uint64, nonScaling bool) (*Bloom, error) {
	if errorRate <= 0 || errorRate >= 1 {
		return nil, errors.New("error rate must be in (0,1)")
	}
	if capacity == 0 {
		return nil, errors.New("capacity must be > 0")
	}
	if expansion == 0 {
		expansion = 2
	}
	b := &Bloom{ErrorRate: errorRate, Capacity: capacity, Expansion: expansion, NonScaling: nonScaling}
	b.Layers = []*bloomLayer{newBloomLayer(errorRate, capacity)}
	return b, nil
}

// newBloomLayer derives optimal m + k from the standard Bloom formulas:
//
//	m = -n*ln(p) / (ln(2)^2)
//	k = m/n * ln(2)
func newBloomLayer(p float64, n uint64) *bloomLayer {
	m := math.Ceil(-float64(n) * math.Log(p) / (math.Ln2 * math.Ln2))
	k := math.Round(m / float64(n) * math.Ln2)
	if k < 1 {
		k = 1
	}
	mu := uint64(m)
	if mu == 0 {
		mu = 1
	}
	words := (mu + 63) / 64
	return &bloomLayer{Bits: make([]uint64, words), M: mu, K: uint32(k), Capacity: n}
}

// Add inserts an element. Returns true when the filter was changed
// (i.e. the element was probably not present before).
func (b *Bloom) Add(item []byte) bool {
	// If the topmost layer is full, scale (or refuse if non-scaling).
	top := b.Layers[len(b.Layers)-1]
	if top.Count >= top.Capacity {
		if b.NonScaling {
			// Still allowed to insert but accuracy degrades — match Redis.
		} else {
			cap := top.Capacity * b.Expansion
			next := newBloomLayer(b.ErrorRate*0.5, cap) // tighten error rate per layer
			b.Layers = append(b.Layers, next)
			top = next
		}
	}
	if b.contains(item) {
		return false
	}
	h1, h2 := pair(item)
	for i := uint32(0); i < top.K; i++ {
		bit := (h1 + uint64(i)*h2) % top.M
		top.Bits[bit/64] |= 1 << (bit % 64)
	}
	top.Count++
	b.Inserted++
	return true
}

// Contains tests whether item might be in the filter.
func (b *Bloom) Contains(item []byte) bool { return b.contains(item) }

func (b *Bloom) contains(item []byte) bool {
	h1, h2 := pair(item)
	for _, layer := range b.Layers {
		hit := true
		for i := uint32(0); i < layer.K; i++ {
			bit := (h1 + uint64(i)*h2) % layer.M
			if layer.Bits[bit/64]&(1<<(bit%64)) == 0 {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// Card returns the number of items inserted (an exact counter, not the
// classic Bloom estimator — RedisBloom uses the counter approach too).
func (b *Bloom) Card() uint64 { return b.Inserted }

// Size returns total bit-array bytes across every layer.
func (b *Bloom) Size() uint64 {
	var n uint64
	for _, l := range b.Layers {
		n += uint64(len(l.Bits)) * 8
	}
	return n
}

// Marshal/Unmarshal — version-tagged binary encoding so AOF replay
// and DUMP/RESTORE round-trip cleanly.
//
// Layout:
//
//	[u8 version]
//	[f64 error_rate][u64 capacity][u64 expansion][u8 nonScaling][u64 inserted]
//	[u32 layer_count]
//	  per layer:
//	    [u64 m][u32 k][u64 capacity][u64 count]
//	    [u32 word_count][word_count * u64 words]

const bloomVersion = 1

func (b *Bloom) Marshal() ([]byte, error) {
	out := make([]byte, 0, 64+len(b.Layers)*64)
	out = append(out, bloomVersion)
	out = appendF64(out, b.ErrorRate)
	out = appendU64(out, b.Capacity)
	out = appendU64(out, b.Expansion)
	if b.NonScaling {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	out = appendU64(out, b.Inserted)
	out = appendU32(out, uint32(len(b.Layers)))
	for _, l := range b.Layers {
		out = appendU64(out, l.M)
		out = appendU32(out, l.K)
		out = appendU64(out, l.Capacity)
		out = appendU64(out, l.Count)
		out = appendU32(out, uint32(len(l.Bits)))
		for _, w := range l.Bits {
			out = appendU64(out, w)
		}
	}
	return out, nil
}

func UnmarshalBloom(in []byte) (*Bloom, error) {
	r := newReader(in)
	v, err := r.u8()
	if err != nil {
		return nil, err
	}
	if v != bloomVersion {
		return nil, errors.New("unsupported bloom version")
	}
	b := &Bloom{}
	if b.ErrorRate, err = r.f64(); err != nil {
		return nil, err
	}
	if b.Capacity, err = r.u64(); err != nil {
		return nil, err
	}
	if b.Expansion, err = r.u64(); err != nil {
		return nil, err
	}
	ns, err := r.u8()
	if err != nil {
		return nil, err
	}
	b.NonScaling = ns == 1
	if b.Inserted, err = r.u64(); err != nil {
		return nil, err
	}
	nl, err := r.u32()
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < nl; i++ {
		l := &bloomLayer{}
		if l.M, err = r.u64(); err != nil {
			return nil, err
		}
		if l.K, err = r.u32(); err != nil {
			return nil, err
		}
		if l.Capacity, err = r.u64(); err != nil {
			return nil, err
		}
		if l.Count, err = r.u64(); err != nil {
			return nil, err
		}
		nw, err := r.u32()
		if err != nil {
			return nil, err
		}
		l.Bits = make([]uint64, nw)
		for j := range l.Bits {
			if l.Bits[j], err = r.u64(); err != nil {
				return nil, err
			}
		}
		b.Layers = append(b.Layers, l)
	}
	return b, nil
}

// ─── tiny binary codec ─────────────────────────────────────────────

func appendU64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}
func appendU32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}
func appendF64(b []byte, v float64) []byte {
	return appendU64(b, math.Float64bits(v))
}

type reader struct {
	b []byte
	i int
}

func newReader(b []byte) *reader { return &reader{b: b} }

func (r *reader) u8() (byte, error) {
	if r.i >= len(r.b) {
		return 0, errors.New("eof")
	}
	v := r.b[r.i]
	r.i++
	return v, nil
}
func (r *reader) u32() (uint32, error) {
	if r.i+4 > len(r.b) {
		return 0, errors.New("eof")
	}
	v := binary.LittleEndian.Uint32(r.b[r.i:])
	r.i += 4
	return v, nil
}
func (r *reader) u64() (uint64, error) {
	if r.i+8 > len(r.b) {
		return 0, errors.New("eof")
	}
	v := binary.LittleEndian.Uint64(r.b[r.i:])
	r.i += 8
	return v, nil
}
func (r *reader) f64() (float64, error) {
	v, err := r.u64()
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}
