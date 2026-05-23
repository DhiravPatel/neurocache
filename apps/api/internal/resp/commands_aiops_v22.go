package resp

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// convForkCmd handles CONV.FORK.* — branched conversation tree.
func (c *conn) convForkCmd(sub string, args []string) {
	switch sub {
	case "SEED":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.seed'")
			return
		}
		if err := c.eng.ConvFork.Seed(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CREATE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.create' (need parent fork [AT n])")
			return
		}
		at := -1
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "AT" {
				writeError(c.bw, "unknown CONV.FORK.CREATE option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				writeError(c.bw, "AT must be a non-negative integer")
				return
			}
			at = n
			i += 2
		}
		if err := c.eng.ConvFork.Create(args[0], args[1], at); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "APPEND":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.append' (need conv role content)")
			return
		}
		if err := c.eng.ConvFork.Append(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.get'")
			return
		}
		turns, ok := c.eng.ConvFork.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(turns))
		for _, t := range turns {
			out = append(out, []any{
				"role", t.Role,
				"content", t.Content,
				"ts", strconv.FormatInt(t.TS, 10),
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.list'")
			return
		}
		kids, ok := c.eng.ConvFork.List(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, kids)
	case "TREE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.tree'")
			return
		}
		nodes, ok := c.eng.ConvFork.Tree(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, []any{
				"id", n.ID,
				"parent_id", n.ParentID,
				"forked_at", strconv.Itoa(n.ForkedAt),
				"turns", strconv.Itoa(n.TurnCount),
				"children", n.ChildIDs,
				"created_at", strconv.FormatInt(n.CreatedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "DELETE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'conv.fork.delete'")
			return
		}
		writeInt(c.bw, int64(c.eng.ConvFork.Delete(args[0])))
	case "STATS":
		s := c.eng.ConvFork.Stats()
		writeArray(c.bw, []string{
			"branches", strconv.Itoa(s.Branches),
			"roots", strconv.Itoa(s.Roots),
			"total_seeds", strconv.FormatInt(s.TotalSeeds, 10),
			"total_forks", strconv.FormatInt(s.TotalForks, 10),
			"total_appends", strconv.FormatInt(s.TotalAppends, 10),
			"total_deletes", strconv.FormatInt(s.TotalDeletes, 10),
		})
	default:
		writeError(c.bw, "unknown CONV.FORK subcommand: "+sub)
	}
}

// semDiffCmd handles SEMDIFF.* — semantic version diff.
func (c *conn) semDiffCmd(sub string, args []string) {
	switch sub {
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.check' (need text-a text-b)")
			return
		}
		r := c.eng.SemDiff.Check(args[0], args[1])
		writeSemDiffResult(c.bw, r.Cosine, r.Verdict, r.Identical, r.Equivalent)
	case "PUT":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.put' (need name version text)")
			return
		}
		if err := c.eng.SemDiff.Put(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.get'")
			return
		}
		label := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "VERSION" {
				writeError(c.bw, "unknown SEMDIFF.GET option: "+key)
				return
			}
			label = val
			i += 2
		}
		lbl, text, ok := c.eng.SemDiff.Get(args[0], label)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{"version", lbl, "text", text})
	case "COMPARE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.compare' (need name v1 v2)")
			return
		}
		r, err := c.eng.SemDiff.Compare(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSemDiffResult(c.bw, r.Cosine, r.Verdict, r.Identical, r.Equivalent)
	case "HISTORY":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.history'")
			return
		}
		rows, ok := c.eng.SemDiff.History(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"version", r.Label,
				"ts", strconv.FormatInt(r.TS, 10),
				"chars", strconv.Itoa(r.Chars),
				"vs_prev_cosine", strconv.FormatFloat(r.VsPrev, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "LATEST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.latest'")
			return
		}
		lbl, text, ok := c.eng.SemDiff.Latest(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{"version", lbl, "text", text})
	case "DELETE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'semdiff.delete'")
			return
		}
		if c.eng.SemDiff.Delete(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "NAMES":
		writeArray(c.bw, c.eng.SemDiff.Names())
	case "STATS":
		s := c.eng.SemDiff.Stats()
		writeArray(c.bw, []string{
			"names", strconv.Itoa(s.Names),
			"total_versions", strconv.Itoa(s.TotalVersions),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_puts", strconv.FormatInt(s.TotalPuts, 10),
			"total_compares", strconv.FormatInt(s.TotalCompares, 10),
		})
	default:
		writeError(c.bw, "unknown SEMDIFF subcommand: "+sub)
	}
}

