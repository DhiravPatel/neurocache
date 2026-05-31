package llmstack

import (
	"testing"
	"time"
)

func TestChaosInjectAndCheck(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "lookup_fail", 1.0, 0, nil, "test")
	r := c.Check("trust", "lookup_fail", nil)
	if !r.Injected || r.FaultID != "f1" {
		t.Fatalf("check: %+v", r)
	}
}

func TestChaosTargetIsolation(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "lookup_fail", 1.0, 0, nil, "")
	if r := c.Check("market", "lookup_fail", nil); r.Injected {
		t.Fatalf("wrong target should not match: %+v", r)
	}
}

func TestChaosKindIsolation(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "lookup_fail", 1.0, 0, nil, "")
	if r := c.Check("trust", "other_kind", nil); r.Injected {
		t.Fatal("wrong kind should not match")
	}
}

func TestChaosScopeMatching(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "lookup_fail", 1.0, 0, map[string]string{"tenant": "acme"}, "")
	if r := c.Check("trust", "lookup_fail", map[string]string{"tenant": "acme"}); !r.Injected {
		t.Fatal("matching scope should hit")
	}
	if r := c.Check("trust", "lookup_fail", map[string]string{"tenant": "globex"}); r.Injected {
		t.Fatal("non-matching scope should miss")
	}
}

func TestChaosScopeEmptyFaultMatchesAny(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "lookup_fail", 1.0, 0, nil, "")
	if r := c.Check("trust", "lookup_fail", map[string]string{"tenant": "anything"}); !r.Injected {
		t.Fatal("empty fault scope should match any caller scope")
	}
}

func TestChaosRateLimits(t *testing.T) {
	c := NewChaosInjector()
	c.ForSeed(42)
	c.Inject("f1", "trust", "fail", 0.5, 0, nil, "")
	hits := 0
	for i := 0; i < 1000; i++ {
		if c.Check("trust", "fail", nil).Injected {
			hits++
		}
	}
	// 0.5 rate over 1000 draws should land in [400, 600]
	if hits < 400 || hits > 600 {
		t.Fatalf("rate=0.5 produced %d hits", hits)
	}
}

func TestChaosExpiry(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "fail", 1.0, 5*time.Millisecond, nil, "")
	time.Sleep(15 * time.Millisecond)
	if r := c.Check("trust", "fail", nil); r.Injected {
		t.Fatal("expired fault should not hit")
	}
}

func TestChaosRevoke(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "trust", "fail", 1.0, 0, nil, "")
	if c.Revoke("f1") != 1 {
		t.Fatal("revoke")
	}
	if r := c.Check("trust", "fail", nil); r.Injected {
		t.Fatal("revoked fault should not hit")
	}
}

func TestChaosDuplicateInjectRejected(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "t", "k", 1.0, 0, nil, "")
	if err := c.Inject("f1", "t", "k", 1.0, 0, nil, ""); err == nil {
		t.Fatal("duplicate inject should fail")
	}
}

func TestChaosActiveAndHistory(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "t", "k", 1.0, 0, nil, "")
	c.Inject("f2", "t", "k", 1.0, 0, nil, "")
	if len(c.Active("", "")) != 2 {
		t.Fatal("active should be 2")
	}
	c.Revoke("f1")
	if len(c.Active("", "")) != 1 || len(c.History(10)) != 1 {
		t.Fatalf("active=%d history=%d", len(c.Active("", "")), len(c.History(10)))
	}
}

func TestChaosStats(t *testing.T) {
	c := NewChaosInjector()
	c.Inject("f1", "t", "k", 1.0, 0, nil, "")
	c.Check("t", "k", nil)
	c.Check("t", "k", nil)
	c.Revoke("f1")
	s := c.Stats()
	if s.TotalInjects != 1 || s.TotalChecks != 2 || s.TotalHits != 2 || s.TotalRevokes != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestChaosRejectsBadInput(t *testing.T) {
	c := NewChaosInjector()
	if err := c.Inject("", "t", "k", 1, 0, nil, ""); err == nil {
		t.Fatal("empty id")
	}
	if err := c.Inject("f", "", "k", 1, 0, nil, ""); err == nil {
		t.Fatal("empty target")
	}
	if err := c.Inject("f", "t", "", 1, 0, nil, ""); err == nil {
		t.Fatal("empty kind")
	}
	if err := c.Inject("f", "t", "k", -0.1, 0, nil, ""); err == nil {
		t.Fatal("rate < 0")
	}
	if err := c.Inject("f", "t", "k", 1.5, 0, nil, ""); err == nil {
		t.Fatal("rate > 1")
	}
}
