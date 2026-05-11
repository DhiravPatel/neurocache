package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// tokenCmd handles TOKEN.* — token counting + budget tracking. Solves
// the "predict cost / prevent context overflow / split long doc"
// pain every LLM app hits.
//
//   TOKEN.COUNT <model> <text>
//   TOKEN.SPLIT <model> <text> <max-tokens>     -> RESP array of chunks
//   TOKEN.BUDGET.SET <budget-id> <model> <max-tokens>
//   TOKEN.BUDGET.FIT <budget-id> <text>         -> [fits, tokens-in, remaining]
//   TOKEN.BUDGET.GET <budget-id>
//   TOKEN.BUDGET.RESET <budget-id>
//   TOKEN.BUDGET.DELETE <budget-id>
//   TOKEN.BUDGET.LIST
//   TOKEN.STATS
func (c *conn) tokenCmd(sub string, args []string) {
	switch sub {
	case "COUNT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'token.count'")
			return
		}
		writeInt(c.bw, int64(c.eng.Tokens.Count(args[0], args[1])))
	case "SPLIT":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'token.split'")
			return
		}
		max, err := strconv.Atoi(args[2])
		if err != nil || max <= 0 {
			writeError(c.bw, "max-tokens must be a positive integer")
			return
		}
		chunks := c.eng.Tokens.Split(args[0], args[1], max)
		writeArray(c.bw, chunks)
	case "BUDGET.SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'token.budget.set'")
			return
		}
		max, err := strconv.Atoi(args[2])
		if err != nil || max <= 0 {
			writeError(c.bw, "max-tokens must be a positive integer")
			return
		}
		c.eng.Tokens.SetBudget(args[0], args[1], max)
		writeSimple(c.bw, "OK")
	case "BUDGET.FIT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'token.budget.fit'")
			return
		}
		r, ok := c.eng.Tokens.FitAndRecord(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNBUDGET", "no budget configured for that ID")
			return
		}
		fitsStr := "0"
		if r.Fits {
			fitsStr = "1"
		}
		writeArray(c.bw, []string{
			"fits", fitsStr,
			"tokens_in", strconv.Itoa(r.TokensIn),
			"remaining", strconv.Itoa(r.Remaining),
		})
	case "BUDGET.GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'token.budget.get'")
			return
		}
		st, ok := c.eng.Tokens.Budget(args[0])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNBUDGET", "no budget configured for that ID")
			return
		}
		writeArray(c.bw, []string{
			"budget_id", st.BudgetID,
			"model", st.Model,
			"max_tokens", strconv.FormatInt(st.MaxTokens, 10),
			"used_tokens", strconv.FormatInt(st.UsedTokens, 10),
			"remaining", strconv.FormatInt(st.Remaining, 10),
			"util_percent", strconv.FormatFloat(st.UtilPercent, 'f', 2, 64),
		})
	case "BUDGET.RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'token.budget.reset'")
			return
		}
		if c.eng.Tokens.ResetBudget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "BUDGET.DELETE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'token.budget.delete'")
			return
		}
		if c.eng.Tokens.DeleteBudget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "BUDGET.LIST":
		rows := c.eng.Tokens.ListBudgets()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				"budget_id", r.BudgetID,
				"model", r.Model,
				"max_tokens", strconv.FormatInt(r.MaxTokens, 10),
				"used_tokens", strconv.FormatInt(r.UsedTokens, 10),
				"remaining", strconv.FormatInt(r.Remaining, 10),
				"util_percent", strconv.FormatFloat(r.UtilPercent, 'f', 2, 64),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Tokens.Stats()
		writeArray(c.bw, []string{
			"total_counts", strconv.FormatInt(s.TotalCounts, 10),
			"total_splits", strconv.FormatInt(s.TotalSplits, 10),
			"unique_budgets", strconv.Itoa(s.UniqueBudgets),
		})
	default:
		writeError(c.bw, "unknown TOKEN subcommand: "+sub)
	}
}

// chunkCmd handles CHUNK.* — text chunking for RAG ingestion.
//
//   CHUNK.TEXT <text> [STRATEGY char|sentence|paragraph|token]
//                     [SIZE n] [OVERLAP n] [MODEL m]
//                                              -> RESP array of chunks
//   CHUNK.STATS
func (c *conn) chunkCmd(sub string, args []string) {
	switch sub {
	case "TEXT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'chunk.text'")
			return
		}
		text := args[0]
		opts := llmstack.ChunkOpts{Strategy: "char", Size: 1024, Overlap: 0}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "STRATEGY":
				opts.Strategy = val
			case "SIZE":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "SIZE must be a positive integer")
					return
				}
				opts.Size = n
			case "OVERLAP":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "OVERLAP must be a non-negative integer")
					return
				}
				opts.Overlap = n
			case "MODEL":
				opts.Model = val
			default:
				writeError(c.bw, "unknown CHUNK.TEXT option: "+key)
				return
			}
			i += 2
		}
		chunks := c.eng.Chunker.Chunk(text, opts)
		writeArray(c.bw, chunks)
	case "STATS":
		s := c.eng.Chunker.Stats()
		writeArray(c.bw, []string{
			"total_chunks", strconv.FormatInt(s.TotalChunks, 10),
		})
	default:
		writeError(c.bw, "unknown CHUNK subcommand: "+sub)
	}
}

// contextAssembleCmd handles CONTEXT.ASSEMBLE.
//
//   CONTEXT.ASSEMBLE <model> <budget-tokens>
//                    SECTION <id> <priority> <text>
//                    SECTION <id> <priority> <text>
//                    ...
//
// Returns:
//   used      = array of section IDs included
//   skipped   = array of section IDs left out
//   total_tokens / budget_tokens
//   combined  = the joined text apps drop into the model context
func (c *conn) contextAssembleCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'context.assemble'")
		return
	}
	model := args[0]
	budget, err := strconv.Atoi(args[1])
	if err != nil || budget <= 0 {
		writeError(c.bw, "budget-tokens must be a positive integer")
		return
	}
	var sections []llmstack.ContextSection
	i := 2
	for i < len(args) {
		if !strings.EqualFold(args[i], "SECTION") {
			writeError(c.bw, "expected SECTION marker at arg "+strconv.Itoa(i+1))
			return
		}
		if i+3 >= len(args) {
			writeError(c.bw, "SECTION needs <id> <priority> <text>")
			return
		}
		pri, err := strconv.Atoi(args[i+2])
		if err != nil {
			writeError(c.bw, "section priority must be integer")
			return
		}
		sections = append(sections, llmstack.ContextSection{
			ID:       args[i+1],
			Priority: pri,
			Text:     args[i+3],
		})
		i += 4
	}
	res := c.eng.Chunker.AssembleContext(model, budget, sections)
	usedAny := make([]any, 0, len(res.Used))
	for _, u := range res.Used {
		usedAny = append(usedAny, u)
	}
	skippedAny := make([]any, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skippedAny = append(skippedAny, s)
	}
	writeValue(c.bw, []any{
		"used", usedAny,
		"skipped", skippedAny,
		"total_tokens", strconv.Itoa(res.TotalToks),
		"budget_tokens", strconv.Itoa(res.BudgetToks),
		"combined", res.Combined,
	})
}
