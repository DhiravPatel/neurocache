package resp

import (
	"strconv"
	"strings"
	"time"
)

// ── HGETDEL key FIELDS numfields field [field ...] ─────────────────
//
// Atomic read + delete on a set of hash fields. Returns one bulk per
// field — value when present, nil when absent. The hash key itself is
// removed when the last field is deleted.
func (c *conn) hgetdelCmd(args []string) {
	if !c.wantArgs("HGETDEL", args, 4) {
		return
	}
	if !strings.EqualFold(args[1], "FIELDS") {
		writeError(c.bw, "syntax error")
		return
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n <= 0 || 3+n > len(args) {
		writeError(c.bw, "ERR numfields must match the field count")
		return
	}
	fields := args[3 : 3+n]
	values, hits, err := c.eng.KV.HGetDel(args[0], fields)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(fields))
	for i := range fields {
		if hits[i] {
			out[i] = values[i]
		} else {
			out[i] = nil
		}
	}
	writeValue(c.bw, out)
}

// ── HGETEX key [EX sec | PX ms | EXAT ts | PXAT ts | PERSIST]
//          FIELDS numfields field [field ...] ─────────────────────
//
// Read fields and atomically adjust their per-field TTL. The TTL flag
// applies uniformly to every named field that exists.
func (c *conn) hgetexCmd(args []string) {
	if !c.wantArgs("HGETEX", args, 4) {
		return
	}
	mode, value := "", int64(0)
	i := 1
loop:
	for ; i < len(args); i++ {
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
		case "FIELDS":
			break loop
		}
	}
	if i >= len(args) || !strings.EqualFold(args[i], "FIELDS") {
		writeError(c.bw, "syntax error: FIELDS clause required")
		return
	}
	if i+2 >= len(args) {
		writeError(c.bw, "syntax error: FIELDS numfields field [...]")
		return
	}
	n, err := strconv.Atoi(args[i+1])
	if err != nil || n <= 0 || i+2+n > len(args) {
		writeError(c.bw, "ERR numfields must match the field count")
		return
	}
	fields := args[i+2 : i+2+n]
	values, hits, err := c.eng.KV.HGetEx(args[0], fields, mode, value)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(fields))
	for j := range fields {
		if hits[j] {
			out[j] = values[j]
		} else {
			out[j] = nil
		}
	}
	writeValue(c.bw, out)
}

// ── HSETEX key seconds [FNX|FXX] FIELDS numfields field value [...] ──
//
// Atomic set + per-field TTL. The condition (FNX / FXX) is evaluated
// across every named field; the whole call is rejected if any one
// field fails the test (Redis HSETEX atomicity).
func (c *conn) hsetexCmd(args []string) {
	if !c.wantArgs("HSETEX", args, 5) {
		return
	}
	secs, err := strconv.Atoi(args[1])
	if err != nil || secs < 0 {
		writeError(c.bw, "invalid expire time in 'hsetex'")
		return
	}
	cond := ""
	i := 2
	if i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "FNX", "FXX":
			cond = strings.ToUpper(args[i])
			i++
		}
	}
	if i >= len(args) || !strings.EqualFold(args[i], "FIELDS") {
		writeError(c.bw, "syntax error: FIELDS clause required")
		return
	}
	if i+2 >= len(args) {
		writeError(c.bw, "ERR FIELDS numfields field value [...]")
		return
	}
	n, err := strconv.Atoi(args[i+1])
	if err != nil || n <= 0 {
		writeError(c.bw, "ERR numfields must be a positive integer")
		return
	}
	rest := args[i+2:]
	if len(rest) != 2*n {
		writeError(c.bw, "ERR numfields does not match the supplied field/value count")
		return
	}
	res, err := c.eng.KV.HSetEx(args[0], time.Duration(secs)*time.Second, cond, rest)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(res))
}

// ── HEXPIRETIME / HPEXPIRETIME key FIELDS numfields field [field ...] ──
//
// Absolute per-field expiry. -2 = field missing, -1 = field exists
// without TTL, otherwise the Unix timestamp in the requested unit.
func (c *conn) hexpireTimeCmd(args []string, ms bool) {
	cmdName := "HEXPIRETIME"
	if ms {
		cmdName = "HPEXPIRETIME"
	}
	if !c.wantArgs(cmdName, args, 4) {
		return
	}
	if !strings.EqualFold(args[1], "FIELDS") {
		writeError(c.bw, "syntax error: FIELDS clause required")
		return
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n <= 0 || 3+n > len(args) {
		writeError(c.bw, "ERR numfields must match the field count")
		return
	}
	fields := args[3 : 3+n]
	out, err := c.eng.KV.HExpireTime(args[0], fields, ms)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	flat := make([]any, len(out))
	for i, v := range out {
		flat[i] = v
	}
	writeValue(c.bw, flat)
}
