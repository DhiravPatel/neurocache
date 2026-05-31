package resp

import (
	"strconv"
	"strings"
	"time"
)

// schemaCmd handles SCHEMA.* — tool/API schema-change classifier
// (separate command family from CONTRACT.* which is the argument
// validator). SCHEMA tracks versioned schemas + DIFFs them with a
// breaking/risky/safe verdict + a migration hint.
func (c *conn) schemaCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 3 {
			writeError(c.bw, "usage: SCHEMA.REGISTER tool version schema-json")
			return
		}
		if err := c.eng.ContractEvolve.Register(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DIFF":
		if len(args) < 3 {
			writeError(c.bw, "usage: SCHEMA.DIFF tool from-version to-version")
			return
		}
		d, err := c.eng.ContractEvolve.Diff(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		changes := make([]any, 0, len(d.Changes))
		for _, ch := range d.Changes {
			changes = append(changes, []any{
				"kind", ch.Kind,
				"op", ch.Op,
				"field", ch.Field,
				"before", ch.Before,
				"after", ch.After,
				"severity", ch.Severity,
				"note", ch.Note,
			})
		}
		writeValue(c.bw, []any{
			"tool", d.Tool,
			"from", d.From,
			"to", d.To,
			"verdict", d.Verdict,
			"hint", d.Hint,
			"changes", changes,
		})
	case "VERSIONS":
		if len(args) < 1 {
			writeError(c.bw, "usage: SCHEMA.VERSIONS tool")
			return
		}
		writeArray(c.bw, c.eng.ContractEvolve.Versions(args[0]))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: SCHEMA.FORGET tool|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.ContractEvolve.Forget(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.ContractEvolve.List())
	case "STATS":
		s := c.eng.ContractEvolve.Stats()
		writeArray(c.bw, []string{
			"tools", strconv.Itoa(s.Tools),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
			"total_diffs", strconv.FormatInt(s.TotalDiffs, 10),
		})
	default:
		writeError(c.bw, "unknown SCHEMA subcommand: "+sub)
	}
}

// whatIfCmd handles WHATIF.* — dry-run cost/quality/latency simulator.
func (c *conn) whatIfCmd(sub string, args []string) {
	switch sub {
	case "OBSERVE":
		if len(args) < 4 {
			writeError(c.bw, "usage: WHATIF.OBSERVE route quality cost-usd latency-ms")
			return
		}
		q, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "quality must be float")
			return
		}
		cost, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "cost-usd must be float")
			return
		}
		latency, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "latency-ms must be float")
			return
		}
		if err := c.eng.WhatIf.Observe(args[0], q, cost, latency); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SIMULATE":
		if len(args) < 1 {
			writeError(c.bw, "usage: WHATIF.SIMULATE route [REPEATS n]")
			return
		}
		repeats := 1
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "REPEATS" {
				writeError(c.bw, "unknown WHATIF.SIMULATE option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "REPEATS must be non-negative integer")
				return
			}
			repeats = n
		}
		p, ok := c.eng.WhatIf.Simulate(args[0], repeats)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"route", p.Route,
			"sample_n", strconv.FormatInt(p.SampleN, 10),
			"projected_quality", strconv.FormatFloat(p.ProjectedQuality, 'f', 4, 64),
			"quality_ci_low", strconv.FormatFloat(p.QualityCILow, 'f', 4, 64),
			"quality_ci_high", strconv.FormatFloat(p.QualityCIHigh, 'f', 4, 64),
			"projected_cost_usd", strconv.FormatFloat(p.ProjectedCostUSD, 'f', 6, 64),
			"projected_p99_ms", strconv.FormatFloat(p.ProjectedP99MS, 'f', 2, 64),
			"projected_mean_ms", strconv.FormatFloat(p.ProjectedMeanMS, 'f', 2, 64),
			"confidence", p.Confidence,
		})
	case "COMPARE":
		if len(args) < 2 {
			writeError(c.bw, "usage: WHATIF.COMPARE route-a route-b")
			return
		}
		r, err := c.eng.WhatIf.Compare(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"a_route", r.A.Route,
			"a_quality", strconv.FormatFloat(r.A.ProjectedQuality, 'f', 4, 64),
			"a_cost_usd", strconv.FormatFloat(r.A.ProjectedCostUSD, 'f', 6, 64),
			"a_p99_ms", strconv.FormatFloat(r.A.ProjectedP99MS, 'f', 2, 64),
			"b_route", r.B.Route,
			"b_quality", strconv.FormatFloat(r.B.ProjectedQuality, 'f', 4, 64),
			"b_cost_usd", strconv.FormatFloat(r.B.ProjectedCostUSD, 'f', 6, 64),
			"b_p99_ms", strconv.FormatFloat(r.B.ProjectedP99MS, 'f', 2, 64),
			"quality_winner", r.QualityWinner,
			"cost_winner", r.CostWinner,
			"latency_winner", r.LatencyWinner,
			"recommendation", r.Recommendation,
		})
	case "ROUTES":
		writeArray(c.bw, c.eng.WhatIf.Routes())
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: WHATIF.FORGET route|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.WhatIf.Forget(args[0])))
	case "STATS":
		s := c.eng.WhatIf.Stats()
		writeArray(c.bw, []string{
			"routes", strconv.Itoa(s.Routes),
			"total_observations", strconv.FormatInt(s.TotalObservations, 10),
			"total_simulations", strconv.FormatInt(s.TotalSimulations, 10),
		})
	default:
		writeError(c.bw, "unknown WHATIF subcommand: "+sub)
	}
}

