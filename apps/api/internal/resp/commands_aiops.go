package resp

// AI-ops command handlers — agent tool cache, stream cache, cost
// budgets, shadow cache, personas, moderation, lineage, SLO, A/B
// experiments, knowledge graph, scheduler, event log, policies,
// inference proxy, and MCP. State lives in `internal/aiops/`. Writes
// flow through `c.eng.RecordWrite` so AOF + replication propagate
// them like any other write-path command.

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// ── AGENT.* ─────────────────────────────────────────────────────────

// agentCmd dispatches the AGENT.<sub> family. Subcommands:
//
//	AGENT.CALL tool argsHash
//	AGENT.STORE tool argsHash result
//	AGENT.PROFILE tool always|day|never
//	AGENT.FORGET tool argsHash
//	AGENT.STATS
//	AGENT.PURGE
func (c *conn) agentCmd(sub string, args []string) {
	switch sub {
	case "CALL":
		if len(args) != 2 {
			writeError(c.bw, "AGENT.CALL tool argsHash")
			return
		}
		v, ok := c.eng.AgentCache.Get(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "STORE":
		if len(args) != 3 {
			writeError(c.bw, "AGENT.STORE tool argsHash result")
			return
		}
		c.eng.AgentCache.Set(args[0], args[1], args[2])
		c.eng.RecordWrite("AGENT.STORE", args)
		writeSimple(c.bw, "OK")
	case "PROFILE":
		if len(args) != 2 {
			writeError(c.bw, "AGENT.PROFILE tool always|day|never")
			return
		}
		var d aiops.Determinism
		switch strings.ToLower(args[1]) {
		case "always":
			d = aiops.DeterminismAlways
		case "day":
			d = aiops.DeterminismDay
		case "never":
			d = aiops.DeterminismNever
		default:
			writeError(c.bw, "ERR profile must be always|day|never")
			return
		}
		c.eng.AgentCache.SetProfile(args[0], d)
		c.eng.RecordWrite("AGENT.PROFILE", args)
		writeSimple(c.bw, "OK")
	case "FORGET":
		if len(args) != 2 {
			writeError(c.bw, "AGENT.FORGET tool argsHash")
			return
		}
		ok := c.eng.AgentCache.Forget(args[0], args[1])
		if ok {
			c.eng.RecordWrite("AGENT.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		st := c.eng.AgentCache.Stats()
		writeValue(c.bw, []any{
			"entries", int64(st.Entries),
			"profiles", int64(st.Profiles),
			"hits", st.Hits,
			"misses", st.Misses,
			"hit_rate", strconv.FormatFloat(st.HitRate, 'f', 4, 64),
		})
	case "PURGE":
		n := c.eng.AgentCache.Purge()
		c.eng.RecordWrite("AGENT.PURGE", nil)
		writeInt(c.bw, int64(n))
	default:
		writeError(c.bw, "Unknown AGENT subcommand "+sub)
	}
}

// ── STREAM.* ────────────────────────────────────────────────────────

// streamCmd dispatches the STREAM.<sub> family. Subcommands:
//
//	STREAM.SET prompt-hash json-tokens [EX sec | PX ms]
//	STREAM.GET prompt-hash
//	STREAM.REPLAY prompt-hash
//	STREAM.FORGET prompt-hash
//	STREAM.PURGE
//	STREAM.STATS
func (c *conn) streamCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "STREAM.SET prompt-hash json-tokens [EX sec | PX ms]")
			return
		}
		var tokens []aiops.StreamToken
		if err := json.Unmarshal([]byte(args[1]), &tokens); err != nil {
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
		c.eng.StreamCache.Set(args[0], tokens, ttl)
		c.eng.RecordWrite("STREAM.SET", args)
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) != 1 {
			writeError(c.bw, "STREAM.GET prompt-hash")
			return
		}
		v, ok := c.eng.StreamCache.Get(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "REPLAY":
		if len(args) != 1 {
			writeError(c.bw, "STREAM.REPLAY prompt-hash")
			return
		}
		toks, ok := c.eng.StreamCache.Replay(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		out := make([]any, 0, len(toks))
		for _, t := range toks {
			out = append(out, []any{"text", t.Text, "delay_ms", t.DelayMs})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "STREAM.FORGET prompt-hash")
			return
		}
		ok := c.eng.StreamCache.Forget(args[0])
		if ok {
			c.eng.RecordWrite("STREAM.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "PURGE":
		n := c.eng.StreamCache.Purge()
		c.eng.RecordWrite("STREAM.PURGE", nil)
		writeInt(c.bw, int64(n))
	case "STATS":
		st := c.eng.StreamCache.Stats()
		writeValue(c.bw, []any{
			"streams", int64(st.Streams),
			"hits", st.Hits,
			"misses", st.Misses,
		})
	default:
		writeError(c.bw, "Unknown STREAM subcommand "+sub)
	}
}

// ── COST.* ──────────────────────────────────────────────────────────

// costCmd dispatches the COST.<sub> family. Subcommands:
//
//	COST.BUDGET tenant max-usd window-ms
//	COST.CHARGE tenant usd
//	COST.USAGE tenant
//	COST.RESET tenant
//	COST.LIST
func (c *conn) costCmd(sub string, args []string) {
	switch sub {
	case "BUDGET":
		if len(args) != 3 {
			writeError(c.bw, "COST.BUDGET tenant max-usd window-ms")
			return
		}
		max, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "ERR max-usd must be a float")
			return
		}
		window, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR window-ms must be an integer")
			return
		}
		if err := c.eng.CostBudgets.SetBudget(args[0], max, window); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("COST.BUDGET", args)
		writeSimple(c.bw, "OK")
	case "CHARGE":
		if len(args) != 2 {
			writeError(c.bw, "COST.CHARGE tenant usd")
			return
		}
		usd, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "ERR usd must be a float")
			return
		}
		allowed, remaining, err := c.eng.CostBudgets.Charge(args[0], usd)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("COST.CHARGE", args)
		writeValue(c.bw, []any{
			"allowed", allowed,
			"remaining", strconv.FormatFloat(remaining, 'f', 4, 64),
		})
	case "USAGE":
		if len(args) != 1 {
			writeError(c.bw, "COST.USAGE tenant")
			return
		}
		used, remaining, max, window := c.eng.CostBudgets.Usage(args[0])
		writeValue(c.bw, []any{
			"used", strconv.FormatFloat(used, 'f', 4, 64),
			"remaining", strconv.FormatFloat(remaining, 'f', 4, 64),
			"max", strconv.FormatFloat(max, 'f', 4, 64),
			"window_ms", window,
		})
	case "RESET":
		if len(args) != 1 {
			writeError(c.bw, "COST.RESET tenant")
			return
		}
		ok := c.eng.CostBudgets.Reset(args[0])
		if ok {
			c.eng.RecordWrite("COST.RESET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "LIST":
		writeArray(c.bw, c.eng.CostBudgets.List())
	default:
		writeError(c.bw, "Unknown COST subcommand "+sub)
	}
}

