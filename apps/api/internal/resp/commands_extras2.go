package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// ── hash field TTLs ────────────────────────────────────────────────

// hexpireCmd handles HEXPIRE / HPEXPIRE / HEXPIREAT / HPEXPIREAT.
// Syntax: <CMD> key seconds [NX|XX|GT|LT] FIELDS numfields field [field ...]
func (c *conn) hexpireCmd(args []string, ms, absolute bool) {
	if len(args) < 5 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	key := args[0]
	num, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		writeError(c.bw, "value is not an integer")
		return
	}
	cond := ""
	i := 2
	switch strings.ToUpper(args[i]) {
	case "NX", "XX", "GT", "LT":
		cond = strings.ToUpper(args[i])
		i++
	}
	if !strings.EqualFold(args[i], "FIELDS") {
		writeError(c.bw, "syntax error: missing FIELDS")
		return
	}
	i++
	n, err := strconv.Atoi(args[i])
	if err != nil || n <= 0 {
		writeError(c.bw, "numfields must be > 0")
		return
	}
	i++
	if i+n > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	fields := args[i : i+n]
	var d time.Duration
	if ms {
		d = time.Duration(num) * time.Millisecond
	} else {
		d = time.Duration(num) * time.Second
	}
	var results []int
	if absolute {
		var t time.Time
		if ms {
			t = time.UnixMilli(num)
		} else {
			t = time.Unix(num, 0)
		}
		results, err = c.eng.KV.HExpireAt(key, t, fields, cond)
	} else {
		results, err = c.eng.KV.HExpire(key, d, fields, cond)
	}
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(results))
	for j, r := range results {
		out[j] = int64(r)
	}
	writeValue(c.bw, out)
}

// httlCmd handles HTTL / HPTTL — same arg shape as HEXPIRE without
// the timestamp.
func (c *conn) httlCmd(args []string, ms bool) {
	if len(args) < 4 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	key := args[0]
	if !strings.EqualFold(args[1], "FIELDS") {
		writeError(c.bw, "syntax error: missing FIELDS")
		return
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n <= 0 {
		writeError(c.bw, "numfields must be > 0")
		return
	}
	if 3+n > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	fields := args[3 : 3+n]
	results, err := c.eng.KV.HTTL(key, fields, ms)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = r
	}
	writeValue(c.bw, out)
}

// hpersistCmd: HPERSIST key FIELDS numfields field [field ...]
func (c *conn) hpersistCmd(args []string) {
	if len(args) < 4 {
		writeError(c.bw, "wrong number of arguments for 'hpersist'")
		return
	}
	key := args[0]
	if !strings.EqualFold(args[1], "FIELDS") {
		writeError(c.bw, "syntax error")
		return
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n <= 0 {
		writeError(c.bw, "numfields must be > 0")
		return
	}
	if 3+n > len(args) {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	results, err := c.eng.KV.HPersist(key, args[3:3+n])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = int64(r)
	}
	writeValue(c.bw, out)
}

// hrandfieldCmd: HRANDFIELD key [count [WITHVALUES]]
func (c *conn) hrandfieldCmd(args []string) {
	if !c.wantArgs("HRANDFIELD", args, 1) {
		return
	}
	if len(args) == 1 {
		// Single-field form — no WITHVALUES.
		fields, _, err := c.eng.KV.HRandFieldCount(args[0], 1, false)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if len(fields) == 0 {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, fields[0])
		return
	}
	count, err := strconv.Atoi(args[1])
	if err != nil {
		writeError(c.bw, "value is not an integer")
		return
	}
	withValues := len(args) >= 3 && strings.EqualFold(args[2], "WITHVALUES")
	fields, vals, err := c.eng.KV.HRandFieldCount(args[0], count, withValues)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !withValues {
		out := make([]any, len(fields))
		for i, f := range fields {
			out[i] = f
		}
		writeValue(c.bw, out)
		return
	}
	out := make([]any, 0, len(fields)*2)
	for i, f := range fields {
		out = append(out, f, vals[i])
	}
	writeValue(c.bw, out)
}

// ── LCS ────────────────────────────────────────────────────────────

// lcsCmd: LCS key1 key2 [LEN | IDX [MINMATCHLEN n] [WITHMATCHLEN]]
func (c *conn) lcsCmd(args []string) {
	if !c.wantArgs("LCS", args, 2) {
		return
	}
	mode := "string"
	minMatch := 0
	withMatchLen := false
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "LEN":
			mode = "len"
		case "IDX":
			mode = "idx"
		case "MINMATCHLEN":
			if i+1 < len(args) {
				minMatch, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "WITHMATCHLEN":
			withMatchLen = true
		}
	}
	str, length, matches, err := c.eng.KV.LCS(args[0], args[1], mode, minMatch)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	switch mode {
	case "len":
		writeInt(c.bw, int64(length))
	case "idx":
		flat := []any{}
		for _, m := range matches {
			match := []any{
				[]any{int64(m.StartA), int64(m.EndA)},
				[]any{int64(m.StartB), int64(m.EndB)},
			}
			if withMatchLen {
				match = append(match, int64(m.Length))
			}
			flat = append(flat, match)
		}
		writeValue(c.bw, []any{"matches", flat, "len", int64(length)})
	default:
		writeBulk(c.bw, str)
	}
}

// ── BITFIELD / BITFIELD_RO ─────────────────────────────────────────

func (c *conn) bitfieldCmd(args []string, readOnly bool) {
	if !c.wantArgs("BITFIELD", args, 1) {
		return
	}
	key := args[0]
	ops, err := parseBitFieldOps(args[1:])
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	out, err := c.eng.KV.BitField(key, ops, readOnly)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, out)
}

