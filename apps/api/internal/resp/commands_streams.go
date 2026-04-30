package resp

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// xgroupCmd implements XGROUP CREATE | SETID | DESTROY | CREATECONSUMER |
// DELCONSUMER. The store does the heavy lifting; this layer just parses
// and formats.
func (c *conn) xgroupCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'xgroup'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "CREATE":
		// XGROUP CREATE key group id [MKSTREAM]
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'xgroup|create'")
			return
		}
		mkstream := len(args) >= 5 && strings.EqualFold(args[4], "MKSTREAM")
		if err := c.eng.KV.XGroupCreate(args[1], args[2], args[3], mkstream); err != nil {
			c.writeStoreErr(err)
			return
		}
		c.eng.RecordWrite("XGROUP", args)
		writeSimple(c.bw, "OK")
	case "SETID":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'xgroup|setid'")
			return
		}
		if err := c.eng.KV.XGroupSetID(args[1], args[2], args[3]); err != nil {
			c.writeStoreErr(err)
			return
		}
		c.eng.RecordWrite("XGROUP", args)
		writeSimple(c.bw, "OK")
	case "DESTROY":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'xgroup|destroy'")
			return
		}
		ok, err := c.eng.KV.XGroupDestroy(args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		c.eng.RecordWrite("XGROUP", args)
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "CREATECONSUMER":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'xgroup|createconsumer'")
			return
		}
		n, err := c.eng.KV.XGroupCreateConsumer(args[1], args[2], args[3])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		c.eng.RecordWrite("XGROUP", args)
		writeInt(c.bw, int64(n))
	case "DELCONSUMER":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'xgroup|delconsumer'")
			return
		}
		n, err := c.eng.KV.XGroupDelConsumer(args[1], args[2], args[3])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		c.eng.RecordWrite("XGROUP", args)
		writeInt(c.bw, int64(n))
	default:
		writeError(c.bw, "unknown XGROUP subcommand "+args[0])
	}
}

// xreadgroupCmd implements XREADGROUP GROUP <g> <c> [COUNT n] [BLOCK ms]
// [NOACK] STREAMS key [key ...] id [id ...]. id == ">" pulls new
// entries; any other id replays that consumer's PEL from there.
func (c *conn) xreadgroupCmd(args []string) {
	if len(args) < 6 {
		writeError(c.bw, "wrong number of arguments for 'xreadgroup'")
		return
	}
	if !strings.EqualFold(args[0], "GROUP") {
		writeError(c.bw, "syntax error: missing GROUP")
		return
	}
	group, consumer := args[1], args[2]
	count, block := 0, time.Duration(-1)
	noack := false
	i := 3
	for ; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "BLOCK":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			ms, _ := strconv.Atoi(args[i+1])
			block = time.Duration(ms) * time.Millisecond
			i++
		case "NOACK":
			noack = true
		case "STREAMS":
			i++
			goto streams
		}
	}
streams:
	rest := args[i:]
	if len(rest) == 0 || len(rest)%2 != 0 {
		writeError(c.bw, "Unbalanced XREADGROUP STREAMS keys and IDs")
		return
	}
	half := len(rest) / 2
	keys, ids := rest[:half], rest[half:]

	// Fast path: try once.
	out, err := c.eng.KV.XReadGroup(group, consumer, keys, ids, count, noack)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if hasEntries(out) || block < 0 {
		writeXReadResult(c.bw, keys, out)
		return
	}
	// Block until one of the keys gets a new entry, or timeout fires.
	deadline := time.Time{}
	if block > 0 {
		deadline = time.Now().Add(block)
	}
	for {
		w := c.eng.Blocker.RegisterFor(c.info.ID, keys...)
		out, err = c.eng.KV.XReadGroup(group, consumer, keys, ids, count, noack)
		if err != nil {
			w.Cancel()
			c.writeStoreErr(err)
			return
		}
		if hasEntries(out) {
			w.Cancel()
			writeXReadResult(c.bw, keys, out)
			return
		}
		var remaining time.Duration
		if !deadline.IsZero() {
			remaining = time.Until(deadline)
			if remaining <= 0 {
				w.Cancel()
				writeNilArray(c.bw)
				return
			}
		}
		_ = c.bw.Flush()
		_, woke := w.Wait(remaining)
		external := w.UnblockedExternal()
		errored := w.UnblockedByError()
		w.Cancel()
		if !woke {
			writeNilArray(c.bw)
			return
		}
		if external {
			if errored {
				writeTypedError(c.bw, "UNBLOCKED", "client unblocked via CLIENT UNBLOCK")
				return
			}
			writeNilArray(c.bw)
			return
		}
	}
}