// ── SHADOW.* ────────────────────────────────────────────────────────

// shadowCmd dispatches the SHADOW.<sub> family. Subcommands:
//
//	SHADOW.PUT key value [STALE-AFTER ms]
//	SHADOW.GET key
//	SHADOW.FORGET key
//	SHADOW.STATS
func (c *conn) shadowCmd(sub string, args []string) {
	switch sub {
	case "PUT":
		if len(args) < 2 {
			writeError(c.bw, "SHADOW.PUT key value [STALE-AFTER ms]")
			return
		}
		stale := 5 * time.Minute
		for i := 2; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "STALE-AFTER" {
				ms, err := strconv.Atoi(args[i+1])
				if err != nil || ms < 0 {
					writeError(c.bw, "ERR stale-after must be a non-negative integer")
					return
				}
				stale = time.Duration(ms) * time.Millisecond
			}
		}
		c.eng.Shadow.Put(args[0], args[1], stale)
		c.eng.RecordWrite("SHADOW.PUT", args)
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) != 1 {
			writeError(c.bw, "SHADOW.GET key")
			return
		}
		v, fresh, had := c.eng.Shadow.Get(args[0])
		if !had {
			writeNil(c.bw)
			return
		}
		writeValue(c.bw, []any{
			"value", v,
			"fresh", fresh,
		})
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "SHADOW.FORGET key")
			return
		}
		ok := c.eng.Shadow.Forget(args[0])
		if ok {
			c.eng.RecordWrite("SHADOW.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		st := c.eng.Shadow.Stats()
		writeValue(c.bw, []any{
			"entries", int64(st.Entries),
			"hits", st.Hits,
			"misses", st.Misses,
			"stale_serves", st.Stale,
			"background_refreshes", st.Refreshes,
		})
	default:
		writeError(c.bw, "Unknown SHADOW subcommand "+sub)
	}
}

// ── PERSONA.* ───────────────────────────────────────────────────────

// personaCmd dispatches the PERSONA.<sub> family. Subcommands:
//
//	PERSONA.SET user persona
//	PERSONA.GET user
//	PERSONA.LIST user
//	PERSONA.FORGET user
func (c *conn) personaCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) != 2 {
			writeError(c.bw, "PERSONA.SET user persona")
			return
		}
		c.eng.Personas.SetActive(args[0], args[1])
		c.eng.RecordWrite("PERSONA.SET", args)
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) != 1 {
			writeError(c.bw, "PERSONA.GET user")
			return
		}
		writeBulk(c.bw, c.eng.Personas.Active(args[0]))
	case "LIST":
		if len(args) != 1 {
			writeError(c.bw, "PERSONA.LIST user")
			return
		}
		writeArray(c.bw, c.eng.Personas.List(args[0]))
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "PERSONA.FORGET user")
			return
		}
		ok := c.eng.Personas.Forget(args[0])
		if ok {
			c.eng.RecordWrite("PERSONA.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	default:
		writeError(c.bw, "Unknown PERSONA subcommand "+sub)
	}
}

