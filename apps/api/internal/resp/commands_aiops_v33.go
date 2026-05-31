package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// cfCacheCmd handles CFCACHE.* — counterfactual RAG cache.
func (c *conn) cfCacheCmd(sub string, args []string) {
	switch sub {
	case "PUT":
		if len(args) < 3 {
			writeError(c.bw, "usage: CFCACHE.PUT query ctx-hash answer [REFS r ...] [TTL s]")
			return
		}
		var refs []string
		var ttl time.Duration
		for i := 3; i < len(args); i++ {
			key := strings.ToUpper(args[i])
			switch key {
			case "REFS":
				for j := i + 1; j < len(args); j++ {
					if strings.ToUpper(args[j]) == "TTL" {
						break
					}
					refs = append(refs, args[j])
					i = j
				}
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
			default:
				writeError(c.bw, "unknown CFCACHE.PUT option: "+key)
				return
			}
		}
		if err := c.eng.CFCache.Put(args[0], args[1], args[2], refs, ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "usage: CFCACHE.GET query ctx-hash")
			return
		}
		r := c.eng.CFCache.Get(args[0], args[1])
		hit := "0"
		if r.Hit {
			hit = "1"
		}
		writeArray(c.bw, []string{
			"hit", hit,
			"answer", r.Answer,
			"refs", strings.Join(r.Refs, ","),
			"age_ms", strconv.FormatInt(r.AgeMS, 10),
		})
	case "VARIANTS":
		if len(args) < 1 {
			writeError(c.bw, "usage: CFCACHE.VARIANTS query [LIMIT n]")
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
				writeError(c.bw, "unknown CFCACHE.VARIANTS option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.CFCache.Variants(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"ctx_hash", r.CtxHash,
				"answer", r.Answer,
				"refs", strings.Join(r.Refs, ","),
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "DIFF":
		if len(args) < 3 {
			writeError(c.bw, "usage: CFCACHE.DIFF query ctx-a ctx-b")
			return
		}
		d, ok := c.eng.CFCache.Diff(args[0], args[1], args[2])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		ident := "0"
		if d.Identical {
			ident = "1"
		}
		writeArray(c.bw, []string{
			"query", d.Query,
			"ctx_a", d.CtxA, "ctx_b", d.CtxB,
			"identical", ident,
			"only_in_a", strings.Join(d.OnlyInA, "\n"),
			"only_in_b", strings.Join(d.OnlyInB, "\n"),
			"common_lines", strconv.Itoa(d.CommonLines),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: CFCACHE.FORGET query|ALL [CTX h]")
			return
		}
		ctx := ""
		if len(args) >= 3 && strings.ToUpper(args[1]) == "CTX" {
			ctx = args[2]
		}
		writeInt(c.bw, int64(c.eng.CFCache.Forget(args[0], ctx)))
	case "LIST":
		writeArray(c.bw, c.eng.CFCache.List())
	case "STATS":
		s := c.eng.CFCache.Stats()
		writeArray(c.bw, []string{
			"queries", strconv.Itoa(s.Queries),
			"total_variants", strconv.Itoa(s.TotalVariants),
			"total_puts", strconv.FormatInt(s.TotalPuts, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown CFCACHE subcommand: "+sub)
	}
}

// blastCmd handles BLAST.* — incident-response kill switch.
func (c *conn) blastCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "usage: BLAST.SET feature version")
			return
		}
		if err := c.eng.Blast.Set(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RECORD":
		if len(args) < 4 {
			writeError(c.bw, "usage: BLAST.RECORD feature version tenant user")
			return
		}
		if err := c.eng.Blast.Record(args[0], args[1], args[2], args[3]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REVERT":
		if len(args) < 3 {
			writeError(c.bw, "usage: BLAST.REVERT feature bad-version safe-version [REASON r]")
			return
		}
		reason := ""
		if len(args) >= 5 && strings.ToUpper(args[3]) == "REASON" {
			reason = args[4]
		}
		r, err := c.eng.Blast.Revert(args[0], args[1], args[2], reason)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBlastReport(c, r)
	case "REPORT":
		if len(args) < 2 {
			writeError(c.bw, "usage: BLAST.REPORT feature version")
			return
		}
		r, ok := c.eng.Blast.Report(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeBlastReport(c, r)
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: BLAST.STATUS feature")
			return
		}
		st, ok := c.eng.Blast.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"feature", st.Feature,
			"current_version", st.CurrentVersion,
			"versions", strings.Join(st.Versions, ","),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: BLAST.FORGET feature|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Blast.Forget(args[0])))
	case "STATS":
		s := c.eng.Blast.Stats()
		writeArray(c.bw, []string{
			"features", strconv.Itoa(s.Features),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_reverts", strconv.FormatInt(s.TotalReverts, 10),
		})
	default:
		writeError(c.bw, "unknown BLAST subcommand: "+sub)
	}
}

func writeBlastReport(c *conn, br llmstack.BlastReport) {
	per := make([]string, 0, len(br.PerTenant)*2)
	for k, v := range br.PerTenant {
		per = append(per, k, strconv.Itoa(v))
	}
	rev := "0"
	if br.Reverted {
		rev = "1"
	}
	writeArray(c.bw, []string{
		"feature", br.Feature,
		"version", br.Version,
		"reverted", rev,
		"revert_reason", br.RevertReason,
		"exposed_users", strconv.Itoa(br.ExposedUsers),
		"exposed_tenants", strconv.Itoa(br.ExposedTenants),
		"first_exposure_unix", strconv.FormatInt(br.FirstExposure, 10),
		"last_exposure_unix", strconv.FormatInt(br.LastExposure, 10),
		"duration_ms", strconv.FormatInt(br.DurationMS, 10),
		"per_tenant", strings.Join(per, ","),
	})
}

// causalCmd handles CAUSAL.* — vector-clock causal log.
func (c *conn) causalCmd(sub string, args []string) {
	switch sub {
	case "APPEND":
		if len(args) < 3 {
			writeError(c.bw, "usage: CAUSAL.APPEND log actor payload [AFTER e1 e2 ...]")
			return
		}
		var after []string
		if len(args) > 3 {
			if strings.ToUpper(args[3]) != "AFTER" {
				writeError(c.bw, "expected AFTER as fourth argument")
				return
			}
			after = args[4:]
		}
		r, err := c.eng.Causal.Append(args[0], args[1], args[2], after)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, r.EventID)
	case "READ":
		if len(args) < 1 {
			writeError(c.bw, "usage: CAUSAL.READ log [LIMIT n]")
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
				writeError(c.bw, "unknown CAUSAL.READ option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.Causal.Read(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"event_id", r.EventID,
				"actor", r.Actor,
				"payload", r.Payload,
				"after", strings.Join(r.After, ","),
				"wall_unix", strconv.FormatInt(r.WallTSUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "HAPPENS_BEFORE":
		if len(args) < 3 {
			writeError(c.bw, "usage: CAUSAL.HAPPENS_BEFORE log a b")
			return
		}
		r, ok := c.eng.Causal.HappensBefore(args[0], args[1], args[2])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		hb := "0"
		if r.HappensBefore {
			hb = "1"
		}
		cc := "0"
		if r.Concurrent {
			cc = "1"
		}
		writeArray(c.bw, []string{
			"a", r.A, "b", r.B,
			"happens_before", hb,
			"concurrent", cc,
			"reason", r.Reason,
		})
	case "CLOCK":
		if len(args) < 2 {
			writeError(c.bw, "usage: CAUSAL.CLOCK log actor")
			return
		}
		n, ok := c.eng.Causal.Clock(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeInt(c.bw, n)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: CAUSAL.FORGET log|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Causal.Forget(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Causal.List())
	case "STATS":
		s := c.eng.Causal.Stats()
		writeArray(c.bw, []string{
			"logs", strconv.Itoa(s.Logs),
			"total_appends", strconv.FormatInt(s.TotalAppends, 10),
			"total_reads", strconv.FormatInt(s.TotalReads, 10),
			"total_events", strconv.Itoa(s.TotalEvents),
		})
	default:
		writeError(c.bw, "unknown CAUSAL subcommand: "+sub)
	}
}
