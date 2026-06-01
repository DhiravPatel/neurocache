package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
	"github.com/dhiravpatel/neurocache/apps/api/internal/rembed"
	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// ─── GROUND.VERIFY / GROUND.REQUIRE / GROUND.VSTATS ──────────────────────
//
// Semantic groundedness. GROUND.CHECK (elsewhere) is the lexical Jaccard
// pass; these are the cosine sentence-to-chunk pass that produces a doc-level
// support score — and GROUND.REQUIRE feeds that score straight into
// RISK.BUDGET.DEBIT, closing the loop the two primitives previously left to
// the client.
//
//	GROUND.VERIFY answer CONTEXT c1 [c2 ...] [MIN_SUPPORT f]
//	GROUND.REQUIRE answer CONTEXT c1 [c2 ...] [MIN_SUPPORT f] [SESSION s]
//	GROUND.VSTATS
func (c *conn) groundVerifyCmd(sub string, args []string) {
	switch sub {
	case "VERIFY":
		answer, ctx, minSup, _, perr := parseGroundArgs(args)
		if perr != "" {
			writeError(c.bw, "ERR "+perr)
			return
		}
		if minSup <= 0 {
			minSup = c.eng.Cfg.GroundMinSupport
		}
		res := c.eng.GroundVerify.Verify(answer, ctx, minSup)
		writeValue(c.bw, []any{
			"doc_score", res.DocScore,
			"mean_score", res.MeanScore,
			"min_support", res.MinSupport,
			"grounded", res.Grounded,
			"unsupported", anyStrings(res.Unsupported),
			"sentences", groundSentencesToValue(res.Sentences),
		})

	case "REQUIRE":
		answer, ctx, minSup, session, perr := parseGroundArgs(args)
		if perr != "" {
			writeError(c.bw, "ERR "+perr)
			return
		}
		if minSup <= 0 {
			minSup = c.eng.Cfg.GroundMinSupport
		}
		res := c.eng.GroundVerify.Require(answer, ctx, minSup)
		out := []any{
			"grounded", res.Grounded,
			"doc_score", res.DocScore,
			"mean_score", res.MeanScore,
			"min_support", res.MinSupport,
			"unsupported", anyStrings(res.Unsupported),
		}
		// Close the loop: the semantic support score becomes the risk debit.
		if session != "" {
			dr, err := c.eng.RiskBudgets.Debit(session, res.DocScore, "ground.require")
			if err == nil {
				out = append(out,
					"risk_session", session,
					"risk_balance", dr.Balance,
					"risk_debited", dr.Debited,
					"risk_enforce", dr.Enforce,
				)
			}
		}
		// AOF/replication: GROUND.REQUIRE is in the writeset, so the RESP
		// execute() path records it exactly once — no explicit RecordWrite
		// here (that would double-apply the risk debit on replay).
		writeValue(c.bw, out)

	case "VSTATS":
		s := c.eng.GroundVerify.Stats()
		writeValue(c.bw, []any{
			"dim", int64(s.Dim),
			"scorer", s.Scorer,
			"total_verify", s.TotalVerify,
			"total_require", s.TotalRequire,
			"total_pass", s.TotalPass,
			"total_fail", s.TotalFail,
			"extern_scores", s.ExternScores,
		})

	case "SCORER":
		// GROUND.SCORER            → read current mode
		// GROUND.SCORER cosine|extern → switch the per-sentence scorer
		if len(args) < 1 {
			writeBulk(c.bw, c.eng.GroundVerify.CurrentScorer())
			return
		}
		if !c.eng.GroundVerify.SetScorer(strings.ToLower(args[0])) {
			writeError(c.bw, "ERR scorer must be cosine or extern")
			return
		}
		writeSimple(c.bw, "OK")

	case "INGEST":
		// GROUND.INGEST answer sentence-idx score — supply an external NLI
		// entailment score for one sentence (used when scorer=extern).
		if len(args) < 3 {
			writeError(c.bw, "ERR GROUND.INGEST answer sentence-idx score")
			return
		}
		idx, err := strconv.Atoi(args[1])
		if err != nil || idx < 0 {
			writeError(c.bw, "ERR sentence-idx must be a non-negative integer")
			return
		}
		score, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "ERR score must be a float in [0,1]")
			return
		}
		c.eng.GroundVerify.Ingest(args[0], idx, score)
		writeSimple(c.bw, "OK")

	default:
		writeError(c.bw, "ERR unknown GROUND subcommand "+sub)
	}
}

