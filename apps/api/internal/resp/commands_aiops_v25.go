package resp

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// contextScanCmd handles CONTEXT.SCAN.* — indirect-injection scanner.
// Sub names arrive here with the "CONTEXT." prefix already stripped, so
// the bare SCAN is sub == "SCAN" and SCAN.BULK arrives as "SCAN.BULK".
func (c *conn) contextScanCmd(sub string, args []string) {
	switch sub {
	case "SCAN":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'context.scan' (need doc-id payload)")
			return
		}
		r := c.eng.ContextScan.Scan(args[0], args[1])
		writeContextScanResult(c.bw, r)
	case "SCAN.BULK":
		if len(args) < 2 || len(args)%2 != 0 {
			writeError(c.bw, "usage: CONTEXT.SCAN.BULK doc-id payload [doc-id payload ...]")
			return
		}
		ids := make([]string, 0, len(args)/2)
		payloads := make([]string, 0, len(args)/2)
		for i := 0; i < len(args); i += 2 {
			ids = append(ids, args[i])
			payloads = append(payloads, args[i+1])
		}
		results, err := c.eng.ContextScan.ScanBulk(ids, payloads)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(results))
		for _, r := range results {
			out = append(out, contextScanResultArr(r))
		}
		writeValue(c.bw, out)
	case "SCAN.SANITIZE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'context.scan.sanitize'")
			return
		}
		writeBulk(c.bw, c.eng.ContextScan.Sanitize(args[0]))
	case "SCAN.RULES":
		rules := c.eng.ContextScan.Rules()
		out := make([]any, 0, len(rules))
		for _, r := range rules {
			out = append(out, []any{
				"class", r.Class,
				"pattern", r.Pattern,
				"severity", strconv.FormatFloat(r.Severity, 'f', 2, 64),
			})
		}
		writeValue(c.bw, out)
	case "SCAN.WHITELIST":
		if len(args) < 1 {
			writeError(c.bw, "usage: CONTEXT.SCAN.WHITELIST ADD|REMOVE|LIST [pattern]")
			return
		}
		switch strings.ToUpper(args[0]) {
		case "ADD":
			if len(args) < 2 {
				writeError(c.bw, "ADD requires a pattern")
				return
			}
			if err := c.eng.ContextScan.WhitelistAdd(args[1]); err != nil {
				writeError(c.bw, err.Error())
				return
			}
			writeSimple(c.bw, "OK")
		case "REMOVE":
			if len(args) < 2 {
				writeError(c.bw, "REMOVE requires a pattern")
				return
			}
			if c.eng.ContextScan.WhitelistRemove(args[1]) {
				writeInt(c.bw, 1)
			} else {
				writeInt(c.bw, 0)
			}
		case "LIST":
			writeArray(c.bw, c.eng.ContextScan.Whitelist())
		default:
			writeError(c.bw, "WHITELIST subop must be ADD | REMOVE | LIST")
		}
	case "SCAN.RECENT":
		limit := 0
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown CONTEXT.SCAN.RECENT option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
			i += 2
		}
		rows := c.eng.ContextScan.Recent(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"ts", strconv.FormatInt(r.TS, 10),
				"doc_id", r.DocID,
				"severity", strconv.FormatFloat(r.Severity, 'f', 4, 64),
				"classes", r.Classes,
				"sample", r.Sample,
			})
		}
		writeValue(c.bw, out)
	case "SCAN.RESET":
		c.eng.ContextScan.Reset()
		writeSimple(c.bw, "OK")
	case "SCAN.STATS":
		s := c.eng.ContextScan.Stats()
		writeArray(c.bw, []string{
			"total_scans", strconv.FormatInt(s.TotalScans, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_sanitized", strconv.FormatInt(s.TotalSanitized, 10),
			"total_whitelisted", strconv.FormatInt(s.TotalWhitelisted, 10),
			"whitelist_size", strconv.Itoa(s.WhitelistSize),
			"recent_size", strconv.Itoa(s.RecentSize),
		})
	default:
		writeError(c.bw, "unknown CONTEXT subcommand: "+sub)
	}
}

