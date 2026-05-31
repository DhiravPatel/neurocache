package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// Phase 16 handlers — Part 2 of 3. DR + NEGOTIATE + PROOF + REPRO.

// drCmd handles DR.* — disaster recovery drill.
func (c *conn) drCmd(sub string, args []string) {
	switch sub {
	case "SNAPSHOT":
		if len(args) < 1 {
			writeError(c.bw, "usage: DR.SNAPSHOT id [META k v ...]")
			return
		}
		meta := map[string]string{}
		if len(args) > 1 && strings.ToUpper(args[1]) == "META" {
			for i := 2; i+1 < len(args); i += 2 {
				meta[args[i]] = args[i+1]
			}
		}
		if err := c.eng.DR.Snapshot(args[0], meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CONTRIBUTE":
		if len(args) < 3 {
			writeError(c.bw, "usage: DR.CONTRIBUTE bundle store payload")
			return
		}
		if err := c.eng.DR.Contribute(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SEAL":
		if len(args) < 1 {
			writeError(c.bw, "usage: DR.SEAL bundle")
			return
		}
		if err := c.eng.DR.Seal(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESTORE_INTO":
		if len(args) < 2 {
			writeError(c.bw, "usage: DR.RESTORE_INTO source shadow")
			return
		}
		if err := c.eng.DR.RestoreInto(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ASSERT":
		if len(args) < 2 {
			writeError(c.bw, "usage: DR.ASSERT source shadow")
			return
		}
		r, err := c.eng.DR.Assert(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		per := make([]any, 0, len(r.PerStore))
		for _, p := range r.PerStore {
			m := "0"
			if p.Match {
				m = "1"
			}
			per = append(per, []any{
				"store", p.Store, "source_hash", p.SourceHash,
				"shadow_hash", p.ShadowHash, "match", m,
			})
		}
		am := "0"
		if r.AllMatch {
			am = "1"
		}
		writeValue(c.bw, []any{
			"source_id", r.SourceID, "shadow_id", r.ShadowID,
			"all_match", am,
			"diverged", strings.Join(r.Diverged, ","),
			"missing_in_shadow", strings.Join(r.MissingInShadow, ","),
			"extra_in_shadow", strings.Join(r.ExtraInShadow, ","),
			"per_store", per,
		})
	case "PROMOTE":
		if len(args) < 1 {
			writeError(c.bw, "usage: DR.PROMOTE bundle")
			return
		}
		if err := c.eng.DR.Promote(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		limit := 50
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown DR.LIST option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.DR.List(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			s := "0"
			if r.Sealed {
				s = "1"
			}
			p := "0"
			if r.Promoted {
				p = "1"
			}
			out = append(out, []any{
				"bundle_id", r.BundleID, "sealed", s, "promoted", p,
				"stores", strconv.Itoa(r.Stores),
				"created_unix", strconv.FormatInt(r.CreatedUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: DR.GET bundle")
			return
		}
		v, ok := c.eng.DR.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		hashes := make([]string, 0, len(v.Hashes)*2)
		for k, h := range v.Hashes {
			hashes = append(hashes, k, h)
		}
		s := "0"
		if v.Sealed {
			s = "1"
		}
		p := "0"
		if v.Promoted {
			p = "1"
		}
		writeArray(c.bw, []string{
			"bundle_id", v.BundleID, "sealed", s, "promoted", p,
			"created_unix", strconv.FormatInt(v.CreatedUnix, 10),
			"stores", strings.Join(v.Stores, ","),
			"hashes", strings.Join(hashes, ","),
		})
	case "PAYLOAD":
		if len(args) < 2 {
			writeError(c.bw, "usage: DR.PAYLOAD bundle store")
			return
		}
		p, ok := c.eng.DR.Payload(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeBulk(c.bw, p)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: DR.FORGET bundle|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.DR.Forget(args[0])))
	case "STATS":
		s := c.eng.DR.Stats()
		writeArray(c.bw, []string{
			"bundles", strconv.Itoa(s.Bundles),
			"total_snapshots", strconv.FormatInt(s.TotalSnapshots, 10),
			"total_restores", strconv.FormatInt(s.TotalRestores, 10),
			"total_asserts", strconv.FormatInt(s.TotalAsserts, 10),
			"total_promotes", strconv.FormatInt(s.TotalPromotes, 10),
		})
	default:
		writeError(c.bw, "unknown DR subcommand: "+sub)
	}
}

// negotiateCmd handles NEGOTIATE.* — bilateral bargaining.
func (c *conn) negotiateCmd(sub string, args []string) {
	switch sub {
	case "OPEN":
		if len(args) < 4 {
			writeError(c.bw, "usage: NEGOTIATE.OPEN id buyer seller asset [BATNA_BUYER f] [BATNA_SELLER f] [DEADLINE ms] [META k v ...]")
			return
		}
		opts := llmstack.NegoOpenOpts{Meta: map[string]string{}}
		for i := 4; i < len(args); i++ {
			key := strings.ToUpper(args[i])
			switch key {
			case "BATNA_BUYER":
				if i+1 >= len(args) {
					writeError(c.bw, "BATNA_BUYER needs a value")
					return
				}
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "BATNA_BUYER must be float")
					return
				}
				opts.BatnaBuyer = &f
				i++
			case "BATNA_SELLER":
				if i+1 >= len(args) {
					writeError(c.bw, "BATNA_SELLER needs a value")
					return
				}
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "BATNA_SELLER must be float")
					return
				}
				opts.BatnaSeller = &f
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
				opts.Deadline = msToDuration(n)
				i++
			case "META":
				for j := i + 1; j+1 < len(args); j += 2 {
					nxt := strings.ToUpper(args[j])
					if nxt == "BATNA_BUYER" || nxt == "BATNA_SELLER" || nxt == "DEADLINE" {
						break
					}
					opts.Meta[args[j]] = args[j+1]
					i = j + 1
				}
			default:
				writeError(c.bw, "unknown NEGOTIATE.OPEN option: "+key)
				return
			}
		}
		if err := c.eng.Negotiations.Open(args[0], args[1], args[2], args[3], opts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "OFFER", "COUNTER":
		if len(args) < 3 {
			writeError(c.bw, "usage: NEGOTIATE."+sub+" id party price [TERMS \"...\"]")
			return
		}
		price, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "price must be float")
			return
		}
		terms := ""
		if len(args) >= 5 && strings.ToUpper(args[3]) == "TERMS" {
			terms = args[4]
		}
		if sub == "OFFER" {
			err = c.eng.Negotiations.Offer(args[0], args[1], price, terms)
		} else {
			err = c.eng.Negotiations.Counter(args[0], args[1], price, terms)
		}
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ACCEPT":
		if len(args) < 2 {
			writeError(c.bw, "usage: NEGOTIATE.ACCEPT id party")
			return
		}
		if err := c.eng.Negotiations.Accept(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REJECT":
		if len(args) < 2 {
			writeError(c.bw, "usage: NEGOTIATE.REJECT id party [REASON r]")
			return
		}
		reason := ""
		if len(args) >= 4 && strings.ToUpper(args[2]) == "REASON" {
			reason = args[3]
		}
		if err := c.eng.Negotiations.Reject(args[0], args[1], reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "WALK":
		if len(args) < 2 {
			writeError(c.bw, "usage: NEGOTIATE.WALK id party [REASON r]")
			return
		}
		reason := ""
		if len(args) >= 4 && strings.ToUpper(args[2]) == "REASON" {
			reason = args[3]
		}
		if err := c.eng.Negotiations.Walk(args[0], args[1], reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: NEGOTIATE.GET id")
			return
		}
		v, ok := c.eng.Negotiations.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		moves := make([]any, 0, len(v.Moves))
		for _, m := range v.Moves {
			moves = append(moves, []any{
				"party", m.Party, "kind", m.Kind,
				"price", strconv.FormatFloat(m.Price, 'f', 4, 64),
				"terms", m.Terms, "reason", m.Reason,
				"at_unix", strconv.FormatInt(m.AtUnix, 10),
			})
		}
		bb := ""
		if v.BatnaBuyer != nil {
			bb = strconv.FormatFloat(*v.BatnaBuyer, 'f', 4, 64)
		}
		bs := ""
		if v.BatnaSeller != nil {
			bs = strconv.FormatFloat(*v.BatnaSeller, 'f', 4, 64)
		}
		writeValue(c.bw, []any{
			"nego_id", v.NegoID, "buyer", v.Buyer, "seller", v.Seller,
			"asset", v.Asset, "state", v.State,
			"current_price", strconv.FormatFloat(v.CurrentPrice, 'f', 4, 64),
			"current_party", v.CurrentParty,
			"batna_buyer", bb, "batna_seller", bs,
			"deadline_unix", strconv.FormatInt(v.DeadlineUnix, 10),
			"moves", moves,
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
				writeError(c.bw, "unknown NEGOTIATE.LIST option: "+key)
				return
			}
			state = args[i+1]
		}
		rows := c.eng.Negotiations.List(state)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"nego_id", r.NegoID, "state", r.State,
				"current_price", strconv.FormatFloat(r.Price, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: NEGOTIATE.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Negotiations.Forget(args[0])))
	case "STATS":
		s := c.eng.Negotiations.Stats()
		writeArray(c.bw, []string{
			"negotiations", strconv.Itoa(s.Negotiations),
			"total_opens", strconv.FormatInt(s.TotalOpens, 10),
			"total_offers", strconv.FormatInt(s.TotalOffers, 10),
			"total_accepts", strconv.FormatInt(s.TotalAccepts, 10),
			"total_rejects", strconv.FormatInt(s.TotalRejects, 10),
			"total_walks", strconv.FormatInt(s.TotalWalks, 10),
		})
	default:
		writeError(c.bw, "unknown NEGOTIATE subcommand: "+sub)
	}
}

// proofCmd handles PROOF.* — verifiable computation receipts.
func (c *conn) proofCmd(sub string, args []string) {
	switch sub {
	case "COMMIT":
		if len(args) < 4 {
			writeError(c.bw, "usage: PROOF.COMMIT id model prompt params-json")
			return
		}
		h, err := c.eng.Proofs.Commit(args[0], args[1], args[2], args[3])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, h)
	case "PRODUCE":
		if len(args) < 3 {
			writeError(c.bw, "usage: PROOF.PRODUCE commit-id receipt-id output")
			return
		}
		r, err := c.eng.Proofs.Produce(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"receipt_id", r.ReceiptID,
			"commit_hash", r.CommitHash,
			"output_hash", r.OutputHash,
			"issued_unix", strconv.FormatInt(r.IssuedUnix, 10),
		})
	case "VERIFY":
		if len(args) < 5 {
			writeError(c.bw, "usage: PROOF.VERIFY receipt-id model prompt params-json output")
			return
		}
		r, err := c.eng.Proofs.Verify(args[0], args[1], args[2], args[3], args[4])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		v := "0"
		if r.Valid {
			v = "1"
		}
		writeArray(c.bw, []string{
			"valid", v, "reason", r.Reason,
			"expected_commit_hash", r.ExpectedCommit,
			"expected_output_hash", r.ExpectedOutput,
		})
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROOF.GET receipt-id")
			return
		}
		r, ok := c.eng.Proofs.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"receipt_id", r.ReceiptID,
			"commit_id", r.CommitID,
			"commit_hash", r.CommitHash,
			"output_hash", r.OutputHash,
			"issued_unix", strconv.FormatInt(r.IssuedUnix, 10),
		})
	case "LIST":
		limit := 100
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown PROOF.LIST option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.Proofs.List(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"receipt_id", r.ReceiptID, "commit_id", r.CommitID,
				"commit_hash", r.CommitHash, "output_hash", r.OutputHash,
				"issued_unix", strconv.FormatInt(r.IssuedUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: PROOF.FORGET receipt-id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Proofs.Forget(args[0])))
	case "STATS":
		s := c.eng.Proofs.Stats()
		writeArray(c.bw, []string{
			"commits", strconv.Itoa(s.Commits),
			"receipts", strconv.Itoa(s.Receipts),
			"total_commits", strconv.FormatInt(s.TotalCommits, 10),
			"total_produces", strconv.FormatInt(s.TotalProduces, 10),
			"total_verifies", strconv.FormatInt(s.TotalVerifies, 10),
		})
	default:
		writeError(c.bw, "unknown PROOF subcommand: "+sub)
	}
}

