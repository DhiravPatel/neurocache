package resp

import (
	"strconv"
	"strings"
)

// Phase 15 handlers — Part 3 of 4. SANDBOX + WMARK.EMBED + RECALL.

// sandboxCmd handles SANDBOX.* — traffic replay dry-run.
func (c *conn) sandboxCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 7 {
			writeError(c.bw, "usage: SANDBOX.RECORD id request-id input route quality cost-usd latency-ms")
			return
		}
		quality, err := strconv.ParseFloat(args[4], 64)
		if err != nil {
			writeError(c.bw, "quality must be float")
			return
		}
		cost, err := strconv.ParseFloat(args[5], 64)
		if err != nil {
			writeError(c.bw, "cost-usd must be float")
			return
		}
		lat, err := strconv.ParseFloat(args[6], 64)
		if err != nil {
			writeError(c.bw, "latency-ms must be float")
			return
		}
		if err := c.eng.Sandbox.Record(args[0], args[1], args[2], args[3], quality, cost, lat); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SET_ROUTE":
		if len(args) < 3 {
			writeError(c.bw, "usage: SANDBOX.SET_ROUTE id input-substring new-route")
			return
		}
		if err := c.eng.Sandbox.SetRoute(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNSET_ROUTE":
		if len(args) < 2 {
			writeError(c.bw, "usage: SANDBOX.UNSET_ROUTE id input-substring")
			return
		}
		writeInt(c.bw, int64(c.eng.Sandbox.UnsetRoute(args[0], args[1])))
	case "SET_PROJECTION":
		if len(args) < 5 {
			writeError(c.bw, "usage: SANDBOX.SET_PROJECTION id route quality-scale cost-scale latency-scale")
			return
		}
		q, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "quality-scale must be float")
			return
		}
		cs, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "cost-scale must be float")
			return
		}
		ls, err := strconv.ParseFloat(args[4], 64)
		if err != nil {
			writeError(c.bw, "latency-scale must be float")
			return
		}
		if err := c.eng.Sandbox.SetProjection(args[0], args[1], q, cs, ls); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RULES":
		if len(args) < 1 {
			writeError(c.bw, "usage: SANDBOX.RULES id")
			return
		}
		rows := c.eng.Sandbox.Rules(args[0])
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{"match", r.Match, "new_route", r.NewRoute})
		}
		writeValue(c.bw, out)
	case "REPLAY":
		if len(args) < 1 {
			writeError(c.bw, "usage: SANDBOX.REPLAY id")
			return
		}
		r, ok := c.eng.Sandbox.Replay(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		per := make([]any, 0, len(r.PerRoute))
		for k, v := range r.PerRoute {
			per = append(per, []any{
				"route", k,
				"before_count", strconv.Itoa(v.BeforeCount),
				"after_count", strconv.Itoa(v.AfterCount),
				"cost_before_total_usd", strconv.FormatFloat(v.CostBefore, 'f', 6, 64),
				"cost_after_total_usd", strconv.FormatFloat(v.CostAfter, 'f', 6, 64),
			})
		}
		writeValue(c.bw, []any{
			"sandbox_id", r.SandboxID,
			"requests_replayed", strconv.Itoa(r.RequestsReplayed),
			"changed_count", strconv.Itoa(r.ChangedCount),
			"cost_delta_total_usd", strconv.FormatFloat(r.CostDeltaTotal, 'f', 6, 64),
			"quality_delta_avg", strconv.FormatFloat(r.QualityDeltaAvg, 'f', 4, 64),
			"latency_delta_avg_ms", strconv.FormatFloat(r.LatencyDeltaAvg, 'f', 2, 64),
			"per_route_breakdown", per,
		})
	case "SIZE":
		if len(args) < 1 {
			writeError(c.bw, "usage: SANDBOX.SIZE id")
			return
		}
		n, ok := c.eng.Sandbox.Size(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeInt(c.bw, int64(n))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: SANDBOX.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Sandbox.Forget(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Sandbox.List())
	case "STATS":
		s := c.eng.Sandbox.Stats()
		writeArray(c.bw, []string{
			"sandboxes", strconv.Itoa(s.Sandboxes),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_replays", strconv.FormatInt(s.TotalReplays, 10),
			"buffered_requests", strconv.Itoa(s.BufferedReqs),
		})
	default:
		writeError(c.bw, "unknown SANDBOX subcommand: "+sub)
	}
}

