package resp

import (
	"strconv"
	"strings"
)

// Phase 16 handlers — Part 3 of 3. REGWATCH + EGRESS + LICENSE + REPLAY.SHADOW.

// regwatchCmd handles REGWATCH.* — regulatory-obligation mapper.
func (c *conn) regwatchCmd(sub string, args []string) {
	switch sub {
	case "RULE":
		// REGWATCH.RULE id TIER tier MATCHES "csv" OBLIGATION "..." [JURIS j]
		if len(args) < 7 || strings.ToUpper(args[1]) != "TIER" ||
			strings.ToUpper(args[3]) != "MATCHES" ||
			strings.ToUpper(args[5]) != "OBLIGATION" {
			writeError(c.bw, "usage: REGWATCH.RULE id TIER t MATCHES \"kw,kw2\" OBLIGATION \"...\" [JURIS j]")
			return
		}
		tier := args[2]
		matches := strings.Split(args[4], ",")
		obligation := args[6]
		juris := ""
		if len(args) >= 9 && strings.ToUpper(args[7]) == "JURIS" {
			juris = args[8]
		}
		if err := c.eng.RegWatch.Rule(args[0], tier, matches, obligation, juris); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNRULE":
		if len(args) < 1 {
			writeError(c.bw, "usage: REGWATCH.UNRULE rule-id")
			return
		}
		writeInt(c.bw, int64(c.eng.RegWatch.Unrule(args[0])))
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "usage: REGWATCH.CHECK capability-text")
			return
		}
		r := c.eng.RegWatch.Check(args[0])
		triggered := make([]any, 0, len(r.TriggeredRules))
		for _, t := range r.TriggeredRules {
			triggered = append(triggered, []any{
				"rule_id", t.RuleID, "tier", t.Tier,
				"obligation", t.Obligation, "jurisdiction", t.Jurisdiction,
			})
		}
		writeValue(c.bw, []any{
			"capability", r.Capability,
			"max_tier", r.MaxTier,
			"obligations", strings.Join(r.Obligations, " | "),
			"triggered_rules", triggered,
		})
	case "CROSS":
		if len(args) < 2 {
			writeError(c.bw, "usage: REGWATCH.CROSS before after")
			return
		}
		r := c.eng.RegWatch.Cross(args[0], args[1])
		crossed := "0"
		if r.Crossed {
			crossed = "1"
		}
		writeArray(c.bw, []string{
			"tier_before", r.TierBefore,
			"tier_after", r.TierAfter,
			"crossed", crossed,
			"new_rules", strings.Join(r.NewRules, ","),
		})
	case "RULES":
		juris := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "JURIS" {
				writeError(c.bw, "unknown REGWATCH.RULES option: "+key)
				return
			}
			juris = args[i+1]
		}
		rows := c.eng.RegWatch.Rules(juris)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"rule_id", r.RuleID, "tier", r.Tier,
				"obligation", r.Obligation, "jurisdiction", r.Jurisdiction,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.RegWatch.Stats()
		writeArray(c.bw, []string{
			"rules", strconv.Itoa(s.Rules),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_crosses", strconv.FormatInt(s.TotalCrosses, 10),
		})
	default:
		writeError(c.bw, "unknown REGWATCH subcommand: "+sub)
	}
}

