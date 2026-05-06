package resp

// Phase 13 — resilience & coordination primitives that genuinely don't
// exist in OSS Redis: distributed circuit breakers (CIRCUIT.*),
// long-running workflow orchestration with compensation (SAGA.*), and
// conflict-free replicated data types (CRDT.*). State lives in
// `internal/aiops/`; mutations flow through `c.eng.RecordWrite` so
// AOF + replication propagate them like any other write-path command.

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// ── CIRCUIT.* ───────────────────────────────────────────────────────

// circuitCmd dispatches the CIRCUIT.<sub> family. Subcommands:
//
//	CIRCUIT.CONFIG service [THRESHOLD f] [WINDOW n] [MIN n] [COOLDOWN ms] [HALFOPEN n]
//	CIRCUIT.RECORD service ok|fail
//	CIRCUIT.CHECK service
//	CIRCUIT.STATE service
//	CIRCUIT.TRIP service [REASON r]
//	CIRCUIT.RESET service
//	CIRCUIT.FORGET service
//	CIRCUIT.LIST
//	CIRCUIT.STATS
func (c *conn) circuitCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "CIRCUIT.CONFIG service [THRESHOLD f] [WINDOW n] [MIN n] [COOLDOWN ms] [HALFOPEN n]")
			return
		}
		cfg := aiops.CircuitConfig{}
		for i := 1; i+1 < len(args); i += 2 {
			tok := strings.ToUpper(args[i])
			val := args[i+1]
			switch tok {
			case "THRESHOLD":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f < 0 {
					writeError(c.bw, "ERR THRESHOLD must be a non-negative float")
					return
				}
				cfg.Threshold = f
			case "WINDOW":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "ERR WINDOW must be a positive integer")
					return
				}
				cfg.WindowSize = n
			case "MIN":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "ERR MIN must be a positive integer")
					return
				}
				cfg.MinSamples = n
			case "COOLDOWN":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "ERR COOLDOWN must be a non-negative integer (ms)")
					return
				}
				cfg.Cooldown = time.Duration(n) * time.Millisecond
			case "HALFOPEN":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "ERR HALFOPEN must be a positive integer")
					return
				}
				cfg.HalfOpenMax = n
			}
		}
		applied := c.eng.Circuits.Configure(args[0], cfg)
		c.eng.RecordWrite("CIRCUIT.CONFIG", args)
		writeValue(c.bw, []any{
			"service", args[0],
			"threshold", applied.Threshold,
			"window_size", int64(applied.WindowSize),
			"min_samples", int64(applied.MinSamples),
			"cooldown_ms", applied.Cooldown.Milliseconds(),
			"half_open_max", int64(applied.HalfOpenMax),
		})
	case "RECORD":
		if len(args) != 2 {
			writeError(c.bw, "CIRCUIT.RECORD service ok|fail")
			return
		}
		var ok bool
		switch strings.ToLower(args[1]) {
		case "ok", "success", "1", "true":
			ok = true
		case "fail", "failure", "0", "false":
			ok = false
		default:
			writeError(c.bw, "ERR outcome must be ok|fail")
			return
		}
		state := c.eng.Circuits.Record(args[0], ok)
		c.eng.RecordWrite("CIRCUIT.RECORD", args)
		writeBulk(c.bw, string(state))
	case "CHECK":
		if len(args) != 1 {
			writeError(c.bw, "CIRCUIT.CHECK service")
			return
		}
		allowed, state := c.eng.Circuits.Check(args[0])
		// CHECK can transition the breaker into HALFOPEN and reserve a
		// probe slot — that's a state mutation, so it goes through the
		// write path even though semantically it feels like a read.
		c.eng.RecordWrite("CIRCUIT.CHECK", args)
		v := int64(0)
		if allowed {
			v = 1
		}
		writeValue(c.bw, []any{
			"allowed", v,
			"state", string(state),
		})
	case "STATE":
		if len(args) != 1 {
			writeError(c.bw, "CIRCUIT.STATE service")
			return
		}
		snap, ok := c.eng.Circuits.State(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, circuitSnapshotToValue(snap))
	case "TRIP":
		if len(args) < 1 {
			writeError(c.bw, "CIRCUIT.TRIP service [REASON r]")
			return
		}
		reason := ""
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "REASON" {
				reason = args[i+1]
			}
		}
		state := c.eng.Circuits.Trip(args[0], reason)
		c.eng.RecordWrite("CIRCUIT.TRIP", args)
		writeBulk(c.bw, string(state))
	case "RESET":
		if len(args) != 1 {
			writeError(c.bw, "CIRCUIT.RESET service")
			return
		}
		ok := c.eng.Circuits.Reset(args[0])
		if ok {
			c.eng.RecordWrite("CIRCUIT.RESET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "CIRCUIT.FORGET service")
			return
		}
		ok := c.eng.Circuits.Forget(args[0])
		if ok {
			c.eng.RecordWrite("CIRCUIT.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "LIST":
		snaps := c.eng.Circuits.List()
		out := make([]any, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, circuitSnapshotToValue(s))
		}
		writeValue(c.bw, out)
	case "STATS":
		st := c.eng.Circuits.Stats()
		writeValue(c.bw, []any{
			"services", int64(st.Services),
			"open", int64(st.Open),
			"half_open", int64(st.HalfOpen),
			"closed", int64(st.Closed),
			"total_requests", st.TotalRequests,
			"total_failures", st.TotalFailures,
			"total_rejected", st.TotalRejected,
		})
	default:
		writeError(c.bw, "Unknown CIRCUIT subcommand "+sub)
	}
}

