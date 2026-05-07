package resp

// Phase 12 — uniqueness primitives. CHURN (tagged invalidation), WORKER
// (production job queue), FLAG (feature flags), AUDIT (compliance log),
// TRACE (in-memory distributed tracing), DOC (JSON-Patch document sync),
// and OBSERVE (Prometheus exporter). State lives in `internal/aiops/`;
// writes flow through `c.eng.RecordWrite` so AOF + replication propagate
// them like any other write-path command.

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// ── CHURN.* ─────────────────────────────────────────────────────────

// churnCmd dispatches the CHURN.<sub> family. Subcommands:
//
//	CHURN.TAG key tag [tag ...]
//	CHURN.UNTAG key [tag ...]
//	CHURN.INVALIDATE tag [tag ...]
//	CHURN.KEYS tag
//	CHURN.TAGS_OF key
//	CHURN.TAGS
//	CHURN.STATS
func (c *conn) churnCmd(sub string, args []string) {
	switch sub {
	case "TAG":
		if len(args) < 2 {
			writeError(c.bw, "CHURN.TAG key tag [tag ...]")
			return
		}
		n := c.eng.Churn.Tag(args[0], args[1:]...)
		c.eng.RecordWrite("CHURN.TAG", args)
		writeInt(c.bw, int64(n))
	case "UNTAG":
		if len(args) < 1 {
			writeError(c.bw, "CHURN.UNTAG key [tag ...]")
			return
		}
		n := c.eng.Churn.Untag(args[0], args[1:]...)
		c.eng.RecordWrite("CHURN.UNTAG", args)
		writeInt(c.bw, int64(n))
	case "INVALIDATE":
		if len(args) < 1 {
			writeError(c.bw, "CHURN.INVALIDATE tag [tag ...]")
			return
		}
		dropped := c.eng.Churn.Invalidate(args...)
		c.eng.RecordWrite("CHURN.INVALIDATE", args)
		writeArray(c.bw, dropped)
	case "KEYS":
		if len(args) != 1 {
			writeError(c.bw, "CHURN.KEYS tag")
			return
		}
		writeArray(c.bw, c.eng.Churn.KeysFor(args[0]))
	case "TAGS_OF":
		if len(args) != 1 {
			writeError(c.bw, "CHURN.TAGS_OF key")
			return
		}
		writeArray(c.bw, c.eng.Churn.TagsOf(args[0]))
	case "TAGS":
		writeArray(c.bw, c.eng.Churn.Tags())
	case "STATS":
		st := c.eng.Churn.Stats()
		writeValue(c.bw, []any{
			"tagged_keys", int64(st.Keys),
			"unique_tags", int64(st.Tags),
		})
	default:
		writeError(c.bw, "Unknown CHURN subcommand "+sub)
	}
}

// ── WORKER.* ────────────────────────────────────────────────────────

