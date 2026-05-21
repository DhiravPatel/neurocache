package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// autocompleteCmd handles AUTOCOMPLETE.* — prefix completion.
//
//   AUTOCOMPLETE.ADD list-id phrase [SCORE n]
//   AUTOCOMPLETE.SUGGEST list-id prefix [K n]
//   AUTOCOMPLETE.DEL list-id phrase
//   AUTOCOMPLETE.SIZE list-id
//   AUTOCOMPLETE.LIST list-id [PREFIX p]
//   AUTOCOMPLETE.FORGET list-id
//   AUTOCOMPLETE.STATS
func (c *conn) autocompleteCmd(sub string, args []string) {
	switch sub {
	case "ADD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'autocomplete.add'")
			return
		}
		score := 0.0
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "SCORE" {
				writeError(c.bw, "unknown AUTOCOMPLETE.ADD option: "+key)
				return
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				writeError(c.bw, "SCORE must be a float")
				return
			}
			score = f
			i += 2
		}
		if err := c.eng.Autocomplete.Add(args[0], args[1], score); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SUGGEST":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'autocomplete.suggest'")
			return
		}
		k := 10
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "K" {
				writeError(c.bw, "unknown AUTOCOMPLETE.SUGGEST option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				writeError(c.bw, "K must be a positive integer")
				return
			}
			k = n
			i += 2
		}
		hits := c.eng.Autocomplete.Suggest(args[0], args[1], k)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"phrase", h.Phrase,
				"score", strconv.FormatFloat(h.Score, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "DEL":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'autocomplete.del'")
			return
		}
		if c.eng.Autocomplete.Del(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SIZE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'autocomplete.size'")
			return
		}
		n, _ := c.eng.Autocomplete.Size(args[0])
		writeInt(c.bw, int64(n))
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'autocomplete.list'")
			return
		}
		prefix := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "PREFIX" {
				writeError(c.bw, "unknown AUTOCOMPLETE.LIST option: "+key)
				return
			}
			prefix = val
			i += 2
		}
		hits := c.eng.Autocomplete.List(args[0], prefix)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"phrase", h.Phrase,
				"score", strconv.FormatFloat(h.Score, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'autocomplete.forget'")
			return
		}
		writeInt(c.bw, int64(c.eng.Autocomplete.Forget(args[0])))
	case "STATS":
		s := c.eng.Autocomplete.Stats()
		writeArray(c.bw, []string{
			"lists", strconv.Itoa(s.Lists),
			"total_phrases", strconv.Itoa(s.TotalPhrases),
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_suggests", strconv.FormatInt(s.TotalSuggests, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
		})
	default:
		writeError(c.bw, "unknown AUTOCOMPLETE subcommand: "+sub)
	}
}

// chainStateCmd handles CHAINSTATE.* — workflow state machine.
//
//   CHAINSTATE.DEFINE chain-id step1 step2 step3 ...
//   CHAINSTATE.START run-id chain-id
//   CHAINSTATE.DONE run-id step-name artifact
//   CHAINSTATE.FAIL run-id step-name reason
//   CHAINSTATE.RESUME run-id
//   CHAINSTATE.ARTIFACT run-id step-name
//   CHAINSTATE.STATUS run-id
//   CHAINSTATE.RUNS chain-id [STATUS running|complete|failed]
//   CHAINSTATE.FORGET run-id
//   CHAINSTATE.FORGET_CHAIN chain-id
//   CHAINSTATE.STATS
func (c *conn) chainStateCmd(sub string, args []string) {
	switch sub {
	case "DEFINE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.define'")
			return
		}
		if err := c.eng.ChainState.Define(args[0], args[1:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "START":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.start'")
			return
		}
		if err := c.eng.ChainState.Start(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DONE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.done'")
			return
		}
		r, err := c.eng.ChainState.Done(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"next_step", r.NextStep,
			"step_idx", strconv.Itoa(r.StepIdx),
			"total_steps", strconv.Itoa(r.TotalSteps),
			"status", r.Status,
		})
	case "FAIL":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.fail'")
			return
		}
		if err := c.eng.ChainState.Fail(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESUME":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.resume'")
			return
		}
		r, ok := c.eng.ChainState.Resume(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		artsAny := make([]any, 0, len(r.Artifacts)*2)
		for k, v := range r.Artifacts {
			artsAny = append(artsAny, k, v)
		}
		writeValue(c.bw, []any{
			"run_id", r.RunID,
			"chain_id", r.ChainID,
			"next_step", r.NextStep,
			"step_idx", strconv.Itoa(r.StepIdx),
			"total_steps", strconv.Itoa(r.TotalSteps),
			"status", r.Status,
			"reason", r.Reason,
			"artifacts", artsAny,
		})
	case "ARTIFACT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.artifact'")
			return
		}
		v, ok := c.eng.ChainState.Artifact(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.status'")
			return
		}
		r, ok := c.eng.ChainState.Resume(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"run_id", r.RunID,
			"chain_id", r.ChainID,
			"step_idx", strconv.Itoa(r.StepIdx),
			"total_steps", strconv.Itoa(r.TotalSteps),
			"status", r.Status,
			"reason", r.Reason,
		})
	case "RUNS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.runs'")
			return
		}
		status := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "STATUS" {
				writeError(c.bw, "unknown CHAINSTATE.RUNS option: "+key)
				return
			}
			status = val
			i += 2
		}
		rows := c.eng.ChainState.Runs(args[0], status)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"run_id", r.RunID,
				"chain_id", r.ChainID,
				"status", r.Status,
				"step_idx", strconv.Itoa(r.StepIdx),
				"started_at", strconv.FormatInt(r.StartedAt, 10),
				"updated_at", strconv.FormatInt(r.UpdatedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.forget'")
			return
		}
		if c.eng.ChainState.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "FORGET_CHAIN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'chainstate.forget_chain'")
			return
		}
		_, dropped := c.eng.ChainState.ForgetChain(args[0])
		writeInt(c.bw, int64(dropped))
	case "STATS":
		s := c.eng.ChainState.Stats()
		writeArray(c.bw, []string{
			"chains", strconv.Itoa(s.Chains),
			"active_runs", strconv.Itoa(s.ActiveRuns),
			"total_runs", strconv.FormatInt(s.TotalRuns, 10),
			"total_completes", strconv.FormatInt(s.TotalCompletes, 10),
			"total_fails", strconv.FormatInt(s.TotalFails, 10),
			"total_steps", strconv.FormatInt(s.TotalSteps, 10),
		})
	default:
		writeError(c.bw, "unknown CHAINSTATE subcommand: "+sub)
	}
}

