package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// provCmd handles PROV.* — answer provenance DAG.
func (c *conn) provCmd(sub string, args []string) {
	switch sub {
	case "BEGIN":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROV.BEGIN answer-id [META k v ...]")
			return
		}
		meta := map[string]string{}
		if len(args) > 1 {
			if (len(args)-1)%2 != 0 {
				writeError(c.bw, "META must be key/value pairs")
				return
			}
			if strings.ToUpper(args[1]) != "META" {
				writeError(c.bw, "expected META after answer-id")
				return
			}
			for i := 2; i+1 <= len(args); i += 2 {
				if i+1 > len(args)-1 {
					break
				}
				meta[args[i]] = args[i+1]
			}
		}
		if err := c.eng.Provenance.Begin(args[0], meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "NODE":
		if len(args) < 4 {
			writeError(c.bw, "usage: PROV.NODE answer node KIND k label [FROM n ...] [REFS r ...]")
			return
		}
		if strings.ToUpper(args[2]) != "KIND" {
			writeError(c.bw, "expected KIND as third argument")
			return
		}
		kind := args[3]
		label := ""
		idx := 4
		if idx < len(args) && strings.ToUpper(args[idx]) != "FROM" && strings.ToUpper(args[idx]) != "REFS" {
			label = args[idx]
			idx++
		}
		var from, refs []string
		for idx < len(args) {
			tag := strings.ToUpper(args[idx])
			idx++
			switch tag {
			case "FROM":
				for idx < len(args) && strings.ToUpper(args[idx]) != "REFS" {
					from = append(from, args[idx])
					idx++
				}
			case "REFS":
				for idx < len(args) && strings.ToUpper(args[idx]) != "FROM" {
					refs = append(refs, args[idx])
					idx++
				}
			default:
				writeError(c.bw, "unknown PROV.NODE option: "+tag)
				return
			}
		}
		if err := c.eng.Provenance.Node(args[0], args[1], kind, label, from, refs); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "WHY":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROV.WHY answer [node] [DEPTH n]")
			return
		}
		node := ""
		depth := 0
		idx := 1
		if idx < len(args) && strings.ToUpper(args[idx]) != "DEPTH" {
			node = args[idx]
			idx++
		}
		if idx < len(args) && strings.ToUpper(args[idx]) == "DEPTH" {
			if idx+1 >= len(args) {
				writeError(c.bw, "DEPTH needs a value")
				return
			}
			n, err := strconv.Atoi(args[idx+1])
			if err != nil || n < 0 {
				writeError(c.bw, "DEPTH must be non-negative integer")
				return
			}
			depth = n
		}
		path, ok := c.eng.Provenance.Why(args[0], node, depth)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(path))
		for _, n := range path {
			out = append(out, []any{
				"id", n.ID,
				"kind", n.Kind,
				"label", n.Label,
				"from", strings.Join(n.From, ","),
				"refs", strings.Join(n.Refs, ","),
				"at_unix", strconv.FormatInt(n.AtUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "IMPACT":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROV.IMPACT ref")
			return
		}
		writeArray(c.bw, c.eng.Provenance.Impact(args[0]))
	case "ANSWER":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROV.ANSWER answer-id")
			return
		}
		v, ok := c.eng.Provenance.Answer(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		nodes := make([]any, 0, len(v.Nodes))
		for _, n := range v.Nodes {
			nodes = append(nodes, []any{
				"id", n.ID, "kind", n.Kind, "label", n.Label,
				"from", strings.Join(n.From, ","),
				"refs", strings.Join(n.Refs, ","),
				"at_unix", strconv.FormatInt(n.AtUnix, 10),
			})
		}
		writeValue(c.bw, []any{
			"answer_id", v.AnswerID,
			"created_unix", strconv.FormatInt(v.CreatedAt, 10),
			"nodes", nodes,
		})
	case "LIST":
		limit := 0
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown PROV.LIST option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.Provenance.List(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"answer_id", r.AnswerID,
				"nodes", strconv.Itoa(r.Nodes),
				"created_unix", strconv.FormatInt(r.CreatedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROV.FORGET answer-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Provenance.Forget(args[0])))
	case "STATS":
		s := c.eng.Provenance.Stats()
		writeArray(c.bw, []string{
			"answers", strconv.Itoa(s.Answers),
			"indexed_refs", strconv.Itoa(s.IndexedRefs),
			"total_begins", strconv.FormatInt(s.TotalBegins, 10),
			"total_nodes", strconv.FormatInt(s.TotalNodes, 10),
			"total_whys", strconv.FormatInt(s.TotalWhys, 10),
			"total_impacts", strconv.FormatInt(s.TotalImpacts, 10),
		})
	default:
		writeError(c.bw, "unknown PROV subcommand: "+sub)
	}
}

// trustCmd handles TRUST.* — Bayesian source/tool reputation.
func (c *conn) trustCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "usage: TRUST.RECORD entity outcome [WEIGHT w]")
			return
		}
		weight := 0.0
		for i := 2; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "WEIGHT" {
				writeError(c.bw, "unknown TRUST.RECORD option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				writeError(c.bw, "WEIGHT must be float")
				return
			}
			weight = f
		}
		if err := c.eng.Trust.Record(args[0], args[1], weight); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SCORE":
		if len(args) < 1 {
			writeError(c.bw, "usage: TRUST.SCORE entity")
			return
		}
		s := c.eng.Trust.Score(args[0])
		writeArray(c.bw, []string{
			"entity", s.Entity,
			"trust", strconv.FormatFloat(s.Trust, 'f', 4, 64),
			"n", strconv.FormatInt(s.N, 10),
			"ci_low", strconv.FormatFloat(s.CILow, 'f', 4, 64),
			"ci_high", strconv.FormatFloat(s.CIHigh, 'f', 4, 64),
			"grounded", strconv.FormatInt(s.Grounded, 10),
			"hallucinated", strconv.FormatInt(s.Hallucinated, 10),
			"cited", strconv.FormatInt(s.Cited, 10),
			"contradicted", strconv.FormatInt(s.Contradicted, 10),
			"neutral", strconv.FormatInt(s.Neutral, 10),
		})
	case "RANK":
		kind := ""
		direction := "top"
		n := 10
		minN := 0
		for i := 0; i < len(args); i++ {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "SOURCES":
				kind = "sources"
			case "TOOLS":
				kind = "tools"
			case "TOP":
				if i+1 >= len(args) {
					writeError(c.bw, "TOP needs a value")
					return
				}
				direction = "top"
				v, err := strconv.Atoi(args[i+1])
				if err != nil || v < 0 {
					writeError(c.bw, "TOP must be non-negative integer")
					return
				}
				n = v
				i++
			case "BOTTOM":
				if i+1 >= len(args) {
					writeError(c.bw, "BOTTOM needs a value")
					return
				}
				direction = "bottom"
				v, err := strconv.Atoi(args[i+1])
				if err != nil || v < 0 {
					writeError(c.bw, "BOTTOM must be non-negative integer")
					return
				}
				n = v
				i++
			case "MIN_N":
				if i+1 >= len(args) {
					writeError(c.bw, "MIN_N needs a value")
					return
				}
				v, err := strconv.Atoi(args[i+1])
				if err != nil || v < 0 {
					writeError(c.bw, "MIN_N must be non-negative integer")
					return
				}
				minN = v
				i++
			default:
				writeError(c.bw, "unknown TRUST.RANK token: "+tok)
				return
			}
		}
		rows := c.eng.Trust.Rank(kind, direction, n, minN)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"entity", r.Entity,
				"trust", strconv.FormatFloat(r.Trust, 'f', 4, 64),
				"n", strconv.FormatInt(r.N, 10),
			})
		}
		writeValue(c.bw, out)
	case "DECAY":
		if len(args) < 1 {
			writeError(c.bw, "usage: TRUST.DECAY half_life_seconds")
			return
		}
		f, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "half_life_seconds must be float")
			return
		}
		if err := c.eng.Trust.Decay(f); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: TRUST.RESET entity|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Trust.Reset(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Trust.List())
	case "STATS":
		s := c.eng.Trust.Stats()
		writeArray(c.bw, []string{
			"entities", strconv.Itoa(s.Entities),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_scores", strconv.FormatInt(s.TotalScores, 10),
			"total_ranks", strconv.FormatInt(s.TotalRanks, 10),
		})
	default:
		writeError(c.bw, "unknown TRUST subcommand: "+sub)
	}
}

