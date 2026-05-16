package resp

import (
	"bufio"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// toolDriftCmd handles TOOLDRIFT.* — tool/API response drift watcher.
func (c *conn) toolDriftCmd(sub string, args []string) {
	switch sub {
	case "BASELINE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'tooldrift.baseline' (need tool-id payload [payload...])")
			return
		}
		if err := c.eng.ToolDrift.Baseline(args[0], args[1:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SAMPLE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'tooldrift.sample' (need tool-id payload)")
			return
		}
		r, err := c.eng.ToolDrift.Sample(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeToolDriftResult(c.bw, r)
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'tooldrift.check' (need tool-id payload)")
			return
		}
		r, err := c.eng.ToolDrift.Check(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeToolDriftResult(c.bw, r)
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'tooldrift.status'")
			return
		}
		st, ok := c.eng.ToolDrift.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"tool_id", st.ToolID,
			"baseline_size", strconv.Itoa(st.BaselineSize),
			"recent_size", strconv.Itoa(st.RecentSize),
			"last_verdict", st.LastVerdict,
			"last_score", strconv.FormatFloat(st.LastScore, 'f', 4, 64),
			"last_ts", strconv.FormatInt(st.LastTS, 10),
		})
	case "RECENT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'tooldrift.recent'")
			return
		}
		limit := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown TOOLDRIFT.RECENT option: "+key)
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
		rows, ok := c.eng.ToolDrift.Recent(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"ts", strconv.FormatInt(r.TS, 10),
				"verdict", r.Verdict,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"payload", r.Payload,
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		writeArray(c.bw, c.eng.ToolDrift.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'tooldrift.reset'")
			return
		}
		if c.eng.ToolDrift.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.ToolDrift.Stats()
		writeArray(c.bw, []string{
			"tools", strconv.Itoa(s.Tools),
			"total_samples", strconv.FormatInt(s.TotalSamples, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_drifts_detected", strconv.FormatInt(s.TotalDrifts, 10),
		})
	default:
		writeError(c.bw, "unknown TOOLDRIFT subcommand: "+sub)
	}
}

// answerCanaryCmd handles ANSWER.CANARY.* — prompt/model A/B.
func (c *conn) answerCanaryCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'answer.canary.config'")
			return
		}
		baseline, canary := "", ""
		rate := -1.0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "BASELINE":
				baseline = val
			case "CANARY":
				canary = val
			case "RATE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f < 0 || f > 1 {
					writeError(c.bw, "RATE must be float in [0,1]")
					return
				}
				rate = f
			default:
				writeError(c.bw, "unknown ANSWER.CANARY.CONFIG option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.AnswerCanary.Configure(args[0], baseline, canary, rate); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ROUTE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'answer.canary.route' (need exp-id request-id)")
			return
		}
		v, err := c.eng.AnswerCanary.Route(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, v)
	case "RECORD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'answer.canary.record' (need exp-id variant quality)")
			return
		}
		quality, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "quality must be float")
			return
		}
		var latency int64
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "LATENCY_MS":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "LATENCY_MS must be non-negative integer")
					return
				}
				latency = n
			case "REQUEST_ID":
				// Not stored — accepted for callsite ergonomics.
			default:
				writeError(c.bw, "unknown ANSWER.CANARY.RECORD option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.AnswerCanary.Record(args[0], args[1], quality, latency); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REPORT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'answer.canary.report'")
			return
		}
		rep, err := c.eng.AnswerCanary.Report(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeValue(c.bw, []any{
			"experiment_id", rep.ExperimentID,
			"canary_rate", strconv.FormatFloat(rep.Rate, 'f', 4, 64),
			"quality_lift", strconv.FormatFloat(rep.QualityLift, 'f', 4, 64),
			"latency_lift_ms", strconv.FormatFloat(rep.LatencyLift, 'f', 2, 64),
			"baseline", variantArr(rep.Baseline),
			"canary", variantArr(rep.Canary),
		})
	case "DECIDE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'answer.canary.decide'")
			return
		}
		d, err := c.eng.AnswerCanary.Decide(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"experiment_id", d.ExperimentID,
			"decision", d.Decision,
			"reason", d.Reason,
			"z_score", strconv.FormatFloat(d.Z, 'f', 4, 64),
			"quality_lift", strconv.FormatFloat(d.QualityLift, 'f', 4, 64),
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'answer.canary.reset'")
			return
		}
		if c.eng.AnswerCanary.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		writeArray(c.bw, c.eng.AnswerCanary.List())
	case "STATS":
		s := c.eng.AnswerCanary.Stats()
		writeArray(c.bw, []string{
			"experiments", strconv.Itoa(s.Experiments),
			"total_routes", strconv.FormatInt(s.TotalRoutes, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
		})
	default:
		writeError(c.bw, "unknown ANSWER.CANARY subcommand: "+sub)
	}
}

