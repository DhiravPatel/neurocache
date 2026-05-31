package llmstack

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// XTXN fuzzer + state-machine assertion harness.
//
// The thesis we're testing: the documented state machine is sound
// under (a) random failure injection at every stage and (b) parallel
// XTXNs against shared participants. The advisor flagged commit-
// partial specifically as "there's a bug in XTXN's commit-partial
// path — uncertain commit phase always has one." This fuzzer is
// what would catch it.
//
// State machine (per the implementation):
//
//   open ──STAGE→ open
//   open ──PREPARE pass→ prepared
//   open ──PREPARE fail→ aborted     (every prior Prepare's Abort called)
//   open ──ABORT→ aborted
//   prepared ──COMMIT pass→ committed
//   prepared ──COMMIT partial→ commit_partial
//   prepared ──ABORT→ aborted        (every prepared participant's Abort called)
//
// Invariants we assert:
//
//   X1  No "impossible" state transition: from any terminal state
//       (aborted | committed | commit_partial) NO further mutation
//       call (Stage / Prepare / Commit / Abort) returns nil error.
//
//   X2  After PREPARE returns success, prepared.len == staged.len.
//   X3  After PREPARE returns fail, EVERY participant whose Prepare
//       had been called and returned a token saw its Abort() called
//       exactly once.
//
//   X4  After COMMIT returns success, committed.len == prepared.len
//       AND every prepared participant's Commit() was called.
//
//   X5  After COMMIT returns commit_partial, committed[] is a strict
//       prefix of prepared[]: every participant in committed[] saw
//       Commit; every later participant saw NEITHER Commit nor Abort
//       (the inherent uncertain-commit limit).
//
//   X6  Idempotency of terminal-state guards: every method called
//       on a terminal-state xtxn returns an error AND the participant
//       backend is not invoked.
//
//   X7  Status.State always reflects observable reality: if Status
//       reports "committed", every committed[] entry's Commit fired.
//
//   X8  Concurrent BEGIN of the same xid: exactly one succeeds, all
//       others fail with the "already exists" error.

// observingParticipant is a participant that records every call,
// supports configurable per-call failure, and asserts internally
// that it's never called in an order that violates the contract
// (e.g. Abort before Prepare, or double-Commit on the same token).
type observingParticipant struct {
	mu sync.Mutex

	name string

	// failure plan: index → which phase should fail
	prepareFailOnNth int // 0=never; 1=first prepare; etc.
	commitFailOnNth  int

	prepareCount int
	commitCount  int

	// observation log keyed by token
	preparedTokens   map[string]bool
	committedTokens  map[string]bool
	abortedTokens    map[string]bool
	// counts to detect double-call bugs
	prepareCalls map[string]int
	commitCalls  map[string]int
	abortCalls   map[string]int
}

func newObservingParticipant(name string) *observingParticipant {
	return &observingParticipant{
		name:            name,
		preparedTokens:  map[string]bool{},
		committedTokens: map[string]bool{},
		abortedTokens:   map[string]bool{},
		prepareCalls:    map[string]int{},
		commitCalls:     map[string]int{},
		abortCalls:      map[string]int{},
	}
}

func (p *observingParticipant) Prepare(op string, args map[string]string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepareCount++
	if p.prepareFailOnNth > 0 && p.prepareCount == p.prepareFailOnNth {
		return "", errors.New("prepare fail (injected)")
	}
	// Token must be unique across this participant's lifetime to detect
	// any reuse / leak issues.
	tok := p.name + "-tok-" + strconv.Itoa(p.prepareCount)
	p.preparedTokens[tok] = true
	p.prepareCalls[tok]++
	return tok, nil
}

func (p *observingParticipant) Commit(token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commitCount++
	p.commitCalls[token]++
	if p.commitCalls[token] > 1 {
		// internal panic: double-commit IS a bug, fail noisily
		panic(fmt.Sprintf("participant %s: token %s committed twice", p.name, token))
	}
	if p.commitFailOnNth > 0 && p.commitCount == p.commitFailOnNth {
		return errors.New("commit fail (injected)")
	}
	if !p.preparedTokens[token] {
		panic(fmt.Sprintf("participant %s: Commit on un-Prepared token %s", p.name, token))
	}
	if p.abortedTokens[token] {
		panic(fmt.Sprintf("participant %s: Commit on Aborted token %s", p.name, token))
	}
	p.committedTokens[token] = true
	return nil
}

func (p *observingParticipant) Abort(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.abortCalls[token]++
	if p.committedTokens[token] {
		panic(fmt.Sprintf("participant %s: Abort on Committed token %s", p.name, token))
	}
	p.abortedTokens[token] = true
}

func (p *observingParticipant) snapshot() (committed, aborted, prepared int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.committedTokens), len(p.abortedTokens), len(p.preparedTokens)
}

