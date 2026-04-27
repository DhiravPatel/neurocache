package blocking

import (
	"sync"
	"testing"
	"time"
)

func TestNotifyWakesWaiter(t *testing.T) {
	h := NewHub()
	w := h.Register("k1")
	defer w.Cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		h.Notify("k1")
	}()
	key, ok := w.Wait(time.Second)
	if !ok || key != "k1" {
		t.Fatalf("expected wake on k1, got %q ok=%v", key, ok)
	}
	wg.Wait()
}

func TestWaitTimeout(t *testing.T) {
	h := NewHub()
	w := h.Register("idle")
	defer w.Cancel()
	if _, ok := w.Wait(20 * time.Millisecond); ok {
		t.Fatal("expected timeout")
	}
}

func TestNotifyOnlyOneWaiter(t *testing.T) {
	h := NewHub()
	a := h.Register("k")
	b := h.Register("k")
	defer a.Cancel()
	defer b.Cancel()
	h.Notify("k")
	got := 0
	for _, w := range []*Waiter{a, b} {
		if _, ok := w.Wait(50 * time.Millisecond); ok {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("expected exactly one wake, got %d", got)
	}
}

// CLIENT UNBLOCK ... TIMEOUT wakes the waiter and flags it as woken
// from outside (so blocking commands return nil, not loop and re-block).
func TestUnblockTimeout(t *testing.T) {
	h := NewHub()
	w := h.RegisterFor(42, "k")
	defer w.Cancel()
	if h.PendingClient(42) != 1 {
		t.Fatalf("PendingClient = %d", h.PendingClient(42))
	}
	if h.Unblock(42, UnblockTimeout) != 1 {
		t.Fatal("expected to unblock 1 waiter")
	}
	_, ok := w.Wait(100 * time.Millisecond)
	if !ok {
		t.Fatal("waiter should have been woken")
	}
	if !w.UnblockedExternal() {
		t.Fatal("waiter should report external wake")
	}
	if w.UnblockedByError() {
		t.Fatal("waiter should not report error reason for TIMEOUT")
	}
}

// CLIENT UNBLOCK ... ERROR sets the error flag the dispatcher uses to
// emit -UNBLOCKED instead of nil.
func TestUnblockError(t *testing.T) {
	h := NewHub()
	w := h.RegisterFor(7, "k")
	defer w.Cancel()
	h.Unblock(7, UnblockError)
	_, ok := w.Wait(50 * time.Millisecond)
	if !ok || !w.UnblockedExternal() || !w.UnblockedByError() {
		t.Fatalf("error wake flags wrong: ok=%v ext=%v errd=%v",
			ok, w.UnblockedExternal(), w.UnblockedByError())
	}
}

// Unknown client IDs return zero from Unblock.
func TestUnblockUnknownClient(t *testing.T) {
	h := NewHub()
	if got := h.Unblock(999, UnblockTimeout); got != 0 {
		t.Fatalf("unknown client unblock = %d, want 0", got)
	}
}

// Cancel removes the waiter from both the keys and clients indexes
// so a subsequent Unblock is a no-op.
func TestCancelClearsClientIndex(t *testing.T) {
	h := NewHub()
	w := h.RegisterFor(123, "k")
	w.Cancel()
	if h.PendingClient(123) != 0 {
		t.Fatalf("client index not cleared")
	}
	if got := h.Unblock(123, UnblockTimeout); got != 0 {
		t.Fatalf("post-cancel unblock = %d", got)
	}
}
