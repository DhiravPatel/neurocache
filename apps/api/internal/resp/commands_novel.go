package resp

import (
	"strconv"
	"strings"
	"time"
)

// splitDottedSubcommand turns commands of the form NAMESPACE.SUBCMD
// into the [SUBCMD, args…] shape the handler expects. We chose the
// dotted style for module-style commands (CACHE.*, KEY.*, AI.*) so
// the dispatcher's flat switch can route each variant separately
// while the handler stays a single function.
func splitDottedSubcommand(full string, args []string) []string {
	idx := strings.Index(full, ".")
	if idx < 0 {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, full[idx+1:])
	out = append(out, args...)
	return out
}

// IDEMPOTENT key ttl-ms <command> arg [arg ...]
//
// Run <command> at most once per (key, ttl-ms) window. Subsequent
// invocations within the window return the cached result without
// re-running the work — exactly the pattern HTTP API integrations
// re-implement in client code with SETNX + careful retries.
func (c *conn) idempotentCmd(args []string) {
	if !c.wantArgs("IDEMPOTENT", args, 3) {
		return
	}
	key := args[0]
	ttlMs, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || ttlMs <= 0 {
		writeError(c.bw, "ttl-ms must be a positive integer")
		return
	}
	ttl := time.Duration(ttlMs) * time.Millisecond
	cmd := strings.ToUpper(args[2])
	cargs := args[3:]
	cached, hit, err := c.eng.Idempotent.Acquire(key, ttl)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	if hit {
		// Replay the cached reply verbatim.
		writeValue(c.bw, cached)
		return
	}
	// We're the leader — execute the inner command. We use the same
	// scriptDispatch shim Lua scripts go through, which gives us a
	// value-returning subset of the dispatcher.
	v, err := scriptDispatch(c, cmd, cargs)
	if err != nil {
		c.eng.Idempotent.Discard(key)
		writeError(c.bw, err.Error())
		return
	}
	c.eng.Idempotent.Complete(key, v, ttl)
	writeValue(c.bw, v)
}

// LOCK ACQUIRE name owner ttl-ms     -> fencing token (integer)
// LOCK RELEASE name owner            -> 1 / 0
// LOCK EXTEND  name owner ttl-ms     -> 1 / 0
// LOCK CHECK   name                  -> [owner, token, remaining-ms] or nil
func (c *conn) lockCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'lock'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "ACQUIRE":
		if len(args) < 4 {
			writeError(c.bw, "LOCK ACQUIRE name owner ttl-ms")
			return
		}
		ttlMs, _ := strconv.ParseInt(args[3], 10, 64)
		tok, ok := c.eng.Locks.Acquire(args[1], args[2], time.Duration(ttlMs)*time.Millisecond)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeInt(c.bw, int64(tok))
	case "RELEASE":
		if len(args) < 3 {
			writeError(c.bw, "LOCK RELEASE name owner")
			return
		}
		if c.eng.Locks.Release(args[1], args[2]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "EXTEND":
		if len(args) < 4 {
			writeError(c.bw, "LOCK EXTEND name owner ttl-ms")
			return
		}
		ttlMs, _ := strconv.ParseInt(args[3], 10, 64)
		if c.eng.Locks.Extend(args[1], args[2], time.Duration(ttlMs)*time.Millisecond) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "LOCK CHECK name")
			return
		}
		info, ok := c.eng.Locks.Check(args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, []any{info.Owner, int64(info.Token), info.RemMs})
	default:
		writeError(c.bw, "Unknown LOCK subcommand "+args[0])
	}
}

// RATELIMIT key window-ms max [COST n]
//
// Returns: [allowed (1/0), remaining, retry-after-ms, reset-ms]
// — the same tuple GitHub's RateLimit RFC standardised. Allowed = 1
// when the call passes; the other three are decision-support data so
// callers can implement Retry-After headers, exponential back-off, etc.
func (c *conn) ratelimitCmd(args []string) {
	if !c.wantArgs("RATELIMIT", args, 3) {
		return
	}
	windowMs, _ := strconv.ParseInt(args[1], 10, 64)
	max, _ := strconv.ParseInt(args[2], 10, 64)
	cost := int64(1)
	if len(args) >= 5 && strings.EqualFold(args[3], "COST") {
		cost, _ = strconv.ParseInt(args[4], 10, 64)
	}
	allowed, remaining, retry, reset := c.eng.RateLimit.Allow(args[0], time.Duration(windowMs)*time.Millisecond, max, cost)
	allowedFlag := int64(0)
	if allowed {
		allowedFlag = 1
	}
	writeValue(c.bw, []any{allowedFlag, remaining, retry, reset})
}

// DEDUP bucket id window-ms
//
// Returns 1 the first time `(bucket, id)` is seen within the window,
// 0 on every subsequent occurrence. Backed by a rotating two-bloom
// scheme so memory is bounded even for unbounded id streams.
func (c *conn) dedupCmd(args []string) {
	if !c.wantArgs("DEDUP", args, 3) {
		return
	}
	windowMs, _ := strconv.ParseInt(args[2], 10, 64)
	if c.eng.Dedup.SeenOrMark(args[0], args[1], time.Duration(windowMs)*time.Millisecond) {
		writeInt(c.bw, 0)
		return
	}
	writeInt(c.bw, 1)
}

