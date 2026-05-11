package resp

import (
	"strconv"
	"strings"
	"time"
)

// hedgeCmd handles HEDGE.* — multi-provider hedged call tracker.
//
//   HEDGE.START call-id provider1 provider2 ...   -> [token, providers]
//   HEDGE.PUBLISH call-id provider result token   -> [is_winner, winner, latency_ms, winner_latency_ms]
//   HEDGE.WAIT call-id timeout-ms                 -> [got, result, winner, latency_ms]
//   HEDGE.STATUS call-id                          -> per-provider state
//   HEDGE.FORGET call-id                          -> int
//   HEDGE.STATS                                   -> per-provider win counts + latencies
func (c *conn) hedgeCmd(sub string, args []string) {
	switch sub {
	case "START":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hedge.start'")
			return
		}
		callID := args[0]
		providers := args[1:]
		r, err := c.eng.Hedge.Start(callID, providers)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		provAny := make([]any, 0, len(r.Providers))
		for _, p := range r.Providers {
			provAny = append(provAny, p)
		}
		writeValue(c.bw, []any{
			"token", r.Token,
			"providers", provAny,
		})
	case "PUBLISH":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'hedge.publish'")
			return
		}
		r, ok := c.eng.Hedge.Publish(args[0], args[1], args[2], args[3])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNHEDGE", "no hedge registered or unknown provider/token")
			return
		}
		winnerInt := "0"
		if r.IsWinner {
			winnerInt = "1"
		}
		writeArray(c.bw, []string{
			"is_winner", winnerInt,
			"winner", r.Winner,
			"latency_ms", strconv.FormatInt(r.LatencyMS, 10),
			"winner_latency_ms", strconv.FormatInt(r.WinnerLatMS, 10),
		})
	case "WAIT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hedge.wait'")
			return
		}
		ms, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || ms <= 0 {
			writeError(c.bw, "timeout-ms must be a positive integer")
			return
		}
		r := c.eng.Hedge.Wait(args[0], time.Duration(ms)*time.Millisecond)
		gotInt := "0"
		if r.Got {
			gotInt = "1"
		}
		writeArray(c.bw, []string{
			"got", gotInt,
			"result", r.Result,
			"winner", r.Winner,
			"latency_ms", strconv.FormatInt(r.LatencyMS, 10),
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'hedge.status'")
			return
		}
		s, ok := c.eng.Hedge.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		provsAny := make([]any, 0, len(s.Providers))
		for _, p := range s.Providers {
			provsAny = append(provsAny, []any{
				"provider", p.Provider,
				"state", p.State,
				"latency_ms", strconv.FormatInt(p.LatencyMS, 10),
			})
		}
		writeValue(c.bw, []any{
			"call_id", s.CallID,
			"winner", s.Winner,
			"winner_latency_ms", strconv.FormatInt(s.WinnerLatMS, 10),
			"started_at", strconv.FormatInt(s.StartedAt, 10),
			"providers", provsAny,
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'hedge.forget'")
			return
		}
		if c.eng.Hedge.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Hedge.Stats()
		provsAny := make([]any, 0, len(s.Providers))
		for _, p := range s.Providers {
			provsAny = append(provsAny, []any{
				"provider", p.Provider,
				"wins", strconv.FormatInt(p.Wins, 10),
				"total_calls", strconv.FormatInt(p.TotalCalls, 10),
				"win_rate", strconv.FormatFloat(p.WinRate, 'f', 4, 64),
				"avg_latency_ms", strconv.FormatInt(p.AvgLatencyMS, 10),
			})
		}
		writeValue(c.bw, []any{
			"providers", provsAny,
			"total_hedges", strconv.FormatInt(s.TotalHedges, 10),
			"total_saved_ms", strconv.FormatInt(s.TotalSavedMS, 10),
			"active_calls", strconv.Itoa(s.ActiveCalls),
		})
	default:
		writeError(c.bw, "unknown HEDGE subcommand: "+sub)
	}
}

// verifyCmd handles VERIFY.* — self-consistency consensus over samples.
//
//   VERIFY.SAMPLE query-id sample [TAGS t1,t2,...]
//   VERIFY.CONSENSUS query-id [STRATEGY exact|medoid|cluster]
//        -> [chosen, confidence, sample_n, buckets[]]
//   VERIFY.SAMPLES query-id   -> raw samples
//   VERIFY.FORGET query-id    -> int
//   VERIFY.STATS
func (c *conn) verifyCmd(sub string, args []string) {
	switch sub {
	case "SAMPLE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'verify.sample'")
			return
		}
		var tags []string
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TAGS" {
				writeError(c.bw, "unknown VERIFY.SAMPLE option: "+key)
				return
			}
			for _, p := range strings.Split(val, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					tags = append(tags, p)
				}
			}
			i += 2
		}
		if err := c.eng.Verify.AddSample(args[0], args[1], tags); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CONSENSUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'verify.consensus'")
			return
		}
		strategy := "exact"
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "STRATEGY" {
				writeError(c.bw, "unknown VERIFY.CONSENSUS option: "+key)
				return
			}
			strategy = val
			i += 2
		}
		r, ok := c.eng.Verify.Consensus(args[0], strategy)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNQUERY", "no samples registered for that query")
			return
		}
		buckets := make([]any, 0, len(r.Buckets))
		for _, b := range r.Buckets {
			buckets = append(buckets, []any{
				"sample", b.Sample,
				"count", strconv.Itoa(b.Count),
				"share", strconv.FormatFloat(b.Share, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"query_id", r.QueryID,
			"strategy", r.Strategy,
			"chosen", r.Chosen,
			"confidence", strconv.FormatFloat(r.Confidence, 'f', 4, 64),
			"sample_n", strconv.Itoa(r.SampleN),
			"buckets", buckets,
		})
	case "SAMPLES":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'verify.samples'")
			return
		}
		writeArray(c.bw, c.eng.Verify.Samples(args[0]))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'verify.forget'")
			return
		}
		if c.eng.Verify.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Verify.Stats()
		writeArray(c.bw, []string{
			"total_samples", strconv.FormatInt(s.TotalSamples, 10),
			"total_consensus", strconv.FormatInt(s.TotalConsensus, 10),
			"queries", strconv.Itoa(s.Queries),
		})
	default:
		writeError(c.bw, "unknown VERIFY subcommand: "+sub)
	}
}