// workerCmd dispatches the WORKER.<sub> family. Subcommands:
//
//	WORKER.ENQUEUE queue payload [PRIORITY n] [IDEMPKEY k]
//	WORKER.DEQUEUE queue [VISIBILITY ms]
//	WORKER.ACK queue id
//	WORKER.NACK queue id error [DELAY ms]
//	WORKER.STATS queue
//	WORKER.DLQ queue
//	WORKER.REQUEUE queue id
//	WORKER.CONFIG queue [MAXATTEMPTS n] [DLQCAP n]
//	WORKER.QUEUES
func (c *conn) workerCmd(sub string, args []string) {
	switch sub {
	case "ENQUEUE":
		if len(args) < 2 {
			writeError(c.bw, "WORKER.ENQUEUE queue payload [PRIORITY n] [IDEMPKEY k]")
			return
		}
		priority := 0
		idempKey := ""
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "PRIORITY":
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					writeError(c.bw, "ERR priority must be an integer")
					return
				}
				priority = n
			case "IDEMPKEY":
				idempKey = args[i+1]
			}
		}
		id := c.eng.Workers.Enqueue(args[0], args[1], priority, idempKey)
		c.eng.RecordWrite("WORKER.ENQUEUE", args)
		writeInt(c.bw, id)
	case "DEQUEUE":
		if len(args) < 1 {
			writeError(c.bw, "WORKER.DEQUEUE queue [VISIBILITY ms]")
			return
		}
		vis := time.Duration(0)
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "VISIBILITY" {
				ms, err := strconv.Atoi(args[i+1])
				if err != nil || ms < 0 {
					writeError(c.bw, "ERR visibility must be a non-negative integer")
					return
				}
				vis = time.Duration(ms) * time.Millisecond
			}
		}
		job := c.eng.Workers.Dequeue(args[0], vis)
		if job == nil {
			writeNil(c.bw)
			return
		}
		c.eng.RecordWrite("WORKER.DEQUEUE", args)
		writeValue(c.bw, jobToMap(job))
	case "ACK":
		if len(args) != 2 {
			writeError(c.bw, "WORKER.ACK queue id")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR id must be an integer")
			return
		}
		ok := c.eng.Workers.Ack(args[0], id)
		if ok {
			c.eng.RecordWrite("WORKER.ACK", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "NACK":
		if len(args) < 3 {
			writeError(c.bw, "WORKER.NACK queue id error [DELAY ms]")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR id must be an integer")
			return
		}
		errMsg := args[2]
		delay := time.Duration(0)
		for i := 3; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "DELAY" {
				ms, err := strconv.Atoi(args[i+1])
				if err != nil || ms < 0 {
					writeError(c.bw, "ERR delay must be a non-negative integer")
					return
				}
				delay = time.Duration(ms) * time.Millisecond
			}
		}
		requeued, dlq := c.eng.Workers.Nack(args[0], id, errMsg, delay)
		c.eng.RecordWrite("WORKER.NACK", args)
		writeValue(c.bw, []any{
			"requeued", requeued,
			"dlq", dlq,
		})
	case "STATS":
		if len(args) != 1 {
			writeError(c.bw, "WORKER.STATS queue")
			return
		}
		st, ok := c.eng.Workers.Stats(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, []any{
			"name", st.Name,
			"pending", int64(st.Pending),
			"reserved", int64(st.Reserved),
			"dlq", int64(st.DLQ),
			"max_attempts", int64(st.MaxAttempts),
			"dlq_cap", int64(st.DLQCap),
		})
	case "DLQ":
		if len(args) != 1 {
			writeError(c.bw, "WORKER.DLQ queue")
			return
		}
		jobs := c.eng.Workers.DLQ(args[0])
		out := make([]any, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, jobToMap(j))
		}
		writeValue(c.bw, out)
	case "REQUEUE":
		if len(args) != 2 {
			writeError(c.bw, "WORKER.REQUEUE queue id")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR id must be an integer")
			return
		}
		if err := c.eng.Workers.Requeue(args[0], id); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("WORKER.REQUEUE", args)
		writeSimple(c.bw, "OK")
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "WORKER.CONFIG queue [MAXATTEMPTS n] [DLQCAP n]")
			return
		}
		maxAtt, dlqCap := 0, 0
		for i := 1; i+1 < len(args); i += 2 {
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				writeError(c.bw, "ERR config value must be an integer")
				return
			}
			switch strings.ToUpper(args[i]) {
			case "MAXATTEMPTS":
				maxAtt = n
			case "DLQCAP":
				dlqCap = n
			}
		}
		c.eng.Workers.Configure(args[0], maxAtt, dlqCap)
		c.eng.RecordWrite("WORKER.CONFIG", args)
		writeSimple(c.bw, "OK")
	case "QUEUES":
		writeArray(c.bw, c.eng.Workers.Queues())
	default:
		writeError(c.bw, "Unknown WORKER subcommand "+sub)
	}
}

// jobToMap turns a *Job into a flat key/value list for writeValue.
func jobToMap(j *aiops.Job) []any {
	deadline := int64(0)
	if !j.DeadlineAt.IsZero() {
		deadline = j.DeadlineAt.UnixMilli()
	}
	return []any{
		"id", j.ID,
		"queue", j.Queue,
		"priority", int64(j.Priority),
		"payload", j.Payload,
		"attempts", int64(j.Attempts),
		"idempotency_key", j.IdempKey,
		"last_error", j.LastError,
		"enqueued_at", j.EnqueuedAt.UnixMilli(),
		"deadline_at", deadline,
	}
}

