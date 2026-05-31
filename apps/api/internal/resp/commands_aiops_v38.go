package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// Phase 15 handlers — Part 4 of 4. CARBON + ENTROPY + TEMPORAL.

// carbonCmd handles CARBON.* — energy/CO₂ attribution.
func (c *conn) carbonCmd(sub string, args []string) {
	switch sub {
	case "INTENSITY":
		if len(args) < 2 {
			writeError(c.bw, "usage: CARBON.INTENSITY model wh-per-1k-tokens")
			return
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "wh-per-1k-tokens must be float")
			return
		}
		if err := c.eng.Carbon.Intensity(args[0], f); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REGION":
		if len(args) < 2 {
			writeError(c.bw, "usage: CARBON.REGION region g-co2-per-kwh")
			return
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "g-co2-per-kwh must be float")
			return
		}
		if err := c.eng.Carbon.Region(args[0], f); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHARGE":
		if len(args) < 4 {
			writeError(c.bw, "usage: CARBON.CHARGE tenant feature model tokens [REGION r]")
			return
		}
		tokens, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil {
			writeError(c.bw, "tokens must be integer")
			return
		}
		region := ""
		if len(args) >= 6 && strings.ToUpper(args[4]) == "REGION" {
			region = args[5]
		}
		r, err := c.eng.Carbon.Charge(args[0], args[1], args[2], region, tokens)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"energy_wh", strconv.FormatFloat(r.EnergyWh, 'f', 4, 64),
			"co2_g", strconv.FormatFloat(r.CO2Gram, 'f', 4, 64),
		})
	case "AGGREGATE":
		tenant := ""
		feature := ""
		model := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "TENANT":
				tenant = args[i+1]
			case "FEATURE":
				feature = args[i+1]
			case "MODEL":
				model = args[i+1]
			default:
				writeError(c.bw, "unknown CARBON.AGGREGATE option: "+key)
				return
			}
		}
		rows := c.eng.Carbon.Aggregate(tenant, feature, model)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"tenant", r.Tenant,
				"feature", r.Feature,
				"model", r.Model,
				"tokens", strconv.FormatInt(r.Tokens, 10),
				"calls", strconv.FormatInt(r.Calls, 10),
				"energy_wh", strconv.FormatFloat(r.EnergyWh, 'f', 4, 64),
				"co2_g", strconv.FormatFloat(r.CO2Gram, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "BUDGET":
		if len(args) < 2 {
			writeError(c.bw, "usage: CARBON.BUDGET tenant co2-grams")
			return
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "co2-grams must be float")
			return
		}
		if err := c.eng.Carbon.Budget(args[0], f); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "OVER":
		if len(args) < 1 {
			writeError(c.bw, "usage: CARBON.OVER tenant")
			return
		}
		r, _ := c.eng.Carbon.Over(args[0])
		over := "0"
		if r.OverBudget {
			over = "1"
		}
		writeArray(c.bw, []string{
			"tenant", r.Tenant,
			"over_budget", over,
			"used_g", strconv.FormatFloat(r.UsedG, 'f', 4, 64),
			"budget_g", strconv.FormatFloat(r.BudgetG, 'f', 4, 64),
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: CARBON.RESET TENANT t|MODEL m|FEATURE f|ALL")
			return
		}
		if strings.ToUpper(args[0]) == "ALL" {
			writeInt(c.bw, int64(c.eng.Carbon.Reset("", "ALL")))
			return
		}
		if len(args) < 2 {
			writeError(c.bw, "RESET kind requires a name")
			return
		}
		writeInt(c.bw, int64(c.eng.Carbon.Reset(strings.ToUpper(args[0]), args[1])))
	case "STATS":
		s := c.eng.Carbon.Stats()
		writeArray(c.bw, []string{
			"models_with_intensity", strconv.Itoa(s.Models),
			"regions_with_intensity", strconv.Itoa(s.Regions),
			"tenants_with_usage", strconv.Itoa(s.Tenants),
			"usage_rows", strconv.Itoa(s.Rows),
			"tenants_with_budget", strconv.Itoa(s.Budgets),
			"total_charges", strconv.FormatInt(s.TotalCharges, 10),
		})
	default:
		writeError(c.bw, "unknown CARBON subcommand: "+sub)
	}
}

