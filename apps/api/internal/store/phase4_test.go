package store

import (
	"strconv"
	"testing"
	"time"
)

// ── DELEX ─────────────────────────────────────────────────────────

func TestDelExMatchingValueDeletes(t *testing.T) {
	s := New()
	s.Set("lease", "owner-A", 0)
	n, err := s.DelEx("lease", "owner-A")
	if err != nil || n != 1 {
		t.Fatalf("DelEx match: n=%d err=%v", n, err)
	}
	if s.Type("lease") != TypeNone {
		t.Fatal("key should be gone after matching DelEx")
	}
}

func TestDelExWrongValueIsNoop(t *testing.T) {
	s := New()
	s.Set("lease", "owner-A", 0)
	n, err := s.DelEx("lease", "owner-B")
	if err != nil || n != 0 {
		t.Fatalf("DelEx mismatch: n=%d err=%v", n, err)
	}
	if v, _ := s.Get("lease"); v != "owner-A" {
		t.Fatalf("non-matching DelEx must not delete; got %q", v)
	}
}

func TestDelExMissingReturnsMinusOne(t *testing.T) {
	s := New()
	n, err := s.DelEx("nope", "x")
	if err != nil || n != -1 {
		t.Fatalf("missing key result: n=%d err=%v, want -1", n, err)
	}
}

func TestDelExWrongTypeErrors(t *testing.T) {
	s := New()
	s.LPush("list", "a")
	if _, err := s.DelEx("list", "a"); err != ErrWrongType {
		t.Fatalf("wrong type should be WRONGTYPE, got %v", err)
	}
}

// ── DIGEST ────────────────────────────────────────────────────────

func TestDigestStableForString(t *testing.T) {
	s := New()
	s.Set("k", "hello", 0)
	d1, ok, err := s.Digest("k")
	if err != nil || !ok || len(d1) != 40 {
		t.Fatalf("digest result: d=%q ok=%v err=%v", d1, ok, err)
	}
	d2, _, _ := s.Digest("k")
	if d1 != d2 {
		t.Fatalf("digest must be stable for same content: %q vs %q", d1, d2)
	}
}

func TestDigestChangesWhenValueChanges(t *testing.T) {
	s := New()
	s.Set("k", "hello", 0)
	d1, _, _ := s.Digest("k")
	s.Set("k", "goodbye", 0)
	d2, _, _ := s.Digest("k")
	if d1 == d2 {
		t.Fatal("digest must change when value changes")
	}
}

func TestDigestStableAcrossInsertionOrder(t *testing.T) {
	a := New()
	a.HSet("h", "one", "1", "two", "2", "three", "3")
	b := New()
	b.HSet("h", "three", "3", "one", "1", "two", "2") // different order
	d1, _, _ := a.Digest("h")
	d2, _, _ := b.Digest("h")
	if d1 != d2 {
		t.Fatalf("digest should be insertion-order independent for hashes:\n  a=%s\n  b=%s", d1, d2)
	}
}

func TestDigestMissingKeyOk(t *testing.T) {
	s := New()
	d, ok, err := s.Digest("nope")
	if err != nil || ok || d != "" {
		t.Fatalf("missing-key digest: d=%q ok=%v err=%v", d, ok, err)
	}
}

// ── MSETEX ────────────────────────────────────────────────────────

