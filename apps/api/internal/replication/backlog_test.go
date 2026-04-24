package replication

import (
	"bytes"
	"testing"
)

func TestBacklogAppendReadRoundTrip(t *testing.T) {
	b := NewBacklog(64)
	a := []byte("hello")
	b.Append(a)
	if got := b.LastOffset(); got != int64(len(a)) {
		t.Fatalf("offset=%d want %d", got, len(a))
	}
	out, ok := b.Slice(0, int64(len(a)))
	if !ok || !bytes.Equal(out, a) {
		t.Fatalf("slice=%q ok=%v want %q", out, ok, a)
	}
}

func TestBacklogWraps(t *testing.T) {
	b := NewBacklog(8)
	// Fill past capacity: three 4-byte appends = 12 bytes, oldest 4 evicted.
	b.Append([]byte("AAAA"))
	b.Append([]byte("BBBB"))
	b.Append([]byte("CCCC"))
	if b.FirstOffset() != 4 {
		t.Fatalf("first=%d want 4", b.FirstOffset())
	}
	if b.LastOffset() != 12 {
		t.Fatalf("last=%d want 12", b.LastOffset())
	}
	if _, ok := b.Slice(0, 4); ok {
		t.Fatal("offset 0 should have been evicted")
	}
	got, ok := b.Slice(4, 8)
	if !ok || !bytes.Equal(got, []byte("BBBBCCCC")) {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
}

func TestBacklogOversizeAppend(t *testing.T) {
	b := NewBacklog(4)
	big := []byte("0123456789")
	b.Append(big)
	// Ring holds only the last 4 bytes.
	got, ok := b.Slice(6, 4)
	if !ok || !bytes.Equal(got, []byte("6789")) {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
	if b.LastOffset() != 10 {
		t.Fatalf("last=%d want 10", b.LastOffset())
	}
}

func TestBacklogContains(t *testing.T) {
	b := NewBacklog(16)
	b.Append([]byte("abcdef"))
	if !b.Contains(0) || !b.Contains(6) {
		t.Fatal("edges should be contained")
	}
	if b.Contains(7) {
		t.Fatal("offset past last should not be contained")
	}
}
