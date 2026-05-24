package llmstack

import (
	"testing"
	"time"
)

func TestSessionClusterObserveCreatesCohort(t *testing.T) {
	s := NewSessionCluster()
	if err := s.Observe("support", "sess-1", "how do I cancel my plan", 0); err != nil {
		t.Fatal(err)
	}
	st := s.Stats()
	if st.Clusters != 1 || st.TotalCohorts != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestSessionClusterMergesSimilarRequests(t *testing.T) {
	s := NewSessionCluster()
	// Three semantically similar billing-cancellation requests
	s.Observe("support", "sess-1", "cancel subscription billing mid-cycle refund", 0)
	s.Observe("support", "sess-2", "subscription cancel billing mid-cycle refund", 0)
	s.Observe("support", "sess-3", "billing cancel subscription mid-cycle refund process", 0)
	st := s.Stats()
	if st.TotalCohorts != 1 {
		t.Fatalf("similar requests should collapse: cohorts=%d", st.TotalCohorts)
	}
}

func TestSessionClusterDifferentTopicsSeparate(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("support", "sess-1", "cancel subscription billing mid-cycle refund", 0)
	s.Observe("support", "sess-2", "weather forecast api token rotation", 0)
	st := s.Stats()
	if st.TotalCohorts != 2 {
		t.Fatalf("different topics should not merge: cohorts=%d", st.TotalCohorts)
	}
}

func TestSessionClusterTopByMemberCount(t *testing.T) {
	s := NewSessionCluster()
	// 5 sessions on cancellation cohort
	for i := 1; i <= 5; i++ {
		s.Observe("support", "sess-c"+itoaBench(i), "cancel subscription billing mid-cycle refund", 0)
	}
	// 2 sessions on weather cohort
	s.Observe("support", "sess-w1", "weather forecast api token rotation", 0)
	s.Observe("support", "sess-w2", "weather forecast api token rotation", 0)
	rows, _ := s.Top("support", 10, 0)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Members != 5 {
		t.Fatalf("top cohort members = %d, want 5", rows[0].Members)
	}
}

func TestSessionClusterMembers(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("support", "alice", "cancel subscription billing mid-cycle refund", 0)
	s.Observe("support", "bob", "subscription cancel billing mid-cycle refund", 0)
	rows, _ := s.Top("support", 1, 0)
	mem, ok := s.Members("support", rows[0].CohortID)
	if !ok || len(mem) != 2 {
		t.Fatalf("members = %v", mem)
	}
	// Sorted alphabetically
	if mem[0] != "alice" || mem[1] != "bob" {
		t.Fatalf("not sorted: %v", mem)
	}
}

func TestSessionClusterStatus(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("support", "sess-1", "cancel subscription billing", 0)
	st, ok := s.Status("support", "sess-1")
	if !ok {
		t.Fatal("status missing")
	}
	if st.SessionID != "sess-1" || st.CohortID == "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestSessionClusterStatusUnknownSession(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("support", "sess-1", "x x x x", 0)
	if _, ok := s.Status("support", "ghost"); ok {
		t.Fatal("unknown session should report not-ok")
	}
}

func TestSessionClusterTopWindowFiltersStale(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("support", "old", "cancellation question lots of words", 0)
	time.Sleep(3 * time.Millisecond)
	s.Observe("support", "new", "weather forecast api token rotation", 0)
	rows, _ := s.Top("support", 10, 1*time.Millisecond)
	// Only the new cohort survives the 1ms window
	if len(rows) != 1 || rows[0].Members != 1 {
		t.Fatalf("window filter broken: %+v", rows)
	}
}

func TestSessionClusterSessionMovesOnObserve(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("support", "alice", "cancel subscription billing", 0)
	cohort1, _ := s.Status("support", "alice")
	// Same session, very different topic
	s.Observe("support", "alice", "completely unrelated weather query api", 0)
	cohort2, _ := s.Status("support", "alice")
	if cohort1.CohortID == cohort2.CohortID {
		t.Fatalf("session should have moved cohort: %s vs %s", cohort1.CohortID, cohort2.CohortID)
	}
}

func TestSessionClusterListSorted(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("zeta", "s", "x", 0)
	s.Observe("alpha", "s", "x", 0)
	s.Observe("mid", "s", "x", 0)
	l := s.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestSessionClusterResetOne(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("a", "s", "x", 0)
	s.Observe("b", "s", "x", 0)
	if s.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestSessionClusterResetAll(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("a", "s", "x", 0)
	s.Observe("b", "s", "x", 0)
	if s.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestSessionClusterRejectsBadInput(t *testing.T) {
	s := NewSessionCluster()
	if err := s.Observe("", "s", "x", 0); err == nil {
		t.Fatal("empty cluster_id should fail")
	}
	if err := s.Observe("c", "", "x", 0); err == nil {
		t.Fatal("empty session_id should fail")
	}
	if err := s.Observe("c", "s", "", 0); err == nil {
		t.Fatal("empty text should fail")
	}
}

func TestSessionClusterMembersUnknownCohort(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("c", "s", "x x x x", 0)
	if _, ok := s.Members("c", "ghost"); ok {
		t.Fatal("unknown cohort should report not-ok")
	}
}

func TestSessionClusterStatsAdvance(t *testing.T) {
	s := NewSessionCluster()
	s.Observe("c", "s1", "x", 0)
	s.Observe("c", "s2", "completely different topic words", 0)
	st := s.Stats()
	if st.Clusters != 1 || st.TotalCohorts != 2 || st.TotalObserves != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkSessionClusterObserve(b *testing.B) {
	s := NewSessionCluster()
	// Pre-seed with 30 cohorts so observe has cohorts to scan
	for i := 0; i < 30; i++ {
		s.Observe("c", "warmup-"+itoaBench(i), "topic " + itoaBench(i) + " filler words here", 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Observe("c", "sess-many", "cancel subscription billing refund", 0)
	}
}
