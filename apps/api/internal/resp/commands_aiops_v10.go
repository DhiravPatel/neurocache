package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// citeCmd handles CITE.* — citation extraction + validation.
//
//   CITE.EXTRACT text [PATTERN regex]
//        -> array of {marker, label, start, end}
//   CITE.RESOLVE text SOURCE id text [SOURCE id text...] [PATTERN regex]
//        -> array of {marker, label, valid, source_text}
//   CITE.VALIDATE text SOURCE id text [SOURCE id text...] [PATTERN regex]
//        -> {valid, total, valid_n, invalid_n, invalid_labels[], unreferenced_ids[]}
//   CITE.STATS
func (c *conn) citeCmd(sub string, args []string) {
	switch sub {
	case "EXTRACT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cite.extract'")
			return
		}
		text := args[0]
		pattern := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "PATTERN" {
				writeError(c.bw, "unknown CITE.EXTRACT option: "+key)
				return
			}
			pattern = val
			i += 2
		}
		cites, err := c.eng.Citations.Extract(text, pattern)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(cites))
		for _, ct := range cites {
			out = append(out, []any{
				"marker", ct.Marker,
				"label", ct.Label,
				"start", strconv.Itoa(ct.Start),
				"end", strconv.Itoa(ct.End),
			})
		}
		writeValue(c.bw, out)
	case "RESOLVE":
		text, pattern, sources, order, err := parseCiteSources(args)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		resolved, err := c.eng.Citations.Resolve(text, pattern, sources, order)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(resolved))
		for _, r := range resolved {
			validInt := "0"
			if r.Valid {
				validInt = "1"
			}
			out = append(out, []any{
				"marker", r.Marker,
				"label", r.Label,
				"valid", validInt,
				"source_text", r.SourceText,
			})
		}
		writeValue(c.bw, out)
	case "VALIDATE":
		text, pattern, sources, order, err := parseCiteSources(args)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		r, err := c.eng.Citations.Validate(text, pattern, sources, order)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		validInt := "0"
		if r.Valid {
			validInt = "1"
		}
		invalidAny := make([]any, 0, len(r.InvalidLabels))
		for _, m := range r.InvalidLabels {
			invalidAny = append(invalidAny, m)
		}
		unrefAny := make([]any, 0, len(r.UnreferencedIDs))
		for _, m := range r.UnreferencedIDs {
			unrefAny = append(unrefAny, m)
		}
		writeValue(c.bw, []any{
			"valid", validInt,
			"total", strconv.Itoa(r.Total),
			"valid_n", strconv.Itoa(r.ValidN),
			"invalid_n", strconv.Itoa(r.InvalidN),
			"invalid_labels", invalidAny,
			"unreferenced_ids", unrefAny,
		})
	case "STATS":
		s := c.eng.Citations.Stats()
		writeArray(c.bw, []string{
			"total_extracts", strconv.FormatInt(s.TotalExtracts, 10),
			"total_resolves", strconv.FormatInt(s.TotalResolves, 10),
			"total_citations", strconv.FormatInt(s.TotalCitations, 10),
			"total_invalid", strconv.FormatInt(s.TotalInvalid, 10),
		})
	default:
		writeError(c.bw, "unknown CITE subcommand: "+sub)
	}
}

func parseCiteSources(args []string) (text, pattern string, sources map[string]string, order []string, err error) {
	if len(args) < 1 {
		err = errMissingText
		return
	}
	text = args[0]
	sources = map[string]string{}
	i := 1
	for i < len(args) {
		key := strings.ToUpper(args[i])
		switch key {
		case "SOURCE":
			if i+2 >= len(args) {
				err = errBadSource
				return
			}
			id := args[i+1]
			body := args[i+2]
			sources[id] = body
			order = append(order, id)
			i += 3
		case "PATTERN":
			if i+1 >= len(args) {
				err = errBadPattern
				return
			}
			pattern = args[i+1]
			i += 2
		default:
			err = errCiteUnknownOpt(key)
			return
		}
	}
	return
}