func circuitSnapshotToValue(s aiops.CircuitSnapshot) []any {
	openedAt := int64(0)
	if !s.OpenedAt.IsZero() {
		openedAt = s.OpenedAt.UnixMilli()
	}
	lastTrip := int64(0)
	if !s.LastTrip.IsZero() {
		lastTrip = s.LastTrip.UnixMilli()
	}
	return []any{
		"service", s.Service,
		"state", string(s.State),
		"threshold", s.Threshold,
		"window_size", int64(s.WindowSize),
		"min_samples", int64(s.MinSamples),
		"cooldown_ms", s.Cooldown.Milliseconds(),
		"half_open_max", int64(s.HalfOpenMax),
		"failure_rate", s.FailureRate,
		"filled", int64(s.Filled),
		"opened_at", openedAt,
		"cooldown_left_ms", s.CooldownLeft.Milliseconds(),
		"probe_allowed", int64(s.ProbeAllowed),
		"total_requests", s.TotalRequests,
		"total_failures", s.TotalFailures,
		"total_rejected", s.TotalRejected,
		"trip_count", s.TripCount,
		"last_reason", s.LastReason,
		"last_trip", lastTrip,
	}
}

// ── SAGA.* ──────────────────────────────────────────────────────────

// sagaCmd dispatches the SAGA.<sub> family. Subcommands:
//
//	SAGA.START id [META k v ...]
//	SAGA.STEP id name [PAYLOAD json] [COMPENSATION cmd]
//	SAGA.COMPLETE id
//	SAGA.FAIL id [REASON r]
//	SAGA.STATUS id
//	SAGA.LIST [STATE running|completed|compensating|failed]
//	SAGA.FORGET id
//	SAGA.STATS
func (c *conn) sagaCmd(sub string, args []string) {
	switch sub {
	case "START":
		if len(args) < 1 {
			writeError(c.bw, "SAGA.START id [META k v ...]")
			return
		}
		meta := map[string]string{}
		i := 1
		if i < len(args) && strings.ToUpper(args[i]) == "META" {
			j := i + 1
			for j+1 < len(args) {
				meta[args[j]] = args[j+1]
				j += 2
			}
		}
		if err := c.eng.Sagas.Start(args[0], meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("SAGA.START", args)
		writeSimple(c.bw, "OK")
	case "STEP":
		if len(args) < 2 {
			writeError(c.bw, "SAGA.STEP id name [PAYLOAD json] [COMPENSATION cmd]")
			return
		}
		payload := ""
		comp := ""
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "PAYLOAD":
				payload = args[i+1]
			case "COMPENSATION":
				comp = args[i+1]
			}
		}
		if err := c.eng.Sagas.Step(args[0], args[1], payload, comp); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("SAGA.STEP", args)
		writeSimple(c.bw, "OK")
	case "COMPLETE":
		if len(args) != 1 {
			writeError(c.bw, "SAGA.COMPLETE id")
			return
		}
		if err := c.eng.Sagas.Complete(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("SAGA.COMPLETE", args)
		writeSimple(c.bw, "OK")
	case "FAIL":
		if len(args) < 1 {
			writeError(c.bw, "SAGA.FAIL id [REASON r]")
			return
		}
		reason := ""
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "REASON" {
				reason = args[i+1]
			}
		}
		comps, err := c.eng.Sagas.Fail(args[0], reason)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("SAGA.FAIL", args)
		// Return the compensations the caller must run, in the order
		// they should be issued (LIFO of completed steps).
		writeArray(c.bw, comps)
	case "STATUS":
		if len(args) != 1 {
			writeError(c.bw, "SAGA.STATUS id")
			return
		}
		snap, ok := c.eng.Sagas.Status(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, sagaSnapshotToValue(snap))
	case "LIST":
		var filter aiops.SagaState
		for i := 0; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "STATE" {
				filter = aiops.SagaState(strings.ToLower(args[i+1]))
			}
		}
		snaps := c.eng.Sagas.List(filter)
		out := make([]any, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, sagaSnapshotToValue(s))
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "SAGA.FORGET id")
			return
		}
		ok := c.eng.Sagas.Forget(args[0])
		if ok {
			c.eng.RecordWrite("SAGA.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		st := c.eng.Sagas.Stats()
		writeValue(c.bw, []any{
			"total", int64(st.Total),
			"running", int64(st.Running),
			"completed", int64(st.Completed),
			"compensating", int64(st.Compensating),
			"failed", int64(st.Failed),
		})
	default:
		writeError(c.bw, "Unknown SAGA subcommand "+sub)
	}
}