// semRateCmd handles RATELIMIT.SEM.* — semantic rate limiter.
func (c *conn) semRateCmd(sub string, args []string) {
	switch sub {
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'ratelimit.sem.check' (need tenant text)")
			return
		}
		r, err := c.eng.SemRate.Check(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSemRateResult(c.bw, r)
	case "PEEK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'ratelimit.sem.peek' (need tenant text)")
			return
		}
		r, err := c.eng.SemRate.Peek(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSemRateResult(c.bw, r)
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ratelimit.sem.config'")
			return
		}
		var (
			limit     int
			threshold float64
			window    time.Duration
		)
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "LIMIT":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			case "THRESHOLD":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f < 0 || f > 1 {
					writeError(c.bw, "THRESHOLD must be float in [0,1]")
					return
				}
				threshold = f
			case "WINDOW":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "WINDOW must be non-negative integer (seconds)")
					return
				}
				window = time.Duration(n) * time.Second
			default:
				writeError(c.bw, "unknown RATELIMIT.SEM.CONFIG option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.SemRate.Configure(args[0], limit, threshold, window); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ratelimit.sem.status'")
			return
		}
		st, ok := c.eng.SemRate.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"tenant", st.Tenant,
			"bucket_size", strconv.Itoa(st.BucketSize),
			"limit", strconv.Itoa(st.Limit),
			"threshold", strconv.FormatFloat(st.Threshold, 'f', 4, 64),
			"window_seconds", strconv.FormatInt(st.WindowSec, 10),
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ratelimit.sem.reset'")
			return
		}
		if c.eng.SemRate.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		writeArray(c.bw, c.eng.SemRate.List())
	case "RECENT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'ratelimit.sem.recent'")
			return
		}
		rec, ok := c.eng.SemRate.Recent(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rec))
		for _, r := range rec {
			out = append(out, []any{
				"ts", strconv.FormatInt(r.TS, 10),
				"text", r.Text,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.SemRate.Stats()
		writeArray(c.bw, []string{
			"tenants", strconv.Itoa(s.Tenants),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_allowed", strconv.FormatInt(s.TotalAllowed, 10),
			"total_denied", strconv.FormatInt(s.TotalDenied, 10),
			"total_peeks", strconv.FormatInt(s.TotalPeeks, 10),
		})
	default:
		writeError(c.bw, "unknown RATELIMIT.SEM subcommand: "+sub)
	}
}

// writeSemDiffResult emits the common SEMDIFF return shape.
func writeSemDiffResult(bw *bufio.Writer, cos float64, verdict string, identical, equivalent bool) {
	idInt := "0"
	if identical {
		idInt = "1"
	}
	eqInt := "0"
	if equivalent {
		eqInt = "1"
	}
	writeArray(bw, []string{
		"cosine", strconv.FormatFloat(cos, 'f', 4, 64),
		"verdict", verdict,
		"identical", idInt,
		"equivalent", eqInt,
	})
}

// writeSemRateResult emits the CHECK/PEEK return.
func writeSemRateResult(bw *bufio.Writer, r llmstack.SemRateResult) {
	allowInt := "0"
	if r.Allow {
		allowInt = "1"
	}
	writeArray(bw, []string{
		"allow", allowInt,
		"reason", r.Reason,
		"similar_count", strconv.Itoa(r.SimilarCount),
		"top_cosine", strconv.FormatFloat(r.TopCosine, 'f', 4, 64),
		"limit", strconv.Itoa(r.Limit),
		"window_seconds", strconv.FormatInt(r.WindowSec, 10),
	})
}
