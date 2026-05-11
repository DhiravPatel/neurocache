package llmstack

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentLoopStartAndStep(t *testing.T) {
	a := NewAgentLoopTracker()
	if err := a.Start("l1", LoopOpts{MaxSteps: 10}); err != nil {
		t.Fatal(err)
	}
	r, ok := a.Step("l1", StepOpts{})
	if !ok {
		t.Fatal("step returned false")
	}
	if r.ShouldStop {
		t.Fatal("should not stop on first step")
	}
	if r.Steps != 1 {
		t.Fatalf("steps = %d, want 1", r.Steps)
	}
}

func TestAgentLoopMaxStepsEnforced(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxSteps: 3})
	for i := 0; i < 3; i++ {
		r, _ := a.Step("l1", StepOpts{})
		if r.ShouldStop {
			t.Fatalf("should not stop at step %d", i+1)
		}
	}
	r, _ := a.Step("l1", StepOpts{})
	if !r.ShouldStop {
		t.Fatalf("should stop at step 4 (over max=3): %+v", r)
	}
}

func TestAgentLoopMaxTokensEnforced(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxTokens: 500})
	a.Step("l1", StepOpts{Tokens: 200}) // 200
	a.Step("l1", StepOpts{Tokens: 200}) // 400
	r, _ := a.Step("l1", StepOpts{Tokens: 200}) // 600 — breach
	if !r.ShouldStop {
		t.Fatal("should stop on token budget")
	}
}

func TestAgentLoopMaxToolCallsEnforced(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxToolCalls: 2})
	a.Step("l1", StepOpts{ToolCall: true}) // 1
	a.Step("l1", StepOpts{ToolCall: true}) // 2
	r, _ := a.Step("l1", StepOpts{ToolCall: true}) // 3 — breach
	if !r.ShouldStop {
		t.Fatal("should stop on tool_call budget")
	}
}

func TestAgentLoopMaxTimeEnforced(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxTimeMS: 30})
	time.Sleep(40 * time.Millisecond)
	r, _ := a.Step("l1", StepOpts{})
	if !r.ShouldStop {
		t.Fatal("should stop on time budget")
	}
}

func TestAgentLoopStopLatched(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxSteps: 1})
	a.Step("l1", StepOpts{})
	r1, _ := a.Step("l1", StepOpts{}) // breach
	if !r1.ShouldStop {
		t.Fatal("should stop")
	}
	r2, _ := a.Step("l1", StepOpts{}) // already stopped
	if !r2.ShouldStop {
		t.Fatal("should remain stopped")
	}
	if r2.Reason != r1.Reason {
		t.Fatalf("reason changed: %q -> %q", r1.Reason, r2.Reason)
	}
	// Counters should not advance after stop
	if r2.Steps != r1.Steps {
		t.Fatalf("steps advanced after stop: %d -> %d", r1.Steps, r2.Steps)
	}
}

func TestAgentLoopReset(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxSteps: 2})
	a.Step("l1", StepOpts{})
	a.Step("l1", StepOpts{})
	r1, _ := a.Step("l1", StepOpts{})
	if !r1.ShouldStop {
		t.Fatal("expected stop")
	}
	if !a.Reset("l1") {
		t.Fatal("reset should return true")
	}
	r2, _ := a.Step("l1", StepOpts{})
	if r2.ShouldStop {
		t.Fatalf("after reset should not stop: %+v", r2)
	}
}

func TestAgentLoopStatusShowsCaps(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxSteps: 10, MaxTokens: 1000})
	a.Step("l1", StepOpts{Tokens: 50})
	s, ok := a.Status("l1")
	if !ok {
		t.Fatal("status returned false")
	}
	if s.MaxSteps != 10 || s.MaxTokens != 1000 {
		t.Fatalf("caps = %+v", s)
	}
	if s.Steps != 1 || s.Tokens != 50 {
		t.Fatalf("counters = %+v", s)
	}
}

func TestAgentLoopUnknownReturnsFalse(t *testing.T) {
	a := NewAgentLoopTracker()
	if _, ok := a.Step("nope", StepOpts{}); ok {
		t.Fatal("step on unknown loop should return false")
	}
	if _, ok := a.Status("nope"); ok {
		t.Fatal("status on unknown loop should return false")
	}
}

func TestAgentLoopForget(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{})
	if !a.Forget("l1") {
		t.Fatal("forget should return true")
	}
	if a.Forget("l1") {
		t.Fatal("forget on missing should return false")
	}
}

func TestAgentLoopZeroCapsMeanUnlimited(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{}) // all caps zero
	for i := 0; i < 100; i++ {
		r, _ := a.Step("l1", StepOpts{Tokens: 1000, ToolCall: true})
		if r.ShouldStop {
			t.Fatalf("should never stop with zero caps, stopped at step %d", i+1)
		}
	}
}

func TestAgentLoopConcurrentStepsRaceFree(t *testing.T) {
	// 100 goroutines step simultaneously with MaxSteps=50.
	// Once stopped, subsequent calls early-return without
	// incrementing (this is the documented latch behavior). We
	// verify: at least 51 steps happened (the over-cap one that
	// triggered the stop), exactly 1 stop reason is latched, and
	// all post-stop callers report should_stop=true with the same
	// reason.
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxSteps: 50})
	var wg sync.WaitGroup
	var stopVerdicts atomic.Int32
	reasons := sync.Map{}
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func() {
			defer wg.Done()
			r, _ := a.Step("l1", StepOpts{})
			if r.ShouldStop {
				stopVerdicts.Add(1)
				reasons.Store(r.Reason, true)
			}
		}()
	}
	wg.Wait()
	if stopVerdicts.Load() == 0 {
		t.Fatal("expected at least one should_stop verdict")
	}
	// Exactly one distinct reason should have been latched.
	n := 0
	reasons.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatalf("expected exactly 1 distinct reason, got %d", n)
	}
	s, _ := a.Status("l1")
	// Must have incremented at least past the cap (51 or more).
	if s.Steps < 51 {
		t.Fatalf("steps = %d, want >=51 (must cross cap)", s.Steps)
	}
}

func TestAgentLoopStatsAdvance(t *testing.T) {
	a := NewAgentLoopTracker()
	a.Start("l1", LoopOpts{MaxSteps: 1})
	a.Step("l1", StepOpts{})
	a.Step("l1", StepOpts{}) // triggers stop
	stats := a.Stats()
	if stats.TotalStarts != 1 || stats.TotalStops != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}