// isolateCmd handles ISOLATE.* — semantic tenant isolation.
func (c *conn) isolateCmd(sub string, args []string) {
	switch sub {
	case "BIND":
		if len(args) < 3 {
			writeError(c.bw, "usage: ISOLATE.BIND vector TENANT t [CLASS c]")
			return
		}
		if strings.ToUpper(args[1]) != "TENANT" {
			writeError(c.bw, "expected TENANT as second argument")
			return
		}
		tenant := args[2]
		class := ""
		if len(args) >= 5 && strings.ToUpper(args[3]) == "CLASS" {
			class = args[4]
		}
		if err := c.eng.Isolation.Bind(args[0], tenant, class); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNBIND":
		if len(args) < 1 {
			writeError(c.bw, "usage: ISOLATE.UNBIND vector")
			return
		}
		writeInt(c.bw, int64(c.eng.Isolation.Unbind(args[0])))
	case "CHECK":
		if len(args) < 3 || strings.ToUpper(args[1]) != "AS_TENANT" {
			writeError(c.bw, "usage: ISOLATE.CHECK vector AS_TENANT t")
			return
		}
		r := c.eng.Isolation.Check(args[0], args[2])
		allowed := "0"
		if r.Allowed {
			allowed = "1"
		}
		writeArray(c.bw, []string{
			"vector_id", r.VectorID,
			"allowed", allowed,
			"tenant_of_vector", r.Tenant,
			"class", r.Class,
			"reason", r.Reason,
		})
	case "PERMITS":
		if len(args) < 3 || strings.ToUpper(args[1]) != "AS_TENANT" {
			writeError(c.bw, "usage: ISOLATE.PERMITS vector AS_TENANT t")
			return
		}
		ok := c.eng.Isolation.Permits(args[0], args[2])
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST_FOR":
		if len(args) < 2 || strings.ToUpper(args[0]) != "TENANT" {
			writeError(c.bw, "usage: ISOLATE.LIST_FOR TENANT t")
			return
		}
		writeArray(c.bw, c.eng.Isolation.ListFor(args[1]))
	case "EXPECT":
		if len(args) < 1 {
			writeError(c.bw, "usage: ISOLATE.EXPECT vector")
			return
		}
		if err := c.eng.Isolation.Expect(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "AUDIT":
		var vecs []string
		if len(args) >= 1 && strings.ToUpper(args[0]) == "VECTORS" {
			vecs = args[1:]
		}
		a := c.eng.Isolation.Audit(vecs)
		writeArray(c.bw, []string{
			"bound", strconv.Itoa(a.Bound),
			"expected", strconv.Itoa(a.Expected),
			"unbound", strings.Join(a.Unbound, ","),
		})
	case "STATS":
		s := c.eng.Isolation.Stats()
		writeArray(c.bw, []string{
			"bound", strconv.Itoa(s.Bound),
			"expected", strconv.Itoa(s.Expected),
			"unbound_expected", strconv.Itoa(s.Unbound),
			"total_binds", strconv.FormatInt(s.TotalBinds, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_denials", strconv.FormatInt(s.TotalDenials, 10),
		})
	default:
		writeError(c.bw, "unknown ISOLATE subcommand: "+sub)
	}
}

// vecspaceCmd handles VECSPACE.* — embedding-space health.
func (c *conn) vecspaceCmd(sub string, args []string) {
	switch sub {
	case "SAMPLE":
		// VECSPACE.SAMPLE space DIM d v1 v2 ... vN where N is dim*nVectors
		if len(args) < 4 || strings.ToUpper(args[1]) != "DIM" {
			writeError(c.bw, "usage: VECSPACE.SAMPLE space DIM d v1 v2 ...")
			return
		}
		dim, err := strconv.Atoi(args[2])
		if err != nil || dim <= 0 {
			writeError(c.bw, "DIM must be positive integer")
			return
		}
		nums := args[3:]
		if len(nums)%dim != 0 {
			writeError(c.bw, "vector count must be a multiple of DIM")
			return
		}
		nVec := len(nums) / dim
		vectors := make([][]float64, nVec)
		for i := 0; i < nVec; i++ {
			vec := make([]float64, dim)
			for j := 0; j < dim; j++ {
				f, err := strconv.ParseFloat(nums[i*dim+j], 64)
				if err != nil {
					writeError(c.bw, "vector value must be float")
					return
				}
				vec[j] = f
			}
			vectors[i] = vec
		}
		if err := c.eng.VecSpace.Sample(args[0], vectors); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "HEALTH":
		if len(args) < 1 {
			writeError(c.bw, "usage: VECSPACE.HEALTH space [COLLAPSE_AT f] [LOW_DIM_AT n]")
			return
		}
		collapseAt := 0.0
		lowDimAt := 0
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "COLLAPSE_AT":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "COLLAPSE_AT must be float")
					return
				}
				collapseAt = f
			case "LOW_DIM_AT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LOW_DIM_AT must be non-negative integer")
					return
				}
				lowDimAt = n
			default:
				writeError(c.bw, "unknown VECSPACE.HEALTH option: "+key)
				return
			}
		}
		r, ok := c.eng.VecSpace.Health(args[0], collapseAt, lowDimAt)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"space_id", r.SpaceID,
			"sample_n", strconv.Itoa(r.SampleN),
			"dim", strconv.Itoa(r.Dim),
			"mean_pairwise_cosine", strconv.FormatFloat(r.MeanPairwiseCosine, 'f', 4, 64),
			"effective_dim", strconv.FormatFloat(r.EffectiveDim, 'f', 4, 64),
			"nan_rate", strconv.FormatFloat(r.NaNRate, 'f', 4, 64),
			"verdict", r.Verdict,
			"reason", r.Reason,
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: VECSPACE.RESET space|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.VecSpace.Reset(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.VecSpace.List())
	case "STATS":
		s := c.eng.VecSpace.Stats()
		writeArray(c.bw, []string{
			"spaces", strconv.Itoa(s.Spaces),
			"total_samples", strconv.FormatInt(s.TotalSamples, 10),
			"total_healths", strconv.FormatInt(s.TotalHealths, 10),
		})
	default:
		writeError(c.bw, "unknown VECSPACE subcommand: "+sub)
	}
}

