package engine

import (
	"sync"
	"testing"
)

// TestBumpKeyGatedOnWatchers locks in the write-path optimization: while
// no connection is WATCHing anything, BumpKey is a no-op (it skips the
// global versions lock entirely). The moment a watcher registers, every
// write must bump the version so WATCH/EXEC can still detect conflicts.
func TestBumpKeyGatedOnWatchers(t *testing.T) {
	e := &Engine{versions: map[string]uint64{}}

	// No watchers → BumpKey must not advance the version.
	for i := 0; i < 100; i++ {
		e.BumpKey("k")
	}
	if v := e.KeyVersion("k"); v != 0 {
		t.Fatalf("with no watchers, version must stay 0, got %d", v)
	}

	// A watcher registers; the WATCH path reads the baseline AFTER raising
	// the gauge. From here every write must bump.
	e.AddWatcher()
	base := e.KeyVersion("k")
	e.BumpKey("k")
	if v := e.KeyVersion("k"); v == base {
		t.Fatalf("with a watcher active, BumpKey must advance the version (base=%d, got=%d)", base, v)
	}
	// This is exactly the EXEC-abort condition: a modification after WATCH
	// makes the observed version differ from the recorded baseline.
	if e.KeyVersion("k") == base {
		t.Fatal("conflict undetectable: version did not change after a watched-window write")
	}

	// Watcher leaves → back to the fast path.
	e.DropWatcher()
	frozen := e.KeyVersion("k")
	for i := 0; i < 100; i++ {
		e.BumpKey("k")
	}
	if v := e.KeyVersion("k"); v != frozen {
		t.Fatalf("after the last watcher leaves, BumpKey must no-op again (frozen=%d, got=%d)", frozen, v)
	}
}

// TestWatcherGaugeConcurrent exercises the gauge under concurrent
// add/drop/bump to prove there's no race and the gate flips cleanly.
// Run with -race.
func TestWatcherGaugeConcurrent(t *testing.T) {
	e := &Engine{versions: map[string]uint64{}}
	var wg sync.WaitGroup
	// Writers hammering BumpKey while watchers come and go.
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				e.BumpKey("hot")
			}
		}()
	}
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				e.AddWatcher()
				_ = e.KeyVersion("hot")
				e.DropWatcher()
			}
		}()
	}
	wg.Wait()
	// Gauge must net to zero once every watcher has dropped.
	if got := e.watchers.Load(); got != 0 {
		t.Fatalf("watcher gauge leaked: %d (want 0)", got)
	}
}
