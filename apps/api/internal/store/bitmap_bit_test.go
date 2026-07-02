package store

import "testing"

// TestBitCountBitUnit pins the Redis 7.0 `BITCOUNT ... BIT` semantics.
// The canonical example from the Redis docs: SET mykey "foobar";
// BITCOUNT mykey 5 30 BIT == 17. Byte-range results must be unchanged.
func TestBitCountBitUnit(t *testing.T) {
	s := New()
	s.Set("mykey", "foobar", 0)

	cases := []struct {
		start, end    int
		hasRange, bit bool
		want          int
	}{
		{0, -1, false, false, 26}, // whole string
		{0, 0, true, false, 4},    // byte 0 = 'f' = 0x66
		{1, 1, true, false, 6},    // byte 1 = 'o' = 0x6f
		{5, 30, true, true, 17},   // Redis docs canonical BIT example
		{0, 5, true, true, 3},     // first six bits of 'f' = 011001 → 3 set
		{0, -1, true, true, 26},   // all bits via BIT range
	}
	for _, tc := range cases {
		got, err := s.BitCount("mykey", tc.start, tc.end, tc.hasRange, tc.bit)
		if err != nil {
			t.Fatalf("BitCount(%d,%d,bit=%v): %v", tc.start, tc.end, tc.bit, err)
		}
		if got != tc.want {
			t.Errorf("BitCount(%d,%d,hasRange=%v,bit=%v) = %d, want %d",
				tc.start, tc.end, tc.hasRange, tc.bit, got, tc.want)
		}
	}
}

// TestBitPosBitUnit pins `BITPOS ... BIT` and the clear-bit edge rules.
func TestBitPosBitUnit(t *testing.T) {
	s := New()

	// 0xff 0xf0 0x00 — first clear bit is at index 12.
	s.Set("k1", string([]byte{0xff, 0xf0, 0x00}), 0)
	if got, _ := s.BitPos("k1", 0, 0, -1, false, false); got != 12 {
		t.Errorf("BITPOS k1 0 = %d, want 12", got)
	}
	// Same, expressed as a BIT range covering the whole string.
	if got, _ := s.BitPos("k1", 0, 0, 23, true, true); got != 12 {
		t.Errorf("BITPOS k1 0 0 23 BIT = %d, want 12", got)
	}

	// 0x00 0x0f 0xff — first set bit is at index 12.
	s.Set("k2", string([]byte{0x00, 0x0f, 0xff}), 0)
	if got, _ := s.BitPos("k2", 1, 0, -1, false, false); got != 12 {
		t.Errorf("BITPOS k2 1 = %d, want 12", got)
	}
	// Constrain to bit range [16,23]: first set bit is bit 16.
	if got, _ := s.BitPos("k2", 1, 16, 23, true, true); got != 16 {
		t.Errorf("BITPOS k2 1 16 23 BIT = %d, want 16", got)
	}

	// All-ones with no end, hunting for a clear bit → first bit past the
	// string (Redis's right-edge zero-padding rule): 0xff -> 8.
	s.Set("k3", string([]byte{0xff}), 0)
	if got, _ := s.BitPos("k3", 0, 0, -1, false, false); got != 8 {
		t.Errorf("BITPOS k3 0 (no end) = %d, want 8", got)
	}
	// All-ones WITH an explicit end and no clear bit → -1.
	if got, _ := s.BitPos("k3", 0, 0, 7, true, false); got != -1 {
		t.Errorf("BITPOS k3 0 0 7 = %d, want -1", got)
	}
}
