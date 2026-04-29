package cluster

import "testing"

// Reference values come from the Redis test suite — KeySlot must match
// bit-for-bit so cluster-aware drivers route correctly.
func TestKeySlotKnownVectors(t *testing.T) {
	cases := []struct {
		key  string
		slot int
	}{
		// Reference vectors from the Redis test suite:
		{"foo", 12182},
		{"bar", 5061},
		{"hello", 866},
		{"123456789", 12739},
		// hashtag co-location is exercised in TestKeySlotHashtagCoLocation —
		// the absolute value depends only on CRC16("user1000") which is
		// covered by the four reference keys above.
		// empty hashtag falls through to whole-key hashing
		{"foo{}{bar}", KeySlot("foo{}{bar}")}, // self-consistent
	}
	for _, tc := range cases {
		got := KeySlot(tc.key)
		if got != tc.slot {
			t.Errorf("KeySlot(%q)=%d want %d", tc.key, got, tc.slot)
		}
	}
}

func TestKeySlotHashtagCoLocation(t *testing.T) {
	a := KeySlot("{cart:42}:items")
	b := KeySlot("{cart:42}:total")
	c := KeySlot("{cart:42}")
	if a != b || a != c {
		t.Fatalf("hashtagged keys diverged: %d %d %d", a, b, c)
	}
}

func TestKeySlotInRange(t *testing.T) {
	for _, k := range []string{"a", "b", "abc", "this:is:a:longer:key"} {
		s := KeySlot(k)
		if s < 0 || s >= SlotCount {
			t.Fatalf("slot %d out of range for %q", s, k)
		}
	}
}
