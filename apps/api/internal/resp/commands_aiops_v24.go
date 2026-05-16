package resp

import (
	"strconv"
	"strings"
)

// specDecCmd handles SPECDEC.* — draft cache + acceptance tracker.
func (c *conn) specDecCmd(sub string, args []string) {
	switch sub {
	case "CACHE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'specdec.cache' (need prefix-hash token [token...])")
			return
		}
		if err := c.eng.SpecDec.Cache(args[0], args[1:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'specdec.get'")
			return
		}
		tokens, ok := c.eng.SpecDec.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, tokens)
	case "RECORD":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'specdec.record' (need model class accepted total)")
			return
		}
		accepted, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "accepted must be integer")
			return
		}
		total, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil {
			writeError(c.bw, "total must be integer")
			return
		}
		if err := c.eng.SpecDec.Record(args[0], args[1], accepted, total); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RATE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'specdec.rate'")
			return
		}
		class := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "PREFIX_CLASS" {
				writeError(c.bw, "unknown SPECDEC.RATE option: "+key)
				return
			}
			class = args[i+1]
			i += 2
		}
		r, ok := c.eng.SpecDec.Rate(args[0], class)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"model", r.Model,
			"prefix_class", r.PrefixClass,
			"rate", strconv.FormatFloat(r.Rate, 'f', 4, 64),
			"samples", strconv.FormatInt(r.Samples, 10),
			"tokens_seen", strconv.FormatInt(r.TokensSeen, 10),
		})
	case "DECIDE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'specdec.decide' (need model class)")
			return
		}
		d := c.eng.SpecDec.Decide(args[0], args[1])
		useInt := "0"
		if d.Use {
			useInt = "1"
		}
		writeArray(c.bw, []string{
			"model", d.Model,
			"prefix_class", d.PrefixClass,
			"use", useInt,
			"rate", strconv.FormatFloat(d.Rate, 'f', 4, 64),
			"samples", strconv.FormatInt(d.Samples, 10),
			"reason", d.Reason,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'specdec.status'")
			return
		}
		rows, ok := c.eng.SpecDec.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"prefix_class", r.PrefixClass,
				"rate", strconv.FormatFloat(r.Rate, 'f', 4, 64),
				"samples", strconv.FormatInt(r.Samples, 10),
				"accepted", strconv.FormatInt(r.Accepted, 10),
				"proposed", strconv.FormatInt(r.Proposed, 10),
			})
		}
		writeValue(c.bw, out)
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'specdec.reset' (use ALL or model)")
			return
		}
		writeInt(c.bw, int64(c.eng.SpecDec.Reset(args[0])))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'specdec.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			writeError(c.bw, "cap must be non-negative integer")
			return
		}
		c.eng.SpecDec.SetCap(n)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.SpecDec.Stats()
		writeArray(c.bw, []string{
			"drafts", strconv.Itoa(s.Drafts),
			"models_tracked", strconv.Itoa(s.ModelsTracked),
			"total_cache_writes", strconv.FormatInt(s.TotalCacheWrites, 10),
			"total_cache_hits", strconv.FormatInt(s.TotalCacheHits, 10),
			"total_cache_misses", strconv.FormatInt(s.TotalCacheMisses, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_decisions", strconv.FormatInt(s.TotalDecisions, 10),
		})
	default:
		writeError(c.bw, "unknown SPECDEC subcommand: "+sub)
	}
}

