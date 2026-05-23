package resp

import (
	"strconv"
	"strings"
	"time"
)

// nliCmd handles NLI.* — entailment cache.
//
//   NLI.SET premise hypothesis relation [SCORE n] [EX sec | PX ms]
//   NLI.GET premise hypothesis            → [relation, score, cached]
//   NLI.CHECK premise hypothesis [DEFAULT r] → same shape; default
//                                              for cache misses
//   NLI.MGET premise hypothesis1 hypothesis2 ...
//        → array of {hypothesis, relation, score, cached}
//   NLI.FORGET premise hypothesis
//   NLI.PURGE                              → int dropped
//   NLI.STATS
func (c *conn) nliCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'nli.set'")
			return
		}
		score := 0.0
		var ttl time.Duration
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "SCORE":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "SCORE must be a float")
					return
				}
				score = f
			case "EX":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "EX must be a positive integer")
					return
				}
				ttl = time.Duration(n) * time.Second
			case "PX":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "PX must be a positive integer")
					return
				}
				ttl = time.Duration(n) * time.Millisecond
			default:
				writeError(c.bw, "unknown NLI.SET option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.NLI.Set(args[0], args[1], args[2], score, ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'nli.get'")
			return
		}
		r, ok := c.eng.NLI.Get(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"relation", r.Relation,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"cached", "1",
		})
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'nli.check'")
			return
		}
		def := "neutral"
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "DEFAULT" {
				writeError(c.bw, "unknown NLI.CHECK option: "+key)
				return
			}
			def = val
			i += 2
		}
		r := c.eng.NLI.Check(args[0], args[1], def)
		cachedInt := "0"
		if r.Cached {
			cachedInt = "1"
		}
		writeArray(c.bw, []string{
			"relation", r.Relation,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"cached", cachedInt,
		})
	case "MGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'nli.mget'")
			return
		}
		rows := c.eng.NLI.MGet(args[0], args[1:])
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			cachedInt := "0"
			if r.Cached {
				cachedInt = "1"
			}
			out = append(out, []any{
				"hypothesis", r.Hypothesis,
				"relation", r.Relation,
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"cached", cachedInt,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'nli.forget'")
			return
		}
		if c.eng.NLI.Forget(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		writeInt(c.bw, int64(c.eng.NLI.Purge()))
	case "STATS":
		s := c.eng.NLI.Stats()
		writeArray(c.bw, []string{
			"entries", strconv.Itoa(s.Entries),
			"cap", strconv.Itoa(s.Cap),
			"total_gets", strconv.FormatInt(s.TotalGets, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"hits_entails", strconv.FormatInt(s.HitsEntails, 10),
			"hits_contradicts", strconv.FormatInt(s.HitsContradicts, 10),
			"hits_neutral", strconv.FormatInt(s.HitsNeutral, 10),
			"total_evicts", strconv.FormatInt(s.TotalEvicts, 10),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown NLI subcommand: "+sub)
	}
}

// cascadeCmd handles CASCADE.* — cost-tier ladder with learning.
//
//   CASCADE.CONFIG cascade-id tier1 tier2 ...
//   CASCADE.PICK cascade-id input         → [tier_idx, tier, learned]
//   CASCADE.RECORD cascade-id input tier-used 0|1
//   CASCADE.STATUS cascade-id input
//   CASCADE.FORGET cascade-id input
//   CASCADE.PURGE [CASCADE id]
//   CASCADE.ALL
//   CASCADE.STATS
func (c *conn) cascadeCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'cascade.config'")
			return
		}
		if err := c.eng.Cascade.Config(args[0], args[1:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PICK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cascade.pick'")
			return
		}
		r, ok := c.eng.Cascade.Pick(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNCASCADE", "no cascade registered for that id")
			return
		}
		learnedInt := "0"
		if r.Learned {
			learnedInt = "1"
		}
		writeArray(c.bw, []string{
			"tier_idx", strconv.Itoa(r.TierIdx),
			"tier", r.Tier,
			"learned", learnedInt,
		})
	case "RECORD":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'cascade.record'")
			return
		}
		tier, err := strconv.Atoi(args[2])
		if err != nil || tier < 0 {
			writeError(c.bw, "tier-used must be a non-negative integer")
			return
		}
		success := args[3] == "1" || strings.EqualFold(args[3], "true")
		if err := c.eng.Cascade.Record(args[0], args[1], tier, success); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATUS":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cascade.status'")
			return
		}
		r, ok := c.eng.Cascade.Status(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNCASCADE", "no cascade registered for that id")
			return
		}
		learnedInt := "0"
		if r.Learned {
			learnedInt = "1"
		}
		writeArray(c.bw, []string{
			"tier_idx", strconv.Itoa(r.TierIdx),
			"tier", r.Tier,
			"learned", learnedInt,
		})
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cascade.forget'")
			return
		}
		if c.eng.Cascade.Forget(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		cascadeID := ""
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "CASCADE" {
				writeError(c.bw, "unknown CASCADE.PURGE option: "+key)
				return
			}
			cascadeID = val
			i += 2
		}
		writeInt(c.bw, int64(c.eng.Cascade.Purge(cascadeID)))
	case "ALL":
		rows := c.eng.Cascade.All()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			tiersAny := make([]any, 0, len(r.Tiers))
			for _, t := range r.Tiers {
				tiersAny = append(tiersAny, []any{
					"tier_idx", strconv.Itoa(t.TierIdx),
					"tier", t.Tier,
					"wins", strconv.FormatInt(t.Wins, 10),
					"fails", strconv.FormatInt(t.Fails, 10),
					"win_rate", strconv.FormatFloat(t.WinRate, 'f', 4, 64),
				})
			}
			out = append(out, []any{
				"cascade_id", r.CascadeID,
				"tiers", tiersAny,
				"learned_count", strconv.Itoa(r.LearnedCount),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Cascade.Stats()
		writeArray(c.bw, []string{
			"cascades", strconv.Itoa(s.Cascades),
			"total_picks", strconv.FormatInt(s.TotalPicks, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_learned_picks", strconv.FormatInt(s.TotalLearned, 10),
		})
	default:
		writeError(c.bw, "unknown CASCADE subcommand: "+sub)
	}
}

// maskCmd handles MASK.* — fill-in-the-middle prompt templates.
//
//   MASK.REGISTER format-id template
//        Template uses {PREFIX} {SUFFIX} {MASK} placeholders.
//   MASK.BUILD format-id prefix suffix [MASK_VAL m]
//        → bulk-string assembled prompt
//   MASK.UNREGISTER format-id
//   MASK.LIST
//   MASK.STATS
func (c *conn) maskCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'mask.register'")
			return
		}
		if err := c.eng.Mask.Register(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BUILD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'mask.build'")
			return
		}
		mask := ""
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "MASK_VAL" {
				writeError(c.bw, "unknown MASK.BUILD option: "+key)
				return
			}
			mask = val
			i += 2
		}
		built, ok := c.eng.Mask.Build(args[0], args[1], args[2], mask)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNFORMAT", "no format registered for that id")
			return
		}
		writeBulk(c.bw, built)
	case "UNREGISTER":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'mask.unregister'")
			return
		}
		if c.eng.Mask.Unregister(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		rows := c.eng.Mask.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"format_id", r.FormatID,
				"template", r.Template,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Mask.Stats()
		writeArray(c.bw, []string{
			"formats", strconv.Itoa(s.Formats),
			"total_builds", strconv.FormatInt(s.TotalBuilds, 10),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
		})
	default:
		writeError(c.bw, "unknown MASK subcommand: "+sub)
	}
}
