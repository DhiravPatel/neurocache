package transaction

import "testing"

func TestQueueAndCommit(t *testing.T) {
	s := New()
	if err := s.Begin(); err != nil {
		t.Fatal(err)
	}
	if !s.InProgress() {
		t.Error("InProgress should be true")
	}
	s.Queue("SET", []string{"k", "v"})
	s.Queue("INCR", []string{"n"})
	cmds, aborted := s.Commit()
	if aborted {
		t.Error("unexpected abort")
	}
	if len(cmds) != 2 {
		t.Fatalf("len(cmds) = %d", len(cmds))
	}
}

func TestWatchAbort(t *testing.T) {
	s := New()
	versions := map[string]uint64{"k": 1}
	s.Watch("k", versions["k"])
	s.Begin()
	s.Queue("SET", []string{"k", "v"})
	versions["k"] = 2 // racy write
	s.CheckDirty(func(k string) uint64 { return versions[k] })
	_, aborted := s.Commit()
	if !aborted {
		t.Error("transaction should abort when watched key changed")
	}
}

func TestNestedMultiRejected(t *testing.T) {
	s := New()
	s.Begin()
	if err := s.Begin(); err == nil {
		t.Error("nested MULTI should error")
	}
}

func TestDiscard(t *testing.T) {
	s := New()
	s.Begin()
	s.Queue("SET", []string{"k", "v"})
	if err := s.Discard(); err != nil {
		t.Fatal(err)
	}
	if s.InProgress() {
		t.Error("InProgress should be false after DISCARD")
	}
}
