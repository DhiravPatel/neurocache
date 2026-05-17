package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

func msToDuration(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// Phase 16 handlers — Part 1 of 3. ACCT + SETTLE + CHAOS + CONTINUAL.

// acctCmd handles ACCT.* (the account half of the double-entry ledger).
func (c *conn) acctCmd(sub string, args []string) {
	switch sub {
	case "OPEN":
		if len(args) < 3 || strings.ToUpper(args[1]) != "TYPE" {
			writeError(c.bw, "usage: ACCT.OPEN name TYPE asset|liability|equity|income|expense [CURRENCY iso] [NO_NEGATIVE 0|1]")
			return
		}
		kind := llmstack.AcctType(strings.ToLower(args[2]))
		currency := "USD"
		var noNeg *bool
		for i := 3; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "CURRENCY":
				currency = args[i+1]
			case "NO_NEGATIVE":
				b := args[i+1] == "1" || strings.ToLower(args[i+1]) == "true"
				noNeg = &b
			default:
				writeError(c.bw, "unknown ACCT.OPEN option: "+key)
				return
			}
		}
		if err := c.eng.Settlement.Open(args[0], kind, currency, noNeg); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BALANCE":
		if len(args) < 1 {
			writeError(c.bw, "usage: ACCT.BALANCE name")
			return
		}
		v, ok := c.eng.Settlement.Balance(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		neg := "0"
		if v.NoNegative {
			neg = "1"
		}
		closed := "0"
		if v.Closed {
			closed = "1"
		}
		writeArray(c.bw, []string{
			"name", v.Name, "type", v.Type, "currency", v.Currency,
			"balance", strconv.FormatFloat(v.Balance, 'f', 4, 64),
			"no_negative", neg, "closed", closed,
			"last_entry_unix", strconv.FormatInt(v.LastEntryUnix, 10),
			"entry_count", strconv.Itoa(v.EntryCount),
		})
	case "STATEMENT":
		if len(args) < 1 {
			writeError(c.bw, "usage: ACCT.STATEMENT name [SINCE unix] [UNTIL unix] [LIMIT n]")
			return
		}
		var since, until int64
		limit := 200
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "SINCE":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil {
					writeError(c.bw, "SINCE must be integer")
					return
				}
				since = n
			case "UNTIL":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil {
					writeError(c.bw, "UNTIL must be integer")
					return
				}
				until = n
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown ACCT.STATEMENT option: "+key)
				return
			}
		}
		rows, ok := c.eng.Settlement.Statement(args[0], since, until, limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"txn_id", r.TxnID, "side", r.Side,
				"amount", strconv.FormatFloat(r.Amount, 'f', 4, 64),
				"running_balance", strconv.FormatFloat(r.Balance, 'f', 4, 64),
				"memo", r.Memo,
				"posted_unix", strconv.FormatInt(r.PostedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "CLOSE":
		if len(args) < 1 {
			writeError(c.bw, "usage: ACCT.CLOSE name")
			return
		}
		if err := c.eng.Settlement.Close(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		writeArray(c.bw, c.eng.Settlement.List())
	default:
		writeError(c.bw, "unknown ACCT subcommand: "+sub)
	}
}

// settleCmd handles SETTLE.* (the transaction half of the ledger).
func (c *conn) settleCmd(sub string, args []string) {
	switch sub {
	case "TXN":
		// SETTLE.TXN txn-id [MEMO "..."] DEBIT a1 amt1 [DEBIT ...] CREDIT b1 amt1 [CREDIT ...]
		if len(args) < 5 {
			writeError(c.bw, "usage: SETTLE.TXN txn-id [MEMO m] DEBIT a amt [DEBIT ...] CREDIT b amt [CREDIT ...]")
			return
		}
		txnID := args[0]
		memo := ""
		idx := 1
		if idx < len(args) && strings.ToUpper(args[idx]) == "MEMO" {
			if idx+1 >= len(args) {
				writeError(c.bw, "MEMO needs a value")
				return
			}
			memo = args[idx+1]
			idx += 2
		}
		var debits, credits []llmstack.SettleLine
		for idx < len(args) {
			tag := strings.ToUpper(args[idx])
			if idx+2 >= len(args) {
				writeError(c.bw, tag+" requires account + amount")
				return
			}
			amt, err := strconv.ParseFloat(args[idx+2], 64)
			if err != nil {
				writeError(c.bw, "amount must be float")
				return
			}
			switch tag {
			case "DEBIT":
				debits = append(debits, llmstack.SettleLine{Account: args[idx+1], Amount: amt})
			case "CREDIT":
				credits = append(credits, llmstack.SettleLine{Account: args[idx+1], Amount: amt})
			default:
				writeError(c.bw, "expected DEBIT or CREDIT, got: "+tag)
				return
			}
			idx += 3
		}
		r, err := c.eng.Settlement.Txn(txnID, memo, debits, credits)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		posted := "0"
		if r.Posted {
			posted = "1"
		}
		dup := "0"
		if r.Duplicate {
			dup = "1"
		}
		writeArray(c.bw, []string{
			"txn_id", r.TxnID, "posted", posted, "duplicate", dup,
			"total", strconv.FormatFloat(r.Total, 'f', 4, 64),
		})
	case "REVERSE":
		if len(args) < 2 {
			writeError(c.bw, "usage: SETTLE.REVERSE original-txn-id new-txn-id [MEMO m]")
			return
		}
		memo := ""
		if len(args) >= 4 && strings.ToUpper(args[2]) == "MEMO" {
			memo = args[3]
		}
		r, err := c.eng.Settlement.Reverse(args[0], args[1], memo)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"txn_id", r.TxnID,
			"total", strconv.FormatFloat(r.Total, 'f', 4, 64),
		})
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: SETTLE.GET txn-id")
			return
		}
		v, ok := c.eng.Settlement.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		debs := make([]any, 0, len(v.Debits))
		for _, d := range v.Debits {
			debs = append(debs, []any{"account", d.Account, "amount", strconv.FormatFloat(d.Amount, 'f', 4, 64)})
		}
		creds := make([]any, 0, len(v.Credits))
		for _, cr := range v.Credits {
			creds = append(creds, []any{"account", cr.Account, "amount", strconv.FormatFloat(cr.Amount, 'f', 4, 64)})
		}
		writeValue(c.bw, []any{
			"txn_id", v.TxnID, "memo", v.Memo,
			"posted_unix", strconv.FormatInt(v.PostedAt, 10),
			"reverses", v.Reverses,
			"debits", debs, "credits", creds,
		})
	case "RECONCILE":
		r := c.eng.Settlement.Reconcile()
		bal := "0"
		if r.Balanced {
			bal = "1"
		}
		writeArray(c.bw, []string{
			"total_debits", strconv.FormatFloat(r.TotalDebits, 'f', 4, 64),
			"total_credits", strconv.FormatFloat(r.TotalCredits, 'f', 4, 64),
			"difference", strconv.FormatFloat(r.Difference, 'f', 6, 64),
			"balanced", bal,
			"account_count", strconv.Itoa(r.AccountCount),
			"txn_count", strconv.Itoa(r.TxnCount),
		})
	case "STATS":
		s := c.eng.Settlement.Stats()
		writeArray(c.bw, []string{
			"accounts", strconv.Itoa(s.Accounts),
			"txns", strconv.Itoa(s.Txns),
			"total_opens", strconv.FormatInt(s.TotalOpens, 10),
			"total_txns", strconv.FormatInt(s.TotalTxns, 10),
			"total_reverses", strconv.FormatInt(s.TotalReverses, 10),
			"total_duplicates", strconv.FormatInt(s.TotalDuplicates, 10),
		})
	default:
		writeError(c.bw, "unknown SETTLE subcommand: "+sub)
	}
}