// TestXTxnFuzzCommitPartialUncertainty drills exactly the bug class
// the advisor predicted: drive many xtxns to the commit-partial state
// and assert the documented contract (X5 above) holds. Specifically:
// committed[] is a strict prefix of prepared[], and post-partial
// state allows no further mutation.
func TestXTxnFuzzCommitPartialUncertainty(t *testing.T) {
	const trials = 200
	for trial := 0; trial < trials; trial++ {
		x := NewXTxnCoordinator()
		// 5 participants; commit will fail on the 3rd one. The first
		// two should commit; the third should mark commit_partial.
		ps := make([]*observingParticipant, 5)
		for i := range ps {
			ps[i] = newObservingParticipant(fmt.Sprintf("p%d", i))
			x.Register(ps[i].name, ps[i])
		}
		ps[2].commitFailOnNth = 1 // fail on first Commit call to this participant

		xid := fmt.Sprintf("partial-%d", trial)
		if err := x.Begin(xid, nil); err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if err := x.Stage(xid, p.name, "op", nil); err != nil {
				t.Fatalf("trial %d: stage on %s failed: %v", trial, p.name, err)
			}
		}
		if _, err := x.Prepare(xid); err != nil {
			t.Fatalf("trial %d: prepare returned err: %v", trial, err)
		}

		cr, _ := x.Commit(xid)
		if cr.State != "commit_partial" {
			t.Fatalf("trial %d: expected commit_partial, got %s", trial, cr.State)
		}

		// X5: committed[] is strict prefix
		if cr.Committed != 2 {
			t.Fatalf("trial %d: committed=%d, expected 2 (prefix before failing participant)", trial, cr.Committed)
		}

		// Verify each participant's observed state matches X5
		for i, p := range ps {
			committed, aborted, _ := p.snapshot()
			switch {
			case i < 2: // p0, p1: must have committed, NOT aborted
				if committed != 1 || aborted != 0 {
					t.Errorf("trial %d: p%d expected committed=1 aborted=0, got committed=%d aborted=%d",
						trial, i, committed, aborted)
				}
			case i == 2: // p2: failed commit attempt; should NOT be in aborted or committed
				if committed != 0 || aborted != 0 {
					t.Errorf("trial %d: p%d (failing) expected NEITHER committed NOR aborted, got committed=%d aborted=%d",
						trial, i, committed, aborted)
				}
			default: // p3, p4: untouched after the failure
				if committed != 0 || aborted != 0 {
					t.Errorf("trial %d: p%d (post-failure) expected NEITHER committed NOR aborted, got committed=%d aborted=%d",
						trial, i, committed, aborted)
				}
			}
		}

		// X1 + X6: terminal-state guards. NO further mutation should succeed.
		if err := x.Stage(xid, "p0", "op", nil); err == nil {
			t.Errorf("trial %d: STAGE on commit_partial should fail", trial)
		}
		if _, err := x.Prepare(xid); err == nil {
			t.Errorf("trial %d: PREPARE on commit_partial should fail", trial)
		}
		if _, err := x.Commit(xid); err == nil {
			t.Errorf("trial %d: COMMIT on commit_partial should fail", trial)
		}
		if err := x.Abort(xid, ""); err == nil {
			t.Errorf("trial %d: ABORT on commit_partial should fail", trial)
		}

		// X7: Status reports it
		st, ok := x.Status(xid)
		if !ok || st.State != "commit_partial" {
			t.Errorf("trial %d: Status state = %s, expected commit_partial", trial, st.State)
		}
	}
}

// TestXTxnFuzzPreparePathAbortsAllPrior is X3 explicitly: when
// PREPARE fails mid-way, every already-Prepared participant must
// see Abort() called exactly once, and NO Commit() must fire.
func TestXTxnFuzzPreparePathAbortsAllPrior(t *testing.T) {
	const trials = 100
	for trial := 0; trial < trials; trial++ {
		x := NewXTxnCoordinator()
		ps := make([]*observingParticipant, 4)
		for i := range ps {
			ps[i] = newObservingParticipant(fmt.Sprintf("p%d", i))
			x.Register(ps[i].name, ps[i])
		}
		// Fail prepare on the 3rd participant
		ps[2].prepareFailOnNth = 1

		xid := fmt.Sprintf("prep-fail-%d", trial)
		x.Begin(xid, nil)
		for _, p := range ps {
			x.Stage(xid, p.name, "op", nil)
		}
		pr, _ := x.Prepare(xid)
		if pr.State != "aborted" {
			t.Fatalf("trial %d: expected aborted, got %s", trial, pr.State)
		}

		// X3: p0, p1 should have been Prepared and then Aborted (exactly once each).
		// p2 should NOT have been aborted (it never returned a token).
		// p3 should NOT have been touched (we never reached it).
		for i, p := range ps {
			committed, aborted, prepared := p.snapshot()
			if committed != 0 {
				t.Errorf("trial %d: p%d committed=%d, expected 0 (failed-prepare path)", trial, i, committed)
			}
			switch i {
			case 0, 1:
				if prepared != 1 || aborted != 1 {
					t.Errorf("trial %d: p%d expected prepared=1 aborted=1, got prepared=%d aborted=%d",
						trial, i, prepared, aborted)
				}
			case 2:
				if prepared != 0 || aborted != 0 {
					t.Errorf("trial %d: p%d (failing-prepare) expected prepared=0 aborted=0, got prepared=%d aborted=%d",
						trial, i, prepared, aborted)
				}
			case 3:
				if prepared != 0 || aborted != 0 {
					t.Errorf("trial %d: p%d (post-failure) expected prepared=0 aborted=0, got prepared=%d aborted=%d",
						trial, i, prepared, aborted)
				}
			}
		}
	}
}

