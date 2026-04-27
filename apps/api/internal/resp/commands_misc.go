package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/blocking"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// touchCmd implements TOUCH key [key ...] — alters last-access time
// for each existing key without returning the value, so a hot key can
// be marked recently used. Returns the count of keys touched.
func (c *conn) touchCmd(args []string) {
	if !c.wantArgs("TOUCH", args, 1) {
		return
	}
	writeInt(c.bw, int64(c.eng.KV.Touch(args...)))
}

// expireTimeCmd implements EXPIRETIME key — returns the absolute expiry
// in Unix seconds, -1 when no TTL, -2 when missing. Mirrors Redis 7.0.
func (c *conn) expireTimeCmd(args []string) {
	if !c.wantArgs("EXPIRETIME", args, 1) {
		return
	}
	writeInt(c.bw, c.eng.KV.ExpireTime(args[0]))
}

// pexpireTimeCmd is EXPIRETIME in milliseconds.
func (c *conn) pexpireTimeCmd(args []string) {
	if !c.wantArgs("PEXPIRETIME", args, 1) {
		return
	}
	writeInt(c.bw, c.eng.KV.PExpireTime(args[0]))
}

// zmscoreCmd implements ZMSCORE key member [member ...] — returns one
// reply per member: the score as bulk string, or nil for absent members.
func (c *conn) zmscoreCmd(args []string) {
	if !c.wantArgs("ZMSCORE", args, 2) {
		return
	}
	scores, hits, err := c.eng.KV.ZMScore(args[0], args[1:]...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(hits))
	for i, h := range hits {
		if !h {
			out[i] = nil
			continue
		}
		out[i] = strconv.FormatFloat(scores[i], 'f', -1, 64)
	}
	writeValue(c.bw, out)
}

// zrandmemberCmd implements ZRANDMEMBER key [count [WITHSCORES]].
//
//   - no count       → bulk string (one random member) or nil
//   - count > 0      → array of unique members
//   - count < 0      → array of |count| members, may repeat
//   - WITHSCORES     → flat [m1, s1, m2, s2, ...] in the array form
func (c *conn) zrandmemberCmd(args []string) {
	if !c.wantArgs("ZRANDMEMBER", args, 1) {
		return
	}
	withScores := false
	hasCount := false
	count := 0
	if len(args) >= 2 {
		hasCount = true
		v, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		count = v
		if len(args) >= 3 && strings.EqualFold(args[2], "WITHSCORES") {
			withScores = true
		}
	}
	if !hasCount {
		// single-member form — pass 0 as the dispatcher's "no count"
		// sentinel into the store helper.
		members, _, ok, err := c.eng.KV.ZRandMember(args[0], 0, false)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok || len(members) == 0 {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, members[0])
		return
	}
	members, scores, ok, err := c.eng.KV.ZRandMember(args[0], count, withScores)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok || len(members) == 0 {
		writeArray(c.bw, []string{})
		return
	}
	if !withScores {
		writeArray(c.bw, members)
		return
	}
	out := make([]any, 0, len(members)*2)
	for i, m := range members {
		out = append(out, m, strconv.FormatFloat(scores[i], 'f', -1, 64))
	}
	writeValue(c.bw, out)
}

