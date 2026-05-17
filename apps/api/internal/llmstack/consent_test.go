package llmstack

import (
	"testing"
	"time"
)

func TestConsentGrantAndCheck(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "memory:summary", "personalization", 0, nil)
	r := c.Check("alice", "memory:summary", "personalization")
	if !r.Allow {
		t.Fatalf("check: %+v", r)
	}
}

func TestConsentFailsClosed(t *testing.T) {
	c := NewConsentLedger()
	r := c.Check("alice", "memory:summary", "personalization")
	if r.Allow {
		t.Fatal("no grant should deny")
	}
}

func TestConsentExpiry(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "s", "p", 5*time.Millisecond, nil)
	time.Sleep(15 * time.Millisecond)
	r := c.Check("alice", "s", "p")
	if r.Allow {
		t.Fatal("expired should deny")
	}
	if r.Reason != "grant expired" {
		t.Fatalf("reason = %s", r.Reason)
	}
}

func TestConsentRevoke(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "s", "p", 0, nil)
	n, _ := c.Revoke("alice", "s", "p")
	if n != 1 {
		t.Fatal("revoke should drop 1")
	}
	if c.Permits("alice", "s", "p") {
		t.Fatal("revoked should deny")
	}
}

func TestConsentWithdraw(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "s1", "p1", 0, nil)
	c.Grant("alice", "s2", "p2", 0, nil)
	c.Grant("bob", "s1", "p1", 0, nil)
	n, _ := c.Withdraw("alice")
	if n != 2 {
		t.Fatalf("withdraw alice = %d", n)
	}
	if c.Permits("alice", "s1", "p1") {
		t.Fatal("alice should be wiped")
	}
	if !c.Permits("bob", "s1", "p1") {
		t.Fatal("bob should remain")
	}
}

func TestConsentList(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "memory", "p", 0, nil)
	c.Grant("alice", "share", "third-party", 0, nil)
	rows, ok := c.List("alice")
	if !ok || len(rows) != 2 {
		t.Fatalf("list: %+v", rows)
	}
}

func TestConsentExpiringWindow(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "s1", "p", 50*time.Millisecond, nil)
	c.Grant("alice", "s2", "p", 24*time.Hour, nil)
	rows := c.Expiring(100 * time.Millisecond)
	if len(rows) != 1 || rows[0].Scope != "s1" {
		t.Fatalf("expiring: %+v", rows)
	}
}

func TestConsentExpiringSkipsPermanentAndExpired(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("alice", "permanent", "p", 0, nil)
	c.Grant("alice", "soon", "p", 50*time.Millisecond, nil)
	c.Grant("alice", "long-gone", "p", 5*time.Millisecond, nil)
	time.Sleep(10 * time.Millisecond)
	rows := c.Expiring(time.Hour)
	for _, r := range rows {
		if r.Scope == "permanent" || r.Scope == "long-gone" {
			t.Fatalf("unexpected: %+v", r)
		}
	}
}

func TestConsentCaseInsensitive(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("Alice", "Memory", "P", 0, nil)
	if !c.Permits("alice", "memory", "p") {
		t.Fatal("should be case-insensitive")
	}
}

func TestConsentRegrantOverwrites(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("a", "s", "p", 5*time.Millisecond, nil)
	c.Grant("a", "s", "p", time.Hour, nil) // refresh
	time.Sleep(15 * time.Millisecond)
	if !c.Permits("a", "s", "p") {
		t.Fatal("refresh should extend expiry")
	}
}

func TestConsentStats(t *testing.T) {
	c := NewConsentLedger()
	c.Grant("a", "s", "p", 0, nil)
	c.Permits("a", "s", "p")
	c.Permits("a", "s", "other") // denial
	c.Revoke("a", "s", "p")
	s := c.Stats()
	if s.TotalGrants != 1 || s.TotalChecks != 2 || s.TotalDenials != 1 || s.TotalRevokes != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestConsentRejectsBadInput(t *testing.T) {
	c := NewConsentLedger()
	if err := c.Grant("", "s", "p", 0, nil); err == nil {
		t.Fatal("empty user should fail")
	}
	if err := c.Grant("a", "", "p", 0, nil); err == nil {
		t.Fatal("empty scope should fail")
	}
	if err := c.Grant("a", "s", "", 0, nil); err == nil {
		t.Fatal("empty purpose should fail")
	}
	if err := c.Grant("a", "s", "p", -1, nil); err == nil {
		t.Fatal("negative ttl should fail")
	}
}
