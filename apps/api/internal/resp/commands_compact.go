package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
)

// compactCmd implements COMPACT.* — reversible, provenance-linked memory
// compaction.
//
//	COMPACT.PLAN user [LAYER l] [THRESHOLD f] [MINSIZE n] [MAXAGE s] [TARGET_BYTES n]
//	COMPACT.APPLY user [...same...] [DROP 0|1]
//	COMPACT.EXPAND user summary-id
//	COMPACT.STATS
func (c *conn) compactCmd(sub string, args []string) {
	switch sub {
	case "PLAN":
		if len(args) < 1 {
			writeError(c.bw, "ERR COMPACT.PLAN user [LAYER l] [THRESHOLD f] [MINSIZE n] [MAXAGE s] [TARGET_BYTES n]")
			return
		}
		opts, perr := parseCompactOpts(args[1:])
		if perr != "" {
			writeError(c.bw, "ERR "+perr)
			return
		}
		plan := c.eng.Compactor.Plan(args[0], opts)
		clusters := make([]any, 0, len(plan.Clusters))
		for _, cl := range plan.Clusters {
			clusters = append(clusters, []any{
				"source_ids", anyStrings(cl.SourceIDs),
				"summary", cl.Summary,
				"bytes", cl.Bytes,
			})
		}
		writeValue(c.bw, []any{
			"user", plan.User,
			"layer", plan.Layer,
			"entries", int64(plan.Entries),
			"bytes_folded", plan.BytesFolded,
			"summary_bytes", plan.SummaryBytes,
			"net_bytes", plan.NetBytes,
			"clusters", clusters,
		})

	case "APPLY":
		if len(args) < 1 {
			writeError(c.bw, "ERR COMPACT.APPLY user [LAYER l] [THRESHOLD f] [MINSIZE n] [MAXAGE s] [TARGET_BYTES n] [DROP 0|1]")
			return
		}
		opts, perr := parseCompactOpts(args[1:])
		if perr != "" {
			writeError(c.bw, "ERR "+perr)
			return
		}
		res := c.eng.Compactor.Apply(args[0], opts)
		// Audit trail: record each summary→source citation in LINEAGE.
		if c.eng.Lineage != nil {
			for _, s := range res.Summaries {
				for _, src := range s.SourceIDs {
					c.eng.Lineage.Record(s.SummaryID, src, "compact", 1.0)
				}
			}
		}
		summaries := make([]any, 0, len(res.Summaries))
		for _, s := range res.Summaries {
			summaries = append(summaries, []any{"summary_id", s.SummaryID, "source_ids", anyStrings(s.SourceIDs)})
		}
		writeValue(c.bw, []any{
			"folded", int64(res.Folded),
			"dropped", int64(res.Dropped),
			"bytes_folded", res.BytesFolded,
			"summaries", summaries,
		})

	case "EXPAND":
		if len(args) < 2 {
			writeError(c.bw, "ERR COMPACT.EXPAND user summary-id")
			return
		}
		res, ok := c.eng.Compactor.Expand(args[0], args[1])
		if !ok {
			writeError(c.bw, "ERR not a known reversible compaction summary: "+args[1])
			return
		}
		if c.eng.Lineage != nil {
			c.eng.Lineage.Forget(args[1])
		}
		writeValue(c.bw, []any{
			"restored", int64(res.Restored),
			"restored_ids", anyStrings(res.RestoredIDs),
		})

	case "STATS":
		s := c.eng.Compactor.Stats()
		writeValue(c.bw, []any{
			"reversible_summaries", int64(s.ReversibleSummaries),
			"total_applied", s.TotalApplied,
			"total_folded", s.TotalFolded,
			"total_expanded", s.TotalExpanded,
		})

	default:
		writeError(c.bw, "ERR unknown COMPACT subcommand "+sub)
	}
}

func parseCompactOpts(args []string) (memory.CompactOptions, string) {
	var o memory.CompactOptions
	for i := 0; i+1 < len(args)+1 && i < len(args); i += 2 {
		if i+1 >= len(args) {
			return o, "option " + args[i] + " needs a value"
		}
		val := args[i+1]
		switch strings.ToUpper(args[i]) {
		case "LAYER":
			l := memory.Layer(strings.ToLower(val))
			if !l.IsValid() {
				return o, "LAYER must be episodic|semantic|procedural"
			}
			o.Layer = l
		case "THRESHOLD":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return o, "THRESHOLD must be a float"
			}
			o.Threshold = f
		case "MINSIZE":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return o, "MINSIZE must be a positive integer"
			}
			o.MinSize = n
		case "MAXAGE":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				return o, "MAXAGE must be a non-negative integer (seconds)"
			}
			o.MaxAgeSec = n
		case "TARGET_BYTES":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				return o, "TARGET_BYTES must be a non-negative integer"
			}
			o.TargetBytes = n
		case "IMPORTANCE":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return o, "IMPORTANCE must be a float"
			}
			o.Importance = f
		case "DROP":
			o.Drop = val == "1" || strings.EqualFold(val, "true")
		default:
			return o, "unknown option " + args[i]
		}
	}
	return o, ""
}
