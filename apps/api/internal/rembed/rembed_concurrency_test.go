package rembed

import (
	"errors"
	"sync"
	"testing"
)

// blockingTarget parks Stage on the cancel channel, so the job stays in
// StateRunning until a Rollback closes cancel — letting tests drive the
// running-state code paths deterministically.
type blockingTarget struct {
	name string
	mu   sync.Mutex
	rb   bool
}

func (b *blockingTarget) Name() string           { return b.name }
func (b *blockingTarget) Count() int             { return 1 }
func (b *blockingTarget) Bytes() int64           { return 1 }
func (b *blockingTarget) Dim() int               { return 8 }
func (b *blockingTarget) SupportsDualRead() bool { return false }
func (b *blockingTarget) Staged() bool           { return false }
func (b *blockingTarget) Swap() error            { return nil }
func (b *blockingTarget) Rollback() error {
	b.mu.Lock()
	b.rb = true
	b.mu.Unlock()
	return nil
}
func (b *blockingTarget) Stage(dim, batch int, dual bool, progress func(d, t int), cancel <-chan struct{}) error {
	<-cancel // block until cancelled
	return errors.New("cancelled")
}

// TestRollbackConcurrentNoPanic fires many concurrent Rollbacks at a running
// job. The cancelOnce guard must make the single close idempotent — the old
// check-then-close double-closed the channel and panicked.
func TestRollbackConcurrentNoPanic(t *testing.T) {
	r := New()
	r.RegisterTarget(&blockingTarget{name: "semantic"})
	id, err := r.Start("semantic", 16, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	// Hammer Rollback from many goroutines while the job is parked running.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Rollback(id) // must never panic
		}()
	}
	wg.Wait()
	waitState(t, r, id, StateCancelled, StateRolledBack, StateFailed)
}

// TestStartRejectsOverlap verifies the reservation guard: a second migration
// on a target with one already in flight is rejected (it would otherwise
// silently overwrite the first job's shadow).
func TestStartRejectsOverlap(t *testing.T) {
	r := New()
	r.RegisterTarget(&blockingTarget{name: "semantic"})

	id1, err := r.Start("semantic", 16, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start("semantic", 32, 1, false); err == nil {
		t.Fatal("overlapping migration on the same target was not rejected")
	}
	// Roll back the first; its reservation must release so a new one succeeds.
	_ = r.Rollback(id1)
	waitState(t, r, id1, StateCancelled, StateRolledBack, StateFailed)
	if _, err := r.Start("semantic", 32, 1, false); err != nil {
		t.Fatalf("start after rollback rejected: %v", err)
	}
}

// TestSwapReleasesReservation confirms a committed job frees its target so a
// follow-up migration can proceed.
func TestSwapReleasesReservation(t *testing.T) {
	r := New()
	ft := &fakeTarget{name: "semantic", count: 2, dim: 8, dual: true}
	r.RegisterTarget(ft)

	id, _ := r.Start("semantic", 16, 10, false)
	waitState(t, r, id, StateStaged)
	if err := r.Swap(id); err != nil {
		t.Fatal(err)
	}
	// Reservation released → a new migration on the same target is allowed.
	if _, err := r.Start("semantic", 32, 10, false); err != nil {
		t.Fatalf("start after swap rejected (reservation leaked): %v", err)
	}
}
