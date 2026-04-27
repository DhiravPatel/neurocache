package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// ── DELEX key value ──────────────────────────────────────────────
//
// Compare-and-delete on a string key. Returns:
//
//   1  — value matched, key removed
//   0  — value did not match (no-op) OR wrong type
//  -1  — key did not exist
//
// The CAS makes "delete only if I still own this lease" patterns safe
// without a Lua script. WRONGTYPE on non-string keys is reported as a
// proper -ERR rather than 0 so callers don't silently miss the bug.
func (c *conn) delexCmd(args []string) {
	if !c.wantArgs("DELEX", args, 2) {
		return
	}
	n, err := c.eng.KV.DelEx(args[0], args[1])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ── DIGEST key [key ...] ─────────────────────────────────────────
//
// 40-char hex SHA1 of each key's content. Missing keys come back as
// nil so callers can MGET-style probe an arbitrary list. The hash is
// stable across insertion order for collections, so two keys with the
// same logical content always produce the same digest.
func (c *conn) digestCmd(args []string) {
	if !c.wantArgs("DIGEST", args, 1) {
		return
	}
	out := make([]any, len(args))
	for i, k := range args {
		d, ok, err := c.eng.KV.Digest(k)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			out[i] = nil
		} else {
			out[i] = d
		}
	}
	writeValue(c.bw, out)
}

// ── MSETEX seconds key value [key value ...] ─────────────────────
//
// Atomic multi-set with a shared TTL. Differs from MSET in that every
// key gets the supplied expiry, and from SETEX in that it batches.
// Either every pair lands with the TTL or none do.
func (c *conn) msetexCmd(args []string) {
	if !c.wantArgs("MSETEX", args, 3) {
		return
	}
	secs, err := strconv.Atoi(args[0])
	if err != nil || secs <= 0 {
		writeError(c.bw, "invalid expire time in 'msetex'")
		return
	}
	rest := args[1:]
	if len(rest)%2 != 0 {
		writeError(c.bw, "wrong number of arguments for MSETEX")
		return
	}
	if err := c.eng.KV.MSetEx(time.Duration(secs)*time.Second, rest...); err != nil {
		c.writeStoreErr(err)
		return
	}
	writeSimple(c.bw, "OK")
}

// ── XACKDEL key group id [id ...] ────────────────────────────────
//
// Atomic ACK + DEL. Returns the count of entries that were both
// pending in the group AND removed from the stream. IDs not in the
// group's PEL are silently skipped — same convention as XACK.
func (c *conn) xackdelCmd(args []string) {
	if !c.wantArgs("XACKDEL", args, 3) {
		return
	}
	n, err := c.eng.KV.XAckDel(args[0], args[1], args[2:]...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ── XDELEX key [REF|KEEPREF|ACKED] id [id ...] ───────────────────
//
// Reference-aware XDEL. Default mode is KEEPREF (classic XDEL —
// removes regardless of PEL state). REF refuses to delete entries
// still pending in any group. ACKED only removes entries no group
// still references.
func (c *conn) xdelexCmd(args []string) {
	if !c.wantArgs("XDELEX", args, 2) {
		return
	}
	mode := store.XDelExKeepRef
	rest := args[1:]
	if len(rest) > 0 {
		switch strings.ToUpper(rest[0]) {
		case "KEEPREF", "REF", "ACKED":
			parsed, err := store.ParseXDelExMode(rest[0])
			if err != nil {
				writeError(c.bw, err.Error())
				return
			}
			mode = parsed
			rest = rest[1:]
		}
	}
	if len(rest) == 0 {
		writeError(c.bw, "wrong number of arguments for 'xdelex'")
		return
	}
	n, err := c.eng.KV.XDelEx(args[0], mode, rest...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// ── XCFGSET key group MAXDELIVERIES n | MINIDLE ms ───────────────
//
// Per-group runtime config setter. Multiple knob/value pairs may
// appear; unknown knobs return -ERR rather than silently ignored.
// Reply is a flat [k v k v ...] array reflecting the post-change
// values so callers can confirm the apply.
func (c *conn) xcfgsetCmd(args []string) {
	if !c.wantArgs("XCFGSET", args, 4) {
		return
	}
	key, group := args[0], args[1]
	cfg := store.GroupConfig{MaxDeliveries: -1, MinIdleMs: -1}
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "MAXDELIVERIES":
			if i+1 >= len(args) {
				writeError(c.bw, "MAXDELIVERIES requires a value")
				return
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || v < 0 {
				writeError(c.bw, "MAXDELIVERIES must be a non-negative integer")
				return
			}
			cfg.MaxDeliveries = v
			i++
		case "MINIDLE":
			if i+1 >= len(args) {
				writeError(c.bw, "MINIDLE requires a value")
				return
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || v < 0 {
				writeError(c.bw, "MINIDLE must be a non-negative integer")
				return
			}
			cfg.MinIdleMs = v
			i++
		default:
			writeError(c.bw, "Unknown XCFGSET option: "+args[i])
			return
		}
	}
	out, err := c.eng.KV.XCfgSet(key, group, cfg)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{
		"max-deliveries", out.MaxDeliveries,
		"min-idle-ms", out.MinIdleMs,
	})
}
