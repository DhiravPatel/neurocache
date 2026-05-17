package resp

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Phase 15 handlers — Part 1 of 4. ATTEST + MARKET + AUTO.

// attestCmd handles ATTEST.* — tamper-evident hash-chained log.
func (c *conn) attestCmd(sub string, args []string) {
	switch sub {
	case "LOG":
		if len(args) < 2 {
			writeError(c.bw, "usage: ATTEST.LOG log-id json-payload")
			return
		}
		r, err := c.eng.Attestation.Log(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"seq", strconv.FormatInt(r.Seq, 10),
			"leaf_hash", r.LeafHash,
			"prev_hash", r.PrevHash,
		})
	case "ROOT":
		if len(args) < 1 {
			writeError(c.bw, "usage: ATTEST.ROOT log-id")
			return
		}
		r, ok := c.eng.Attestation.Root(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"log_id", r.LogID,
			"seq", strconv.FormatInt(r.Seq, 10),
			"merkle_root", r.MerkleRoot,
			"head_hash", r.HeadHash,
		})
	case "PROVE":
		if len(args) < 2 {
			writeError(c.bw, "usage: ATTEST.PROVE log-id seq")
			return
		}
		seq, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "seq must be integer")
			return
		}
		r, ok := c.eng.Attestation.Prove(args[0], seq)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		indices := make([]string, len(r.Indices))
		for i, x := range r.Indices {
			indices[i] = strconv.Itoa(x)
		}
		writeArray(c.bw, []string{
			"log_id", r.LogID,
			"seq", strconv.FormatInt(r.Seq, 10),
			"canon", r.Canon,
			"leaf_hash", r.LeafHash,
			"path", strings.Join(r.Path, ","),
			"indices", strings.Join(indices, ","),
			"root", r.Root,
		})
	case "VERIFY":
		if len(args) < 4 {
			writeError(c.bw, "usage: ATTEST.VERIFY root leaf-canon path-csv indices-csv")
			return
		}
		path := []string{}
		if args[2] != "" {
			path = strings.Split(args[2], ",")
		}
		idxStrs := []string{}
		if args[3] != "" {
			idxStrs = strings.Split(args[3], ",")
		}
		indices := make([]int, len(idxStrs))
		for i, s := range idxStrs {
			n, err := strconv.Atoi(s)
			if err != nil {
				writeError(c.bw, "indices must be integers")
				return
			}
			indices[i] = n
		}
		valid, err := c.eng.Attestation.Verify(args[0], args[1], path, indices)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if valid {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "RECEIPT":
		if len(args) < 2 {
			writeError(c.bw, "usage: ATTEST.RECEIPT log-id seq [PROV ans-id]")
			return
		}
		seq, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "seq must be integer")
			return
		}
		var ansID string
		if len(args) >= 4 && strings.ToUpper(args[2]) == "PROV" {
			ansID = args[3]
		}
		r, ok := c.eng.Attestation.Receipt(args[0], seq, c.eng.Provenance, ansID)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"log_id", r.LogID,
			"seq", strconv.FormatInt(r.Proof.Seq, 10),
			"merkle_root", r.Proof.Root,
			"leaf_hash", r.Proof.LeafHash,
			"canon", r.Proof.Canon,
			"issued_unix", strconv.FormatInt(r.IssuedAt, 10),
			"has_provenance", boolStr(r.Provenance != nil),
		})
	case "SEAL":
		if len(args) < 3 || strings.ToUpper(args[1]) != "PUBKEY" {
			writeError(c.bw, "usage: ATTEST.SEAL log-id PUBKEY hex-bytes")
			return
		}
		pub, err := hex.DecodeString(args[2])
		if err != nil {
			writeError(c.bw, "PUBKEY must be hex")
			return
		}
		if err := c.eng.Attestation.Seal(args[0], pub); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SIGN":
		if len(args) < 4 || strings.ToUpper(args[2]) != "PRIVKEY" {
			writeError(c.bw, "usage: ATTEST.SIGN log-id seq PRIVKEY hex-bytes")
			return
		}
		seq, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "seq must be integer")
			return
		}
		priv, err := hex.DecodeString(args[3])
		if err != nil {
			writeError(c.bw, "PRIVKEY must be hex")
			return
		}
		sig, err := c.eng.Attestation.Sign(args[0], seq, priv)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, sig)
	case "VERIFY_SIG":
		if len(args) < 2 {
			writeError(c.bw, "usage: ATTEST.VERIFY_SIG log-id seq")
			return
		}
		seq, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "seq must be integer")
			return
		}
		ok, reason, err := c.eng.Attestation.VerifySig(args[0], seq)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		valid := "0"
		if ok {
			valid = "1"
		}
		writeArray(c.bw, []string{"valid", valid, "reason", reason})
	case "SCAN":
		if len(args) < 1 {
			writeError(c.bw, "usage: ATTEST.SCAN log-id [FROM seq] [LIMIT n]")
			return
		}
		var from int64
		limit := 100
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
				writeError(c.bw, "unknown ATTEST.SCAN option: "+key)
				return
			}
		}
		rows, ok := c.eng.Attestation.Scan(args[0], from, limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			signed := "0"
			if r.Signed {
				signed = "1"
			}
			out = append(out, []any{
				"seq", strconv.FormatInt(r.Seq, 10),
				"canon", r.Canon,
				"leaf_hash", r.LeafHash,
				"prev_hash", r.PrevHash,
				"at_unix", strconv.FormatInt(r.AtUnix, 10),
				"signed", signed,
			})
		}
		writeValue(c.bw, out)
	case "HEAD":
		if len(args) < 1 {
			writeError(c.bw, "usage: ATTEST.HEAD log-id")
			return
		}
		h, ok := c.eng.Attestation.Head(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		sealed := "0"
		if h.Sealed {
			sealed = "1"
		}
		writeArray(c.bw, []string{
			"log_id", h.LogID,
			"seq", strconv.FormatInt(h.Seq, 10),
			"head_hash", h.HeadHash,
			"sealed", sealed,
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: ATTEST.FORGET log-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Attestation.Forget(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Attestation.List())
	case "STATS":
		s := c.eng.Attestation.Stats()
		writeArray(c.bw, []string{
			"logs", strconv.Itoa(s.Logs),
			"total_leaves", strconv.Itoa(s.TotalLeaves),
			"total_logs", strconv.FormatInt(s.TotalLogs, 10),
			"total_roots", strconv.FormatInt(s.TotalRoots, 10),
			"total_proves", strconv.FormatInt(s.TotalProves, 10),
			"total_verifies", strconv.FormatInt(s.TotalVerifies, 10),
			"total_signs", strconv.FormatInt(s.TotalSigns, 10),
		})
	default:
		writeError(c.bw, "unknown ATTEST subcommand: "+sub)
	}
}

// marketCmd handles MARKET.* — agent resource auction.
func (c *conn) marketCmd(sub string, args []string) {
	switch sub {
	case "CREATE":
		if len(args) < 3 || strings.ToUpper(args[1]) != "CAPACITY" {
			writeError(c.bw, "usage: MARKET.CREATE id CAPACITY n [CLEARING uniform|second_price] [WINDOW ms] [MAX_BIDS_PER_AGENT n]")
			return
		}
		cap, err := strconv.Atoi(args[2])
		if err != nil {
			writeError(c.bw, "CAPACITY must be integer")
			return
		}
		clearing := "uniform"
		var window time.Duration
		maxPer := 0
		for i := 3; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "CLEARING":
				clearing = strings.ToLower(args[i+1])
			case "WINDOW":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "WINDOW must be non-negative integer (ms)")
					return
				}
				window = time.Duration(n) * time.Millisecond
			case "MAX_BIDS_PER_AGENT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "MAX_BIDS_PER_AGENT must be non-negative integer")
					return
				}
				maxPer = n
			default:
				writeError(c.bw, "unknown MARKET.CREATE option: "+key)
				return
			}
		}
		if err := c.eng.Market.Create(args[0], cap, clearing, window, maxPer); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BID":
		if len(args) < 6 || strings.ToUpper(args[2]) != "PRICE" || strings.ToUpper(args[4]) != "QTY" {
			writeError(c.bw, "usage: MARKET.BID market agent PRICE p QTY q [DEADLINE ms]")
			return
		}
		price, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "PRICE must be float")
			return
		}
		qty, err := strconv.Atoi(args[5])
		if err != nil {
			writeError(c.bw, "QTY must be integer")
			return
		}
		var deadline time.Duration
		if len(args) >= 8 && strings.ToUpper(args[6]) == "DEADLINE" {
			n, err := strconv.ParseInt(args[7], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "DEADLINE must be non-negative integer (ms)")
				return
			}
			deadline = time.Duration(n) * time.Millisecond
		}
		r, err := c.eng.Market.Bid(args[0], args[1], price, qty, deadline)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"bid_id", r.BidID,
			"position", strconv.Itoa(r.Position),
		})
	case "CLEAR":
		if len(args) < 1 {
			writeError(c.bw, "usage: MARKET.CLEAR market")
			return
		}
		r, err := c.eng.Market.Clear(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		filled := make([]any, 0, len(r.Filled))
		for _, f := range r.Filled {
			filled = append(filled, []any{
				"bid_id", f.BidID, "agent_id", f.AgentID,
				"price", strconv.FormatFloat(f.Price, 'f', 4, 64),
				"qty", strconv.Itoa(f.Qty),
				"awarded", strconv.Itoa(f.Awarded),
			})
		}
		unfilled := make([]any, 0, len(r.Unfilled))
		for _, f := range r.Unfilled {
			unfilled = append(unfilled, []any{
				"bid_id", f.BidID, "agent_id", f.AgentID,
				"price", strconv.FormatFloat(f.Price, 'f', 4, 64),
				"qty", strconv.Itoa(f.Qty),
			})
		}
		writeValue(c.bw, []any{
			"market_id", r.MarketID,
			"clearing_price", strconv.FormatFloat(r.ClearingPrice, 'f', 4, 64),
			"capacity", strconv.Itoa(r.Capacity),
			"cleared_unix", strconv.FormatInt(r.ClearedAt, 10),
			"filled", filled,
			"unfilled", unfilled,
		})
	case "LEASE":
		if len(args) < 2 {
			writeError(c.bw, "usage: MARKET.LEASE market agent")
			return
		}
		r, err := c.eng.Market.Lease(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"token", r.Token,
			"agent_id", r.AgentID,
			"qty", strconv.Itoa(r.Qty),
			"price", strconv.FormatFloat(r.Price, 'f', 4, 64),
		})
	case "RELEASE":
		if len(args) < 2 {
			writeError(c.bw, "usage: MARKET.RELEASE market token")
			return
		}
		n, err := c.eng.Market.Release(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "PRICE":
		if len(args) < 1 {
			writeError(c.bw, "usage: MARKET.PRICE market")
			return
		}
		p, ok := c.eng.Market.Price(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeFloat(c.bw, p)
	case "STARVED":
		if len(args) < 1 {
			writeError(c.bw, "usage: MARKET.STARVED market [MIN_LOSSES n]")
			return
		}
		minLosses := 3
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "MIN_LOSSES" {
				writeError(c.bw, "unknown MARKET.STARVED option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				writeError(c.bw, "MIN_LOSSES must be positive integer")
				return
			}
			minLosses = n
		}
		rows, ok := c.eng.Market.Starved(args[0], minLosses)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{"agent_id", r.AgentID, "losses", strconv.Itoa(r.Losses)})
		}
		writeValue(c.bw, out)
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: MARKET.STATUS market")
			return
		}
		st, ok := c.eng.Market.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"market_id", st.MarketID,
			"capacity", strconv.Itoa(st.Capacity),
			"clearing", st.Clearing,
			"window_ms", strconv.FormatInt(st.WindowMS, 10),
			"pending_bids", strconv.Itoa(st.PendingBids),
			"active_leases", strconv.Itoa(st.ActiveLeases),
			"last_price", strconv.FormatFloat(st.LastPrice, 'f', 4, 64),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: MARKET.FORGET market|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Market.Forget(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Market.List())
	case "STATS":
		s := c.eng.Market.Stats()
		writeArray(c.bw, []string{
			"markets", strconv.Itoa(s.Markets),
			"total_bids", strconv.FormatInt(s.TotalBids, 10),
			"total_clears", strconv.FormatInt(s.TotalClears, 10),
			"total_leases", strconv.FormatInt(s.TotalLeases, 10),
		})
	default:
		writeError(c.bw, "unknown MARKET subcommand: "+sub)
	}
}