// chaosCmd handles CHAOS.* — fault injection.
func (c *conn) chaosCmd(sub string, args []string) {
	switch sub {
	case "INJECT":
		// CHAOS.INJECT id TARGET t KIND k [RATE r] [DURATION ms] [SCOPE k=v,k=v] [REASON r]
		if len(args) < 5 {
			writeError(c.bw, "usage: CHAOS.INJECT id TARGET t KIND k [RATE r] [DURATION ms] [SCOPE k=v,...] [REASON r]")
			return
		}
		if strings.ToUpper(args[1]) != "TARGET" || strings.ToUpper(args[3]) != "KIND" {
			writeError(c.bw, "expected TARGET and KIND in fixed positions")
			return
		}
		target, kind := args[2], args[4]
		var rate float64
		var duration int64
		scope := map[string]string{}
		reason := ""
		for i := 5; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "RATE":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "RATE must be float")
					return
				}
				rate = f
			case "DURATION":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "DURATION must be non-negative integer (ms)")
					return
				}
				duration = n
			case "SCOPE":
				for _, kv := range strings.Split(args[i+1], ",") {
					p := strings.SplitN(kv, "=", 2)
					if len(p) == 2 {
						scope[p[0]] = p[1]
					}
				}
			case "REASON":
				reason = args[i+1]
			default:
				writeError(c.bw, "unknown CHAOS.INJECT option: "+key)
				return
			}
		}
		if err := c.eng.Chaos.Inject(args[0], target, kind, rate, msToDuration(duration), scope, reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REVOKE":
		if len(args) < 1 {
			writeError(c.bw, "usage: CHAOS.REVOKE id")
			return
		}
		writeInt(c.bw, int64(c.eng.Chaos.Revoke(args[0])))
	case "ACTIVE":
		target := ""
		kind := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "TARGET":
				target = args[i+1]
			case "KIND":
				kind = args[i+1]
			default:
				writeError(c.bw, "unknown CHAOS.ACTIVE option: "+key)
				return
			}
		}
		rows := c.eng.Chaos.Active(target, kind)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, faultRow(r))
		}
		writeValue(c.bw, out)
	case "HISTORY":
		limit := 0
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown CHAOS.HISTORY option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.Chaos.History(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, faultRow(r))
		}
		writeValue(c.bw, out)
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "usage: CHAOS.CHECK target kind [scope-k v ...]")
			return
		}
		scope := map[string]string{}
		for i := 2; i+1 < len(args); i += 2 {
			scope[args[i]] = args[i+1]
		}
		r := c.eng.Chaos.Check(args[0], args[1], scope)
		inj := "0"
		if r.Injected {
			inj = "1"
		}
		writeArray(c.bw, []string{
			"injected", inj,
			"fault_id", r.FaultID,
			"kind", r.Kind,
			"reason", r.Reason,
		})
	case "STATS":
		s := c.eng.Chaos.Stats()
		writeArray(c.bw, []string{
			"active", strconv.Itoa(s.Active),
			"history", strconv.Itoa(s.History),
			"total_injects", strconv.FormatInt(s.TotalInjects, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_revokes", strconv.FormatInt(s.TotalRevokes, 10),
		})
	default:
		writeError(c.bw, "unknown CHAOS subcommand: "+sub)
	}
}

