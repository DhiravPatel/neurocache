package llmstack

import (
	"testing"
)

func TestChainStateDefineAndStart(t *testing.T) {
	c := NewChainStateMgr()
	if err := c.Define("ingest", []string{"fetch", "parse", "store"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start("run-1", "ingest"); err != nil {
		t.Fatal(err)
	}
}

func TestChainStateDefineRejectsBad(t *testing.T) {
	c := NewChainStateMgr()
	if err := c.Define("", []string{"a"}); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := c.Define("c", nil); err == nil {
		t.Fatal("empty steps should fail")
	}
	if err := c.Define("c", []string{"a", "a"}); err == nil {
		t.Fatal("duplicate step should fail")
	}
}

func TestChainStateStartUnknownChain(t *testing.T) {
	c := NewChainStateMgr()
	if err := c.Start("r1", "nope"); err == nil {
		t.Fatal("unknown chain should fail")
	}
}

func TestChainStateDoneAdvances(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"fetch", "parse", "store"})
	c.Start("r1", "ingest")
	r, err := c.Done("r1", "fetch", "fetched-data")
	if err != nil {
		t.Fatal(err)
	}
	if r.NextStep != "parse" || r.StepIdx != 1 {
		t.Fatalf("after fetch = %+v", r)
	}
	r, _ = c.Done("r1", "parse", "parsed-data")
	if r.NextStep != "store" || r.StepIdx != 2 {
		t.Fatalf("after parse = %+v", r)
	}
	r, _ = c.Done("r1", "store", "stored")
	if r.Status != "complete" {
		t.Fatalf("after store should be complete: %+v", r)
	}
}

func TestChainStateOutOfOrderDoneFails(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"fetch", "parse"})
	c.Start("r1", "ingest")
	if _, err := c.Done("r1", "parse", "x"); err == nil {
		t.Fatal("out-of-order done should fail")
	}
}

func TestChainStateResume(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"fetch", "parse", "store"})
	c.Start("r1", "ingest")
	c.Done("r1", "fetch", "<fetched>")
	// Worker crashed here — recovery worker calls RESUME
	r, ok := c.Resume("r1")
	if !ok {
		t.Fatal("resume should succeed")
	}
	if r.NextStep != "parse" {
		t.Fatalf("next step = %s, want parse", r.NextStep)
	}
	if r.Artifacts["fetch"] != "<fetched>" {
		t.Fatalf("artifact lost: %v", r.Artifacts)
	}
}

func TestChainStateResumeAfterComplete(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"fetch"})
	c.Start("r1", "ingest")
	c.Done("r1", "fetch", "x")
	r, _ := c.Resume("r1")
	if r.Status != "complete" {
		t.Fatalf("status = %s, want complete", r.Status)
	}
	if r.NextStep != "" {
		t.Fatalf("next_step should be empty after complete: %s", r.NextStep)
	}
}

func TestChainStateFail(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"fetch", "parse"})
	c.Start("r1", "ingest")
	if err := c.Fail("r1", "fetch", "upstream timeout"); err != nil {
		t.Fatal(err)
	}
	r, _ := c.Resume("r1")
	if r.Status != "failed" || r.Reason != "upstream timeout" {
		t.Fatalf("status = %+v", r)
	}
}

func TestChainStateFailIdempotent(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"a"})
	c.Start("r1", "ingest")
	c.Done("r1", "a", "x") // run is now complete
	if err := c.Fail("r1", "a", "late"); err != nil {
		t.Fatal(err)
	}
	r, _ := c.Resume("r1")
	if r.Status != "complete" {
		t.Fatalf("fail after complete should be no-op, got %s", r.Status)
	}
}

func TestChainStateArtifact(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"a", "b"})
	c.Start("r1", "ingest")
	c.Done("r1", "a", "value-a")
	v, ok := c.Artifact("r1", "a")
	if !ok || v != "value-a" {
		t.Fatalf("artifact = %q ok=%v", v, ok)
	}
	if _, ok := c.Artifact("r1", "b"); ok {
		t.Fatal("artifact b should not exist yet")
	}
}

func TestChainStateRuns(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"a"})
	c.Start("r1", "ingest")
	c.Start("r2", "ingest")
	c.Done("r1", "a", "x") // r1 complete; r2 running
	rows := c.Runs("ingest", "")
	if len(rows) != 2 {
		t.Fatalf("runs = %d", len(rows))
	}
	rows = c.Runs("ingest", "complete")
	if len(rows) != 1 || rows[0].RunID != "r1" {
		t.Fatalf("complete runs = %+v", rows)
	}
	rows = c.Runs("ingest", "running")
	if len(rows) != 1 || rows[0].RunID != "r2" {
		t.Fatalf("running runs = %+v", rows)
	}
}

func TestChainStateForgetChain(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"a"})
	c.Start("r1", "ingest")
	c.Start("r2", "ingest")
	ok, dropped := c.ForgetChain("ingest")
	if !ok || dropped != 2 {
		t.Fatalf("forget = ok:%v dropped:%d", ok, dropped)
	}
}

func TestChainStateStatsAdvance(t *testing.T) {
	c := NewChainStateMgr()
	c.Define("ingest", []string{"a", "b"})
	c.Start("r1", "ingest")
	c.Done("r1", "a", "x")
	c.Done("r1", "b", "y")
	c.Start("r2", "ingest")
	c.Fail("r2", "a", "boom")
	s := c.Stats()
	if s.TotalRuns != 2 || s.TotalCompletes != 1 || s.TotalFails != 1 || s.TotalSteps != 2 {
		t.Fatalf("stats = %+v", s)
	}
}
