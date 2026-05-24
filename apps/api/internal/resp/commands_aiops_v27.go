package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// escalateCmd handles ESCALATE.* — composed escalation ladder.
func (c *conn) escalateCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 3 {
			writeError(c.bw, "usage: ESCALATE.CONFIG policy-id [CACHE_IF expr] [CHEAP_IF expr] [EXPENSIVE_IF expr] [HUMAN_IF expr]")
			return
		}
		exprs := map[string]string{}
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "CACHE_IF":
				exprs["cache"] = val
			case "CHEAP_IF":
				exprs["cheap"] = val
			case "EXPENSIVE_IF":
				exprs["expensive"] = val
			case "HUMAN_IF":
				exprs["human"] = val
			default:
				writeError(c.bw, "unknown ESCALATE.CONFIG option: "+key)
				return
			}
		}
		if err := c.eng.Escalate.Configure(args[0], exprs); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DECIDE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'escalate.decide'")
			return
		}
		signals := map[string]float64{}
		for i := 1; i < len(args); i++ {
			eq := strings.IndexByte(args[i], '=')
			if eq <= 0 || eq == len(args[i])-1 {
				writeError(c.bw, "signal must be key=value: "+args[i])
				return
			}
			name := args[i][:eq]
			val, err := strconv.ParseFloat(args[i][eq+1:], 64)
			if err != nil {
				writeError(c.bw, "signal value must be numeric for "+name)
				return
			}
			signals[name] = val
		}
		d, err := c.eng.Escalate.Decide(args[0], signals)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		sigArr := make([]any, 0, len(d.Signals)*2)
		for k, v := range d.Signals {
			sigArr = append(sigArr, k, strconv.FormatFloat(v, 'g', -1, 64))
		}
		writeValue(c.bw, []any{
			"policy_id", d.PolicyID,
			"tier", d.Tier,
			"reason", d.Reason,
			"signals", sigArr,
		})
	case "RECORD":
		if len(args) < 3 {
			writeError(c.bw, "usage: ESCALATE.RECORD policy-id tier outcome [QUALITY q]")
			return
		}
		quality := 0.0
		for i := 3; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "QUALITY" {
				writeError(c.bw, "unknown ESCALATE.RECORD option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				writeError(c.bw, "QUALITY must be float")
				return
			}
			quality = f
		}
		if err := c.eng.Escalate.Record(args[0], args[1], args[2], quality); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REPORT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'escalate.report'")
			return
		}
		rows, ok := c.eng.Escalate.Report(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"tier", r.Tier,
				"count", strconv.FormatInt(r.Count, 10),
				"mean_quality", strconv.FormatFloat(r.MeanQuality, 'f', 4, 64),
				"outcome_win", strconv.FormatInt(r.OutcomeWin, 10),
				"outcome_lose", strconv.FormatInt(r.OutcomeLose, 10),
			})
		}
		writeValue(c.bw, out)
	case "POLICY":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'escalate.policy'")
			return
		}
		rows, ok := c.eng.Escalate.Policy(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{"tier", r.Tier, "expr", r.Expr})
		}
		writeValue(c.bw, out)
	case "LIST":
		writeArray(c.bw, c.eng.Escalate.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'escalate.reset' (use ALL or policy-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.Escalate.Reset(args[0])))
	case "STATS":
		s := c.eng.Escalate.Stats()
		writeArray(c.bw, []string{
			"policies", strconv.Itoa(s.Policies),
			"total_decisions", strconv.FormatInt(s.TotalDecisions, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
		})
	default:
		writeError(c.bw, "unknown ESCALATE subcommand: "+sub)
	}
}

// forecastCmd handles FORECAST.* — cost burn-rate forecasting.
func (c *conn) forecastCmd(sub string, args []string) {
	switch sub {
	case "OBSERVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'forecast.observe' (need tenant spend-usd)")
			return
		}
		spend, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "spend must be float")
			return
		}
		if err := c.eng.Forecast.Observe(args[0], spend); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PROJECT":
		if len(args) < 5 {
			writeError(c.bw, "usage: FORECAST.PROJECT tenant WINDOW seconds CAP usd")
			return
		}
		var window time.Duration
		var cap float64
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "WINDOW":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n <= 0 {
					writeError(c.bw, "WINDOW must be positive integer (seconds)")
					return
				}
				window = time.Duration(n) * time.Second
			case "CAP":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f <= 0 {
					writeError(c.bw, "CAP must be positive")
					return
				}
				cap = f
			default:
				writeError(c.bw, "unknown FORECAST.PROJECT option: "+key)
				return
			}
		}
		p, err := c.eng.Forecast.Project(args[0], window, cap)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"tenant", p.Tenant,
			"spent", strconv.FormatFloat(p.Spent, 'f', 4, 64),
			"samples", strconv.Itoa(p.Samples),
			"window_seconds", strconv.FormatInt(p.WindowSec, 10),
			"cap", strconv.FormatFloat(p.Cap, 'f', 4, 64),
			"slope_usd_per_sec", strconv.FormatFloat(p.SlopeUSDPerSec, 'g', -1, 64),
			"rate_usd_per_day", strconv.FormatFloat(p.RateUSDPerDay, 'f', 4, 64),
			"projected_end", strconv.FormatFloat(p.ProjectedEnd, 'f', 4, 64),
			"verdict", p.Verdict,
			"breach_eta_unix", strconv.FormatInt(p.BreachETAUnix, 10),
			"headroom_days", strconv.FormatFloat(p.HeadroomDays, 'f', 4, 64),
		})
	case "ALERT":
		if len(args) < 3 || !strings.EqualFold(args[1], "AT") {
			writeError(c.bw, "usage: FORECAST.ALERT tenant AT fraction")
			return
		}
		f, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "fraction must be float")
			return
		}
		if err := c.eng.Forecast.Alert(args[0], f); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ALERTS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'forecast.alerts'")
			return
		}
		rows, ok := c.eng.Forecast.Alerts(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"fraction", strconv.FormatFloat(r.Fraction, 'f', 4, 64),
				"last_fired_unix", strconv.FormatInt(r.LastFiredUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "TENANTS":
		writeArray(c.bw, c.eng.Forecast.Tenants())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'forecast.reset' (use ALL or tenant)")
			return
		}
		writeInt(c.bw, int64(c.eng.Forecast.Reset(args[0])))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'forecast.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			writeError(c.bw, "cap must be non-negative integer")
			return
		}
		c.eng.Forecast.SetCap(n)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.Forecast.Stats()
		writeArray(c.bw, []string{
			"tenants", strconv.Itoa(s.Tenants),
			"total_ticks", strconv.Itoa(s.TotalTicks),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
			"total_projects", strconv.FormatInt(s.TotalProjects, 10),
			"cap", strconv.Itoa(s.Cap),
		})
	default:
		writeError(c.bw, "unknown FORECAST subcommand: "+sub)
	}
}

