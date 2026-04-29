package probmod

import (
	"errors"
	"math"
)

// CMS is a Count-Min Sketch — a probabilistic frequency estimator that
// over-counts (never under-counts), with error bound `error_rate * total`
// and confidence `1 - probability`. width chooses the per-row bucket
// count, depth chooses the number of independent rows.
//
// This implementation matches the Redis CMS API: INITBYDIM takes width
// + depth directly; INITBYPROB derives them from the desired error
// guarantee. INCRBY accumulates, QUERY returns min(row_i[h_i(item)]),
// MERGE folds multiple sketches with optional weights.
type CMS struct {
	Width uint64
	Depth uint64
	Count uint64 // total events incremented (sum of all increments)
	Rows  [][]uint64
}

// NewCMSByDim allocates a sketch of width × depth.
func NewCMSByDim(width, depth uint64) (*CMS, error) {
	if width == 0 || depth == 0 {
		return nil, errors.New("width and depth must be > 0")
	}
	c := &CMS{Width: width, Depth: depth}
	c.Rows = make([][]uint64, depth)
	for i := range c.Rows {
		c.Rows[i] = make([]uint64, width)
	}
	return c, nil
}

// NewCMSByProb derives width + depth from the standard CMS formulas:
//
//	width = ceil(e / errRate)
//	depth = ceil(ln(1 / probability))
func NewCMSByProb(errRate, probability float64) (*CMS, error) {
	if errRate <= 0 || errRate >= 1 {
		return nil, errors.New("error rate must be in (0,1)")
	}
	if probability <= 0 || probability >= 1 {
		return nil, errors.New("probability must be in (0,1)")
	}
	width := uint64(math.Ceil(math.E / errRate))
	depth := uint64(math.Ceil(math.Log(1 / probability)))
	if width < 2 {
		width = 2
	}
	if depth < 1 {
		depth = 1
	}
	return NewCMSByDim(width, depth)
}

// IncrBy adds delta to the count of item, returning the post-increment
// minimum across rows (i.e. the latest estimate).
func (c *CMS) IncrBy(item []byte, delta int64) int64 {
	if delta < 0 {
		// Redis only supports positive increments — match that.
		return c.Query(item)
	}
	h1, h2 := pair(item)
	min := uint64(math.MaxUint64)
	for i := uint64(0); i < c.Depth; i++ {
		col := (h1 + i*h2) % c.Width
		c.Rows[i][col] += uint64(delta)
		if c.Rows[i][col] < min {
			min = c.Rows[i][col]
		}
	}
	c.Count += uint64(delta)
	return int64(min)
}

// Query returns the current estimate (min over rows) for item.
func (c *CMS) Query(item []byte) int64 {
	h1, h2 := pair(item)
	min := uint64(math.MaxUint64)
	for i := uint64(0); i < c.Depth; i++ {
		col := (h1 + i*h2) % c.Width
		if c.Rows[i][col] < min {
			min = c.Rows[i][col]
		}
	}
	if min == math.MaxUint64 {
		return 0
	}
	return int64(min)
}

// Merge folds src into c, optionally scaling each cell by weight.
// Returns an error when dimensions don't line up. When weights == nil,
// each source contributes 1×.
func (c *CMS) Merge(srcs []*CMS, weights []uint64) error {
	for _, s := range srcs {
		if s.Width != c.Width || s.Depth != c.Depth {
			return errors.New("CMS dimension mismatch")
		}
	}
	for si, s := range srcs {
		w := uint64(1)
		if weights != nil && si < len(weights) {
			w = weights[si]
		}
		for i := range c.Rows {
			for j := range c.Rows[i] {
				c.Rows[i][j] += s.Rows[i][j] * w
			}
		}
		c.Count += s.Count * w
	}
	return nil
}

// Marshal/Unmarshal — straightforward LE-uint64 dump.
const cmsVersion = 1

func (c *CMS) Marshal() ([]byte, error) {
	out := make([]byte, 0, 32+int(c.Width*c.Depth)*8)
	out = append(out, cmsVersion)
	out = appendU64(out, c.Width)
	out = appendU64(out, c.Depth)
	out = appendU64(out, c.Count)
	for _, row := range c.Rows {
		for _, v := range row {
			out = appendU64(out, v)
		}
	}
	return out, nil
}

func UnmarshalCMS(in []byte) (*CMS, error) {
	r := newReader(in)
	v, err := r.u8()
	if err != nil {
		return nil, err
	}
	if v != cmsVersion {
		return nil, errors.New("unsupported CMS version")
	}
	c := &CMS{}
	if c.Width, err = r.u64(); err != nil {
		return nil, err
	}
	if c.Depth, err = r.u64(); err != nil {
		return nil, err
	}
	if c.Count, err = r.u64(); err != nil {
		return nil, err
	}
	c.Rows = make([][]uint64, c.Depth)
	for i := range c.Rows {
		c.Rows[i] = make([]uint64, c.Width)
		for j := range c.Rows[i] {
			if c.Rows[i][j], err = r.u64(); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}
