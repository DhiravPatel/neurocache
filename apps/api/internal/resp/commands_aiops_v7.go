package resp

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// rerankCmd handles RERANK.* — cross-encoder rerank score cache.
//
//   RERANK.GET query doc-id           -> bulk score or nil
//   RERANK.SET query doc-id score [EX sec | PX ms]
//   RERANK.SCORE query DOC doc-id [DOC doc-id...]
//                                     -> [scores[], hits[], hit_n, miss_n]
//   RERANK.FORGET query doc-id        -> int (1 if existed)
//   RERANK.PURGE                      -> int dropped
//   RERANK.SETCAP n                   -> OK
//   RERANK.SETCOST usd                -> OK
//   RERANK.STATS
func (c *conn) rerankCmd(sub string, args []string) {
	switch sub {
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'rerank.get'")
			return
		}
		s, ok := c.eng.Rerank.Get(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(s, 'f', 6, 64))
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'rerank.set'")
			return
		}
		score, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "score must be a float")
			return
		}
		var ttl time.Duration
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "EX":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "EX must be a positive integer (seconds)")
					return
				}
				ttl = time.Duration(n) * time.Second
			case "PX":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "PX must be a positive integer (ms)")
					return
				}
				ttl = time.Duration(n) * time.Millisecond
			default:
				writeError(c.bw, "unknown RERANK.SET option: "+key)
				return
			}
			i += 2
		}
		c.eng.Rerank.Set(args[0], args[1], score, ttl)
		writeSimple(c.bw, "OK")
	case "SCORE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rerank.score'")
			return
		}
		query := args[0]
		var docIDs []string
		i := 1
		for i < len(args) {
			if !strings.EqualFold(args[i], "DOC") {
				writeError(c.bw, "expected DOC marker at arg "+strconv.Itoa(i+1))
				return
			}
			if i+1 >= len(args) {
				writeError(c.bw, "DOC needs a doc-id")
				return
			}
			docIDs = append(docIDs, args[i+1])
			i += 2
		}
		r := c.eng.Rerank.ScoreBatch(query, docIDs)
		scoresAny := make([]any, 0, len(r.Scores))
		hitsAny := make([]any, 0, len(r.Hits))
		for i, s := range r.Scores {
			if math.IsNaN(s) {
				scoresAny = append(scoresAny, "")
			} else {
				scoresAny = append(scoresAny, strconv.FormatFloat(s, 'f', 6, 64))
			}
			if r.Hits[i] {
				hitsAny = append(hitsAny, "1")
			} else {
				hitsAny = append(hitsAny, "0")
			}
		}
		writeValue(c.bw, []any{
			"scores", scoresAny,
			"hits", hitsAny,
			"hit_n", strconv.Itoa(r.HitN),
			"miss_n", strconv.Itoa(r.MissN),
		})
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'rerank.forget'")
			return
		}
		if c.eng.Rerank.Forget(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		writeInt(c.bw, int64(c.eng.Rerank.Purge()))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rerank.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil {
			writeError(c.bw, "cap must be an integer")
			return
		}
		c.eng.Rerank.SetCap(n)
		writeSimple(c.bw, "OK")
	case "SETCOST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rerank.setcost'")
			return
		}
		usd, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "cost must be a float")
			return
		}
		c.eng.Rerank.SetCostUSD(usd)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.Rerank.Stats()
		writeArray(c.bw, []string{
			"entries", strconv.Itoa(s.Entries),
			"cap", strconv.Itoa(s.Cap),
			"total_gets", strconv.FormatInt(s.TotalGets, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"saved_calls", strconv.FormatInt(s.SavedCalls, 10),
			"saved_usd", strconv.FormatFloat(s.SavedUSD, 'f', 4, 64),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
			"total_evicts", strconv.FormatInt(s.TotalEvicts, 10),
			"cost_usd", strconv.FormatFloat(s.CostUSD, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown RERANK subcommand: "+sub)
	}
}

// judgeCmd handles JUDGE.* — LLM-as-judge eval suite.
//
//   JUDGE.CASE.ADD prompt-id case-id input expected
//                  [GRADER exact|contains|regex|numeric_within|llm]
//                  [TOL n]
//   JUDGE.CASE.REMOVE prompt-id case-id      -> int
//   JUDGE.CASE.LIST prompt-id                -> [case_id, input, expected, grader]
//   JUDGE.SCORE prompt-id case-id actual [LLM_PASS 0|1] [LLM_SCORE n]
//                                            -> [pass, score, grader, details]
//   JUDGE.HISTORY prompt-id [LIMIT n]
//   JUDGE.PASSRATE prompt-id [WINDOW n]
//   JUDGE.PROMPTS                            -> [prompt-ids]
//   JUDGE.FORGET prompt-id                   -> int
//   JUDGE.STATS
func (c *conn) judgeCmd(sub string, args []string) {
	switch sub {
	case "CASE.ADD":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'judge.case.add'")
			return
		}
		opts := llmstack.CaseOpts{}
		i := 4
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "GRADER":
				opts.Grader = val
			case "TOL":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "TOL must be a float")
					return
				}
				opts.Tolerance = f
			default:
				writeError(c.bw, "unknown JUDGE.CASE.ADD option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.Judge.AddCase(args[0], args[1], args[2], args[3], opts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CASE.REMOVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'judge.case.remove'")
			return
		}
		if c.eng.Judge.RemoveCase(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "CASE.LIST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'judge.case.list'")
			return
		}
		rows := c.eng.Judge.Cases(args[0])
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"case_id", r.CaseID,
				"input", r.Input,
				"expected", r.Expected,
				"grader", r.Grader,
				"tol", strconv.FormatFloat(r.Tol, 'f', 6, 64),
			})
		}
		writeValue(c.bw, out)
	case "SCORE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'judge.score'")
			return
		}
		opts := llmstack.ScoreOpts{}
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "LLM_PASS":
				opts.LLMPass = val == "1" || strings.EqualFold(val, "true")
			case "LLM_SCORE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "LLM_SCORE must be a float")
					return
				}
				opts.LLMScore = f
			default:
				writeError(c.bw, "unknown JUDGE.SCORE option: "+key)
				return
			}
			i += 2
		}
		r, ok := c.eng.Judge.Score(args[0], args[1], args[2], opts)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNCASE", "no case registered for that prompt+case")
			return
		}
		passInt := "0"
		if r.Pass {
			passInt = "1"
		}
		writeArray(c.bw, []string{
			"pass", passInt,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"grader", r.Grader,
			"details", r.Details,
		})
	case "HISTORY":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'judge.history'")
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
			val := args[i+1]
			if key != "LIMIT" {
				writeError(c.bw, "unknown JUDGE.HISTORY option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be a non-negative integer")
				return
			}
			limit = n
			i += 2
		}
		rows := c.eng.Judge.History(args[0], limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			passInt := "0"
			if r.Pass {
				passInt = "1"
			}
			out = append(out, []any{
				"case_id", r.CaseID,
				"pass", passInt,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"actual", r.Actual,
				"details", r.Details,
				"ts_unix", strconv.FormatInt(r.TSUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "PASSRATE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'judge.passrate'")
			return
		}
		window := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "WINDOW" {
				writeError(c.bw, "unknown JUDGE.PASSRATE option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				writeError(c.bw, "WINDOW must be a non-negative integer")
				return
			}
			window = n
			i += 2
		}
		r, ok := c.eng.Judge.PassRate(args[0], window)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNPROMPT", "no cases registered for that prompt")
			return
		}
		writeArray(c.bw, []string{
			"prompt_id", r.PromptID,
			"window_n", strconv.Itoa(r.WindowN),
			"pass", strconv.Itoa(r.Pass),
			"fail", strconv.Itoa(r.Fail),
			"pass_rate", strconv.FormatFloat(r.PassRate, 'f', 4, 64),
			"cases", strconv.Itoa(r.Cases),
		})
	case "PROMPTS":
		writeArray(c.bw, c.eng.Judge.PromptIDs())
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'judge.forget'")
			return
		}
		if c.eng.Judge.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Judge.Stats()
		writeArray(c.bw, []string{
			"total_runs", strconv.FormatInt(s.TotalRuns, 10),
			"total_pass", strconv.FormatInt(s.TotalPass, 10),
			"total_fail", strconv.FormatInt(s.TotalFail, 10),
			"prompts", strconv.Itoa(s.Prompts),
			"cases", strconv.Itoa(s.Cases),
		})
	default:
		writeError(c.bw, "unknown JUDGE subcommand: "+sub)
	}
}

