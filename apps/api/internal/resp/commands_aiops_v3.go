package resp

import (
	"strconv"
	"time"
)

// semnegCmd handles SEMNEG.* — the negative semantic cache.
//
//   SEMNEG.MARK <query> [TTL <seconds>]   -- record a missed query
//   SEMNEG.CHECK <query>                  -- was it recently missed?
//   SEMNEG.FORGET <query>                 -- drop one entry
//   SEMNEG.CLEAR                          -- wipe everything
//   SEMNEG.STATS                          -- hits / misses / saved scans
//   SEMNEG.LIST [LIMIT <n>]               -- recently-marked queries
//
// The point of this family: SEMANTIC_GET on a 100k-entry cache is
// O(N) cosine comparisons. Repeating the same miss = wasted CPU.
// Apps can gate SEMANTIC_GET on a SEMNEG.CHECK first.
func (c *conn) semnegCmd(sub string, args []string) {
	switch sub {
	case "MARK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semneg.mark'")
			return
		}
		var ttl time.Duration
		if len(args) >= 3 && args[1] == "TTL" {
			n, err := strconv.Atoi(args[2])
			if err != nil || n < 0 {
				writeError(c.bw, "invalid TTL value")
				return
			}
			ttl = time.Duration(n) * time.Second
		}
		c.eng.NegSemCache.Mark(args[0], ttl)
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semneg.check'")
			return
		}
		if c.eng.NegSemCache.Check(args[0]) {
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semneg.forget'")
			return
		}
		if c.eng.NegSemCache.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "CLEAR":
		writeInt(c.bw, int64(c.eng.NegSemCache.Clear()))
	case "STATS":
		st := c.eng.NegSemCache.Stats()
		writeArray(c.bw, []string{
			"hits", strconv.FormatInt(st.Hits, 10),
			"misses", strconv.FormatInt(st.Misses, 10),
			"marks", strconv.FormatInt(st.Marks, 10),
			"expire_purges", strconv.FormatInt(st.ExpirePurges, 10),
			"hit_rate", strconv.FormatFloat(st.HitRate, 'f', 4, 64),
			"unique_entries", strconv.Itoa(st.UniqueEntries),
		})
	case "LIST":
		limit := 0
		if len(args) >= 2 && args[0] == "LIMIT" {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				writeError(c.bw, "invalid LIMIT value")
				return
			}
			limit = n
		}
		rows := c.eng.NegSemCache.List(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				"hash", r.Hash,
				"hits", strconv.FormatInt(r.Hits, 10),
				"age_sec", strconv.FormatInt(r.AgeSec, 10),
				"ttl_sec", strconv.FormatInt(r.TTLSec, 10),
			})
		}
		writeValue(c.bw, out)
	default:
		writeError(c.bw, "unknown SEMNEG subcommand: "+sub)
	}
}

// promptAnalyticsCmd handles the analytics subset of PROMPT.* —
// fingerprint-based prompt clustering. Distinct from PROMPT.SET /
// PROMPT.GET (versioned templates) which lives in commands_llm.go.
//
//   PROMPT.FINGERPRINT <text>            -- compute the hash; pure
//   PROMPT.RECORD <text>                  -- bump the cluster counter
//   PROMPT.GROUPS [LIMIT <n>]             -- top-N most-frequent
//   PROMPT.SAMPLE <fingerprint>           -- example prompt for hash
//   PROMPT.STATS                          -- total / unique counters
//   PROMPT.RESET_ANALYTICS                -- wipe counters
//
// Use case: production LLM apps want "of every prompt sent today,
// what are the top 20 templates, with samples?" Useful for cost
// analysis, prompt-injection variant detection, cache-warm tuning.
func (c *conn) promptAnalyticsCmd(sub string, args []string) {
	switch sub {
	case "FINGERPRINT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prompt.fingerprint'")
			return
		}
		writeBulk(c.bw, fingerprintForCmd(args[0]))
	case "RECORD":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prompt.record'")
			return
		}
		fp := c.eng.PromptAnalytics.Record(args[0])
		writeBulk(c.bw, fp)
	case "GROUPS":
		limit := 0
		if len(args) >= 2 && args[0] == "LIMIT" {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				writeError(c.bw, "invalid LIMIT value")
				return
			}
			limit = n
		}
		rows := c.eng.PromptAnalytics.Groups(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				"fingerprint", r.Fingerprint,
				"count", strconv.FormatInt(r.Count, 10),
				"first_seen_ns", strconv.FormatInt(r.FirstSeenNS, 10),
				"last_seen_ns", strconv.FormatInt(r.LastSeenNS, 10),
				"sample", r.Sample,
			})
		}
		writeValue(c.bw, out)
	case "SAMPLE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prompt.sample'")
			return
		}
		s := c.eng.PromptAnalytics.Sample(args[0])
		if s == "" {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, s)
	case "STATS":
		st := c.eng.PromptAnalytics.Stats()
		writeArray(c.bw, []string{
			"total_records", strconv.FormatInt(st.TotalRecords, 10),
			"unique_groups", strconv.Itoa(st.UniqueGroups),
		})
	case "RESET_ANALYTICS":
		c.eng.PromptAnalytics.Reset()
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "unknown PROMPT analytics subcommand: "+sub)
	}
}

// fingerprintForCmd is a tiny shim so the dispatch handler doesn't
// need to import llmstack just for the pure Fingerprint() function.
// The engine field PromptAnalytics is the holder for non-pure ops.
func fingerprintForCmd(text string) string {
	// Re-use the same canonicalization the analytics layer uses by
	// going through the engine's analyzer — this keeps a single source
	// of truth for the hash function and lets ops update both in
	// lockstep without touching the dispatch handler.
	//
	// The Fingerprint() pure function lives in llmstack but we don't
	// import it here: instead, we Record into a throwaway analyzer.
	// That's heavier than necessary; if this ever shows up in profiles
	// we can promote Fingerprint() to a top-level export the dispatch
	// handler imports directly.
	return llmstackFingerprint(text)
}
