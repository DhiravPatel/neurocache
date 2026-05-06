package resp

// MEMORY.* — layered memory family. Episodic / semantic / procedural
// layers, importance hints, dedup-on-write, recency-weighted query,
// soft + hard decay, and bulk consolidation. Backed by
// `internal/memory` AddWithOptions / QueryLayered / Decay /
// Consolidate.
//
// The legacy MEMORY_ADD / MEMORY_QUERY commands are unchanged — they
// remain untyped and route to the episodic layer by default.

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
)

// memoryFamilyCmd dispatches the MEMORY.<sub> family. Subcommands:
//
//	MEMORY.ADD user text [LAYER episodic|semantic|procedural]
//	          [IMPORTANCE f] [DEDUP f] [META k v ...]
//	MEMORY.QUERY user text [LAYER l] [K n] [THRESHOLD f]
//	          [RECENCY f] [TOUCH 0|1]
//	MEMORY.LIST user [LAYER l]
//	MEMORY.DEL user id
//	MEMORY.STATS [user]
//	MEMORY.DECAY user [LAYER l] [HALFLIFE seconds] [MAXAGE seconds]
//	          [UNTOUCHED seconds] [MINSCORE f] [DRYRUN 0|1]
//	MEMORY.CONSOLIDATE user [THRESHOLD f] [MIN n] [DROP 0|1]
//	          [IMPORTANCE f]
func (c *conn) memoryFamilyCmd(sub string, args []string) {
	switch sub {
	case "ADD":
		if len(args) < 2 {
			writeError(c.bw, "MEMORY.ADD user text [LAYER l] [IMPORTANCE f] [DEDUP f] [META k v ...]")
			return
		}
		opts := memory.AddOptions{Layer: memory.LayerEpisodic, Importance: 0.5}
		text := args[1]
		i := 2
		for i+1 < len(args) {
			key := strings.ToUpper(args[i])
			val := args[i+1]
			advance := 2
			switch key {
			case "LAYER":
				opts.Layer = memory.Layer(strings.ToLower(val))
			case "IMPORTANCE":
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					opts.Importance = f
				}
			case "DEDUP":
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					opts.DedupThreshold = f
				}
			case "META":
				// META k1 v1 k2 v2 ... — consume to end.
				rest := args[i+1:]
				if len(rest)%2 != 0 {
					writeError(c.bw, "META expects even number of k/v")
					return
				}
				opts.Meta = make(map[string]string, len(rest)/2)
				for j := 0; j < len(rest); j += 2 {
					opts.Meta[rest[j]] = rest[j+1]
				}
				i = len(args) // done
				advance = 0
			default:
				writeError(c.bw, "unknown option "+args[i])
				return
			}
			i += advance
		}
		e, isNew, err := c.eng.Memory.AddWithOptions(args[0], text, opts)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("MEMORY.ADD", args)
		flag := "0"
		if isNew {
			flag = "1"
		}
		writeValue(c.bw, []any{
			"id", e.ID,
			"new", flag,
			"layer", string(e.Layer),
		})
	case "QUERY":
		if len(args) < 2 {
			writeError(c.bw, "MEMORY.QUERY user text [LAYER l] [K n] [THRESHOLD f] [RECENCY f] [TOUCH 0|1]")
			return
		}
		opts := memory.LayerQueryOptions{K: 5, Threshold: 0.3}
		text := args[1]
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "LAYER":
				opts.Layer = memory.Layer(strings.ToLower(args[i+1]))
			case "K":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.K = n
				}
			case "THRESHOLD":
				if f, err := strconv.ParseFloat(args[i+1], 32); err == nil {
					opts.Threshold = float32(f)
				}
			case "RECENCY":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.RecencyBias = f
				}
			case "TOUCH":
				opts.TouchHits = args[i+1] != "0" && strings.ToLower(args[i+1]) != "false"
			}
		}
		hits := c.eng.Memory.QueryLayered(args[0], text, opts)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"id", h.Entry.ID,
				"text", h.Entry.Text,
				"score", strconv.FormatFloat(float64(h.Score), 'f', 6, 64),
				"layer", string(h.Entry.Layer),
				"importance", strconv.FormatFloat(h.Entry.Importance, 'f', 3, 64),
				"created_at", h.Entry.CreatedAt.UTC().Format(time.RFC3339),
				"access_count", int64(h.Entry.AccessCount),
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "MEMORY.LIST user [LAYER l]")
			return
		}
		var layer memory.Layer
		for i := 1; i+1 < len(args); i += 2 {
			if strings.ToUpper(args[i]) == "LAYER" {
				layer = memory.Layer(strings.ToLower(args[i+1]))
			}
		}
		entries := c.eng.Memory.ListByLayer(args[0], layer)
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, []any{
				"id", e.ID,
				"text", e.Text,
				"layer", string(e.Layer),
				"importance", strconv.FormatFloat(e.Importance, 'f', 3, 64),
				"created_at", e.CreatedAt.UTC().Format(time.RFC3339),
				"access_count", int64(e.AccessCount),
			})
		}
		writeValue(c.bw, out)
	case "DEL":
		if len(args) != 2 {
			writeError(c.bw, "MEMORY.DEL user id")
			return
		}
		if c.eng.Memory.Delete(args[0], args[1]) {
			c.eng.RecordWrite("MEMORY.DEL", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "STATS":
		userID := ""
		if len(args) > 0 {
			userID = args[0]
		}
		st := c.eng.Memory.LayerStats(userID)
		writeValue(c.bw, []any{
			"episodic", int64(st.Episodic),
			"semantic", int64(st.Semantic),
			"procedural", int64(st.Procedural),
			"other", int64(st.Other),
		})
	case "DECAY":
		if len(args) < 1 {
			writeError(c.bw, "MEMORY.DECAY user [LAYER l] [HALFLIFE s] [MAXAGE s] [UNTOUCHED s] [MINSCORE f] [DRYRUN 0|1]")
			return
		}
		opts := memory.DecayOptions{Layer: memory.LayerEpisodic, MinScore: 0.05}
		for i := 1; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "LAYER":
				opts.Layer = memory.Layer(strings.ToLower(args[i+1]))
			case "HALFLIFE":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.HalfLife = time.Duration(n) * time.Second
				}
			case "MAXAGE":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.MaxAge = time.Duration(n) * time.Second
				}
			case "UNTOUCHED":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.UntouchedFor = time.Duration(n) * time.Second
				}
			case "MINSCORE":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.MinScore = f
				}
			case "DRYRUN":
				opts.DryRun = args[i+1] != "0" && strings.ToLower(args[i+1]) != "false"
			}
		}
		res := c.eng.Memory.Decay(args[0], opts)
		if !opts.DryRun {
			c.eng.RecordWrite("MEMORY.DECAY", args)
		}
		writeValue(c.bw, []any{
			"scanned", int64(res.Scanned),
			"dropped", int64(res.Dropped),
			"dry_run", opts.DryRun,
		})
	case "CONSOLIDATE":
		if len(args) < 1 {
			writeError(c.bw, "MEMORY.CONSOLIDATE user [THRESHOLD f] [MIN n] [DROP 0|1] [IMPORTANCE f]")
			return
		}
		opts := memory.ConsolidateOptions{UserID: args[0]}
		for i := 1; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "THRESHOLD":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.Threshold = f
				}
			case "MIN":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.MinSize = n
				}
			case "DROP":
				opts.Drop = args[i+1] != "0" && strings.ToLower(args[i+1]) != "false"
			case "IMPORTANCE":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.Importance = f
				}
			}
		}
		res := c.eng.Memory.Consolidate(opts)
		c.eng.RecordWrite("MEMORY.CONSOLIDATE", args)
		newIDs := make([]any, 0, len(res.NewIDs))
		for _, id := range res.NewIDs {
			newIDs = append(newIDs, id)
		}
		writeValue(c.bw, []any{
			"clusters", int64(res.Clusters),
			"written", int64(res.Written),
			"dropped", int64(res.Dropped),
			"new_ids", newIDs,
		})
	default:
		writeError(c.bw, "Unknown MEMORY subcommand "+sub)
	}
}
