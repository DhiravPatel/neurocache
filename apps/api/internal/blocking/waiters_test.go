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
