package llmstack

import (
	"strings"
	"testing"
)

// ─── REPRO ──────────────────────────────────────────────────────

func TestReproDeterministic(t *testing.T) {
	r := NewReproSeeds()
	r.Bundle("b1", 42, nil)
	v1, _ := r.Use("b1", "decision-A")
	v2, _ := r.Use("b1", "decision-A") // same name → same value
	if v1 != v2 {
		t.Fatalf("not deterministic: %d vs %d", v1, v2)
	}
}

func TestReproDifferentNamesDifferentSeeds(t *testing.T) {
	r := NewReproSeeds()
	r.Bundle("b", 1, nil)
	a, _ := r.Use("b", "a")
	b, _ := r.Use("b", "b")
	if a == b {
		t.Fatal("different names should produce different seeds (overwhelmingly)")
	}
}

func TestReproHashReproducible(t *testing.T) {
	r := NewReproSeeds()
	r.Bundle("b1", 42, nil)
	r.Use("b1", "a")
	r.Use("b1", "b")
	h1, _ := r.Hash("b1")
	r2 := NewReproSeeds()
	r2.Bundle("b1", 42, nil)
	r2.Use("b1", "a")
	r2.Use("b1", "b")
	h2, _ := r2.Hash("b1")
	if h1 != h2 {
		t.Fatalf("hash should be reproducible: %s vs %s", h1, h2)
	}
}

func TestReproTrace(t *testing.T) {
	r := NewReproSeeds()
	r.Bundle("b", 1, nil)
	r.Use("b", "x")
	r.Use("b", "y")
	rows, _ := r.Trace("b")
	if len(rows) != 2 || rows[0].Name != "x" {
		t.Fatalf("trace: %+v", rows)
	}
}

func TestReproRejectsBadInput(t *testing.T) {
	r := NewReproSeeds()
	if err := r.Bundle("", 1, nil); err == nil {
		t.Fatal("empty bundle")
	}
	if _, err := r.Use("ghost", "x"); err == nil {
		t.Fatal("unknown bundle")
	}
}

// ─── REGWATCH ───────────────────────────────────────────────────

func TestRegWatchCheck(t *testing.T) {
	r := NewRegWatch()
	r.Rule("euai-bio", "high", []string{"biometric", "facial recognition"}, "EU AI Act Art. 6 obligations apply", "EU")
	out := r.Check("This system uses facial recognition for entry")
	if out.MaxTier != "high" || len(out.TriggeredRules) != 1 {
		t.Fatalf("check: %+v", out)
	}
}

func TestRegWatchCrossDetectsTierBump(t *testing.T) {
	r := NewRegWatch()
	r.Rule("low-rule", "limited", []string{"chatbot"}, "transparency note", "EU")
	r.Rule("hi-rule", "high", []string{"medical"}, "conformity assessment", "EU")
	x := r.Cross("just a chatbot", "now also handles medical diagnosis")
	if !x.Crossed {
		t.Fatalf("should cross: %+v", x)
	}
	if x.TierBefore != "limited" || x.TierAfter != "high" {
		t.Fatalf("tiers: %+v", x)
	}
}

func TestRegWatchUnknownTier(t *testing.T) {
	r := NewRegWatch()
	if err := r.Rule("x", "ultra", []string{"a"}, "y", ""); err == nil {
		t.Fatal("bad tier should fail")
	}
}

// ─── EGRESS ─────────────────────────────────────────────────────

func TestEgressBlocksClose(t *testing.T) {
	e := NewEgressGuard()
	e.Register("secrets", "the launch codes are alpha bravo charlie", "doc1")
	r := e.Check("the launch codes are alpha bravo charlie delta", "", 0.6)
	if !r.Blocked {
		t.Fatalf("near-identical text should block: %+v", r)
	}
}

func TestEgressDoesNotBlockUnrelated(t *testing.T) {
	e := NewEgressGuard()
	e.Register("secrets", "internal weather forecast for the team", "doc1")
	r := e.Check("public schedule of company events", "", 0.85)
	if r.Blocked {
		t.Fatalf("unrelated text should not block: %+v", r)
	}
}

