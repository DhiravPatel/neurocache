// Package rembed orchestrates embedding-recompute migrations across the
// vector-bearing subsystems (semantic / llm / memory / retrieve). The whole
// semantic layer is pinned to one embedder + dimension; the day that changes
// every stored vector becomes silently incomparable to new ones, and nothing
// else in the engine re-embeds them. REMBED is the migration tool for exactly
// that: it snapshots the source texts, rebuilds each space at the target
// dimension OFF the hot path, optionally serves both spaces during cutover
// (dual-read) so retrieval quality never dips, then commits (SWAP) or aborts
// (ROLLBACK) atomically.
//
// This package is pure orchestration: a job state machine plus a Target
// interface. The concrete re-embed work (and the dual-read plumbing) lives in
// engine adapters that implement Target over the real stores — keeping rembed
// free of any dependency on the subsystems it drives.
package rembed

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Target is one re-embeddable subsystem. Implemented by engine adapters.
type Target interface {
	// Name is the stable scope token (e.g. "semantic", "memory").
	Name() string
	// Count is the number of entries that would be re-embedded.
	Count() int
	// Bytes is the approximate in-memory footprint of the live space.
	Bytes() int64
	// Dim is the current (source) embedding dimension.
	Dim() int
	// SupportsDualRead reports whether this target can serve the old and new
	// spaces simultaneously during cutover.
	SupportsDualRead() bool

	// Stage rebuilds the space at dim, re-embedding every entry. progress is
	// invoked with (done,total) as the build advances; dualRead asks for live
	// dual-read during the build (honored only when SupportsDualRead). cancel
	// aborts. On success a replacement is staged, awaiting Swap or Rollback.
	Stage(dim, batch int, dualRead bool, progress func(done, total int), cancel <-chan struct{}) error
	// Staged reports whether this target currently holds a pending (built but
	// not yet committed) replacement — used to pre-validate a swap.
	Staged() bool
	// Swap promotes the staged space to live (commit).
	Swap() error
	// Rollback discards the staged space (abort).
	Rollback() error
}

// State is a job's lifecycle position.
type State string

