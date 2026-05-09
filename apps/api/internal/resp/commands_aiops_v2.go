package resp

import (
	"errors"
	"strconv"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// toolCmd handles the TOOL.* family — tool/function-call memoization
// for AI agents.
//
//   TOOL.SET <tool> <args> <value> [EX <ttl-seconds>] [COST <usd>]
//   TOOL.GET <tool> <args>
//   TOOL.FORGET <tool> <args>
//   TOOL.PURGE [<tool>]              — drop all entries (or one tool's)
//   TOOL.STATS                        — hits / misses / saved $ / size
//   TOOL.LIST [<tool>] [LIMIT <n>]   — peek at cached entries
//
// Args is opaque to the cache — pass JSON, an opaque string, whatever
// the caller normalized. JSON object args are canonicalized
// (top-level key sort) before hashing so {"a":1,"b":2} matches
// {"b":2,"a":1}.
func (c *conn) toolCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'tool.set'")
			return
		}
		tool, argsKey, value := args[0], args[1], args[2]
		var ttl time.Duration
		var costMicroUSD int64
		// Parse optional EX / COST flags.
		for i := 3; i < len(args); i++ {
			switch args[i] {
			case "EX":
				if i+1 >= len(args) {
					writeError(c.bw, "EX requires a value")
					return
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "invalid EX value")
					return
				}
				ttl = time.Duration(n) * time.Second
				i++
			case "COST":
				if i+1 >= len(args) {
					writeError(c.bw, "COST requires a value")
					return
				}
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil || f < 0 {
					writeError(c.bw, "invalid COST value")
					return
				}
				costMicroUSD = int64(f * 1_000_000)
				i++
			default:
				writeError(c.bw, "unknown TOOL.SET flag: "+args[i])
				return
			}
		}
		c.eng.ToolCache.Set(tool, argsKey, value, ttl, costMicroUSD)
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'tool.get'")
			return
		}
		v, ok := c.eng.ToolCache.Get(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'tool.forget'")
			return
		}
		if c.eng.ToolCache.Forget(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		var n int
		if len(args) == 0 {
			n = c.eng.ToolCache.PurgeAll()
		} else {
			n = c.eng.ToolCache.Purge(args[0])
		}
		writeInt(c.bw, int64(n))
	case "STATS":
		st := c.eng.ToolCache.Stats()
		writeArray(c.bw, []string{
			"hits", strconv.FormatInt(st.Hits, 10),
			"misses", strconv.FormatInt(st.Misses, 10),
			"stores", strconv.FormatInt(st.Stores, 10),
			"purges", strconv.FormatInt(st.Purges, 10),
			"hit_rate", strconv.FormatFloat(st.HitRate, 'f', 4, 64),
			"saved_usd", strconv.FormatFloat(st.SavedUSD, 'f', 6, 64),
			"unique_entries", strconv.Itoa(st.UniqueEntries),
		})
	case "LIST":
		filter := ""
		limit := 0
		i := 0
		if len(args) > 0 && args[0] != "LIMIT" {
			filter = args[0]
			i = 1
		}
		if i+1 < len(args) && args[i] == "LIMIT" {
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				writeError(c.bw, "invalid LIMIT value")
				return
			}
			limit = n
		}
		rows := c.eng.ToolCache.List(filter, limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				"tool", r.Tool,
				"key_hash", r.KeyHash,
				"age_ms", strconv.FormatInt(r.AgeMs, 10),
				"ttl_ms", strconv.FormatInt(r.TTLMs, 10),
				"cost_micro_usd", strconv.FormatInt(r.CostMicroUSD, 10),
			})
		}
		writeValue(c.bw, out)
	default:
		writeError(c.bw, "unknown TOOL subcommand: "+sub)
	}
}