// zremrangebyrankCmd implements ZREMRANGEBYRANK key start stop.
func (c *conn) zremrangebyrankCmd(args []string) {
	if !c.wantArgs("ZREMRANGEBYRANK", args, 3) {
		return
	}
	start, err := strconv.Atoi(args[1])
	if err != nil {
		writeError(c.bw, "value is not an integer or out of range")
		return
	}
	stop, err := strconv.Atoi(args[2])
	if err != nil {
		writeError(c.bw, "value is not an integer or out of range")
		return
	}
	n, err := c.eng.KV.ZRemRangeByRank(args[0], start, stop)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// zremrangebyscoreCmd implements ZREMRANGEBYSCORE key min max.
func (c *conn) zremrangebyscoreCmd(args []string) {
	if !c.wantArgs("ZREMRANGEBYSCORE", args, 3) {
		return
	}
	n, err := c.eng.KV.ZRemRangeByScore(args[0], args[1], args[2])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// zremrangebylexCmd implements ZREMRANGEBYLEX key min max.
func (c *conn) zremrangebylexCmd(args []string) {
	if !c.wantArgs("ZREMRANGEBYLEX", args, 3) {
		return
	}
	n, err := c.eng.KV.ZRemRangeByLex(args[0], args[1], args[2])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// lmoveCmd implements LMOVE source destination LEFT|RIGHT LEFT|RIGHT.
// Returns the moved element, nil when source is empty/missing.
func (c *conn) lmoveCmd(args []string) {
	if !c.wantArgs("LMOVE", args, 4) {
		return
	}
	srcEnd := strings.ToUpper(args[2])
	dstEnd := strings.ToUpper(args[3])
	if (srcEnd != "LEFT" && srcEnd != "RIGHT") || (dstEnd != "LEFT" && dstEnd != "RIGHT") {
		writeError(c.bw, "syntax error")
		return
	}
	v, ok, err := c.eng.KV.LMove(args[0], args[1], srcEnd == "RIGHT", dstEnd == "RIGHT")
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

// geosearchstoreCmd implements GEOSEARCHSTORE dest src ...search-args
// [STOREDIST]. Layout matches GEOSEARCH after the leading dest/src
// pair. Returns the resulting destination cardinality.
func (c *conn) geosearchstoreCmd(args []string) {
	if !c.wantArgs("GEOSEARCHSTORE", args, 8) {
		return
	}
	dest := args[0]
	rest := args[1:]
	storeDist := false
	for i, a := range rest {
		if strings.EqualFold(a, "STOREDIST") {
			rest = append(append([]string{}, rest[:i]...), rest[i+1:]...)
			storeDist = true
			break
		}
	}
	src, lat, lon, radius, unit, count, err := parseGeoSearchArgs(rest)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	n, err := c.eng.KV.GeoSearchStore(dest, src, lat, lon, radius, unit, count, storeDist)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// parseGeoSearchArgs parses the shared GEOSEARCH / GEOSEARCHSTORE
// payload (after the leading source key). Returns
// (src, lat, lon, radius, unit, count, err). Only FROMLONLAT +
// BYRADIUS are honoured today — matching the existing GEOSEARCH path.
func parseGeoSearchArgs(args []string) (src string, lat, lon, radius float64, unit string, count int, err error) {
	if len(args) < 1 {
		err = wrap("syntax error: missing source key")
		return
	}
	src = args[0]
	unit = "m"
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "FROMLONLAT":
			if i+2 >= len(args) {
				err = wrap("syntax error: FROMLONLAT lon lat")
				return
			}
			lon, _ = strconv.ParseFloat(args[i+1], 64)
			lat, _ = strconv.ParseFloat(args[i+2], 64)
			i += 2
		case "BYRADIUS":
			if i+2 >= len(args) {
				err = wrap("syntax error: BYRADIUS radius unit")
				return
			}
			radius, _ = strconv.ParseFloat(args[i+1], 64)
			unit = strings.ToLower(args[i+2])
			i += 2
		case "COUNT":
			if i+1 >= len(args) {
				err = wrap("syntax error: COUNT n")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "ASC", "DESC", "WITHCOORD", "WITHDIST", "WITHHASH":
			// store path doesn't surface these — accept and ignore.
		}
	}
	return
}

func wrap(msg string) error { return errString(msg) }

type errString string

func (e errString) Error() string { return string(e) }

// clientUnblockCmd implements CLIENT UNBLOCK <id> [TIMEOUT|ERROR].
//
//   - TIMEOUT (default): the blocking command returns nil as if its
//     timeout fired.
//   - ERROR: the blocking command returns the canonical
//     "UNBLOCKED client unblocked via CLIENT UNBLOCK" -ERR.
//
// Reply: 1 when a waiter was unblocked, 0 when the target client
// wasn't blocked at all.
func (c *conn) clientUnblockCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'client|unblock'")
		return
	}
	id, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		writeError(c.bw, "value is not an integer or out of range")
		return
	}
	reason := blocking.UnblockTimeout
	if len(args) >= 2 {
		switch strings.ToUpper(args[1]) {
		case "TIMEOUT":
			reason = blocking.UnblockTimeout
		case "ERROR":
			reason = blocking.UnblockError
		default:
			writeError(c.bw, "syntax error")
			return
		}
	}
	if c.eng.Blocker.Unblock(id, reason) > 0 {
		writeInt(c.bw, 1)
	} else {
		writeInt(c.bw, 0)
	}
}

// silence "imported and not used" if a refactor drops store.
var _ = store.ErrWrongType
