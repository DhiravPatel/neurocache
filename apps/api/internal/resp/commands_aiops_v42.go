package resp

import (
	"strconv"
	"strings"
	"time"
)

// Phase 17 handlers — NETTING + XTXN + AIWAL.

// nettingCmd handles NETTING.* — clearing/netting on top of SETTLE.
func (c *conn) nettingCmd(sub string, args []string) {
	switch sub {
	case "OPEN":
		if len(args) < 1 {
			writeError(c.bw, "usage: NETTING.OPEN cycle-id [DEADLINE ms]")
			return
		}
		var deadline time.Duration
		if len(args) >= 3 && strings.ToUpper(args[1]) == "DEADLINE" {
			n, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "DEADLINE must be non-negative integer (ms)")
				return
			}
			deadline = time.Duration(n) * time.Millisecond
		}
		if err := c.eng.Netting.Open(args[0], deadline); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADD":
		if len(args) < 4 {
			writeError(c.bw, "usage: NETTING.ADD cycle from to amount [TXN_ID i]")
			return
		}
		amt, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "amount must be float")
			return
		}
		txnRef := ""
		if len(args) >= 6 && strings.ToUpper(args[4]) == "TXN_ID" {
			txnRef = args[5]
		}
		if err := c.eng.Netting.Add(args[0], args[1], args[2], amt, txnRef); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CLOSE":
		if len(args) < 1 {
			writeError(c.bw, "usage: NETTING.CLOSE cycle-id [DRY_RUN 0|1]")
			return
		}
		dryRun := false
		if len(args) >= 3 && strings.ToUpper(args[1]) == "DRY_RUN" {
			dryRun = args[2] == "1" || strings.ToLower(args[2]) == "true"
		}
		p, err := c.eng.Netting.Close(args[0], dryRun)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		plan := make([]any, 0, len(p.Plan))
		for _, pp := range p.Plan {
			plan = append(plan, []any{
				"from", pp.From, "to", pp.To,
				"amount", strconv.FormatFloat(pp.Amount, 'f', 4, 64),
			})
		}
		dr := "0"
		if p.DryRun {
			dr = "1"
		}
		writeValue(c.bw, []any{
			"cycle_id", p.CycleID,
			"gross_count", strconv.Itoa(p.GrossCount),
			"gross_total", strconv.FormatFloat(p.GrossTotal, 'f', 4, 64),
			"net_transfers", strconv.Itoa(p.NetTransfers),
			"net_total", strconv.FormatFloat(p.NetTotal, 'f', 4, 64),
			"savings_pct", strconv.FormatFloat(p.SavingsPct, 'f', 2, 64),
			"dry_run", dr,
			"plan", plan,
		})
	case "APPLY":
		if len(args) < 1 {
			writeError(c.bw, "usage: NETTING.APPLY cycle-id [LEDGER l]")
			return
		}
		ledger := "default"
		if len(args) >= 3 && strings.ToUpper(args[1]) == "LEDGER" {
			ledger = args[2]
		}
		r, err := c.eng.Netting.Apply(args[0], ledger)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"cycle_id", r.CycleID,
			"state", r.State,
			"posted_txn_ids", strings.Join(r.PostedTxnIDs, ","),
			"failed_at", strconv.Itoa(r.FailedAt),
			"reason", r.Reason,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: NETTING.STATUS cycle-id")
			return
		}
		s, ok := c.eng.Netting.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		plan := make([]any, 0, len(s.Plan))
		for _, p := range s.Plan {
			plan = append(plan, []any{
				"from", p.From, "to", p.To,
				"amount", strconv.FormatFloat(p.Amount, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"cycle_id", s.CycleID,
			"state", s.State,
			"gross_count", strconv.Itoa(s.GrossCount),
			"gross_total", strconv.FormatFloat(s.GrossTotal, 'f', 4, 64),
			"net_transfers", strconv.Itoa(s.NetTransfers),
			"net_total", strconv.FormatFloat(s.NetTotal, 'f', 4, 64),
			"savings_pct", strconv.FormatFloat(s.SavingsPct, 'f', 2, 64),
			"posted_txn_ids", strings.Join(s.PostedTxnIDs, ","),
			"failure_reason", s.FailureReason,
			"deadline_unix", strconv.FormatInt(s.DeadlineUnix, 10),
			"plan", plan,
		})
	case "LIST":
		state := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STATE" {
				writeError(c.bw, "unknown NETTING.LIST option: "+key)
				return
			}
			state = args[i+1]
		}
		rows := c.eng.Netting.List(state)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"cycle_id", r.CycleID, "state", r.State,
				"gross_count", strconv.Itoa(r.GrossCount),
				"net_transfers", strconv.Itoa(r.NetTransfers),
				"savings_pct", strconv.FormatFloat(r.SavingsPct, 'f', 2, 64),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: NETTING.FORGET cycle-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Netting.Forget(args[0])))
	case "STATS":
		s := c.eng.Netting.Stats()
		writeArray(c.bw, []string{
			"cycles", strconv.Itoa(s.Cycles),
			"total_opens", strconv.FormatInt(s.TotalOpens, 10),
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_closes", strconv.FormatInt(s.TotalCloses, 10),
			"total_applies", strconv.FormatInt(s.TotalApplies, 10),
		})
	default:
		writeError(c.bw, "unknown NETTING subcommand: "+sub)
	}
}