// CACHE.WEIGH key cost          — annotate
// CACHE.UNWEIGH key             — drop annotation
// CACHE.STATS                   — totals + savings
// CACHE.WEIGHTS [count]         — top-N weighted keys
// CACHE.HIT key                 — credit a hit (typically auto-fired by SEMANTIC_GET / CACHE_LLM_GET)
func (c *conn) cacheCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'cache'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "WEIGH":
		if len(args) < 3 {
			writeError(c.bw, "CACHE.WEIGH key cost")
			return
		}
		cost, _ := strconv.ParseFloat(args[2], 64)
		c.eng.CostTable.Weigh(args[1], cost)
		writeSimple(c.bw, "OK")
	case "UNWEIGH":
		if len(args) < 2 {
			writeError(c.bw, "CACHE.UNWEIGH key")
			return
		}
		if c.eng.CostTable.Unweigh(args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "HIT":
		if len(args) < 2 {
			writeError(c.bw, "CACHE.HIT key")
			return
		}
		c.eng.CostTable.RecordHit(args[1])
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.CostTable.Stats()
		writeValue(c.bw, []any{
			"tracked_keys", s.TrackedKeys,
			"total_cost", strconv.FormatFloat(s.TotalCost, 'f', -1, 64),
			"hits_served", s.HitsServed,
			"total_saved", strconv.FormatFloat(s.TotalSaved, 'f', -1, 64),
		})
	case "WEIGHTS":
		count := 20
		if len(args) >= 2 {
			count, _ = strconv.Atoi(args[1])
		}
		rows := c.eng.CostTable.Snapshot()
		if count > 0 && count < len(rows) {
			rows = rows[:count]
		}
		out := []any{}
		for _, r := range rows {
			out = append(out, []any{
				r.Key,
				strconv.FormatFloat(r.Cost, 'f', -1, 64),
				r.Hits,
				strconv.FormatFloat(r.Score, 'f', -1, 64),
			})
		}
		writeValue(c.bw, out)
	default:
		writeError(c.bw, "Unknown CACHE subcommand "+args[0])
	}
}

// KEY.TRACK key                 — opt this key into version history
// KEY.UNTRACK key               — drop history + stop tracking
// KEY.HISTORY key [count]       — list every retained version
// KEY.AT key timestamp          — value as-of unix-second timestamp
func (c *conn) keyHistoryCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'key'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "TRACK":
		if len(args) < 2 {
			writeError(c.bw, "KEY.TRACK key")
			return
		}
		c.eng.History.Track(args[1])
		// snapshot the current value immediately so KEY.AT works for
		// any timestamp ≥ now.
		if v, ok, _ := c.eng.KV.GetTyped(args[1]); ok {
			c.eng.History.Snapshot(args[1], v)
		}
		writeSimple(c.bw, "OK")
	case "UNTRACK":
		if len(args) < 2 {
			writeError(c.bw, "KEY.UNTRACK key")
			return
		}
		c.eng.History.Untrack(args[1])
		writeSimple(c.bw, "OK")
	case "HISTORY":
		if len(args) < 2 {
			writeError(c.bw, "KEY.HISTORY key [count]")
			return
		}
		max := 0
		if len(args) >= 3 {
			max, _ = strconv.Atoi(args[2])
		}
		versions := c.eng.History.History(args[1], max)
		out := []any{}
		for _, v := range versions {
			out = append(out, []any{v.At.Unix(), v.Value})
		}
		writeValue(c.bw, out)
	case "AT":
		if len(args) < 3 {
			writeError(c.bw, "KEY.AT key unix-seconds")
			return
		}
		ts, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "timestamp must be an integer")
			return
		}
		v, ok := c.eng.History.At(args[1], time.Unix(ts, 0))
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	default:
		writeError(c.bw, "Unknown KEY subcommand "+args[0])
	}
}

// AI.LIKE      user item [weight]   — record an interaction
// AI.RECOMMEND user [k]             — top-K items for this user
// AI.SIMILAR   user [k]             — top-K similar users
// AI.STATS                          — totals
// AI.FORGET    user                 — drop everything for a user
func (c *conn) aiCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'ai'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "LIKE":
		if len(args) < 3 {
			writeError(c.bw, "AI.LIKE user item [weight]")
			return
		}
		weight := 1.0
		if len(args) >= 4 {
			weight, _ = strconv.ParseFloat(args[3], 64)
		}
		c.eng.Recommender.Like(args[1], args[2], weight)
		writeSimple(c.bw, "OK")
	case "RECOMMEND":
		if len(args) < 2 {
			writeError(c.bw, "AI.RECOMMEND user [k]")
			return
		}
		k := 10
		if len(args) >= 3 {
			k, _ = strconv.Atoi(args[2])
		}
		recs := c.eng.Recommender.Recommend(args[1], k)
		out := []any{}
		for _, r := range recs {
			out = append(out, []any{r.Item, strconv.FormatFloat(r.Score, 'f', -1, 64)})
		}
		writeValue(c.bw, out)
	case "SIMILAR":
		if len(args) < 2 {
			writeError(c.bw, "AI.SIMILAR user [k]")
			return
		}
		k := 10
		if len(args) >= 3 {
			k, _ = strconv.Atoi(args[2])
		}
		sims := c.eng.Recommender.Similar(args[1], k)
		out := []any{}
		for _, s := range sims {
			out = append(out, []any{s.User, strconv.FormatFloat(s.Similarity, 'f', -1, 64)})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Recommender.Stats()
		writeValue(c.bw, []any{
			"users", int64(s.Users),
			"items", int64(s.Items),
			"interactions", int64(s.Interactions),
		})
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "AI.FORGET user")
			return
		}
		c.eng.Recommender.Forget(args[1])
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown AI subcommand "+args[0])
	}
}