// moeCmd handles MOE.* — mixture-of-experts router.
//
//   MOE.EXPERT.REGISTER expert-id name description
//        [TAGS t1,t2,...] [EMBED v,v,...]
//   MOE.ROUTE query [K n] [TAGS t1,t2,...] [EMBED v,v,...]
//   MOE.RECORD expert-id 0|1 [LATENCY_MS n]
//   MOE.EXPERTS [TAGS t1,t2,...]
//   MOE.FORGET expert-id
//   MOE.STATS
func (c *conn) moeCmd(sub string, args []string) {
	switch sub {
	case "EXPERT.REGISTER":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'moe.expert.register'")
			return
		}
		opts, err := parseMoEOpts(args[3:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.MoE.RegisterExpert(args[0], args[1], args[2],
			llmstack.ExpertOpts{Tags: opts.tags, Vec: opts.vec}); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ROUTE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'moe.route'")
			return
		}
		opts, err := parseMoEOpts(args[1:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		hits := c.eng.MoE.Route(args[0], llmstack.RouteOpts{
			K: opts.k, Tags: opts.tags, Vec: opts.vec,
		})
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"expert_id", h.ExpertID,
				"name", h.Name,
				"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
				"capability", strconv.FormatFloat(h.Capability, 'f', 6, 64),
				"success_rate", strconv.FormatFloat(h.SuccessRate, 'f', 4, 64),
				"calls", strconv.FormatInt(h.Calls, 10),
			})
		}
		writeValue(c.bw, out)
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'moe.record'")
			return
		}
		success := args[1] == "1" || strings.EqualFold(args[1], "true")
		latencyMS := int64(0)
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "LATENCY_MS" {
				writeError(c.bw, "unknown MOE.RECORD option: "+key)
				return
			}
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "LATENCY_MS must be a non-negative integer")
				return
			}
			latencyMS = n
			i += 2
		}
		if c.eng.MoE.Record(args[0], success, latencyMS) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "EXPERTS":
		var tags []string
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TAGS" {
				writeError(c.bw, "unknown MOE.EXPERTS option: "+key)
				return
			}
			for _, p := range strings.Split(val, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					tags = append(tags, p)
				}
			}
			i += 2
		}
		rows := c.eng.MoE.Experts(tags)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			tagsAny := make([]any, 0, len(r.Tags))
			for _, t := range r.Tags {
				tagsAny = append(tagsAny, t)
			}
			out = append(out, []any{
				"expert_id", r.ExpertID,
				"name", r.Name,
				"description", r.Description,
				"tags", tagsAny,
				"calls", strconv.FormatInt(r.Calls, 10),
				"successes", strconv.FormatInt(r.Successes, 10),
				"success_rate", strconv.FormatFloat(r.SuccessRate, 'f', 4, 64),
				"avg_latency_ms", strconv.FormatInt(r.AvgLatencyMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'moe.forget'")
			return
		}
		if c.eng.MoE.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.MoE.Stats()
		writeArray(c.bw, []string{
			"experts", strconv.Itoa(s.Experts),
			"total_routes", strconv.FormatInt(s.TotalRoutes, 10),
			"total_returns", strconv.FormatInt(s.TotalReturns, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
		})
	default:
		writeError(c.bw, "unknown MOE subcommand: "+sub)
	}
}

type moeParsed struct {
	tags []string
	vec  []float64
	k    int
}

func parseMoEOpts(rest []string) (moeParsed, error) {
	out := moeParsed{}
	i := 0
	for i+1 < len(rest)+1 && i < len(rest) {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return out, &v13Err{msg: key + " needs a value"}
		}
		val := rest[i+1]
		switch key {
		case "TAGS":
			for _, p := range strings.Split(val, ",") {
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
					return out, &v13Err{msg: "EMBED component not parseable: " + p}
				}
				vec = append(vec, f)
			}
			out.vec = vec
		case "K":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return out, &v13Err{msg: "K must be a positive integer"}
			}
			out.k = n
		default:
			return out, &v13Err{msg: "unknown MOE option: " + key}
		}
		i += 2
	}
	return out, nil
}

type v13Err struct{ msg string }

func (e *v13Err) Error() string { return e.msg }