// parseGroundArgs parses `answer [CONTEXT c...] [MIN_SUPPORT f] [SESSION s]`
// in any order. answer is positional (args[0]); the rest are keyword groups.
func parseGroundArgs(args []string) (answer string, ctx []string, minSup float64, session string, perr string) {
	if len(args) < 1 {
		return "", nil, 0, "", "wrong number of arguments (answer required)"
	}
	answer = args[0]
	i := 1
	for i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "CONTEXT":
			i++
			for i < len(args) {
				u := strings.ToUpper(args[i])
				if u == "MIN_SUPPORT" || u == "SESSION" {
					break
				}
				ctx = append(ctx, args[i])
				i++
			}
		case "MIN_SUPPORT":
			if i+1 >= len(args) {
				return "", nil, 0, "", "MIN_SUPPORT needs a value"
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				return "", nil, 0, "", "MIN_SUPPORT must be a float"
			}
			minSup = f
			i += 2
		case "SESSION":
			if i+1 >= len(args) {
				return "", nil, 0, "", "SESSION needs a value"
			}
			session = args[i+1]
			i += 2
		default:
			return "", nil, 0, "", "unexpected token " + args[i]
		}
	}
	return answer, ctx, minSup, session, ""
}

func groundSentencesToValue(ss []llmstack.SentenceSupport) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, []any{
			"sentence", s.Sentence,
			"support", s.Support,
			"best_chunk", int64(s.BestChunk),
			"supported", s.Supported,
		})
	}
	return out
}

// ─── QUOTA.* — composite admission control ───────────────────────────────
//
//	QUOTA.POLICY name REQUIRE g1,g2,... [MODE all|any]
//	QUOTA.GET name
//	QUOTA.LIST
//	QUOTA.DELETE name
//	QUOTA.ADMIT name [COST scope usd] [CARBON tenant tokens model]
//	                 [RISK session score] [RATE key window-ms max] [MARKET market maxprice]
//	QUOTA.SIMULATE name ...same dims...   (dry run; consumes nothing)
//	QUOTA.STATS
func (c *conn) quotaCmd(sub string, args []string) {
	switch sub {
	case "POLICY":
		if len(args) < 3 {
			writeError(c.bw, "ERR QUOTA.POLICY name REQUIRE g1,g2,... [MODE all|any]")
			return
		}
		name := args[0]
		var gates []string
		mode := ""
		i := 1
		for i < len(args) {
			switch strings.ToUpper(args[i]) {
			case "REQUIRE":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR REQUIRE needs a comma-list of gates")
					return
				}
				gates = splitGates(args[i+1])
				i += 2
			case "MODE":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR MODE needs a value (all|any)")
					return
				}
				mode = args[i+1]
				i += 2
			default:
				writeError(c.bw, "ERR unexpected token "+args[i])
				return
			}
		}
		if mode == "" {
			mode = c.eng.Cfg.QuotaDefaultMode
		}
		pol, err := c.eng.Quota.Define(name, gates, mode)
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		// In the writeset → recorded once by execute(); no explicit call.
		writeValue(c.bw, quotaPolicyToValue(pol))

	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "ERR QUOTA.GET name")
			return
		}
		pol, ok := c.eng.Quota.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeValue(c.bw, quotaPolicyToValue(pol))

	case "LIST":
		pols := c.eng.Quota.List()
		out := make([]any, 0, len(pols))
		for _, p := range pols {
			out = append(out, quotaPolicyToValue(p))
		}
		writeValue(c.bw, out)

	case "DELETE":
		if len(args) < 1 {
			writeError(c.bw, "ERR QUOTA.DELETE name")
			return
		}
		// In the writeset → recorded once by execute(); no explicit call.
		ok := c.eng.Quota.Delete(args[0])
		writeInt(c.bw, boolToInt64(ok))

	case "ADMIT", "SIMULATE":
		if len(args) < 1 {
			writeError(c.bw, "ERR QUOTA."+sub+" name [gate dims ...]")
			return
		}
		pol, ok := c.eng.Quota.Get(args[0])
		if !ok {
			writeError(c.bw, "ERR no such policy: "+args[0])
			return
		}
		dims, perr := parseQuotaDims(args[1:])
		if perr != "" {
			writeError(c.bw, "ERR "+perr)
			return
		}
		commit := sub == "ADMIT"
		dec, err := c.eng.QuotaEvaluate(pol, dims, commit)
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		c.eng.Quota.RecordDecision(dec, !commit)
		// QUOTA.ADMIT is in the writeset; execute() records it exactly once
		// so replay reconstructs the consumed budgets without double-charging.
		// QUOTA.SIMULATE is a read — not in the writeset, never recorded.
		writeValue(c.bw, quotaDecisionToValue(dec))

	case "STATS":
		s := c.eng.Quota.Stats()
		writeValue(c.bw, []any{
			"policies", int64(s.Policies),
			"admits", s.Admits,
			"denials", s.Denials,
			"simulations", s.Simulations,
		})

	default:
		writeError(c.bw, "ERR unknown QUOTA subcommand "+sub)
	}
}

