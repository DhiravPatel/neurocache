package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// goalCmd handles GOAL.* — agent objective + stagnation tracking.
func (c *conn) goalCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'goal.set'")
			return
		}
		if err := c.eng.Goal.Set(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PROGRESS":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'goal.progress'")
			return
		}
		if err := c.eng.Goal.Progress(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'goal.check'")
			return
		}
		r, ok := c.eng.Goal.Check(args[0])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNSESSION", "no goal set for that session")
			return
		}
		stagInt := "0"
		if r.Stagnation {
			stagInt = "1"
		}
		writeArray(c.bw, []string{
			"progress", strconv.FormatFloat(r.Progress, 'f', 4, 64),
			"stagnation", stagInt,
			"stalled_steps", strconv.Itoa(r.StalledSteps),
			"hint", r.Hint,
			"total_updates", strconv.Itoa(r.TotalUpdates),
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'goal.status'")
			return
		}
		s, ok := c.eng.Goal.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"session_id", s.SessionID,
			"goal", s.Goal,
			"started_at", strconv.FormatInt(s.StartedAt, 10),
			"total_updates", strconv.Itoa(s.TotalUpdates),
			"latest_update", s.LatestUpdate,
			"progress", strconv.FormatFloat(s.Progress, 'f', 4, 64),
			"hint", s.Hint,
		})
	case "HISTORY":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'goal.history'")
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
				writeError(c.bw, "unknown GOAL.HISTORY option: "+key)
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
		rows := c.eng.Goal.History(args[0], limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"ts", strconv.FormatInt(r.TS, 10),
				"text", r.Text,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'goal.forget'")
			return
		}
		if c.eng.Goal.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SESSIONS":
		writeArray(c.bw, c.eng.Goal.Sessions())
	case "STATS":
		s := c.eng.Goal.Stats()
		writeArray(c.bw, []string{
			"sessions", strconv.Itoa(s.Sessions),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_progresses", strconv.FormatInt(s.TotalProgresses, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_loops_detected", strconv.FormatInt(s.TotalLoops, 10),
		})
	default:
		writeError(c.bw, "unknown GOAL subcommand: "+sub)
	}
}