// wmarkEmbedCmd handles WMARK.EMBED, DETECT, KEY, etc.
func (c *conn) wmarkEmbedCmd(sub string, args []string) {
	switch sub {
	case "EMBED":
		if len(args) < 1 {
			writeError(c.bw, "usage: WMARK.EMBED text [KEY k] [STRENGTH 0..1]")
			return
		}
		text := args[0]
		key := ""
		strength := 0.7
		for i := 1; i+1 <= len(args); i += 2 {
			tag := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, tag+" needs a value")
				return
			}
			switch tag {
			case "KEY":
				key = args[i+1]
			case "STRENGTH":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "STRENGTH must be float")
					return
				}
				strength = f
			default:
				writeError(c.bw, "unknown WMARK.EMBED option: "+tag)
				return
			}
		}
		if key == "" {
			writeError(c.bw, "KEY required")
			return
		}
		r, err := c.eng.WmarkEmbed.Embed(text, key, strength)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"text", r.Text,
			"replacements", strconv.Itoa(r.Replacements),
			"green_rate", strconv.FormatFloat(r.GreenRate, 'f', 4, 64),
		})
	case "DETECT":
		if len(args) < 1 {
			writeError(c.bw, "usage: WMARK.DETECT text [KEY k]")
			return
		}
		text := args[0]
		key := ""
		for i := 1; i+1 <= len(args); i += 2 {
			tag := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, tag+" needs a value")
				return
			}
			if tag != "KEY" {
				writeError(c.bw, "unknown WMARK.DETECT option: "+tag)
				return
			}
			key = args[i+1]
		}
		if key == "" {
			writeError(c.bw, "KEY required")
			return
		}
		r, err := c.eng.WmarkEmbed.Detect(text, key)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		wm := "0"
		if r.Watermarked {
			wm = "1"
		}
		writeArray(c.bw, []string{
			"green_rate", strconv.FormatFloat(r.GreenRate, 'f', 4, 64),
			"baseline", strconv.FormatFloat(r.Baseline, 'f', 4, 64),
			"z_score", strconv.FormatFloat(r.ZScore, 'f', 4, 64),
			"n", strconv.Itoa(r.N),
			"watermarked", wm,
			"confidence", r.Confidence,
		})
	case "KEY":
		if len(args) < 3 || strings.ToUpper(args[1]) != "PUBLISH" {
			writeError(c.bw, "usage: WMARK.KEY register-id PUBLISH key")
			return
		}
		if err := c.eng.WmarkEmbed.Key(args[0], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "KEYS":
		writeArray(c.bw, c.eng.WmarkEmbed.Keys())
	case "DROPKEY":
		if len(args) < 1 {
			writeError(c.bw, "usage: WMARK.DROPKEY register-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.WmarkEmbed.DropKey(args[0])))
	case "STATS":
		s := c.eng.WmarkEmbed.Stats()
		writeArray(c.bw, []string{
			"registered_keys", strconv.Itoa(s.RegisteredKeys),
			"total_embeds", strconv.FormatInt(s.TotalEmbeds, 10),
			"total_detects", strconv.FormatInt(s.TotalDetects, 10),
			"total_marks", strconv.FormatInt(s.TotalMarks, 10),
		})
	default:
		writeError(c.bw, "unknown WMARK subcommand: "+sub)
	}
}

// recallCmd handles RECALL.* — drift-driven proactive invalidation.
func (c *conn) recallCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "usage: RECALL.REGISTER answer-id model-version [PROMPT v] [EMBED v] [AT unix-ms]")
			return
		}
		promptVer := ""
		embedVer := ""
		var atMS int64
		for i := 2; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "PROMPT":
				promptVer = args[i+1]
			case "EMBED":
				embedVer = args[i+1]
			case "AT":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "AT must be non-negative integer (unix ms)")
					return
				}
				atMS = n
			default:
				writeError(c.bw, "unknown RECALL.REGISTER option: "+key)
				return
			}
		}
		if err := c.eng.Recall.Register(args[0], args[1], promptVer, embedVer, atMS); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "MARK":
		// RECALL.MARK id REASON "text" FROM ms TO ms [HALF_LIFE_S s] [SCOPE s]
		if len(args) < 7 || strings.ToUpper(args[1]) != "REASON" || strings.ToUpper(args[3]) != "FROM" || strings.ToUpper(args[5]) != "TO" {
			writeError(c.bw, "usage: RECALL.MARK id REASON \"text\" FROM ms TO ms [HALF_LIFE_S s] [SCOPE s]")
			return
		}
		from, err := strconv.ParseInt(args[4], 10, 64)
		if err != nil {
			writeError(c.bw, "FROM must be integer")
			return
		}
		to, err := strconv.ParseInt(args[6], 10, 64)
		if err != nil {
			writeError(c.bw, "TO must be integer")
			return
		}
		halfLife := 0.0
		scope := ""
		for i := 7; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "HALF_LIFE_S":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "HALF_LIFE_S must be float")
					return
				}
				halfLife = f
			case "SCOPE":
				scope = args[i+1]
			default:
				writeError(c.bw, "unknown RECALL.MARK option: "+key)
				return
			}
		}
		if err := c.eng.Recall.Mark(args[0], args[2], from, to, halfLife, scope); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SCAN":
		minConf := 0.0
		limit := 100
		scope := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "MIN_CONFIDENCE":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "MIN_CONFIDENCE must be float")
					return
				}
				minConf = f
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			case "SCOPE":
				scope = args[i+1]
			default:
				writeError(c.bw, "unknown RECALL.SCAN option: "+key)
				return
			}
		}
		rows := c.eng.Recall.Scan(minConf, limit, scope)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"answer_id", r.AnswerID,
				"recall_confidence", strconv.FormatFloat(r.Confidence, 'f', 4, 64),
				"reason", r.Reason,
				"change_id", r.ChangeID,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: RECALL.FORGET answer-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Recall.Forget(args[0])))
	case "UNMARK":
		if len(args) < 1 {
			writeError(c.bw, "usage: RECALL.UNMARK change-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Recall.Unmark(args[0])))
	case "STATS":
		s := c.eng.Recall.Stats()
		writeArray(c.bw, []string{
			"answers", strconv.Itoa(s.Answers),
			"events", strconv.Itoa(s.Events),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
			"total_scans", strconv.FormatInt(s.TotalScans, 10),
		})
	default:
		writeError(c.bw, "unknown RECALL subcommand: "+sub)
	}
}