// entropyCmd handles ENTROPY.* — population mode-collapse detector.
func (c *conn) entropyCmd(sub string, args []string) {
	switch sub {
	case "OBSERVE":
		if len(args) < 2 {
			writeError(c.bw, "usage: ENTROPY.OBSERVE pop output")
			return
		}
		if err := c.eng.Entropy.Observe(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REPORT":
		if len(args) < 1 {
			writeError(c.bw, "usage: ENTROPY.REPORT pop [TOP n]")
			return
		}
		top := 5
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "TOP" {
				writeError(c.bw, "unknown ENTROPY.REPORT option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "TOP must be non-negative integer")
				return
			}
			top = n
		}
		r, ok := c.eng.Entropy.Report(args[0], top)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		modes := make([]any, 0, len(r.TopModes))
		for _, m := range r.TopModes {
			modes = append(modes, []any{"output", m.Output, "count", strconv.Itoa(m.Count)})
		}
		writeValue(c.bw, []any{
			"pop_id", r.PopID,
			"n", strconv.Itoa(r.N),
			"distinct", strconv.Itoa(r.Distinct),
			"shannon_bits", strconv.FormatFloat(r.ShannonBits, 'f', 4, 64),
			"max_possible_bits", strconv.FormatFloat(r.MaxPossibleBits, 'f', 4, 64),
			"unique_fraction", strconv.FormatFloat(r.UniqueFraction, 'f', 4, 64),
			"verdict", r.Verdict,
			"reason", r.Reason,
			"top_modes", modes,
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: ENTROPY.RESET pop|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Entropy.Reset(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Entropy.List())
	case "STATS":
		s := c.eng.Entropy.Stats()
		writeArray(c.bw, []string{
			"pops", strconv.Itoa(s.Pops),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
			"total_reports", strconv.FormatInt(s.TotalReports, 10),
		})
	default:
		writeError(c.bw, "unknown ENTROPY subcommand: "+sub)
	}
}

// temporalCmd handles TEMPORAL.* — unified point-in-time snapshots.
func (c *conn) temporalCmd(sub string, args []string) {
	switch sub {
	case "SNAPSHOT":
		if len(args) < 1 {
			writeError(c.bw, "usage: TEMPORAL.SNAPSHOT id [META k v ...]")
			return
		}
		meta := map[string]string{}
		if len(args) > 1 && strings.ToUpper(args[1]) == "META" {
			for i := 2; i+1 < len(args); i += 2 {
				meta[args[i]] = args[i+1]
			}
		}
		if err := c.eng.Temporal.Snapshot(args[0], meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CONTRIBUTE":
		if len(args) < 3 {
			writeError(c.bw, "usage: TEMPORAL.CONTRIBUTE id store payload")
			return
		}
		if err := c.eng.Temporal.Contribute(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CLOSE":
		if len(args) < 1 {
			writeError(c.bw, "usage: TEMPORAL.CLOSE id")
			return
		}
		if err := c.eng.Temporal.Close(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "AT":
		if len(args) < 1 {
			writeError(c.bw, "usage: TEMPORAL.AT unix-ms")
			return
		}
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			writeError(c.bw, "unix-ms must be integer")
			return
		}
		v, ok := c.eng.Temporal.At(n)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeTemporalView(c, v)
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: TEMPORAL.GET id")
			return
		}
		gv, ok := c.eng.Temporal.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeTemporalView(c, gv)
	case "DIFF":
		if len(args) < 2 {
			writeError(c.bw, "usage: TEMPORAL.DIFF snap-a snap-b")
			return
		}
		d, ok := c.eng.Temporal.Diff(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		ident := "0"
		if d.Identical {
			ident = "1"
		}
		writeArray(c.bw, []string{
			"snap_a", d.SnapA, "snap_b", d.SnapB,
			"identical", ident,
			"only_in_a", strings.Join(d.OnlyInA, ","),
			"only_in_b", strings.Join(d.OnlyInB, ","),
			"changed", strings.Join(d.Changed, ","),
			"same", strings.Join(d.Same, ","),
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
				writeError(c.bw, "unknown TEMPORAL.LIST option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.Temporal.List(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			closed := "0"
			if r.Closed {
				closed = "1"
			}
			out = append(out, []any{
				"snap_id", r.SnapID,
				"at_unix_ms", strconv.FormatInt(r.AtUnix, 10),
				"closed", closed,
				"stores", strconv.Itoa(r.Stores),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: TEMPORAL.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Temporal.Forget(args[0])))
	case "STATS":
		s := c.eng.Temporal.Stats()
		writeArray(c.bw, []string{
			"snapshots", strconv.Itoa(s.Snapshots),
			"total_snaps", strconv.FormatInt(s.TotalSnaps, 10),
			"total_contributions", strconv.FormatInt(s.TotalContrib, 10),
			"total_queries", strconv.FormatInt(s.TotalQueries, 10),
		})
	default:
		writeError(c.bw, "unknown TEMPORAL subcommand: "+sub)
	}
}

func writeTemporalView(c *conn, tv llmstack.TemporalView) {
	stores := make([]string, 0, len(tv.Stores)*2)
	for k, v := range tv.Stores {
		stores = append(stores, k, v)
	}
	closed := "0"
	if tv.Closed {
		closed = "1"
	}
	writeArray(c.bw, []string{
		"snap_id", tv.SnapID,
		"at_unix_ms", strconv.FormatInt(tv.AtUnix, 10),
		"closed", closed,
		"stores", strings.Join(stores, ","),
	})
}