// ── FLAG.* ──────────────────────────────────────────────────────────

// flagCmd dispatches the FLAG.<sub> family. Subcommands:
//
//	FLAG.SET name on|off PERCENTAGE n [ALLOW u1 u2 ...] [DENY u1 u2 ...]
//	FLAG.IS name user
//	FLAG.ALLOW name user
//	FLAG.DENY name user
//	FLAG.GET name
//	FLAG.LIST
//	FLAG.DELETE name
func (c *conn) flagCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "FLAG.SET name on|off PERCENTAGE n [ALLOW u1 ...] [DENY u1 ...]")
			return
		}
		name := args[0]
		var on bool
		switch strings.ToLower(args[1]) {
		case "on", "1", "true":
			on = true
		case "off", "0", "false":
			on = false
		default:
			writeError(c.bw, "ERR state must be on|off")
			return
		}
		percentage := 0
		var allow, deny []string
		i := 2
		for i < len(args) {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "PERCENTAGE":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR PERCENTAGE requires a value")
					return
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					writeError(c.bw, "ERR percentage must be an integer")
					return
				}
				percentage = n
				i += 2
			case "ALLOW":
				j := i + 1
				for j < len(args) && strings.ToUpper(args[j]) != "DENY" && strings.ToUpper(args[j]) != "PERCENTAGE" {
					allow = append(allow, args[j])
					j++
				}
				i = j
			case "DENY":
				j := i + 1
				for j < len(args) && strings.ToUpper(args[j]) != "ALLOW" && strings.ToUpper(args[j]) != "PERCENTAGE" {
					deny = append(deny, args[j])
					j++
				}
				i = j
			default:
				i++
			}
		}
		c.eng.Flags.Set(name, on, percentage, allow, deny)
		c.eng.RecordWrite("FLAG.SET", args)
		writeSimple(c.bw, "OK")
	case "IS":
		if len(args) != 2 {
			writeError(c.bw, "FLAG.IS name user")
			return
		}
		if c.eng.Flags.Is(args[0], args[1]) {
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "ALLOW":
		if len(args) != 2 {
			writeError(c.bw, "FLAG.ALLOW name user")
			return
		}
		ok := c.eng.Flags.Allow(args[0], args[1])
		if ok {
			c.eng.RecordWrite("FLAG.ALLOW", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "DENY":
		if len(args) != 2 {
			writeError(c.bw, "FLAG.DENY name user")
			return
		}
		ok := c.eng.Flags.Deny(args[0], args[1])
		if ok {
			c.eng.RecordWrite("FLAG.DENY", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "GET":
		if len(args) != 1 {
			writeError(c.bw, "FLAG.GET name")
			return
		}
		st, ok := c.eng.Flags.Get(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, []any{
			"name", st.Name,
			"on", st.On,
			"percentage", int64(st.Percentage),
			"allow", st.Allow,
			"deny", st.Deny,
			"evals", st.Evals,
			"enabled", st.Enabled,
			"created_at", st.CreatedAt.Unix(),
			"updated_at", st.UpdatedAt.Unix(),
		})
	case "LIST":
		writeArray(c.bw, c.eng.Flags.List())
	case "DELETE":
		if len(args) != 1 {
			writeError(c.bw, "FLAG.DELETE name")
			return
		}
		ok := c.eng.Flags.Delete(args[0])
		if ok {
			c.eng.RecordWrite("FLAG.DELETE", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	default:
		writeError(c.bw, "Unknown FLAG subcommand "+sub)
	}
}

// ── AUDIT.* ─────────────────────────────────────────────────────────

// auditCmd dispatches the AUDIT.<sub> family. Subcommands:
//
//	AUDIT.LOG actor action resource [OUTCOME outcome] [ATTRS k v ...]
//	AUDIT.QUERY [ACTOR a] [ACTION a] [RESOURCE r] [SINCE unix-ms] [UNTIL unix-ms] [LIMIT n]
//	AUDIT.COUNT
//	AUDIT.STATS
//	AUDIT.RETENTION n
func (c *conn) auditCmd(sub string, args []string) {
	switch sub {
	case "LOG":
		if len(args) < 3 {
			writeError(c.bw, "AUDIT.LOG actor action resource [OUTCOME outcome] [ATTRS k v ...]")
			return
		}
		actor, action, resource := args[0], args[1], args[2]
		outcome := ""
		attrs := map[string]string{}
		i := 3
		for i < len(args) {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "OUTCOME":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR OUTCOME requires a value")
					return
				}
				outcome = args[i+1]
				i += 2
			case "ATTRS":
				j := i + 1
				for j+1 < len(args) {
					attrs[args[j]] = args[j+1]
					j += 2
				}
				i = j
			default:
				i++
			}
		}
		id := c.eng.Audit.Log(actor, action, resource, outcome, attrs)
		c.eng.RecordWrite("AUDIT.LOG", args)
		writeInt(c.bw, id)
	case "QUERY":
		q := aiops.AuditQuery{}
		for i := 0; i+1 < len(args); i += 2 {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "ACTOR":
				q.Actor = args[i+1]
			case "ACTION":
				q.Action = args[i+1]
			case "RESOURCE":
				q.Resource = args[i+1]
			case "SINCE":
				ms, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil {
					writeError(c.bw, "ERR SINCE must be unix-ms")
					return
				}
				q.Since = time.UnixMilli(ms)
			case "UNTIL":
				ms, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil {
					writeError(c.bw, "ERR UNTIL must be unix-ms")
					return
				}
				q.Until = time.UnixMilli(ms)
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "ERR LIMIT must be a non-negative integer")
					return
				}
				q.Limit = n
			}
		}
		evs := c.eng.Audit.Query(q)
		out := make([]any, 0, len(evs))
		for _, ev := range evs {
			attrs := make([]any, 0, len(ev.Attrs)*2)
			for k, v := range ev.Attrs {
				attrs = append(attrs, k, v)
			}
			out = append(out, []any{
				"id", ev.ID,
				"actor", ev.Actor,
				"action", ev.Action,
				"resource", ev.Resource,
				"outcome", ev.Outcome,
				"attrs", attrs,
				"at", ev.At.UnixMilli(),
			})
		}
		writeValue(c.bw, out)
	case "COUNT":
		writeInt(c.bw, int64(c.eng.Audit.Count()))
	case "STATS":
		st := c.eng.Audit.Stats()
		writeValue(c.bw, []any{
			"entries", int64(st.Entries),
			"max_entries", int64(st.MaxEntries),
			"unique_actors", int64(st.Actors),
			"unique_resources", int64(st.Resources),
			"unique_actions", int64(st.Actions),
		})
	case "RETENTION":
		if len(args) != 1 {
			writeError(c.bw, "AUDIT.RETENTION n")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n <= 0 {
			writeError(c.bw, "ERR retention must be a positive integer")
			return
		}
		c.eng.Audit.SetMaxEntries(n)
		c.eng.RecordWrite("AUDIT.RETENTION", args)
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown AUDIT subcommand "+sub)
	}
}

// ── TRACE.* ─────────────────────────────────────────────────────────

// traceCmd dispatches the TRACE.<sub> family. Subcommands:
//
//	TRACE.START trace_id span_id [PARENT pid] name [ATTRS k v ...]
//	TRACE.END trace_id span_id [STATUS s]
//	TRACE.ANNOTATE trace_id span_id k v [k v ...]
//	TRACE.GET trace_id
//	TRACE.LIST [LIMIT n]
//	TRACE.FORGET trace_id
//	TRACE.STATS
func (c *conn) traceCmd(sub string, args []string) {
	switch sub {
	case "START":
		if len(args) < 3 {
			writeError(c.bw, "TRACE.START trace_id span_id [PARENT pid] name [ATTRS k v ...]")
			return
		}
		traceID, spanID := args[0], args[1]
		parent := ""
		i := 2
		if i+1 < len(args) && strings.ToUpper(args[i]) == "PARENT" {
			parent = args[i+1]
			i += 2
		}
		if i >= len(args) {
			writeError(c.bw, "ERR span name required")
			return
		}
		name := args[i]
		i++
		attrs := map[string]string{}
		if i < len(args) && strings.ToUpper(args[i]) == "ATTRS" {
			j := i + 1
			for j+1 < len(args) {
				attrs[args[j]] = args[j+1]
				j += 2
			}
		}
		c.eng.Tracer.Start(traceID, spanID, parent, name, attrs)
		c.eng.RecordWrite("TRACE.START", args)
		writeSimple(c.bw, "OK")
	case "END":
		if len(args) < 2 {
			writeError(c.bw, "TRACE.END trace_id span_id [STATUS s]")
			return
		}
		status := ""
		for i := 2; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "STATUS" {
				status = args[i+1]
			}
		}
		ok := c.eng.Tracer.End(args[0], args[1], status)
		if ok {
			c.eng.RecordWrite("TRACE.END", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "ANNOTATE":
		if len(args) < 4 || (len(args)-2)%2 != 0 {
			writeError(c.bw, "TRACE.ANNOTATE trace_id span_id k v [k v ...]")
			return
		}
		attrs := map[string]string{}
		for i := 2; i+1 < len(args); i += 2 {
			attrs[args[i]] = args[i+1]
		}
		ok := c.eng.Tracer.Annotate(args[0], args[1], attrs)
		if ok {
			c.eng.RecordWrite("TRACE.ANNOTATE", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "GET":
		if len(args) != 1 {
			writeError(c.bw, "TRACE.GET trace_id")
			return
		}
		spans := c.eng.Tracer.Get(args[0])
		out := make([]any, 0, len(spans))
		for _, s := range spans {
			attrs := make([]any, 0, len(s.Attrs)*2)
			for k, v := range s.Attrs {
				attrs = append(attrs, k, v)
			}
			finished := int64(0)
			if !s.FinishedAt.IsZero() {
				finished = s.FinishedAt.UnixMilli()
			}
			out = append(out, []any{
				"trace_id", s.TraceID,
				"span_id", s.SpanID,
				"parent_id", s.ParentID,
				"name", s.Name,
				"started_at", s.StartedAt.UnixMilli(),
				"finished_at", finished,
				"duration_ms", s.DurationMs,
				"status", s.Status,
				"attrs", attrs,
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		limit := 0
		for i := 0; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "LIMIT" {
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "ERR LIMIT must be a non-negative integer")
					return
				}
				limit = n
			}
		}
		writeArray(c.bw, c.eng.Tracer.List(limit))
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "TRACE.FORGET trace_id")
			return
		}
		ok := c.eng.Tracer.Forget(args[0])
		if ok {
			c.eng.RecordWrite("TRACE.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		st := c.eng.Tracer.Stats()
		writeValue(c.bw, []any{
			"traces", int64(st.Traces),
			"total_spans", int64(st.TotalSpans),
			"max_per_trace", int64(st.MaxPerTrace),
		})
	default:
		writeError(c.bw, "Unknown TRACE subcommand "+sub)
	}
}

// ── DOC.* ───────────────────────────────────────────────────────────

// docCmd dispatches the DOC.<sub> family. Subcommands:
//
//	DOC.INIT key json-value
//	DOC.APPLY key json-patch-array
//	DOC.GET key
//	DOC.SINCE key version
//	DOC.LIST
//	DOC.FORGET key
func (c *conn) docCmd(sub string, args []string) {
	switch sub {
	case "INIT":
		if len(args) != 2 {
			writeError(c.bw, "DOC.INIT key json-value")
			return
		}
		v, err := c.eng.Docs.Init(args[0], []byte(args[1]))
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("DOC.INIT", args)
		writeInt(c.bw, v)
	case "APPLY":
		if len(args) != 2 {
			writeError(c.bw, "DOC.APPLY key json-patch-array")
			return
		}
		v, err := c.eng.Docs.Apply(args[0], []byte(args[1]))
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("DOC.APPLY", args)
		writeInt(c.bw, v)
	case "GET":
		if len(args) != 1 {
			writeError(c.bw, "DOC.GET key")
			return
		}
		snap, ok := c.eng.Docs.Get(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, docSnapshotToValue(snap))
	case "SINCE":
		if len(args) != 2 {
			writeError(c.bw, "DOC.SINCE key version")
			return
		}
		v, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || v < 0 {
			writeError(c.bw, "ERR version must be a non-negative integer")
			return
		}
		patches, snap, ok := c.eng.Docs.Since(args[0], v)
		if !ok {
			writeNil(c.bw)
			return
		}
		if snap != nil {
			writeValue(c.bw, []any{
				"snapshot", docSnapshotToValue(*snap),
			})
			return
		}
		out := make([]any, 0, len(patches))
		for _, p := range patches {
			out = append(out, []any{
				"version", p.Version,
				"at", p.At.UnixMilli(),
				"ops", string(p.Ops),
			})
		}
		writeValue(c.bw, []any{"patches", out})
	case "LIST":
		writeArray(c.bw, c.eng.Docs.List())
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "DOC.FORGET key")
			return
		}
		ok := c.eng.Docs.Forget(args[0])
		if ok {
			c.eng.RecordWrite("DOC.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	default:
		writeError(c.bw, "Unknown DOC subcommand "+sub)
	}
}

// docSnapshotToValue serialises a DocSnapshot for RESP. The Value field
// can be any JSON shape so we hand it back as a marshalled JSON string —
// callers that want structured access already use the HTTP surface.
func docSnapshotToValue(snap aiops.DocSnapshot) []any {
	valJSON := ""
	if snap.Value != nil {
		b, _ := json.Marshal(snap.Value)
		valJSON = string(b)
	}
	return []any{
		"key", snap.Key,
		"version", snap.Version,
		"value", valJSON,
		"updated_at", snap.UpdatedAt.UnixMilli(),
	}
}

// ── OBSERVE.* ───────────────────────────────────────────────────────

// observeCmd dispatches the OBSERVE.<sub> family. Subcommands:
//
//	OBSERVE.REGISTER COUNTER|GAUGE name help [LABEL k v ...]
//	OBSERVE.INC name [delta]
//	OBSERVE.SET name value
//	OBSERVE.RENDER
func (c *conn) observeCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 3 {
			writeError(c.bw, "OBSERVE.REGISTER COUNTER|GAUGE name help [LABEL k v ...]")
			return
		}
		kind := strings.ToUpper(args[0])
		name := args[1]
		help := args[2]
		labels := map[string]string{}
		for i := 3; i+2 < len(args); i += 3 {
			if strings.ToUpper(args[i]) == "LABEL" {
				labels[args[i+1]] = args[i+2]
			}
		}
		if len(labels) == 0 {
			labels = nil
		}
		switch kind {
		case "COUNTER":
			c.eng.Observe.RegisterCounter(name, help, labels)
		case "GAUGE":
			c.eng.Observe.RegisterGauge(name, help, labels)
		default:
			writeError(c.bw, "ERR kind must be COUNTER or GAUGE")
			return
		}
		c.eng.RecordWrite("OBSERVE.REGISTER", args)
		writeSimple(c.bw, "OK")
	case "INC":
		if len(args) < 1 {
			writeError(c.bw, "OBSERVE.INC name [delta]")
			return
		}
		delta := int64(1)
		if len(args) >= 2 {
			n, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				writeError(c.bw, "ERR delta must be an integer")
				return
			}
			delta = n
		}
		c.eng.Observe.Inc(args[0], delta)
		c.eng.RecordWrite("OBSERVE.INC", args)
		writeSimple(c.bw, "OK")
	case "SET":
		if len(args) != 2 {
			writeError(c.bw, "OBSERVE.SET name value")
			return
		}
		v, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "ERR value must be a float")
			return
		}
		c.eng.Observe.SetGauge(args[0], v)
		c.eng.RecordWrite("OBSERVE.SET", args)
		writeSimple(c.bw, "OK")
	case "RENDER":
		writeBulk(c.bw, c.eng.Observe.Render())
	default:
		writeError(c.bw, "Unknown OBSERVE subcommand "+sub)
	}
}