// ledgerCmd handles LEDGER.* — cost attribution.
func (c *conn) ledgerCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'ledger.record' (need tenant feature model cost)")
			return
		}
		cost, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "cost must be a float")
			return
		}
		var tokensIn, tokensOut int64
		i := 4
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TOKENS_IN":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "TOKENS_IN must be non-negative")
					return
				}
				tokensIn = n
			case "TOKENS_OUT":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "TOKENS_OUT must be non-negative")
					return
				}
				tokensOut = n
			default:
				writeError(c.bw, "unknown LEDGER.RECORD option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.Ledger.Record(args[0], args[1], args[2], cost, tokensIn, tokensOut); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REPORT":
		// LEDGER.REPORT BY dimension [TENANT t] [FEATURE f] [MODEL m] [WINDOW seconds]
		if len(args) < 2 || !strings.EqualFold(args[0], "BY") {
			writeError(c.bw, "usage: LEDGER.REPORT BY dimension [TENANT t] [FEATURE f] [MODEL m] [WINDOW seconds]")
			return
		}
		dim := args[1]
		f, err := parseLedgerFilter(args[2:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		rows, err := c.eng.Ledger.Report(dim, f)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeValue(c.bw, ledgerRowsReply(rows))
	case "TOP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ledger.top'")
			return
		}
		dim := args[0]
		var window time.Duration
		limit := 10
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "WINDOW":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "WINDOW must be non-negative integer (seconds)")
					return
				}
				window = time.Duration(n) * time.Second
			case "LIMIT":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "LIMIT must be positive")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown LEDGER.TOP option: "+key)
				return
			}
			i += 2
		}
		rows, err := c.eng.Ledger.Top(dim, window, limit)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeValue(c.bw, ledgerRowsReply(rows))
	case "SPEND":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ledger.spend'")
			return
		}
		f, err := parseLedgerFilter(args[1:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		r := c.eng.Ledger.Spend(args[0], f)
		writeArray(c.bw, []string{
			"tenant", r.Tenant,
			"total_cost_usd", strconv.FormatFloat(r.TotalCostUSD, 'f', 6, 64),
			"calls", strconv.FormatInt(r.Calls, 10),
			"tokens_in", strconv.FormatInt(r.TokensIn, 10),
			"tokens_out", strconv.FormatInt(r.TokensOut, 10),
		})
	case "EXPORT":
		f, err := parseLedgerFilter(args)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		format := "csv"
		for i := 0; i+1 < len(args); i += 2 {
			if strings.EqualFold(args[i], "FORMAT") {
				format = args[i+1]
			}
		}
		out, err := c.eng.Ledger.Export(f, format)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, out)
	case "PURGE":
		tenant := ""
		var olderThan time.Duration
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TENANT":
				tenant = val
			case "OLDER_THAN":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "OLDER_THAN must be non-negative integer (seconds)")
					return
				}
				olderThan = time.Duration(n) * time.Second
			default:
				writeError(c.bw, "unknown LEDGER.PURGE option: "+key)
				return
			}
			i += 2
		}
		writeInt(c.bw, int64(c.eng.Ledger.Purge(tenant, olderThan)))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ledger.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			writeError(c.bw, "cap must be a non-negative integer")
			return
		}
		c.eng.Ledger.SetCap(n)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.Ledger.Stats()
		writeArray(c.bw, []string{
			"records", strconv.Itoa(s.Records),
			"cap", strconv.Itoa(s.Cap),
			"tenants", strconv.Itoa(s.Tenants),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_reports", strconv.FormatInt(s.TotalReports, 10),
			"total_spend_usd", strconv.FormatFloat(s.TotalSpendUSD, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown LEDGER subcommand: "+sub)
	}
}

func parseLedgerFilter(rest []string) (llmstack.LedgerFilter, error) {
	f := llmstack.LedgerFilter{}
	i := 0
	for i+1 < len(rest)+1 && i < len(rest) {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return f, &v21Err{msg: key + " needs a value"}
		}
		val := rest[i+1]
		switch key {
		case "TENANT":
			f.Tenant = val
		case "FEATURE":
			f.Feature = val
		case "MODEL":
			f.Model = val
		case "WINDOW":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				return f, &v21Err{msg: "WINDOW must be non-negative integer (seconds)"}
			}
			f.Window = time.Duration(n) * time.Second
		case "FORMAT":
			// recognized by EXPORT but unused for filter; ignore here
		default:
			return f, &v21Err{msg: "unknown ledger option: " + key}
		}
		i += 2
	}
	return f, nil
}

func ledgerRowsReply(rows []llmstack.LedgerReportRow) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, []any{
			"key", r.Key,
			"total_cost_usd", strconv.FormatFloat(r.TotalCostUSD, 'f', 6, 64),
			"calls", strconv.FormatInt(r.Calls, 10),
			"tokens_in", strconv.FormatInt(r.TokensIn, 10),
			"tokens_out", strconv.FormatInt(r.TokensOut, 10),
			"avg_cost_per_call", strconv.FormatFloat(r.AvgCostPerCall, 'f', 6, 64),
		})
	}
	return out
}

