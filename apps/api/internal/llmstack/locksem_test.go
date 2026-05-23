package llmstack

import (
	"testing"
	"time"
)

func TestSemLockAcquireUnique(t *testing.T) {
	s := NewSemLocks()
	r, err := s.Acquire("agents", "summarize doc 12", 0.85, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Acquired {
		t.Fatal("first acquire should succeed")
	}
	if r.Token == "" {
		t.Fatal("acquired token should be set")
	}
}

func TestSemLockRejectsSemanticCollision(t *testing.T) {
	// Use heavy lexical overlap so the hashed-BoW fallback produces
	// a high cosine. Real workloads ship app-supplied embeddings via
	// the Vec field for paraphrase-level matching.
	s := NewSemLocks()
	r1, _ := s.Acquire("agents", "summarize document twelve", 0.5, time.Second)
	if !r1.Acquired {
		t.Fatal("first should acquire")
	}
	r2, _ := s.Acquire("agents", "summarize document twelve please now", 0.5, time.Second)
	if r2.Acquired {
		t.Fatalf("overlapping work should be rejected: %+v", r2)
	}
	if r2.SimilarText == "" {
		t.Fatal("rejection should report the colliding text")
	}
}

func TestSemLockAcceptsDifferentMeaning(t *testing.T) {
	s := NewSemLocks()
	s.Acquire("agents", "summarize document twelve", 0.85, time.Second)
	// Different work
	r, _ := s.Acquire("agents", "compute pi to 100 digits", 0.85, time.Second)
	if !r.Acquired {
		t.Fatalf("unrelated work should acquire: %+v", r)
	}
}

func TestSemLockRelease(t *testing.T) {
	s := NewSemLocks()
	r, _ := s.Acquire("agents", "work A", 0.85, time.Second)
	if !s.Release("agents", r.Token) {
		t.Fatal("release valid token should succeed")
	}
	if s.Release("agents", r.Token) {
		t.Fatal("release of released token should be no-op")
	}
}

func TestSemLockAfterReleaseCanReacquire(t *testing.T) {
	s := NewSemLocks()
	r1, _ := s.Acquire("agents", "summarize doc 12", 0.85, time.Second)
	s.Release("agents", r1.Token)
	r2, _ := s.Acquire("agents", "summarize document 12", 0.85, time.Second)
	if !r2.Acquired {
		t.Fatalf("after release paraphrase should acquire: %+v", r2)
	}
}

func TestSemLockTTLExpiry(t *testing.T) {
	s := NewSemLocks()
	s.Acquire("agents", "work A", 0.85, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	r, _ := s.Acquire("agents", "work A", 0.85, time.Second)
	if !r.Acquired {
		t.Fatalf("after TTL expiry, paraphrase should acquire: %+v", r)
	}
}

func TestSemLockStatus(t *testing.T) {
	s := NewSemLocks()
	s.Acquire("agents", "work A", 0.85, time.Second)
	s.Acquire("agents", "totally different work B", 0.85, time.Second)
	rows := s.Status("agents", 0)
	if len(rows) != 2 {
		t.Fatalf("status = %d, want 2", len(rows))
	}
}

func TestSemLockForgetByText(t *testing.T) {
	s := NewSemLocks()
	s.Acquire("agents", "specific work", 0.85, time.Second)
	if n := s.ForgetByText("agents", "specific work"); n != 1 {
		t.Fatalf("forget = %d, want 1", n)
	}
}

func TestSemLockForgetNamespace(t *testing.T) {
	s := NewSemLocks()
	s.Acquire("ns1", "a", 0.85, time.Second)
	s.Acquire("ns1", "b really different content here entirely", 0.85, time.Second)
	if n := s.ForgetNamespace("ns1"); n != 2 {
		t.Fatalf("forget namespace = %d, want 2", n)
	}
}

func TestSemLockRejectsEmpty(t *testing.T) {
	s := NewSemLocks()
	if _, err := s.Acquire("", "x", 0.85, time.Second); err == nil {
		t.Fatal("empty namespace should fail")
	}
	if _, err := s.Acquire("ns", "", 0.85, time.Second); err == nil {
		t.Fatal("empty text should fail")
	}
}

func TestSemLockStatsAdvance(t *testing.T) {
	s := NewSemLocks()
	r, _ := s.Acquire("ns", "work A", 0.85, time.Second)
	s.Acquire("ns", "work A duplicate", 0.5, time.Second) // collide & reject
	s.Release("ns", r.Token)
	st := s.Stats()
	if st.TotalAcquires != 2 || st.TotalAcquired != 1 || st.TotalRejected != 1 || st.TotalReleases != 1 {
		t.Fatalf("stats = %+v", st)
	}
}