// xtxnCmd handles XTXN.* — cross-primitive two-phase commit coordinator.
func (c *conn) xtxnCmd(sub string, args []string) {
	switch sub {
	case "BEGIN":
		if len(args) < 1 {
			writeError(c.bw, "usage: XTXN.BEGIN xid [META k v ...]")
			return
		}
		meta := map[string]string{}
		if len(args) > 1 && strings.ToUpper(args[1]) == "META" {
			for i := 2; i+1 < len(args); i += 2 {
				meta[args[i]] = args[i+1]
			}
		}
		if err := c.eng.XTxn.Begin(args[0], meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STAGE":
		// XTXN.STAGE xid participant op [ARG k v ...]
		if len(args) < 3 {
			writeError(c.bw, "usage: XTXN.STAGE xid participant op [ARG k v ...]")
			return
		}
		stageArgs := map[string]string{}
		idx := 3
		for idx < len(args) {
			if strings.ToUpper(args[idx]) != "ARG" {
				writeError(c.bw, "expected ARG at position "+strconv.Itoa(idx))
				return
			}
			if idx+2 >= len(args) {
				writeError(c.bw, "ARG requires key + value")
				return
			}
			stageArgs[args[idx+1]] = args[idx+2]
			idx += 3
		}
		if err := c.eng.XTxn.Stage(args[0], args[1], args[2], stageArgs); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PREPARE":
		if len(args) < 1 {
			writeError(c.bw, "usage: XTXN.PREPARE xid")
			return
		}
		r, err := c.eng.XTxn.Prepare(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"xid", r.XID, "state", r.State,
			"prepared", strconv.Itoa(r.Prepared),
			"reason", r.Reason,
		})
	case "COMMIT":
		if len(args) < 1 {
			writeError(c.bw, "usage: XTXN.COMMIT xid")
			return
		}
		r, err := c.eng.XTxn.Commit(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"xid", r.XID, "state", r.State,
			"committed", strconv.Itoa(r.Committed),
			"reason", r.Reason,
		})
	case "ABORT":
		if len(args) < 1 {
			writeError(c.bw, "usage: XTXN.ABORT xid [REASON r]")
			return
		}
		reason := ""
		if len(args) >= 3 && strings.ToUpper(args[1]) == "REASON" {
			reason = args[2]
		}
		if err := c.eng.XTxn.Abort(args[0], reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: XTXN.STATUS xid")
			return
		}
		s, ok := c.eng.XTxn.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"xid", s.XID, "state", s.State,
			"staged_count", strconv.Itoa(s.StagedCount),
			"prepared_count", strconv.Itoa(s.PreparedCount),
			"committed_count", strconv.Itoa(s.CommittedCount),
			"reason", s.Reason,
			"created_unix", strconv.FormatInt(s.CreatedUnix, 10),
			"finished_unix", strconv.FormatInt(s.FinishedUnix, 10),
		})
	case "LIST":
		state := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STATE" {
				writeError(c.bw, "unknown XTXN.LIST option: "+key)
				return
			}
			state = args[i+1]
		}
		rows := c.eng.XTxn.List(state)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"xid", r.XID, "state", r.State,
				"staged", strconv.Itoa(r.Staged),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: XTXN.FORGET xid|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.XTxn.Forget(args[0])))
	case "PARTICIPANTS":
		writeArray(c.bw, c.eng.XTxn.Participants())
	case "STATS":
		s := c.eng.XTxn.Stats()
		writeArray(c.bw, []string{
			"active", strconv.Itoa(s.Active),
			"participants_registered", strconv.Itoa(s.Participants),
			"total_begins", strconv.FormatInt(s.TotalBegins, 10),
			"total_stages", strconv.FormatInt(s.TotalStages, 10),
			"total_prepares", strconv.FormatInt(s.TotalPrepares, 10),
			"total_commits", strconv.FormatInt(s.TotalCommits, 10),
			"total_aborts", strconv.FormatInt(s.TotalAborts, 10),
			"total_commit_partials", strconv.FormatInt(s.TotalPartials, 10),
		})
	default:
		writeError(c.bw, "unknown XTXN subcommand: "+sub)
	}
}

