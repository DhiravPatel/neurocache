package llmstack

import "testing"

func TestContinualCheckpointBless(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("trust", "v1", "payload-v1", true)
	c.Checkpoint("trust", "v2", "payload-v2", false)
	rows := c.List("")
	if len(rows) != 1 || rows[0].Blessed != "v1" {
		t.Fatalf("blessed: %+v", rows)
	}
}

func TestContinualReblessUnsetsPrevious(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("t", "v1", "p", true)
	c.Checkpoint("t", "v2", "p", true) // re-bless
	rb, err := c.Rollback("t", "")
	if err != nil {
		t.Fatal(err)
	}
	if rb.CheckpointID != "v2" {
		t.Fatalf("blessed should be v2: %s", rb.CheckpointID)
	}
}

func TestContinualAnchorAndRehearseStringMatch(t *testing.T) {
	c := NewContinualLearning()
	c.Anchor("t", "a1", "input", "yes", 0)
	r, _ := c.Rehearse("t", "obs1", "a1", "yes")
	if !r.Pass {
		t.Fatal("exact-match string anchor should pass")
	}
	r, _ = c.Rehearse("t", "obs2", "a1", "no")
	if r.Pass {
		t.Fatal("wrong answer should fail")
	}
}

func TestContinualAnchorNumericTolerance(t *testing.T) {
	c := NewContinualLearning()
	c.Anchor("t", "a1", "x", "0.85", 0.05)
	if r, _ := c.Rehearse("t", "o1", "a1", "0.83"); !r.Pass {
		t.Fatal("within tolerance should pass")
	}
	if r, _ := c.Rehearse("t", "o2", "a1", "0.50"); r.Pass {
		t.Fatal("out of tolerance should fail")
	}
}

func TestContinualDivergenceHealthy(t *testing.T) {
	c := NewContinualLearning()
	for i := 0; i < 10; i++ {
		c.Anchor("t", "a-"+itoaBench(i), "in", "yes", 0)
	}
	for i := 0; i < 10; i++ {
		c.Rehearse("t", "o-"+itoaBench(i), "a-"+itoaBench(i), "yes")
	}
	d, _ := c.Divergence("t")
	if d.Verdict != "HEALTHY" {
		t.Fatalf("verdict: %+v", d)
	}
}

func TestContinualDivergenceForgotten(t *testing.T) {
	c := NewContinualLearning()
	for i := 0; i < 10; i++ {
		c.Anchor("t", "a-"+itoaBench(i), "in", "yes", 0)
	}
	for i := 0; i < 10; i++ {
		c.Rehearse("t", "o-"+itoaBench(i), "a-"+itoaBench(i), "no")
	}
	d, _ := c.Divergence("t")
	if d.Verdict != "FORGOTTEN" {
		t.Fatalf("verdict: %+v", d)
	}
}

func TestContinualDivergenceInsufficient(t *testing.T) {
	c := NewContinualLearning()
	c.Anchor("t", "a", "x", "y", 0)
	c.Rehearse("t", "o", "a", "y")
	d, _ := c.Divergence("t")
	if d.Verdict != "INSUFFICIENT" {
		t.Fatalf("verdict: %s", d.Verdict)
	}
}

func TestContinualRollback(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("t", "v1", "old-state", true)
	c.Checkpoint("t", "v2", "new-state", false)
	rb, _ := c.Rollback("t", "v1")
	if rb.Payload != "old-state" {
		t.Fatalf("rollback: %+v", rb)
	}
}

func TestContinualRollbackDefaultsToBlessed(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("t", "v1", "blessed", true)
	c.Checkpoint("t", "v2", "drift", false)
	rb, _ := c.Rollback("t", "")
	if rb.Payload != "blessed" {
		t.Fatalf("rollback: %+v", rb)
	}
}

func TestContinualRollbackUnknown(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("t", "v1", "x", false)
	if _, err := c.Rollback("t", "ghost"); err == nil {
		t.Fatal("unknown checkpoint should fail")
	}
	if _, err := c.Rollback("t", ""); err == nil {
		t.Fatal("no blessed + no TO should fail")
	}
}

func TestContinualForgetAndList(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("a", "v", "p", false)
	c.Checkpoint("b", "v", "p", false)
	if len(c.List("")) != 2 {
		t.Fatal("list")
	}
	if c.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if c.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestContinualStats(t *testing.T) {
	c := NewContinualLearning()
	c.Checkpoint("t", "v1", "p", true)
	c.Anchor("t", "a", "x", "y", 0)
	c.Rehearse("t", "o", "a", "y")
	c.Rollback("t", "v1")
	s := c.Stats()
	if s.TotalCheckpoints != 1 || s.TotalRehearses != 1 || s.TotalRollbacks != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestContinualRejectsBadInput(t *testing.T) {
	c := NewContinualLearning()
	if err := c.Checkpoint("", "v", "p", false); err == nil {
		t.Fatal("empty learner")
	}
	if err := c.Anchor("t", "", "x", "y", 0); err == nil {
		t.Fatal("empty anchor")
	}
	if err := c.Anchor("t", "a", "", "y", 0); err == nil {
		t.Fatal("empty input")
	}
	if err := c.Anchor("t", "a", "x", "y", -1); err == nil {
		t.Fatal("negative tol")
	}
}