func TestEgressClusterFilter(t *testing.T) {
	e := NewEgressGuard()
	e.Register("a", "alpha bravo", "")
	e.Register("b", "charlie delta", "")
	r := e.Check("alpha bravo extra", "b", 0.6)
	if r.Blocked {
		t.Fatalf("cluster b should not match alpha bravo: %+v", r)
	}
}

// ─── LICENSE ────────────────────────────────────────────────────

func TestLicenseTagAndCheck(t *testing.T) {
	l := NewLicenseTracker()
	l.Tag("doc-1", "MIT", "", "")
	l.Tag("doc-2", "GPL-3.0", "", "")
	r := l.Check("commercial", []string{"doc-1", "doc-2"})
	if !r.Blocked {
		t.Fatal("GPL in commercial use should block")
	}
	if len(r.IncompatibleSources) != 1 || r.IncompatibleSources[0].Source != "doc-2" {
		t.Fatalf("incompatible: %+v", r.IncompatibleSources)
	}
}

func TestLicenseUnknownTagDefaultDeny(t *testing.T) {
	l := NewLicenseTracker()
	r := l.Check("commercial", []string{"untagged-doc"})
	if !r.Blocked {
		t.Fatal("unknown source should block by default")
	}
}

func TestLicenseCompatOverride(t *testing.T) {
	l := NewLicenseTracker()
	l.CompatSet("custom-license", "commercial", true, "custom approved")
	m := l.Matrix("custom-license", "commercial")
	if !m.Compatible {
		t.Fatalf("custom override: %+v", m)
	}
}

// ─── REPLAY.SHADOW ──────────────────────────────────────────────

func TestReplayShadowAgree(t *testing.T) {
	r := NewReplayShadow()
	r.Enable("p1", "live", "shadow", 0.8)
	for i := 0; i < 20; i++ {
		r.Record("p1", "r-"+itoaBench(i), "same output", "same output")
	}
	d, _ := r.Divergence("p1", 5)
	if d.AgreeRate < 0.99 || d.Alert {
		t.Fatalf("identical outputs should agree: %+v", d)
	}
}

func TestReplayShadowAlertOnDivergence(t *testing.T) {
	r := NewReplayShadow()
	r.Enable("p1", "live", "shadow", 0.9)
	for i := 0; i < 20; i++ {
		r.Record("p1", "r-"+itoaBench(i), "live output", "totally different shadow output here")
	}
	d, _ := r.Divergence("p1", 5)
	if !d.Alert {
		t.Fatalf("divergent shadow should alert: %+v", d)
	}
}

func TestReplayShadowTopDivergent(t *testing.T) {
	r := NewReplayShadow()
	r.Enable("p1", "live", "shadow", 0.9)
	for i := 0; i < 10; i++ {
		r.Record("p1", "r-"+itoaBench(i), "x", "x")
	}
	// One very divergent
	r.Record("p1", "bad", "a\nb\nc", "totally\nelse\nentirely")
	d, _ := r.Divergence("p1", 3)
	if len(d.TopDivergent) == 0 || d.TopDivergent[0].RequestID != "bad" {
		t.Fatalf("top divergent: %+v", d.TopDivergent)
	}
}

func TestReplayShadowDisable(t *testing.T) {
	r := NewReplayShadow()
	r.Enable("p", "l", "s", 0)
	r.Disable("p")
	if err := r.Record("p", "r", "x", "y"); err == nil {
		t.Fatal("disabled pair should refuse record")
	}
}

func TestReplayShadowList(t *testing.T) {
	r := NewReplayShadow()
	r.Enable("a", "l", "s", 0)
	r.Enable("b", "l", "s", 0)
	rows := r.List()
	if len(rows) != 2 {
		t.Fatalf("list: %d", len(rows))
	}
}

// ─── PROOF aux ──────────────────────────────────────────────────

func TestProofParamsCanonical(t *testing.T) {
	p := NewProofRegistry()
	h, _ := p.Commit("c", "m", "p", `{"a":1,"b":[1,2,3]}`)
	if !strings.HasPrefix(h, "") || len(h) != 64 {
		t.Fatalf("hash len: %d", len(h))
	}
}