// aiwalCmd handles AIWAL.* — per-primitive write-ahead log.
func (c *conn) aiwalCmd(sub string, args []string) {
	switch sub {
	case "APPEND":
		if len(args) < 2 {
			writeError(c.bw, "usage: AIWAL.APPEND primitive entry")
			return
		}
		r, err := c.eng.AIWAL.Append(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, r.Seq)
	case "FSYNC":
		if len(args) < 1 {
			writeError(c.bw, "usage: AIWAL.FSYNC primitive")
			return
		}
		n, err := c.eng.AIWAL.Fsync(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, n)
	case "READ":
		if len(args) < 1 {
			writeError(c.bw, "usage: AIWAL.READ primitive [FROM seq] [LIMIT n]")
			return
		}
		var from int64
		limit := 1000
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "FROM":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "FROM must be non-negative integer")
					return
				}
				from = n
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown AIWAL.READ option: "+key)
				return
			}
		}
		rows, ok := c.eng.AIWAL.Read(args[0], from, limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"seq", strconv.FormatInt(r.Seq, 10),
				"at_unix", strconv.FormatInt(r.AtUnix, 10),
				"data", r.Data,
			})
		}
		writeValue(c.bw, out)
	case "CHECKPOINT":
		if len(args) < 3 {
			writeError(c.bw, "usage: AIWAL.CHECKPOINT primitive seq blob")
			return
		}
		seq, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "seq must be integer")
			return
		}
		if err := c.eng.AIWAL.Checkpoint(args[0], seq, args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RECOVER":
		if len(args) < 1 {
			writeError(c.bw, "usage: AIWAL.RECOVER primitive")
			return
		}
		r, ok := c.eng.AIWAL.Recover(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		replay := make([]any, 0, len(r.Replay))
		for _, e := range r.Replay {
			replay = append(replay, []any{
				"seq", strconv.FormatInt(e.Seq, 10),
				"at_unix", strconv.FormatInt(e.AtUnix, 10),
				"data", e.Data,
			})
		}
		writeValue(c.bw, []any{
			"primitive", r.Primitive,
			"checkpoint_seq", strconv.FormatInt(r.CheckpointSeq, 10),
			"checkpoint_blob", r.CheckpointBlob,
			"fsynced_head", strconv.FormatInt(r.FsyncedHead, 10),
			"replay", replay,
		})
	case "TRUNCATE":
		if len(args) < 3 || strings.ToUpper(args[1]) != "UPTO" {
			writeError(c.bw, "usage: AIWAL.TRUNCATE primitive UPTO seq")
			return
		}
		seq, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "seq must be integer")
			return
		}
		n, err := c.eng.AIWAL.Truncate(args[0], seq)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: AIWAL.STATUS primitive")
			return
		}
		s, ok := c.eng.AIWAL.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"primitive", s.Primitive,
			"head_seq", strconv.FormatInt(s.HeadSeq, 10),
			"fsynced_seq", strconv.FormatInt(s.FsyncedSeq, 10),
			"checkpoint_seq", strconv.FormatInt(s.CheckpointSeq, 10),
			"entry_count", strconv.Itoa(s.EntryCount),
		})
	case "LIST":
		writeArray(c.bw, c.eng.AIWAL.List())
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: AIWAL.FORGET primitive|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.AIWAL.Forget(args[0])))
	case "STATS":
		s := c.eng.AIWAL.Stats()
		writeArray(c.bw, []string{
			"wals", strconv.Itoa(s.WALs),
			"total_appends", strconv.FormatInt(s.TotalAppends, 10),
			"total_reads", strconv.FormatInt(s.TotalReads, 10),
			"total_checkpoints", strconv.FormatInt(s.TotalCheckpoints, 10),
			"total_recovers", strconv.FormatInt(s.TotalRecovers, 10),
			"total_truncates", strconv.FormatInt(s.TotalTruncates, 10),
		})
	default:
		writeError(c.bw, "unknown AIWAL subcommand: "+sub)
	}
}