func sagaSnapshotToValue(s aiops.SagaSnapshot) []any {
	steps := make([]any, 0, len(s.Steps))
	for _, st := range s.Steps {
		steps = append(steps, []any{
			"name", st.Name,
			"payload", st.PayloadJSON,
			"compensation", st.Compensation,
			"recorded_at", st.RecordedAt.UnixMilli(),
		})
	}
	meta := make([]any, 0, len(s.Meta)*2)
	for k, v := range s.Meta {
		meta = append(meta, k, v)
	}
	finished := int64(0)
	if !s.FinishedAt.IsZero() {
		finished = s.FinishedAt.UnixMilli()
	}
	return []any{
		"id", s.ID,
		"state", string(s.State),
		"steps", steps,
		"meta", meta,
		"started_at", s.StartedAt.UnixMilli(),
		"finished_at", finished,
		"fail_reason", s.FailReason,
	}
}

// ── CRDT.* ──────────────────────────────────────────────────────────

// crdtCmd dispatches the CRDT.<sub> family. Subcommands:
//
//	CRDT.GINCR key actor [delta]                        # G-Counter
//	CRDT.GVALUE key
//	CRDT.PNINCR key actor delta                          # PN-Counter
//	CRDT.PNVALUE key
//	CRDT.SADD key actor member                           # OR-Set
//	CRDT.SREM key member
//	CRDT.SMEMBERS key
//	CRDT.SISMEMBER key member
//	CRDT.LWWSET key actor value [TS unix-ns]             # LWW-Register
//	CRDT.LWWGET key
//	CRDT.MERGE dest src
//	CRDT.STATE key
//	CRDT.TYPE key
//	CRDT.LIST [TYPE g_counter|pn_counter|or_set|lww_register]
//	CRDT.FORGET key
//	CRDT.STATS
func (c *conn) crdtCmd(sub string, args []string) {
	switch sub {
	case "GINCR":
		if len(args) < 2 {
			writeError(c.bw, "CRDT.GINCR key actor [delta]")
			return
		}
		delta := int64(1)
		if len(args) >= 3 {
			n, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				writeError(c.bw, "ERR delta must be an integer")
				return
			}
			delta = n
		}
		v, err := c.eng.CRDTs.GIncr(args[0], args[1], delta)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CRDT.GINCR", args)
		writeInt(c.bw, v)
	case "GVALUE":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.GVALUE key")
			return
		}
		v, err := c.eng.CRDTs.GValue(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, v)
	case "PNINCR":
		if len(args) != 3 {
			writeError(c.bw, "CRDT.PNINCR key actor delta")
			return
		}
		delta, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR delta must be an integer")
			return
		}
		v, err := c.eng.CRDTs.PNIncr(args[0], args[1], delta)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CRDT.PNINCR", args)
		writeInt(c.bw, v)
	case "PNVALUE":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.PNVALUE key")
			return
		}
		v, err := c.eng.CRDTs.PNValue(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, v)
	case "SADD":
		if len(args) != 3 {
			writeError(c.bw, "CRDT.SADD key actor member")
			return
		}
		n, err := c.eng.CRDTs.SAdd(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CRDT.SADD", args)
		writeInt(c.bw, int64(n))
	case "SREM":
		if len(args) != 2 {
			writeError(c.bw, "CRDT.SREM key member")
			return
		}
		n, err := c.eng.CRDTs.SRem(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CRDT.SREM", args)
		writeInt(c.bw, int64(n))
	case "SMEMBERS":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.SMEMBERS key")
			return
		}
		members, err := c.eng.CRDTs.SMembers(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, members)
	case "SISMEMBER":
		if len(args) != 2 {
			writeError(c.bw, "CRDT.SISMEMBER key member")
			return
		}
		ok, err := c.eng.CRDTs.SIsMember(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if ok {
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "LWWSET":
		if len(args) < 3 {
			writeError(c.bw, "CRDT.LWWSET key actor value [TS unix-ns]")
			return
		}
		var ts int64
		for i := 3; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "TS" {
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil {
					writeError(c.bw, "ERR TS must be unix-ns integer")
					return
				}
				ts = n
			}
		}
		if err := c.eng.CRDTs.LWWSet(args[0], args[1], args[2], ts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CRDT.LWWSET", args)
		writeSimple(c.bw, "OK")
	case "LWWGET":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.LWWGET key")
			return
		}
		val, ts, actor, err := c.eng.CRDTs.LWWGet(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeValue(c.bw, []any{
			"value", val,
			"ts", ts,
			"actor", actor,
		})
	case "MERGE":
		if len(args) != 2 {
			writeError(c.bw, "CRDT.MERGE dest src")
			return
		}
		if err := c.eng.CRDTs.Merge(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CRDT.MERGE", args)
		writeSimple(c.bw, "OK")
	case "STATE":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.STATE key")
			return
		}
		snap, ok := c.eng.CRDTs.State(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, crdtSnapshotToValue(snap))
	case "TYPE":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.TYPE key")
			return
		}
		k := c.eng.CRDTs.Type(args[0])
		if k == "" {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, string(k))
	case "LIST":
		var kind aiops.CRDTKind
		for i := 0; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "TYPE" {
				kind = aiops.CRDTKind(strings.ToLower(args[i+1]))
			}
		}
		writeArray(c.bw, c.eng.CRDTs.List(kind))
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "CRDT.FORGET key")
			return
		}
		ok := c.eng.CRDTs.Forget(args[0])
		if ok {
			c.eng.RecordWrite("CRDT.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		st := c.eng.CRDTs.Stats()
		writeValue(c.bw, []any{
			"total", int64(st.Total),
			"g_counters", int64(st.GCounters),
			"pn_counters", int64(st.PNCounters),
			"or_sets", int64(st.ORSets),
			"lww_registers", int64(st.LWWRegisters),
		})
	default:
		writeError(c.bw, "Unknown CRDT subcommand "+sub)
	}
}

func crdtSnapshotToValue(s aiops.CRDTSnapshot) []any {
	out := []any{
		"key", s.Key,
		"kind", string(s.Kind),
		"created_at", s.CreatedAt.UnixMilli(),
		"updated_at", s.UpdatedAt.UnixMilli(),
	}
	switch s.Kind {
	case aiops.CRDTGCounter, aiops.CRDTPNCounter:
		actors := make([]any, 0, len(s.Actors)*2)
		for k, v := range s.Actors {
			actors = append(actors, k, v)
		}
		out = append(out, "value", s.GValue, "actors", actors)
		if s.Kind == aiops.CRDTPNCounter {
			neg := make([]any, 0, len(s.NegActors)*2)
			for k, v := range s.NegActors {
				neg = append(neg, k, v)
			}
			out = append(out, "neg_actors", neg)
		}
	case aiops.CRDTORSet:
		out = append(out, "members", s.Members)
	case aiops.CRDTLWW:
		out = append(out, "value", s.LWWValue, "ts", s.LWWTS, "actor", s.LWWActor)
	}
	return out
}
