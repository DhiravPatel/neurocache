package llmstack

import (
	"testing"
)

func TestIsolateBindAndCheck(t *testing.T) {
	i := NewIsolation()
	i.Bind("chunk-991", "acme", "confidential")
	c := i.Check("chunk-991", "acme")
	if !c.Allowed || c.Tenant != "acme" {
		t.Fatalf("acme should be allowed: %+v", c)
	}
}

func TestIsolateRejectsCrossTenant(t *testing.T) {
	i := NewIsolation()
	i.Bind("chunk-991", "acme", "confidential")
	c := i.Check("chunk-991", "globex")
	if c.Allowed {
		t.Fatalf("globex should be denied: %+v", c)
	}
	if c.Reason != "tenant mismatch" {
		t.Fatalf("reason = %s", c.Reason)
	}
}

func TestIsolateFailsClosedWithoutBinding(t *testing.T) {
	i := NewIsolation()
	c := i.Check("unknown", "acme")
	if c.Allowed {
		t.Fatal("unbound vector should be denied")
	}
}

func TestIsolatePermitsFastPath(t *testing.T) {
	i := NewIsolation()
	i.Bind("v", "t", "")
	if !i.Permits("v", "t") {
		t.Fatal("permits should be true")
	}
	if i.Permits("v", "other") {
		t.Fatal("permits cross-tenant should be false")
	}
}

func TestIsolateUnbind(t *testing.T) {
	i := NewIsolation()
	i.Bind("v", "t", "")
	if i.Unbind("v") != 1 {
		t.Fatal("unbind should drop 1")
	}
	if i.Permits("v", "t") {
		t.Fatal("after unbind should be denied")
	}
}

func TestIsolateAuditUsingExpected(t *testing.T) {
	i := NewIsolation()
	i.Expect("v1")
	i.Expect("v2")
	i.Expect("v3")
	i.Bind("v1", "t", "")
	a := i.Audit(nil)
	if len(a.Unbound) != 2 {
		t.Fatalf("unbound = %v", a.Unbound)
	}
	if a.Bound != 1 || a.Expected != 3 {
		t.Fatalf("audit = %+v", a)
	}
}

func TestIsolateAuditExplicitList(t *testing.T) {
	i := NewIsolation()
	i.Bind("v1", "t", "")
	a := i.Audit([]string{"v1", "v2", "v3"})
	if len(a.Unbound) != 2 {
		t.Fatalf("explicit audit unbound = %v", a.Unbound)
	}
}

func TestIsolateListFor(t *testing.T) {
	i := NewIsolation()
	i.Bind("v1", "acme", "")
	i.Bind("v2", "globex", "")
	i.Bind("v3", "acme", "")
	out := i.ListFor("acme")
	if len(out) != 2 {
		t.Fatalf("listfor acme = %v", out)
	}
}

func TestIsolateClassNormalization(t *testing.T) {
	i := NewIsolation()
	i.Bind("v", "t", "Confidential")
	c := i.Check("v", "t")
	if c.Class != "confidential" {
		t.Fatalf("class not normalised: %s", c.Class)
	}
}

func TestIsolateRebindOverwrites(t *testing.T) {
	i := NewIsolation()
	i.Bind("v", "old", "internal")
	i.Bind("v", "new", "public")
	c := i.Check("v", "new")
	if !c.Allowed || c.Class != "public" {
		t.Fatalf("rebind: %+v", c)
	}
}

func TestIsolateStats(t *testing.T) {
	i := NewIsolation()
	i.Bind("v", "t", "")
	i.Check("v", "t")
	i.Check("v", "x") // denial
	i.Check("ghost", "t") // denial
	s := i.Stats()
	if s.Bound != 1 || s.TotalBinds != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.TotalChecks != 3 || s.TotalDenials != 2 {
		t.Fatalf("counters = %+v", s)
	}
}

func TestIsolateRejectsBadInput(t *testing.T) {
	i := NewIsolation()
	if err := i.Bind("", "t", ""); err == nil {
		t.Fatal("empty vector should fail")
	}
	if err := i.Bind("v", "", ""); err == nil {
		t.Fatal("empty tenant should fail")
	}
	c := i.Check("", "")
	if c.Allowed {
		t.Fatal("empty check should be denied")
	}
}

func BenchmarkIsolatePermits(b *testing.B) {
	i := NewIsolation()
	i.Bind("v", "t", "")
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		i.Permits("v", "t")
	}
}
