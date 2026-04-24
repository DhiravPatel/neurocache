package replication

import "testing"

func TestNewStateIsMaster(t *testing.T) {
	s := NewState()
	if s.Role() != RoleMaster {
		t.Fatalf("role=%v want master", s.Role())
	}
	if s.ReplID() == "" || len(s.ReplID()) != 40 {
		t.Fatalf("replid=%q want 40-hex", s.ReplID())
	}
}

func TestRoleFlipResetsReplID(t *testing.T) {
	s := NewState()
	first := s.ReplID()
	s.SetRoleReplica("h", "6379")
	if !s.IsReplica() {
		t.Fatal("expected IsReplica=true after SetRoleReplica")
	}
	s.SetRoleMaster()
	if s.IsReplica() {
		t.Fatal("expected IsReplica=false after promotion")
	}
	if s.ReplID() == first {
		t.Fatal("replid should roll on promotion")
	}
	if s.PrevReplID() != first {
		t.Fatal("prev replid should carry the prior value for partial resync")
	}
}

func TestOffsetAdvances(t *testing.T) {
	s := NewState()
	s.AdvanceOffset(10)
	s.AdvanceOffset(5)
	if s.Offset() != 15 {
		t.Fatalf("offset=%d want 15", s.Offset())
	}
}