// ragGapCmd handles RAG.GAP.* — coverage-gap analytics.
func (c *conn) ragGapCmd(sub string, args []string) {
	switch sub {
	case "OBSERVE":
		if len(args) < 4 || !strings.EqualFold(args[2], "SCORE") {
			writeError(c.bw, "usage: RAG.GAP.OBSERVE index query SCORE f")
			return
		}
		score, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "SCORE must be float")
			return
		}
		if err := c.eng.RAGGap.Observe(args[0], args[1], score); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REPORT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rag.gap.report'")
			return
		}
		f, err := parseRAGGapFilter(args[1:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		rows, err := c.eng.RAGGap.Report(args[0], f)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			resolvedInt := "0"
			if r.Resolved {
				resolvedInt = "1"
			}
			out = append(out, []any{
				"cluster_id", r.ClusterID,
				"sample_query", r.SampleQuery,
				"n", strconv.Itoa(r.N),
				"avg_score", strconv.FormatFloat(r.AvgScore, 'f', 4, 64),
				"last_seen", strconv.FormatInt(r.LastSeen, 10),
				"resolved", resolvedInt,
				"gap_weight", strconv.FormatFloat(r.GapWeight, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "QUERIES":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rag.gap.queries'")
			return
		}
		threshold := 0.0
		limit := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "THRESHOLD":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "THRESHOLD must be float")
					return
				}
				threshold = f
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown RAG.GAP.QUERIES option: "+key)
				return
			}
			i += 2
		}
		rows, ok := c.eng.RAGGap.Queries(args[0], threshold, limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"query", r.Query,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"ts", strconv.FormatInt(r.TS, 10),
			})
		}
		writeValue(c.bw, out)
	case "RESOLVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'rag.gap.resolve' (need index cluster-id)")
			return
		}
		if err := c.eng.RAGGap.Resolve(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "INDEXES":
		writeArray(c.bw, c.eng.RAGGap.Indexes())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rag.gap.reset' (use ALL or index-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.RAGGap.Reset(args[0])))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rag.gap.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			writeError(c.bw, "cap must be non-negative integer")
			return
		}
		c.eng.RAGGap.SetCap(n)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.RAGGap.Stats()
		writeArray(c.bw, []string{
			"indexes", strconv.Itoa(s.Indexes),
			"observations", strconv.Itoa(s.Observations),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
			"total_reports", strconv.FormatInt(s.TotalReports, 10),
			"total_resolves", strconv.FormatInt(s.TotalResolves, 10),
			"cap", strconv.Itoa(s.Cap),
		})
	default:
		writeError(c.bw, "unknown RAG.GAP subcommand: "+sub)
	}
}

// replayCmd handles REPLAY.* — deterministic record/replay.
func (c *conn) replayCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		// REPLAY.RECORD sess STEP n KIND k IN in OUT out
		if len(args) < 9 {
			writeError(c.bw, "usage: REPLAY.RECORD sess STEP n KIND k IN in OUT out")
			return
		}
		opts := parseReplayRecordArgs(args[1:])
		if opts.err != "" {
			writeError(c.bw, opts.err)
			return
		}
		if err := c.eng.Replay.Record(args[0], opts.step, opts.kind, opts.in, opts.out); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "OPEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'replay.open'")
			return
		}
		if err := c.eng.Replay.Open(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "NEXT":
		// REPLAY.NEXT sess KIND k IN in
		if len(args) < 5 {
			writeError(c.bw, "usage: REPLAY.NEXT sess KIND k IN in")
			return
		}
		kind, in := "", ""
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "KIND":
				kind = args[i+1]
			case "IN":
				in = args[i+1]
			default:
				writeError(c.bw, "unknown REPLAY.NEXT option: "+key)
				return
			}
		}
		step, err := c.eng.Replay.Next(args[0], kind, in)
		if err != nil {
			if llmstack.IsReplayDrift(err) {
				writeTypedError(c.bw, "REPLAYDRIFT", err.Error())
				return
			}
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"step", strconv.Itoa(step.Step),
			"kind", step.Kind,
			"in", step.In,
			"out", step.Out,
		})
	case "CLOSE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'replay.close'")
			return
		}
		c.eng.Replay.Close(args[0])
		writeSimple(c.bw, "OK")
	case "DIFF":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'replay.diff' (need sess-a sess-b)")
			return
		}
		rows, err := c.eng.Replay.Diff(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"step", strconv.Itoa(r.Step),
				"kind", r.Kind,
				"field", r.Field,
				"a", r.A,
				"b", r.B,
			})
		}
		writeValue(c.bw, out)
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'replay.get'")
			return
		}
		step := -1
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STEP" {
				writeError(c.bw, "unknown REPLAY.GET option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "STEP must be non-negative integer")
				return
			}
			step = n
			i += 2
		}
		rows, ok := c.eng.Replay.Get(args[0], step)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"step", strconv.Itoa(r.Step),
				"kind", r.Kind,
				"in", r.In,
				"out", r.Out,
				"ts", strconv.FormatInt(r.TS, 10),
			})
		}
		writeValue(c.bw, out)
	case "EXPORT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'replay.export'")
			return
		}
		b, ok := c.eng.Replay.Export(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		steps := make([]any, 0, len(b.Steps))
		for _, r := range b.Steps {
			steps = append(steps, []any{
				"step", strconv.Itoa(r.Step),
				"kind", r.Kind,
				"in", r.In,
				"out", r.Out,
				"ts", strconv.FormatInt(r.TS, 10),
			})
		}
		writeValue(c.bw, []any{
			"session_id", b.SessionID,
			"created_at", strconv.FormatInt(b.CreatedAt, 10),
			"n_steps", strconv.Itoa(b.NSteps),
			"steps", steps,
		})
	case "SESSIONS":
		writeArray(c.bw, c.eng.Replay.Sessions())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'replay.reset' (use ALL or session-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.Replay.Reset(args[0])))
	case "STATS":
		s := c.eng.Replay.Stats()
		writeArray(c.bw, []string{
			"sessions", strconv.Itoa(s.Sessions),
			"total_steps", strconv.Itoa(s.TotalSteps),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_nexts", strconv.FormatInt(s.TotalNexts, 10),
			"total_diffs", strconv.FormatInt(s.TotalDiffs, 10),
			"total_drifts", strconv.FormatInt(s.TotalDrifts, 10),
		})
	default:
		writeError(c.bw, "unknown REPLAY subcommand: "+sub)
	}
}