// splitGates splits a comma list, trimming blanks.
func splitGates(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseQuotaDims walks the gate-keyword groups. Each keyword consumes a fixed
// number of positional args so parsing is unambiguous:
//
//	COST scope usd | CARBON tenant tokens model | RISK session score
//	RATE key window-ms max | MARKET market maxprice
func parseQuotaDims(args []string) (aiops.QuotaDims, string) {
	var d aiops.QuotaDims
	i := 0
	for i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "COST":
			if i+2 >= len(args) {
				return d, "COST needs: scope usd"
			}
			usd, err := strconv.ParseFloat(args[i+2], 64)
			if err != nil {
				return d, "COST usd must be a float"
			}
			d.HasCost, d.CostScope, d.CostUSD = true, args[i+1], usd
			i += 3
		case "CARBON":
			if i+3 >= len(args) {
				return d, "CARBON needs: tenant tokens model"
			}
			tok, err := strconv.ParseInt(args[i+2], 10, 64)
			if err != nil {
				return d, "CARBON tokens must be an integer"
			}
			d.HasCarbon, d.CarbonTenant, d.CarbonTokens, d.CarbonModel = true, args[i+1], tok, args[i+3]
			d.CarbonFeature, d.CarbonRegion = "quota", "default"
			i += 4
		case "RISK":
			if i+2 >= len(args) {
				return d, "RISK needs: session score"
			}
			sc, err := strconv.ParseFloat(args[i+2], 64)
			if err != nil {
				return d, "RISK score must be a float"
			}
			d.HasRisk, d.RiskSession, d.RiskScore = true, args[i+1], sc
			i += 3
		case "RATE":
			if i+3 >= len(args) {
				return d, "RATE needs: key window-ms max"
			}
			win, err := strconv.ParseInt(args[i+2], 10, 64)
			if err != nil {
				return d, "RATE window-ms must be an integer"
			}
			mx, err := strconv.ParseInt(args[i+3], 10, 64)
			if err != nil {
				return d, "RATE max must be an integer"
			}
			d.HasRate, d.RateKey, d.RateWindowMs, d.RateMax, d.RateCost = true, args[i+1], win, mx, 1
			i += 4
		case "MARKET":
			if i+2 >= len(args) {
				return d, "MARKET needs: market maxprice"
			}
			mp, err := strconv.ParseFloat(args[i+2], 64)
			if err != nil {
				return d, "MARKET maxprice must be a float"
			}
			d.HasMarket, d.MarketID, d.MarketMaxPrice = true, args[i+1], mp
			i += 3
		default:
			return d, "unexpected token " + args[i]
		}
	}
	return d, ""
}

func quotaPolicyToValue(p aiops.QuotaPolicy) []any {
	gates := make([]any, len(p.Gates))
	for i, g := range p.Gates {
		gates[i] = g
	}
	return []any{"name", p.Name, "mode", p.Mode, "gates", gates}
}

func quotaDecisionToValue(d aiops.QuotaDecision) []any {
	gates := make([]any, 0, len(d.Gates))
	for _, g := range d.Gates {
		gates = append(gates, []any{
			"gate", g.Gate,
			"allowed", g.Allowed,
			"consumed", g.Consumed,
			"reason", g.Reason,
			"retry_after_ms", g.RetryAfterMs,
			"detail", g.Detail,
		})
	}
	return []any{
		"policy", d.Policy,
		"mode", d.Mode,
		"admitted", d.Admitted,
		"committed", d.Committed,
		"denied_by", anyStrings(d.DeniedBy),
		"retry_after_ms", d.RetryAfterMs,
		"gates", gates,
	}
}

