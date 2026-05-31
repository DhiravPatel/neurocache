package llmstack

import (
	"testing"
	"time"
)

func TestQuorumProposeAndCommit(t *testing.T) {
	q := NewQuorumGates()
	if err := q.Propose("g", "wire-10k", 2, []string{"a", "b", "c"}, 0); err != nil {
		t.Fatal(err)
	}
	q.Approve("g", "a", "")
	q.Approve("g", "b", "")
	r, err := q.Commit("g")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "committed" {
		t.Fatalf("state = %s", r.State)
	}
}

func TestQuorumUnmet(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 2, []string{"a", "b"}, 0)
	q.Approve("g", "a", "")
	if _, err := q.Commit("g"); err == nil {
		t.Fatal("commit should fail with insufficient approvals")
	}
}

func TestQuorumRejectFails(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 1, []string{"a", "b"}, 0)
	q.Reject("g", "b", "no")
	if _, err := q.Commit("g"); err == nil {
		t.Fatal("rejected gate should not commit")
	}
}

func TestQuorumApprovalOverridesPriorReject(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 1, []string{"a"}, 0)
	q.Reject("g", "a", "")
	// Once rejected, gate is closed; approval shouldn't reopen
	if err := q.Approve("g", "a", "changed mind"); err == nil {
		t.Fatal("post-reject approval should fail")
	}
}

func TestQuorumNonVoterRejected(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 1, []string{"a"}, 0)
	if err := q.Approve("g", "intruder", ""); err == nil {
		t.Fatal("non-voter approval should fail")
	}
}

func TestQuorumDeadlineExpires(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 1, []string{"a"}, 5*time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	st, _ := q.Status("g")
	if st.State != "expired" {
		t.Fatalf("state = %s", st.State)
	}
	if err := q.Approve("g", "a", ""); err == nil {
		t.Fatal("expired gate should refuse approvals")
	}
}

func TestQuorumDuplicateProposeRejected(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 1, []string{"a"}, 0)
	if err := q.Propose("g", "y", 1, []string{"a"}, 0); err == nil {
		t.Fatal("duplicate propose should fail")
	}
}

func TestQuorumStatusListsVotes(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 2, []string{"a", "b", "c"}, 0)
	q.Approve("g", "a", "yes")
	q.Approve("g", "b", "also yes")
	st, _ := q.Status("g")
	if len(st.Approvals) != 2 {
		t.Fatalf("approvals: %+v", st.Approvals)
	}
}

func TestQuorumListByState(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("a", "x", 1, []string{"x"}, 0)
	q.Propose("b", "x", 1, []string{"x"}, 0)
	q.Approve("a", "x", "")
	q.Commit("a")
	rows := q.List("committed")
	if len(rows) != 1 || rows[0].GateID != "a" {
		t.Fatalf("list: %+v", rows)
	}
}

func TestQuorumStats(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("g", "x", 1, []string{"a"}, 0)
	q.Approve("g", "a", "")
	q.Commit("g")
	s := q.Stats()
	if s.TotalProposals != 1 || s.TotalApprovals != 1 || s.TotalCommits != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestQuorumForget(t *testing.T) {
	q := NewQuorumGates()
	q.Propose("a", "x", 1, []string{"a"}, 0)
	q.Propose("b", "x", 1, []string{"a"}, 0)
	if q.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if q.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestQuorumRejectsBadInput(t *testing.T) {
	q := NewQuorumGates()
	if err := q.Propose("", "x", 1, []string{"a"}, 0); err == nil {
		t.Fatal("empty id")
	}
	if err := q.Propose("g", "", 1, []string{"a"}, 0); err == nil {
		t.Fatal("empty payload")
	}
	if err := q.Propose("g", "x", 0, []string{"a"}, 0); err == nil {
		t.Fatal("zero quorum")
	}
	if err := q.Propose("g", "x", 5, []string{"a"}, 0); err == nil {
		t.Fatal("quorum > voters")
	}
	if err := q.Propose("g", "x", 1, nil, 0); err == nil {
		t.Fatal("no voters")
	}
	if err := q.Propose("g", "x", 1, []string{""}, 0); err == nil {
		t.Fatal("empty voter")
	}
}