func faultRow(r llmstack.ChaosFaultView) []any {
	scope := make([]string, 0, len(r.Scope)*2)
	for k, v := range r.Scope {
		scope = append(scope, k, v)
	}
	rev := "0"
	if r.Revoked {
		rev = "1"
	}
	return []any{
		"fault_id", r.ID, "target", r.Target, "kind", r.Kind,
		"rate", strconv.FormatFloat(r.Rate, 'f', 4, 64),
		"scope", strings.Join(scope, ","),
		"reason", r.Reason,
		"started_unix", strconv.FormatInt(r.StartedUnix, 10),
		"expires_unix", strconv.FormatInt(r.ExpiresUnix, 10),
		"revoked", rev,
	}
}

// continualCmd handles CONTINUAL.* — catastrophic-forgetting guards.
func (c *conn) continualCmd(sub string, args []string) {
	switch sub {
	case "CHECKPOINT":
		if len(args) < 3 {
			writeError(c.bw, "usage: CONTINUAL.CHECKPOINT learner-id checkpoint-id payload [BLESS 0|1]")
			return
		}
		bless := false
		if len(args) >= 5 && strings.ToUpper(args[3]) == "BLESS" {
			bless = args[4] == "1" || strings.ToLower(args[4]) == "true"
		}
		if err := c.eng.Continual.Checkpoint(args[0], args[1], args[2], bless); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ANCHOR":
		if len(args) < 4 {
			writeError(c.bw, "usage: CONTINUAL.ANCHOR learner-id anchor-id input expected [TOL f]")
			return
		}
		tol := 0.0
		if len(args) >= 6 && strings.ToUpper(args[4]) == "TOL" {
			f, err := strconv.ParseFloat(args[5], 64)
			if err != nil {
				writeError(c.bw, "TOL must be float")
				return
			}
			tol = f
		}
		if err := c.eng.Continual.Anchor(args[0], args[1], args[2], args[3], tol); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REHEARSE":
		if len(args) < 4 {
			writeError(c.bw, "usage: CONTINUAL.REHEARSE learner obs-id anchor-id observed")
			return
		}
		r, err := c.eng.Continual.Rehearse(args[0], args[1], args[2], args[3])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		pass := "0"
		if r.Pass {
			pass = "1"
		}
		writeArray(c.bw, []string{
			"pass", pass,
			"error", strconv.FormatFloat(r.Error, 'f', 4, 64),
		})
	case "DIVERGENCE":
		if len(args) < 1 {
			writeError(c.bw, "usage: CONTINUAL.DIVERGENCE learner-id")
			return
		}
		d, ok := c.eng.Continual.Divergence(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"learner_id", d.LearnerID,
			"anchors", strconv.Itoa(d.Anchors),
			"observations_seen", strconv.Itoa(d.ObservationsSeen),
			"pass_rate", strconv.FormatFloat(d.PassRate, 'f', 4, 64),
			"mean_error", strconv.FormatFloat(d.MeanError, 'f', 4, 64),
			"blessed_checkpoint", d.BlessedCheckpoint,
			"verdict", d.Verdict, "reason", d.Reason,
		})
	case "ROLLBACK":
		if len(args) < 1 {
			writeError(c.bw, "usage: CONTINUAL.ROLLBACK learner-id [TO checkpoint-id]")
			return
		}
		to := ""
		if len(args) >= 3 && strings.ToUpper(args[1]) == "TO" {
			to = args[2]
		}
		r, err := c.eng.Continual.Rollback(args[0], to)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"learner_id", r.LearnerID,
			"checkpoint_id", r.CheckpointID,
			"payload", r.Payload,
		})
	case "LIST":
		filter := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LEARNER" {
				writeError(c.bw, "unknown CONTINUAL.LIST option: "+key)
				return
			}
			filter = args[i+1]
		}
		rows := c.eng.Continual.List(filter)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"learner_id", r.LearnerID,
				"checkpoints", strconv.Itoa(r.Checkpoints),
				"anchors", strconv.Itoa(r.Anchors),
				"blessed_checkpoint", r.Blessed,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: CONTINUAL.FORGET learner-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Continual.Forget(args[0])))
	case "STATS":
		s := c.eng.Continual.Stats()
		writeArray(c.bw, []string{
			"learners", strconv.Itoa(s.Learners),
			"total_checkpoints", strconv.FormatInt(s.TotalCheckpoints, 10),
			"total_rehearses", strconv.FormatInt(s.TotalRehearses, 10),
			"total_rollbacks", strconv.FormatInt(s.TotalRollbacks, 10),
		})
	default:
		writeError(c.bw, "unknown CONTINUAL subcommand: "+sub)
	}
}