// consentCmd handles CONSENT.* — GDPR/CCPA consent ledger.
func (c *conn) consentCmd(sub string, args []string) {
	switch sub {
	case "GRANT":
		if len(args) < 3 {
			writeError(c.bw, "usage: CONSENT.GRANT user scope purpose [TTL s] [META k v ...]")
			return
		}
		var ttl time.Duration
		meta := map[string]string{}
		for i := 3; i < len(args); i++ {
			key := strings.ToUpper(args[i])
			switch key {
			case "TTL":
				if i+1 >= len(args) {
					writeError(c.bw, "TTL needs a value")
					return
				}
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "TTL must be non-negative integer (seconds)")
					return
				}
				ttl = time.Duration(n) * time.Second
				i++
			case "META":
				for j := i + 1; j+1 < len(args); j += 2 {
					if strings.ToUpper(args[j]) == "TTL" {
						break
					}
					meta[args[j]] = args[j+1]
					i = j + 1
				}
			default:
				writeError(c.bw, "unknown CONSENT.GRANT option: "+key)
				return
			}
		}
		if err := c.eng.Consent.Grant(args[0], args[1], args[2], ttl, meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REVOKE":
		if len(args) < 3 {
			writeError(c.bw, "usage: CONSENT.REVOKE user scope purpose")
			return
		}
		n, err := c.eng.Consent.Revoke(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "WITHDRAW":
		if len(args) < 1 {
			writeError(c.bw, "usage: CONSENT.WITHDRAW user")
			return
		}
		n, err := c.eng.Consent.Withdraw(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "PERMITS":
		if len(args) < 3 {
			writeError(c.bw, "usage: CONSENT.PERMITS user scope purpose")
			return
		}
		if c.eng.Consent.Permits(args[0], args[1], args[2]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "CHECK":
		if len(args) < 3 {
			writeError(c.bw, "usage: CONSENT.CHECK user scope purpose")
			return
		}
		r := c.eng.Consent.Check(args[0], args[1], args[2])
		allow := "0"
		if r.Allow {
			allow = "1"
		}
		writeArray(c.bw, []string{
			"user", r.User,
			"scope", r.Scope,
			"purpose", r.Purpose,
			"allow", allow,
			"granted_unix", strconv.FormatInt(r.GrantedUnix, 10),
			"expires_unix", strconv.FormatInt(r.ExpiresUnix, 10),
			"reason", r.Reason,
		})
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "usage: CONSENT.LIST user")
			return
		}
		rows, ok := c.eng.Consent.List(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			expired := "0"
			if r.Expired {
				expired = "1"
			}
			out = append(out, []any{
				"scope", r.Scope,
				"purpose", r.Purpose,
				"granted_unix", strconv.FormatInt(r.GrantedUnix, 10),
				"expires_unix", strconv.FormatInt(r.ExpiresUnix, 10),
				"expired", expired,
			})
		}
		writeValue(c.bw, out)
	case "EXPIRING":
		within := 24 * time.Hour
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "WITHIN" {
				writeError(c.bw, "unknown CONSENT.EXPIRING option: "+key)
				return
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "WITHIN must be non-negative integer (seconds)")
				return
			}
			within = time.Duration(n) * time.Second
		}
		rows := c.eng.Consent.Expiring(within)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"user", r.User,
				"scope", r.Scope,
				"purpose", r.Purpose,
				"expires_unix", strconv.FormatInt(r.ExpiresUnix, 10),
				"seconds_left", strconv.FormatInt(r.SecondsLeft, 10),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Consent.Stats()
		writeArray(c.bw, []string{
			"grants", strconv.Itoa(s.Grants),
			"total_grants", strconv.FormatInt(s.TotalGrants, 10),
			"total_revokes", strconv.FormatInt(s.TotalRevokes, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_denials", strconv.FormatInt(s.TotalDenials, 10),
		})
	default:
		writeError(c.bw, "unknown CONSENT subcommand: "+sub)
	}
}

