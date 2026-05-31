package llmstack

import "testing"

func TestAIWALAppendMonotonic(t *testing.T) {
	a := NewAIWALRegistry()
	r1, _ := a.Append("trust", "entry-1")
	r2, _ := a.Append("trust", "entry-2")
	if r1.Seq != 1 || r2.Seq != 2 {
		t.Fatalf("seqs: %d %d", r1.Seq, r2.Seq)
	}
}

func TestAIWALSeparateLogsPerPrimitive(t *testing.T) {
	a := NewAIWALRegistry()
	r1, _ := a.Append("trust", "x")
	r2, _ := a.Append("market", "x")
	// Each primitive's seq starts at 1
	if r1.Seq != 1 || r2.Seq != 1 {
		t.Fatalf("per-primitive seqs should both start at 1: %d %d", r1.Seq, r2.Seq)
	}
}

func TestAIWALReadFromAndLimit(t *testing.T) {
	a := NewAIWALRegistry()
	for i := 0; i < 10; i++ {
		a.Append("p", "e-"+itoaInline(i))
	}
	rows, _ := a.Read("p", 5, 0)
	if len(rows) != 6 { // seq 5..10
		t.Fatalf("expected 6 rows, got %d", len(rows))
	}
	rows, _ = a.Read("p", 0, 3)
	if len(rows) != 3 {
		t.Fatalf("limit not enforced: %d", len(rows))
	}
}

func TestAIWALFsyncMarksBoundary(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "e1")
	a.Append("p", "e2")
	head, _ := a.Fsync("p")
	if head != 2 {
		t.Fatalf("fsync head = %d", head)
	}
	a.Append("p", "e3") // beyond fsynced
	s, _ := a.Status("p")
	if s.HeadSeq != 3 || s.FsyncedSeq != 2 {
		t.Fatalf("status: %+v", s)
	}
}

func TestAIWALCheckpointAndRecover(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "e1")
	a.Append("p", "e2")
	a.Append("p", "e3")
	a.Fsync("p")
	a.Checkpoint("p", 2, "state-after-2")
	r, _ := a.Recover("p")
	if r.CheckpointSeq != 2 {
		t.Fatalf("checkpoint seq: %d", r.CheckpointSeq)
	}
	if r.CheckpointBlob != "state-after-2" {
		t.Fatalf("blob: %s", r.CheckpointBlob)
	}
	// Replay should contain only entry 3 (after checkpoint, within fsynced)
	if len(r.Replay) != 1 || r.Replay[0].Seq != 3 {
		t.Fatalf("replay: %+v", r.Replay)
	}
}

func TestAIWALRecoverStopsAtFsyncedBoundary(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "e1")
	a.Append("p", "e2")
	a.Fsync("p")
	a.Append("p", "e3-not-fsynced") // beyond fsynced
	r, _ := a.Recover("p")
	for _, e := range r.Replay {
		if e.Seq > 2 {
			t.Fatalf("recovery should stop at fsynced: replayed seq %d", e.Seq)
		}
	}
}

func TestAIWALRecoverNoCheckpointReturnsFull(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "a")
	a.Append("p", "b")
	a.Fsync("p")
	r, _ := a.Recover("p")
	if r.CheckpointSeq != 0 || len(r.Replay) != 2 {
		t.Fatalf("no-checkpoint recovery: %+v", r)
	}
}

func TestAIWALCheckpointBeyondHeadRejected(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "x")
	if err := a.Checkpoint("p", 99, "blob"); err == nil {
		t.Fatal("checkpoint beyond head should fail")
	}
}

func TestAIWALTruncateRequiresCheckpoint(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "x")
	a.Append("p", "y")
	a.Fsync("p")
	if _, err := a.Truncate("p", 2); err == nil {
		t.Fatal("truncate without checkpoint should fail")
	}
}

func TestAIWALTruncateAfterCheckpoint(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "a")
	a.Append("p", "b")
	a.Append("p", "c")
	a.Fsync("p")
	a.Checkpoint("p", 2, "blob")
	dropped, err := a.Truncate("p", 2)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 2 {
		t.Fatalf("dropped %d, want 2", dropped)
	}
	s, _ := a.Status("p")
	if s.EntryCount != 1 {
		t.Fatalf("entry count after truncate: %d", s.EntryCount)
	}
}

func TestAIWALTruncatePastCheckpointRejected(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "a")
	a.Append("p", "b")
	a.Append("p", "c")
	a.Fsync("p")
	a.Checkpoint("p", 2, "blob")
	if _, err := a.Truncate("p", 3); err == nil {
		t.Fatal("truncate past checkpoint should fail")
	}
}

func TestAIWALStatusReportsAll(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "a")
	a.Append("p", "b")
	a.Fsync("p")
	a.Checkpoint("p", 1, "blob")
	s, _ := a.Status("p")
	if s.HeadSeq != 2 || s.FsyncedSeq != 2 || s.CheckpointSeq != 1 || s.EntryCount != 2 {
		t.Fatalf("status: %+v", s)
	}
}

func TestAIWALList(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("trust", "x")
	a.Append("market", "x")
	l := a.List()
	if len(l) != 2 {
		t.Fatalf("list: %+v", l)
	}
}

func TestAIWALForget(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("a", "x")
	a.Append("b", "x")
	if a.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if a.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestAIWALStats(t *testing.T) {
	a := NewAIWALRegistry()
	a.Append("p", "x")
	a.Read("p", 0, 0)
	a.Checkpoint("p", 1, "blob")
	a.Recover("p")
	s := a.Stats()
	if s.TotalAppends != 1 || s.TotalReads != 1 || s.TotalCheckpoints != 1 || s.TotalRecovers != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestAIWALRejectsBadInput(t *testing.T) {
	a := NewAIWALRegistry()
	if _, err := a.Append("", "x"); err == nil {
		t.Fatal("empty primitive")
	}
	if _, err := a.Append("p", ""); err == nil {
		t.Fatal("empty entry")
	}
	if _, err := a.Fsync(""); err == nil {
		t.Fatal("empty primitive fsync")
	}
	if _, err := a.Fsync("ghost"); err == nil {
		t.Fatal("unknown primitive fsync")
	}
	if err := a.Checkpoint("p", -1, "blob"); err == nil {
		t.Fatal("negative seq")
	}
}
