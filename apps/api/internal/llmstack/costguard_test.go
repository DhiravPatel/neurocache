package llmstack

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCostGuardBasicCheckRecord(t *testing.T) {
	g := NewCostGuard()
	g.SetCap("user:42", 1.00, 0) // $1.00 lifetime cap

	// Within cap.
	if err := g.Check("user:42", 0.50); err != nil {
		t.Fatalf("Check should pass: %v", err)
	}
	total, err := g.Record("user:42", 0.50)
	if err != nil {
		t.Fatalf("Record err: %v", err)
	}
	if total < 0.49 || total > 0.51 {
		t.Fatalf("total=%f want~0.50", total)
	}

	// Approaching cap.
	if err := g.Check("user:42", 0.40); err != nil {
		t.Fatalf("0.40 check should pass at 0.50 spent: %v", err)
	}
	g.Record("user:42", 0.40)

	// Now over cap on next check.
	if err := g.Check("user:42", 0.20); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("over-cap check should reject; got %v", err)
	}
}

func TestCostGuardUnknownScope(t *testing.T) {
	g := NewCostGuard()
	if err := g.Check("nobody", 1.0); !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("unknown scope should error; got %v", err)
	}
}

func TestCostGuardCheckAndRecordAtomic(t *testing.T) {
	// 100 concurrent goroutines each try to spend $1 against a $50 cap.
	// Only 50 should succeed; the rest must reject.
	g := NewCostGuard()
	g.SetCap("u", 50.0, 0)
	var wg sync.WaitGroup
	var ok, rej int64
	var mu sync.Mutex
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.CheckAndRecord("u", 1.0)
			mu.Lock()
			if err == nil {
				ok++
			} else if errors.Is(err, ErrCapExceeded) {
				rej++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if ok != 50 || rej != 50 {
		t.Fatalf("CAS should fairly split; ok=%d rej=%d", ok, rej)
	}
}

func TestCostGuardWindowRollover(t *testing.T) {
	g := NewCostGuard()
	g.SetCap("u", 1.0, 1) // $1 / 1 second
	g.Record("u", 0.80)
	if spent, _ := g.Spent("u"); spent < 0.79 {
		t.Fatalf("spent=%f", spent)
	}
	time.Sleep(1100 * time.Millisecond)
	// Window rolled — Spent should reset on next call.
	if spent, _ := g.Spent("u"); spent != 0 {
		t.Fatalf("after rollover spent=%f want=0", spent)
	}
	// Full cap available again.
	if err := g.Check("u", 0.95); err != nil {
		t.Fatalf("post-rollover check should pass: %v", err)
	}
}

func TestCostGuardReset(t *testing.T) {
	g := NewCostGuard()
	g.SetCap("u", 1.0, 0)
	g.Record("u", 0.90)
	if err := g.Reset("u"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if spent, _ := g.Spent("u"); spent != 0 {
		t.Fatalf("after Reset spent=%f want=0", spent)
	}
}

func TestCostGuardListAndMeta(t *testing.T) {
	g := NewCostGuard()
	g.SetCap("a", 10.0, 0)
	g.SetCap("b", 5.0, 60)
	g.Record("a", 2.5)
	g.Check("a", 100) // should reject; util high
	rows := g.List()
	if len(rows) != 2 {
		t.Fatalf("List=%d want=2", len(rows))
	}
	meta := g.Meta()
	if meta.TotalChecks != 1 || meta.TotalRejections != 1 {
		t.Fatalf("meta=%+v want checks=1 rej=1", meta)
	}
}

func TestCostGuardCapUpdatePreservesSpent(t *testing.T) {
	g := NewCostGuard()
	g.SetCap("u", 1.0, 0)
	g.Record("u", 0.50)
	g.SetCap("u", 5.0, 0) // raise the cap
	if spent, _ := g.Spent("u"); spent < 0.49 || spent > 0.51 {
		t.Fatalf("cap update lost spent: %f", spent)
	}
	if limit, _ := g.Limit("u"); limit < 4.99 || limit > 5.01 {
		t.Fatalf("limit not updated: %f", limit)
	}
}

// ─── benchmarks ─────────────────────────────────────────────────

func BenchmarkCostGuardCheck(b *testing.B) {
	g := NewCostGuard()
	g.SetCap("u", 1_000_000.0, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.Check("u", 0.001)
	}
}

func BenchmarkCostGuardCheckAndRecord(b *testing.B) {
	g := NewCostGuard()
	g.SetCap("u", 1_000_000.0, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.CheckAndRecord("u", 0.001)
	}
}
