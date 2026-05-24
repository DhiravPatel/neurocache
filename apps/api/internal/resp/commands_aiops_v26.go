package resp

import (
	"strconv"
	"strings"
	"time"
)

// shadowEvalCmd handles SHADOW.EVAL.* — mirror evaluation.
// (The existing SHADOW.* family at commands_aiops.go is the
// stale-while-revalidate cache; this one is namespaced as
// SHADOW.EVAL.* to avoid the collision.)
func (c *conn) shadowEvalCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'shadow.eval.config'")
			return
		}
		baseline, candidate := "", ""
		regressionThreshold, sampleRate := 0.0, 0.0
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
			case "CANDIDATE":
				candidate = val
			case "REGRESSION_THRESHOLD":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f <= 0 || f > 1 {
					writeError(c.bw, "REGRESSION_THRESHOLD must be float in (0,1]")
					return
				}
				regressionThreshold = f
			case "SAMPLE_RATE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f <= 0 || f > 1 {
					writeError(c.bw, "SAMPLE_RATE must be float in (0,1]")
					return
				}
				sampleRate = f
			default:
				writeError(c.bw, "unknown SHADOW.CONFIG option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.ShadowEval.Configure(args[0], baseline, candidate, regressionThreshold, sampleRate); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "MIRROR":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'shadow.mirror' (need exp-id req-id input)")
			return
		}
		v, err := c.eng.ShadowEval.Mirror(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, v)
	case "RECORD":
		// SHADOW.RECORD exp req BASELINE q CANDIDATE q [LATENCY_BASELINE_MS n] [LATENCY_CANDIDATE_MS n]
		if len(args) < 6 {
			writeError(c.bw, "usage: SHADOW.RECORD exp req BASELINE q CANDIDATE q [LATENCY_BASELINE_MS n] [LATENCY_CANDIDATE_MS n]")
			return
		}
		baselineQ, candidateQ := -1.0, -1.0
		latBase, latCand := int64(0), int64(0)
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "BASELINE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "BASELINE quality must be float")
					return
				}
				baselineQ = f
			case "CANDIDATE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "CANDIDATE quality must be float")
					return
				}
				candidateQ = f
			case "LATENCY_BASELINE_MS":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "LATENCY_BASELINE_MS must be non-negative integer")
					return
				}
				latBase = n
			case "LATENCY_CANDIDATE_MS":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "LATENCY_CANDIDATE_MS must be non-negative integer")
					return
				}
				latCand = n
			default:
				writeError(c.bw, "unknown SHADOW.RECORD option: "+key)
				return
			}
		}
		if baselineQ < 0 || candidateQ < 0 {
			writeError(c.bw, "BASELINE and CANDIDATE quality required")
			return
		}
		if err := c.eng.ShadowEval.Record(args[0], args[1], baselineQ, candidateQ, latBase, latCand); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REPORT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'shadow.report'")
			return
		}
		regLimit := 10
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "REGRESSION_LIMIT" {
				writeError(c.bw, "unknown SHADOW.REPORT option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "REGRESSION_LIMIT must be non-negative integer")
				return
			}
			regLimit = n
		}
		rep, err := c.eng.ShadowEval.Report(args[0], regLimit)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		regs := make([]any, 0, len(rep.Regressions))
		for _, r := range rep.Regressions {
			regs = append(regs, []any{
				"req_id", r.ReqID,
				"baseline", strconv.FormatFloat(r.Baseline, 'f', 4, 64),
				"candidate", strconv.FormatFloat(r.Candidate, 'f', 4, 64),
				"diff", strconv.FormatFloat(r.Diff, 'f', 4, 64),
				"ts", strconv.FormatInt(r.TS, 10),
			})
		}
		writeValue(c.bw, []any{
			"experiment_id", rep.ExperimentID,
			"baseline_name", rep.BaselineName,
			"candidate_name", rep.CandidateName,
			"n", strconv.FormatInt(rep.N, 10),
			"win_rate_candidate", strconv.FormatFloat(rep.WinRateCandidate, 'f', 4, 64),
			"mean_lift", strconv.FormatFloat(rep.MeanLift, 'f', 4, 64),
			"baseline_mean", strconv.FormatFloat(rep.BaselineMean, 'f', 4, 64),
			"candidate_mean", strconv.FormatFloat(rep.CandidateMean, 'f', 4, 64),
			"baseline_stddev", strconv.FormatFloat(rep.BaselineStddev, 'f', 4, 64),
			"candidate_stddev", strconv.FormatFloat(rep.CandidateStddev, 'f', 4, 64),
			"latency_lift_ms", strconv.FormatFloat(rep.LatencyLiftMS, 'f', 2, 64),
			"regressions_count", strconv.Itoa(rep.RegressionsCount),
			"regressions", regs,
		})
	case "PROMOTE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'shadow.promote'")
			return
		}
		rate := 0.0
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "RATE" {
				writeError(c.bw, "unknown SHADOW.PROMOTE option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || f <= 0 || f > 1 {
				writeError(c.bw, "RATE must be float in (0,1]")
				return
			}
			rate = f
		}
		p, err := c.eng.ShadowEval.Promote(args[0], rate)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"experiment_id", p.ExperimentID,
			"baseline_name", p.BaselineName,
			"candidate_name", p.CandidateName,
			"suggested_rate", strconv.FormatFloat(p.SuggestedRate, 'f', 4, 64),
			"based_on_n", strconv.FormatInt(p.BasedOnN, 10),
			"mean_lift", strconv.FormatFloat(p.MeanLift, 'f', 4, 64),
			"verdict", p.Verdict,
			"reason", p.Reason,
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'shadow.reset'")
			return
		}
		if c.eng.ShadowEval.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		writeArray(c.bw, c.eng.ShadowEval.List())
	case "STATS":
		s := c.eng.ShadowEval.Stats()
		writeArray(c.bw, []string{
			"experiments", strconv.Itoa(s.Experiments),
			"total_mirrors", strconv.FormatInt(s.TotalMirrors, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
		})
	default:
		writeError(c.bw, "unknown SHADOW subcommand: "+sub)
	}
}

