package store

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEncodingString verifies the embstr / raw / int triage Redis
// uses on string values.
func TestEncodingString(t *testing.T) {
	s := New()
	s.Set("intish", "12345", 0)
	if got, _ := s.Object("intish"); got.Encoding != "int" {
		t.Errorf("pure-int string should be 'int', got %q", got.Encoding)
	}
	s.Set("short", "hello world", 0)
	if got, _ := s.Object("short"); got.Encoding != "embstr" {
		t.Errorf("short string should be 'embstr', got %q", got.Encoding)
	}
	s.Set("long", strings.Repeat("x", 100), 0)
	if got, _ := s.Object("long"); got.Encoding != "raw" {
		t.Errorf("long string should be 'raw', got %q", got.Encoding)
	}
}

// TestEncodingHash verifies listpack ↔ hashtable promotion.
func TestEncodingHash(t *testing.T) {
	s := New()
	s.HSet("small", "a", "1", "b", "2")
	if got, _ := s.Object("small"); got.Encoding != "listpack" {
		t.Errorf("small hash should be 'listpack', got %q", got.Encoding)
	}
	pairs := []string{}
	for i := 0; i < 200; i++ {
		pairs = append(pairs, "f"+strconv.Itoa(i), "v")
	}
	s.HSet("big", pairs...)
	if got, _ := s.Object("big"); got.Encoding != "hashtable" {
		t.Errorf("hash with >128 fields should be 'hashtable', got %q", got.Encoding)
	}
	// Long-value promotion (single field, >64-byte value).
	s.HSet("longval", "f", strings.Repeat("x", 100))
	if got, _ := s.Object("longval"); got.Encoding != "hashtable" {
		t.Errorf("hash with >64-byte value should be 'hashtable', got %q", got.Encoding)
	}
}

// TestEncodingSet covers intset / listpack / hashtable triage.
func TestEncodingSet(t *testing.T) {
	s := New()
	s.SAdd("ints", "1", "2", "3")
	if got, _ := s.Object("ints"); got.Encoding != "intset" {
		t.Errorf("all-int set should be 'intset', got %q", got.Encoding)
	}
	s.SAdd("strs", "foo", "bar")
	if got, _ := s.Object("strs"); got.Encoding != "listpack" {
		t.Errorf("small string set should be 'listpack', got %q", got.Encoding)
	}
	for i := 0; i < 200; i++ {
		s.SAdd("big", "m"+strconv.Itoa(i))
	}
	if got, _ := s.Object("big"); got.Encoding != "hashtable" {
		t.Errorf("big set should be 'hashtable', got %q", got.Encoding)
	}
}

// TestEncodingZSet covers listpack ↔ skiplist promotion.
func TestEncodingZSet(t *testing.T) {
	s := New()
	s.ZAdd("small", ZPair{1, "a"}, ZPair{2, "b"})
	if got, _ := s.Object("small"); got.Encoding != "listpack" {
		t.Errorf("small zset should be 'listpack', got %q", got.Encoding)
	}
	for i := 0; i < 200; i++ {
		s.ZAdd("big", ZPair{float64(i), "m" + strconv.Itoa(i)})
	}
	if got, _ := s.Object("big"); got.Encoding != "skiplist" {
		t.Errorf("big zset should be 'skiplist', got %q", got.Encoding)
	}
}

// TestEncodingList covers listpack ↔ quicklist promotion.
func TestEncodingList(t *testing.T) {
	s := New()
	s.RPush("small", "a", "b", "c")
	if got, _ := s.Object("small"); got.Encoding != "listpack" {
		t.Errorf("small list should be 'listpack', got %q", got.Encoding)
	}
	vals := []string{}
	for i := 0; i < 200; i++ {
		vals = append(vals, "x")
	}
	s.RPush("big", vals...)
	if got, _ := s.Object("big"); got.Encoding != "quicklist" {
		t.Errorf("big list should be 'quicklist', got %q", got.Encoding)
	}
}

// TestPeekTouchStateDoesntBumpHits verifies CLIENT NO-TOUCH's peek
// helper truly leaves the entry alone.
func TestPeekTouchStateDoesntBumpHits(t *testing.T) {
	s := New()
	s.Set("k", "v", 0)
	for i := 0; i < 5; i++ {
		s.Get("k")
	}
	hitsBefore, lastBefore, ok := s.PeekTouchState("k")
	if !ok {
		t.Fatal("PeekTouchState should report ok=true on existing key")
	}
	for i := 0; i < 10; i++ {
		s.PeekTouchState("k")
	}
	hitsAfter, _, _ := s.PeekTouchState("k")
	if hitsAfter != hitsBefore {
		t.Errorf("PeekTouchState bumped hits: before=%d after=%d", hitsBefore, hitsAfter)
	}
	_ = lastBefore
}

// TestRestoreTouchStateRollsBack verifies the round-trip semantics
// CLIENT NO-TOUCH relies on: after a Get then a Restore, the entry
// looks untouched.
func TestRestoreTouchStateRollsBack(t *testing.T) {
	s := New()
	s.Set("k", "v", 0)
	hitsBefore, lastBefore, _ := s.PeekTouchState("k")
	time.Sleep(2 * time.Millisecond) // make LastRead bump observable
	s.Get("k")
	s.Get("k")
	hitsMid, _, _ := s.PeekTouchState("k")
	if hitsMid <= hitsBefore {
		t.Fatalf("expected hits to increase after Get; before=%d after=%d", hitsBefore, hitsMid)
	}
	s.RestoreTouchState("k", hitsBefore, lastBefore)
	hitsAfter, lastAfter, _ := s.PeekTouchState("k")
	if hitsAfter != hitsBefore {
		t.Errorf("restore failed to roll back hits: %d → %d → %d", hitsBefore, hitsMid, hitsAfter)
	}
	if !lastAfter.Equal(lastBefore) {
		t.Errorf("restore failed to roll back LastRead")
	}
}