func hasEntries(m map[string][]store.StreamEntry) bool {
	for _, v := range m {
		if len(v) > 0 {
			return true
		}
	}
	return false
}

// xackCmd implements XACK key group id [id ...].
func (c *conn) xackCmd(args []string) {
	if len(args) < 3 {
		writeError(c.bw, "wrong number of arguments for 'xack'")
		return
	}
	n, err := c.eng.KV.XAck(args[0], args[1], args[2:])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	c.eng.RecordWrite("XACK", args)
	writeInt(c.bw, int64(n))
}

// xpendingCmd implements both forms:
//
//	XPENDING key group                                   (summary)
//	XPENDING key group [IDLE ms] start end count [consumer]
func (c *conn) xpendingCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'xpending'")
		return
	}
	key, group := args[0], args[1]
	if len(args) == 2 {
		out, err := c.eng.KV.XPending(key, group, true, "-", "+", 0, "")
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		c.writePendingSummary(out.(store.PendingSummary))
		return
	}
	i := 2
	if strings.EqualFold(args[i], "IDLE") {
		i += 2 // ignore IDLE filter for now (Redis introduced ms filter; harmless to skip)
	}
	if len(args) < i+3 {
		writeError(c.bw, "syntax error")
		return
	}
	start, end := args[i], args[i+1]
	count, _ := strconv.Atoi(args[i+2])
	consumer := ""
	if len(args) > i+3 {
		consumer = args[i+3]
	}
	out, err := c.eng.KV.XPending(key, group, false, start, end, count, consumer)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	c.writePendingDetail(out.([]store.PendingDetail))
}

func (c *conn) writePendingSummary(p store.PendingSummary) {
	fmt.Fprintf(c.bw, "*4\r\n")
	writeInt(c.bw, p.Count)
	if p.MinID == "" {
		writeNil(c.bw)
	} else {
		writeBulk(c.bw, p.MinID)
	}
	if p.MaxID == "" {
		writeNil(c.bw)
	} else {
		writeBulk(c.bw, p.MaxID)
	}
	if len(p.Consumers) == 0 {
		writeNilArray(c.bw)
		return
	}
	fmt.Fprintf(c.bw, "*%d\r\n", len(p.Consumers))
	for _, row := range p.Consumers {
		writeArray(c.bw, []string{row[0], row[1]})
	}
}

func (c *conn) writePendingDetail(rows []store.PendingDetail) {
	fmt.Fprintf(c.bw, "*%d\r\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(c.bw, "*4\r\n")
		writeBulk(c.bw, r.ID)
		writeBulk(c.bw, r.Consumer)
		writeInt(c.bw, r.IdleMs)
		writeInt(c.bw, r.DeliveryCount)
	}
}

// xclaimCmd implements XCLAIM key group consumer min-idle-ms id [id ...]
// [IDLE ms] [TIME ms-unix] [RETRYCOUNT n] [FORCE] [JUSTID]
func (c *conn) xclaimCmd(args []string) {
	if len(args) < 5 {
		writeError(c.bw, "wrong number of arguments for 'xclaim'")
		return
	}
	key, group, consumer := args[0], args[1], args[2]
	minIdle, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		writeError(c.bw, "min-idle-ms is not an integer")
		return
	}
	ids := []string{}
	opts := store.XClaimOpts{}
	i := 4
	for ; i < len(args); i++ {
		// IDs precede the option keywords; once we see one, switch.
		switch strings.ToUpper(args[i]) {
		case "IDLE":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			opts.IdleMs, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "TIME":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			opts.Time, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "RETRYCOUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			opts.Retry, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "FORCE":
			opts.Force = true
		case "JUSTID":
			opts.JustIDs = true
		default:
			ids = append(ids, args[i])
		}
	}
	entries, justIDs, err := c.eng.KV.XClaim(key, group, consumer, minIdle, ids, opts)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	c.eng.RecordWrite("XCLAIM", args)
	if opts.JustIDs {
		writeArray(c.bw, justIDs)
		return
	}
	writeStreamEntries(c.bw, entries)
}

