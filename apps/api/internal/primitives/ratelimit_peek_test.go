package primitives

import (
	"testing"
	"time"
)

// TestRateLimiterPeekDoesNotConsume — Peek evaluates the GCRA decision but
// leaves the bucket untouched, so QUOTA.SIMULATE never burns a token.
func TestRateLimiterPeekDoesNotConsume(t *testing.T) {
	r := NewRateLimiter()
	period := time.Second
	const max = 2

	// Many peeks must all pass and consume nothing.
	for i := 0; i < 5; i++ {
		ok, _, _, _ := r.Peek("k", period, max, 1)
		if !ok {
			t.Fatalf("peek %d denied — bucket was consumed", i)
		}
	}

	// Real calls now consume: two pass (burst), the third is rejected.
	if ok, _, _, _ := r.Allow("k", period, max, 1); !ok {
		t.Fatal("first Allow denied")
	}
	if ok, _, _, _ := r.Allow("k", period, max, 1); !ok {
		t.Fatal("second Allow denied")
	}
	if ok, _, retry, _ := r.Allow("k", period, max, 1); ok {
		t.Fatal("third Allow should be denied (burst exhausted)")
	} else if retry <= 0 {
		t.Fatalf("denied call returned non-positive retry %d", retry)
	}

	// A peek after exhaustion reflects the denial without changing state.
	if ok, _, _, _ := r.Peek("k", period, max, 1); ok {
		t.Fatal("peek should report denied after burst exhausted")
	}
}