func parseBitFieldOps(args []string) ([]store.BitFieldOp, error) {
	out := []store.BitFieldOp{}
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "GET":
			if i+2 >= len(args) {
				return nil, errSyntax{msg: "GET needs type + offset"}
			}
			out = append(out, store.BitFieldOp{Op: "GET", Type: args[i+1], Offset: args[i+2]})
			i += 2
		case "SET":
			if i+3 >= len(args) {
				return nil, errSyntax{msg: "SET needs type + offset + value"}
			}
			v, _ := strconv.ParseInt(args[i+3], 10, 64)
			out = append(out, store.BitFieldOp{Op: "SET", Type: args[i+1], Offset: args[i+2], Value: v})
			i += 3
		case "INCRBY":
			if i+3 >= len(args) {
				return nil, errSyntax{msg: "INCRBY needs type + offset + delta"}
			}
			v, _ := strconv.ParseInt(args[i+3], 10, 64)
			out = append(out, store.BitFieldOp{Op: "INCRBY", Type: args[i+1], Offset: args[i+2], Value: v})
			i += 3
		case "OVERFLOW":
			if i+1 >= len(args) {
				return nil, errSyntax{msg: "OVERFLOW needs WRAP | SAT | FAIL"}
			}
			out = append(out, store.BitFieldOp{Op: "OVERFLOW", Overflow: args[i+1]})
			i++
		}
	}
	return out, nil
}

// ── SORT / SORT_RO ─────────────────────────────────────────────────

// sortCmd parses the full SORT surface:
//
//   SORT key [BY pattern] [LIMIT offset count] [GET pattern [GET ...]]
//        [ASC | DESC] [ALPHA] [STORE dest]
func (c *conn) sortCmd(args []string, readOnly bool) {
	if !c.wantArgs("SORT", args, 1) {
		return
	}
	key := args[0]
	opts := store.SortOpts{Limit: [2]int{0, -1}, Order: "ASC"}
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "BY":
			if i+1 < len(args) {
				opts.By = args[i+1]
				i++
			}
		case "LIMIT":
			if i+2 < len(args) {
				opts.Limit[0], _ = strconv.Atoi(args[i+1])
				opts.Limit[1], _ = strconv.Atoi(args[i+2])
				i += 2
			}
		case "GET":
			if i+1 < len(args) {
				opts.Get = append(opts.Get, args[i+1])
				i++
			}
		case "ASC":
			opts.Order = "ASC"
		case "DESC":
			opts.Order = "DESC"
		case "ALPHA":
			opts.Alpha = true
		case "STORE":
			if readOnly {
				writeError(c.bw, "SORT_RO is read-only")
				return
			}
			if i+1 < len(args) {
				opts.Store = args[i+1]
				i++
			}
		}
	}
	out, err := c.eng.KV.Sort(key, opts)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if opts.Store != "" {
		// SORT ... STORE returns the destination's length.
		writeInt(c.bw, int64(len(out)))
		return
	}
	writeArray(c.bw, out)
}

// ── XSETID + XADD MINID/NOMKSTREAM ─────────────────────────────────
//
// XSETID is a thin wrapper on the existing stream — we just expose
// it via the dispatcher. XADD already accepts MAXLEN; we extend the
// parser in commands.go to also accept NOMKSTREAM and MINID.
//
// The store-side support already exists implicitly: NOMKSTREAM is
// honoured by checking for key existence before XAdd, and MINID is
// the same trim-by-id logic XADD MAXLEN uses but bounded by id.

// ── CLUSTER LINKS ──────────────────────────────────────────────────

func (c *conn) clusterLinksCmd() {
	if c.eng.Cluster == nil {
		writeError(c.bw, "ERR cluster support disabled")
		return
	}
	rows := []any{}
	myself := c.eng.Cluster.Myself()
	for _, n := range c.eng.Cluster.Nodes() {
		if myself != nil && n.ID == myself.ID {
			continue
		}
		rows = append(rows, []any{
			"direction", "to",
			"node", n.ID,
			"create-time", int64(time.Now().Unix()),
			"events", "rw",
			"send-buffer-allocated", int64(0),
			"send-buffer-used", int64(0),
		})
	}
	writeValue(c.bw, rows)
}

// ── stub for the WAITAOF command ───────────────────────────────────

// WAITAOF numlocal numreplicas timeout-ms — wait until the local AOF
// has fsynced numlocal copies + numreplicas replica AOFs have caught up.
// We treat fsync-everysec as "always satisfied within 1s" and just
// reply with the actual figures, matching the contract.
func (c *conn) waitaofCmd(args []string) {
	if !c.wantArgs("WAITAOF", args, 3) {
		return
	}
	numLocal, _ := strconv.Atoi(args[0])
	numReplicas, _ := strconv.Atoi(args[1])
	timeoutMs, _ := strconv.Atoi(args[2])
	deadline := time.Time{}
	if timeoutMs > 0 {
		deadline = time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	}
	for {
		localOK := 0
		if c.eng.AOF != nil {
			localOK = 1
		}
		replicaOK := c.eng.Replication.ConnectedReplicasAtOffset(c.eng.Replication.Offset())
		if localOK >= numLocal && replicaOK >= numReplicas {
			writeValue(c.bw, []any{int64(localOK), int64(replicaOK)})
			return
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			writeValue(c.bw, []any{int64(localOK), int64(replicaOK)})
			return
		}
		_ = c.bw.Flush()
		time.Sleep(50 * time.Millisecond)
	}
}
