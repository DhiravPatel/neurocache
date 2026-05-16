package llmstack

import (
	"testing"
)

func TestCascadeConfigAndPickFirstTier(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("models", []string{"gpt-3.5", "gpt-4", "gpt-4-turbo"})
	r, ok := c.Pick("models", "what is bitcoin?")
	if !ok {
		t.Fatal("pick returned false")
	}
	if r.TierIdx != 0 || r.Tier != "gpt-3.5" {
		t.Fatalf("first pick = %+v, want gpt-3.5", r)
	}
	if r.Learned {
		t.Fatal("first pick should not be learned")
	}
}

func TestCascadeLearnsFromSuccess(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("models", []string{"gpt-3.5", "gpt-4"})
	// Record that gpt-4 succeeded for this input
	c.Record("models", "complex query", 1, true)
	// Next pick should learn gpt-4
	r, _ := c.Pick("models", "complex query")
	if !r.Learned || r.TierIdx != 1 {
		t.Fatalf("expected learned gpt-4, got %+v", r)
	}
}

func TestCascadeForgetsOnLastTierFail(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("models", []string{"a", "b"})
	c.Record("models", "x", 1, true)            // learn b
	c.Record("models", "x", 1, false)           // last-tier fail
	r, _ := c.Pick("models", "x")
	if r.Learned {
		t.Fatalf("should have forgotten on last-tier fail: %+v", r)
	}
}

func TestCascadeMiddleTierFailNoCacheUpdate(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("models", []string{"a", "b", "c"})
	c.Record("models", "x", 2, true)            // learn c
	c.Record("models", "x", 1, false)           // middle-tier fail (not last)
	r, _ := c.Pick("models", "x")
	if r.TierIdx != 2 {
		t.Fatalf("learning should be preserved: %+v", r)
	}
}

func TestCascadeStatusReturnsMinusOneForUnlearned(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("models", []string{"a", "b"})
	r, _ := c.Status("models", "never-seen")
	if r.TierIdx != -1 || r.Learned {
		t.Fatalf("unlearned status = %+v", r)
	}
}

func TestCascadeRejectsBadConfig(t *testing.T) {
	c := NewCascadeRouter()
	if err := c.Config("", []string{"a", "b"}); err == nil {
		t.Fatal("empty cascade_id should fail")
	}
	if err := c.Config("x", []string{"a"}); err == nil {
		t.Fatal("single-tier cascade should fail")
	}
	if err := c.Config("x", []string{"a", ""}); err == nil {
		t.Fatal("empty tier should fail")
	}
}

func TestCascadeRecordOutOfRange(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("models", []string{"a", "b"})
	if err := c.Record("models", "x", 5, true); err == nil {
		t.Fatal("out-of-range tier should fail")
	}
}

func TestCascadeRecordUnknownCascade(t *testing.T) {
	c := NewCascadeRouter()
	if err := c.Record("nope", "x", 0, true); err == nil {
		t.Fatal("unknown cascade should fail")
	}
}

func TestCascadeForget(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("m", []string{"a", "b"})
	c.Record("m", "x", 1, true)
	if !c.Forget("m", "x") {
		t.Fatal("forget should return true")
	}
	if c.Forget("m", "x") {
		t.Fatal("forget on missing should return false")
	}
}

func TestCascadePurge(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("m1", []string{"a", "b"})
	c.Config("m2", []string{"a", "b"})
	if n := c.Purge(""); n != 2 {
		t.Fatalf("purge all = %d, want 2", n)
	}
}

func TestCascadeAllPerTierStats(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("m", []string{"a", "b"})
	c.Record("m", "x1", 0, true)
	c.Record("m", "x2", 0, true)
	c.Record("m", "x3", 1, true)
	c.Record("m", "x4", 0, false)
	rows := c.All()
	if len(rows) != 1 || len(rows[0].Tiers) != 2 {
		t.Fatalf("all = %+v", rows)
	}
	if rows[0].Tiers[0].Wins != 2 || rows[0].Tiers[0].Fails != 1 {
		t.Fatalf("tier 0 = %+v", rows[0].Tiers[0])
	}
	if rows[0].Tiers[1].Wins != 1 {
		t.Fatalf("tier 1 = %+v", rows[0].Tiers[1])
	}
}

func TestCascadeStatsAdvance(t *testing.T) {
	c := NewCascadeRouter()
	c.Config("m", []string{"a", "b"})
	c.Pick("m", "x")
	c.Record("m", "x", 1, true)
	c.Pick("m", "x") // now learned
	s := c.Stats()
	if s.Cascades != 1 || s.TotalPicks != 2 || s.TotalLearned != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
