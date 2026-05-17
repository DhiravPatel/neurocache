package llmstack

import (
	"errors"
	"sync"
	"testing"
)

// fakeXParticipant is a test participant we can configure to fail at
// any phase.
type fakeXParticipant struct {
	mu             sync.Mutex
	prepareFail    bool
	commitFail     bool
	preparedTokens map[string]bool
	committedTokens map[string]bool
	abortedTokens   map[string]bool
}

func newFakeXParticipant() *fakeXParticipant {
	return &fakeXParticipant{
		preparedTokens: map[string]bool{},
		committedTokens: map[string]bool{},
		abortedTokens: map[string]bool{},
	}
}

func (p *fakeXParticipant) Prepare(op string, args map[string]string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prepareFail {
		return "", errors.New("prepare failed")
	}
	tok := "tok-" + op
	p.preparedTokens[tok] = true
	return tok, nil
}

func (p *fakeXParticipant) Commit(token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.commitFail {
		return errors.New("commit failed")
	}
	p.committedTokens[token] = true
	return nil
}

func (p *fakeXParticipant) Abort(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.abortedTokens[token] = true
}

func TestXTxnHappyPath(t *testing.T) {
	x := NewXTxnCoordinator()
	a := newFakeXParticipant()
	b := newFakeXParticipant()
	x.Register("settle", a)
	x.Register("trust", b)
	x.Begin("xid-1", nil)
	x.Stage("xid-1", "settle", "post-100", nil)
	x.Stage("xid-1", "trust", "record", nil)
	pr, _ := x.Prepare("xid-1")
	if pr.State != "prepared" {
		t.Fatalf("prepare: %+v", pr)
	}
	cr, _ := x.Commit("xid-1")
	if cr.State != "committed" || cr.Committed != 2 {
		t.Fatalf("commit: %+v", cr)
	}
	if !a.committedTokens["tok-post-100"] || !b.committedTokens["tok-record"] {
		t.Fatal("not all participants committed")
	}
}

func TestXTxnPrepareFailureAbortsAll(t *testing.T) {
	x := NewXTxnCoordinator()
	a := newFakeXParticipant()
	b := newFakeXParticipant()
	b.prepareFail = true
	x.Register("a", a)
	x.Register("b", b)
	x.Begin("xid", nil)
	x.Stage("xid", "a", "op", nil)
	x.Stage("xid", "b", "op", nil)
	pr, _ := x.Prepare("xid")
	if pr.State != "aborted" {
		t.Fatalf("prepare should abort: %+v", pr)
	}
	// a was prepared then aborted; b never prepared.
	if !a.abortedTokens["tok-op"] {
		t.Fatal("a should have been aborted")
	}
	if len(a.committedTokens) > 0 {
		t.Fatal("a should NOT have committed")
	}
}

func TestXTxnCommitPartialOnFailure(t *testing.T) {
	x := NewXTxnCoordinator()
	a := newFakeXParticipant()
	b := newFakeXParticipant()
	b.commitFail = true
	x.Register("a", a)
	x.Register("b", b)
	x.Begin("xid", nil)
	x.Stage("xid", "a", "op", nil)
	x.Stage("xid", "b", "op", nil)
	x.Prepare("xid")
	cr, _ := x.Commit("xid")
	if cr.State != "commit_partial" {
		t.Fatalf("expected commit_partial: %+v", cr)
	}
	if cr.Committed != 1 {
		t.Fatalf("committed = %d, want 1", cr.Committed)
	}
	if cr.Reason == "" {
		t.Fatal("reason should be populated")
	}
}

func TestXTxnExplicitAbort(t *testing.T) {
	x := NewXTxnCoordinator()
	a := newFakeXParticipant()
	x.Register("a", a)
	x.Begin("xid", nil)
	x.Stage("xid", "a", "op", nil)
	x.Prepare("xid")
	if err := x.Abort("xid", "user changed mind"); err != nil {
		t.Fatal(err)
	}
	if !a.abortedTokens["tok-op"] {
		t.Fatal("a should be aborted")
	}
	s, _ := x.Status("xid")
	if s.State != "aborted" || s.Reason != "user changed mind" {
		t.Fatalf("status: %+v", s)
	}
}

