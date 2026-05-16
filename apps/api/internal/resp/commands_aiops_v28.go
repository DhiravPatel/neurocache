package resp

import (
	"strconv"
	"strings"
)

// planValidateCmd handles PLAN.VALIDATE.* — multi-step plan validator.
func (c *conn) planValidateCmd(sub string, args []string) {
	switch sub {
	case "NEW":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'plan.validate.new'")
			return
		}
		if err := c.eng.PlanValidate.New(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADDSTEP":
		if len(args) < 2 {
			writeError(c.bw, "usage: PLAN.VALIDATE.ADDSTEP plan-id step-id [DEPS d1,d2,...] [INPUTS k=v,...] [OUTPUTS o1,o2,...]")
			return
		}
		var deps []string
		inputs := map[string]string{}
		var outputs []string
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "DEPS":
				for _, d := range strings.Split(val, ",") {
					d = strings.TrimSpace(d)
					if d != "" {
						deps = append(deps, d)
					}
				}
			case "INPUTS":
				for _, kv := range strings.Split(val, ",") {
					kv = strings.TrimSpace(kv)
					if kv == "" {
						continue
					}
					eq := strings.IndexByte(kv, '=')
					if eq <= 0 || eq == len(kv)-1 {
						writeError(c.bw, "INPUTS entry must be key=value: "+kv)
						return
					}
					inputs[kv[:eq]] = kv[eq+1:]
				}
			case "OUTPUTS":
				for _, o := range strings.Split(val, ",") {
					o = strings.TrimSpace(o)
					if o != "" {
						outputs = append(outputs, o)
					}
				}
			default:
				writeError(c.bw, "unknown PLAN.VALIDATE.ADDSTEP option: "+key)
				return
			}
		}
		if err := c.eng.PlanValidate.AddStep(args[0], args[1], deps, inputs, outputs); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'plan.validate.check'")
			return
		}
		strict := false
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STRICT" {
				writeError(c.bw, "unknown PLAN.VALIDATE.CHECK option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || (n != 0 && n != 1) {
				writeError(c.bw, "STRICT must be 0 or 1")
				return
			}
			strict = n == 1
		}
		r, err := c.eng.PlanValidate.Check(args[0], strict)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		issues := make([]any, 0, len(r.Issues))
		for _, iss := range r.Issues {
			issues = append(issues, []any{
				"level", iss.Level,
				"code", iss.Code,
				"step_id", iss.StepID,
				"message", iss.Message,
			})
		}
		validInt := "0"
		if r.Valid {
			validInt = "1"
		}
		writeValue(c.bw, []any{
			"plan_id", r.PlanID,
			"valid", validInt,
			"n_steps", strconv.Itoa(r.NSteps),
			"n_cycles", strconv.Itoa(r.NCycles),
			"issues", issues,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'plan.validate.status'")
			return
		}
		rows, ok := c.eng.PlanValidate.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			ins := make([]any, 0, len(r.Inputs)*2)
			for k, v := range r.Inputs {
				ins = append(ins, k, v)
			}
			out = append(out, []any{
				"step_id", r.StepID,
				"deps", r.Deps,
				"inputs", ins,
				"outputs", r.Outputs,
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		writeArray(c.bw, c.eng.PlanValidate.List())
	case "DROP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'plan.validate.drop' (use ALL or plan-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.PlanValidate.Drop(args[0])))
	case "STATS":
		s := c.eng.PlanValidate.Stats()
		writeArray(c.bw, []string{
			"plans", strconv.Itoa(s.Plans),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
		})
	default:
		writeError(c.bw, "unknown PLAN.VALIDATE subcommand: "+sub)
	}
}

// vecAuditCmd handles VEC.AUDIT.* — vector poisoning detector.
func (c *conn) vecAuditCmd(sub string, args []string) {
	switch sub {
	case "BASELINE":
		if len(args) < 2 {
			writeError(c.bw, "usage: VEC.AUDIT.BASELINE index v1 v2 ... (each comma-separated floats)")
			return
		}
		vecs := make([][]float64, 0, len(args)-1)
		for i := 1; i < len(args); i++ {
			v, err := parseFloatVec(args[i])
			if err != nil {
				writeError(c.bw, "baseline arg "+strconv.Itoa(i)+": "+err.Error())
				return
			}
			vecs = append(vecs, v)
		}
		if err := c.eng.VecAudit.Baseline(args[0], vecs); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADDQUERY":
		if len(args) < 2 {
			writeError(c.bw, "usage: VEC.AUDIT.ADDQUERY index v")
			return
		}
		v, err := parseFloatVec(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.VecAudit.AddQuery(args[0], v); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "usage: VEC.AUDIT.CHECK index v")
			return
		}
		v, err := parseFloatVec(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		r, err := c.eng.VecAudit.Check(args[0], v)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"index_id", r.IndexID,
			"verdict", r.Verdict,
			"anomaly_score", strconv.FormatFloat(r.AnomalyScore, 'f', 4, 64),
			"centroid_distance", strconv.FormatFloat(r.CentroidDistance, 'f', 4, 64),
			"top_query_affinity", strconv.FormatFloat(r.TopQueryAffinity, 'f', 4, 64),
			"baseline_size", strconv.Itoa(r.BaselineSize),
			"reason", r.Reason,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'vec.audit.status'")
			return
		}
		st, ok := c.eng.VecAudit.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"index_id", st.IndexID,
			"baseline_size", strconv.Itoa(st.BaselineSize),
			"min_healthy_dist", strconv.FormatFloat(st.MinHealthyDist, 'f', 4, 64),
			"max_healthy_dist", strconv.FormatFloat(st.MaxHealthyDist, 'f', 4, 64),
			"query_buffer_size", strconv.Itoa(st.QueryBufferSize),
		})
	case "LIST":
		writeArray(c.bw, c.eng.VecAudit.List())
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'vec.audit.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			writeError(c.bw, "cap must be non-negative integer")
			return
		}
		c.eng.VecAudit.SetCap(n)
		writeSimple(c.bw, "OK")
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'vec.audit.reset' (use ALL or index-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.VecAudit.Reset(args[0])))
	case "STATS":
		s := c.eng.VecAudit.Stats()
		writeArray(c.bw, []string{
			"indexes", strconv.Itoa(s.Indexes),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_poisons_detected", strconv.FormatInt(s.TotalPoisons, 10),
			"total_queries", strconv.FormatInt(s.TotalQueries, 10),
			"cap", strconv.Itoa(s.Cap),
		})
	default:
		writeError(c.bw, "unknown VEC.AUDIT subcommand: "+sub)
	}
}