// shrinkCmd handles SHRINK.* — prompt compression.
//
//   SHRINK.TEXT text [STRATEGY whitespace|stopwords|truncate|all]
//                    [TARGET tokens] [MODEL m] [FROM_END 1]
//        -> {text, original_*, shrunk_*, ratio, tokens_saved, strategy}
//   SHRINK.STATS
func (c *conn) shrinkCmd(sub string, args []string) {
	switch sub {
	case "TEXT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'shrink.text'")
			return
		}
		opts := llmstack.ShrinkOpts{Strategy: "all"}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "STRATEGY":
				opts.Strategy = val
			case "TARGET":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "TARGET must be a positive integer")
					return
				}
				opts.Target = n
			case "MODEL":
				opts.Model = val
			case "FROM_END":
				opts.FromEnd = val == "1" || strings.EqualFold(val, "true")
			default:
				writeError(c.bw, "unknown SHRINK.TEXT option: "+key)
				return
			}
			i += 2
		}
		r := c.eng.Shrinker.Shrink(args[0], opts)
		writeValue(c.bw, []any{
			"text", r.Text,
			"original_chars", strconv.Itoa(r.OriginalChars),
			"shrunk_chars", strconv.Itoa(r.ShrunkChars),
			"original_tokens", strconv.Itoa(r.OriginalToks),
			"shrunk_tokens", strconv.Itoa(r.ShrunkToks),
			"ratio", strconv.FormatFloat(r.Ratio, 'f', 4, 64),
			"tokens_saved", strconv.Itoa(r.Saved),
			"strategy", r.Strategy,
		})
	case "STATS":
		s := c.eng.Shrinker.Stats()
		writeArray(c.bw, []string{
			"total_runs", strconv.FormatInt(s.TotalRuns, 10),
			"total_tokens_in", strconv.FormatInt(s.TotalTokensIn, 10),
			"total_tokens_out", strconv.FormatInt(s.TotalTokensOut, 10),
			"total_tokens_saved", strconv.FormatInt(s.TotalTokensSaved, 10),
			"avg_ratio", strconv.FormatFloat(s.AvgRatio, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown SHRINK subcommand: "+sub)
	}
}

// agentLoopCmd handles AGENTLOOP.* — agent step budget enforcement.
//
//   AGENTLOOP.START loop-id [MAX_STEPS n] [MAX_TOOL_CALLS n]
//                           [MAX_TOKENS n] [MAX_TIME_MS ms]
//   AGENTLOOP.STEP loop-id [TOKENS n] [TOOL_CALL 0|1]
//        -> [should_stop, reason, steps, tool_calls, tokens, elapsed_ms]
//   AGENTLOOP.STATUS loop-id    -> full snapshot
//   AGENTLOOP.RESET loop-id     -> int (1 if reset)
//   AGENTLOOP.FORGET loop-id    -> int
//   AGENTLOOP.ACTIVE            -> array of loop_ids
//   AGENTLOOP.STATS
func (c *conn) agentLoopCmd(sub string, args []string) {
	switch sub {
	case "START":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'agentloop.start'")
			return
		}
		opts := llmstack.LoopOpts{}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, key+" must be a non-negative integer")
				return
			}
			switch key {
			case "MAX_STEPS":
				opts.MaxSteps = n
			case "MAX_TOOL_CALLS":
				opts.MaxToolCalls = n
			case "MAX_TOKENS":
				opts.MaxTokens = n
			case "MAX_TIME_MS":
				opts.MaxTimeMS = n
			default:
				writeError(c.bw, "unknown AGENTLOOP.START option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.AgentLoop.Start(args[0], opts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STEP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'agentloop.step'")
			return
		}
		opts := llmstack.StepOpts{}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TOKENS":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "TOKENS must be non-negative")
					return
				}
				opts.Tokens = n
			case "TOOL_CALL":
				opts.ToolCall = val == "1" || strings.EqualFold(val, "true")
			default:
				writeError(c.bw, "unknown AGENTLOOP.STEP option: "+key)
				return
			}
			i += 2
		}
		r, ok := c.eng.AgentLoop.Step(args[0], opts)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNLOOP", "no loop registered for that id")
			return
		}
		stopInt := "0"
		if r.ShouldStop {
			stopInt = "1"
		}
		writeArray(c.bw, []string{
			"should_stop", stopInt,
			"reason", r.Reason,
			"steps", strconv.FormatInt(r.Steps, 10),
			"tool_calls", strconv.FormatInt(r.ToolCalls, 10),
			"tokens", strconv.FormatInt(r.Tokens, 10),
			"elapsed_ms", strconv.FormatInt(r.ElapsedMS, 10),
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'agentloop.status'")
			return
		}
		s, ok := c.eng.AgentLoop.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		stoppedInt := "0"
		if s.Stopped {
			stoppedInt = "1"
		}
		writeArray(c.bw, []string{
			"loop_id", s.LoopID,
			"stopped", stoppedInt,
			"reason", s.Reason,
			"steps", strconv.FormatInt(s.Steps, 10),
			"tool_calls", strconv.FormatInt(s.ToolCalls, 10),
			"tokens", strconv.FormatInt(s.Tokens, 10),
			"elapsed_ms", strconv.FormatInt(s.ElapsedMS, 10),
			"max_steps", strconv.FormatInt(s.MaxSteps, 10),
			"max_tool_calls", strconv.FormatInt(s.MaxToolCalls, 10),
			"max_tokens", strconv.FormatInt(s.MaxTokens, 10),
			"max_time_ms", strconv.FormatInt(s.MaxTimeMS, 10),
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'agentloop.reset'")
			return
		}
		if c.eng.AgentLoop.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'agentloop.forget'")
			return
		}
		if c.eng.AgentLoop.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "ACTIVE":
		writeArray(c.bw, c.eng.AgentLoop.Active())
	case "STATS":
		s := c.eng.AgentLoop.Stats()
		writeArray(c.bw, []string{
			"total_starts", strconv.FormatInt(s.TotalStarts, 10),
			"total_steps", strconv.FormatInt(s.TotalSteps, 10),
			"total_stops", strconv.FormatInt(s.TotalStops, 10),
			"active", strconv.Itoa(s.Active),
		})
	default:
		writeError(c.bw, "unknown AGENTLOOP subcommand: "+sub)
	}
}

type citeErr struct{ msg string }

func (e *citeErr) Error() string { return e.msg }

var (
	errMissingText = &citeErr{"text argument required"}
	errBadSource   = &citeErr{"SOURCE needs <id> <text>"}
	errBadPattern  = &citeErr{"PATTERN needs a regex argument"}
)

func errCiteUnknownOpt(key string) error {
	return &citeErr{msg: "unknown CITE option: " + key}
}