// egressCmd handles EGRESS.* — semantic DLP.
func (c *conn) egressCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "usage: EGRESS.REGISTER cluster text [LABEL l]")
			return
		}
		label := ""
		if len(args) >= 4 && strings.ToUpper(args[2]) == "LABEL" {
			label = args[3]
		}
		if err := c.eng.Egress.Register(args[0], args[1], label); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "usage: EGRESS.CHECK text [CLUSTER c] [MIN_BLOCK f]")
			return
		}
		cluster := ""
		minBlock := 0.0
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "CLUSTER":
				cluster = args[i+1]
			case "MIN_BLOCK":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "MIN_BLOCK must be float")
					return
				}
				minBlock = f
			default:
				writeError(c.bw, "unknown EGRESS.CHECK option: "+key)
				return
			}
		}
		r := c.eng.Egress.Check(args[0], cluster, minBlock)
		bl := "0"
		if r.Blocked {
			bl = "1"
		}
		writeArray(c.bw, []string{
			"blocked", bl,
			"cluster_id", r.ClusterID,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"sample_label", r.SampleLabel,
			"reason", r.Reason,
		})
	case "UNREGISTER":
		if len(args) < 2 {
			writeError(c.bw, "usage: EGRESS.UNREGISTER cluster sample-label")
			return
		}
		writeInt(c.bw, int64(c.eng.Egress.Unregister(args[0], args[1])))
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: EGRESS.RESET cluster|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Egress.Reset(args[0])))
	case "CLUSTERS":
		writeArray(c.bw, c.eng.Egress.Clusters())
	case "STATS":
		s := c.eng.Egress.Stats()
		writeArray(c.bw, []string{
			"clusters", strconv.Itoa(s.Clusters),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_blocks", strconv.FormatInt(s.TotalBlocks, 10),
		})
	default:
		writeError(c.bw, "unknown EGRESS subcommand: "+sub)
	}
}

// licenseCmd handles LICENSE.* — source/use license tracker.
func (c *conn) licenseCmd(sub string, args []string) {
	switch sub {
	case "TAG":
		// LICENSE.TAG source LICENSE "MIT" [URL u] [AUTHOR a]
		if len(args) < 3 || strings.ToUpper(args[1]) != "LICENSE" {
			writeError(c.bw, "usage: LICENSE.TAG source LICENSE name [URL u] [AUTHOR a]")
			return
		}
		license := args[2]
		url := ""
		author := ""
		for i := 3; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "URL":
				url = args[i+1]
			case "AUTHOR":
				author = args[i+1]
			default:
				writeError(c.bw, "unknown LICENSE.TAG option: "+key)
				return
			}
		}
		if err := c.eng.License.Tag(args[0], license, url, author); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNTAG":
		if len(args) < 1 {
			writeError(c.bw, "usage: LICENSE.UNTAG source")
			return
		}
		writeInt(c.bw, int64(c.eng.License.Untag(args[0])))
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: LICENSE.GET source")
			return
		}
		v, ok := c.eng.License.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"source", v.Source, "license", v.License,
			"url", v.URL, "author", v.Author,
		})
	case "MATRIX":
		if len(args) < 2 {
			writeError(c.bw, "usage: LICENSE.MATRIX license use")
			return
		}
		m := c.eng.License.Matrix(args[0], args[1])
		comp := "0"
		if m.Compatible {
			comp = "1"
		}
		known := "0"
		if m.Known {
			known = "1"
		}
		writeArray(c.bw, []string{
			"license", m.License, "use", m.Use,
			"compatible", comp, "known", known, "note", m.Note,
		})
	case "COMPAT_SET":
		if len(args) < 4 {
			writeError(c.bw, "usage: LICENSE.COMPAT_SET license use compatible|incompatible \"note\"")
			return
		}
		var ok bool
		switch strings.ToLower(args[2]) {
		case "compatible", "yes", "1", "true":
			ok = true
		case "incompatible", "no", "0", "false":
			ok = false
		default:
			writeError(c.bw, "third arg must be compatible|incompatible")
			return
		}
		if err := c.eng.License.CompatSet(args[0], args[1], ok, args[3]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 3 || strings.ToUpper(args[1]) != "SOURCES" {
			writeError(c.bw, "usage: LICENSE.CHECK use SOURCES s1,s2,...")
			return
		}
		sources := strings.Split(args[2], ",")
		r := c.eng.License.Check(args[0], sources)
		bl := "0"
		if r.Blocked {
			bl = "1"
		}
		incomp := make([]any, 0, len(r.IncompatibleSources))
		for _, x := range r.IncompatibleSources {
			incomp = append(incomp, []any{
				"source", x.Source, "license", x.License, "reason", x.Reason,
			})
		}
		writeValue(c.bw, []any{
			"use", r.Use, "blocked", bl,
			"incompatible_sources", incomp,
		})
	case "LIST":
		rows := c.eng.License.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"source", r.Source, "license", r.License,
				"url", r.URL, "author", r.Author,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.License.Stats()
		writeArray(c.bw, []string{
			"tags", strconv.Itoa(s.Tags),
			"matrix_size", strconv.Itoa(s.MatrixSize),
			"total_tags", strconv.FormatInt(s.TotalTags, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_blocks", strconv.FormatInt(s.TotalBlocks, 10),
		})
	default:
		writeError(c.bw, "unknown LICENSE subcommand: "+sub)
	}
}

