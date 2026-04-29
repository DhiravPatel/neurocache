package aiops

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// EventLog is an append-only event log with declarative materialized
// projections. Lightweight CQRS without Kafka — each `EVENT.APPEND
// stream {json}` adds an event; projections automatically update from
// every append (sum / count / max / latest by key).
//
// We store events as raw JSON strings so callers control the schema.
// Projections are typed: each projection knows its reducer (sum,
// count, max) and the field it operates on.
type EventLog struct {
	mu          sync.RWMutex
	streams     map[string]*eventStream
}

type eventStream struct {
	events      []event
	projections map[string]*projection // proj name → state
}

type event struct {
	Seq       int64                  `json:"seq"`
	Payload   map[string]interface{} `json:"payload"`
	CreatedAt time.Time              `json:"created_at"`
}

// projection is a declarative aggregation over events.
type projection struct {
	Reducer string  // "count" | "sum" | "max" | "latest"
	Field   string  // payload field to operate on (empty = whole event for count)
	GroupBy string  // payload field to group by (empty = global)

	// state — keyed by group-by value; value is reducer-specific.
	values map[string]float64
	latest map[string]map[string]interface{}
}

// NewEventLog returns an empty manager.
func NewEventLog() *EventLog {
	return &EventLog{streams: map[string]*eventStream{}}
}

// Append adds a JSON event to a stream. Returns the new sequence
// number (which is the index in the stream's append log).
func (e *EventLog) Append(stream string, payload []byte) (int64, error) {
	var p map[string]interface{}
	if err := json.Unmarshal(payload, &p); err != nil {
		return 0, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.streams[stream]
	if !ok {
		s = &eventStream{projections: map[string]*projection{}}
		e.streams[stream] = s
	}
	ev := event{Seq: int64(len(s.events) + 1), Payload: p, CreatedAt: time.Now()}
	s.events = append(s.events, ev)
	for _, pr := range s.projections {
		applyEventToProjection(pr, ev)
	}
	return ev.Seq, nil
}

// Project declares a materialized projection over a stream.
//
//	reducer  : "count" / "sum" / "max" / "latest"
//	field    : the payload field the reducer operates on; ignored for "count" / "latest"
//	groupBy  : the payload field to group rows by; "" = global aggregate
//
// Re-defining an existing projection rebuilds it from the stream's
// existing events — useful when changing reducers.
func (e *EventLog) Project(stream, name, reducer, field, groupBy string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.streams[stream]
	if !ok {
		s = &eventStream{projections: map[string]*projection{}}
		e.streams[stream] = s
	}
	pr := &projection{
		Reducer: reducer,
		Field:   field,
		GroupBy: groupBy,
		values:  map[string]float64{},
		latest:  map[string]map[string]interface{}{},
	}
	for _, ev := range s.events {
		applyEventToProjection(pr, ev)
	}
	s.projections[name] = pr
}

// Read returns the projection's current key/value state.
func (e *EventLog) Read(stream, projectionName string) (map[string]interface{}, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.streams[stream]
	if !ok {
		return nil, false
	}
	pr, ok := s.projections[projectionName]
	if !ok {
		return nil, false
	}
	out := map[string]interface{}{}
	if pr.Reducer == "latest" {
		for k, v := range pr.latest {
			out[k] = v
		}
		return out, true
	}
	for k, v := range pr.values {
		out[k] = v
	}
	return out, true
}

// Range returns events in [start, end] (inclusive, 1-based seq numbers).
// end <= 0 means "to the end".
func (e *EventLog) Range(stream string, start, end int64) []map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.streams[stream]
	if !ok {
		return nil
	}
	if start < 1 {
		start = 1
	}
	if end <= 0 || end > int64(len(s.events)) {
		end = int64(len(s.events))
	}
	out := []map[string]interface{}{}
	for i := start - 1; i < end; i++ {
		ev := s.events[i]
		out = append(out, map[string]interface{}{
			"seq":        ev.Seq,
			"payload":    ev.Payload,
			"created_at": ev.CreatedAt.Unix(),
		})
	}
	return out
}

// Len returns the number of events in a stream.
func (e *EventLog) Len(stream string) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.streams[stream]
	if !ok {
		return 0
	}
	return len(s.events)
}

// applyEventToProjection updates a projection's state from one event.
func applyEventToProjection(pr *projection, ev event) {
	groupKey := "_total"
	if pr.GroupBy != "" {
		if v, ok := ev.Payload[pr.GroupBy]; ok {
			groupKey = toString(v)
		}
	}
	switch pr.Reducer {
	case "count":
		pr.values[groupKey]++
	case "sum":
		if v, ok := ev.Payload[pr.Field]; ok {
			pr.values[groupKey] += toFloat(v)
		}
	case "max":
		if v, ok := ev.Payload[pr.Field]; ok {
			f := toFloat(v)
			if cur, exists := pr.values[groupKey]; !exists || f > cur {
				pr.values[groupKey] = f
			}
		}
	case "latest":
		pr.latest[groupKey] = ev.Payload
	}
}

func toFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func toString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}