// xautoclaimCmd implements XAUTOCLAIM key group consumer min-idle-ms
// start [COUNT n] [JUSTID]
func (c *conn) xautoclaimCmd(args []string) {
	if len(args) < 5 {
		writeError(c.bw, "wrong number of arguments for 'xautoclaim'")
		return
	}
	key, group, consumer := args[0], args[1], args[2]
	minIdle, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		writeError(c.bw, "min-idle-ms is not an integer")
		return
	}
	start := args[4]
	count := 100
	justIDs := false
	for i := 5; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "JUSTID":
			justIDs = true
		}
	}
	entries, justs, cursor, deleted, err := c.eng.KV.XAutoClaim(key, group, consumer, minIdle, start, count, justIDs)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	c.eng.RecordWrite("XAUTOCLAIM", args)
	// Reply: [cursor, claimed-entries-or-ids, deleted-ids]
	fmt.Fprintf(c.bw, "*3\r\n")
	writeBulk(c.bw, cursor)
	if justIDs {
		writeArray(c.bw, justs)
	} else {
		writeStreamEntries(c.bw, entries)
	}
	writeArray(c.bw, deleted)
}

// xinfoCmd implements XINFO STREAM | GROUPS | CONSUMERS.
func (c *conn) xinfoCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'xinfo'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "STREAM":
		// XINFO STREAM key [FULL [COUNT n]] — when FULL is given, we
		// emit the standard flat map plus a per-group + per-consumer
		// breakdown including the PEL contents.
		full := len(args) >= 3 && strings.EqualFold(args[2], "FULL")
		out, err := c.eng.KV.XInfoStream(args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !full {
			writeFlatMap(c.bw, out)
			return
		}
		groups, err := c.eng.KV.XInfoGroups(args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		groupRows := []any{}
		for _, g := range groups {
			row := g.([]any)
			// group rows already carry name/consumers/pending/last-delivered-id;
			// expand with per-consumer detail under the same key.
			var groupName string
			for i := 0; i < len(row); i += 2 {
				if k, ok := row[i].(string); ok && k == "name" && i+1 < len(row) {
					groupName, _ = row[i+1].(string)
					break
				}
			}
			consumers, _ := c.eng.KV.XInfoConsumers(args[1], groupName)
			row = append(row, "consumers-detail", consumers)
			groupRows = append(groupRows, row)
		}
		out = append(out, "groups-detail", groupRows)
		writeFlatMap(c.bw, out)
	case "GROUPS":
		out, err := c.eng.KV.XInfoGroups(args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		fmt.Fprintf(c.bw, "*%d\r\n", len(out))
		for _, row := range out {
			writeFlatMap(c.bw, row.([]any))
		}
	case "CONSUMERS":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'xinfo|consumers'")
			return
		}
		out, err := c.eng.KV.XInfoConsumers(args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		fmt.Fprintf(c.bw, "*%d\r\n", len(out))
		for _, row := range out {
			writeFlatMap(c.bw, row.([]any))
		}
	default:
		writeError(c.bw, "unknown XINFO subcommand "+args[0])
	}
}

// writeFlatMap encodes [k1, v1, k2, v2, ...] as a RESP array, picking
// the right writer per value kind.
func writeFlatMap(w *bufio.Writer, kv []any) {
	fmt.Fprintf(w, "*%d\r\n", len(kv))
	for _, v := range kv {
		writeValue(w, v)
	}
}