// replayShadowCmd handles REPLAY.SHADOW.* — live continuous shadow replay.
func (c *conn) replayShadowCmd(sub string, args []string) {
	switch sub {
	case "ENABLE":
		if len(args) < 3 {
			writeError(c.bw, "usage: REPLAY.SHADOW.ENABLE pair-id live-route shadow-route [MIN_AGREE f]")
			return
		}
		minAgree := 0.0
		if len(args) >= 5 && strings.ToUpper(args[3]) == "MIN_AGREE" {
			f, err := strconv.ParseFloat(args[4], 64)
			if err != nil {
				writeError(c.bw, "MIN_AGREE must be float")
				return
			}
			minAgree = f
		}
		if err := c.eng.ReplayShadow.Enable(args[0], args[1], args[2], minAgree); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RECORD":
		// REPLAY.SHADOW.RECORD pair req-id LIVE "..." SHADOW "..."
		if len(args) < 6 || strings.ToUpper(args[2]) != "LIVE" || strings.ToUpper(args[4]) != "SHADOW" {
			writeError(c.bw, "usage: REPLAY.SHADOW.RECORD pair req-id LIVE \"live-output\" SHADOW \"shadow-output\"")
			return
		}
		if err := c.eng.ReplayShadow.Record(args[0], args[1], args[3], args[5]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DIVERGENCE":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPLAY.SHADOW.DIVERGENCE pair-id [LIMIT n]")
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
				writeError(c.bw, "unknown DIVERGENCE option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		d, ok := c.eng.ReplayShadow.Divergence(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		alert := "0"
		if d.Alert {
			alert = "1"
		}
		top := make([]any, 0, len(d.TopDivergent))
		for _, t := range d.TopDivergent {
			top = append(top, []any{
				"request_id", t.RequestID,
				"live", t.Live, "shadow", t.Shadow,
				"similarity", strconv.FormatFloat(t.Similarity, 'f', 4, 64),
				"at_unix", strconv.FormatInt(t.AtUnix, 10),
			})
		}
		writeValue(c.bw, []any{
			"pair_id", d.PairID,
			"n", strconv.Itoa(d.N),
			"agree_rate", strconv.FormatFloat(d.AgreeRate, 'f', 4, 64),
			"mean_similarity", strconv.FormatFloat(d.MeanSimilarity, 'f', 4, 64),
			"min_agree", strconv.FormatFloat(d.MinAgree, 'f', 4, 64),
			"alert", alert,
			"top_divergent", top,
		})
	case "DISABLE":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPLAY.SHADOW.DISABLE pair-id")
			return
		}
		if err := c.eng.ReplayShadow.Disable(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		rows := c.eng.ReplayShadow.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			d := "0"
			if r.Disabled {
				d = "1"
			}
			out = append(out, []any{
				"pair_id", r.PairID,
				"live_route", r.LiveRoute,
				"shadow_route", r.ShadowRoute,
				"samples", strconv.Itoa(r.Samples),
				"disabled", d,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPLAY.SHADOW.FORGET pair-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.ReplayShadow.Forget(args[0])))
	case "STATS":
		s := c.eng.ReplayShadow.Stats()
		writeArray(c.bw, []string{
			"pairs", strconv.Itoa(s.Pairs),
			"total_enables", strconv.FormatInt(s.TotalEnables, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_alerts", strconv.FormatInt(s.TotalAlerts, 10),
		})
	default:
		writeError(c.bw, "unknown REPLAY.SHADOW subcommand: "+sub)
	}
}
