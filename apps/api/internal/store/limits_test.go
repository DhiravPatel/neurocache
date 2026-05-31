package store

import "testing"

// TestSetBitOffsetCapped: a huge SETBIT offset must be refused, not turned
// into a multi-gigabyte allocation.
func TestSetBitOffsetCapped(t *testing.T) {
	s := New()
	if _, err := s.SetBit("k", 99999999999999, 1); err == nil {
		t.Fatal("SETBIT with an out-of-range offset must error")
	}
	// A normal offset still works.
	if _, err := s.SetBit("k", 100, 1); err != nil {
		t.Fatalf("normal SETBIT failed: %v", err)
	}
}

// TestSetRangeOffsetCapped: a huge SETRANGE offset must be refused.
func TestSetRangeOffsetCapped(t *testing.T) {
	s := New()
	if _, err := s.SetRange("k", 99999999999, "x"); err == nil {
		t.Fatal("SETRANGE with an out-of-range offset must error")
	}
	if n, err := s.SetRange("k", 5, "hi"); err != nil || n != 7 {
		t.Fatalf("normal SETRANGE failed: n=%d err=%v", n, err)
	}
}

// TestLCSRefusesHugeProduct: LCS over two large values must error rather
// than allocating an O(n*m) DP table that OOMs the server.
func TestLCSRefusesHugeProduct(t *testing.T) {
	s := New()
	big := make([]byte, 20*1024*1024) // 20 MiB each -> 4e14-cell table
	for i := range big {
		big[i] = 'a'
	}
	s.Set("a", string(big), 0)
	s.Set("b", string(big), 0)
	if _, _, _, err := s.LCS("a", "b", "len", 0); err == nil {
		t.Fatal("LCS over oversized strings must error, not allocate")
	}
	// Small inputs still compute.
	s.Set("x", "ohmytext", 0)
	s.Set("y", "mynewtext", 0)
	if _, n, _, err := s.LCS("x", "y", "len", 0); err != nil || n != 6 {
		t.Fatalf("normal LCS failed: n=%d err=%v", n, err)
	}
}

// TestLZFDecompressBounded: a decompressed-length header claiming more than
// the 512 MiB ceiling is rejected (decompression bomb guard).
func TestLZFDecompressBounded(t *testing.T) {
	if _, err := lzfDecompress([]byte{0x00, 0x41}, 1<<30); err == nil {
		t.Fatal("lzfDecompress must reject an oversized ulen")
	}
	if _, err := lzfDecompress([]byte{0x00, 0x41}, -1); err == nil {
		t.Fatal("lzfDecompress must reject a negative ulen")
	}
}

// TestIntsetDecodeBoundedAlloc: a crafted intset header with a 4-billion
// element count must not pre-allocate a giant slice — the cap-hint is
// bounded by the bytes actually present, so this returns an error quickly.
func TestIntsetDecodeBoundedAlloc(t *testing.T) {
	// enc=2, n=0xFFFFFFFF, but only 8 header bytes follow -> must error,
	// and critically must not try to make([]string, 0, 4e9).
	b := []byte{2, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF}
	if _, err := intsetDecode(b); err == nil {
		t.Fatal("intsetDecode must error on a truncated oversized-count payload")
	}
}
