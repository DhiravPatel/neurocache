package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// SMISMEMBER key member [member ...]
func (c *conn) smismemberCmd(args []string) {
	if !c.wantArgs("SMISMEMBER", args, 2) {
		return
	}
	hits, err := c.eng.KV.SMIsMember(args[0], args[1:]...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(hits))
	for i, b := range hits {
		if b {
			out[i] = int64(1)
		} else {
			out[i] = int64(0)
		}
	}
	writeValue(c.bw, out)
}

// SINTERCARD numkeys key [key ...] [LIMIT n]
func (c *conn) sintercardCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'sintercard'")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		writeError(c.bw, "numkeys must be > 0")
		return
	}
	if 1+n > len(args) {
		writeError(c.bw, "numkeys is greater than the number of keys")
		return
	}
	keys := args[1 : 1+n]
	limit := 0
	for i := 1 + n; i < len(args); i++ {
		if strings.EqualFold(args[i], "LIMIT") && i+1 < len(args) {
			limit, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	count, err := c.eng.KV.SInterCard(keys, limit)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(count))
}

// GETDEL key
func (c *conn) getdelCmd(args []string) {
	if !c.wantArgs("GETDEL", args, 1) {
		return
	}
	v, ok, err := c.eng.KV.GetDel(args[0])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeBulk(c.bw, v)
}

// GETEX key [EX sec | PX ms | EXAT unix | PXAT unix-ms | PERSIST | KEEPTTL]
func (c *conn) getexCmd(args []string) {
	if !c.wantArgs("GETEX", args, 1) {
		return
	}
	mode, value := "", int64(0)
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "EX", "PX", "EXAT", "PXAT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			mode = strings.ToUpper(args[i])
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			value = v
			i++
		case "PERSIST":
			mode = "PERSIST"
		case "KEEPTTL":
			mode = "KEEP"
		}
	}
	v, ok, err := c.eng.KV.GetEx(args[0], mode, value)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeBulk(c.bw, v)
}

// LPOS key element [RANK rank] [COUNT count] [MAXLEN maxlen]
func (c *conn) lposCmd(args []string) {
	if !c.wantArgs("LPOS", args, 2) {
		return
	}
	rank := 1
	count := 0
	maxlen := 0
	hasCount := false
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "RANK":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			rank, _ = strconv.Atoi(args[i+1])
			i++
		case "COUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			hasCount = true
			i++
		case "MAXLEN":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			maxlen, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	if rank == 0 {
		writeError(c.bw, "RANK can't be zero")
		return
	}
	out, ok, err := c.eng.KV.LPos(args[0], args[1], rank, count, maxlen)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if hasCount {
		// COUNT form returns an array (possibly empty) regardless.
		ints := make([]any, len(out))
		for i, v := range out {
			ints[i] = int64(v)
		}
		writeValue(c.bw, ints)
		return
	}
	if !ok || len(out) == 0 {
		writeNil(c.bw)
		return
	}
	writeInt(c.bw, int64(out[0]))
}

// ── ZSet combinatorial ops ─────────────────────────────────────────

// parseZSetOpArgs parses the common shape:
//
//   numkeys key [key ...] [WEIGHTS w [w ...]] [AGGREGATE SUM|MIN|MAX]
//
// Used by ZUNIONSTORE / ZINTERSTORE / ZUNION / ZINTER / ZDIFF /
// ZINTERCARD. afterKeysIdx returns the position right after the key
// list so callers can scan the remaining flags.
func parseZSetOpArgs(args []string, hasDest bool) (dest string, keys []string, weights []float64, agg store.ZSetOpAggregate, withScores bool, err error) {
	i := 0
	if hasDest {
		dest = args[i]
		i++
	}
	if i >= len(args) {
		err = errZSetOpUsage
		return
	}
	n, e := strconv.Atoi(args[i])
	if e != nil || n <= 0 {
		err = errZSetOpUsage
		return
	}
	i++
	if i+n > len(args) {
		err = errZSetOpUsage
		return
	}
	keys = args[i : i+n]
	i += n
	for i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "WEIGHTS":
			if i+n >= len(args) {
				err = errZSetOpUsage
				return
			}
			weights = make([]float64, n)
			for j := 0; j < n; j++ {
				weights[j], _ = strconv.ParseFloat(args[i+1+j], 64)
			}
			i += 1 + n
		case "AGGREGATE":
			if i+1 >= len(args) {
				err = errZSetOpUsage
				return
			}
			agg, err = store.ParseZSetOpAggregate(args[i+1])
			if err != nil {
				return
			}
			i += 2
		case "WITHSCORES":
			withScores = true
			i++
		default:
			i++
		}
	}
	return
}

