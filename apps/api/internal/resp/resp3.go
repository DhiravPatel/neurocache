package resp

import (
	"bufio"
	"math"
	"strconv"
)

// RESP3 wire types layered onto our existing writer. We default to
// RESP2 for back-compat — `HELLO 3` flips a per-conn flag and the
// type-aware helpers below switch their encoding. Commands that don't
// care (PING, SET, …) keep using the original writeSimple/writeBulk
// helpers and produce identical bytes either way.

const (
	tagBlobError   = '!'
	tagBigNumber   = '('
	tagBoolean     = '#'
	tagDouble      = ','
	tagMap         = '%'
	tagSet         = '~'
	tagPush        = '>'
	tagVerbatim    = '='
	tagNull        = '_'
	tagAttribute   = '|'
)

// writeNull writes the protocol-aware nil. RESP2 nil bulk is "$-1\r\n";
// RESP3 nil is "_\r\n". Callers that want a typed nil array use
// writeNilArray which negotiates the same way.
func (c *conn) writeNull() {
	if c.proto >= 3 {
		_, _ = c.bw.WriteString("_\r\n")
		return
	}
	writeNil(c.bw)
}

// writeNullArray — RESP3 has only one null; RESP2 distinguishes
// "$-1\r\n" (nil bulk) from "*-1\r\n" (nil array).
func (c *conn) writeNullArray() {
	if c.proto >= 3 {
		_, _ = c.bw.WriteString("_\r\n")
		return
	}
	writeNilArray(c.bw)
}

// writeBoolean: RESP3 `#t\r\n`/`#f\r\n`; RESP2 falls back to integer
// 0/1, which is what every existing client already expects.
func (c *conn) writeBoolean(b bool) {
	if c.proto >= 3 {
		if b {
			_, _ = c.bw.WriteString("#t\r\n")
		} else {
			_, _ = c.bw.WriteString("#f\r\n")
		}
		return
	}
	if b {
		writeInt(c.bw, 1)
	} else {
		writeInt(c.bw, 0)
	}
}

// writeDouble: RESP3 `,3.14\r\n` with `inf`/`-inf`/`nan` literals.
// RESP2 sends the same value as a bulk string — what ZSCORE uses today.
func (c *conn) writeDouble(f float64) {
	if c.proto >= 3 {
		switch {
		case math.IsNaN(f):
			_, _ = c.bw.WriteString(",nan\r\n")
		case math.IsInf(f, 1):
			_, _ = c.bw.WriteString(",inf\r\n")
		case math.IsInf(f, -1):
			_, _ = c.bw.WriteString(",-inf\r\n")
		default:
			_, _ = c.bw.WriteString("," + strconv.FormatFloat(f, 'g', -1, 64) + "\r\n")
		}
		return
	}
	writeFloat(c.bw, f)
}

// writeBigNumber: RESP3 `(123456789...\r\n`. We accept Go's
// `*big.Int`-style decimal string. RESP2 falls back to a bulk string.
func (c *conn) writeBigNumber(decimal string) {
	if c.proto >= 3 {
		_, _ = c.bw.WriteString("(" + decimal + "\r\n")
		return
	}
	writeBulk(c.bw, decimal)
}

// writeVerbatim: RESP3 `=15\r\ntxt:hello world\r\n`. Format is a
// 3-letter prefix the client honors for rendering. RESP2 sees a bulk.
func (c *conn) writeVerbatim(format, body string) {
	if c.proto >= 3 {
		s := format + ":" + body
		_, _ = c.bw.WriteString("=" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n")
		return
	}
	writeBulk(c.bw, body)
}

// writeMap encodes a key/value map. RESP3 uses `%<n>` with n pairs;
// RESP2 falls back to a flat array of 2n elements (HGETALL shape).
func (c *conn) writeMap(pairs []any) {
	n := len(pairs) / 2
	if c.proto >= 3 {
		_, _ = c.bw.WriteString("%" + strconv.Itoa(n) + "\r\n")
	} else {
		_, _ = c.bw.WriteString("*" + strconv.Itoa(2*n) + "\r\n")
	}
	for _, v := range pairs {
		writeValue(c.bw, v)
	}
}

// writeSet encodes a unique set. RESP3 uses `~<n>`; RESP2 sees an array.
func (c *conn) writeSet(items []any) {
	if c.proto >= 3 {
		_, _ = c.bw.WriteString("~" + strconv.Itoa(len(items)) + "\r\n")
	} else {
		_, _ = c.bw.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
	}
	for _, v := range items {
		writeValue(c.bw, v)
	}
}

// writePush emits an out-of-band push (pub/sub messages, keyspace
// notifications, MONITOR frames). RESP3 uses `>`; RESP2 emits the
// same data as a regular array, which is what existing clients pick
// up via their pub/sub callbacks.
func (c *conn) writePush(items []any) {
	if c.proto >= 3 {
		_, _ = c.bw.WriteString(">" + strconv.Itoa(len(items)) + "\r\n")
	} else {
		_, _ = c.bw.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
	}
	for _, v := range items {
		writeValue(c.bw, v)
	}
}

// silence unused warnings for tags reserved for future use.
var _ = []byte{tagBlobError, tagBigNumber, tagBoolean, tagDouble, tagMap, tagSet, tagPush, tagVerbatim, tagNull, tagAttribute}
var _ = bufio.NewWriter