// ── SAFE.* ──────────────────────────────────────────────────────────

// safeCmd dispatches the SAFE.<sub> family. Subcommands:
//
//	SAFE.SET text safe(0|1) score [CATEGORIES cat1 cat2 ...] [EX sec]
//	SAFE.CHECK text
//	SAFE.INJECT text
//	SAFE.FORGET text
//	SAFE.PURGE
//	SAFE.STATS
func (c *conn) safeCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "SAFE.SET text safe(0|1) score [CATEGORIES ...] [EX sec]")
			return
		}
		text := args[0]
		safeFlag, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "ERR safe must be 0 or 1")
			return
		}
		score, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "ERR score must be a float")
			return
		}
		var cats []string
		ttl := time.Duration(0)
		i := 3
		for i < len(args) {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "CATEGORIES":
				j := i + 1
				for j < len(args) && strings.ToUpper(args[j]) != "EX" {
					cats = append(cats, args[j])
					j++
				}
				i = j
			case "EX":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR EX requires a value")
					return
				}
				n, e2 := strconv.Atoi(args[i+1])
				if e2 != nil || n < 0 {
					writeError(c.bw, "ERR ttl must be a non-negative integer")
					return
				}
				ttl = time.Duration(n) * time.Second
				i += 2
			default:
				i++
			}
		}
		c.eng.Moderation.Set(text, aiops.ModerationResult{
			Safe:       safeFlag != 0,
			Score:      score,
			Categories: cats,
		}, ttl)
		c.eng.RecordWrite("SAFE.SET", args)
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) != 1 {
			writeError(c.bw, "SAFE.CHECK text")
			return
		}
		r, ok := c.eng.Moderation.Check(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		out := []any{
			"safe", r.Safe,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"categories", r.Categories,
			"stored_at", r.StoredAt.Unix(),
		}
		writeValue(c.bw, out)
	case "INJECT":
		if len(args) != 1 {
			writeError(c.bw, "SAFE.INJECT text")
			return
		}
		score := aiops.InjectionScore(args[0])
		matches := aiops.MatchedPatterns(args[0])
		writeValue(c.bw, []any{
			"score", strconv.FormatFloat(score, 'f', 4, 64),
			"matched", matches,
		})
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "SAFE.FORGET text")
			return
		}
		ok := c.eng.Moderation.Forget(args[0])
		if ok {
			c.eng.RecordWrite("SAFE.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "PURGE":
		n := c.eng.Moderation.Purge()
		c.eng.RecordWrite("SAFE.PURGE", nil)
		writeInt(c.bw, int64(n))
	case "STATS":
		st := c.eng.Moderation.Stats()
		writeValue(c.bw, []any{
			"entries", int64(st.Entries),
			"hits", st.Hits,
			"misses", st.Misses,
		})
	default:
		writeError(c.bw, "Unknown SAFE subcommand "+sub)
	}
}

// ── LINEAGE.* ───────────────────────────────────────────────────────

// lineageCmd dispatches the LINEAGE.<sub> family. Subcommands:
//
//	LINEAGE.RECORD output-id source-id [SNIPPET s] [CONFIDENCE f]
//	LINEAGE.LIST output-id
//	LINEAGE.SOURCES output-id
//	LINEAGE.CONSUMERS source-id
//	LINEAGE.FORGET output-id
//	LINEAGE.STATS
func (c *conn) lineageCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "LINEAGE.RECORD output-id source-id [SNIPPET s] [CONFIDENCE f]")
			return
		}
		snippet := ""
		conf := 0.0
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "SNIPPET":
				snippet = args[i+1]
			case "CONFIDENCE":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "ERR confidence must be a float")
					return
				}
				conf = f
			}
		}
		c.eng.Lineage.Record(args[0], args[1], snippet, conf)
		c.eng.RecordWrite("LINEAGE.RECORD", args)
		writeSimple(c.bw, "OK")
	case "LIST":
		if len(args) != 1 {
			writeError(c.bw, "LINEAGE.LIST output-id")
			return
		}
		cs := c.eng.Lineage.List(args[0])
		out := make([]any, 0, len(cs))
		for _, ct := range cs {
			out = append(out, []any{
				"output_id", ct.OutputID,
				"source_id", ct.SourceID,
				"confidence", strconv.FormatFloat(ct.Confidence, 'f', 4, 64),
				"snippet", ct.Snippet,
				"recorded_at", ct.RecordedAt.Unix(),
			})
		}
		writeValue(c.bw, out)
	case "SOURCES":
		if len(args) != 1 {
			writeError(c.bw, "LINEAGE.SOURCES output-id")
			return
		}
		writeArray(c.bw, c.eng.Lineage.Sources(args[0]))
	case "CONSUMERS":
		if len(args) != 1 {
			writeError(c.bw, "LINEAGE.CONSUMERS source-id")
			return
		}
		writeArray(c.bw, c.eng.Lineage.Consumers(args[0]))
	case "FORGET":
		if len(args) != 1 {
			writeError(c.bw, "LINEAGE.FORGET output-id")
			return
		}
		n := c.eng.Lineage.Forget(args[0])
		if n > 0 {
			c.eng.RecordWrite("LINEAGE.FORGET", args)
		}
		writeInt(c.bw, int64(n))
	case "STATS":
		st := c.eng.Lineage.Stats()
		writeValue(c.bw, []any{
			"outputs", int64(st.Outputs),
			"unique_sources", int64(st.Sources),
			"total_citations", int64(st.Citations),
		})
	default:
		writeError(c.bw, "Unknown LINEAGE subcommand "+sub)
	}
}