// prefetchCmd handles PREFETCH.PREDICT.* — next-request predictor.
func (c *conn) prefetchCmd(sub string, args []string) {
	switch sub {
	case "OBSERVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'prefetch.predict.observe' (need session-id text)")
			return
		}
		if err := c.eng.PrefetchPredict.Observe(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PREDICT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefetch.predict.predict'")
			return
		}
		limit := 5
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown PREFETCH.PREDICT.PREDICT option: "+key)
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
		cands, ok := c.eng.PrefetchPredict.Predict(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(cands))
		for _, r := range cands {
			out = append(out, []any{
				"text", r.Text,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "HIT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'prefetch.predict.hit' (need session-id text)")
			return
		}
		if err := c.eng.PrefetchPredict.Hit(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefetch.predict.status'")
			return
		}
		st, ok := c.eng.PrefetchPredict.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"session_id", st.SessionID,
			"history_size", strconv.Itoa(st.HistorySize),
			"horizon", strconv.Itoa(st.Horizon),
			"hit_rate_ema", strconv.FormatFloat(st.HitRate, 'f', 4, 64),
			"total_predictions", strconv.FormatInt(st.TotalPredictions, 10),
			"total_hits", strconv.FormatInt(st.TotalHits, 10),
		})
	case "SESSIONS":
		writeArray(c.bw, c.eng.PrefetchPredict.Sessions())
	case "HORIZON":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'prefetch.predict.horizon' (need session-id n)")
			return
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 {
			writeError(c.bw, "horizon must be non-negative integer")
			return
		}
		if err := c.eng.PrefetchPredict.Horizon(args[0], n); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefetch.predict.reset' (use ALL or session-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.PrefetchPredict.Reset(args[0])))
	case "STATS":
		s := c.eng.PrefetchPredict.Stats()
		writeArray(c.bw, []string{
			"sessions", strconv.Itoa(s.Sessions),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
			"total_predicts", strconv.FormatInt(s.TotalPredicts, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
		})
	default:
		writeError(c.bw, "unknown PREFETCH.PREDICT subcommand: "+sub)
	}
}

// juryCmd handles JURY.* — multi-judge voting.
func (c *conn) juryCmd(sub string, args []string) {
	switch sub {
	case "SUBMIT":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'jury.submit' (need question-id candidate-id text)")
			return
		}
		if err := c.eng.Jury.Submit(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "VOTE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'jury.vote' (need question-id judge-id candidate-id [CONFIDENCE f])")
			return
		}
		conf := 1.0
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "CONFIDENCE" {
				writeError(c.bw, "unknown JURY.VOTE option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || f < 0 || f > 1 {
				writeError(c.bw, "CONFIDENCE must be float in [0,1]")
				return
			}
			conf = f
			i += 2
		}
		if err := c.eng.Jury.Vote(args[0], args[1], args[2], conf); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "VERDICT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'jury.verdict'")
			return
		}
		v, err := c.eng.Jury.Verdict(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		tieInt := "0"
		if v.TieBroken {
			tieInt = "1"
		}
		writeArray(c.bw, []string{
			"question_id", v.QuestionID,
			"winner", v.Winner,
			"winner_text", v.WinnerText,
			"winner_score", strconv.FormatFloat(v.WinnerScore, 'f', 4, 64),
			"agreement", strconv.FormatFloat(v.Agreement, 'f', 4, 64),
			"candidates_n", strconv.Itoa(v.CandidatesN),
			"judges_n", strconv.Itoa(v.JudgesN),
			"tie_broken", tieInt,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'jury.status'")
			return
		}
		st, ok := c.eng.Jury.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		rows := make([]any, 0, len(st.Rows))
		for _, r := range st.Rows {
			rows = append(rows, []any{
				"candidate_id", r.CandidateID,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"picks", strconv.Itoa(r.Picks),
			})
		}
		writeValue(c.bw, []any{
			"question_id", st.QuestionID,
			"candidates_n", strconv.Itoa(st.CandidatesN),
			"judges_n", strconv.Itoa(st.JudgesN),
			"created_at", strconv.FormatInt(st.CreatedAt, 10),
			"candidates", rows,
		})
	case "LIST":
		writeArray(c.bw, c.eng.Jury.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'jury.reset' (use ALL or question-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.Jury.Reset(args[0])))
	case "STATS":
		s := c.eng.Jury.Stats()
		writeArray(c.bw, []string{
			"questions", strconv.Itoa(s.Questions),
			"total_submits", strconv.FormatInt(s.TotalSubmits, 10),
			"total_votes", strconv.FormatInt(s.TotalVotes, 10),
			"total_verdicts", strconv.FormatInt(s.TotalVerdicts, 10),
		})
	default:
		writeError(c.bw, "unknown JURY subcommand: "+sub)
	}
}