// autoCmd handles AUTO.* — autonomous closed-loop rules.
func (c *conn) autoCmd(sub string, args []string) {
	switch sub {
	case "RULE":
		if len(args) < 5 || strings.ToUpper(args[1]) != "WHEN" || strings.ToUpper(args[3]) != "DO" {
			writeError(c.bw, "usage: AUTO.RULE id WHEN \"cond\" DO \"action\" [COOLDOWN ms]")
			return
		}
		cond := args[2]
		action := args[4]
		var cooldown time.Duration
		if len(args) >= 7 && strings.ToUpper(args[5]) == "COOLDOWN" {
			n, err := strconv.ParseInt(args[6], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "COOLDOWN must be non-negative integer (ms)")
				return
			}
			cooldown = time.Duration(n) * time.Millisecond
		}
		if err := c.eng.Auto.Rule(args[0], cond, action, cooldown); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNRULE":
		if len(args) < 1 {
			writeError(c.bw, "usage: AUTO.UNRULE id")
			return
		}
		writeInt(c.bw, int64(c.eng.Auto.Unrule(args[0])))
	case "EVALUATE":
		limit := 0
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown AUTO.EVALUATE option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		fires := c.eng.Auto.Evaluate(limit)
		out := make([]any, 0, len(fires))
		for _, f := range fires {
			out = append(out, []any{
				"rule_id", f.RuleID, "action", f.Action,
				"at_unix", strconv.FormatInt(f.AtUnix, 10),
				"reason", f.Reason,
			})
		}
		writeValue(c.bw, out)
	case "DRYRUN":
		if len(args) < 1 {
			writeError(c.bw, "usage: AUTO.DRYRUN id")
			return
		}
		r, ok := c.eng.Auto.DryRun(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		truth := "0"
		if r.Truth {
			truth = "1"
		}
		would := "0"
		if r.WouldFire {
			would = "1"
		}
		writeArray(c.bw, []string{
			"rule_id", r.RuleID,
			"truth", truth,
			"would_fire", would,
			"reason", r.Reason,
		})
	case "FIRES":
		ruleID := ""
		limit := 0
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "RULE":
				ruleID = args[i+1]
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown AUTO.FIRES option: "+key)
				return
			}
		}
		fires := c.eng.Auto.Fires(ruleID, limit)
		out := make([]any, 0, len(fires))
		for _, f := range fires {
			out = append(out, []any{
				"rule_id", f.RuleID, "action", f.Action,
				"at_unix", strconv.FormatInt(f.AtUnix, 10),
				"reason", f.Reason,
			})
		}
		writeValue(c.bw, out)
	case "PAUSE":
		if len(args) < 1 {
			writeError(c.bw, "usage: AUTO.PAUSE id")
			return
		}
		if err := c.eng.Auto.Pause(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESUME":
		if len(args) < 1 {
			writeError(c.bw, "usage: AUTO.RESUME id")
			return
		}
		if err := c.eng.Auto.Resume(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		rows := c.eng.Auto.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			paused := "0"
			if r.Paused {
				paused = "1"
			}
			was := "0"
			if r.WasTrue {
				was = "1"
			}
			out = append(out, []any{
				"rule_id", r.ID,
				"when", r.Condition,
				"do", r.Action,
				"cooldown_ms", strconv.FormatInt(r.CooldownMS, 10),
				"paused", paused,
				"last_fired_unix", strconv.FormatInt(r.LastFiredUnix, 10),
				"was_true", was,
			})
		}
		writeValue(c.bw, out)
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: AUTO.GET id")
			return
		}
		r, ok := c.eng.Auto.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		paused := "0"
		if r.Paused {
			paused = "1"
		}
		was := "0"
		if r.WasTrue {
			was = "1"
		}
		writeArray(c.bw, []string{
			"rule_id", r.ID,
			"when", r.Condition,
			"do", r.Action,
			"cooldown_ms", strconv.FormatInt(r.CooldownMS, 10),
			"paused", paused,
			"last_fired_unix", strconv.FormatInt(r.LastFiredUnix, 10),
			"was_true", was,
		})
	case "STATS":
		s := c.eng.Auto.Stats()
		writeArray(c.bw, []string{
			"rules", strconv.Itoa(s.Rules),
			"fires_logged", strconv.Itoa(s.Fires),
			"total_evals", strconv.FormatInt(s.TotalEvals, 10),
			"total_fires", strconv.FormatInt(s.TotalFires, 10),
		})
	default:
		writeError(c.bw, "unknown AUTO subcommand: "+sub)
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
