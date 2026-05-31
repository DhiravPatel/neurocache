package resp

import (
	"bufio"
	"testing"
)

// repeatReader yields p over and over, never returning EOF — lets a single
// bufio.Reader feed readArray b.N times without re-allocating per iteration.
type repeatReader struct {
	p   []byte
	off int
}

func (r *repeatReader) Read(b []byte) (int, error) {
	n := 0
	for n < len(b) {
		if r.off == len(r.p) {
			r.off = 0
		}
		c := copy(b[n:], r.p[r.off:])
		r.off += c
		n += c
	}
	return n, nil
}

func benchReadArray(b *testing.B, cmd []byte) {
	br := bufio.NewReaderSize(&repeatReader{p: cmd}, 128*1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parts, err := readArray(br)
		if err != nil || len(parts) == 0 {
			b.Fatalf("readArray: parts=%d err=%v", len(parts), err)
		}
	}
}

// BenchmarkReadArraySET — the canonical 3-arg command. 1 array header + 3
// bulk headers + 3 trailing CRLFs are the length lines we parse.
func BenchmarkReadArraySET(b *testing.B) {
	benchReadArray(b, []byte("*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"))
}

// BenchmarkReadArrayHSET — the 59%-of-Redis outlier shape (4 args).
func BenchmarkReadArrayHSET(b *testing.B) {
	benchReadArray(b, []byte("*4\r\n$4\r\nHSET\r\n$1\r\nh\r\n$1\r\nf\r\n$1\r\nv\r\n"))
}

// BenchmarkReadArrayGET — the 2-arg read shape.
func BenchmarkReadArrayGET(b *testing.B) {
	benchReadArray(b, []byte("*2\r\n$3\r\nGET\r\n$3\r\nfoo\r\n"))
}