// rewriteCmd handles REWRITE.* — query rewrite cache.
//
//   REWRITE.SET technique query rewritten [EX sec | PX ms]
//   REWRITE.GET technique query                    -> bulk or nil
//   REWRITE.SET_MULTI technique query v1 v2 v3 ... [EX sec]
//   REWRITE.LIST technique query                   -> array or nil
//   REWRITE.FORGET technique query                 -> int
//   REWRITE.PURGE [TECHNIQUE name]                 -> int dropped
//   REWRITE.SETCAP n
//   REWRITE.SETCOST usd
//   REWRITE.STATS
func (c *conn) rewriteCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.set'")
			return
		}
		ttl, err := parseRewriteTTL(args[3:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.Rewrite.Set(args[0], args[1], args[2], ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.get'")
			return
		}
		s, ok := c.eng.Rewrite.Get(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, s)
	case "SET_MULTI":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.set_multi'")
			return
		}
		// Find optional trailing [EX|PX n] (look at the LAST 2 args)
		variants := args[2:]
		var ttl time.Duration
		if len(variants) >= 2 {
			key := strings.ToUpper(variants[len(variants)-2])
			if key == "EX" || key == "PX" {
				rest := variants[len(variants)-2:]
				v, err := parseRewriteTTL(rest)
				if err != nil {
					writeError(c.bw, err.Error())
					return
				}
				ttl = v
				variants = variants[:len(variants)-2]
			}
		}
		if err := c.eng.Rewrite.SetMulti(args[0], args[1], variants, ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.list'")
			return
		}
		variants, ok := c.eng.Rewrite.List(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, variants)
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.forget'")
			return
		}
		if c.eng.Rewrite.Forget(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		technique := ""
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TECHNIQUE" {
				writeError(c.bw, "unknown REWRITE.PURGE option: "+key)
				return
			}
			technique = val
			i += 2
		}
		writeInt(c.bw, int64(c.eng.Rewrite.Purge(technique)))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil {
			writeError(c.bw, "cap must be an integer")
			return
		}
		c.eng.Rewrite.SetCap(n)
		writeSimple(c.bw, "OK")
	case "SETCOST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'rewrite.setcost'")
			return
		}
		usd, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "cost must be a float")
			return
		}
		c.eng.Rewrite.SetCostUSD(usd)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.Rewrite.Stats()
		techsAny := make([]any, 0, len(s.Techniques))
		for _, t := range s.Techniques {
			techsAny = append(techsAny, []any{
				"technique", t.Technique,
				"hits", strconv.FormatInt(t.Hits, 10),
				"misses", strconv.FormatInt(t.Misses, 10),
				"sets", strconv.FormatInt(t.Sets, 10),
				"hit_rate", strconv.FormatFloat(t.HitRate, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"entries", strconv.Itoa(s.Entries),
			"cap", strconv.Itoa(s.Cap),
			"total_gets", strconv.FormatInt(s.TotalGets, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"saved_calls", strconv.FormatInt(s.SavedCalls, 10),
			"saved_usd", strconv.FormatFloat(s.SavedUSD, 'f', 4, 64),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
			"total_evicts", strconv.FormatInt(s.TotalEvicts, 10),
			"cost_usd", strconv.FormatFloat(s.CostUSD, 'f', 4, 64),
			"techniques", techsAny,
		})
	default:
		writeError(c.bw, "unknown REWRITE subcommand: "+sub)
	}
}

func parseRewriteTTL(rest []string) (time.Duration, error) {
	if len(rest) == 0 {
		return 0, nil
	}
	if len(rest) < 2 {
		return 0, errBadTTLOption
	}
	key := strings.ToUpper(rest[0])
	val := rest[1]
	switch key {
	case "EX":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return 0, errBadTTLOption
		}
		return time.Duration(n) * time.Second, nil
	case "PX":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return 0, errBadTTLOption
		}
		return time.Duration(n) * time.Millisecond, nil
	}
	return 0, errBadTTLOption
}

type rewriteRESPErr struct{ msg string }

func (e *rewriteRESPErr) Error() string { return e.msg }

var errBadTTLOption = &rewriteRESPErr{"TTL option must be EX <sec> or PX <ms>"}
