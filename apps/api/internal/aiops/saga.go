package aiops

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Sagas implements the saga pattern: a sequence of steps where each
// step records an optional compensating action. On failure, the
// already-completed compensations are returned in reverse order so
// the caller can run them. Redis Streams gets you a queue, but it
// doesn't model "this work needs an explicit rollback if anything
// downstream fails" — that's why every microservice shop ends up with
// Temporal/Cadence/Camunda. The cache layer is a fine place for the
// state machine when the compensations themselves are NeuroCache
// commands or HTTP calls back to your services.
//
// State machine:
//
//	START → running ──STEP──► running … ──COMPLETE──► completed
//	                  └──FAIL──► compensating ──(emit comps)──► failed
//
// Once a saga is failed or completed it's terminal — further STEPs
// are rejected. The compensations are never executed automatically by
// this manager; FAIL returns them, and the caller dispatches. This
// keeps the saga library free of an opinion about how to talk to your
// downstream — the same machinery works whether the rollback is a
// RESP command, an HTTP DELETE, or a queue message.
type Sagas struct {
	mu    sync.Mutex
	sagas map[string]*saga
}

// SagaState is one of the canonical lifecycle states.
type SagaState string

const (
	SagaRunning      SagaState = "running"
	SagaCompleted    SagaState = "completed"
	SagaFailing      SagaState = "compensating"
	SagaFailed       SagaState = "failed"
)

type saga struct {
	id          string
	state       SagaState
	meta        map[string]string
	steps       []SagaStep
	startedAt   time.Time
	finishedAt  time.Time
	failReason  string
}

// SagaStep is one recorded step. Compensation is the literal command
// (or any opaque token the caller wants — we don't interpret it; on
// FAIL we hand it back). PayloadJSON is the data emitted by the step,
// useful for the compensation to reference (e.g. an order ID).
type SagaStep struct {
	Name         string    `json:"name"`
	PayloadJSON  string    `json:"payload,omitempty"`
	Compensation string    `json:"compensation,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// NewSagas returns an empty registry.
func NewSagas() *Sagas { return &Sagas{sagas: map[string]*saga{}} }

var (
	errSagaUnknown    = errors.New("saga not found")
	errSagaTerminal   = errors.New("saga is in a terminal state")
	errSagaExists     = errors.New("saga already exists")
)

// Start opens a new saga. id must be unique; reusing an id of a
// completed/failed saga is rejected to avoid silently grafting onto
// stale history.
func (s *Sagas) Start(id string, meta map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sagas[id]; ok {
		return errSagaExists
	}
	cp := map[string]string{}
	for k, v := range meta {
		cp[k] = v
	}
	s.sagas[id] = &saga{
		id:        id,
		state:     SagaRunning,
		meta:      cp,
		startedAt: time.Now(),
	}
	return nil
}

// Step appends a step to a running saga. compensation is opaque to us;
// callers typically format it as `cmd args...` and re-issue it via the
// engine on FAIL. Empty compensation is fine — not every step has a
// rollback (e.g. an idempotent read).
func (s *Sagas) Step(id, name, payloadJSON, compensation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.sagas[id]
	if !ok {
		return errSagaUnknown
	}
	if sg.state != SagaRunning {
		return errSagaTerminal
	}
	sg.steps = append(sg.steps, SagaStep{
		Name:         name,
		PayloadJSON:  payloadJSON,
		Compensation: compensation,
		RecordedAt:   time.Now(),
	})
	return nil
}

// Complete marks the saga successful. Returns errSagaTerminal if the
// saga has already completed or failed.
func (s *Sagas) Complete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.sagas[id]
	if !ok {
		return errSagaUnknown
	}
	if sg.state != SagaRunning {
		return errSagaTerminal
	}
	sg.state = SagaCompleted
	sg.finishedAt = time.Now()
	return nil
}

// Fail transitions the saga to compensating, returns the recorded
// compensations in reverse order (LIFO — newest steps undone first),
// then marks the saga as failed. The caller is responsible for
// actually issuing the returned compensation commands.
func (s *Sagas) Fail(id, reason string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.sagas[id]
	if !ok {
		return nil, errSagaUnknown
	}
	if sg.state != SagaRunning {
		return nil, errSagaTerminal
	}
	sg.state = SagaFailing
	sg.failReason = reason
	out := make([]string, 0, len(sg.steps))
	for i := len(sg.steps) - 1; i >= 0; i-- {
		c := sg.steps[i].Compensation
		if c == "" {
			continue
		}
		out = append(out, c)
	}
	sg.state = SagaFailed
	sg.finishedAt = time.Now()
	return out, nil
}

// SagaSnapshot is the outward-facing record returned by Status / List.
type SagaSnapshot struct {
	ID         string            `json:"id"`
	State      SagaState         `json:"state"`
	Steps      []SagaStep        `json:"steps"`
	Meta       map[string]string `json:"meta,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at,omitempty"`
	FailReason string            `json:"fail_reason,omitempty"`
}

// Status returns the current snapshot for a saga.
func (s *Sagas) Status(id string) (SagaSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.sagas[id]
	if !ok {
		return SagaSnapshot{}, false
	}
	return s.snapshot(sg), true
}

// List returns every known saga, optionally filtered by state. The
// returned slice is sorted by id for deterministic output (callers like
// dashboards rely on stable ordering).
func (s *Sagas) List(stateFilter SagaState) []SagaSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SagaSnapshot, 0, len(s.sagas))
	for _, sg := range s.sagas {
		if stateFilter != "" && sg.state != stateFilter {
			continue
		}
		out = append(out, s.snapshot(sg))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Forget drops a saga from the registry.
func (s *Sagas) Forget(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sagas[id]
	delete(s.sagas, id)
	return ok
}

// SagaStats rolls up the registry.
type SagaStats struct {
	Total        int `json:"total"`
	Running      int `json:"running"`
	Completed    int `json:"completed"`
	Compensating int `json:"compensating"`
	Failed       int `json:"failed"`
}

// Stats aggregates state counts.
func (s *Sagas) Stats() SagaStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := SagaStats{Total: len(s.sagas)}
	for _, sg := range s.sagas {
		switch sg.state {
		case SagaRunning:
			st.Running++
		case SagaCompleted:
			st.Completed++
		case SagaFailing:
			st.Compensating++
		case SagaFailed:
			st.Failed++
		}
	}
	return st
}

// snapshot copies a saga out under the lock. We deep-copy the slice
// and meta map so callers can mutate the result without touching
// engine state.
func (s *Sagas) snapshot(sg *saga) SagaSnapshot {
	steps := make([]SagaStep, len(sg.steps))
	copy(steps, sg.steps)
	var meta map[string]string
	if len(sg.meta) > 0 {
		meta = make(map[string]string, len(sg.meta))
		for k, v := range sg.meta {
			meta[k] = v
		}
	}
	return SagaSnapshot{
		ID:         sg.id,
		State:      sg.state,
		Steps:      steps,
		Meta:       meta,
		StartedAt:  sg.startedAt,
		FinishedAt: sg.finishedAt,
		FailReason: sg.failReason,
	}
}

// SagaErrCode classifies the typed errors so the RESP handler can map
// them to canonical Redis error kinds (NOSAGA / TERMINAL / EXISTS).
func SagaErrCode(err error) string {
	switch err {
	case errSagaUnknown:
		return "NOSAGA"
	case errSagaTerminal:
		return "TERMINAL"
	case errSagaExists:
		return "EXISTS"
	}
	return ""
}
