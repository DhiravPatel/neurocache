package resp

// Compatibility fillers — small additive handlers for Redis / Valkey /
// DiceDB commands that real applications rarely call but every official
// driver exposes by default. Keeping them in one file makes it obvious
// which surface exists purely for cross-engine parity.

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/eviction"
)

// brpoplpushCmd implements BRPOPLPUSH source destination timeout.
//
// Deprecated in Redis 6.2 in favour of BLMOVE src dst RIGHT LEFT timeout —
// the two are byte-for-byte equivalent. Drivers built before the rename
// still issue BRPOPLPUSH, so we accept it and rewrite into BLMOVE's
// argument shape rather than duplicating the wait/notify loop.
func (c *conn) brpoplpushCmd(args []string) {
	if len(args) != 3 {
		writeError(c.bw, "wrong number of arguments for 'brpoplpush'")
		return
	}
	c.blmoveCmd([]string{args[0], args[1], "RIGHT", "LEFT", args[2]})
}

// moveCmd implements MOVE key db. NeuroCache exposes a single logical
// database (db 0). MOVE to db 0 with the key already present is a no-op
// returning 0 (target db already has it); MOVE to db 0 from "another
// db" can never happen, so we reject any non-zero target. This matches
// what Cluster-mode Redis does — MOVE is forbidden once cluster is on.
func (c *conn) moveCmd(args []string) {
	if len(args) != 2 {
		writeError(c.bw, "wrong number of arguments for 'move'")
		return
	}
	db, err := strconv.Atoi(args[1])
	if err != nil || db != 0 {
		writeError(c.bw, "ERR DB index is out of range")
		return
	}
	// Source and target db are the same — MOVE is a no-op, key already
	// resides where it would land. Redis returns 0 in that case.
	writeInt(c.bw, 0)
}

// swapdbCmd implements SWAPDB index1 index2. With one logical database
// the only legal call is SWAPDB 0 0.
func (c *conn) swapdbCmd(args []string) {
	if len(args) != 2 {
		writeError(c.bw, "wrong number of arguments for 'swapdb'")
		return
	}
	a, errA := strconv.Atoi(args[0])
	b, errB := strconv.Atoi(args[1])
	if errA != nil || errB != nil || a != 0 || b != 0 {
		writeError(c.bw, "ERR DB index is out of range")
		return
	}
	writeSimple(c.bw, "OK")
}

// evictCmd implements EVICT [key ...] (Valkey 8.0). With no arguments,
// pick one key by the active eviction policy and drop it. With keys,
// drop exactly those keys (DEL semantics) and return the deletion count.
//
// The two-mode design mirrors Valkey's: an operator can preempt the
// memory-pressure scorer (no-arg form) or surgically drop a known set
// (multi-arg form). We reuse the same scorer the background eviction
// loop runs so policy stays consistent.
func (c *conn) evictCmd(args []string) {
	if len(args) > 0 {
		n := c.eng.KV.Del(args...)
		c.eng.RecordWrite("EVICT", args)
		writeInt(c.bw, int64(n))
		return
	}
	if c.eng.Scorer == nil {
		writeInt(c.bw, 0)
		return
	}
	victims := eviction.PickVictims(c.eng.KV.Snapshot(), c.eng.Scorer, 1)
	if len(victims) == 0 {
		writeInt(c.bw, 0)
		return
	}
	n := c.eng.KV.Del(victims...)
	c.eng.RecordWrite("EVICT", victims)
	writeInt(c.bw, int64(n))
}

// pfdebugCmd implements PFDEBUG GETREG <key> | DECODE <key> | TOGET <key>
// — the diagnostic surface real Redis exposes for HyperLogLog internals.
//
// We stay faithful to the contract (returns an array of register values
// for GETREG; "encoding" + register array for DECODE) without reaching
// for the dense/sparse encoding distinction Redis carries — every HLL
// in NeuroCache is dense by construction, so the answer is uniform.
func (c *conn) pfdebugCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'pfdebug'")
		return
	}
	sub := strings.ToUpper(args[0])
	key := args[1]
	regs, ok := c.eng.KV.PFRegisters(key)
	if !ok {
		writeError(c.bw, "ERR The specified key does not exist")
		return
	}
	switch sub {
	case "GETREG":
		out := make([]any, len(regs))
		for i, r := range regs {
			out[i] = int64(r)
		}
		writeValue(c.bw, out)
	case "DECODE":
		// Redis returns a textual decode dump; we approximate with one
		// register per line so the output is greppable and parseable.
		var sb strings.Builder
		for i, r := range regs {
			fmt.Fprintf(&sb, "[%d]=%d\n", i, r)
		}
		writeBulk(c.bw, sb.String())
	case "TOGET":
		// Internal Redis form — return register array as integers.
		out := make([]any, len(regs))
		for i, r := range regs {
			out[i] = int64(r)
		}
		writeValue(c.bw, out)
	case "ENCODING":
		// We always store HLL as the dense fixed-width register layout,
		// so report it directly.
		writeBulk(c.bw, "dense")
	default:
		writeError(c.bw, "Unknown PFDEBUG subcommand")
	}
}