// streamWatchCmd handles STREAM.WATCH.* — degeneration detector.
func (c *conn) streamWatchCmd(sub string, args []string) {
	switch sub {
	case "OPEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.watch.open'")
			return
		}
		cfg := llmstack.StreamWatchConfigPublic{}
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "MAX_LEN":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "MAX_LEN must be non-negative integer")
					return
				}
				cfg.MaxLen = n
			case "CYCLE_THRESHOLD":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "CYCLE_THRESHOLD must be non-negative integer")
					return
				}
				cfg.CycleThreshold = n
			case "NGRAM":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "NGRAM must be non-negative integer")
					return
				}
				cfg.NGram = n
			case "NGRAM_REPEAT_THRESHOLD":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "NGRAM_REPEAT_THRESHOLD must be non-negative integer")
					return
				}
				cfg.NGramRepeatThreshold = n
			case "DIVERSITY_FLOOR":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f < 0 || f > 1 {
					writeError(c.bw, "DIVERSITY_FLOOR must be float in [0,1]")
					return
				}
				cfg.DiversityFloor = f
			case "MIN_TOKENS":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "MIN_TOKENS must be non-negative integer")
					return
				}
				cfg.MinTokens = n
			default:
				writeError(c.bw, "unknown STREAM.WATCH.OPEN option: "+key)
				return
			}
		}
		if err := c.eng.StreamWatch.OpenPublic(args[0], cfg); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "TOKEN":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'stream.watch.token' (need session-id token)")
			return
		}
		r, err := c.eng.StreamWatch.Token(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"verdict", r.Verdict,
			"reason", r.Reason,
			"length", strconv.Itoa(r.Length),
			"repeat_count", strconv.Itoa(r.RepeatCount),
			"unique_ratio", strconv.FormatFloat(r.UniqueRatio, 'f', 4, 64),
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.watch.status'")
			return
		}
		st, ok := c.eng.StreamWatch.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		stoppedInt := "0"
		if st.Stopped {
			stoppedInt = "1"
		}
		writeArray(c.bw, []string{
			"session_id", st.SessionID,
			"length", strconv.Itoa(st.Length),
			"unique_tokens", strconv.Itoa(st.UniqueTokens),
			"unique_ratio", strconv.FormatFloat(st.UniqueRatio, 'f', 4, 64),
			"cycle_count", strconv.Itoa(st.CycleCount),
			"last_verdict", st.LastVerdict,
			"last_reason", st.LastReason,
			"started_at", strconv.FormatInt(st.StartedAt, 10),
			"closed_at", strconv.FormatInt(st.ClosedAt, 10),
			"closed_reason", st.ClosedReason,
			"stopped_by_watch", stoppedInt,
		})
	case "CLOSE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.watch.close'")
			return
		}
		reason := ""
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "REASON" {
				writeError(c.bw, "unknown STREAM.WATCH.CLOSE option: "+key)
				return
			}
			reason = args[i+1]
		}
		if c.eng.StreamWatch.Close(args[0], reason) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SESSIONS":
		writeArray(c.bw, c.eng.StreamWatch.Sessions())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.watch.reset' (use ALL or session-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.StreamWatch.Reset(args[0])))
	case "STATS":
		s := c.eng.StreamWatch.Stats()
		writeArray(c.bw, []string{
			"sessions", strconv.Itoa(s.Sessions),
			"total_tokens", strconv.FormatInt(s.TotalTokens, 10),
			"total_stops", strconv.FormatInt(s.TotalStops, 10),
			"total_warns", strconv.FormatInt(s.TotalWarns, 10),
		})
	default:
		writeError(c.bw, "unknown STREAM.WATCH subcommand: "+sub)
	}
}