var errZSetOpUsage = errSyntax{msg: "wrong number of arguments for zset op"}

type errSyntax struct{ msg string }

func (e errSyntax) Error() string { return e.msg }

// ZUNIONSTORE dest numkeys key [key ...] [WEIGHTS w ...] [AGGREGATE op]
func (c *conn) zunionstoreCmd(args []string) {
	dest, keys, weights, agg, _, err := parseZSetOpArgs(args, true)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	n, err := c.eng.KV.ZUnionStore(dest, keys, weights, agg)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ZINTERSTORE dest numkeys key [key ...] [WEIGHTS w ...] [AGGREGATE op]
func (c *conn) zinterstoreCmd(args []string) {
	dest, keys, weights, agg, _, err := parseZSetOpArgs(args, true)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	n, err := c.eng.KV.ZInterStore(dest, keys, weights, agg)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ZDIFFSTORE dest numkeys key [key ...]
func (c *conn) zdiffstoreCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'zdiffstore'")
		return
	}
	dest := args[0]
	n, err := strconv.Atoi(args[1])
	if err != nil || n <= 0 {
		writeError(c.bw, "numkeys must be > 0")
		return
	}
	if 2+n > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	count, err := c.eng.KV.ZDiffStore(dest, args[2:2+n])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(count))
}

// ZUNION numkeys key [key ...] [WEIGHTS ...] [AGGREGATE op] [WITHSCORES]
func (c *conn) zunionCmd(args []string) {
	_, keys, weights, agg, withScores, err := parseZSetOpArgs(args, false)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	out, err := c.eng.KV.ZUnion(keys, weights, agg)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

// ZINTER numkeys key [key ...] [WEIGHTS ...] [AGGREGATE op] [WITHSCORES]
func (c *conn) zinterCmd(args []string) {
	_, keys, weights, agg, withScores, err := parseZSetOpArgs(args, false)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	out, err := c.eng.KV.ZInter(keys, weights, agg)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

// ZDIFF numkeys key [key ...] [WITHSCORES]
func (c *conn) zdiffCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'zdiff'")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		writeError(c.bw, "numkeys must be > 0")
		return
	}
	if 1+n > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	withScores := false
	for _, t := range args[1+n:] {
		if strings.EqualFold(t, "WITHSCORES") {
			withScores = true
		}
	}
	out, err := c.eng.KV.ZDiff(args[1 : 1+n])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

// ZINTERCARD numkeys key [key ...] [LIMIT n]
func (c *conn) zintercardCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'zintercard'")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		writeError(c.bw, "numkeys must be > 0")
		return
	}
	if 1+n > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	keys := args[1 : 1+n]
	limit := 0
	for i := 1 + n; i < len(args); i++ {
		if strings.EqualFold(args[i], "LIMIT") && i+1 < len(args) {
			limit, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	count, err := c.eng.KV.ZInterCard(keys, limit)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(count))
}

