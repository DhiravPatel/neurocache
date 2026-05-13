package llmstack

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHedgeStartRequiresProviders(t *testing.T) {
	h := NewHedgeTracker()
	if _, err := h.Start("c1", nil); err == nil {
		t.Fatal("expected error for empty providers")
	}
}

func TestHedgeFirstPublishWins(t *testing.T) {
	h := NewHedgeTracker()
	r, err := h.Start("c1", []string{"openai", "anthropic", "google"})
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := h.Publish("c1", "anthropic", "claude-answer", r.Token)
	if !ok {
		t.Fatal("publish returned false")
	}
	if !pr.IsWinner {
		t.Fatal("first publish should win")
	}
	if pr.Winner != "anthropic" {
		t.Fatalf("winner = %s", pr.Winner)
	}
}

func TestHedgeSecondPublishLoses(t *testing.T) {
	h := NewHedgeTracker()
	r, _ := h.Start("c1", []string{"openai", "anthropic"})
	h.Publish("c1", "openai", "gpt-answer", r.Token)
	time.Sleep(5 * time.Millisecond)
	pr, _ := h.Publish("c1", "anthropic", "claude-answer", r.Token)
	if pr.IsWinner {
		t.Fatal("second publish should NOT win")
	}
	if pr.Winner != "openai" {
		t.Fatalf("winner = %s, want openai", pr.Winner)
	}
	if pr.LatencyMS < pr.WinnerLatMS {
		t.Fatalf("late arrival should have >= winner latency: got %d vs winner %d",
			pr.LatencyMS, pr.WinnerLatMS)
	}
}

func TestHedgePublishRejectsBadToken(t *testing.T) {
	h := NewHedgeTracker()
	h.Start("c1", []string{"a", "b"})
	if _, ok := h.Publish("c1", "a", "result", "wrong-token"); ok {
		t.Fatal("publish with wrong token should fail")
	}
}

func TestHedgePublishRejectsUnknownProvider(t *testing.T) {
	h := NewHedgeTracker()
	r, _ := h.Start("c1", []string{"a", "b"})
	if _, ok := h.Publish("c1", "rogue", "result", r.Token); ok {
		t.Fatal("publish from unregistered provider should fail")
	}
}

func TestHedgeWaitGetsFirstResult(t *testing.T) {
	h := NewHedgeTracker()
	r, _ := h.Start("c1", []string{"openai", "anthropic"})
	done := make(chan HedgeWaitResult, 1)
	go func() {
		done <- h.Wait("c1", 2*time.Second)
	}()
	time.Sleep(10 * time.Millisecond)
	h.Publish("c1", "anthropic", "claude-answer", r.Token)
	w := <-done
	if !w.Got {
		t.Fatal("wait should succeed")
	}
	if w.Result != "claude-answer" || w.Winner != "anthropic" {
		t.Fatalf("wait result = %+v", w)
	}
}

func TestHedgeWaitTimesOut(t *testing.T) {
	h := NewHedgeTracker()
	h.Start("c1", []string{"a"})
	w := h.Wait("c1", 30*time.Millisecond)
	if w.Got {
		t.Fatal("wait should time out when no publish")
	}
}

func TestHedgeWaitOnUnknownCall(t *testing.T) {
	h := NewHedgeTracker()
	w := h.Wait("nope", 100*time.Millisecond)
	if w.Got {
		t.Fatal("unknown call_id should return got=false")
	}
}

func TestHedgeStatusReportsPerProviderState(t *testing.T) {
	h := NewHedgeTracker()
	r, _ := h.Start("c1", []string{"openai", "anthropic", "google"})
	h.Publish("c1", "openai", "x", r.Token)
	h.Publish("c1", "anthropic", "y", r.Token)
	s, ok := h.Status("c1")
	if !ok {
		t.Fatal("status returned false")
	}
	if s.Winner != "openai" {
		t.Fatalf("winner = %s", s.Winner)
	}
	states := map[string]string{}
	for _, p := range s.Providers {
		states[p.Provider] = p.State
	}
	if states["openai"] != "winner" ||
		states["anthropic"] != "late" ||
		states["google"] != "pending" {
		t.Fatalf("states = %v", states)
	}
}

func TestHedgeForget(t *testing.T) {
	h := NewHedgeTracker()
	h.Start("c1", []string{"a"})
	if !h.Forget("c1") {
		t.Fatal("forget should return true")
	}
	if h.Forget("c1") {
		t.Fatal("forget on missing should return false")
	}
}

func TestHedgeStatsAggregatePerProvider(t *testing.T) {
	h := NewHedgeTracker()
	// openai wins twice; anthropic wins once.
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("c%d", i)
		r, _ := h.Start(id, []string{"openai", "anthropic"})
		h.Publish(id, "openai", "x", r.Token)
	}
	r, _ := h.Start("c2", []string{"openai", "anthropic"})
	h.Publish("c2", "anthropic", "y", r.Token)

	s := h.Stats()
	if s.TotalHedges != 3 {
		t.Fatalf("total_hedges = %d", s.TotalHedges)
	}
	stats := map[string]ProviderStatsRow{}
	for _, p := range s.Providers {
		stats[p.Provider] = p
	}
	if stats["openai"].Wins != 2 {
		t.Fatalf("openai wins = %d", stats["openai"].Wins)
	}
	if stats["anthropic"].Wins != 1 {
		t.Fatalf("anthropic wins = %d", stats["anthropic"].Wins)
	}
}

func TestHedgeConcurrentPublishOneWinner(t *testing.T) {
	// 50 goroutines all publish simultaneously; exactly 1 should win.
	h := NewHedgeTracker()
	providers := make([]string, 50)
	for i := range providers {
		providers[i] = fmt.Sprintf("p%d", i)
	}
	r, _ := h.Start("c1", providers)

	var wg sync.WaitGroup
	var winners atomic.Int32
	wg.Add(len(providers))
	for _, p := range providers {
		p := p
		go func() {
			defer wg.Done()
			pr, _ := h.Publish("c1", p, "result-"+p, r.Token)
			if pr.IsWinner {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", winners.Load())
	}
}

func TestHedgeSavedMSCounter(t *testing.T) {
	h := NewHedgeTracker()
	r, _ := h.Start("c1", []string{"a", "b"})
	h.Publish("c1", "a", "x", r.Token)
	time.Sleep(20 * time.Millisecond)
	h.Publish("c1", "b", "x", r.Token) // late by ~20ms
	s := h.Stats()
	if s.TotalSavedMS < 10 {
		t.Fatalf("saved_ms = %d, expected ≥10", s.TotalSavedMS)
	}
}