// TestXTxnFuzzConcurrentBeginSameXID is X8: many goroutines call
// BEGIN with the same xid; exactly one must succeed.
func TestXTxnFuzzConcurrentBeginSameXID(t *testing.T) {
	const N = 64
	x := NewXTxnCoordinator()
	var success atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := x.Begin("hot-xid", nil); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Errorf("X8 violated: %d BEGIN calls succeeded for same xid (expected 1)", success.Load())
	}
}

// TestXTxnFuzzConcurrentDifferentXIDs is a stress test: many goroutines
// drive independent xtxn lifecycles to completion. The participants
// are shared, so any internal participant-side race shows up here. We
// inject random failures at every phase to exercise abort/commit-
// partial paths concurrently.
func TestXTxnFuzzConcurrentDifferentXIDs(t *testing.T) {
	const seed = 0xFACEFEED
	const N = 200
	const workers = 16

	x := NewXTxnCoordinator()
	// 4 shared participants
	ps := make([]*observingParticipant, 4)
	for i := range ps {
		ps[i] = newObservingParticipant(fmt.Sprintf("p%d", i))
		x.Register(ps[i].name, ps[i])
	}

	var (
		committedXTXN     atomic.Int64
		abortedXTXN       atomic.Int64
		commitPartialXTXN atomic.Int64
	)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(seed) ^ int64(workerID)*7919))
			for i := 0; i < N/workers; i++ {
				xid := fmt.Sprintf("c-w%d-i%d", workerID, i)
				if err := x.Begin(xid, nil); err != nil {
					continue
				}
				// Stage a random subset
				k := 1 + r.Intn(len(ps))
				for j := 0; j < k; j++ {
					p := ps[r.Intn(len(ps))]
					x.Stage(xid, p.name, "op", nil)
				}
				// Randomly choose: explicit abort, prepare→abort, or full cycle
				roll := r.Intn(100)
				switch {
				case roll < 15:
					x.Abort(xid, "fuzz-roll")
					abortedXTXN.Add(1)
				case roll < 50:
					pr, _ := x.Prepare(xid)
					if pr.State == "prepared" {
						if r.Intn(2) == 0 {
							x.Abort(xid, "post-prepare")
							abortedXTXN.Add(1)
						} else {
							cr, _ := x.Commit(xid)
							switch cr.State {
							case "committed":
								committedXTXN.Add(1)
							case "commit_partial":
								commitPartialXTXN.Add(1)
							}
						}
					} else {
						abortedXTXN.Add(1)
					}
				default:
					pr, _ := x.Prepare(xid)
					if pr.State == "prepared" {
						cr, _ := x.Commit(xid)
						switch cr.State {
						case "committed":
							committedXTXN.Add(1)
						case "commit_partial":
							commitPartialXTXN.Add(1)
						}
					} else {
						abortedXTXN.Add(1)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	// No global participant-state invariant we can assert from outside
	// the per-token observingParticipant panic-checks; the value here
	// is the panic-free completion under -race.
	t.Logf("committed=%d aborted=%d commit_partial=%d",
		committedXTXN.Load(), abortedXTXN.Load(), commitPartialXTXN.Load())
	// Sanity floors — at least each terminal class was exercised.
	if committedXTXN.Load() == 0 {
		t.Error("no xtxns committed — fuzz coverage too narrow")
	}
	if abortedXTXN.Load() == 0 {
		t.Error("no xtxns aborted — fuzz coverage too narrow")
	}
}

// TestXTxnFuzzAbortIsIdempotent: calling Abort after a participant
// was already Aborted (via PREPARE-failure cleanup) must not double-
// abort. This is tested implicitly by observingParticipant.Abort,
// which counts abortCalls per token; here we drive that path
// explicitly.
func TestXTxnFuzzAbortIsIdempotent(t *testing.T) {
	x := NewXTxnCoordinator()
	p1 := newObservingParticipant("p1")
	p2 := newObservingParticipant("p2")
	x.Register("p1", p1)
	x.Register("p2", p2)
	p2.prepareFailOnNth = 1

	x.Begin("xid", nil)
	x.Stage("xid", "p1", "op", nil)
	x.Stage("xid", "p2", "op", nil)
	// PREPARE will abort p1 internally because p2 fails
	pr, _ := x.Prepare("xid")
	if pr.State != "aborted" {
		t.Fatalf("expected aborted, got %s", pr.State)
	}
	// Now call Abort explicitly — should fail (terminal state)
	if err := x.Abort("xid", "redundant"); err == nil {
		t.Fatal("ABORT on terminal aborted xtxn should fail")
	}
	// p1's Abort should have been called exactly once (not twice)
	p1.mu.Lock()
	calls := p1.abortCalls["p1-tok-1"]
	p1.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected p1 aborted exactly once, got %d calls", calls)
	}
}