// batchCmd handles BATCH.* — micro-batch accumulator.
func (c *conn) batchCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'batch.config'")
			return
		}
		var maxWait time.Duration
		var maxSize int
		var costPerCall, costPerItem float64
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "MAXWAIT_MS":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "MAXWAIT_MS must be non-negative integer")
					return
				}
				maxWait = time.Duration(n) * time.Millisecond
			case "MAXSIZE":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "MAXSIZE must be non-negative integer")
					return
				}
				maxSize = n
			case "COST_PER_CALL":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f < 0 {
					writeError(c.bw, "COST_PER_CALL must be non-negative")
					return
				}
				costPerCall = f
			case "COST_PER_ITEM":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f < 0 {
					writeError(c.bw, "COST_PER_ITEM must be non-negative")
					return
				}
				costPerItem = f
			default:
				writeError(c.bw, "unknown BATCH.CONFIG option: "+key)
				return
			}
		}
		if err := c.eng.Batch.Configure(args[0], maxWait, maxSize, costPerCall, costPerItem); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'batch.add' (need bucket item payload)")
			return
		}
		r, err := c.eng.Batch.Add(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		readyInt := "0"
		if r.Ready {
			readyInt = "1"
		}
		writeArray(c.bw, []string{
			"batch_id", r.BatchID,
			"slot", strconv.Itoa(r.Slot),
			"ready", readyInt,
			"age_ms", strconv.FormatInt(r.AgeMS, 10),
		})
	case "FLUSH":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'batch.flush'")
			return
		}
		out, ok := c.eng.Batch.Flush(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		items := make([]any, 0, len(out.Items))
		for _, it := range out.Items {
			items = append(items, []any{
				"item_id", it.ItemID,
				"payload", it.Payload,
			})
		}
		writeValue(c.bw, []any{
			"batch_id", out.BatchID,
			"items", items,
		})
	case "PEEK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'batch.peek'")
			return
		}
		p, ok := c.eng.Batch.Peek(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		readyInt := "0"
		if p.Ready {
			readyInt = "1"
		}
		writeArray(c.bw, []string{
			"batch_id", p.BatchID,
			"size", strconv.Itoa(p.Size),
			"age_ms", strconv.FormatInt(p.AgeMS, 10),
			"ready", readyInt,
		})
	case "RESOLVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'batch.resolve' (need bucket batch-id [RESULTS r1 r2 ...])")
			return
		}
		resultsCount := 0
		for i := 2; i < len(args); i++ {
			if strings.EqualFold(args[i], "RESULTS") {
				resultsCount = len(args) - i - 1
				break
			}
		}
		if err := c.eng.Batch.Resolve(args[0], args[1], resultsCount); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BUCKETS":
		writeArray(c.bw, c.eng.Batch.Buckets())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'batch.reset' (use ALL or bucket-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.Batch.Reset(args[0])))
	case "STATS":
		s := c.eng.Batch.Stats()
		per := make([]any, 0, len(s.PerBucket))
		for _, b := range s.PerBucket {
			per = append(per, []any{
				"bucket_id", b.BucketID,
				"total_items", strconv.FormatInt(b.TotalItems, 10),
				"total_calls", strconv.FormatInt(b.TotalCalls, 10),
				"calls_saved", strconv.FormatInt(b.SavedCalls, 10),
				"avg_batch", strconv.FormatFloat(b.AvgBatch, 'f', 4, 64),
				"saved_usd", strconv.FormatFloat(b.SavedUSD, 'f', 6, 64),
			})
		}
		writeValue(c.bw, []any{
			"buckets", strconv.Itoa(s.Buckets),
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_flushes", strconv.FormatInt(s.TotalFlushes, 10),
			"total_resolves", strconv.FormatInt(s.TotalResolves, 10),
			"per_bucket", per,
		})
	default:
		writeError(c.bw, "unknown BATCH subcommand: "+sub)
	}
}

