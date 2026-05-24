package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// evalSetCmd handles EVALSET.* — versioned golden set + diff.
func (c *conn) evalSetCmd(sub string, args []string) {
	switch sub {
	case "CREATE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'evalset.create'")
			return
		}
		if err := c.eng.EvalSet.Create(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADDCASE":
		if len(args) < 3 {
			writeError(c.bw, "usage: EVALSET.ADDCASE eval-id case-id input [EXPECTED v]")
			return
		}
		expected := ""
		for i := 3; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "EXPECTED" {
				writeError(c.bw, "unknown EVALSET.ADDCASE option: "+key)
				return
			}
			expected = args[i+1]
		}
		if err := c.eng.EvalSet.AddCase(args[0], args[1], args[2], expected); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "FREEZE":
		if len(args) < 2 {
			writeError(c.bw, "usage: EVALSET.FREEZE eval-id version-tag")
			return
		}
		if err := c.eng.EvalSet.Freeze(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RECORD":
		// EVALSET.RECORD eval-id version case-id model-tag SCORE q [OUTPUT out]
		if len(args) < 6 {
			writeError(c.bw, "usage: EVALSET.RECORD eval-id version case-id model-tag SCORE q [OUTPUT out]")
			return
		}
		score := -1.0
		output := ""
		for i := 4; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "SCORE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "SCORE must be float")
					return
				}
				score = f
			case "OUTPUT":
				output = val
			default:
				writeError(c.bw, "unknown EVALSET.RECORD option: "+key)
				return
			}
		}
		if score < 0 {
			writeError(c.bw, "SCORE is required")
			return
		}
		if err := c.eng.EvalSet.Record(args[0], args[1], args[2], args[3], score, output); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DIFF":
		if len(args) < 4 {
			writeError(c.bw, "usage: EVALSET.DIFF eval-id version model-a model-b")
			return
		}
		d, err := c.eng.EvalSet.Diff(args[0], args[1], args[2], args[3])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		regs := make([]any, 0, len(d.Regressions))
		for _, r := range d.Regressions {
			regs = append(regs, []any{
				"case_id", r.CaseID,
				"score_a", strconv.FormatFloat(r.ScoreA, 'f', 4, 64),
				"score_b", strconv.FormatFloat(r.ScoreB, 'f', 4, 64),
				"delta", strconv.FormatFloat(r.Delta, 'f', 4, 64),
			})
		}
		imps := make([]any, 0, len(d.Improvements))
		for _, r := range d.Improvements {
			imps = append(imps, []any{
				"case_id", r.CaseID,
				"score_a", strconv.FormatFloat(r.ScoreA, 'f', 4, 64),
				"score_b", strconv.FormatFloat(r.ScoreB, 'f', 4, 64),
				"delta", strconv.FormatFloat(r.Delta, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"eval_id", d.EvalID,
			"version", d.Version,
			"model_a", d.ModelA,
			"model_b", d.ModelB,
			"total_a", strconv.Itoa(d.TotalA),
			"total_b", strconv.Itoa(d.TotalB),
			"delta_mean", strconv.FormatFloat(d.DeltaMean, 'f', 4, 64),
			"no_change", strconv.Itoa(d.NoChange),
			"new_failures", d.NewFailures,
			"newly_passing", d.NewlyPassing,
			"regressions", regs,
			"improvements", imps,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'evalset.status'")
			return
		}
		st, ok := c.eng.EvalSet.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		versions := make([]any, 0, len(st.Versions))
		for _, v := range st.Versions {
			versions = append(versions, []any{
				"version", v.Version,
				"cases", strconv.Itoa(v.Cases),
				"models", v.Models,
				"frozen_at", strconv.FormatInt(v.FrozenAt, 10),
			})
		}
		writeValue(c.bw, []any{
			"eval_id", st.EvalID,
			"draft_cases", strconv.Itoa(st.DraftCases),
			"versions", versions,
		})
	case "LIST":
		writeArray(c.bw, c.eng.EvalSet.List())
	case "DROP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'evalset.drop' (use ALL or eval-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.EvalSet.Drop(args[0])))
	case "STATS":
		s := c.eng.EvalSet.Stats()
		writeArray(c.bw, []string{
			"sets", strconv.Itoa(s.Sets),
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_freezes", strconv.FormatInt(s.TotalFreezes, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_diffs", strconv.FormatInt(s.TotalDiffs, 10),
		})
	default:
		writeError(c.bw, "unknown EVALSET subcommand: "+sub)
	}
}

// adaptLatencyCmd handles ADAPT.LATENCY.* — latency-driven downgrader.
func (c *conn) adaptLatencyCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'adapt.latency.config'")
			return
		}
		var targets []llmstack.AdaptLatencyTarget
		var window time.Duration
		var minN int
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TARGETS":
				// model:cost,model:cost,...
				for _, item := range strings.Split(val, ",") {
					item = strings.TrimSpace(item)
					if item == "" {
						continue
					}
					colon := strings.IndexByte(item, ':')
					if colon <= 0 || colon == len(item)-1 {
						writeError(c.bw, "TARGETS entry must be model:cost, got: "+item)
						return
					}
					cost, err := strconv.ParseFloat(item[colon+1:], 64)
					if err != nil {
						writeError(c.bw, "TARGETS cost must be float: "+item)
						return
					}
					targets = append(targets, llmstack.AdaptLatencyTarget{
						Model: item[:colon], Cost: cost,
					})
				}
			case "WINDOW":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "WINDOW must be non-negative integer (seconds)")
					return
				}
				window = time.Duration(n) * time.Second
			case "MIN_SAMPLES":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "MIN_SAMPLES must be non-negative integer")
					return
				}
				minN = n
			default:
				writeError(c.bw, "unknown ADAPT.LATENCY.CONFIG option: "+key)
				return
			}
		}
		if err := c.eng.AdaptLatency.ConfigurePublic(args[0], targets, window, minN); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "OBSERVE":
		if len(args) < 3 {
			writeError(c.bw, "usage: ADAPT.LATENCY.OBSERVE policy-id model latency_ms")
			return
		}
		ms, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "latency_ms must be integer")
			return
		}
		if err := c.eng.AdaptLatency.Observe(args[0], args[1], ms); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PICK":
		if len(args) < 3 || !strings.EqualFold(args[1], "TARGET_P99_MS") {
			writeError(c.bw, "usage: ADAPT.LATENCY.PICK policy-id TARGET_P99_MS n")
			return
		}
		ms, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "TARGET_P99_MS must be integer")
			return
		}
		d, err := c.eng.AdaptLatency.Pick(args[0], ms)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		demotedInt := "0"
		if d.Demoted {
			demotedInt = "1"
		}
		writeArray(c.bw, []string{
			"policy_id", d.PolicyID,
			"model", d.Model,
			"cost", strconv.FormatFloat(d.Cost, 'g', -1, 64),
			"p99_ms", strconv.FormatFloat(d.P99MS, 'f', 2, 64),
			"samples", strconv.Itoa(d.Samples),
			"target_p99_ms", strconv.FormatInt(d.TargetMS, 10),
			"reason", d.Reason,
			"demoted", demotedInt,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'adapt.latency.status'")
			return
		}
		rows, ok := c.eng.AdaptLatency.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"model", r.Model,
				"cost", strconv.FormatFloat(r.Cost, 'g', -1, 64),
				"samples", strconv.Itoa(r.Samples),
				"p50_ms", strconv.FormatFloat(r.P50, 'f', 2, 64),
				"p95_ms", strconv.FormatFloat(r.P95, 'f', 2, 64),
				"p99_ms", strconv.FormatFloat(r.P99, 'f', 2, 64),
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		writeArray(c.bw, c.eng.AdaptLatency.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'adapt.latency.reset' (use ALL or policy-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.AdaptLatency.Reset(args[0])))
	case "STATS":
		s := c.eng.AdaptLatency.Stats()
		writeArray(c.bw, []string{
			"policies", strconv.Itoa(s.Policies),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
			"total_picks", strconv.FormatInt(s.TotalPicks, 10),
			"total_demotes", strconv.FormatInt(s.TotalDemotes, 10),
		})
	default:
		writeError(c.bw, "unknown ADAPT.LATENCY subcommand: "+sub)
	}
}