// retrievalLearnCmd handles RETRIEVAL.LEARN.* — closed-loop re-rank.
func (c *conn) retrievalLearnCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'retrieval.learn.record' (need chunk-id cited|not_cited [SCORE q])")
			return
		}
		cited := strings.EqualFold(args[1], "cited") || args[1] == "1" || strings.EqualFold(args[1], "true")
		quality := 0.0
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "SCORE" {
				writeError(c.bw, "unknown RETRIEVAL.LEARN.RECORD option: "+key)
				return
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				writeError(c.bw, "SCORE must be float")
				return
			}
			quality = f
			i += 2
		}
		if err := c.eng.RetrievalLearn.Record(args[0], cited, quality); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RERANK":
		if len(args) < 2 || len(args)%2 != 0 {
			writeError(c.bw, "usage: RETRIEVAL.LEARN.RERANK chunk-id score [chunk-id score ...]")
			return
		}
		rows := make([]llmstack.RerankRow, 0, len(args)/2)
		for i := 0; i < len(args); i += 2 {
			score, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				writeError(c.bw, "score must be float at arg "+strconv.Itoa(i+1))
				return
			}
			rows = append(rows, llmstack.RerankRow{ChunkID: args[i], Score: score})
		}
		out := c.eng.RetrievalLearn.Rerank(rows)
		arr := make([]any, 0, len(out))
		for _, r := range out {
			arr = append(arr, []any{
				"chunk_id", r.ChunkID,
				"original", strconv.FormatFloat(r.Original, 'f', 4, 64),
				"boost", strconv.FormatFloat(r.Boost, 'f', 4, 64),
				"reranked", strconv.FormatFloat(r.Reranked, 'f', 4, 64),
			})
		}
		writeValue(c.bw, arr)
	case "WEIGHT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'retrieval.learn.weight'")
			return
		}
		w := c.eng.RetrievalLearn.Weight(args[0])
		writeBulk(c.bw, strconv.FormatFloat(w, 'f', 4, 64))
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'retrieval.learn.status'")
			return
		}
		st, ok := c.eng.RetrievalLearn.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"chunk_id", st.ChunkID,
			"cited_rate", strconv.FormatFloat(st.CitedRate, 'f', 4, 64),
			"weight", strconv.FormatFloat(st.Weight, 'f', 4, 64),
			"samples", strconv.FormatInt(st.Samples, 10),
			"cited_count", strconv.FormatInt(st.CitedCount, 10),
		})
	case "TOP", "BOTTOM":
		limit := 10
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				writeError(c.bw, "LIMIT must be positive integer")
				return
			}
			limit = n
			i += 2
		}
		var rows []llmstack.RetrievalLearnRow
		if sub == "TOP" {
			rows = c.eng.RetrievalLearn.Top(limit)
		} else {
			rows = c.eng.RetrievalLearn.Bottom(limit)
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"chunk_id", r.ChunkID,
				"cited_rate", strconv.FormatFloat(r.CitedRate, 'f', 4, 64),
				"weight", strconv.FormatFloat(r.Weight, 'f', 4, 64),
				"samples", strconv.FormatInt(r.Samples, 10),
			})
		}
		writeValue(c.bw, out)
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'retrieval.learn.reset' (use ALL or chunk-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.RetrievalLearn.Reset(args[0])))
	case "ALPHA":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'retrieval.learn.alpha'")
			return
		}
		a, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "alpha must be float")
			return
		}
		if err := c.eng.RetrievalLearn.SetAlpha(a); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.RetrievalLearn.Stats()
		writeArray(c.bw, []string{
			"chunks", strconv.Itoa(s.Chunks),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_reranks", strconv.FormatInt(s.TotalReranks, 10),
			"mean_weight", strconv.FormatFloat(s.MeanWeight, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown RETRIEVAL.LEARN subcommand: "+sub)
	}
}

// ─── shared helpers ────────────────────────────────────────────

func writeToolDriftResult(bw *bufio.Writer, r llmstack.ToolDriftResult) {
	writeArray(bw, []string{
		"drift_score", strconv.FormatFloat(r.DriftScore, 'f', 4, 64),
		"verdict", r.Verdict,
		"signature_size", strconv.Itoa(r.SignatureSize),
		"baseline_size", strconv.Itoa(r.BaselineSize),
	})
}

func variantArr(v llmstack.CanaryVariantRow) []any {
	return []any{
		"name", v.Name,
		"n", strconv.FormatInt(v.N, 10),
		"mean_quality", strconv.FormatFloat(v.MeanQuality, 'f', 4, 64),
		"stddev_quality", strconv.FormatFloat(v.StddevQual, 'f', 4, 64),
		"mean_latency_ms", strconv.FormatFloat(v.MeanLatency, 'f', 2, 64),
		"max_latency_ms", strconv.FormatInt(v.MaxLatency, 10),
	}
}