// pfselftestCmd implements PFSELFTEST. The Redis original runs a
// statistical sanity check over its HLL primitives; we synthesize a
// small in-memory HLL, populate it, and confirm the count estimate is
// within tolerance. A pass returns +OK; a fail returns an error.
func (c *conn) pfselftestCmd() {
	// Borrow the public PFAdd/PFCount path via a transient key — that
	// way the test exercises the same code path callers use.
	const probeKey = "__pfselftest_probe__"
	c.eng.KV.Del(probeKey)
	defer c.eng.KV.Del(probeKey)
	for i := 0; i < 1000; i++ {
		_, _ = c.eng.KV.PFAdd(probeKey, "m"+strconv.Itoa(i))
	}
	count, _ := c.eng.KV.PFCount(probeKey)
	// HLL standard error is ~1.04/sqrt(m) — for m=16384 that's ~0.81%.
	// A 5% tolerance keeps the self-test stable across CPU architectures.
	if count < 950 || count > 1050 {
		writeError(c.bw, "PFSELFTEST estimate "+strconv.FormatInt(count, 10)+" outside tolerance")
		return
	}
	writeSimple(c.bw, "OK")
}

// restoreAskingCmd implements RESTORE-ASKING — the cluster-mode variant
// of RESTORE used during slot import. Functionally identical to RESTORE
// but skips the ASKING gate (the importing node is meant to receive
// these even when the slot is in the IMPORTING state).
//
// Our cluster.redirect already accepts RESTORE during import via the
// ASKING flag; RESTORE-ASKING just bypasses the gate explicitly.
func (c *conn) restoreAskingCmd(args []string) {
	c.asking = true
	c.restoreCmd(args)
}

// commandGetKeysAndFlagsReply formats COMMAND GETKEYSANDFLAGS:
// returns each key paired with its access flags (RW / RO / OW / RM).
//
// We don't track granular per-key access at the registry level — every
// key a command touches is reported with the command's primary flag
// derived from the ACL category set (RW for writers, RO for readers).
func commandGetKeysAndFlagsReply(name string, keys []string) []any {
	flags := []any{"RW"}
	if isReadOnlyCommand(name) {
		flags = []any{"RO", "access"}
	} else {
		flags = []any{"RW", "access", "update"}
	}
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, []any{k, flags})
	}
	return out
}

// isReadOnlyCommand reports whether a command name is purely a reader.
// We list the non-write families explicitly so additions to the write
// set don't accidentally flip the default — matches how the writeset
// table is the canonical source of "this mutates state" elsewhere.
func isReadOnlyCommand(name string) bool {
	switch strings.ToUpper(name) {
	case "GET", "GETRANGE", "SUBSTR", "STRLEN", "EXISTS", "TYPE",
		"TTL", "PTTL", "EXPIRETIME", "PEXPIRETIME",
		"HGET", "HMGET", "HGETALL", "HKEYS", "HVALS", "HLEN", "HEXISTS",
		"HSTRLEN", "HRANDFIELD", "HSCAN",
		"LRANGE", "LLEN", "LINDEX", "LPOS",
		"SMEMBERS", "SCARD", "SISMEMBER", "SMISMEMBER", "SINTERCARD",
		"SDIFF", "SINTER", "SUNION", "SRANDMEMBER", "SSCAN",
		"ZRANGE", "ZRANGEBYLEX", "ZRANGEBYSCORE", "ZREVRANGE",
		"ZREVRANGEBYLEX", "ZREVRANGEBYSCORE", "ZRANK", "ZREVRANK",
		"ZSCORE", "ZMSCORE", "ZCARD", "ZCOUNT", "ZLEXCOUNT",
		"ZRANDMEMBER", "ZSCAN", "ZINTERCARD",
		"XLEN", "XRANGE", "XREVRANGE", "XINFO",
		"BITCOUNT", "BITPOS", "GETBIT", "BITFIELD_RO",
		"PFCOUNT", "GEOPOS", "GEODIST", "GEOHASH", "GEOSEARCH",
		"GEORADIUS_RO", "GEORADIUSBYMEMBER_RO",
		"OBJECT", "DEBUG", "DUMP",
		"SORT_RO", "DIGEST",
		"VEMB", "VINFO", "VCARD", "VDIM", "VRANDMEMBER", "VSCAN",
		"VGETATTR", "VLINKS", "VSIM",
		"HOTKEYS":
		return true
	}
	return false
}

// silence unused-import warnings when the build trims helpers.
var _ = runtime.NumCPU