// guardCmd handles the GUARD.* family — LLM spend guardrails.
// Renamed from COST.* (which the Phase 11 budget system already
// owns under different verbs). GUARD is a hard $ cap — apps call
// GUARD.CHECK before each chargeable LLM call, GUARD.RECORD on
// success, or GUARD.CHECKRECORD for atomic semantics.
//
//   GUARD.SETCAP <scope> <usd> [WINDOW <seconds>]
//                                  — configure / update a cap. WINDOW=0
//                                    (or omitted) means lifetime.
//   GUARD.CHECK <scope> <usd>     — would this charge fit? (no record)
//   GUARD.RECORD <scope> <usd>    — bump the spend counter (no check)
//   GUARD.CHECKRECORD <scope> <usd>— atomic check-and-record via CAS
//                                    (race-safe under concurrency)
//   GUARD.SPENT <scope>           — current window spend in $
//   GUARD.LIMIT <scope>           — configured cap in $
//   GUARD.RESET <scope>           — clear spend counter (cap unchanged)
//   GUARD.LIST                    — every scope's status (for dashboard)
//   GUARD.STATS                   — process-wide check / rejection counts
func (c *conn) guardCmd(sub string, args []string) {
	switch sub {
	case "SETCAP":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'guard.setcap'")
			return
		}
		scope := args[0]
		limit, err := strconv.ParseFloat(args[1], 64)
		if err != nil || limit < 0 {
			writeError(c.bw, "invalid limit USD")
			return
		}
		var windowSec int64
		if len(args) >= 4 && args[2] == "WINDOW" {
			n, err := strconv.ParseInt(args[3], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "invalid WINDOW value")
				return
			}
			windowSec = n
		}
		c.eng.CostGuard.SetCap(scope, limit, windowSec)
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'guard.check'")
			return
		}
		cost, err := strconv.ParseFloat(args[1], 64)
		if err != nil || cost < 0 {
			writeError(c.bw, "invalid cost USD")
			return
		}
		err = c.eng.CostGuard.Check(args[0], cost)
		c.writeGuardErr(err, 1)
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'guard.record'")
			return
		}
		cost, err := strconv.ParseFloat(args[1], 64)
		if err != nil || cost < 0 {
			writeError(c.bw, "invalid cost USD")
			return
		}
		total, err := c.eng.CostGuard.Record(args[0], cost)
		if err != nil {
			c.writeGuardErr(err, 0)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(total, 'f', 6, 64))
	case "CHECKRECORD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'guard.checkrecord'")
			return
		}
		cost, err := strconv.ParseFloat(args[1], 64)
		if err != nil || cost < 0 {
			writeError(c.bw, "invalid cost USD")
			return
		}
		total, err := c.eng.CostGuard.CheckAndRecord(args[0], cost)
		if err != nil {
			c.writeGuardErr(err, 0)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(total, 'f', 6, 64))
	case "SPENT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'guard.spent'")
			return
		}
		v, err := c.eng.CostGuard.Spent(args[0])
		if err != nil {
			c.writeGuardErr(err, 0)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', 6, 64))
	case "LIMIT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'guard.limit'")
			return
		}
		v, err := c.eng.CostGuard.Limit(args[0])
		if err != nil {
			c.writeGuardErr(err, 0)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', 6, 64))
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'guard.reset'")
			return
		}
		if err := c.eng.CostGuard.Reset(args[0]); err != nil {
			c.writeGuardErr(err, 0)
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		rows := c.eng.CostGuard.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				"scope", r.Scope,
				"limit_usd", strconv.FormatFloat(r.LimitUSD, 'f', 6, 64),
				"spent_usd", strconv.FormatFloat(r.SpentUSD, 'f', 6, 64),
				"window_sec", strconv.FormatInt(r.WindowSec, 10),
				"window_age_sec", strconv.FormatInt(r.WindowAgeSec, 10),
				"util_percent", strconv.FormatFloat(r.UtilPercent, 'f', 2, 64),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		m := c.eng.CostGuard.Meta()
		writeArray(c.bw, []string{
			"total_checks", strconv.FormatInt(m.TotalChecks, 10),
			"total_rejections", strconv.FormatInt(m.TotalRejections, 10),
		})
	default:
		writeError(c.bw, "unknown COST subcommand: "+sub)
	}
}

// writeGuardErr maps the typed CostGuard errors to RESP responses.
//
// For COST.CHECK specifically, callers want a clean integer "did this
// pass" reply rather than an error reply they have to special-case
// — `okIfPass` controls that. Pass okIfPass=1 for CHECK, 0 elsewhere.
func (c *conn) writeGuardErr(err error, okIfPass int) {
	if err == nil {
		if okIfPass == 1 {
			writeInt(c.bw, 1)
			return
		}
		writeSimple(c.bw, "OK")
		return
	}
	if errors.Is(err, llmstack.ErrCapExceeded) {
		writeTypedError(c.bw, "CAPEXCEEDED", err.Error())
		return
	}
	if errors.Is(err, llmstack.ErrUnknownScope) {
		writeTypedError(c.bw, "UNKNOWNSCOPE", err.Error())
		return
	}
	writeError(c.bw, err.Error())
}