// ─── REMBED.* — embedding-recompute migration ────────────────────────────
//
//	REMBED.PLAN scope [TO dim]
//	REMBED.START scope [TO dim] [BATCH n] [DUAL_READ 0|1]
//	REMBED.PROGRESS job | REMBED.STATUS job
//	REMBED.SWAP job | REMBED.ROLLBACK job
//	REMBED.LIST | REMBED.STATS
//
// REMBED state is rebuilt from traffic, not persisted (like HOTKEYS): the
// vectors derive from the entries, which ARE in the AOF — so these commands
// are deliberately NOT in the writeset.
func (c *conn) rembedCmd(sub string, args []string) {
	switch sub {
	case "PLAN":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.PLAN scope [TO dim]")
			return
		}
		toDim := 0
		if len(args) >= 3 && strings.EqualFold(args[1], "TO") {
			n, err := strconv.Atoi(args[2])
			if err != nil || n <= 0 {
				writeError(c.bw, "ERR TO dim must be a positive integer")
				return
			}
			toDim = n
		}
		plan, err := c.eng.Rembed.Plan(args[0], toDim)
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		targets := make([]any, 0, len(plan.Targets))
		for _, t := range plan.Targets {
			targets = append(targets, []any{
				"name", t.Name, "count", int64(t.Count), "bytes", t.Bytes,
				"dim", int64(t.Dim), "dual_read", t.DualRead,
			})
		}
		writeValue(c.bw, []any{
			"scope", plan.Scope,
			"to_dim", int64(plan.ToDim),
			"total_count", int64(plan.TotalCount),
			"total_bytes", plan.TotalBytes,
			"targets", targets,
		})

	case "START":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.START scope [TO dim] [BATCH n] [DUAL_READ 0|1]")
			return
		}
		scope := args[0]
		toDim := c.eng.Cfg.EmbeddingDim
		batch := 0
		dualRead := false
		mode := "embed"
		i := 1
		for i < len(args) {
			switch strings.ToUpper(args[i]) {
			case "MODE":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR MODE needs a value (embed|extern)")
					return
				}
				mode = strings.ToLower(args[i+1])
				i += 2
			case "TO":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR TO needs a value")
					return
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n <= 0 {
					writeError(c.bw, "ERR TO dim must be a positive integer")
					return
				}
				toDim = n
				i += 2
			case "BATCH":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR BATCH needs a value")
					return
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n <= 0 {
					writeError(c.bw, "ERR BATCH must be a positive integer")
					return
				}
				batch = n
				i += 2
			case "DUAL_READ":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR DUAL_READ needs 0|1")
					return
				}
				dualRead = args[i+1] == "1" || strings.EqualFold(args[i+1], "true")
				i += 2
			default:
				writeError(c.bw, "ERR unexpected token "+args[i])
				return
			}
		}
		if batch <= 0 {
			batch = c.eng.Cfg.RembedBatch
		}
		var id string
		var err error
		switch mode {
		case "extern":
			// Bring-your-own re-embedder: stage an empty shadow, hand the
			// client the source set via REMBED.EXTERN, accept vectors back via
			// REMBED.INGEST, then REMBED.FINALIZE → REMBED.SWAP.
			id, err = c.eng.Rembed.StartExtern(scope, toDim)
		case "embed", "":
			id, err = c.eng.Rembed.Start(scope, toDim, batch, dualRead)
		default:
			writeError(c.bw, "ERR MODE must be embed or extern")
			return
		}
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		writeBulk(c.bw, id)

	case "EXTERN":
		// REMBED.EXTERN job [CURSOR c] [COUNT n] [WITHVEC]
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.EXTERN job [CURSOR c] [COUNT n] [WITHVEC]")
			return
		}
		cursor, count, withVec := 0, 100, false
		for i := 1; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "CURSOR":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR CURSOR needs a value")
					return
				}
				cursor, _ = strconv.Atoi(args[i+1])
				i++
			case "COUNT":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR COUNT needs a value")
					return
				}
				count, _ = strconv.Atoi(args[i+1])
				i++
			case "WITHVEC":
				withVec = true
			default:
				writeError(c.bw, "ERR unexpected token "+args[i])
				return
			}
		}
		entries, next, err := c.eng.Rembed.Export(args[0], cursor, count)
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		rows := make([]any, 0, len(entries))
		for _, e := range entries {
			row := []any{"id", e.ID, "attr", e.Attr}
			if withVec {
				row = append(row, "vec", formatFloat32CSV(e.Vec))
			}
			rows = append(rows, row)
		}
		writeValue(c.bw, []any{"cursor", int64(next), "entries", rows})

	case "INGEST":
		// REMBED.INGEST job id vec [id vec ...] — vec is FP32 binary or CSV.
		if len(args) < 3 || (len(args)-1)%2 != 0 {
			writeError(c.bw, "ERR REMBED.INGEST job id vec [id vec ...]")
			return
		}
		st, ok := c.eng.Rembed.Status(args[0])
		if !ok {
			writeError(c.bw, "ERR unknown rembed job")
			return
		}
		pairs := make([]rembed.ExternEntry, 0, (len(args)-1)/2)
		for i := 1; i+1 < len(args); i += 2 {
			vec, verr := vectorindex.ParseVector(args[i+1], st.ToDim)
			if verr != nil {
				writeError(c.bw, "ERR bad vector for id "+args[i]+": "+verr.Error())
				return
			}
			pairs = append(pairs, rembed.ExternEntry{ID: args[i], Vec: vec})
		}
		done, total, err := c.eng.Rembed.Ingest(args[0], pairs)
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		writeValue(c.bw, []any{"ingested", int64(done), "total", int64(total)})

	case "FINALIZE":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.FINALIZE job")
			return
		}
		done, total, err := c.eng.Rembed.Finalize(args[0])
		if err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		writeValue(c.bw, []any{"state", "staged", "ingested", int64(done), "total", int64(total)})

	case "PROGRESS":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.PROGRESS job")
			return
		}
		p, ok := c.eng.Rembed.Progress(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		targets := make([]any, 0, len(p.Targets))
		for _, t := range p.Targets {
			targets = append(targets, rembedTargetToValue(t.Name, t.FromDim, t.Total, t.Done, t.State, t.Err))
		}
		writeValue(c.bw, []any{
			"id", p.ID,
			"state", string(p.State),
			"scope", p.Scope,
			"to_dim", int64(p.ToDim),
			"dual_read", p.DualRead,
			"total", int64(p.Total),
			"done", int64(p.Done),
			"rps", p.Rps,
			"eta_seconds", p.EtaSeconds,
			"err", p.Err,
			"targets", targets,
		})

	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.STATUS job")
			return
		}
		st, ok := c.eng.Rembed.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeValue(c.bw, rembedStatusToValue(st))

	case "SWAP":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.SWAP job")
			return
		}
		if err := c.eng.Rembed.Swap(args[0]); err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		writeSimple(c.bw, "OK")

	case "ROLLBACK":
		if len(args) < 1 {
			writeError(c.bw, "ERR REMBED.ROLLBACK job")
			return
		}
		if err := c.eng.Rembed.Rollback(args[0]); err != nil {
			writeError(c.bw, "ERR "+err.Error())
			return
		}
		writeSimple(c.bw, "OK")

	case "LIST":
		jobs := c.eng.Rembed.List()
		out := make([]any, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, rembedStatusToValue(j))
		}
		writeValue(c.bw, out)

	case "STATS":
		s := c.eng.Rembed.Stats()
		writeValue(c.bw, []any{
			"targets", int64(s.Targets),
			"jobs", int64(s.Jobs),
			"total_jobs", s.TotalJobs,
			"active", int64(s.Active),
		})

	default:
		writeError(c.bw, "ERR unknown REMBED subcommand "+sub)
	}
}

func rembedTargetToValue(name string, fromDim, total, done int, state, errStr string) []any {
	return []any{
		"name", name, "from_dim", int64(fromDim), "total", int64(total),
		"done", int64(done), "state", state, "err", errStr,
	}
}

func rembedStatusToValue(st rembed.JobStatus) []any {
	targets := make([]any, 0, len(st.Targets))
	for _, t := range st.Targets {
		targets = append(targets, rembedTargetToValue(t.Name, t.FromDim, t.Total, t.Done, t.State, t.Err))
	}
	return []any{
		"id", st.ID,
		"state", string(st.State),
		"scope", st.Scope,
		"to_dim", int64(st.ToDim),
		"batch", int64(st.Batch),
		"dual_read", st.DualRead,
		"err", st.Err,
		"targets", targets,
	}
}