// sessionClusterCmd handles SESSION.CLUSTER.* — semantic cohort analytics.
func (c *conn) sessionClusterCmd(sub string, args []string) {
	switch sub {
	case "OBSERVE":
		if len(args) < 3 {
			writeError(c.bw, "usage: SESSION.CLUSTER.OBSERVE cluster-id session-id request [MIN_SIM f]")
			return
		}
		minSim := 0.0
		for i := 3; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "MIN_SIM" {
				writeError(c.bw, "unknown SESSION.CLUSTER.OBSERVE option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || f < 0 || f > 1 {
				writeError(c.bw, "MIN_SIM must be float in [0,1]")
				return
			}
			minSim = f
		}
		if err := c.eng.SessionCluster.Observe(args[0], args[1], args[2], minSim); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "TOP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'session.cluster.top'")
			return
		}
		limit := 10
		var window time.Duration
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "LIMIT":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "LIMIT must be positive integer")
					return
				}
				limit = n
			case "WINDOW":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "WINDOW must be non-negative integer (seconds)")
					return
				}
				window = time.Duration(n) * time.Second
			default:
				writeError(c.bw, "unknown SESSION.CLUSTER.TOP option: "+key)
				return
			}
		}
		rows, ok := c.eng.SessionCluster.Top(args[0], limit, window)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"cohort_id", r.CohortID,
				"sample_query", r.SampleQuery,
				"member_sessions", strconv.Itoa(r.Members),
				"observations", strconv.Itoa(r.Observations),
				"last_seen", strconv.FormatInt(r.LastSeen, 10),
				"age_seconds", strconv.FormatInt(r.AgeSeconds, 10),
			})
		}
		writeValue(c.bw, out)
	case "MEMBERS":
		if len(args) < 2 {
			writeError(c.bw, "usage: SESSION.CLUSTER.MEMBERS cluster-id cohort-id")
			return
		}
		mem, ok := c.eng.SessionCluster.Members(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, mem)
	case "STATUS":
		if len(args) < 2 {
			writeError(c.bw, "usage: SESSION.CLUSTER.STATUS cluster-id session-id")
			return
		}
		st, ok := c.eng.SessionCluster.Status(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"session_id", st.SessionID,
			"cohort_id", st.CohortID,
		})
	case "LIST":
		writeArray(c.bw, c.eng.SessionCluster.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'session.cluster.reset' (use ALL or cluster-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.SessionCluster.Reset(args[0])))
	case "STATS":
		s := c.eng.SessionCluster.Stats()
		writeArray(c.bw, []string{
			"clusters", strconv.Itoa(s.Clusters),
			"total_cohorts", strconv.Itoa(s.TotalCohorts),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
		})
	default:
		writeError(c.bw, "unknown SESSION.CLUSTER subcommand: "+sub)
	}
}