// prefCmd handles PREF.* — preference dataset.
func (c *conn) prefCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		// PREF.RECORD dataset prompt CHOSEN c REJECTED r [SOURCE s] [MARGIN m]
		if len(args) < 6 {
			writeError(c.bw, "usage: PREF.RECORD dataset prompt CHOSEN c REJECTED r [SOURCE s] [MARGIN m]")
			return
		}
		if strings.ToUpper(args[2]) != "CHOSEN" || strings.ToUpper(args[4]) != "REJECTED" {
			writeError(c.bw, "expected CHOSEN and REJECTED in fixed positions")
			return
		}
		chosen := args[3]
		rejected := args[5]
		source := ""
		margin := 0.0
		for i := 6; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "SOURCE":
				source = args[i+1]
			case "MARGIN":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "MARGIN must be float")
					return
				}
				margin = f
			default:
				writeError(c.bw, "unknown PREF.RECORD option: "+key)
				return
			}
		}
		r, err := c.eng.Preferences.Record(args[0], args[1], chosen, rejected, source, margin)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		recorded := "0"
		if r.Recorded {
			recorded = "1"
		}
		dup := "0"
		if r.Duplicate {
			dup = "1"
		}
		writeArray(c.bw, []string{"recorded", recorded, "duplicate", dup})
	case "STATS":
		if len(args) < 1 {
			writeError(c.bw, "usage: PREF.STATS dataset")
			return
		}
		s, ok := c.eng.Preferences.Stats(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		bySrc := make([]string, 0, len(s.BySource)*2)
		for k, v := range s.BySource {
			bySrc = append(bySrc, k, strconv.Itoa(v))
		}
		writeArray(c.bw, []string{
			"dataset", s.Dataset,
			"pairs", strconv.Itoa(s.Pairs),
			"mean_margin", strconv.FormatFloat(s.MeanMargin, 'f', 4, 64),
			"clean_pairs", strconv.Itoa(s.CleanPairs),
			"by_source", strings.Join(bySrc, ","),
		})
	case "EXPORT":
		if len(args) < 1 {
			writeError(c.bw, "usage: PREF.EXPORT dataset [FORMAT f] [MIN_MARGIN m] [SOURCE s] [LIMIT n]")
			return
		}
		format := "dpo"
		minMargin := 0.0
		source := ""
		limit := 0
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "FORMAT":
				format = args[i+1]
			case "MIN_MARGIN":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "MIN_MARGIN must be float")
					return
				}
				minMargin = f
			case "SOURCE":
				source = args[i+1]
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown PREF.EXPORT option: "+key)
				return
			}
		}
		ex, ok := c.eng.Preferences.Export(args[0], format, minMargin, source, limit)
		if !ok {
			writeError(c.bw, "unknown dataset or unsupported FORMAT")
			return
		}
		writeArray(c.bw, []string{
			"dataset", ex.Dataset,
			"format", ex.Format,
			"pairs", strconv.Itoa(ex.Pairs),
			"jsonl", ex.JSONL,
		})
	case "LIST":
		writeArray(c.bw, c.eng.Preferences.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: PREF.RESET dataset|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Preferences.Reset(args[0])))
	case "STATS_GLOBAL":
		s := c.eng.Preferences.GlobalStats()
		writeArray(c.bw, []string{
			"datasets", strconv.Itoa(s.Datasets),
			"total_pairs", strconv.Itoa(s.TotalPairs),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_exports", strconv.FormatInt(s.TotalExports, 10),
			"total_dupes", strconv.FormatInt(s.TotalDupes, 10),
		})
	default:
		writeError(c.bw, "unknown PREF subcommand: "+sub)
	}
}

