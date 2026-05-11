package llmstack

import (
	"fmt"
	"testing"
)

func TestCanaryCreateAndPickBaseline(t *testing.T) {
	c := NewCanaryDeploys()
	if err := c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 0}); err != nil {
		t.Fatal(err)
	}
	r, ok := c.Pick("c1", "session-1")
	if !ok {
		t.Fatal("pick returned false")
	}
	if r.Arm != "baseline" {
		t.Fatalf("arm = %s, want baseline (pct=0)", r.Arm)
	}
}

func TestCanaryPickFullCandidateAt100(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 100})
	r, _ := c.Pick("c1", "session-x")
	if r.Arm != "candidate" || r.Prompt != "CAND" {
		t.Fatalf("at 100%% should always be candidate, got %+v", r)
	}
}

func TestCanaryPickStableForSameSeed(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 50})
	first, _ := c.Pick("c1", "user-42")
	for i := 0; i < 20; i++ {
		got, _ := c.Pick("c1", "user-42")
		if got.Arm != first.Arm {
			t.Fatalf("seed routing not stable: first=%s got=%s", first.Arm, got.Arm)
		}
	}
}

func TestCanaryPickRoughlyHonorsTrafficPct(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 30})
	candCount := 0
	for i := 0; i < 1000; i++ {
		r, _ := c.Pick("c1", fmt.Sprintf("seed-%d", i))
		if r.Arm == "candidate" {
			candCount++
		}
	}
	// 30% ± 8 percentage points (loose, sha256 buckets are uniform but
	// still finite-sample)
	if candCount < 220 || candCount > 380 {
		t.Fatalf("candidate count = %d, expected ~300 of 1000", candCount)
	}
}

func TestCanaryRecordAndStatus(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 50, MinSamples: 5, DeltaThreshold: 0.1})
	for i := 0; i < 5; i++ {
		c.Record("c1", "baseline", 0.9)
		c.Record("c1", "candidate", 0.92)
	}
	st, ok := c.Status("c1")
	if !ok {
		t.Fatal("status returned false")
	}
	if st.BaselineN != 5 || st.CandidateN != 5 {
		t.Fatalf("counts = %d/%d", st.BaselineN, st.CandidateN)
	}
	if st.BaselineMean < 0.89 || st.BaselineMean > 0.91 {
		t.Fatalf("baseline mean = %.4f", st.BaselineMean)
	}
	// delta of 0.02 < threshold 0.1 → neutral
	if st.Verdict != "neutral" {
		t.Fatalf("verdict = %s, want neutral", st.Verdict)
	}
}

func TestCanaryAutoRollback(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 50, MinSamples: 5, DeltaThreshold: 0.1})
	for i := 0; i < 5; i++ {
		c.Record("c1", "baseline", 0.95)
		c.Record("c1", "candidate", 0.50) // big regression
	}
	st, _ := c.Status("c1")
	if st.Verdict != "auto_rollback" {
		t.Fatalf("verdict = %s, want auto_rollback (delta=%.4f)", st.Verdict, st.Delta)
	}
	if st.TrafficPercent != 0 {
		t.Fatalf("traffic_percent = %d, want 0 after rollback", st.TrafficPercent)
	}
	// Subsequent picks must return baseline only.
	r, _ := c.Pick("c1", "anything")
	if r.Arm != "baseline" {
		t.Fatalf("after auto-rollback arm = %s, want baseline", r.Arm)
	}
}

func TestCanaryNoVerdictBeforeMinSamples(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 50, MinSamples: 100, DeltaThreshold: 0.1})
	for i := 0; i < 10; i++ {
		c.Record("c1", "baseline", 0.9)
		c.Record("c1", "candidate", 0.1) // huge regression but undersized
	}
	st, _ := c.Status("c1")
	if st.Verdict != "monitoring" {
		t.Fatalf("verdict = %s, want monitoring before min_samples", st.Verdict)
	}
}

func TestCanaryPromote(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 25})
	c.Record("c1", "baseline", 0.5)
	c.Record("c1", "candidate", 0.9)
	if !c.Promote("c1") {
		t.Fatal("promote returned false")
	}
	st, _ := c.Status("c1")
	if st.Baseline != "CAND" {
		t.Fatalf("baseline after promote = %q, want CAND", st.Baseline)
	}
	if st.Candidate != "" {
		t.Fatalf("candidate after promote = %q, want empty", st.Candidate)
	}
	if st.BaselineN != 0 || st.CandidateN != 0 {
		t.Fatalf("tallies not cleared after promote: %d/%d", st.BaselineN, st.CandidateN)
	}
}

func TestCanaryManualRollback(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{TrafficPct: 50})
	if !c.Rollback("c1") {
		t.Fatal("rollback returned false")
	}
	st, _ := c.Status("c1")
	if st.TrafficPercent != 0 {
		t.Fatalf("traffic_percent = %d, want 0 after manual rollback", st.TrafficPercent)
	}
	if st.Verdict != "auto_rollback" {
		t.Fatalf("verdict = %s, want auto_rollback flag set", st.Verdict)
	}
}

func TestCanaryListAndForget(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("a", "x", "y", CanaryOpts{})
	c.Create("b", "x", "y", CanaryOpts{})
	rows := c.List()
	if len(rows) != 2 {
		t.Fatalf("list len = %d", len(rows))
	}
	if !c.Forget("a") {
		t.Fatal("forget returned false")
	}
	rows = c.List()
	if len(rows) != 1 || rows[0].ID != "b" {
		t.Fatalf("list after forget = %+v", rows)
	}
}

func TestCanarySetTrafficBounds(t *testing.T) {
	c := NewCanaryDeploys()
	c.Create("c1", "BASE", "CAND", CanaryOpts{})
	if c.SetTraffic("c1", 101) {
		t.Fatal("expected SetTraffic(101) to fail")
	}
	if c.SetTraffic("c1", -1) {
		t.Fatal("expected SetTraffic(-1) to fail")
	}
	if !c.SetTraffic("c1", 75) {
		t.Fatal("SetTraffic(75) failed")
	}
	st, _ := c.Status("c1")
	if st.TrafficPercent != 75 {
		t.Fatalf("traffic_percent = %d", st.TrafficPercent)
	}
}

func TestCanaryRejectBadCreate(t *testing.T) {
	c := NewCanaryDeploys()
	cases := []struct {
		id, base, cand string
		opts           CanaryOpts
	}{
		{"", "b", "c", CanaryOpts{}},
		{"x", "", "c", CanaryOpts{}},
		{"x", "b", "", CanaryOpts{}},
		{"x", "b", "c", CanaryOpts{TrafficPct: 150}},
		{"x", "b", "c", CanaryOpts{TrafficPct: -5}},
	}
	for i, ca := range cases {
		if err := c.Create(ca.id, ca.base, ca.cand, ca.opts); err == nil {
			t.Errorf("case %d expected error, got nil", i)
		}
	}
}