// memConflictCmd handles MEMORY.CONFLICT.* — contradiction detection.
func (c *conn) memConflictCmd(sub string, args []string) {
	switch sub {
	case "ADD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'memory.conflict.add' (need key text [ID id])")
			return
		}
		id := ""
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "ID" {
				writeError(c.bw, "unknown MEMORY.CONFLICT.ADD option: "+key)
				return
			}
			id = args[i+1]
		}
		newID, err := c.eng.MemConflicts.Add(args[0], args[1], id)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, newID)
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'memory.conflict.check' (need key text [STRICT 0|1])")
			return
		}
		strict := false
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STRICT" {
				writeError(c.bw, "unknown MEMORY.CONFLICT.CHECK option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || (n != 0 && n != 1) {
				writeError(c.bw, "STRICT must be 0 or 1")
				return
			}
			strict = n == 1
		}
		r, err := c.eng.MemConflicts.Check(args[0], args[1], strict)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		conflictInt := "0"
		if r.Conflict {
			conflictInt = "1"
		}
		writeArray(c.bw, []string{
			"conflict", conflictInt,
			"with", r.With,
			"with_id", r.WithID,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"resolution_hint", r.ResolutionHint,
			"reason", r.Reason,
		})
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'memory.conflict.list'")
			return
		}
		rows, ok := c.eng.MemConflicts.List(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"conflict_id", r.ConflictID,
				"newer_id", r.NewerID,
				"older_id", r.OlderID,
				"newer_text", r.NewerText,
				"older_text", r.OlderText,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"hint", r.Hint,
				"ts", strconv.FormatInt(r.TS, 10),
			})
		}
		writeValue(c.bw, out)
	case "RESOLVE":
		if len(args) < 4 || !strings.EqualFold(args[2], "KEEP") {
			writeError(c.bw, "usage: MEMORY.CONFLICT.RESOLVE key conflict-id KEEP newer|older|both")
			return
		}
		if err := c.eng.MemConflicts.Resolve(args[0], args[1], strings.ToLower(args[3])); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PURGE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'memory.conflict.purge'")
			return
		}
		if c.eng.MemConflicts.Purge(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "KEYS":
		writeArray(c.bw, c.eng.MemConflicts.Keys())
	case "STATS":
		s := c.eng.MemConflicts.Stats()
		writeArray(c.bw, []string{
			"keys", strconv.Itoa(s.Keys),
			"total_facts", strconv.Itoa(s.TotalFacts),
			"open_conflicts", strconv.Itoa(s.OpenConflicts),
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_conflicts_detected", strconv.FormatInt(s.TotalConflicts, 10),
			"total_resolves", strconv.FormatInt(s.TotalResolves, 10),
		})
	default:
		writeError(c.bw, "unknown MEMORY.CONFLICT subcommand: "+sub)
	}
}
