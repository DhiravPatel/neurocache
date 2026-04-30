package store

import (
	"testing"
	"time"
)

// ── HGETDEL ───────────────────────────────────────────────────────

func TestHGetDelMixedHits(t *testing.T) {
	s := New()
	s.HSet("h", "a", "1", "b", "2", "c", "3")
	values, hits, err := s.HGetDel("h", []string{"a", "missing", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if !hits[0] || values[0] != "1" {
		t.Fatalf("a should be hit with value 1, got %q hit=%v", values[0], hits[0])
	}
	if hits[1] || values[1] != "" {
		t.Fatalf("missing should not be hit, got %q hit=%v", values[1], hits[1])
	}
	if !hits[2] || values[2] != "3" {
		t.Fatalf("c should be hit, got %q hit=%v", values[2], hits[2])
	}
	// Deleted fields should be gone; surviving field b should remain.
	if _, ok, _ := s.HGet("h", "a"); ok {
		t.Fatal("a should have been deleted")
	}
	if v, ok, _ := s.HGet("h", "b"); !ok || v != "2" {
		t.Fatalf("b should remain with value 2, got %q ok=%v", v, ok)
	}
}

func TestHGetDelLastFieldRemovesKey(t *testing.T) {
	s := New()
	s.HSet("h", "only", "value")
	_, _, _ = s.HGetDel("h", []string{"only"})
	if s.Type("h") != TypeNone {
		t.Fatalf("hash should disappear when last field is deleted, type=%v", s.Type("h"))
	}
}

// ── HGETEX ────────────────────────────────────────────────────────

func TestHGetExSetsTTL(t *testing.T) {
	s := New()
	s.HSet("h", "a", "1", "b", "2")
	values, hits, err := s.HGetEx("h", []string{"a", "b"}, "EX", 60)
	if err != nil {
		t.Fatal(err)
	}
	if !hits[0] || values[0] != "1" || !hits[1] || values[1] != "2" {
		t.Fatalf("expected values [1 2], got %v", values)
	}
	ttls, _ := s.HTTL("h", []string{"a", "b"}, false)
	if ttls[0] <= 0 || ttls[0] > 60 {
		t.Fatalf("a TTL = %v, want 0<x<=60", ttls[0])
	}
	if ttls[1] <= 0 || ttls[1] > 60 {
		t.Fatalf("b TTL = %v, want 0<x<=60", ttls[1])
	}
}

func TestHGetExPersist(t *testing.T) {
	s := New()
	s.HSet("h", "a", "1")
	s.HExpire("h", 60*time.Second, []string{"a"}, "")
	_, _, err := s.HGetEx("h", []string{"a"}, "PERSIST", 0)
	if err != nil {
		t.Fatal(err)
	}
	ttls, _ := s.HTTL("h", []string{"a"}, false)
	if ttls[0] != -1 {
		t.Fatalf("expected -1 (no TTL) after PERSIST, got %v", ttls[0])
	}
}

func TestHGetExAbsoluteExpiry(t *testing.T) {
	s := New()
	s.HSet("h", "a", "1")
	at := time.Now().Add(2 * time.Hour).Unix()
	_, _, err := s.HGetEx("h", []string{"a"}, "EXAT", at)
	if err != nil {
		t.Fatal(err)
	}
	exps, _ := s.HExpireTime("h", []string{"a"}, false)
	if exps[0] != at {
		t.Fatalf("expected absolute expiry %d, got %d", at, exps[0])
	}
}

// ── HSETEX ────────────────────────────────────────────────────────

func TestHSetExWritesAllAndSetsTTL(t *testing.T) {
	s := New()
	n, err := s.HSetEx("h", 30*time.Second, "", []string{"a", "1", "b", "2"})
	if err != nil || n != 1 {
		t.Fatalf("first call result=%d err=%v", n, err)
	}
	if v, _, _ := s.HGet("h", "a"); v != "1" {
		t.Fatalf("a should be 1, got %q", v)
	}
	ttls, _ := s.HTTL("h", []string{"a", "b"}, false)
	if ttls[0] <= 0 || ttls[1] <= 0 {
		t.Fatalf("ttls should be positive, got %v", ttls)
	}
}

func TestHSetExFNXAtomic(t *testing.T) {
	s := New()
	s.HSet("h", "exists", "old")
	// FNX must reject because "exists" already exists — and atomically
	// must not write the "new" field either.
	n, _ := s.HSetEx("h", 0, "FNX", []string{"new", "1", "exists", "x"})
	if n != 0 {
		t.Fatalf("FNX should reject when any field already exists, got %d", n)
	}
	if _, ok, _ := s.HGet("h", "new"); ok {
		t.Fatal("new field must NOT have been written under failing FNX")
	}
}

func TestHSetExFXXAtomic(t *testing.T) {
	s := New()
	s.HSet("h", "exists", "old")
	// FXX requires every field to already exist — "missing" doesn't, so
	// the call should be rejected wholesale.
	n, _ := s.HSetEx("h", 0, "FXX", []string{"exists", "new", "missing", "x"})
	if n != 0 {
		t.Fatalf("FXX should reject when any field is missing, got %d", n)
	}
	if v, _, _ := s.HGet("h", "exists"); v != "old" {
		t.Fatalf("exists should remain 'old', got %q", v)
	}
}

// ── HEXPIRETIME / HPEXPIRETIME ────────────────────────────────────

func TestHExpireTimeReportsAbsolute(t *testing.T) {
	s := New()
	s.HSet("h", "a", "1", "b", "2")
	at := time.Now().Add(time.Hour)
	s.HExpireAt("h", at, []string{"a"}, "")
	out, _ := s.HExpireTime("h", []string{"a", "b", "missing"}, false)
	if out[0] < time.Now().Unix() {
		t.Fatalf("a expiry = %d (now=%d)", out[0], time.Now().Unix())
	}
	if out[1] != -1 {
		t.Fatalf("b should report -1 (no TTL), got %d", out[1])
	}
	if out[2] != -2 {
		t.Fatalf("missing should report -2, got %d", out[2])
	}
}

func TestHPExpireTimeMs(t *testing.T) {
	s := New()
	s.HSet("h", "a", "1")
	at := time.Now().Add(time.Hour)
	s.HExpireAt("h", at, []string{"a"}, "")
	out, _ := s.HExpireTime("h", []string{"a"}, true)
	if out[0] < time.Now().UnixMilli() {
		t.Fatalf("ms expiry too small: %d", out[0])
	}
}