// extractTraceCmd handles EXTRACT.TRACE.* — extraction provenance.
func (c *conn) extractTraceCmd(sub string, args []string) {
	switch sub {
	case "NEW":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'extract.trace.new' (need extract-id source-text)")
			return
		}
		if err := c.eng.ExtractTrace.New(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SET":
		// EXTRACT.TRACE.SET extract field VALUE v SPAN s e [CONFIDENCE c]
		if len(args) < 7 {
			writeError(c.bw, "usage: EXTRACT.TRACE.SET extract field VALUE v SPAN start end [CONFIDENCE c]")
			return
		}
		value := ""
		start, end := -1, -1
		conf := 0.0
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "VALUE":
				value = val
			case "SPAN":
				// Two consecutive args: start, end
				s, err := strconv.Atoi(val)
				if err != nil {
					writeError(c.bw, "SPAN start must be integer")
					return
				}
				if i+2 >= len(args) {
					writeError(c.bw, "SPAN needs start AND end")
					return
				}
				e, err := strconv.Atoi(args[i+2])
				if err != nil {
					writeError(c.bw, "SPAN end must be integer")
					return
				}
				start, end = s, e
				i++ // consume the extra arg
			case "CONFIDENCE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "CONFIDENCE must be float")
					return
				}
				conf = f
			default:
				writeError(c.bw, "unknown EXTRACT.TRACE.SET option: "+key)
				return
			}
		}
		if start < 0 || end < 0 {
			writeError(c.bw, "SPAN start and end required")
			return
		}
		if err := c.eng.ExtractTrace.Set(args[0], args[1], value, start, end, conf); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'extract.trace.get' (need extract-id field)")
			return
		}
		row, ok := c.eng.ExtractTrace.Get(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"field", row.Field,
			"value", row.Value,
			"span_start", strconv.Itoa(row.SpanStart),
			"span_end", strconv.Itoa(row.SpanEnd),
			"source_span", row.SourceSpan,
			"confidence", strconv.FormatFloat(row.Confidence, 'f', 4, 64),
		})
	case "ALL":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'extract.trace.all'")
			return
		}
		rows, ok := c.eng.ExtractTrace.All(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"field", r.Field,
				"value", r.Value,
				"span_start", strconv.Itoa(r.SpanStart),
				"span_end", strconv.Itoa(r.SpanEnd),
				"source_span", r.SourceSpan,
				"confidence", strconv.FormatFloat(r.Confidence, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "VERIFY":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'extract.trace.verify'")
			return
		}
		r, err := c.eng.ExtractTrace.Verify(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		validInt := "0"
		if r.Valid {
			validInt = "1"
		}
		issues := make([]any, 0, len(r.Issues))
		for _, iss := range r.Issues {
			issues = append(issues, []any{
				"field", iss.Field,
				"code", iss.Code,
				"message", iss.Message,
			})
		}
		writeValue(c.bw, []any{
			"extract_id", r.ExtractID,
			"valid", validInt,
			"n_fields", strconv.Itoa(r.NFields),
			"issues", issues,
		})
	case "LIST":
		writeArray(c.bw, c.eng.ExtractTrace.List())
	case "DROP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'extract.trace.drop' (use ALL or extract-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.ExtractTrace.Drop(args[0])))
	case "STATS":
		s := c.eng.ExtractTrace.Stats()
		writeArray(c.bw, []string{
			"extracts", strconv.Itoa(s.Extracts),
			"total_new", strconv.FormatInt(s.TotalNew, 10),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_verifies", strconv.FormatInt(s.TotalVerifies, 10),
		})
	default:
		writeError(c.bw, "unknown EXTRACT.TRACE subcommand: "+sub)
	}
}

// parseFloatVec parses "v1,v2,v3" into []float64.
func parseFloatVec(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	out := make([]float64, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, errFromString("invalid float at index " + strconv.Itoa(i) + ": " + p)
		}
		out[i] = f
	}
	return out, nil
}
