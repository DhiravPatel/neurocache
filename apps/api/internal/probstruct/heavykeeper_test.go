package probstruct

import (
	"strconv"
	"testing"
)

// TestHeavyKeeperFindsTrueHeavyHitter verifies the algorithm's
// headline guarantee: a key seen disproportionately often shows up at
// the top of the heap even when many other keys compete for buckets.
func TestHeavyKeeperFindsTrueHeavyHitter(t *testing.T) {
	hk := New(5, 16, 4, 0.9)
	// 2 000 unique noise keys, each seen once.
	for i := 0; i < 2000; i++ {
		hk.Add("noise-" + strconv.Itoa(i))
	}
	// Heavy hitter — seen 500 times.
	for i := 0; i < 500; i++ {
		hk.Add("heavy")
	}
	top := hk.Top(0)
	if len(top) == 0 {
		t.Fatal("heap should not be empty after 2500 adds")
	}
	found := false
	for i, h := range top {
		if h.Item == "heavy" {
			found = true
			if i != 0 {
				t.Errorf("heavy should be first, was at position %d", i)
			}
			if h.Count < 100 {
				t.Errorf("heavy count = %d, expected ≥100", h.Count)
			}
			break
		}
	}
	if !found {
		t.Fatalf("heavy hitter dropped from heap; got top: %v", top)
	}
}

func TestHeavyKeeperRespectsK(t *testing.T) {
	hk := New(3, 64, 6, 0.9)
	for i := 0; i < 50; i++ {
		hk.IncrBy(strconv.Itoa(i), uint64(i+1))
	}
	top := hk.Top(0)
	if len(top) > 3 {
		t.Fatalf("heap size %d exceeds K=3", len(top))
	}
	// HeavyKeeper is probabilistic — the heap can occasionally hold
	// items with mid-range true counts when decay fails to evict a
	// high-count resident. The contract we *can* test: at least one
	// of the three largest true counts (items "47", "48", "49" with
	// true counts 48/49/50) lands in the heap. Anything tighter
	// risks flaking on the global PRNG seed.
	wantAny := map[string]bool{"47": true, "48": true, "49": true}
	for _, h := range top {
		if wantAny[h.Item] {
			return
		}
	}
	t.Fatalf("expected at least one of the three largest items in the heap; got %v", top)
}

func TestHeavyKeeperReset(t *testing.T) {
	hk := New(5, 8, 4, 0.9)
	for i := 0; i < 100; i++ {
		hk.Add("x")
	}
	if len(hk.Top(0)) == 0 {
		t.Fatal("expected non-empty heap")
	}
	hk.Reset()
	if len(hk.Top(0)) != 0 {
		t.Fatal("Reset should empty the heap")
	}
	if hk.Stats().Observations != 0 {
		t.Fatal("Reset should zero the observation counter")
	}
}

func TestHeavyKeeperResize(t *testing.T) {
	hk := New(5, 8, 4, 0.9)
	for i := 0; i < 10; i++ {
		hk.Add(strconv.Itoa(i))
	}
	if !hk.Resize(20) {
		t.Fatal("Resize should report changed = true")
	}
	if hk.Stats().K != 20 {
		t.Fatalf("after resize K = %d, want 20", hk.Stats().K)
	}
	if hk.Resize(20) {
		t.Fatal("Resize to same K should report changed = false")
	}
}

func TestHeavyKeeperCountIsMonotonic(t *testing.T) {
	hk := New(5, 8, 4, 0.9)
	hk.IncrBy("k", 10)
	c1 := hk.Count("k")
	hk.IncrBy("k", 5)
	c2 := hk.Count("k")
	if c2 < c1 {
		t.Fatalf("Count should not decrease across same-key adds (was %d → %d)", c1, c2)
	}
}

func TestHeavyKeeperZeroDeltaIsNoOp(t *testing.T) {
	hk := New(5, 8, 4, 0.9)
	if displaced := hk.IncrBy("x", 0); displaced != "" {
		t.Fatalf("zero delta should not displace; got %q", displaced)
	}
	if hk.Stats().Observations != 0 {
		t.Fatal("zero delta should not bump observation counter")
	}
}
