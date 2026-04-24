package resp

import (
	"strconv"
	"strings"
	"time"
)

// blpopCmd / brpopCmd implement BLPOP / BRPOP. Syntax:
//
//	BLPOP key [key ...] timeout
//
// timeout is in seconds (float, 0 = wait forever). Returns
// [key, value] on success, nil-array on timeout.
func (c *conn) blpopCmd(args []string, fromTail bool) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'blpop'")
		return
	}
	timeoutSec, err := strconv.ParseFloat(args[len(args)-1], 64)
	if err != nil {
		writeError(c.bw, "timeout is not a float or out of range")
		return
	}
	keys := args[:len(args)-1]
	timeout := time.Duration(timeoutSec * float64(time.Second))

	// Fast path: try every key once before blocking.
	if k, v, ok := c.tryListPop(keys, fromTail); ok {
		writeValue(c.bw, []any{k, v})
		return
	}

	// Slow path: register a waiter and re-poll on each notification.
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		w := c.eng.Blocker.Register(keys...)
		// Re-check the keys after registering — a producer might have
		// pushed between the fast-path miss and registration.
		if k, v, ok := c.tryListPop(keys, fromTail); ok {
			w.Cancel()
			writeValue(c.bw, []any{k, v})
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
		// Flush so the client knows we accepted the request before
		// parking — without this, the kernel may hold the reply buffer.
		_ = c.bw.Flush()
		_, woke := w.Wait(remaining)
		w.Cancel()
		if !woke {
			writeNilArray(c.bw)
			return
		}
		// Loop back and try the pop. Another consumer may have raced
		// us and drained the value, in which case we re-block.
	}
}

func (c *conn) tryListPop(keys []string, fromTail bool) (string, string, bool) {
	for _, k := range keys {
		var (
			v   string
			ok  bool
			err error
		)
		if fromTail {
			v, ok, err = c.eng.KV.RPop(k)
		} else {
			v, ok, err = c.eng.KV.LPop(k)
		}
		if err == nil && ok {
			c.eng.RecordWrite(opName(fromTail), []string{k})
			return k, v, true
		}
	}
	return "", "", false
}

func opName(fromTail bool) string {
	if fromTail {
		return "RPOP"
	}
	return "LPOP"
}

// blmoveCmd implements BLMOVE source destination LEFT|RIGHT LEFT|RIGHT timeout.
// On the fast path it acts like LMOVE; otherwise it blocks until source
// becomes non-empty or timeout fires.
func (c *conn) blmoveCmd(args []string) {
	if len(args) < 5 {
		writeError(c.bw, "wrong number of arguments for 'blmove'")
		return
	}
	src, dst := args[0], args[1]
	srcEnd, dstEnd := strings.ToUpper(args[2]), strings.ToUpper(args[3])
	if srcEnd != "LEFT" && srcEnd != "RIGHT" {
		writeError(c.bw, "syntax error")
		return
	}
	if dstEnd != "LEFT" && dstEnd != "RIGHT" {
		writeError(c.bw, "syntax error")
		return
	}
	timeoutSec, err := strconv.ParseFloat(args[4], 64)
	if err != nil {
		writeError(c.bw, "timeout is not a float or out of range")
		return
	}
	timeout := time.Duration(timeoutSec * float64(time.Second))

	if v, ok := c.tryLMove(src, dst, srcEnd == "RIGHT", dstEnd == "RIGHT"); ok {
		writeBulk(c.bw, v)
		return
	}
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		w := c.eng.Blocker.Register(src)
		if v, ok := c.tryLMove(src, dst, srcEnd == "RIGHT", dstEnd == "RIGHT"); ok {
			w.Cancel()
			writeBulk(c.bw, v)
			return
		}
		var remaining time.Duration
		if !deadline.IsZero() {
			remaining = time.Until(deadline)
			if remaining <= 0 {
				w.Cancel()
				writeNil(c.bw)
				return
			}
		}
		_ = c.bw.Flush()
		_, woke := w.Wait(remaining)
		w.Cancel()
		if !woke {
			writeNil(c.bw)
			return
		}
	}
}

// tryLMove pops from one end of src and pushes onto one end of dst.
// Mirrors LMOVE's atomicity by going through the existing RPopLPush
// when the geometry matches; otherwise falls back to a two-step pop
// + push. The store's per-key locking still prevents partial reads.
func (c *conn) tryLMove(src, dst string, srcRight, dstLeft bool) (string, bool) {
	// LMOVE src dst RIGHT LEFT == RPOPLPUSH (already atomic in store).
	if srcRight && dstLeft {
		v, ok, err := c.eng.KV.RPopLPush(src, dst)
		if err != nil || !ok {
			return "", false
		}
		c.eng.RecordWrite("RPOPLPUSH", []string{src, dst})
		return v, true
	}
	var (
		v   string
		ok  bool
		err error
	)
	if srcRight {
		v, ok, err = c.eng.KV.RPop(src)
	} else {
		v, ok, err = c.eng.KV.LPop(src)
	}
	if err != nil || !ok {
		return "", false
	}
	if dstLeft {
		_, _ = c.eng.KV.LPush(dst, v)
	} else {
		_, _ = c.eng.KV.RPush(dst, v)
	}
	c.eng.RecordWrite("LMOVE", []string{src, dst})
	return v, true
}

// bzpopCmd implements BZPOPMIN / BZPOPMAX. Same blocking shape as BLPOP
// but pops from a sorted set and returns [key, member, score].
func (c *conn) bzpopCmd(args []string, max bool) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	timeoutSec, err := strconv.ParseFloat(args[len(args)-1], 64)
	if err != nil {
		writeError(c.bw, "timeout is not a float or out of range")
		return
	}
	keys := args[:len(args)-1]
	timeout := time.Duration(timeoutSec * float64(time.Second))

	if k, m, sc, ok := c.tryZPop(keys, max); ok {
		writeValue(c.bw, []any{k, m, strconv.FormatFloat(sc, 'f', -1, 64)})
		return
	}
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		w := c.eng.Blocker.Register(keys...)
		if k, m, sc, ok := c.tryZPop(keys, max); ok {
			w.Cancel()
			writeValue(c.bw, []any{k, m, strconv.FormatFloat(sc, 'f', -1, 64)})
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
		w.Cancel()
		if !woke {
			writeNilArray(c.bw)
			return
		}
	}
}

func (c *conn) tryZPop(keys []string, max bool) (string, string, float64, bool) {
	for _, k := range keys {
		var (
			m   string
			sc  float64
			ok  bool
			err error
		)
		if max {
			m, sc, ok, err = c.eng.KV.ZPopMax(k)
		} else {
			m, sc, ok, err = c.eng.KV.ZPopMin(k)
		}
		if err == nil && ok {
			c.eng.RecordWrite(zpopName(max), []string{k})
			return k, m, sc, true
		}
	}
	return "", "", 0, false
}

func zpopName(max bool) string {
	if max {
		return "ZPOPMAX"
	}
	return "ZPOPMIN"
}
