package llmstack

import (
	"testing"
)

func TestLLMLimiterConfigAndReserve(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 10000)
	r, err := l.Reserve("openai", "", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Allowed || r.Reserved != 5000 {
		t.Fatalf("reserve = %+v", r)
	}
	if r.Remaining != 5000 {
		t.Fatalf("remaining = %d, want 5000", r.Remaining)
	}
}

func TestLLMLimiterRejectsOverBudget(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 1000)
	l.Reserve("openai", "", 700)
	r, _ := l.Reserve("openai", "", 500) // would total 1200 > 1000
	if r.Allowed {
		t.Fatalf("should reject; remaining=%d", r.Remaining)
	}
	if r.Remaining != 300 {
		t.Fatalf("remaining = %d, want 300", r.Remaining)
	}
}

func TestLLMLimiterUnknownProvider(t *testing.T) {
	l := NewLLMLimiter()
	if _, err := l.Reserve("nope", "", 100); err == nil {
		t.Fatal("reserve on unknown should fail")
	}
}

func TestLLMLimiterRecordsActualOvershoot(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 1000)
	l.Reserve("openai", "", 200)
	// Actual call used 250 — eat the overshoot
	l.Record("openai", "", 250, 200)
	r, _ := l.Usage("openai", "")
	if r.Used != 250 {
		t.Fatalf("used after overshoot = %d, want 250", r.Used)
	}
}

func TestLLMLimiterRecordsActualUndershoot(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 1000)
	l.Reserve("openai", "", 500)
	// Actual call used only 300 — return 200 to the bucket
	l.Record("openai", "", 300, 500)
	r, _ := l.Usage("openai", "")
	if r.Used != 300 {
		t.Fatalf("used after undershoot = %d, want 300", r.Used)
	}
}

func TestLLMLimiterTenantScoped(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "acme", 1000)
	l.Config("openai", "globex", 1000)
	l.Reserve("openai", "acme", 800)
	r, _ := l.Reserve("openai", "globex", 800)
	if !r.Allowed {
		t.Fatal("globex should be allowed (acme's budget doesn't affect it)")
	}
}

func TestLLMLimiterReset(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 1000)
	l.Reserve("openai", "", 500)
	l.Reset("openai", "")
	r, _ := l.Usage("openai", "")
	if r.Used != 0 {
		t.Fatalf("used after reset = %d", r.Used)
	}
}

func TestLLMLimiterResetAll(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 1000)
	l.Config("anthropic", "", 1000)
	if n := l.Reset("", ""); n != 2 {
		t.Fatalf("reset all = %d, want 2", n)
	}
}

func TestLLMLimiterAll(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "acme", 1000)
	l.Config("anthropic", "acme", 2000)
	rows := l.All()
	if len(rows) != 2 {
		t.Fatalf("all = %d, want 2", len(rows))
	}
}

func TestLLMLimiterStatsAdvance(t *testing.T) {
	l := NewLLMLimiter()
	l.Config("openai", "", 100)
	l.Reserve("openai", "", 50)
	l.Reserve("openai", "", 200) // rejected
	l.Record("openai", "", 50, 50)
	s := l.Stats()
	if s.TotalReserves != 2 || s.TotalAllowed != 1 || s.TotalRejected != 1 || s.TotalRecords != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestLLMLimiterRejectsBadConfig(t *testing.T) {
	l := NewLLMLimiter()
	if err := l.Config("", "", 100); err == nil {
		t.Fatal("empty provider should fail")
	}
	if err := l.Config("x", "", 0); err == nil {
		t.Fatal("zero TPM should fail")
	}
}
