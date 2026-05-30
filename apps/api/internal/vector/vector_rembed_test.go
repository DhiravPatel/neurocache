package vector

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestSwapShadowPreservesWritesDuringMigration — entries written/deleted
// AFTER the shadow was built (so they reached only the live map) must be
// carried across the swap by the reconcile step, not silently dropped.
func TestSwapShadowPreservesWritesDuringMigration(t *testing.T) {
	ix := NewIndex(8)
	ix.Upsert("a", "alpha apple", nil)

	// Build a shadow at a new dim from a point-in-time snapshot ({a}).
	sh := NewIndex(16)
	for _, e := range ix.Snapshot() {
		sh.Upsert(e.ID, e.Text, e.Meta)
	}
	ix.AttachShadow(sh)
	ix.SetDualRead(true)

	// During the migration: add b, drop a. These hit only the live map.
	ix.Upsert("b", "beta banana", nil)
	ix.Delete("a")

	if !ix.SwapShadow() {
		t.Fatal("SwapShadow returned false")
	}
	if ix.Dim() != 16 {
		t.Fatalf("post-swap dim = %d, want 16", ix.Dim())
	}
	// b (added mid-migration) must survive the swap into the new space.
	hitsB := ix.Search("beta banana", 0, 0)
	if len(hitsB) == 0 || hitsB[0].ID != "b" {
		t.Fatalf("entry added during migration was lost after swap: %v", hitsB)
	}
	// a (deleted mid-migration) must not reappear from the stale snapshot.
	for _, h := range ix.Search("alpha apple", 0, 0) {
		if h.ID == "a" {
			t.Fatal("entry deleted during migration reappeared after swap")
		}
	}
}

// TestSwapShadowConcurrentSafe stresses the exact interleaving the review
// flagged as a fatal "concurrent map read and map write": dual-read Searches
// and Upserts running while a SwapShadow commits. With the aliasing bug this
// crashes the process; with the fresh-map fix it is clean (run under -race).
func TestSwapShadowConcurrentSafe(t *testing.T) {
	ix := NewIndex(8)
	for i := 0; i < 50; i++ {
		ix.Upsert(fmt.Sprintf("k%d", i), fmt.Sprintf("text number %d here", i), nil)
	}
	sh := NewIndex(16)
	for _, e := range ix.Snapshot() {
		sh.Upsert(e.ID, e.Text, e.Meta)
	}
	ix.AttachShadow(sh)
	ix.SetDualRead(true)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ { // searchers (capture + scan the shadow)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					ix.Search("text number 1 here", 5, 0)
				}
			}
		}()
	}
	for i := 0; i < 2; i++ { // writers (mutate the live map)
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			j := 0
			for {
				select {
				case <-stop:
					return
				default:
					ix.Upsert(fmt.Sprintf("new%d-%d", n, j), "live write", nil)
					j++
				}
			}
		}(i)
	}

	time.Sleep(20 * time.Millisecond)
	ix.SwapShadow() // commit in the middle of live traffic
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestDualReadShadowSwap exercises the REMBED cutover primitives: snapshot →
// build shadow at a new dim → dual-read union → commit.
func TestDualReadShadowSwap(t *testing.T) {
	ix := NewIndex(8)
	ix.Upsert("a", "alpha apple", nil)
	ix.Upsert("b", "beta banana", map[string]string{"k": "v"})

	snap := ix.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	// Snapshot must carry text + meta so the shadow can re-embed.
	var sawMeta bool
	for _, e := range snap {
		if e.Text == "" {
			t.Fatalf("snapshot entry %q lost its text", e.ID)
		}
		if e.ID == "b" && e.Meta["k"] == "v" {
			sawMeta = true
		}
	}
	if !sawMeta {
		t.Fatal("snapshot dropped meta")
	}

	shadow := NewIndex(16)
	for _, e := range snap {
		shadow.Upsert(e.ID, e.Text, e.Meta)
	}
	ix.AttachShadow(shadow)

	present, dim, size, dual := ix.ShadowInfo()
	if !present || dim != 16 || size != 2 || dual {
		t.Fatalf("ShadowInfo = (%v,%d,%d,%v), want (true,16,2,false)", present, dim, size, dual)
	}

	ix.SetDualRead(true)
	if _, _, _, dual := ix.ShadowInfo(); !dual {
		t.Fatal("dual-read not enabled")
	}

	// Union search across both spaces — ids must be de-duplicated.
	hits := ix.Search("alpha apple", 0, 0)
	ids := map[string]bool{}
	for _, h := range hits {
		ids[h.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("dual-read returned %d unique ids, want 2", len(ids))
	}

	if !ix.SwapShadow() {
		t.Fatal("SwapShadow returned false")
	}
	if ix.Dim() != 16 {
		t.Fatalf("post-swap dim = %d, want 16", ix.Dim())
	}
	if present, _, _, _ := ix.ShadowInfo(); present {
		t.Fatal("shadow not cleared after swap")
	}
}

// TestDropShadowRollback verifies an aborted migration leaves the live space
// untouched.
func TestDropShadowRollback(t *testing.T) {
	ix := NewIndex(8)
	ix.Upsert("a", "x", nil)
	shadow := NewIndex(16)
	shadow.Upsert("a", "x", nil)
	ix.AttachShadow(shadow)
	ix.SetDualRead(true)

	if !ix.DropShadow() {
		t.Fatal("DropShadow returned false")
	}
	if ix.Dim() != 8 {
		t.Fatalf("post-rollback dim = %d, want 8 (unchanged)", ix.Dim())
	}
	if present, _, _, _ := ix.ShadowInfo(); present {
		t.Fatal("shadow not cleared after rollback")
	}
	// SetDualRead with no shadow must not leave a stale dual-read flag on.
	if ix.DropShadow() {
		t.Fatal("second DropShadow should report nothing staged")
	}
}