// handoffCmd handles HANDOFF.* — subagent spawn/join.
func (c *conn) handoffCmd(sub string, args []string) {
	switch sub {
	case "SPAWN":
		if len(args) < 2 {
			writeError(c.bw, "usage: HANDOFF.SPAWN parent task [BUDGET n] [DEADLINE ms] [RETURN k,...] [META k v ...]")
			return
		}
		var budget int64
		var deadline time.Duration
		var required []string
		meta := map[string]string{}
		for i := 2; i < len(args); i++ {
			key := strings.ToUpper(args[i])
			switch key {
			case "BUDGET":
				if i+1 >= len(args) {
					writeError(c.bw, "BUDGET needs a value")
					return
				}
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "BUDGET must be non-negative integer")
					return
				}
				budget = n
				i++
			case "DEADLINE":
				if i+1 >= len(args) {
					writeError(c.bw, "DEADLINE needs a value")
					return
				}
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "DEADLINE must be non-negative integer (ms)")
					return
				}
				deadline = time.Duration(n) * time.Millisecond
				i++
			case "RETURN":
				if i+1 >= len(args) {
					writeError(c.bw, "RETURN needs a value")
					return
				}
				required = strings.Split(args[i+1], ",")
				i++
			case "META":
				// META consumes k v pairs until another keyword
				for j := i + 1; j+1 < len(args); j += 2 {
					nxt := strings.ToUpper(args[j])
					if nxt == "BUDGET" || nxt == "DEADLINE" || nxt == "RETURN" {
						break
					}
					meta[args[j]] = args[j+1]
					i = j + 1
				}
			default:
				writeError(c.bw, "unknown HANDOFF.SPAWN option: "+key)
				return
			}
		}
		r, err := c.eng.Handoffs.Spawn(args[0], args[1], budget, deadline, required, meta)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, r.ID)
	case "REPORT_USAGE":
		if len(args) < 2 {
			writeError(c.bw, "usage: HANDOFF.REPORT_USAGE id tokens")
			return
		}
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "tokens must be integer")
			return
		}
		r, err := c.eng.Handoffs.ReportUsage(args[0], n)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		over := "0"
		if r.OverBudget {
			over = "1"
		}
		writeArray(c.bw, []string{
			"used_tokens", strconv.FormatInt(r.UsedTokens, 10),
			"budget_tokens", strconv.FormatInt(r.BudgetTokens, 10),
			"remaining", strconv.FormatInt(r.Remaining, 10),
			"over_budget", over,
		})
	case "RETURN":
		if len(args) < 3 || (len(args)-1)%2 != 0 {
			writeError(c.bw, "usage: HANDOFF.RETURN id key value [key value ...]")
			return
		}
		fields := map[string]string{}
		for i := 1; i+1 < len(args); i += 2 {
			fields[args[i]] = args[i+1]
		}
		if err := c.eng.Handoffs.Return(args[0], fields); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "JOIN":
		if len(args) < 1 {
			writeError(c.bw, "usage: HANDOFF.JOIN id [TIMEOUT ms]")
			return
		}
		var timeout time.Duration
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "TIMEOUT" {
				writeError(c.bw, "unknown HANDOFF.JOIN option: "+key)
				return
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "TIMEOUT must be non-negative integer (ms)")
				return
			}
			timeout = time.Duration(n) * time.Millisecond
		}
		st, ok := c.eng.Handoffs.Join(args[0], timeout)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeHandoffStatusReal(c, st)
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: HANDOFF.STATUS id")
			return
		}
		st, ok := c.eng.Handoffs.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeHandoffStatusReal(c, st)
	case "CANCEL":
		if len(args) < 1 {
			writeError(c.bw, "usage: HANDOFF.CANCEL id [reason]")
			return
		}
		reason := ""
		if len(args) > 1 {
			reason = args[1]
		}
		n, err := c.eng.Handoffs.Cancel(args[0], reason)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "LIST":
		parent := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "PARENT" {
				writeError(c.bw, "unknown HANDOFF.LIST option: "+key)
				return
			}
			parent = args[i+1]
		}
		rows := c.eng.Handoffs.List(parent)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"id", r.ID, "parent", r.Parent, "task", r.Task,
				"status", r.Status, "age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: HANDOFF.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Handoffs.Forget(args[0])))
	case "STATS":
		s := c.eng.Handoffs.Stats()
		writeArray(c.bw, []string{
			"active", strconv.Itoa(s.Active),
			"total_spawns", strconv.FormatInt(s.TotalSpawns, 10),
			"total_joins", strconv.FormatInt(s.TotalJoins, 10),
			"total_returns", strconv.FormatInt(s.TotalReturns, 10),
			"total_cancels", strconv.FormatInt(s.TotalCancels, 10),
			"total_timeouts", strconv.FormatInt(s.TotalTimeouts, 10),
		})
	default:
		writeError(c.bw, "unknown HANDOFF subcommand: "+sub)
	}
}