// reproCmd handles REPRO.* — deterministic seed bundles.
func (c *conn) reproCmd(sub string, args []string) {
	switch sub {
	case "BUNDLE":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPRO.BUNDLE id [SEED u64] [META k v ...]")
			return
		}
		var seed uint64
		meta := map[string]string{}
		for i := 1; i < len(args); i++ {
			key := strings.ToUpper(args[i])
			switch key {
			case "SEED":
				if i+1 >= len(args) {
					writeError(c.bw, "SEED needs a value")
					return
				}
				n, err := strconv.ParseUint(args[i+1], 10, 64)
				if err != nil {
					writeError(c.bw, "SEED must be uint64")
					return
				}
				seed = n
				i++
			case "META":
				for j := i + 1; j+1 < len(args); j += 2 {
					if strings.ToUpper(args[j]) == "SEED" {
						break
					}
					meta[args[j]] = args[j+1]
					i = j + 1
				}
			default:
				writeError(c.bw, "unknown REPRO.BUNDLE option: "+key)
				return
			}
		}
		if err := c.eng.Repro.Bundle(args[0], seed, meta); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "USE":
		if len(args) < 2 {
			writeError(c.bw, "usage: REPRO.USE bundle name")
			return
		}
		v, err := c.eng.Repro.Use(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, strconv.FormatUint(v, 10))
	case "TRACE":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPRO.TRACE bundle")
			return
		}
		rows, ok := c.eng.Repro.Trace(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"name", r.Name,
				"value", strconv.FormatUint(r.Value, 10),
				"at_unix", strconv.FormatInt(r.AtUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "HASH":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPRO.HASH bundle")
			return
		}
		h, ok := c.eng.Repro.Hash(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeBulk(c.bw, h)
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPRO.GET bundle")
			return
		}
		v, ok := c.eng.Repro.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"bundle_id", v.BundleID,
			"root_seed", strconv.FormatUint(v.RootSeed, 10),
			"uses", strconv.Itoa(v.Uses),
			"created_unix", strconv.FormatInt(v.CreatedUnix, 10),
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
				writeError(c.bw, "unknown REPRO.LIST option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.Repro.List(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"bundle_id", r.BundleID, "uses", strconv.Itoa(r.Uses),
				"created_unix", strconv.FormatInt(r.CreatedUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: REPRO.FORGET bundle|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Repro.Forget(args[0])))
	case "STATS":
		s := c.eng.Repro.Stats()
		writeArray(c.bw, []string{
			"bundles", strconv.Itoa(s.Bundles),
			"total_bundles", strconv.FormatInt(s.TotalBundles, 10),
			"total_uses", strconv.FormatInt(s.TotalUses, 10),
		})
	default:
		writeError(c.bw, "unknown REPRO subcommand: "+sub)
	}
}
