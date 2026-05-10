package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// redactCmd handles REDACT.* — PII redaction with restore tokens.
//
//   REDACT.SCRUB <text>
//                          -> [text, restore_token, replacements_kv...]
//   REDACT.RESTORE <token> <text>
//                          -> [restored_text, ok-int]
//   REDACT.FORGET <token>  -> int (1 if existed)
//   REDACT.PATTERN.ADD <name> <regex> <placeholder>
//   REDACT.PATTERN.REMOVE <name>
//   REDACT.PATTERN.LIST
//   REDACT.STATS
func (c *conn) redactCmd(sub string, args []string) {
	switch sub {
	case "SCRUB":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'redact.scrub'")
			return
		}
		r := c.eng.Redactor.Scrub(args[0])
		out := []any{
			"text", r.Text,
			"restore_token", r.RestoreToken,
		}
		repl := make([]any, 0, len(r.Replacements)*2)
		for name, count := range r.Replacements {
			repl = append(repl, name, strconv.Itoa(count))
		}
		out = append(out, "replacements", repl)
		writeValue(c.bw, out)
	case "RESTORE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'redact.restore'")
			return
		}
		text, ok := c.eng.Redactor.Restore(args[0], args[1])
		okInt := int64(0)
		if ok {
			okInt = 1
		}
		writeValue(c.bw, []any{
			"text", text,
			"ok", strconv.FormatInt(okInt, 10),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'redact.forget'")
			return
		}
		if c.eng.Redactor.ForgetToken(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PATTERN.ADD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'redact.pattern.add'")
			return
		}
		if err := c.eng.Redactor.Add(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, "bad regex: "+err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PATTERN.REMOVE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'redact.pattern.remove'")
			return
		}
		if c.eng.Redactor.Remove(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PATTERN.LIST":
		rows := c.eng.Redactor.Patterns()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			builtinStr := "0"
			if r.Builtin {
				builtinStr = "1"
			}
			out = append(out, []any{
				"name", r.Name,
				"source", r.Source,
				"placeholder", r.Placeholder,
				"builtin", builtinStr,
				"hits", strconv.FormatInt(r.Hits, 10),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Redactor.Stats()
		writeArray(c.bw, []string{
			"total_scrubs", strconv.FormatInt(s.TotalScrubs, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_restores", strconv.FormatInt(s.TotalRestores, 10),
		})
	default:
		writeError(c.bw, "unknown REDACT subcommand: "+sub)
	}
}

// groundCmd handles GROUND.* — citation grounding scorer.
//
//   GROUND.CHECK <output> SOURCE <text> [SOURCE <text>...]
//                                -> [doc_score, verdict, claims[]]
//   GROUND.THRESHOLDS            -> [ok, bad]
//   GROUND.SET_THRESHOLDS <ok> <bad>
//   GROUND.STATS
func (c *conn) groundCmd(sub string, args []string) {
	switch sub {
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ground.check'")
			return
		}
		output := args[0]
		var sources []string
		i := 1
		for i < len(args) {
			if !strings.EqualFold(args[i], "SOURCE") {
				writeError(c.bw, "expected SOURCE marker at arg "+strconv.Itoa(i+1))
				return
			}
			if i+1 >= len(args) {
				writeError(c.bw, "SOURCE needs a text argument")
				return
			}
			sources = append(sources, args[i+1])
			i += 2
		}
		r := c.eng.Ground.Check(output, sources)
		claimsAny := make([]any, 0, len(r.Claims))
		for _, cl := range r.Claims {
			claimsAny = append(claimsAny, []any{
				"claim", cl.Claim,
				"best_source", strconv.Itoa(cl.BestSource),
				"best_score", strconv.FormatFloat(cl.BestScore, 'f', 4, 64),
				"verdict", cl.Verdict,
			})
		}
		writeValue(c.bw, []any{
			"doc_score", strconv.FormatFloat(r.DocScore, 'f', 4, 64),
			"verdict", r.Verdict,
			"claims", claimsAny,
		})
	case "THRESHOLDS":
		t := c.eng.Ground.CurrentThresholds()
		writeArray(c.bw, []string{
			"ok", strconv.FormatFloat(t.OK, 'f', 4, 64),
			"bad", strconv.FormatFloat(t.Bad, 'f', 4, 64),
		})
	case "SET_THRESHOLDS":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'ground.set_thresholds'")
			return
		}
		ok, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "ok threshold must be a float")
			return
		}
		bad, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "bad threshold must be a float")
			return
		}
		if bad >= ok {
			writeError(c.bw, "bad must be < ok")
			return
		}
		c.eng.Ground.SetThresholds(ok, bad)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.Ground.Stats()
		writeArray(c.bw, []string{
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_accept", strconv.FormatInt(s.TotalAccept, 10),
			"total_gray", strconv.FormatInt(s.TotalGray, 10),
			"total_reject", strconv.FormatInt(s.TotalReject, 10),
			"threshold_ok", strconv.FormatFloat(s.Thresholds.OK, 'f', 4, 64),
			"threshold_bad", strconv.FormatFloat(s.Thresholds.Bad, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown GROUND subcommand: "+sub)
	}
}

// canaryCmd handles CANARY.* — prompt canary deployments.
//
//   CANARY.CREATE <id> <baseline> <candidate>
//                  [PCT n] [DELTA d] [MIN_N n]
//   CANARY.PICK <id> [seed]                  -> [arm, prompt]
//   CANARY.RECORD <id> <baseline|candidate> <score> -> status
//   CANARY.STATUS <id>                       -> status
//   CANARY.SET_TRAFFIC <id> <pct>
//   CANARY.PROMOTE <id>
//   CANARY.ROLLBACK <id>
//   CANARY.LIST
//   CANARY.FORGET <id>
//   CANARY.STATS
func (c *conn) canaryCmd(sub string, args []string) {
	switch sub {
	case "CREATE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'canary.create'")
			return
		}
		opts := llmstack.CanaryOpts{}
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "PCT":
				n, err := strconv.Atoi(val)
				if err != nil {
					writeError(c.bw, "PCT must be an integer")
					return
				}
				opts.TrafficPct = n
			case "DELTA":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "DELTA must be a float")
					return
				}
				opts.DeltaThreshold = f
			case "MIN_N":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil {
					writeError(c.bw, "MIN_N must be an integer")
					return
				}
				opts.MinSamples = n
			default:
				writeError(c.bw, "unknown CANARY.CREATE option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.Canaries.Create(args[0], args[1], args[2], opts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PICK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'canary.pick'")
			return
		}
		seed := ""
		if len(args) >= 2 {
			seed = args[1]
		}
		r, ok := c.eng.Canaries.Pick(args[0], seed)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNCANARY", "no canary registered for that id")
			return
		}
		writeValue(c.bw, []any{
			"arm", r.Arm,
			"prompt", r.Prompt,
		})
	case "RECORD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'canary.record'")
			return
		}
		score, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "score must be a float")
			return
		}
		st, ok := c.eng.Canaries.Record(args[0], args[1], score)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNCANARY", "no canary registered or unknown arm")
			return
		}
		writeValue(c.bw, canaryStatusReply(st))
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'canary.status'")
			return
		}
		st, ok := c.eng.Canaries.Status(args[0])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNCANARY", "no canary registered for that id")
			return
		}
		writeValue(c.bw, canaryStatusReply(st))
	case "SET_TRAFFIC":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'canary.set_traffic'")
			return
		}
		pct, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "pct must be an integer")
			return
		}
		if c.eng.Canaries.SetTraffic(args[0], pct) {
			writeSimple(c.bw, "OK")
		} else {
			writeTypedError(c.bw, "UNKNOWNCANARY", "no canary registered or pct out of range")
		}
	case "PROMOTE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'canary.promote'")
			return
		}
		if c.eng.Canaries.Promote(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "ROLLBACK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'canary.rollback'")
			return
		}
		if c.eng.Canaries.Rollback(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		rows := c.eng.Canaries.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, canaryStatusReply(r))
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'canary.forget'")
			return
		}
		if c.eng.Canaries.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Canaries.Stats()
		writeArray(c.bw, []string{
			"total_creates", strconv.FormatInt(s.TotalCreates, 10),
			"total_picks", strconv.FormatInt(s.TotalPicks, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_rollbacks", strconv.FormatInt(s.TotalRollbacks, 10),
			"total_promotes", strconv.FormatInt(s.TotalPromotes, 10),
			"active_canaries", strconv.Itoa(s.ActiveCanaries),
		})
	default:
		writeError(c.bw, "unknown CANARY subcommand: "+sub)
	}
}

func canaryStatusReply(st llmstack.CanaryStatus) []any {
	return []any{
		"id", st.ID,
		"baseline", st.Baseline,
		"candidate", st.Candidate,
		"traffic_percent", strconv.FormatInt(st.TrafficPercent, 10),
		"baseline_n", strconv.FormatInt(st.BaselineN, 10),
		"candidate_n", strconv.FormatInt(st.CandidateN, 10),
		"baseline_mean", strconv.FormatFloat(st.BaselineMean, 'f', 4, 64),
		"candidate_mean", strconv.FormatFloat(st.CandidateMean, 'f', 4, 64),
		"delta", strconv.FormatFloat(st.Delta, 'f', 4, 64),
		"verdict", st.Verdict,
		"delta_threshold", strconv.FormatFloat(st.DeltaThreshold, 'f', 4, 64),
		"min_samples", strconv.FormatInt(st.MinSamples, 10),
		"created_at", strconv.FormatInt(st.CreatedAt, 10),
		"updated_at", strconv.FormatInt(st.UpdatedAt, 10),
		"rolled_back_at", strconv.FormatInt(st.RolledBackAt, 10),
	}
}
