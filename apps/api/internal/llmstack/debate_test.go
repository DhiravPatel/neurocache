package llmstack

import "testing"

func TestDebateLifecycle(t *testing.T) {
	d := NewDebates()
	if err := d.Start("d1", "alice", "ship v5"); err != nil {
		t.Fatal(err)
	}
	if err := d.Critique("d1", "bob", "v5 has a bug"); err != nil {
		t.Fatal(err)
	}
	if err := d.Vote("d1", "bob", false, "still buggy"); err != nil {
		t.Fatal(err)
	}
	if err := d.Vote("d1", "carol", true, "lgtm"); err != nil {
		t.Fatal(err)
	}
	if err := d.Vote("d1", "alice", true, ""); err != nil {
		t.Fatal(err)
	}
	r, err := d.Resolve("d1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Approved {
		t.Fatalf("should approve (2 yes vs 1 no): %+v", r)
	}
	if len(r.Dissent) != 1 || r.Dissent[0] != "bob" {
		t.Fatalf("dissent: %+v", r.Dissent)
	}
}

func TestDebateStartRejectsDuplicate(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "a", "x")
	if err := d.Start("d1", "a", "x"); err == nil {
		t.Fatal("duplicate start should fail")
	}
}

func TestDebateReviseClearsVotes(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "alice", "v1")
	d.Vote("d1", "bob", true, "")
	d.Vote("d1", "carol", true, "")
	if err := d.Revise("d1", "alice", "v2"); err != nil {
		t.Fatal(err)
	}
	r, _ := d.Resolve("d1", 0)
	if r.Votes != 0 {
		t.Fatalf("votes should reset on revise: %d", r.Votes)
	}
}

func TestDebateReviseOnlyByProposer(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "alice", "v1")
	if err := d.Revise("d1", "bob", "v2"); err == nil {
		t.Fatal("non-proposer revise should fail")
	}
}

func TestDebateVoteReplaces(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "alice", "x")
	d.Vote("d1", "bob", true, "")
	d.Vote("d1", "bob", false, "changed my mind")
	r, _ := d.Resolve("d1", 0)
	if r.ApproveN != 0 || r.RejectN != 1 {
		t.Fatalf("vote not replaced: %+v", r)
	}
}

func TestDebateRejectsAfterResolve(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "a", "x")
	d.Vote("d1", "b", true, "")
	d.Resolve("d1", 0)
	if err := d.Critique("d1", "c", "too late"); err == nil {
		t.Fatal("post-resolve critique should fail")
	}
	if err := d.Vote("d1", "c", true, ""); err == nil {
		t.Fatal("post-resolve vote should fail")
	}
	if _, err := d.Resolve("d1", 0); err == nil {
		t.Fatal("re-resolve should fail")
	}
}

func TestDebateExplicitQuorum(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "a", "x")
	d.Vote("d1", "b", true, "")
	d.Vote("d1", "c", true, "")
	// 2 yes, 0 no, quorum=5 → not approved
	r, _ := d.Resolve("d1", 5)
	if r.Approved {
		t.Fatalf("should not reach quorum: %+v", r)
	}
}

func TestDebateGetReturnsTranscript(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "a", "x")
	d.Critique("d1", "b", "..")
	d.Vote("d1", "c", true, "yes")
	v, _ := d.Get("d1")
	if len(v.Critiques) != 1 || len(v.Votes) != 1 {
		t.Fatalf("transcript: %+v", v)
	}
}

func TestDebateListByState(t *testing.T) {
	d := NewDebates()
	d.Start("d1", "a", "x")
	d.Start("d2", "a", "x")
	d.Vote("d1", "x", true, "")
	rows := d.List("voting")
	if len(rows) != 1 {
		t.Fatalf("list voting = %d", len(rows))
	}
}

func TestDebateStats(t *testing.T) {
	d := NewDebates()
	d.Start("d", "a", "x")
	d.Critique("d", "b", "..")
	d.Vote("d", "b", true, "")
	d.Resolve("d", 0)
	s := d.Stats()
	if s.TotalStarts != 1 || s.TotalCritiques != 1 || s.TotalVotes != 1 || s.TotalResolves != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestDebateRejectsBadInput(t *testing.T) {
	d := NewDebates()
	if err := d.Start("", "a", "x"); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := d.Start("d", "", "x"); err == nil {
		t.Fatal("empty proposer should fail")
	}
	if err := d.Start("d", "a", ""); err == nil {
		t.Fatal("empty proposal should fail")
	}
	d.Start("d", "a", "x")
	if err := d.Critique("d", "", "x"); err == nil {
		t.Fatal("empty critique agent should fail")
	}
	if err := d.Vote("d", "", true, ""); err == nil {
		t.Fatal("empty vote agent should fail")
	}
}