func TestMSetExSetsAllWithTTL(t *testing.T) {
	s := New()
	if err := s.MSetEx(60*time.Second, "a", "1", "b", "2", "c", "3"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("a"); v != "1" {
		t.Fatalf("a should be 1, got %q", v)
	}
	if d := s.TTL("a"); d <= 0 || d > 60*time.Second {
		t.Fatalf("a TTL out of range: %v", d)
	}
	if d := s.TTL("c"); d <= 0 || d > 60*time.Second {
		t.Fatalf("c TTL out of range: %v", d)
	}
}

func TestMSetExRejectsZeroTTL(t *testing.T) {
	s := New()
	if err := s.MSetEx(0, "a", "1"); err == nil {
		t.Fatal("MSETEX with TTL=0 must error")
	}
}

func TestMSetExRequiresEvenPairs(t *testing.T) {
	s := New()
	if err := s.MSetEx(time.Second, "a", "1", "b"); err == nil {
		t.Fatal("MSETEX with odd argc must error")
	}
}

// ── XACKDEL ───────────────────────────────────────────────────────

// helper: add an entry, create a group, deliver it to a consumer so
// the group's PEL holds a reference, return the entry's ID.
func setupPendingEntry(t *testing.T, s *Store, key, group, consumer string) string {
	t.Helper()
	id, err := s.XAdd(key, "*", []string{"f", "v"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.XGroupCreate(key, group, "0", false); err != nil {
		t.Fatal(err)
	}
	out, err := s.XReadGroup(group, consumer, []string{key}, []string{">"}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out[key]) == 0 {
		t.Fatalf("expected delivery on %s", key)
	}
	return id
}

func TestXAckDelAcksAndRemoves(t *testing.T) {
	s := New()
	id := setupPendingEntry(t, s, "k", "g", "c1")
	n, err := s.XAckDel("k", "g", id)
	if err != nil || n != 1 {
		t.Fatalf("XACKDEL: n=%d err=%v", n, err)
	}
	if length, _ := s.XLen("k"); length != 0 {
		t.Fatalf("entry should be gone from stream, len=%d", length)
	}
}

func TestXAckDelUnknownGroupErrors(t *testing.T) {
	s := New()
	s.XAdd("k", "*", []string{"f", "v"}, 0)
	if _, err := s.XAckDel("k", "ghost", "1-1"); err == nil {
		t.Fatal("XACKDEL with unknown group must error")
	}
}

// ── XDELEX ────────────────────────────────────────────────────────

func TestXDelExKeepRefRemovesUnconditionally(t *testing.T) {
	s := New()
	id := setupPendingEntry(t, s, "k", "g", "c1")
	n, err := s.XDelEx("k", XDelExKeepRef, id)
	if err != nil || n != 1 {
		t.Fatalf("KEEPREF: n=%d err=%v", n, err)
	}
}

func TestXDelExRefRefusesPending(t *testing.T) {
	s := New()
	id := setupPendingEntry(t, s, "k", "g", "c1")
	n, _ := s.XDelEx("k", XDelExRef, id)
	if n != 0 {
		t.Fatalf("REF should refuse pending entries, removed %d", n)
	}
	if length, _ := s.XLen("k"); length != 1 {
		t.Fatalf("entry should still be in stream, len=%d", length)
	}
}

func TestXDelExAckedDeletesUnreferenced(t *testing.T) {
	s := New()
	id, _ := s.XAdd("k", "*", []string{"f", "v"}, 0)
	if err := s.XGroupCreate("k", "g", "0", false); err != nil {
		t.Fatal(err)
	}
	// no XREADGROUP — entry was never delivered, so no group references it.
	n, _ := s.XDelEx("k", XDelExAcked, id)
	if n != 1 {
		t.Fatalf("ACKED should delete unreferenced entries, removed %d", n)
	}
}

func TestParseXDelExMode(t *testing.T) {
	cases := map[string]XDelExMode{
		"":        XDelExKeepRef,
		"keepref": XDelExKeepRef,
		"REF":     XDelExRef,
		"acked":   XDelExAcked,
	}
	for in, want := range cases {
		got, err := ParseXDelExMode(in)
		if err != nil || got != want {
			t.Errorf("ParseXDelExMode(%q) = %v err=%v, want %v", in, got, err, want)
		}
	}
	if _, err := ParseXDelExMode("nonsense"); err == nil {
		t.Fatal("invalid mode should error")
	}
}

// ── XCFGSET ───────────────────────────────────────────────────────

func TestXCfgSetAndGet(t *testing.T) {
	s := New()
	s.XAdd("k", "*", []string{"f", "v"}, 0)
	if err := s.XGroupCreate("k", "g", "0", false); err != nil {
		t.Fatal(err)
	}
	out, err := s.XCfgSet("k", "g", GroupConfig{MaxDeliveries: 5, MinIdleMs: 1000})
	if err != nil || out.MaxDeliveries != 5 || out.MinIdleMs != 1000 {
		t.Fatalf("XCfgSet returned %+v err=%v", out, err)
	}
	got, _ := s.XCfgGet("k", "g")
	if got.MaxDeliveries != 5 || got.MinIdleMs != 1000 {
		t.Fatalf("XCfgGet returned %+v", got)
	}
}

func TestXCfgSetUnknownGroupErrors(t *testing.T) {
	s := New()
	s.XAdd("k", "*", []string{"f", "v"}, 0)
	if _, err := s.XCfgSet("k", "ghost", GroupConfig{MaxDeliveries: 1}); err == nil {
		t.Fatal("XCfgSet on unknown group must error")
	}
}

// silence the strconv import — sometimes this drops out across edits
var _ = strconv.Atoi