const (
	StatePending    State = "pending"
	StateRunning    State = "running"
	StateStaged     State = "staged"  // built; awaiting SWAP / ROLLBACK
	StateSwapped    State = "swapped" // committed
	StateRolledBack State = "rolled_back"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

// terminal reports whether no further transition is possible.
func (s State) terminal() bool {
	switch s {
	case StateSwapped, StateRolledBack, StateFailed, StateCancelled:
		return true
	}
	return false
}

// Rembedder is the migration manager.
type Rembedder struct {
	mu      sync.Mutex
	targets map[string]Target
	order   []string
	jobs    map[string]*job
	seq     int

	// reserved guards against two jobs staging the same target concurrently
	// (which would silently overwrite one shadow). Maps target name → owning
	// job id while a job has that target in flight (running or staged);
	// released when the job reaches a terminal state.
	reserved map[string]string

	totalJobs atomic.Int64

	// now is injectable for deterministic tests; defaults to time.Now.
	now func() time.Time
}

// New returns an empty manager.
func New() *Rembedder {
	return &Rembedder{
		targets:  map[string]Target{},
		jobs:     map[string]*job{},
		reserved: map[string]string{},
		now:      time.Now,
	}
}

// SetClock overrides the time source (tests).
func (r *Rembedder) SetClock(now func() time.Time) { r.now = now }

// RegisterTarget adds a target. Call at engine boot, before any job runs.
func (r *Rembedder) RegisterTarget(t Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.targets[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.targets[t.Name()] = t
}

type targetProgress struct {
	target  Target
	name    string
	fromDim int
	total   int
	done    int
	state   string // pending / running / staged / swapped / rolled_back / failed
	err     string
}

type job struct {
	id       string
	scope    string
	toDim    int
	batch    int
	dualRead bool

	mu         sync.Mutex
	state      State
	err        string
	targets    []*targetProgress
	startedAt  time.Time
	endedAt    time.Time // when staging finished (success or fail)
	cancel     chan struct{}
	cancelOnce sync.Once // guards exactly one close(cancel)
}

// ─── scope resolution + PLAN ─────────────────────────────────────────────

// resolveScope maps a scope token to an ordered, de-duplicated target list.
// "all" expands to every registered target; otherwise the scope is a single
// target name or a comma-free single token. Caller holds r.mu.
func (r *Rembedder) resolveScopeLocked(scope string) ([]Target, error) {
	if scope == "" || scope == "all" {
		out := make([]Target, 0, len(r.order))
		for _, n := range r.order {
			out = append(out, r.targets[n])
		}
		if len(out) == 0 {
			return nil, errors.New("no re-embeddable targets registered")
		}
		return out, nil
	}
	t, ok := r.targets[scope]
	if !ok {
		return nil, fmt.Errorf("unknown rembed scope %q (known: %s)", scope, r.knownLocked())
	}
	return []Target{t}, nil
}

func (r *Rembedder) knownLocked() string {
	names := append([]string{"all"}, r.order...)
	return joinComma(names)
}

// PlanTarget is one row of a PLAN.
type PlanTarget struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	Bytes    int64  `json:"bytes"`
	Dim      int    `json:"dim"`
	DualRead bool   `json:"dual_read"`
}

// PlanResult is REMBED.PLAN's return.
type PlanResult struct {
	Scope      string       `json:"scope"`
	ToDim      int          `json:"to_dim"`
	Targets    []PlanTarget `json:"targets"`
	TotalCount int          `json:"total_count"`
	TotalBytes int64        `json:"total_bytes"`
}

// Plan estimates the cost of re-embedding a scope. Read-only.
func (r *Rembedder) Plan(scope string, toDim int) (PlanResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, err := r.resolveScopeLocked(scope)
	if err != nil {
		return PlanResult{}, err
	}
	res := PlanResult{Scope: scope, ToDim: toDim, Targets: make([]PlanTarget, 0, len(ts))}
	for _, t := range ts {
		c := t.Count()
		b := t.Bytes()
		res.Targets = append(res.Targets, PlanTarget{
			Name: t.Name(), Count: c, Bytes: b, Dim: t.Dim(), DualRead: t.SupportsDualRead(),
		})
		res.TotalCount += c
		res.TotalBytes += b
	}
	return res, nil
}

// ─── START ───────────────────────────────────────────────────────────────

// Start kicks off a migration job and returns its id. The re-embed runs in a
// background goroutine; poll Progress / Status. toDim must be positive; when
// 0 the caller should substitute the current dim before calling.
func (r *Rembedder) Start(scope string, toDim, batch int, dualRead bool) (string, error) {
	if toDim <= 0 {
		return "", errors.New("target dim must be positive")
	}
	if batch <= 0 {
		batch = 512
	}
	r.mu.Lock()
	ts, err := r.resolveScopeLocked(scope)
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	// Refuse to start if any target already has an in-flight migration — two
	// jobs staging the same target would silently overwrite one shadow and can
	// commit the wrong data under the loser's job id.
	for _, t := range ts {
		if owner, busy := r.reserved[t.Name()]; busy {
			r.mu.Unlock()
			return "", fmt.Errorf("target %q already has an in-flight migration (job %s); SWAP or ROLLBACK it first", t.Name(), owner)
		}
	}
	r.seq++
	id := fmt.Sprintf("rembed-%d", r.seq)
	for _, t := range ts {
		r.reserved[t.Name()] = id
	}
	tps := make([]*targetProgress, 0, len(ts))
	for _, t := range ts {
		tps = append(tps, &targetProgress{
			target: t, name: t.Name(), fromDim: t.Dim(), total: t.Count(), state: "pending",
		})
	}
	j := &job{
		id: id, scope: scope, toDim: toDim, batch: batch, dualRead: dualRead,
		state: StatePending, targets: tps, startedAt: r.now(), cancel: make(chan struct{}),
	}
	r.jobs[id] = j
	r.mu.Unlock()
	r.totalJobs.Add(1)

	go r.run(j)
	return id, nil
}

// run drives every target's Stage in order. Any failure rolls back the
// targets already staged in this job, so a job is all-or-nothing at swap time.
func (r *Rembedder) run(j *job) {
	j.mu.Lock()
	j.state = StateRunning
	j.mu.Unlock()

	for _, tp := range j.targets {
		j.mu.Lock()
		tp.state = "running"
		j.mu.Unlock()

		err := tp.target.Stage(j.toDim, j.batch, j.dualRead && tp.target.SupportsDualRead(),
			func(done, total int) {
				j.mu.Lock()
				tp.done = done
				if total > 0 {
					tp.total = total
				}
				j.mu.Unlock()
			}, j.cancel)

		j.mu.Lock()
		if err != nil {
			tp.state = "failed"
			tp.err = err.Error()
			j.mu.Unlock()
			r.unwind(j, err.Error())
			return
		}
		tp.state = "staged"
		j.mu.Unlock()
	}

	j.mu.Lock()
	// A Rollback may have closed cancel AFTER the last Stage returned but
	// before we reach here — the per-entry cancel checks inside Stage can't
	// see it. Re-check under j.mu so the cancel observation and the StateStaged
	// transition are mutually exclusive; otherwise the rollback is silently
	// lost and a later SWAP would commit a migration the operator aborted.
	select {
	case <-j.cancel:
		for _, tp := range j.targets {
			if tp.state == "staged" {
				_ = tp.target.Rollback()
				tp.state = "rolled_back"
			}
		}
		j.state = StateCancelled
		j.endedAt = r.now()
		j.mu.Unlock()
		r.release(j)
		return
	default:
	}
	j.state = StateStaged
	j.endedAt = r.now()
	j.mu.Unlock()
	// StateStaged is NOT terminal — the target reservation is held until SWAP
	// or ROLLBACK so no other job can stage the same target meanwhile.
}

// release frees a job's target reservations. Idempotent.
func (r *Rembedder) release(j *job) {
	r.mu.Lock()
	for _, tp := range j.targets {
		if r.reserved[tp.name] == j.id {
			delete(r.reserved, tp.name)
		}
	}
	r.mu.Unlock()
}

// unwind rolls back every staged target after a mid-job failure/cancel and
// marks the job failed (or cancelled if the cancel channel fired).
func (r *Rembedder) unwind(j *job, errMsg string) {
	cancelled := false
	select {
	case <-j.cancel:
		cancelled = true
	default:
	}
	j.mu.Lock()
	for _, tp := range j.targets {
		if tp.state == "staged" {
			_ = tp.target.Rollback()
			tp.state = "rolled_back"
		}
	}
	if cancelled {
		j.state = StateCancelled
	} else {
		j.state = StateFailed
		j.err = errMsg
	}
	j.endedAt = r.now()
	j.mu.Unlock()
	r.release(j)
}

// ─── PROGRESS / STATUS ───────────────────────────────────────────────────

// TargetProgress is one target's live counters.
type TargetProgress struct {
	Name    string `json:"name"`
	FromDim int    `json:"from_dim"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	State   string `json:"state"`
	Err     string `json:"err,omitempty"`
}

// ProgressResult is REMBED.PROGRESS's return.
type ProgressResult struct {
	ID         string           `json:"id"`
	State      State            `json:"state"`
	Scope      string           `json:"scope"`
	ToDim      int              `json:"to_dim"`
	DualRead   bool             `json:"dual_read"`
	Total      int              `json:"total"`
	Done       int              `json:"done"`
	Rps        float64          `json:"rps"`
	EtaSeconds float64          `json:"eta_seconds"`
	Err        string           `json:"err,omitempty"`
	Targets    []TargetProgress `json:"targets"`
}

// Progress returns a job's live progress with rps + eta estimates.
func (r *Rembedder) Progress(id string) (ProgressResult, bool) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return ProgressResult{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	res := ProgressResult{
		ID: j.id, State: j.state, Scope: j.scope, ToDim: j.toDim,
		DualRead: j.dualRead, Err: j.err,
	}
	for _, tp := range j.targets {
		res.Total += tp.total
		res.Done += tp.done
		res.Targets = append(res.Targets, TargetProgress{
			Name: tp.name, FromDim: tp.fromDim, Total: tp.total,
			Done: tp.done, State: tp.state, Err: tp.err,
		})
	}
	end := j.endedAt
	if end.IsZero() {
		end = r.now()
	}
	elapsed := end.Sub(j.startedAt).Seconds()
	if elapsed > 0 && res.Done > 0 {
		res.Rps = float64(res.Done) / elapsed
		if res.Rps > 0 && res.Total > res.Done {
			res.EtaSeconds = float64(res.Total-res.Done) / res.Rps
		}
	}
	return res, true
}

// JobStatus is REMBED.STATUS's return (config + outcome, no per-batch detail).
type JobStatus struct {
	ID         string           `json:"id"`
	State      State            `json:"state"`
	Scope      string           `json:"scope"`
	ToDim      int              `json:"to_dim"`
	Batch      int              `json:"batch"`
	DualRead   bool             `json:"dual_read"`
	Err        string           `json:"err,omitempty"`
	Targets    []TargetProgress `json:"targets"`
}

// Status returns a job's full snapshot.
func (r *Rembedder) Status(id string) (JobStatus, bool) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return JobStatus{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	st := JobStatus{
		ID: j.id, State: j.state, Scope: j.scope, ToDim: j.toDim,
		Batch: j.batch, DualRead: j.dualRead, Err: j.err,
	}
	for _, tp := range j.targets {
		st.Targets = append(st.Targets, TargetProgress{
			Name: tp.name, FromDim: tp.fromDim, Total: tp.total,
			Done: tp.done, State: tp.state, Err: tp.err,
		})
	}
	return st, true
}

// List returns every job id with its state, newest first.
func (r *Rembedder) List() []JobStatus {
	r.mu.Lock()
	js := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		js = append(js, j)
	}
	r.mu.Unlock()
	out := make([]JobStatus, 0, len(js))
	for _, j := range js {
		j.mu.Lock()
		out = append(out, JobStatus{
			ID: j.id, State: j.state, Scope: j.scope, ToDim: j.toDim,
			Batch: j.batch, DualRead: j.dualRead, Err: j.err,
		})
		j.mu.Unlock()
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID > out[k].ID })
	return out
}

// ─── SWAP / ROLLBACK ─────────────────────────────────────────────────────

// Swap commits a staged job: every target's rebuilt space becomes live.
// Only valid from StateStaged.
func (r *Rembedder) Swap(id string) error {
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return errors.New("unknown rembed job")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StateStaged {
		return fmt.Errorf("job %s is %s, can only SWAP a staged job", id, j.state)
	}
	// Pre-validate every target is still staged BEFORE committing any — so a
	// multi-target swap is all-or-nothing and we never leave the engine in a
	// split state (some subsystems at the new dim, some at the old).
	for _, tp := range j.targets {
		if !tp.target.Staged() {
			return fmt.Errorf("job %s: target %s is no longer staged; not swapping (re-stage required)", id, tp.name)
		}
	}
	for _, tp := range j.targets {
		if err := tp.target.Swap(); err != nil {
			return fmt.Errorf("swap failed on target %s: %w", tp.name, err)
		}
		tp.state = "swapped"
	}
	j.state = StateSwapped
	r.release(j)
	return nil
}

// Rollback aborts a job. From StateStaged it discards every shadow; from
// StateRunning it signals cancellation and lets the worker unwind.
func (r *Rembedder) Rollback(id string) error {
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return errors.New("unknown rembed job")
	}
	j.mu.Lock()
	switch j.state {
	case StateStaged:
		for _, tp := range j.targets {
			if tp.state == "staged" {
				_ = tp.target.Rollback()
				tp.state = "rolled_back"
			}
		}
		j.state = StateRolledBack
		j.mu.Unlock()
		r.release(j)
		return nil
	case StateRunning, StatePending:
		j.mu.Unlock()
		// Signal cancel; the worker unwinds staged targets and releases the
		// reservation when it reaches a terminal state. cancelOnce makes the
		// close safe against concurrent rollbacks (a plain check-then-close
		// would double-close and panic).
		j.cancelOnce.Do(func() { close(j.cancel) })
		return nil
	default:
		st := j.state
		j.mu.Unlock()
		return fmt.Errorf("job %s is %s, nothing to roll back", id, st)
	}
}

// ─── STATS ───────────────────────────────────────────────────────────────

// Stats is REMBED.STATS's return.
type Stats struct {
	Targets   int   `json:"targets"`
	Jobs      int   `json:"jobs"`
	TotalJobs int64 `json:"total_jobs"`
	Active    int   `json:"active"` // running or staged (in flight)
}

func (r *Rembedder) Stats() Stats {
	r.mu.Lock()
	s := Stats{Targets: len(r.targets), Jobs: len(r.jobs), TotalJobs: r.totalJobs.Load()}
	js := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		js = append(js, j)
	}
	r.mu.Unlock()
	for _, j := range js {
		j.mu.Lock()
		if !j.state.terminal() {
			s.Active++
		}
		j.mu.Unlock()
	}
	return s
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