// graphExtractCmd handles GRAPH.EXTRACT.* — auto-triple extractor.
// Uses sub-subcommands (RUN/LIST/SOURCES/FORGET/STATS) under
// GRAPH.EXTRACT.* to keep it cleanly namespaced away from the existing
// GRAPH.LINK/UNLINK family.
func (c *conn) graphExtractCmd(sub string, args []string) {
	switch sub {
	case "RUN":
		if len(args) < 2 {
			writeError(c.bw, "usage: GRAPH.EXTRACT.RUN graph text [SOURCE s]")
			return
		}
		source := ""
		for i := 2; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "SOURCE" {
				writeError(c.bw, "unknown GRAPH.EXTRACT.RUN option: "+key)
				return
			}
			source = args[i+1]
		}
		trips, err := c.eng.GraphExtract.Run(args[0], args[1], source)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(trips))
		for _, t := range trips {
			out = append(out, []any{
				"subject", t.Subject,
				"relation", t.Relation,
				"object", t.Object,
				"source", t.Source,
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "usage: GRAPH.EXTRACT.LIST graph [LIMIT n]")
			return
		}
		limit := 0
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown GRAPH.EXTRACT.LIST option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.GraphExtract.List(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, t := range rows {
			out = append(out, []any{
				"subject", t.Subject,
				"relation", t.Relation,
				"object", t.Object,
				"source", t.Source,
			})
		}
		writeValue(c.bw, out)
	case "SOURCES":
		if len(args) < 1 {
			writeError(c.bw, "usage: GRAPH.EXTRACT.SOURCES graph")
			return
		}
		writeArray(c.bw, c.eng.GraphExtract.Sources(args[0]))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: GRAPH.EXTRACT.FORGET graph|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.GraphExtract.Forget(args[0])))
	case "STATS":
		s := c.eng.GraphExtract.Stats()
		writeArray(c.bw, []string{
			"graphs", strconv.Itoa(s.Graphs),
			"total_runs", strconv.FormatInt(s.TotalRuns, 10),
			"total_triples", strconv.FormatInt(s.TotalTriples, 10),
			"total_dupes", strconv.FormatInt(s.TotalDupes, 10),
		})
	default:
		writeError(c.bw, "unknown GRAPH.EXTRACT subcommand: "+sub)
	}
}
