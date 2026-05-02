package resp

// AI-stack command handlers — embedding cache, conversation management,
// versioned prompt templates. Each family is small + cohesive; the
// in-memory state lives in `internal/llmstack/`. Writes flow through
// `c.eng.RecordWrite` so AOF + replication propagate them like any
// other write-path command.

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// ── EMB.* ───────────────────────────────────────────────────────────

// embCmd dispatches the EMB.<sub> family. Subcommands:
//
//	EMB.CACHE_SET <text> <vec> [EX seconds | PX ms]
//	EMB.CACHE_GET <text>
//	EMB.CACHE_DEL <text>
//	EMB.STATS
//	EMB.PURGE
//	EMB.COST <usd-per-call>
func (c *conn) embCmd(sub string, args []string) {
	switch sub {
	case "CACHE_SET":
		if len(args) < 2 {
			writeError(c.bw, "EMB.CACHE_SET text vec [EX sec | PX ms]")
			return
		}
		text := args[0]
		vec, err := parseFloat32CSV(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		ttl := time.Duration(0)
		for i := 2; i+1 < len(args); i += 2 {
			n, e2 := strconv.Atoi(args[i+1])
			if e2 != nil || n < 0 {
				writeError(c.bw, "ERR ttl must be a non-negative integer")
				return
			}
			switch strings.ToUpper(args[i]) {
			case "EX":
				ttl = time.Duration(n) * time.Second
			case "PX":
				ttl = time.Duration(n) * time.Millisecond
			default:
				writeError(c.bw, "syntax error")
				return
			}
		}
		c.eng.EmbCache.Set(text, vec, ttl)
		c.eng.RecordWrite("EMB.CACHE_SET", args)
		writeSimple(c.bw, "OK")
	case "CACHE_GET":
		if len(args) != 1 {
			writeError(c.bw, "EMB.CACHE_GET text")
			return
		}
		vec, ok := c.eng.EmbCache.Get(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, formatFloat32CSV(vec))
	case "CACHE_DEL":
		if len(args) != 1 {
			writeError(c.bw, "EMB.CACHE_DEL text")
			return
		}
		dropped := c.eng.EmbCache.Delete(args[0])
		if dropped {
			c.eng.RecordWrite("EMB.CACHE_DEL", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		st := c.eng.EmbCache.Stats()
		writeValue(c.bw, []any{
			"entries", int64(st.Entries),
			"hits", st.Hits,
			"misses", st.Misses,
			"hit_rate", strconv.FormatFloat(st.HitRate, 'f', 4, 64),
			"cost_per_call_usd", strconv.FormatFloat(st.CostPerCall, 'f', 6, 64),
			"saved_usd", strconv.FormatFloat(st.SavedUSD, 'f', 6, 64),
		})
	case "PURGE":
		n := c.eng.EmbCache.Purge()
		c.eng.RecordWrite("EMB.PURGE", nil)
		writeInt(c.bw, int64(n))
	case "COST":
		if len(args) != 1 {
			writeError(c.bw, "EMB.COST usd-per-call")
			return
		}
		f, err := strconv.ParseFloat(args[0], 64)
		if err != nil || f < 0 {
			writeError(c.bw, "ERR cost must be a non-negative float")
			return
		}
		c.eng.EmbCache.SetCost(f)
		c.eng.RecordWrite("EMB.COST", args)
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown EMB subcommand "+sub)
	}
}

// ── CONV.* ──────────────────────────────────────────────────────────

// convCmd dispatches the CONV.<sub> family. Subcommands:
//
//	CONV.APPEND key role content
//	CONV.WINDOW key [MAXTOKENS n]
//	CONV.SUMMARIZE key summary [KEEP n]
//	CONV.RESET key
//	CONV.LEN key
//	CONV.LIST
func (c *conn) convCmd(sub string, args []string) {
	switch sub {
	case "APPEND":
		if len(args) != 3 {
			writeError(c.bw, "CONV.APPEND key role content")
			return
		}
		n, err := c.eng.Conversations.Append(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CONV.APPEND", args)
		writeInt(c.bw, int64(n))
	case "WINDOW":
		if len(args) < 1 {
			writeError(c.bw, "CONV.WINDOW key [MAXTOKENS n]")
			return
		}
		key := args[0]
		max := 0
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "MAXTOKENS" {
				max, _ = strconv.Atoi(args[i+1])
			}
		}
		turns := c.eng.Conversations.Window(key, max)
		writeConvTurns(c, turns)
	case "SUMMARIZE":
		if len(args) < 2 {
			writeError(c.bw, "CONV.SUMMARIZE key summary [KEEP n]")
			return
		}
		key, summary := args[0], args[1]
		keep := 0
		for i := 2; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "KEEP" {
				keep, _ = strconv.Atoi(args[i+1])
			}
		}
		dropped, total, err := c.eng.Conversations.Summarize(key, summary, keep)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("CONV.SUMMARIZE", args)
		writeValue(c.bw, []any{
			"dropped_turns", int64(dropped),
			"tokens_remaining", int64(total),
		})
	case "RESET":
		if len(args) != 1 {
			writeError(c.bw, "CONV.RESET key")
			return
		}
		had := c.eng.Conversations.Reset(args[0])
		if had {
			c.eng.RecordWrite("CONV.RESET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "LEN":
		if len(args) != 1 {
			writeError(c.bw, "CONV.LEN key")
			return
		}
		st := c.eng.Conversations.Stats(args[0])
		writeValue(c.bw, []any{
			"turns", int64(st.Turns),
			"tokens", int64(st.Tokens),
			"has_summary", boolToInt64(st.HasSummary),
			"summary_tokens", int64(st.SummaryToks),
		})
	case "LIST":
		writeArray(c.bw, c.eng.Conversations.Keys())
	default:
		writeError(c.bw, "Unknown CONV subcommand "+sub)
	}
}

func writeConvTurns(c *conn, turns []llmstack.Turn) {
	out := make([]any, 0, len(turns))
	for _, t := range turns {
		out = append(out, []any{
			"role", t.Role,
			"content", t.Content,
			"tokens", int64(t.Tokens),
			"created_at", t.CreatedAt.Unix(),
		})
	}
	writeValue(c.bw, out)
}

// ── PROMPT.* ────────────────────────────────────────────────────────

// promptCmd dispatches the PROMPT.<sub> family. Subcommands:
//
//	PROMPT.SET name body [VERSION v]
//	PROMPT.GET name [VERSION v]
//	PROMPT.RENDER name [VERSION v] [VARS k v [k v ...]]
//	PROMPT.LIST
//	PROMPT.DELETE name [VERSION v]
//	PROMPT.VERSIONS name
func (c *conn) promptCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "PROMPT.SET name body [VERSION v]")
			return
		}
		name, body := args[0], args[1]
		ver := 0
		for i := 2; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "VERSION" {
				ver, _ = strconv.Atoi(args[i+1])
			}
		}
		assigned, err := c.eng.Prompts.Set(name, ver, body)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("PROMPT.SET", args)
		writeInt(c.bw, int64(assigned))
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "PROMPT.GET name [VERSION v]")
			return
		}
		ver := 0
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "VERSION" {
				ver, _ = strconv.Atoi(args[i+1])
			}
		}
		pv, ok := c.eng.Prompts.Get(args[0], ver)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, []any{
			"version", int64(pv.Version),
			"body", pv.Body,
			"created_at", pv.CreatedAt.Unix(),
		})
	case "RENDER":
		if len(args) < 1 {
			writeError(c.bw, "PROMPT.RENDER name [VERSION v] [VARS k v [k v ...]]")
			return
		}
		name := args[0]
		ver := 0
		vars := map[string]string{}
		for i := 1; i < len(args); {
			tok := strings.ToUpper(args[i])
			if tok == "VERSION" && i+1 < len(args) {
				ver, _ = strconv.Atoi(args[i+1])
				i += 2
				continue
			}
			if tok == "VARS" {
				// every subsequent pair becomes a variable binding
				for j := i + 1; j+1 < len(args); j += 2 {
					vars[args[j]] = args[j+1]
				}
				break
			}
			i++
		}
		out, err := c.eng.Prompts.Render(name, ver, vars)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, out)
	case "LIST":
		entries := c.eng.Prompts.List()
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, []any{
				"name", e.Name,
				"latest_version", int64(e.LatestVersion),
				"versions", int64(e.Versions),
			})
		}
		writeValue(c.bw, out)
	case "DELETE":
		if len(args) < 1 {
			writeError(c.bw, "PROMPT.DELETE name [VERSION v]")
			return
		}
		ver := 0
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "VERSION" {
				ver, _ = strconv.Atoi(args[i+1])
			}
		}
		removed := c.eng.Prompts.Delete(args[0], ver)
		if removed > 0 {
			c.eng.RecordWrite("PROMPT.DELETE", args)
		}
		writeInt(c.bw, int64(removed))
	case "VERSIONS":
		if len(args) != 1 {
			writeError(c.bw, "PROMPT.VERSIONS name")
			return
		}
		versions := c.eng.Prompts.Versions(args[0])
		out := make([]any, 0, len(versions))
		for _, v := range versions {
			out = append(out, []any{
				"version", int64(v.Version),
				"body", v.Body,
				"created_at", v.CreatedAt.Unix(),
			})
		}
		writeValue(c.bw, out)
	default:
		writeError(c.bw, "Unknown PROMPT subcommand "+sub)
	}
}

// ── helpers ─────────────────────────────────────────────────────────

// parseFloat32CSV accepts either a comma-separated decimal list or a
// single "0,0,0,..." form. We keep the wire format as decimals (rather
// than the binary 4-byte float blob VADD accepts) because EMB.* is
// LLM-facing and most callers already serialize as JSON arrays of
// floats — keeping the format human-readable matches that.
func parseFloat32CSV(s string) ([]float32, error) {
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

// formatFloat32CSV is the reverse. Uses 'g' format so we don't emit
// trailing zeros for integral values, matching how OpenAI / Anthropic
// embedding APIs return their JSON.
func formatFloat32CSV(v []float32) string {
	var sb strings.Builder
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	return sb.String()
}