// embMigrateCmd handles EMB.MIGRATE.* — embedding-model migration.
func (c *conn) embMigrateCmd(sub string, args []string) {
	switch sub {
	case "START":
		// EMB.MIGRATE.START id FROM old-model TO new-model
		if len(args) < 5 {
			writeError(c.bw, "usage: EMB.MIGRATE.START id FROM old-model TO new-model")
			return
		}
		if !strings.EqualFold(args[1], "FROM") || !strings.EqualFold(args[3], "TO") {
			writeError(c.bw, "syntax: EMB.MIGRATE.START id FROM old TO new")
			return
		}
		if err := c.eng.EmbMigrate.Start(args[0], args[2], args[4]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "WRITE":
		// EMB.MIGRATE.WRITE id row-id OLD v,v,v NEW v,v,v
		if len(args) < 6 {
			writeError(c.bw, "usage: EMB.MIGRATE.WRITE id row-id OLD v,v NEW v,v")
			return
		}
		var oldVec, newVec []float64
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "OLD":
				v, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, "OLD: "+err.Error())
					return
				}
				oldVec = v
			case "NEW":
				v, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, "NEW: "+err.Error())
					return
				}
				newVec = v
			default:
				writeError(c.bw, "unknown EMB.MIGRATE.WRITE option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.EmbMigrate.Write(args[0], args[1], oldVec, newVec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'emb.migrate.status'")
			return
		}
		r, ok := c.eng.EmbMigrate.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		cutInt := "0"
		if r.CutOver {
			cutInt = "1"
		}
		writeArray(c.bw, []string{
			"migration_id", r.MigrationID,
			"from_model", r.FromModel,
			"to_model", r.ToModel,
			"started_at", strconv.FormatInt(r.StartedAt, 10),
			"rows_written", strconv.Itoa(r.RowsWritten),
			"old_dim", strconv.Itoa(r.OldDim),
			"new_dim", strconv.Itoa(r.NewDim),
			"cut_over", cutInt,
		})
	case "COMPARE":
		// EMB.MIGRATE.COMPARE id OLD v,v NEW v,v K n
		if len(args) < 5 {
			writeError(c.bw, "usage: EMB.MIGRATE.COMPARE id OLD v,v NEW v,v [K n]")
			return
		}
		var oldQ, newQ []float64
		k := 10
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "OLD":
				v, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, "OLD: "+err.Error())
					return
				}
				oldQ = v
			case "NEW":
				v, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, "NEW: "+err.Error())
					return
				}
				newQ = v
			case "K":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "K must be positive integer")
					return
				}
				k = n
			default:
				writeError(c.bw, "unknown EMB.MIGRATE.COMPARE option: "+key)
				return
			}
			i += 2
		}
		r, ok := c.eng.EmbMigrate.Compare(args[0], oldQ, newQ, k)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNMIGRATION", "no migration registered or dim mismatch")
			return
		}
		oldAny := topkReply(r.OldTopK)
		newAny := topkReply(r.NewTopK)
		writeValue(c.bw, []any{
			"old_topk", oldAny,
			"new_topk", newAny,
			"overlap_at_k", strconv.Itoa(r.OverlapAtK),
			"jaccard_at_k", strconv.FormatFloat(r.JaccardAtK, 'f', 4, 64),
		})
	case "CUTOVER":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'emb.migrate.cutover'")
			return
		}
		if c.eng.EmbMigrate.Cutover(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "ABORT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'emb.migrate.abort'")
			return
		}
		if c.eng.EmbMigrate.Abort(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		rows := c.eng.EmbMigrate.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			cutInt := "0"
			if r.CutOver {
				cutInt = "1"
			}
			out = append(out, []any{
				"migration_id", r.MigrationID,
				"from_model", r.FromModel,
				"to_model", r.ToModel,
				"rows", strconv.Itoa(r.Rows),
				"cut_over", cutInt,
				"started_at", strconv.FormatInt(r.StartedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.EmbMigrate.Stats()
		writeArray(c.bw, []string{
			"active", strconv.Itoa(s.Active),
			"total_starts", strconv.FormatInt(s.TotalStarts, 10),
			"total_writes", strconv.FormatInt(s.TotalWrites, 10),
			"total_compares", strconv.FormatInt(s.TotalCompares, 10),
			"total_cutovers", strconv.FormatInt(s.TotalCutovers, 10),
			"total_aborts", strconv.FormatInt(s.TotalAborts, 10),
		})
	default:
		writeError(c.bw, "unknown EMB.MIGRATE subcommand: "+sub)
	}
}

func topkReply(hits []llmstack.TopKHit) []any {
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, []any{
			"row_id", h.RowID,
			"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
		})
	}
	return out
}

type v21Err struct{ msg string }

func (e *v21Err) Error() string { return e.msg }
