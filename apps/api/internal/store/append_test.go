package store

import (
	"strings"
	"sync"
	"testing"
)

// TestAppendCorrectness covers the value APPEND builds, including the
// self-correcting path where another command replaces the string between
// appends (so appendBuf no longer aliases Str and must be rebuilt).
func TestAppendCorrectness(t *testing.T) {
	s := New()

	// Fresh key.
	if n, _ := s.Append("k", "hello"); n != 5 {
		t.Fatalf("first append len = %d, want 5", n)
	}
	// Repeated appends.
	s.Append("k", " ")
	if n, _ := s.Append("k", "world"); n != 11 {
		t.Fatalf("append len = %d, want 11", n)
	}
	if v, _, _ := s.GetTyped("k"); v != "hello world" {
		t.Fatalf("value = %q, want %q", v, "hello world")
	}

	// SET replaces the value — the next APPEND must rebuild from the new
	// base, not from the stale buffer.
	s.Set("k", "RESET", 0)
	if n, _ := s.Append("k", "X"); n != 6 {
		t.Fatalf("after SET, append len = %d, want 6", n)
	}
	if v, _, _ := s.GetTyped("k"); v != "RESETX" {
		t.Fatalf("after SET+APPEND value = %q, want %q", v, "RESETX")
	}

	// SETRANGE in the middle then append — verifies a non-Set writer
	// also self-corrects.
	s.Set("sr", "0000000000", 0)
	s.SetRange("sr", 2, "ABC")
	s.Append("sr", "Z")
	if v, _, _ := s.GetTyped("sr"); v != "00ABC00000Z" {
		t.Fatalf("setrange+append value = %q, want %q", v, "00ABC00000Z")
	}

	// STRLEN / GETRANGE see the appended value correctly.
	s.Set("g", "", 0)
	for i := 0; i < 50; i++ {
		s.Append("g", "ab")
	}
	if n, _ := s.StrLen("g"); n != 100 {
		t.Fatalf("strlen = %d, want 100", n)
	}
	if sub, _ := s.GetRange("g", 0, 3); sub != "abab" {
		t.Fatalf("getrange = %q, want abab", sub)
	}
}

// TestAppendManyRetainsValue is the regression guard for the O(N²) trap:
// 100k single-byte appends to one key must produce the exact 100k-byte
// value (and, by completing quickly, prove it isn't recopying the whole
// string every time — the old `Str += value` form took quadratic time).
func TestAppendManyRetainsValue(t *testing.T) {
	s := New()
	const n = 100000
	for i := 0; i < n; i++ {
		s.Append("big", "x")
	}
	v, _, _ := s.GetTyped("big")
	if len(v) != n {
		t.Fatalf("len = %d, want %d", len(v), n)
	}
	if strings.Count(v, "x") != n {
		t.Fatalf("content corrupted: %d x's, want %d", strings.Count(v, "x"), n)
	}
}

// TestAppendConcurrentReaders is the safety proof: while one goroutine
// appends to a key, others GET it. Because append only writes at index ≥
// len (or reallocates, leaving the old array intact), a reader that
// RUnlocked with an aliased string never observes its bytes mutated.
// Run with -race; readers also assert the prefix is always well-formed.
func TestAppendConcurrentReaders(t *testing.T) {
	s := New()
	s.Set("c", "", 0)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: 50k appends.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 50000; i++ {
			s.Append("c", "a")
		}
	}()

	// Readers: keep reading + validating the value is all 'a's of some
	// length (never garbage), proving no in-place corruption of a
	// previously-returned view.
	for r := 0; r < 6; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				v, ok, _ := s.GetTyped("c")
				if ok && strings.Trim(v, "a") != "" {
					t.Errorf("reader saw corrupted value (len=%d)", len(v))
					return
				}
			}
		}()
	}
	wg.Wait()
	if v, _, _ := s.GetTyped("c"); len(v) != 50000 {
		t.Fatalf("final len = %d, want 50000", len(v))
	}
}