func TestXTxnStageRequiresRegisteredParticipant(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Begin("xid", nil)
	if err := x.Stage("xid", "ghost", "op", nil); err == nil {
		t.Fatal("stage with unknown participant should fail")
	}
}

func TestXTxnRejectsAfterTerminal(t *testing.T) {
	x := NewXTxnCoordinator()
	a := newFakeXParticipant()
	x.Register("a", a)
	x.Begin("xid", nil)
	x.Stage("xid", "a", "op", nil)
	x.Prepare("xid")
	x.Commit("xid")
	if err := x.Stage("xid", "a", "op2", nil); err == nil {
		t.Fatal("stage on committed xtxn should fail")
	}
	if _, err := x.Prepare("xid"); err == nil {
		t.Fatal("re-prepare should fail")
	}
	if _, err := x.Commit("xid"); err == nil {
		t.Fatal("re-commit should fail")
	}
	if err := x.Abort("xid", ""); err == nil {
		t.Fatal("abort committed should fail")
	}
}

func TestXTxnCommitWithoutPrepareFails(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Register("a", newFakeXParticipant())
	x.Begin("xid", nil)
	x.Stage("xid", "a", "op", nil)
	if _, err := x.Commit("xid"); err == nil {
		t.Fatal("commit without prepare should fail")
	}
}

func TestXTxnDuplicateBegin(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Begin("xid", nil)
	if err := x.Begin("xid", nil); err == nil {
		t.Fatal("duplicate begin should fail")
	}
}

func TestXTxnStatusFlow(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Register("a", newFakeXParticipant())
	x.Begin("xid", nil)
	s, _ := x.Status("xid")
	if s.State != "open" {
		t.Fatalf("initial: %s", s.State)
	}
	x.Stage("xid", "a", "op", nil)
	s, _ = x.Status("xid")
	if s.StagedCount != 1 {
		t.Fatalf("staged: %+v", s)
	}
	x.Prepare("xid")
	s, _ = x.Status("xid")
	if s.State != "prepared" || s.PreparedCount != 1 {
		t.Fatalf("prepared: %+v", s)
	}
}

func TestXTxnListByState(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Register("a", newFakeXParticipant())
	x.Begin("a", nil)
	x.Begin("b", nil)
	x.Stage("a", "a", "op", nil)
	x.Prepare("a")
	x.Commit("a")
	if rows := x.List("committed"); len(rows) != 1 || rows[0].XID != "a" {
		t.Fatalf("filter: %+v", rows)
	}
	if rows := x.List("open"); len(rows) != 1 || rows[0].XID != "b" {
		t.Fatalf("open filter: %+v", rows)
	}
}

func TestXTxnStats(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Register("a", newFakeXParticipant())
	x.Begin("a", nil)
	x.Stage("a", "a", "op", nil)
	x.Prepare("a")
	x.Commit("a")
	x.Begin("b", nil)
	x.Abort("b", "")
	s := x.Stats()
	if s.TotalBegins != 2 || s.TotalCommits != 1 || s.TotalAborts != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestXTxnForget(t *testing.T) {
	x := NewXTxnCoordinator()
	x.Begin("a", nil)
	x.Begin("b", nil)
	if x.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if x.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestXTxnRejectsBadInput(t *testing.T) {
	x := NewXTxnCoordinator()
	if err := x.Begin("", nil); err == nil {
		t.Fatal("empty xid")
	}
	x.Begin("xid", nil)
	if err := x.Stage("xid", "", "op", nil); err == nil {
		t.Fatal("empty participant")
	}
	if err := x.Stage("xid", "a", "", nil); err == nil {
		t.Fatal("empty op")
	}
}

func TestXTxnConcurrentDifferentXIDs(t *testing.T) {
	// Multiple concurrent xtxns must not interfere.
	x := NewXTxnCoordinator()
	x.Register("a", newFakeXParticipant())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "x-" + itoaInline(i)
			x.Begin(id, nil)
			x.Stage(id, "a", "op", nil)
			x.Prepare(id)
			x.Commit(id)
		}(i)
	}
	wg.Wait()
	s := x.Stats()
	if s.TotalBegins != 20 || s.TotalCommits != 20 {
		t.Fatalf("concurrent: %+v", s)
	}
}