// ── SLO.* ───────────────────────────────────────────────────────────

// sloCmd dispatches the SLO.<sub> family. Subcommands:
//
//	SLO.SET cmd percentile max-ms
//	SLO.SNAPSHOT
//	SLO.RESET [cmd]
func (c *conn) sloCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) != 3 {
			writeError(c.bw, "SLO.SET cmd percentile max-ms")
			return
		}
		max, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "ERR max-ms must be a float")
			return
		}
		c.eng.SLOTracker.SetTarget(args[0], args[1], max)
		c.eng.RecordWrite("SLO.SET", args)
		writeSimple(c.bw, "OK")
	case "SNAPSHOT":
		snap := c.eng.SLOTracker.Snapshot()
		out := make([]any, 0, len(snap))
		for _, s := range snap {
			tgts := make([]any, 0, len(s.Targets)*2)
			for k, v := range s.Targets {
				tgts = append(tgts, k, strconv.FormatFloat(v, 'f', 4, 64))
			}
			obs := make([]any, 0, len(s.Observed)*2)
			for k, v := range s.Observed {
				obs = append(obs, k, strconv.FormatFloat(v, 'f', 4, 64))
			}
			out = append(out, []any{
				"command", s.Command,
				"targets_ms", tgts,
				"observed_ms", obs,
				"breaches", s.Breaches,
				"last_breach", s.LastBreach.Unix(),
			})
		}
		writeValue(c.bw, out)
	case "RESET":
		cmd := ""
		if len(args) > 0 {
			cmd = args[0]
		}
		n := c.eng.SLOTracker.Reset(cmd)
		c.eng.RecordWrite("SLO.RESET", args)
		writeInt(c.bw, int64(n))
	default:
		writeError(c.bw, "Unknown SLO subcommand "+sub)
	}
}

// ── AB.* ────────────────────────────────────────────────────────────