// fewshotCmd handles FEWSHOT.* — few-shot example library w/ semantic retrieval.
//
//   FEWSHOT.ADD bank-id ex-id input output [TAGS t1,t2,...] [EMBED v1,v2,...]
//   FEWSHOT.QUERY bank-id input [K n] [TAGS t1,t2,...] [EMBED v1,v2,...]
//   FEWSHOT.GET bank-id ex-id              -> single example
//   FEWSHOT.DEL bank-id ex-id              -> int
//   FEWSHOT.LIST bank-id                   -> all examples
//   FEWSHOT.BANKS                          -> [bank_id, examples, dim]
//   FEWSHOT.FORGET bank-id                 -> int
//   FEWSHOT.STATS
func (c *conn) fewshotCmd(sub string, args []string) {
	switch sub {
	case "ADD":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'fewshot.add'")
			return
		}
		opts, err := parseFewShotOpts(args[4:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.FewShot.Add(args[0], args[1], args[2], args[3], llmstack.AddOpts{
			Tags: opts.tags, Vec: opts.vec,
		}); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "QUERY":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'fewshot.query'")
			return
		}
		opts, err := parseFewShotOpts(args[2:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		hits, ok := c.eng.FewShot.Query(args[0], args[1], llmstack.QueryOpts{
			K: opts.k, Tags: opts.tags, Vec: opts.vec,
		})
		if !ok {
			writeTypedError(c.bw, "UNKNOWNBANK", "no bank registered for that id")
			return
		}
		writeValue(c.bw, fewshotHitsReply(hits))
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'fewshot.get'")
			return
		}
		hit, ok := c.eng.FewShot.Get(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeValue(c.bw, []any{fewshotHitReply(hit)})
	case "DEL":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'fewshot.del'")
			return
		}
		if c.eng.FewShot.Del(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fewshot.list'")
			return
		}
		hits := c.eng.FewShot.List(args[0])
		writeValue(c.bw, fewshotHitsReply(hits))
	case "BANKS":
		rows := c.eng.FewShot.Banks()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"bank_id", r.BankID,
				"examples", strconv.Itoa(r.Examples),
				"dim", strconv.Itoa(r.Dim),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fewshot.forget'")
			return
		}
		if c.eng.FewShot.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.FewShot.Stats()
		writeArray(c.bw, []string{
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_queries", strconv.FormatInt(s.TotalQueries, 10),
			"total_returns", strconv.FormatInt(s.TotalReturns, 10),
			"banks", strconv.Itoa(s.Banks),
			"examples", strconv.Itoa(s.Examples),
		})
	default:
		writeError(c.bw, "unknown FEWSHOT subcommand: "+sub)
	}
}