func writeHandoffStatusReal(c *conn, st llmstack.HandoffStatus) {
	returned := make([]string, 0, len(st.Returned)*2)
	for k, v := range st.Returned {
		returned = append(returned, k, v)
	}
	writeArray(c.bw, []string{
		"id", st.ID,
		"parent", st.Parent,
		"task", st.Task,
		"status", st.Status,
		"age_ms", strconv.FormatInt(st.AgeMS, 10),
		"budget_tokens", strconv.FormatInt(st.BudgetTokens, 10),
		"used_tokens", strconv.FormatInt(st.UsedTokens, 10),
		"deadline_ms", strconv.FormatInt(st.DeadlineMS, 10),
		"returned", strings.Join(returned, ","),
		"cancel_reason", st.CancelReason,
	})
}

// riskBudgetCmd handles RISK.BUDGET.* — hallucination-risk accumulator.
func (c *conn) riskBudgetCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "usage: RISK.BUDGET.SET session budget [WEIGHT w]")
			return
		}
		budget, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "budget must be float")
			return
		}
		weight := 0.0
		for i := 2; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "WEIGHT" {
				writeError(c.bw, "unknown RISK.BUDGET.SET option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				writeError(c.bw, "WEIGHT must be float")
				return
			}
			weight = f
		}
		if err := c.eng.RiskBudgets.Set(args[0], budget, weight); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DEBIT":
		if len(args) < 2 {
			writeError(c.bw, "usage: RISK.BUDGET.DEBIT session score [REASON r]")
			return
		}
		score, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "score must be float")
			return
		}
		reason := ""
		for i := 2; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "REASON" {
				writeError(c.bw, "unknown RISK.BUDGET.DEBIT option: "+key)
				return
			}
			reason = args[i+1]
		}
		r, err := c.eng.RiskBudgets.Debit(args[0], score, reason)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		enforce := "0"
		if r.Enforce {
			enforce = "1"
		}
		writeArray(c.bw, []string{
			"balance", strconv.FormatFloat(r.Balance, 'f', 4, 64),
			"budget", strconv.FormatFloat(r.Budget, 'f', 4, 64),
			"debited", strconv.FormatFloat(r.Debited, 'f', 4, 64),
			"enforce", enforce,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: RISK.BUDGET.STATUS session")
			return
		}
		st, ok := c.eng.RiskBudgets.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		enforce := "0"
		if st.Enforce {
			enforce = "1"
		}
		writeArray(c.bw, []string{
			"session", st.Session,
			"budget", strconv.FormatFloat(st.Budget, 'f', 4, 64),
			"balance", strconv.FormatFloat(st.Balance, 'f', 4, 64),
			"debits", strconv.FormatInt(st.Debits, 10),
			"mean_score", strconv.FormatFloat(st.MeanScore, 'f', 4, 64),
			"enforce", enforce,
			"last_reason", st.LastReason,
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: RISK.BUDGET.RESET session|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.RiskBudgets.Reset(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.RiskBudgets.List())
	case "STATS":
		s := c.eng.RiskBudgets.Stats()
		writeArray(c.bw, []string{
			"sessions", strconv.Itoa(s.Sessions),
			"total_debits", strconv.FormatInt(s.TotalDebits, 10),
			"total_enforced", strconv.FormatInt(s.TotalEnforced, 10),
		})
	default:
		writeError(c.bw, "unknown RISK.BUDGET subcommand: "+sub)
	}
}