// ZRANGEBYLEX key min max [LIMIT offset count]
func (c *conn) zrangeByLexCmd(args []string, reverse bool) {
	if !c.wantArgs("ZRANGEBYLEX", args, 3) {
		return
	}
	offset, count := 0, 0
	for i := 3; i < len(args); i++ {
		if strings.EqualFold(args[i], "LIMIT") && i+2 < len(args) {
			offset, _ = strconv.Atoi(args[i+1])
			count, _ = strconv.Atoi(args[i+2])
			i += 2
		}
	}
	minArg, maxArg := args[1], args[2]
	if reverse {
		minArg, maxArg = args[2], args[1]
	}
	out, err := c.eng.KV.ZRangeByLex(args[0], minArg, maxArg, offset, count, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeArray(c.bw, out)
}

// ZLEXCOUNT key min max
func (c *conn) zlexcountCmd(args []string) {
	if !c.wantArgs("ZLEXCOUNT", args, 3) {
		return
	}
	n, err := c.eng.KV.ZLexCount(args[0], args[1], args[2])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ZRANGESTORE dest src start stop [BYSCORE|BYLEX] [REV] [LIMIT off count] [WITHSCORES]
func (c *conn) zrangestoreCmd(args []string) {
	if !c.wantArgs("ZRANGESTORE", args, 4) {
		return
	}
	dest, src, startStr, stopStr := args[0], args[1], args[2], args[3]
	rangeBy := "INDEX"
	reverse := false
	offset, count := 0, 0
	for i := 4; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "BYSCORE":
			rangeBy = "BYSCORE"
		case "BYLEX":
			rangeBy = "BYLEX"
		case "REV":
			reverse = true
		case "LIMIT":
			if i+2 < len(args) {
				offset, _ = strconv.Atoi(args[i+1])
				count, _ = strconv.Atoi(args[i+2])
				i += 2
			}
		}
	}
	n, err := c.eng.KV.ZRangeStore(dest, src, startStr, stopStr, rangeBy, offset, count, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ── multi-key pops ─────────────────────────────────────────────────

// ZMPOP numkeys key [key ...] MIN|MAX [COUNT n]
func (c *conn) zmpopCmd(args []string) {
	if len(args) < 3 {
		writeError(c.bw, "wrong number of arguments for 'zmpop'")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		writeError(c.bw, "numkeys must be > 0")
		return
	}
	if 1+n+1 > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	keys := args[1 : 1+n]
	dir := strings.ToUpper(args[1+n])
	if dir != "MIN" && dir != "MAX" {
		writeError(c.bw, "syntax error: MIN|MAX")
		return
	}
	count := 1
	for i := 2 + n; i < len(args); i++ {
		if strings.EqualFold(args[i], "COUNT") && i+1 < len(args) {
			count, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	key, popped, err := c.eng.KV.ZMPop(keys, dir == "MAX", count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if key == "" {
		writeNilArray(c.bw)
		return
	}
	for range popped {
		c.eng.RecordWrite(zmpopRecordCmd(dir == "MAX"), []string{key})
	}
	flat := make([]any, 0, len(popped))
	for _, p := range popped {
		flat = append(flat, []any{p.Member, strconv.FormatFloat(p.Score, 'f', -1, 64)})
	}
	writeValue(c.bw, []any{key, flat})
}

func zmpopRecordCmd(max bool) string {
	if max {
		return "ZPOPMAX"
	}
	return "ZPOPMIN"
}

// BZMPOP timeout numkeys key [key ...] MIN|MAX [COUNT n]
func (c *conn) bzmpopCmd(args []string) {
	if len(args) < 4 {
		writeError(c.bw, "wrong number of arguments for 'bzmpop'")
		return
	}
	timeoutSec, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		writeError(c.bw, "timeout is not a float")
		return
	}
	timeout := time.Duration(timeoutSec * float64(time.Second))
	rest := args[1:]
	tryPop := func() (string, []store.ZRangeResult, bool) {
		if len(rest) < 2 {
			return "", nil, false
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil || n <= 0 || 1+n+1 > len(rest) {
			return "", nil, false
		}
		keys := rest[1 : 1+n]
		dir := strings.ToUpper(rest[1+n])
		count := 1
		for i := 2 + n; i < len(rest); i++ {
			if strings.EqualFold(rest[i], "COUNT") && i+1 < len(rest) {
				count, _ = strconv.Atoi(rest[i+1])
				i++
			}
		}
		k, popped, _ := c.eng.KV.ZMPop(keys, dir == "MAX", count)
		return k, popped, k != ""
	}
	if k, popped, ok := tryPop(); ok {
		emitBZMPop(c, k, popped)
		return
	}
	// Block on the supplied keys (best-effort key list extraction).
	if len(rest) < 1 {
		writeNilArray(c.bw)
		return
	}
	n, _ := strconv.Atoi(rest[0])
	if n <= 0 || 1+n > len(rest) {
		writeNilArray(c.bw)
		return
	}
	keys := rest[1 : 1+n]
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		w := c.eng.Blocker.Register(keys...)
		if k, popped, ok := tryPop(); ok {
			w.Cancel()
			emitBZMPop(c, k, popped)
			return
		}
		var rem time.Duration
		if !deadline.IsZero() {
			rem = time.Until(deadline)
			if rem <= 0 {
				w.Cancel()
				writeNilArray(c.bw)
				return
			}
		}
		_ = c.bw.Flush()
		_, woke := w.Wait(rem)
		w.Cancel()
		if !woke {
			writeNilArray(c.bw)
			return
		}
	}
}

func emitBZMPop(c *conn, key string, popped []store.ZRangeResult) {
	flat := make([]any, 0, len(popped))
	for _, p := range popped {
		flat = append(flat, []any{p.Member, strconv.FormatFloat(p.Score, 'f', -1, 64)})
	}
	writeValue(c.bw, []any{key, flat})
}

// LMPOP numkeys key [key ...] LEFT|RIGHT [COUNT n]
func (c *conn) lmpopCmd(args []string) {
	if len(args) < 3 {
		writeError(c.bw, "wrong number of arguments for 'lmpop'")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		writeError(c.bw, "numkeys must be > 0")
		return
	}
	if 1+n+1 > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	keys := args[1 : 1+n]
	dir := strings.ToUpper(args[1+n])
	count := 1
	for i := 2 + n; i < len(args); i++ {
		if strings.EqualFold(args[i], "COUNT") && i+1 < len(args) {
			count, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	key, popped, err := c.eng.KV.LMPop(keys, dir == "RIGHT", count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if key == "" {
		writeNilArray(c.bw)
		return
	}
	out := make([]any, len(popped))
	for i, v := range popped {
		out[i] = v
	}
	writeValue(c.bw, []any{key, out})
}

// BLMPOP timeout numkeys key [key ...] LEFT|RIGHT [COUNT n]
func (c *conn) blmpopCmd(args []string) {
	if len(args) < 4 {
		writeError(c.bw, "wrong number of arguments for 'blmpop'")
		return
	}
	timeoutSec, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		writeError(c.bw, "timeout is not a float")
		return
	}
	timeout := time.Duration(timeoutSec * float64(time.Second))
	rest := args[1:]
	tryPop := func() (string, []string, bool) {
		if len(rest) < 2 {
			return "", nil, false
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil || n <= 0 || 1+n+1 > len(rest) {
			return "", nil, false
		}
		keys := rest[1 : 1+n]
		dir := strings.ToUpper(rest[1+n])
		count := 1
		for i := 2 + n; i < len(rest); i++ {
			if strings.EqualFold(rest[i], "COUNT") && i+1 < len(rest) {
				count, _ = strconv.Atoi(rest[i+1])
				i++
			}
		}
		k, popped, _ := c.eng.KV.LMPop(keys, dir == "RIGHT", count)
		return k, popped, k != ""
	}
	if k, popped, ok := tryPop(); ok {
		out := make([]any, len(popped))
		for i, v := range popped {
			out[i] = v
		}
		writeValue(c.bw, []any{k, out})
		return
	}
	n, _ := strconv.Atoi(rest[0])
	if n <= 0 || 1+n > len(rest) {
		writeNilArray(c.bw)
		return
	}
	keys := rest[1 : 1+n]
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		w := c.eng.Blocker.Register(keys...)
		if k, popped, ok := tryPop(); ok {
			w.Cancel()
			out := make([]any, len(popped))
			for i, v := range popped {
				out[i] = v
			}
			writeValue(c.bw, []any{k, out})
			return
		}
		var rem time.Duration
		if !deadline.IsZero() {
			rem = time.Until(deadline)
			if rem <= 0 {
				w.Cancel()
				writeNilArray(c.bw)
				return
			}
		}
		_ = c.bw.Flush()
		_, woke := w.Wait(rem)
		w.Cancel()
		if !woke {
			writeNilArray(c.bw)
			return
		}
	}
}
