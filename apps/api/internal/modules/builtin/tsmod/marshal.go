package tsmod

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

// Marshal/Unmarshal use a compact little-endian binary layout so AOF
// replay and DUMP/RESTORE round-trip cleanly. The format is internal —
// version-tagged so we can evolve it.
//
// Layout v1:
//
//   [u8 version]
//   [u64 retention][u64 chunkSize][u8 dupPolicy]
//   [u32 sample_count][sample_count * (i64 ts, f64 value)]
//   [u32 label_count][label_count * (u32 keylen, key, u32 vallen, val)]
//   [u32 rule_count][rule_count * (u32 destlen, dest, u8 agg, i64 bucket, i64 align)]
//   [u32 srclen, src]      // empty for plain series

const seriesVersion = 1

// Marshal serialises the series. Caller is responsible for snapshotting
// — we take a read lock to capture a consistent view.
func (s *Series) Marshal() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var buf bytes.Buffer
	buf.WriteByte(seriesVersion)
	writeU64(&buf, uint64(s.RetentionMs))
	writeU64(&buf, uint64(s.ChunkSize))
	buf.WriteByte(byte(s.DuplicateMode))

	writeU32(&buf, uint32(len(s.Samples)))
	for _, sm := range s.Samples {
		writeU64(&buf, uint64(sm.TS))
		writeU64(&buf, math.Float64bits(sm.Value))
	}

	writeU32(&buf, uint32(len(s.Labels)))
	for k, v := range s.Labels {
		writeU32(&buf, uint32(len(k)))
		buf.WriteString(k)
		writeU32(&buf, uint32(len(v)))
		buf.WriteString(v)
	}

	writeU32(&buf, uint32(len(s.Rules)))
	for _, r := range s.Rules {
		writeU32(&buf, uint32(len(r.DestKey)))
		buf.WriteString(r.DestKey)
		buf.WriteByte(byte(r.Aggregator))
		writeU64(&buf, uint64(r.BucketMs))
		writeU64(&buf, uint64(r.AlignTS))
	}

	writeU32(&buf, uint32(len(s.SourceKey)))
	buf.WriteString(s.SourceKey)
	return buf.Bytes(), nil
}

// Unmarshal restores a series from the wire bytes.
func Unmarshal(in []byte) (*Series, error) {
	r := &reader{b: in}
	v, err := r.u8()
	if err != nil || v != seriesVersion {
		return nil, errors.New("invalid TS series payload")
	}
	s := &Series{Labels: map[string]string{}}
	if v, err := r.u64(); err == nil {
		s.RetentionMs = int64(v)
	}
	if v, err := r.u64(); err == nil {
		s.ChunkSize = int64(v)
	}
	dp, _ := r.u8()
	s.DuplicateMode = DuplicatePolicy(dp)

	n, _ := r.u32()
	s.Samples = make([]Sample, n)
	for i := uint32(0); i < n; i++ {
		ts, _ := r.u64()
		val, _ := r.u64()
		s.Samples[i] = Sample{TS: int64(ts), Value: math.Float64frombits(val)}
	}

	n, _ = r.u32()
	for i := uint32(0); i < n; i++ {
		klen, _ := r.u32()
		k, _ := r.bytes(int(klen))
		vlen, _ := r.u32()
		val, _ := r.bytes(int(vlen))
		s.Labels[string(k)] = string(val)
	}

	n, _ = r.u32()
	for i := uint32(0); i < n; i++ {
		dlen, _ := r.u32()
		dest, _ := r.bytes(int(dlen))
		agg, _ := r.u8()
		bucket, _ := r.u64()
		align, _ := r.u64()
		s.Rules = append(s.Rules, &Rule{
			DestKey: string(dest), Aggregator: AggType(agg),
			BucketMs: int64(bucket), AlignTS: int64(align),
		})
	}
	if slen, err := r.u32(); err == nil && slen > 0 {
		src, _ := r.bytes(int(slen))
		s.SourceKey = string(src)
	}
	return s, nil
}

// ── tiny LE codec ─────────────────────────────────────────────────

func writeU32(b *bytes.Buffer, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	b.Write(buf[:])
}
func writeU64(b *bytes.Buffer, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	b.Write(buf[:])
}

type reader struct {
	b []byte
	i int
}

func (r *reader) u8() (uint8, error) {
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
func (r *reader) bytes(n int) ([]byte, error) {
	if r.i+n > len(r.b) {
		return nil, errors.New("eof")
	}
	v := r.b[r.i : r.i+n]
	r.i += n
	return v, nil
}
