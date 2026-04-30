package aiops

import (
	"sort"
	"sync"
	"time"
)

// Tracer is in-memory distributed tracing for agentic workflows.
// Standard OpenTelemetry collectors are overkill for the "I want to
// see why my agent took 12 seconds to plan a sandwich" case. With
// TRACE.* you can record spans inline and inspect the timeline
// without an external collector.
//
// Every trace is a list of spans keyed by trace_id. Spans have a
// parent_id that reconstructs the tree at query time. Spans are
// timestamped at creation; users can flush an end-time later.
type Tracer struct {
	mu     sync.RWMutex
	traces map[string]*trace
	cap    int // max spans per trace (default 10k); after that, oldest dropped
}

type trace struct {
	spans     []*Span
	createdAt time.Time
	updatedAt time.Time
}

// Span is one named operation inside a trace.
type Span struct {
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Name       string            `json:"name"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at,omitempty"`
	DurationMs int64             `json:"duration_ms"`
	Status     string            `json:"status,omitempty"` // "" | "ok" | "error"
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// NewTracer returns an empty tracer.
func NewTracer() *Tracer {
	return &Tracer{
		traces: map[string]*trace{},
		cap:    10_000,
	}
}

// SetCap adjusts the per-trace span cap.
func (t *Tracer) SetCap(n int) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.cap = n
	t.mu.Unlock()
}

// Start records a span begin. Returns the SpanID. parent_id may be
// empty for root spans.
func (t *Tracer) Start(traceID, spanID, parentID, name string, attrs map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, ok := t.traces[traceID]
	if !ok {
		tr = &trace{createdAt: time.Now()}
		t.traces[traceID] = tr
	}
	tr.updatedAt = time.Now()
	tr.spans = append(tr.spans, &Span{
		TraceID:   traceID,
		SpanID:    spanID,
		ParentID:  parentID,
		Name:      name,
		StartedAt: time.Now(),
		Attrs:     cloneAttrs(attrs),
	})
	if len(tr.spans) > t.cap {
		tr.spans = tr.spans[len(tr.spans)-t.cap:]
	}
}

// End closes a span with the given status and computes its duration.
// Silently no-ops on unknown trace_id / span_id.
func (t *Tracer) End(traceID, spanID, status string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, ok := t.traces[traceID]
	if !ok {
		return false
	}
	for _, s := range tr.spans {
		if s.SpanID == spanID {
			s.FinishedAt = time.Now()
			s.DurationMs = s.FinishedAt.Sub(s.StartedAt).Milliseconds()
			if status != "" {
				s.Status = status
			}
			tr.updatedAt = time.Now()
			return true
		}
	}
	return false
}

// Annotate adds attributes to an existing span. Existing keys are
// overwritten.
func (t *Tracer) Annotate(traceID, spanID string, attrs map[string]string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, ok := t.traces[traceID]
	if !ok {
		return false
	}
	for _, s := range tr.spans {
		if s.SpanID == spanID {
			if s.Attrs == nil {
				s.Attrs = map[string]string{}
			}
			for k, v := range attrs {
				s.Attrs[k] = v
			}
			return true
		}
	}
	return false
}

// Get returns every span in a trace, sorted by start time. Use this
// to materialize the trace tree client-side.
func (t *Tracer) Get(traceID string) []Span {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tr, ok := t.traces[traceID]
	if !ok {
		return nil
	}
	out := make([]Span, len(tr.spans))
	for i, s := range tr.spans {
		out[i] = *s
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// List returns every active trace_id, optionally filtered to the most
// recent N (limit=0 → all).
func (t *Tracer) List(limit int) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	type pair struct {
		id string
		at time.Time
	}
	pairs := make([]pair, 0, len(t.traces))
	for id, tr := range t.traces {
		pairs = append(pairs, pair{id, tr.updatedAt})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].at.After(pairs[j].at) })
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.id
	}
	return out
}

// Forget drops a trace.
func (t *Tracer) Forget(traceID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.traces[traceID]
	delete(t.traces, traceID)
	return ok
}

// TraceStats snapshots the manager.
type TraceStats struct {
	Traces      int `json:"traces"`
	TotalSpans  int `json:"total_spans"`
	MaxPerTrace int `json:"max_per_trace"`
}

// Stats returns a snapshot.
func (t *Tracer) Stats() TraceStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	total := 0
	for _, tr := range t.traces {
		total += len(tr.spans)
	}
	return TraceStats{Traces: len(t.traces), TotalSpans: total, MaxPerTrace: t.cap}
}