// abCmd dispatches the AB.<sub> family. Subcommands:
//
//	AB.DEFINE name [WEIGHTS f1 f2 ...] variants...
//	AB.ASSIGN name user
//	AB.EXPOSE name variant
//	AB.RECORD name variant value
//	AB.STATS name
//	AB.LIST
//	AB.RESET name
//	AB.DELETE name
func (c *conn) abCmd(sub string, args []string) {
	switch sub {
	case "DEFINE":
		if len(args) < 2 {
			writeError(c.bw, "AB.DEFINE name [WEIGHTS f1 f2 ...] variants...")
			return
		}
		name := args[0]
		var variants []string
		var weights []float64
		i := 1
		if i < len(args) && strings.ToUpper(args[i]) == "WEIGHTS" {
			j := i + 1
			for j < len(args) {
				f, err := strconv.ParseFloat(args[j], 64)
				if err != nil {
					break
				}
				weights = append(weights, f)
				j++
			}
			i = j
		}
		variants = args[i:]
		if len(variants) == 0 {
			writeError(c.bw, "ERR at least one variant required")
			return
		}
		if len(weights) > 0 && len(weights) != len(variants) {
			writeError(c.bw, "ERR weight count must match variant count")
			return
		}
		if len(weights) > 0 {
			c.eng.Experiments.DefineWeighted(name, variants, weights)
		} else {
			c.eng.Experiments.Define(name, variants)
		}
		c.eng.RecordWrite("AB.DEFINE", args)
		writeSimple(c.bw, "OK")
	case "ASSIGN":
		if len(args) != 2 {
			writeError(c.bw, "AB.ASSIGN name user")
			return
		}
		v, ok := c.eng.Experiments.Assign(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "EXPOSE":
		if len(args) != 2 {
			writeError(c.bw, "AB.EXPOSE name variant")
			return
		}
		c.eng.Experiments.Expose(args[0], args[1])
		c.eng.RecordWrite("AB.EXPOSE", args)
		writeSimple(c.bw, "OK")
	case "RECORD":
		if len(args) != 3 {
			writeError(c.bw, "AB.RECORD name variant value")
			return
		}
		v, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "ERR value must be a float")
			return
		}
		c.eng.Experiments.Record(args[0], args[1], v)
		c.eng.RecordWrite("AB.RECORD", args)
		writeSimple(c.bw, "OK")
	case "STATS":
		if len(args) != 1 {
			writeError(c.bw, "AB.STATS name")
			return
		}
		st, ok := c.eng.Experiments.Stats(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		variants := make([]any, 0, len(st.Variants))
		for _, v := range st.Variants {
			variants = append(variants, []any{
				"variant", v.Variant,
				"exposures", v.Exposures,
				"wins", v.Wins,
				"win_rate", strconv.FormatFloat(v.WinRate, 'f', 4, 64),
				"total_value", strconv.FormatFloat(v.TotalValue, 'f', 4, 64),
				"avg_value", strconv.FormatFloat(v.AvgValue, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"name", st.Name,
			"variants", variants,
			"winner", st.Winner,
			"created_at", st.CreatedAt.Unix(),
		})
	case "LIST":
		writeArray(c.bw, c.eng.Experiments.List())
	case "RESET":
		if len(args) != 1 {
			writeError(c.bw, "AB.RESET name")
			return
		}
		ok := c.eng.Experiments.Reset(args[0])
		if ok {
			c.eng.RecordWrite("AB.RESET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "DELETE":
		if len(args) != 1 {
			writeError(c.bw, "AB.DELETE name")
			return
		}
		ok := c.eng.Experiments.Delete(args[0])
		if ok {
			c.eng.RecordWrite("AB.DELETE", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	default:
		writeError(c.bw, "Unknown AB subcommand "+sub)
	}
}

// ── GRAPH.* ─────────────────────────────────────────────────────────

// graphCmd dispatches the GRAPH.<sub> family. Subcommands:
//
//	GRAPH.LINK subject predicate object
//	GRAPH.UNLINK subject predicate object
//	GRAPH.NEIGHBORS subject [PREDICATE p]
//	GRAPH.IN object [PREDICATE p]
//	GRAPH.PATH from to [MAXDEPTH n] [PREDICATE p]
//	GRAPH.SUBJECTS
//	GRAPH.STATS
func (c *conn) graphCmd(sub string, args []string) {
	switch sub {
	case "LINK":
		if len(args) != 3 {
			writeError(c.bw, "GRAPH.LINK subject predicate object")
			return
		}
		ok := c.eng.Graph.Link(args[0], args[1], args[2])
		c.eng.RecordWrite("GRAPH.LINK", args)
		if ok {
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "UNLINK":
		if len(args) != 3 {
			writeError(c.bw, "GRAPH.UNLINK subject predicate object")
			return
		}
		ok := c.eng.Graph.Unlink(args[0], args[1], args[2])
		if ok {
			c.eng.RecordWrite("GRAPH.UNLINK", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "NEIGHBORS":
		if len(args) < 1 {
			writeError(c.bw, "GRAPH.NEIGHBORS subject [PREDICATE p]")
			return
		}
		pred := ""
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "PREDICATE" {
				pred = args[i+1]
			}
		}
		ns := c.eng.Graph.Neighbors(args[0], pred)
		out := make([]any, 0, len(ns))
		for _, n := range ns {
			out = append(out, []any{"predicate", n.Predicate, "object", n.Object})
		}
		writeValue(c.bw, out)
	case "IN":
		if len(args) < 1 {
			writeError(c.bw, "GRAPH.IN object [PREDICATE p]")
			return
		}
		pred := ""
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "PREDICATE" {
				pred = args[i+1]
			}
		}
		writeArray(c.bw, c.eng.Graph.In(args[0], pred))
	case "PATH":
		if len(args) < 2 {
			writeError(c.bw, "GRAPH.PATH from to [MAXDEPTH n] [PREDICATE p]")
			return
		}
		max := 0
		pred := ""
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "MAXDEPTH":
				max, _ = strconv.Atoi(args[i+1])
			case "PREDICATE":
				pred = args[i+1]
			}
		}
		path, ok := c.eng.Graph.Path(args[0], args[1], max, pred)
		if !ok {
			writeNil(c.bw)
			return
		}
		out := make([]any, 0, len(path))
		for _, n := range path {
			out = append(out, []any{"predicate", n.Predicate, "object", n.Object})
		}
		writeValue(c.bw, out)
	case "SUBJECTS":
		writeArray(c.bw, c.eng.Graph.Subjects())
	case "STATS":
		st := c.eng.Graph.Stats()
		writeValue(c.bw, []any{
			"subjects", int64(st.Subjects),
			"objects", int64(st.Objects),
			"edges", int64(st.Edges),
		})
	default:
		writeError(c.bw, "Unknown GRAPH subcommand "+sub)
	}
}

// ── SCHEDULE.* ──────────────────────────────────────────────────────

// scheduleCmd dispatches the SCHEDULE.<sub> family. Subcommands:
//
//	SCHEDULE.AT unix-millis cmd args...
//	SCHEDULE.IN delay-ms cmd args...
//	SCHEDULE.CANCEL id
//	SCHEDULE.LIST
//	SCHEDULE.STATS
func (c *conn) scheduleCmd(sub string, args []string) {
	switch sub {
	case "AT":
		if len(args) < 2 {
			writeError(c.bw, "SCHEDULE.AT unix-millis cmd args...")
			return
		}
		ms, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR unix-millis must be an integer")
			return
		}
		id := c.eng.Scheduler.At(time.UnixMilli(ms), args[1], args[2:])
		c.eng.RecordWrite("SCHEDULE.AT", args)
		writeInt(c.bw, id)
	case "IN":
		if len(args) < 2 {
			writeError(c.bw, "SCHEDULE.IN delay-ms cmd args...")
			return
		}
		ms, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR delay-ms must be an integer")
			return
		}
		id := c.eng.Scheduler.In(time.Duration(ms)*time.Millisecond, args[1], args[2:])
		c.eng.RecordWrite("SCHEDULE.IN", args)
		writeInt(c.bw, id)
	case "CANCEL":
		if len(args) != 1 {
			writeError(c.bw, "SCHEDULE.CANCEL id")
			return
		}
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			writeError(c.bw, "ERR id must be an integer")
			return
		}
		ok := c.eng.Scheduler.Cancel(id)
		if ok {
			c.eng.RecordWrite("SCHEDULE.CANCEL", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "LIST":
		tasks := c.eng.Scheduler.List()
		out := make([]any, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, []any{
				"id", t.ID,
				"fire_at", t.FireAt.UnixMilli(),
				"cmd", t.Cmd,
				"args", t.Args,
				"created_at", t.CreatedAt.Unix(),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		st := c.eng.Scheduler.Stats()
		writeValue(c.bw, []any{
			"pending", int64(st.Pending),
			"total_scheduled", st.Total,
		})
	default:
		writeError(c.bw, "Unknown SCHEDULE subcommand "+sub)
	}
}

// ── EVENT.* ─────────────────────────────────────────────────────────

// eventCmd dispatches the EVENT.<sub> family. Subcommands:
//
//	EVENT.APPEND stream json-payload
//	EVENT.PROJECT stream name reducer field [GROUPBY field]
//	EVENT.READ stream projection
//	EVENT.RANGE stream [start [end]]
//	EVENT.LEN stream
func (c *conn) eventCmd(sub string, args []string) {
	switch sub {
	case "APPEND":
		if len(args) != 2 {
			writeError(c.bw, "EVENT.APPEND stream json-payload")
			return
		}
		seq, err := c.eng.EventLog.Append(args[0], []byte(args[1]))
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("EVENT.APPEND", args)
		writeInt(c.bw, seq)
	case "PROJECT":
		if len(args) < 4 {
			writeError(c.bw, "EVENT.PROJECT stream name reducer field [GROUPBY field]")
			return
		}
		stream, name, reducer, field := args[0], args[1], args[2], args[3]
		groupBy := ""
		for i := 4; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "GROUPBY" {
				groupBy = args[i+1]
			}
		}
		c.eng.EventLog.Project(stream, name, reducer, field, groupBy)
		c.eng.RecordWrite("EVENT.PROJECT", args)
		writeSimple(c.bw, "OK")
	case "READ":
		if len(args) != 2 {
			writeError(c.bw, "EVENT.READ stream projection")
			return
		}
		v, ok := c.eng.EventLog.Read(args[0], args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		out := make([]any, 0, len(v)*2)
		for k, val := range v {
			out = append(out, k, formatProjectionValue(val))
		}
		writeValue(c.bw, out)
	case "RANGE":
		if len(args) < 1 {
			writeError(c.bw, "EVENT.RANGE stream [start [end]]")
			return
		}
		var start, end int64
		if len(args) >= 2 {
			start, _ = strconv.ParseInt(args[1], 10, 64)
		}
		if len(args) >= 3 {
			end, _ = strconv.ParseInt(args[2], 10, 64)
		}
		evs := c.eng.EventLog.Range(args[0], start, end)
		out := make([]any, 0, len(evs))
		for _, ev := range evs {
			payload, _ := json.Marshal(ev["payload"])
			out = append(out, []any{
				"seq", ev["seq"],
				"payload", string(payload),
				"created_at", ev["created_at"],
			})
		}
		writeValue(c.bw, out)
	case "LEN":
		if len(args) != 1 {
			writeError(c.bw, "EVENT.LEN stream")
			return
		}
		writeInt(c.bw, int64(c.eng.EventLog.Len(args[0])))
	default:
		writeError(c.bw, "Unknown EVENT subcommand "+sub)
	}
}

// formatProjectionValue serialises a projection's per-group value as a
// bulk string. Floats use 4 dp; latest-style maps are JSON.
func formatProjectionValue(v interface{}) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', 4, 64)
	case string:
		return x
	default:
		out, _ := json.Marshal(v)
		return string(out)
	}
}

// ── POLICY.* ────────────────────────────────────────────────────────

// policyCmd dispatches the POLICY.<sub> family. Subcommands:
//
//	POLICY.ALLOW user resource action [TTL sec] [CTX k v ...]
//	POLICY.SET user resource action allow(0|1) reason [TTL sec] [CTX k v ...]
//	POLICY.PURGE
//	POLICY.STATS
func (c *conn) policyCmd(sub string, args []string) {
	switch sub {
	case "ALLOW":
		if len(args) < 3 {
			writeError(c.bw, "POLICY.ALLOW user resource action [TTL sec] [CTX k v ...]")
			return
		}
		ttl := time.Duration(0)
		ctx := map[string]string{}
		i := 3
		for i < len(args) {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "TTL":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR TTL requires a value")
					return
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "ERR ttl must be a non-negative integer")
					return
				}
				ttl = time.Duration(n) * time.Second
				i += 2
			case "CTX":
				j := i + 1
				for j+1 < len(args) {
					ctx[args[j]] = args[j+1]
					j += 2
				}
				i = j
			default:
				i++
			}
		}
		allow, reason := c.eng.Policies.Allow(args[0], args[1], args[2], ctx, ttl)
		writeValue(c.bw, []any{
			"allow", allow,
			"reason", reason,
		})
	case "SET":
		if len(args) < 5 {
			writeError(c.bw, "POLICY.SET user resource action allow(0|1) reason [TTL sec] [CTX k v ...]")
			return
		}
		allowFlag, err := strconv.Atoi(args[3])
		if err != nil {
			writeError(c.bw, "ERR allow must be 0 or 1")
			return
		}
		reason := args[4]
		ttl := time.Duration(0)
		ctx := map[string]string{}
		i := 5
		for i < len(args) {
			tok := strings.ToUpper(args[i])
			switch tok {
			case "TTL":
				if i+1 >= len(args) {
					writeError(c.bw, "ERR TTL requires a value")
					return
				}
				n, e2 := strconv.Atoi(args[i+1])
				if e2 != nil || n < 0 {
					writeError(c.bw, "ERR ttl must be a non-negative integer")
					return
				}
				ttl = time.Duration(n) * time.Second
				i += 2
			case "CTX":
				j := i + 1
				for j+1 < len(args) {
					ctx[args[j]] = args[j+1]
					j += 2
				}
				i = j
			default:
				i++
			}
		}
		c.eng.Policies.Set(args[0], args[1], args[2], ctx, allowFlag != 0, reason, ttl)
		c.eng.RecordWrite("POLICY.SET", args)
		writeSimple(c.bw, "OK")
	case "PURGE":
		n := c.eng.Policies.Purge()
		c.eng.RecordWrite("POLICY.PURGE", nil)
		writeInt(c.bw, int64(n))
	case "STATS":
		st := c.eng.Policies.Stats()
		writeValue(c.bw, []any{
			"entries", int64(st.Entries),
			"hits", st.Hits,
			"misses", st.Misses,
		})
	default:
		writeError(c.bw, "Unknown POLICY subcommand "+sub)
	}
}

// ── INFER.* ─────────────────────────────────────────────────────────

// inferCmd dispatches the INFER.<sub> family. Subcommands:
//
//	INFER.GENERATE prompt [MODEL m] [TEMP t] [MAXTOK n] [TENANT id] [TTL sec]
//	INFER.FORGET prompt [MODEL m] [TEMP t]
//	INFER.PURGE
//	INFER.STATS
//	INFER.DEFAULT provider
func (c *conn) inferCmd(sub string, args []string) {
	switch sub {
	case "GENERATE":
		if len(args) < 1 {
			writeError(c.bw, "INFER.GENERATE prompt [MODEL m] [TEMP t] [MAXTOK n] [TENANT id] [TTL sec]")
			return
		}
		prompt := args[0]
		opts := aiops.InferOpts{}
		ttl := time.Duration(0)
		for i := 1; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "MODEL":
				opts.Model = args[i+1]
			case "TEMP":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "ERR temp must be a float")
					return
				}
				opts.Temperature = f
			case "MAXTOK":
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					writeError(c.bw, "ERR maxtok must be an integer")
					return
				}
				opts.MaxTokens = n
			case "TENANT":
				opts.Tenant = args[i+1]
			case "TTL":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "ERR ttl must be a non-negative integer")
					return
				}
				ttl = time.Duration(n) * time.Second
			}
		}
		resp, hit, cost, err := c.eng.Inference.Generate(prompt, opts, ttl)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		// Charge against the tenant budget on a real upstream call (not
		// a cache hit). Best-effort — a Charge() failure here doesn't
		// invalidate the response we already produced.
		if !hit && cost > 0 && opts.Tenant != "" && c.eng.CostBudgets != nil {
			_, _, _ = c.eng.CostBudgets.Charge(opts.Tenant, cost)
		}
		c.eng.RecordWrite("INFER.GENERATE", args)
		writeValue(c.bw, []any{
			"response", resp,
			"hit", hit,
			"cost", strconv.FormatFloat(cost, 'f', 6, 64),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "INFER.FORGET prompt [MODEL m] [TEMP t]")
			return
		}
		prompt := args[0]
		opts := aiops.InferOpts{}
		for i := 1; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "MODEL":
				opts.Model = args[i+1]
			case "TEMP":
				f, _ := strconv.ParseFloat(args[i+1], 64)
				opts.Temperature = f
			}
		}
		ok := c.eng.Inference.Forget(prompt, opts)
		if ok {
			c.eng.RecordWrite("INFER.FORGET", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "PURGE":
		n := c.eng.Inference.Purge()
		c.eng.RecordWrite("INFER.PURGE", nil)
		writeInt(c.bw, int64(n))
	case "STATS":
		st := c.eng.Inference.Stats()
		writeValue(c.bw, []any{
			"cached_entries", int64(st.Entries),
			"providers", st.Providers,
			"default_provider", st.Default,
			"cache_hits", st.Hits,
			"cache_misses", st.Misses,
			"upstream_calls", st.Calls,
			"upstream_errors", st.Errors,
		})
	case "DEFAULT":
		if len(args) != 1 {
			writeError(c.bw, "INFER.DEFAULT provider")
			return
		}
		if err := c.eng.Inference.SetDefault(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("INFER.DEFAULT", args)
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown INFER subcommand "+sub)
	}
}

// ── MCP.* ───────────────────────────────────────────────────────────

// mcpCmd dispatches the MCP.<sub> family. Subcommands:
//
//	MCP.TOOLS
//	MCP.RESOURCES
//	MCP.CALL name json-args
//	MCP.READ uri
//	MCP.RPC json-rpc-frame
func (c *conn) mcpCmd(sub string, args []string) {
	switch sub {
	case "TOOLS":
		tools := c.eng.MCP.Tools()
		out := make([]any, 0, len(tools))
		for _, t := range tools {
			schema, _ := json.Marshal(t.InputSchema)
			out = append(out, []any{
				"name", t.Name,
				"description", t.Description,
				"input_schema", string(schema),
			})
		}
		writeValue(c.bw, out)
	case "RESOURCES":
		res := c.eng.MCP.Resources()
		out := make([]any, 0, len(res))
		for _, r := range res {
			out = append(out, []any{
				"uri", r.URI,
				"name", r.Name,
				"description", r.Description,
				"mime_type", r.MimeType,
			})
		}
		writeValue(c.bw, out)
	case "CALL":
		if len(args) != 2 {
			writeError(c.bw, "MCP.CALL name json-args")
			return
		}
		// Build a tools/call JSON-RPC frame and dispatch through the
		// MCP server so we get the same path as a real MCP client.
		frame := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]interface{}{
				"name":      args[0],
				"arguments": json.RawMessage(args[1]),
			},
		}
		// Validate the JSON args first so we surface parse errors with
		// a helpful message rather than the JSON-RPC error envelope.
		var probe map[string]interface{}
		if err := json.Unmarshal([]byte(args[1]), &probe); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		raw, _ := json.Marshal(frame)
		out := c.eng.MCP.HandleBytes(raw)
		writeBulk(c.bw, string(out))
	case "READ":
		if len(args) != 1 {
			writeError(c.bw, "MCP.READ uri")
			return
		}
		frame := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "resources/read",
			"params":  map[string]interface{}{"uri": args[0]},
		}
		raw, _ := json.Marshal(frame)
		out := c.eng.MCP.HandleBytes(raw)
		writeBulk(c.bw, string(out))
	case "RPC":
		if len(args) != 1 {
			writeError(c.bw, "MCP.RPC json-rpc-frame")
			return
		}
		out := c.eng.MCP.HandleBytes([]byte(args[0]))
		writeBulk(c.bw, string(out))
	default:
		writeError(c.bw, "Unknown MCP subcommand "+sub)
	}
}

// ── KV.SUBSCRIBE / KV.UNSUBSCRIBE ───────────────────────────────────

// kvSubscribeCmd is a thin sugar over SUBSCRIBE that translates each
// key into the canonical __keyspace__:<key> channel name. Lets clients
// say "watch this key for changes" without knowing the keyspace
// notification convention.
func (c *conn) kvSubscribeCmd(args []string) {
	if len(args) == 0 {
		writeError(c.bw, "KV.SUBSCRIBE key [key ...]")
		return
	}
	channels := make([]string, 0, len(args))
	for _, k := range args {
		channels = append(channels, "__keyspace__:"+k)
	}
	c.subscribeCmd(channels, false)
}

// kvUnsubscribeCmd is the matching sugar over UNSUBSCRIBE.
func (c *conn) kvUnsubscribeCmd(args []string) {
	channels := make([]string, 0, len(args))
	for _, k := range args {
		channels = append(channels, "__keyspace__:"+k)
	}
	c.unsubscribeCmd(channels, false)
}
