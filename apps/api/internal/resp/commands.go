package resp

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// dispatch routes a single command to the right handler. Kept as one big
// switch for clarity — a map-of-funcs reads nicely but makes argument
// validation repetitive. Order follows the Redis command groups.
func (c *conn) dispatch(cmd string, args []string) {
	switch cmd {

	// ─── connection / server ────────────────────────────────────────
	case "PING":
		if len(args) == 0 {
			writeSimple(c.bw, "PONG")
		} else {
			writeBulk(c.bw, args[0])
		}
	case "ECHO":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		writeBulk(c.bw, args[0])
	case "SELECT":
		// Single database — accept 0, reject others.
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		if args[0] != "0" {
			writeError(c.bw, "NeuroCache supports a single logical database (db 0 only)")
			return
		}
		writeSimple(c.bw, "OK")
	case "DBSIZE":
		writeInt(c.bw, int64(c.eng.KV.Size()))
	case "COMMAND", "HELLO":
		writeArray(c.bw, []string{})
	case "QUIT":
		writeSimple(c.bw, "OK")
	case "AUTH":
		writeSimple(c.bw, "OK") // no auth yet; stub so clients don't choke
	case "CLIENT":
		writeSimple(c.bw, "OK") // CLIENT SETNAME etc. — ignored
	case "INFO":
		writeBulk(c.bw, c.infoString())
	case "DEBUG":
		writeSimple(c.bw, "OK")
	case "TIME":
		now := time.Now()
		writeValue(c.bw, []any{
			strconv.FormatInt(now.Unix(), 10),
			strconv.FormatInt(int64(now.Nanosecond()/1000), 10),
		})
	case "FLUSHDB", "FLUSHALL":
		c.eng.KV.FlushAll()
		writeSimple(c.bw, "OK")

	// ─── keys / TTL ─────────────────────────────────────────────────
	case "DEL", "UNLINK":
		writeInt(c.bw, int64(c.eng.KV.Del(args...)))
	case "EXISTS":
		writeInt(c.bw, int64(c.eng.KV.Exists(args...)))
	case "TYPE":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		writeSimple(c.bw, c.eng.KV.Type(args[0]).String())
	case "EXPIRE", "PEXPIRE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		d := time.Duration(n) * time.Second
		if cmd == "PEXPIRE" {
			d = time.Duration(n) * time.Millisecond
		}
		if c.eng.KV.Expire(args[0], d) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "EXPIREAT", "PEXPIREAT":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		var t time.Time
		if cmd == "EXPIREAT" {
			t = time.Unix(n, 0)
		} else {
			t = time.UnixMilli(n)
		}
		if c.eng.KV.ExpireAt(args[0], t) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PERSIST":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		if c.eng.KV.Persist(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "TTL":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		d := c.eng.KV.TTL(args[0])
		if d < 0 {
			writeInt(c.bw, int64(d))
			return
		}
		writeInt(c.bw, int64(d.Seconds()))
	case "PTTL":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		d := c.eng.KV.TTL(args[0])
		if d < 0 {
			writeInt(c.bw, int64(d))
			return
		}
		writeInt(c.bw, d.Milliseconds())
	case "KEYS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		writeArray(c.bw, c.eng.KV.Keys(args[0]))
	case "RENAME":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		if !c.eng.KV.Rename(args[0], args[1]) {
			writeError(c.bw, "no such key")
			return
		}
		writeSimple(c.bw, "OK")
	case "RENAMENX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		if c.eng.KV.RenameNX(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SCAN":
		c.scanCmd(args)
	case "RANDOMKEY":
		keys := c.eng.KV.Keys("*")
		if len(keys) == 0 {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, keys[0])

	// ─── strings ───────────────────────────────────────────────────
	case "SET":
		c.setCmd(args)
	case "SETNX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		if c.eng.KV.SetNX(args[0], args[1], 0) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SETEX":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			writeError(c.bw, "invalid expire time in 'setex'")
			return
		}
		c.eng.KV.Set(args[0], args[2], time.Duration(n)*time.Second)
		writeSimple(c.bw, "OK")
	case "PSETEX":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			writeError(c.bw, "invalid expire time in 'psetex'")
			return
		}
		c.eng.KV.Set(args[0], args[2], time.Duration(n)*time.Millisecond)
		writeSimple(c.bw, "OK")
	case "GET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.GetTyped(args[0])
		c.eng.Metrics.RecordKVHit(args[0], ok)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "GETSET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		prev, had, err := c.eng.KV.GetSet(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !had {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, prev)
	case "MSET":
		if len(args) < 2 || len(args)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for MSET")
			return
		}
		if err := c.eng.KV.MSet(args...); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "MSETNX":
		if len(args) < 2 || len(args)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for MSETNX")
			return
		}
		ok, err := c.eng.KV.MSetNX(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "MGET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		vals, hits, _ := c.eng.KV.MGet(args...)
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			} else {
				out[i] = nil
			}
		}
		writeValue(c.bw, out)
	case "APPEND":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.Append(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "STRLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.StrLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "GETRANGE", "SUBSTR":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		s, err := c.eng.KV.GetRange(args[0], a, b)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeBulk(c.bw, s)
	case "SETRANGE":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		off, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "offset is not an integer")
			return
		}
		n, err := c.eng.KV.SetRange(args[0], off, args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "INCR":
		c.incrBy(args, 1)
	case "DECR":
		c.incrBy(args, -1)
	case "INCRBY":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		c.incrBy(args[:1], d)
	case "DECRBY":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		c.incrBy(args[:1], -d)
	case "INCRBYFLOAT":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		d, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "value is not a valid float")
			return
		}
		v, err := c.eng.KV.IncrByFloat(args[0], d)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', -1, 64))

	// ─── lists ─────────────────────────────────────────────────────
	case "LPUSH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.LPush(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "RPUSH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.RPush(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LPUSHX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.LPushX(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "RPUSHX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.RPushX(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LPOP":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.LPop(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "RPOP":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.RPop(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "LLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.LLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LINDEX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		i, _ := strconv.Atoi(args[1])
		v, ok, err := c.eng.KV.LIndex(args[0], i)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "LRANGE":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		out, err := c.eng.KV.LRange(args[0], a, b)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "LSET":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		i, _ := strconv.Atoi(args[1])
		if err := c.eng.KV.LSet(args[0], i, args[2]); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "LREM":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		count, _ := strconv.Atoi(args[1])
		n, err := c.eng.KV.LRem(args[0], count, args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LTRIM":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		if err := c.eng.KV.LTrim(args[0], a, b); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "LINSERT":
		if !c.wantArgs(cmd, args, 4) {
			return
		}
		before := strings.EqualFold(args[1], "BEFORE")
		n, err := c.eng.KV.LInsert(args[0], before, args[2], args[3])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "RPOPLPUSH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		v, ok, err := c.eng.KV.RPopLPush(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)

	// ─── hashes ────────────────────────────────────────────────────
	case "HSET", "HMSET":
		if !c.wantArgs(cmd, args, 3) || (len(args)-1)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for "+cmd)
			return
		}
		n, err := c.eng.KV.HSet(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if cmd == "HMSET" {
			writeSimple(c.bw, "OK")
			return
		}
		writeInt(c.bw, int64(n))
	case "HSETNX":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		ok, err := c.eng.KV.HSetNX(args[0], args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "HGET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		v, ok, err := c.eng.KV.HGet(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "HMGET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		vals, hits, err := c.eng.KV.HMGet(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			} else {
				out[i] = nil
			}
		}
		writeValue(c.bw, out)
	case "HGETALL":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.HGetAll(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "HDEL":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.HDel(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "HEXISTS":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		ok, err := c.eng.KV.HExists(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "HLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.HLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "HKEYS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.HKeys(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "HVALS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.HVals(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "HINCRBY":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		d, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer")
			return
		}
		v, err := c.eng.KV.HIncrBy(args[0], args[1], d)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, v)
	case "HINCRBYFLOAT":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		d, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "value is not a float")
			return
		}
		v, err := c.eng.KV.HIncrByFloat(args[0], args[1], d)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', -1, 64))
	case "HSTRLEN":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.HStrLen(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "HSCAN":
		c.hscanCmd(args)

	// ─── sets ──────────────────────────────────────────────────────
	case "SADD":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SAdd(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SREM":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SRem(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SISMEMBER":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		ok, err := c.eng.KV.SIsMember(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SMEMBERS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.SMembers(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SCARD":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.SCard(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SPOP":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.SPop(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "SRANDMEMBER":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		count := 1
		useArray := false
		if len(args) >= 2 {
			useArray = true
			n, err := strconv.Atoi(args[1])
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			count = n
		}
		out, err := c.eng.KV.SRandMember(args[0], count)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !useArray {
			if len(out) == 0 {
				writeNil(c.bw)
				return
			}
			writeBulk(c.bw, out[0])
			return
		}
		writeArray(c.bw, out)
	case "SMOVE":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		ok, err := c.eng.KV.SMove(args[0], args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SINTER":
		out, err := c.eng.KV.SInter(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SUNION":
		out, err := c.eng.KV.SUnion(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SDIFF":
		out, err := c.eng.KV.SDiff(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SINTERSTORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SInterStore(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SUNIONSTORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SUnionStore(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SDIFFSTORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SDiffStore(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SSCAN":
		c.sscanCmd(args)

	// ─── sorted sets ───────────────────────────────────────────────
	case "ZADD":
		c.zaddCmd(args)
	case "ZSCORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		sc, ok, err := c.eng.KV.ZScore(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeFloat(c.bw, sc)
	case "ZREM":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.ZRem(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "ZCARD":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.ZCard(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "ZINCRBY":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		d, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "value is not a float")
			return
		}
		sc, err := c.eng.KV.ZIncrBy(args[0], d, args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeFloat(c.bw, sc)
	case "ZRANK":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		r, ok, err := c.eng.KV.ZRank(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeInt(c.bw, int64(r))
	case "ZREVRANK":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		r, ok, err := c.eng.KV.ZRevRank(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeInt(c.bw, int64(r))
	case "ZRANGE":
		c.zrangeCmd(args, false)
	case "ZREVRANGE":
		c.zrangeCmd(args, true)
	case "ZRANGEBYSCORE":
		c.zrangeByScoreCmd(args, false)
	case "ZREVRANGEBYSCORE":
		c.zrangeByScoreCmd(args, true)
	case "ZCOUNT":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := c.eng.KV.ZCount(args[0], args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "ZPOPMIN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		m, sc, ok, err := c.eng.KV.ZPopMin(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeArray(c.bw, []string{})
			return
		}
		writeValue(c.bw, []any{m, strconv.FormatFloat(sc, 'f', -1, 64)})
	case "ZPOPMAX":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		m, sc, ok, err := c.eng.KV.ZPopMax(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeArray(c.bw, []string{})
			return
		}
		writeValue(c.bw, []any{m, strconv.FormatFloat(sc, 'f', -1, 64)})
	case "ZSCAN":
		c.zscanCmd(args)

	// ─── pub/sub ───────────────────────────────────────────────────
	case "SUBSCRIBE":
		c.subscribeCmd(args, false)
	case "UNSUBSCRIBE":
		c.unsubscribeCmd(args, false)
	case "PSUBSCRIBE":
		c.subscribeCmd(args, true)
	case "PUNSUBSCRIBE":
		c.unsubscribeCmd(args, true)
	case "PUBLISH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		writeInt(c.bw, int64(c.eng.PubSub.Publish(args[0], args[1])))
	case "PUBSUB":
		c.pubsubCmd(args)

	// ─── transactions ──────────────────────────────────────────────
	case "MULTI":
		if err := c.tx.Begin(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "EXEC":
		c.execCmd()
	case "DISCARD":
		if err := c.tx.Discard(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "WATCH":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		for _, k := range args {
			if err := c.tx.Watch(k, c.eng.KeyVersion(k)); err != nil {
				writeError(c.bw, err.Error())
				return
			}
		}
		writeSimple(c.bw, "OK")
	case "UNWATCH":
		c.tx.Unwatch()
		writeSimple(c.bw, "OK")

	// ─── AI-native ─────────────────────────────────────────────────
	case "SEMANTIC_SET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		c.eng.Semantic.Set(args[0], args[1])
		writeSimple(c.bw, "OK")
	case "SEMANTIC_GET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, _, ok := c.eng.Semantic.Get(args[0], float32(c.eng.Cfg.SemThreshold))
		c.eng.Metrics.RecordSemantic(ok)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "CACHE_LLM":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		c.eng.LLM.Set(args[0], args[1])
		writeSimple(c.bw, "OK")
	case "CACHE_LLM_GET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, _, ok := c.eng.LLM.Get(args[0], 0.88)
		c.eng.Metrics.RecordLLM(ok)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "MEMORY_ADD":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		e := c.eng.Memory.Add(args[0], strings.Join(args[1:], " "), nil)
		writeBulk(c.bw, e.ID)
	case "MEMORY_QUERY":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		hits := c.eng.Memory.Query(args[0], strings.Join(args[1:], " "), 5, 0.3)
		writeBulk(c.bw, memory.Synthesize(hits))

	default:
		writeError(c.bw, "unknown command '"+cmd+"'")
	}
}

// ─── shared helpers ─────────────────────────────────────────────────────

func (c *conn) infoString() string {
	i := c.eng.Info()
	return fmt.Sprintf("neurocache_version:%s\r\nuptime_in_seconds:%d\r\nkeys:%d\r\nused_memory:%d\r\nconnected_clients:%d\r\n",
		i.Version, int(i.UptimeSeconds), i.KV.Keys, i.KV.Bytes, i.Runtime.Goroutines)
}

// setCmd handles SET with [EX seconds | PX ms | NX | XX].
func (c *conn) setCmd(args []string) {
	if !c.wantArgs("SET", args, 2) {
		return
	}
	key, value := args[0], args[1]
	var ttl time.Duration
	nx, xx := false, false
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "EX":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			ttl = time.Duration(n) * time.Second
			i++
		case "PX":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			ttl = time.Duration(n) * time.Millisecond
			i++
		case "NX":
			nx = true
		case "XX":
			xx = true
		default:
			writeError(c.bw, "syntax error")
			return
		}
	}
	if nx {
		if !c.eng.KV.SetNX(key, value, ttl) {
			writeNil(c.bw)
			return
		}
		writeSimple(c.bw, "OK")
		return
	}
	if xx {
		if c.eng.KV.Exists(key) == 0 {
			writeNil(c.bw)
			return
		}
	}
	c.eng.KV.Set(key, value, ttl)
	writeSimple(c.bw, "OK")
}

// incrBy is the shared body for INCR/DECR/INCRBY/DECRBY after the delta
// has been parsed.
func (c *conn) incrBy(args []string, delta int64) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	v, err := c.eng.KV.Incr(args[0], delta)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, v)
}

// ─── ZADD / ZRANGE helpers ─────────────────────────────────────────────

func (c *conn) zaddCmd(args []string) {
	if len(args) < 3 || len(args)%2 == 0 {
		writeError(c.bw, "wrong number of arguments for 'zadd'")
		return
	}
	pairs := make([]store.ZPair, 0, (len(args)-1)/2)
	for i := 1; i+1 < len(args); i += 2 {
		sc, err := strconv.ParseFloat(args[i], 64)
		if err != nil {
			writeError(c.bw, "value is not a valid float")
			return
		}
		pairs = append(pairs, store.ZPair{Score: sc, Member: args[i+1]})
	}
	n, err := c.eng.KV.ZAdd(args[0], pairs...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

func (c *conn) zrangeCmd(args []string, reverse bool) {
	if !c.wantArgs("ZRANGE", args, 3) {
		return
	}
	a, _ := strconv.Atoi(args[1])
	b, _ := strconv.Atoi(args[2])
	withScores := false
	for _, t := range args[3:] {
		if strings.EqualFold(t, "WITHSCORES") {
			withScores = true
		}
	}
	out, err := c.eng.KV.ZRange(args[0], a, b, withScores, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

func (c *conn) zrangeByScoreCmd(args []string, reverse bool) {
	if !c.wantArgs("ZRANGEBYSCORE", args, 3) {
		return
	}
	withScores := false
	offset, count := 0, -1
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "WITHSCORES":
			withScores = true
		case "LIMIT":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			offset, _ = strconv.Atoi(args[i+1])
			count, _ = strconv.Atoi(args[i+2])
			i += 2
		}
	}
	minArg, maxArg := args[1], args[2]
	if reverse {
		minArg, maxArg = args[2], args[1]
	}
	out, err := c.eng.KV.ZRangeByScore(args[0], minArg, maxArg, offset, count, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

func writeZRange(w *bufio.Writer, out []store.ZRangeResult, withScores bool) {
	if !withScores {
		members := make([]string, len(out))
		for i, r := range out {
			members[i] = r.Member
		}
		writeArray(w, members)
		return
	}
	flat := make([]string, 0, len(out)*2)
	for _, r := range out {
		flat = append(flat, r.Member, strconv.FormatFloat(r.Score, 'f', -1, 64))
	}
	writeArray(w, flat)
}

// ─── SCAN helpers ──────────────────────────────────────────────────────

func (c *conn) scanCmd(args []string) {
	cursor := "0"
	if len(args) >= 1 {
		cursor = args[0]
	}
	match, typeFilter, count := parseScanOpts(args[1:])
	next, keys := c.eng.KV.Scan(cursor, match, typeFilter, count)
	writeValue(c.bw, []any{next, keys})
}

func (c *conn) hscanCmd(args []string) {
	if !c.wantArgs("HSCAN", args, 2) {
		return
	}
	match, _, count := parseScanOpts(args[2:])
	next, out, err := c.eng.KV.HScan(args[0], args[1], match, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{next, out})
}

func (c *conn) sscanCmd(args []string) {
	if !c.wantArgs("SSCAN", args, 2) {
		return
	}
	match, _, count := parseScanOpts(args[2:])
	next, out, err := c.eng.KV.SScan(args[0], args[1], match, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{next, out})
}

func (c *conn) zscanCmd(args []string) {
	if !c.wantArgs("ZSCAN", args, 2) {
		return
	}
	match, _, count := parseScanOpts(args[2:])
	next, out, err := c.eng.KV.ZScan(args[0], args[1], match, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{next, out})
}

func parseScanOpts(args []string) (match string, typeFilter string, count int) {
	count = 10
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "MATCH":
			if i+1 < len(args) {
				match = args[i+1]
				i++
			}
		case "COUNT":
			if i+1 < len(args) {
				count, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "TYPE":
			if i+1 < len(args) {
				typeFilter = args[i+1]
				i++
			}
		}
	}
	return
}

// ─── pub/sub helpers ───────────────────────────────────────────────────

// subscribeCmd registers one subscription per channel and starts a
// background goroutine that pushes inbound messages to the client. Redis
// sends back one reply per channel with the running subscription count.
func (c *conn) subscribeCmd(args []string, pattern bool) {
	if len(args) == 0 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	for _, ch := range args {
		if pattern {
			if _, already := c.psub[ch]; already {
				continue
			}
			sub := c.eng.PubSub.PSubscribe(ch)
			c.psub[ch] = sub
			go c.pumpSubscription(sub, true)
		} else {
			if _, already := c.subs[ch]; already {
				continue
			}
			sub := c.eng.PubSub.Subscribe(ch)
			c.subs[ch] = sub
			go c.pumpSubscription(sub, false)
		}
		kind := "subscribe"
		if pattern {
			kind = "psubscribe"
		}
		writeValue(c.bw, []any{kind, ch, int64(len(c.subs) + len(c.psub))})
	}
}

func (c *conn) unsubscribeCmd(args []string, pattern bool) {
	targets := args
	if len(targets) == 0 {
		if pattern {
			for ch := range c.psub {
				targets = append(targets, ch)
			}
		} else {
			for ch := range c.subs {
				targets = append(targets, ch)
			}
		}
	}
	if len(targets) == 0 {
		kind := "unsubscribe"
		if pattern {
			kind = "punsubscribe"
		}
		writeValue(c.bw, []any{kind, nil, int64(len(c.subs) + len(c.psub))})
		return
	}
	for _, ch := range targets {
		if pattern {
			if sub, ok := c.psub[ch]; ok {
				sub.Close()
				delete(c.psub, ch)
			}
		} else {
			if sub, ok := c.subs[ch]; ok {
				sub.Close()
				delete(c.subs, ch)
			}
		}
		kind := "unsubscribe"
		if pattern {
			kind = "punsubscribe"
		}
		writeValue(c.bw, []any{kind, ch, int64(len(c.subs) + len(c.psub))})
	}
}

// pumpSubscription forwards broker messages to the TCP client, locking
// the writer so a push never interleaves with a command reply.
func (c *conn) pumpSubscription(sub *pubsub.Subscription, pattern bool) {
	for {
		select {
		case <-c.done:
			return
		case m, ok := <-sub.Ch():
			if !ok {
				return
			}
			c.writeMu.Lock()
			if pattern {
				writeValue(c.bw, []any{"pmessage", m.Pattern, m.Channel, m.Payload})
			} else {
				writeValue(c.bw, []any{"message", m.Channel, m.Payload})
			}
			_ = c.bw.Flush()
			c.writeMu.Unlock()
		}
	}
}

func (c *conn) pubsubCmd(args []string) {
	if !c.wantArgs("PUBSUB", args, 1) {
		return
	}
	switch strings.ToUpper(args[0]) {
	case "CHANNELS":
		pat := "*"
		if len(args) >= 2 {
			pat = args[1]
		}
		writeArray(c.bw, c.eng.PubSub.Channels(pat))
	case "NUMSUB":
		counts := c.eng.PubSub.NumSub(args[1:]...)
		out := make([]any, 0, len(args[1:])*2)
		for _, ch := range args[1:] {
			out = append(out, ch, int64(counts[ch]))
		}
		writeValue(c.bw, out)
	case "NUMPAT":
		writeInt(c.bw, int64(c.eng.PubSub.NumPat()))
	default:
		writeError(c.bw, "unknown PUBSUB subcommand")
	}
}

// ─── EXEC ──────────────────────────────────────────────────────────────

// execCmd replays the queued commands after checking WATCHed keys.
// Each queued command is dispatched through the normal path so any side
// effects (pub/sub notifications, metrics) fire just like a direct call.
func (c *conn) execCmd() {
	c.tx.CheckDirty(c.eng.KeyVersion)
	queued, aborted := c.tx.Commit()
	if aborted {
		writeNilArray(c.bw)
		return
	}
	// Emit an array header, then dispatch each queued command with its
	// own nested reply. We flush the buffered writer between each so
	// multi-value replies (HGETALL etc.) stream correctly.
	fmt.Fprintf(c.bw, "*%d\r\n", len(queued))
	for _, q := range queued {
		c.dispatch(q.Cmd, q.Args)
	}
}