// ─── shared helpers ────────────────────────────────────────────

func writeContextScanResult(bw *bufio.Writer, r llmstack.ContextScanResult) {
	hitInt := "0"
	if r.Hit {
		hitInt = "1"
	}
	// Spans as nested arrays
	spans := make([]any, 0, len(r.Spans))
	for _, sp := range r.Spans {
		spans = append(spans, []any{
			"start", strconv.Itoa(sp.Start),
			"end", strconv.Itoa(sp.End),
			"class", sp.Class,
			"match", sp.Match,
		})
	}
	writeValue(bw, []any{
		"doc_id", r.DocID,
		"hit", hitInt,
		"severity", strconv.FormatFloat(r.Severity, 'f', 4, 64),
		"classes", r.Classes,
		"spans", spans,
		"sanitized", r.Sanitized,
	})
}

func contextScanResultArr(r llmstack.ContextScanResult) []any {
	hitInt := "0"
	if r.Hit {
		hitInt = "1"
	}
	spans := make([]any, 0, len(r.Spans))
	for _, sp := range r.Spans {
		spans = append(spans, []any{
			"start", strconv.Itoa(sp.Start),
			"end", strconv.Itoa(sp.End),
			"class", sp.Class,
			"match", sp.Match,
		})
	}
	return []any{
		"doc_id", r.DocID,
		"hit", hitInt,
		"severity", strconv.FormatFloat(r.Severity, 'f', 4, 64),
		"classes", r.Classes,
		"spans", spans,
		"sanitized", r.Sanitized,
	}
}

func parseRAGGapFilter(rest []string) (llmstack.RAGGapFilter, error) {
	f := llmstack.RAGGapFilter{}
	for i := 0; i+1 < len(rest)+1 && i < len(rest); i += 2 {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return f, errFromString(key + " needs a value")
		}
		val := rest[i+1]
		switch key {
		case "THRESHOLD":
			v, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return f, errFromString("THRESHOLD must be float")
			}
			f.Threshold = v
		case "WINDOW":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				return f, errFromString("WINDOW must be non-negative integer (seconds)")
			}
			f.Window = time.Duration(n) * time.Second
		case "LIMIT":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return f, errFromString("LIMIT must be positive integer")
			}
			f.Limit = n
		case "CLUSTER_SIM":
			v, err := strconv.ParseFloat(val, 64)
			if err != nil || v < 0 || v > 1 {
				return f, errFromString("CLUSTER_SIM must be float in [0,1]")
			}
			f.ClusterMinSim = v
		default:
			return f, errFromString("unknown RAG.GAP option: " + key)
		}
	}
	return f, nil
}

// replayRecordOpts is the parsed STEP/KIND/IN/OUT bundle.
type replayRecordOpts struct {
	step      int
	kind      string
	in, out   string
	err       string
}

func parseReplayRecordArgs(rest []string) replayRecordOpts {
	opts := replayRecordOpts{step: -1}
	for i := 0; i+1 < len(rest)+1 && i < len(rest); i += 2 {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			opts.err = key + " needs a value"
			return opts
		}
		val := rest[i+1]
		switch key {
		case "STEP":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				opts.err = "STEP must be non-negative integer"
				return opts
			}
			opts.step = n
		case "KIND":
			opts.kind = val
		case "IN":
			opts.in = val
		case "OUT":
			opts.out = val
		default:
			opts.err = "unknown REPLAY.RECORD option: " + key
			return opts
		}
	}
	if opts.step < 0 {
		opts.err = "STEP is required"
	}
	if opts.kind == "" {
		opts.err = "KIND is required"
	}
	return opts
}

// errFromString returns a Go error for the parser helpers.
func errFromString(s string) error { return errorString(s) }

type errorString string

func (e errorString) Error() string { return string(e) }
