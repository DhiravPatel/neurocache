package llmstack

import (
	"testing"
	"time"
)

func TestSemRateLimiterAllowsFirstRequest(t *testing.T) {
	r := NewSemRateLimiter()
	res, err := r.Check("acme", "summarize the doc")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Allow {
		t.Fatalf("first request was denied: %+v", res)
	}
}

func TestSemRateLimiterDeniesSimilarBurst(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("acme", 3, 0.85, 60*time.Second)
	q := "summarize the document carefully please"
	for i := 0; i < 3; i++ {
		res, _ := r.Check("acme", q)
		if !res.Allow {
			t.Fatalf("denied too early at %d: %+v", i, res)
		}
	}
	// 4th similar request should be denied
	res, _ := r.Check("acme", q)
	if res.Allow {
		t.Fatalf("expected deny, got allow: %+v", res)
	}
	if res.Reason != "rate_limit_exceeded" {
		t.Fatalf("reason = %s", res.Reason)
	}
}

func TestSemRateLimiterDifferentSemanticsBypassesLimit(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("acme", 2, 0.85, 60*time.Second)
	r.Check("acme", "summarize the doc carefully")
	r.Check("acme", "summarize the doc carefully")
	// Wildly different request — should bypass the bucket
	res, _ := r.Check("acme", "translate French to English now")
	if !res.Allow {
		t.Fatalf("unrelated request was denied: %+v", res)
	}
}

func TestSemRateLimiterPeekDoesNotRecord(t *testing.T) {
	r := NewSemRateLimiter()
	r.Peek("acme", "ignored")
	r.Peek("acme", "ignored")
	st, _ := r.Status("acme")
	if st.BucketSize != 0 {
		t.Fatalf("peek polluted bucket: %d", st.BucketSize)
	}
}

func TestSemRateLimiterPerTenantIsolation(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("acme", 1, 0.85, 60*time.Second)
	r.Check("acme", "summarize document")
	resAcme, _ := r.Check("acme", "summarize document")
	if resAcme.Allow {
		t.Fatal("acme should be denied")
	}
	resGlobex, _ := r.Check("globex", "summarize document")
	if !resGlobex.Allow {
		t.Fatal("globex should be allowed (own bucket)")
	}
}

func TestSemRateLimiterReset(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("acme", 2, 0.85, 60*time.Second)
	r.Check("acme", "x x x")
	r.Check("acme", "x x x")
	r.Reset("acme")
	res, _ := r.Check("acme", "x x x")
	if !res.Allow {
		t.Fatal("after reset, request should be allowed")
	}
}

func TestSemRateLimiterWindowExpires(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("acme", 1, 0.85, 1*time.Millisecond)
	r.Check("acme", "summarize the doc")
	time.Sleep(5 * time.Millisecond)
	// Window has expired — bucket is empty
	res, _ := r.Check("acme", "summarize the doc")
	if !res.Allow {
		t.Fatalf("post-window request should be allowed: %+v", res)
	}
}

func TestSemRateLimiterStatus(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("acme", 7, 0.75, 30*time.Second)
	r.Check("acme", "x")
	st, ok := r.Status("acme")
	if !ok || st.Limit != 7 || st.Threshold != 0.75 || st.WindowSec != 30 {
		t.Fatalf("status = %+v", st)
	}
	if st.BucketSize != 1 {
		t.Fatalf("bucket size = %d", st.BucketSize)
	}
}

func TestSemRateLimiterStatusMissingTenant(t *testing.T) {
	r := NewSemRateLimiter()
	if _, ok := r.Status("ghost"); ok {
		t.Fatal("unknown tenant should report ok=false")
	}
}

func TestSemRateLimiterList(t *testing.T) {
	r := NewSemRateLimiter()
	r.Check("c", "x")
	r.Check("a", "x")
	r.Check("b", "x")
	l := r.List()
	if len(l) != 3 || l[0] != "a" || l[2] != "c" {
		t.Fatalf("list = %v", l)
	}
}

func TestSemRateLimiterRecent(t *testing.T) {
	r := NewSemRateLimiter()
	r.Check("acme", "first")
	r.Check("acme", "second")
	rec, ok := r.Recent("acme")
	if !ok || len(rec) != 2 {
		t.Fatalf("recent = %d", len(rec))
	}
	if rec[1].Text != "second" {
		t.Fatalf("newest last broken: %+v", rec)
	}
}

func TestSemRateLimiterConfigRejectsBadInput(t *testing.T) {
	r := NewSemRateLimiter()
	if err := r.Configure("", 1, 0.5, time.Second); err == nil {
		t.Fatal("empty tenant should fail")
	}
	if err := r.Configure("a", 1, 1.5, time.Second); err == nil {
		t.Fatal("threshold > 1 should fail")
	}
	if err := r.Configure("a", -1, 0.5, time.Second); err == nil {
		t.Fatal("negative limit should fail")
	}
}

func TestSemRateLimiterStatsAdvance(t *testing.T) {
	r := NewSemRateLimiter()
	r.Configure("a", 1, 0.85, 60*time.Second)
	r.Check("a", "x x x")
	r.Check("a", "x x x") // denied
	r.Peek("a", "y")
	s := r.Stats()
	if s.TotalChecks != 2 || s.TotalAllowed != 1 || s.TotalDenied != 1 || s.TotalPeeks != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func BenchmarkSemRateLimiterCheck(b *testing.B) {
	r := NewSemRateLimiter()
	r.Configure("acme", 100, 0.85, 60*time.Second)
	for i := 0; i < 20; i++ {
		r.Check("acme", "warmup request "+itoaBench(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Peek("acme", "summarize the doc briefly")
	}
}

func itoaBench(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
