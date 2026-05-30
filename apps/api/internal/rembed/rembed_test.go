package rembed

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeTarget is a controllable rembed.Target for the FSM tests.
type fakeTarget struct {
	name      string
	count     int
	dim       int
	dual      bool
	failStage bool

	mu         sync.Mutex
	staged     bool
	swapped    bool
	rolledBack bool
	stageDim   int
}

func (f *fakeTarget) Name() string           { return f.name }
func (f *fakeTarget) Count() int             { return f.count }
func (f *fakeTarget) Bytes() int64           { return int64(f.count) * 4 }
func (f *fakeTarget) Dim() int               { return f.dim }
func (f *fakeTarget) SupportsDualRead() bool { return f.dual }

func (f *fakeTarget) Stage(dim, batch int, dualRead bool, progress func(done, total int), cancel <-chan struct{}) error {
	if f.failStage {
		return errors.New("boom")
	}
	for i := 0; i < f.count; i++ {
		select {
		case <-cancel:
			return errors.New("cancelled")
		default:
		}
		if progress != nil {
			progress(i+1, f.count)
		}
	}
	if progress != nil && f.count == 0 {
		progress(0, 0)
	}
	f.mu.Lock()
	f.staged = true
	f.stageDim = dim
	f.mu.Unlock()
	return nil
}

func (f *fakeTarget) Staged() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.staged
}

func (f *fakeTarget) Swap() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.staged {
		return errors.New("nothing staged")
	}
	f.swapped = true
	f.staged = false
	f.dim = f.stageDim
	return nil
}

func (f *fakeTarget) Rollback() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rolledBack = true
	f.staged = false
	return nil
}

// waitState polls a job until it reaches one of want or the deadline passes.
func waitState(t *testing.T, r *Rembedder, id string, want ...State) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := r.Progress(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		for _, w := range want {
			if p.State == w {
				return p.State
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	p, _ := r.Progress(id)
	t.Fatalf("job %s stuck in %s, wanted %v", id, p.State, want)
	return ""
}

func TestRembedPlanAndScope(t *testing.T) {
	r := New()
	r.RegisterTarget(&fakeTarget{name: "semantic", count: 3, dim: 8, dual: true})
	r.RegisterTarget(&fakeTarget{name: "memory", count: 5, dim: 8, dual: true})

	plan, err := r.Plan("all", 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 2 || plan.TotalCount != 8 {
		t.Fatalf("plan = %+v, want 2 targets total 8", plan)
	}

	if _, err := r.Plan("nope", 16); err == nil {
		t.Fatal("unknown scope accepted")
	}
	single, err := r.Plan("memory", 16)
	if err != nil || len(single.Targets) != 1 || single.TotalCount != 5 {
		t.Fatalf("single-scope plan = %+v err=%v", single, err)
	}
}

func TestRembedStartSwap(t *testing.T) {
	r := New()
	sem := &fakeTarget{name: "semantic", count: 4, dim: 8, dual: true}
	mem := &fakeTarget{name: "memory", count: 2, dim: 8, dual: true}
	r.RegisterTarget(sem)
	r.RegisterTarget(mem)

	id, err := r.Start("all", 16, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, r, id, StateStaged)

	p, _ := r.Progress(id)
	if p.Done != p.Total || p.Total != 6 {
		t.Fatalf("progress done=%d total=%d, want 6/6", p.Done, p.Total)
	}

	if err := r.Swap(id); err != nil {
		t.Fatal(err)
	}
	st, _ := r.Status(id)
	if st.State != StateSwapped {
		t.Fatalf("post-swap state = %s", st.State)
	}
	if !sem.swapped || sem.dim != 16 || !mem.swapped {
		t.Fatalf("targets not swapped: sem=%+v mem=%+v", sem, mem)
	}
	// Swapping a non-staged job is rejected.
	if err := r.Swap(id); err == nil {
		t.Fatal("double swap accepted")
	}
}

func TestRembedRollbackFromStaged(t *testing.T) {
	r := New()
	sem := &fakeTarget{name: "semantic", count: 3, dim: 8, dual: true}
	r.RegisterTarget(sem)

	id, _ := r.Start("semantic", 32, 10, false)
	waitState(t, r, id, StateStaged)

	if err := r.Rollback(id); err != nil {
		t.Fatal(err)
	}
	st, _ := r.Status(id)
	if st.State != StateRolledBack {
		t.Fatalf("state = %s, want rolled_back", st.State)
	}
	if !sem.rolledBack || sem.swapped {
		t.Fatalf("target not rolled back: %+v", sem)
	}
}

func TestRembedStartFailUnwinds(t *testing.T) {
	r := New()
	ok := &fakeTarget{name: "semantic", count: 2, dim: 8, dual: true}
	bad := &fakeTarget{name: "memory", count: 2, dim: 8, dual: true, failStage: true}
	r.RegisterTarget(ok)
	r.RegisterTarget(bad)

	id, _ := r.Start("all", 16, 10, true)
	waitState(t, r, id, StateFailed)

	st, _ := r.Status(id)
	if st.State != StateFailed || st.Err == "" {
		t.Fatalf("state = %s err=%q, want failed with message", st.State, st.Err)
	}
	// The first target staged then must be unwound; the failing one never swaps.
	if !ok.rolledBack || ok.swapped {
		t.Fatalf("staged target not unwound on failure: %+v", ok)
	}
}

func TestRembedStats(t *testing.T) {
	r := New()
	r.RegisterTarget(&fakeTarget{name: "semantic", count: 1, dim: 8, dual: true})
	id, _ := r.Start("semantic", 16, 10, false)
	waitState(t, r, id, StateStaged)

	s := r.Stats()
	if s.Targets != 1 || s.Jobs != 1 || s.TotalJobs != 1 || s.Active != 1 {
		t.Fatalf("stats = %+v, want targets=1 jobs=1 total=1 active=1 (staged is in flight)", s)
	}
	_ = r.Swap(id)
	if s := r.Stats(); s.Active != 0 {
		t.Fatalf("active = %d after swap, want 0", s.Active)
	}
}