type fewshotParsed struct {
	tags []string
	vec  []float64
	k    int
}

func parseFewShotOpts(rest []string) (fewshotParsed, error) {
	out := fewshotParsed{}
	i := 0
	for i+1 < len(rest)+1 && i < len(rest) {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return out, &fewshotErr{msg: key + " needs a value"}
		}
		val := rest[i+1]
		switch key {
		case "TAGS":
			parts := strings.Split(val, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out.tags = append(out.tags, p)
				}
			}
		case "EMBED":
			parts := strings.Split(val, ",")
			vec := make([]float64, 0, len(parts))
			for _, p := range parts {
				f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
				if err != nil {
					return out, &fewshotErr{msg: "EMBED component not parseable: " + p}
				}
				vec = append(vec, f)
			}
			out.vec = vec
		case "K":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return out, &fewshotErr{msg: "K must be a positive integer"}
			}
			out.k = n
		default:
			return out, &fewshotErr{msg: "unknown FEWSHOT option: " + key}
		}
		i += 2
	}
	return out, nil
}

type fewshotErr struct{ msg string }

func (e *fewshotErr) Error() string { return e.msg }

func fewshotHitsReply(hits []llmstack.QueryHit) []any {
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, fewshotHitReply(h))
	}
	return out
}

func fewshotHitReply(h llmstack.QueryHit) []any {
	tagsAny := make([]any, 0, len(h.Tags))
	for _, t := range h.Tags {
		tagsAny = append(tagsAny, t)
	}
	return []any{
		"id", h.ID,
		"input", h.Input,
		"output", h.Output,
		"tags", tagsAny,
		"score", strconv.FormatFloat(h.Score, 'f', 4, 64),
	}
}
