package llmstack

import (
	"testing"
)

func TestFactSetAndGet(t *testing.T) {
	f := NewFactRegistry()
	f.Set("refund-policy", "30-day window, no restocking fee")
	r, ok := f.Get("refund-policy")
	if !ok {
		t.Fatal("get returned false")
	}
	if r.Version != 1 {
		t.Fatalf("version = %d, want 1", r.Version)
	}
	if r.Content != "30-day window, no restocking fee" {
		t.Fatalf("content = %q", r.Content)
	}
}

func TestFactBumpIncrementsVersion(t *testing.T) {
	f := NewFactRegistry()
	f.Set("refund-policy", "v1 content")
	v2, _ := f.Bump("refund-policy", "v2 content")
	if v2 != 2 {
		t.Fatalf("bump returned %d, want 2", v2)
	}
	v3, _ := f.Bump("refund-policy", "v3 content")
	if v3 != 3 {
		t.Fatalf("bump returned %d, want 3", v3)
	}
	r, _ := f.Get("refund-policy")
	if r.Content != "v3 content" {
		t.Fatalf("content = %q", r.Content)
	}
}

func TestFactBumpOnNewCreatesAtV1(t *testing.T) {
	f := NewFactRegistry()
	v, _ := f.Bump("new-fact", "content")
	if v != 1 {
		t.Fatalf("bump on new fact = %d, want 1", v)
	}
}

func TestFactStampAndStaleDetection(t *testing.T) {
	f := NewFactRegistry()
	f.Set("refund-policy", "30-day window")
	// App caches an LLM answer and stamps it with the fact
	if err := f.Stamp("answer:abc123", []string{"refund-policy"}); err != nil {
		t.Fatal(err)
	}
	// Initially fresh
	if f.Stale("answer:abc123") {
		t.Fatal("freshly-stamped key should not be stale")
	}
	// Operator bumps the policy
	f.Bump("refund-policy", "14-day window")
	// Now stale
	if !f.Stale("answer:abc123") {
		t.Fatal("after BUMP, stamped key should be stale")
	}
}

func TestFactStampMultiple(t *testing.T) {
	f := NewFactRegistry()
	f.Set("policy", "v1")
	f.Set("pricing", "v1")
	f.Stamp("answer:multi", []string{"policy", "pricing"})
	if f.Stale("answer:multi") {
		t.Fatal("freshly stamped should not be stale")
	}
	f.Bump("pricing", "v2")
	if !f.Stale("answer:multi") {
		t.Fatal("any one bumped fact should stale the key")
	}
}

func TestFactUnstampedKeyNotStale(t *testing.T) {
	f := NewFactRegistry()
	if f.Stale("random-key") {
		t.Fatal("unstamped key should not be stale")
	}
}

func TestFactForgetCausesStale(t *testing.T) {
	f := NewFactRegistry()
	f.Set("policy", "v1")
	f.Stamp("k", []string{"policy"})
	f.Forget("policy")
	if !f.Stale("k") {
		t.Fatal("stamped key with forgotten fact should be stale")
	}
}

func TestFactStaleKeysLists(t *testing.T) {
	f := NewFactRegistry()
	f.Set("policy", "v1")
	f.Set("pricing", "v1")
	f.Stamp("answer1", []string{"policy"})
	f.Stamp("answer2", []string{"pricing"})
	f.Stamp("answer3", []string{"policy"})
	f.Bump("policy", "v2")
	stale := f.StaleKeys(0)
	if len(stale) != 2 {
		t.Fatalf("stale keys = %d, want 2", len(stale))
	}
}

func TestFactStaleKeysLimit(t *testing.T) {
	f := NewFactRegistry()
	f.Set("p", "v1")
	for i := 0; i < 10; i++ {
		f.Stamp("k"+string(rune('a'+i)), []string{"p"})
	}
	f.Bump("p", "v2")
	out := f.StaleKeys(3)
	if len(out) != 3 {
		t.Fatalf("limit not honored: %d", len(out))
	}
}

func TestFactUnstamp(t *testing.T) {
	f := NewFactRegistry()
	f.Set("p", "v1")
	f.Stamp("k", []string{"p"})
	if !f.Unstamp("k") {
		t.Fatal("unstamp should return true")
	}
	if f.Unstamp("k") {
		t.Fatal("unstamp on missing should return false")
	}
}

func TestFactRejectsEmpty(t *testing.T) {
	f := NewFactRegistry()
	if err := f.Set("", "x"); err == nil {
		t.Fatal("empty fact_id should fail")
	}
	if err := f.Stamp("", []string{"x"}); err == nil {
		t.Fatal("empty cache_key should fail")
	}
	if err := f.Stamp("k", nil); err == nil {
		t.Fatal("no fact_ids should fail")
	}
}

func TestFactStampUnknownFact(t *testing.T) {
	f := NewFactRegistry()
	if err := f.Stamp("k", []string{"never-existed"}); err == nil {
		t.Fatal("stamping unknown fact should fail")
	}
}

func TestFactStatsAdvance(t *testing.T) {
	f := NewFactRegistry()
	f.Set("p", "v1")
	f.Bump("p", "v2")
	f.Stamp("k", []string{"p"})
	f.Stale("k")
	s := f.Stats()
	if s.TotalSets != 1 || s.TotalBumps != 1 || s.TotalStamps != 1 || s.TotalChecks != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
