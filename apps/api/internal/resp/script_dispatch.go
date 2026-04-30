package resp

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// scriptDispatch is the value-returning subset of the dispatcher used
// for redis.call() inside EVAL scripts. It covers the commands most
// scripts actually call. Anything missing falls through to a clear
// "command not supported in scripting context" error so callers know
// they need to widen the bridge rather than hitting a silent miscompile.
//
// Keep this list aligned with the writeset — any new write command we
// add to the AOF allowlist should also be wired here.
func scriptDispatch(c *conn, cmd string, args []string) (any, error) {
	switch cmd {
	// strings / keys
	case "SET":
		if len(args) < 2 {
			return nil, errors.New("SET requires key value")
		}
		ttl := time.Duration(0)
		nx, xx := false, false
		for i := 2; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "EX":
				n, _ := strconv.Atoi(args[i+1])
				ttl = time.Duration(n) * time.Second
				i++
			case "PX":
				n, _ := strconv.Atoi(args[i+1])
				ttl = time.Duration(n) * time.Millisecond
				i++
			case "NX":
				nx = true
			case "XX":
				xx = true
			}
		}
		if nx && !c.eng.KV.SetNX(args[0], args[1], ttl) {
			return nil, nil
		}
		if xx && c.eng.KV.Exists(args[0]) == 0 {
			return nil, nil
		}
		c.eng.KV.Set(args[0], args[1], ttl)
		c.eng.RecordWrite("SET", args)
		return "OK", nil
	case "GET":
		if len(args) < 1 {
			return nil, errors.New("GET requires key")
		}
		v, ok, err := c.eng.KV.GetTyped(args[0])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "DEL", "UNLINK":
		n := c.eng.KV.Del(args...)
		c.eng.RecordWrite(cmd, args)
		return int64(n), nil
	case "EXISTS":
		return int64(c.eng.KV.Exists(args...)), nil
	case "INCR":
		if len(args) < 1 {
			return nil, errors.New("INCR requires key")
		}
		v, err := c.eng.KV.Incr(args[0], 1)
		c.eng.RecordWrite("INCR", args)
		return v, err
	case "DECR":
		v, err := c.eng.KV.Incr(args[0], -1)
		c.eng.RecordWrite("DECR", args)
		return v, err
	case "INCRBY":
		d, _ := strconv.ParseInt(args[1], 10, 64)
		v, err := c.eng.KV.Incr(args[0], d)
		c.eng.RecordWrite("INCRBY", args)
		return v, err
	case "DECRBY":
		d, _ := strconv.ParseInt(args[1], 10, 64)
		v, err := c.eng.KV.Incr(args[0], -d)
		c.eng.RecordWrite("DECRBY", args)
		return v, err
	case "EXPIRE":
		n, _ := strconv.Atoi(args[1])
		ok := c.eng.KV.Expire(args[0], time.Duration(n)*time.Second)
		c.eng.RecordWrite("EXPIRE", args)
		if ok {
			return int64(1), nil
		}
		return int64(0), nil
	case "PEXPIRE":
		n, _ := strconv.Atoi(args[1])
		ok := c.eng.KV.Expire(args[0], time.Duration(n)*time.Millisecond)
		c.eng.RecordWrite("PEXPIRE", args)
		if ok {
			return int64(1), nil
		}
		return int64(0), nil
	case "PERSIST":
		ok := c.eng.KV.Persist(args[0])
		c.eng.RecordWrite("PERSIST", args)
		if ok {
			return int64(1), nil
		}
		return int64(0), nil
	case "TTL":
		d := c.eng.KV.TTL(args[0])
		if d < 0 {
			return int64(d), nil
		}
		return int64(d.Seconds()), nil
	case "TYPE":
		return c.eng.KV.Type(args[0]).String(), nil
	case "APPEND":
		n, err := c.eng.KV.Append(args[0], args[1])
		c.eng.RecordWrite("APPEND", args)
		return int64(n), err
	case "STRLEN":
		n, err := c.eng.KV.StrLen(args[0])
		return int64(n), err
	case "MSET":
		err := c.eng.KV.MSet(args...)
		c.eng.RecordWrite("MSET", args)
		return "OK", err
	case "MGET":
		vals, hits, _ := c.eng.KV.MGet(args...)
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			}
		}
		return out, nil

	// hashes
	case "HSET":
		n, err := c.eng.KV.HSet(args[0], args[1:]...)
		c.eng.RecordWrite("HSET", args)
		return int64(n), err
	case "HGET":
		v, ok, err := c.eng.KV.HGet(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "HDEL":
		n, err := c.eng.KV.HDel(args[0], args[1:]...)
		c.eng.RecordWrite("HDEL", args)
		return int64(n), err
	case "HGETALL":
		flat, err := c.eng.KV.HGetAll(args[0])
		if err != nil {
			return nil, err
		}
		out := make([]any, len(flat))
		for i, s := range flat {
			out[i] = s
		}
		return out, nil
	case "HEXISTS":
		ok, err := c.eng.KV.HExists(args[0], args[1])
		if err != nil {
			return nil, err
		}
		if ok {
			return int64(1), nil
		}
		return int64(0), nil
	case "HINCRBY":
		d, _ := strconv.ParseInt(args[2], 10, 64)
		v, err := c.eng.KV.HIncrBy(args[0], args[1], d)
		c.eng.RecordWrite("HINCRBY", args)
		return v, err

	// lists
	case "LPUSH":
		n, err := c.eng.KV.LPush(args[0], args[1:]...)
		c.eng.RecordWrite("LPUSH", args)
		return int64(n), err
	case "RPUSH":
		n, err := c.eng.KV.RPush(args[0], args[1:]...)
		c.eng.RecordWrite("RPUSH", args)
		return int64(n), err
	case "LPOP":
		v, ok, err := c.eng.KV.LPop(args[0])
		c.eng.RecordWrite("LPOP", args)
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "RPOP":
		v, ok, err := c.eng.KV.RPop(args[0])
		c.eng.RecordWrite("RPOP", args)
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "LRANGE":
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		out, err := c.eng.KV.LRange(args[0], a, b)
		if err != nil {
			return nil, err
		}
		ret := make([]any, len(out))
		for i, s := range out {
			ret[i] = s
		}
		return ret, nil
	case "LLEN":
		n, err := c.eng.KV.LLen(args[0])
		return int64(n), err

	// sets
	case "SADD":
		n, err := c.eng.KV.SAdd(args[0], args[1:]...)
		c.eng.RecordWrite("SADD", args)
		return int64(n), err
	case "SREM":
		n, err := c.eng.KV.SRem(args[0], args[1:]...)
		c.eng.RecordWrite("SREM", args)
		return int64(n), err
	case "SMEMBERS":
		out, err := c.eng.KV.SMembers(args[0])
		if err != nil {
			return nil, err
		}
		ret := make([]any, len(out))
		for i, s := range out {
			ret[i] = s
		}
		return ret, nil
	case "SISMEMBER":
		ok, err := c.eng.KV.SIsMember(args[0], args[1])
		if err != nil {
			return nil, err
		}
		if ok {
			return int64(1), nil
		}
		return int64(0), nil

	// zsets
	case "ZADD":
		pairs := make([]store.ZPair, 0, (len(args)-1)/2)
		for i := 1; i+1 < len(args); i += 2 {
			sc, _ := strconv.ParseFloat(args[i], 64)
			pairs = append(pairs, store.ZPair{Score: sc, Member: args[i+1]})
		}
		n, err := c.eng.KV.ZAdd(args[0], pairs...)
		c.eng.RecordWrite("ZADD", args)
		return int64(n), err
	case "ZSCORE":
		sc, ok, err := c.eng.KV.ZScore(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return strconv.FormatFloat(sc, 'f', -1, 64), nil
	case "ZRANGE":
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		withScores := false
		for _, t := range args[3:] {
			if strings.EqualFold(t, "WITHSCORES") {
				withScores = true
			}
		}
		out, err := c.eng.KV.ZRange(args[0], a, b, withScores, false)
		if err != nil {
			return nil, err
		}
		ret := make([]any, 0, len(out)*2)
		for _, r := range out {
			ret = append(ret, r.Member)
			if withScores {
				ret = append(ret, strconv.FormatFloat(r.Score, 'f', -1, 64))
			}
		}
		return ret, nil

	// phase 1 fillers — same shape as the dispatcher handlers, with the
	// reply lowered to a plain Go value so Lua can consume it via the
	// resp <-> Lua bridge in lua_real.go.
	case "TOUCH":
		return int64(c.eng.KV.Touch(args...)), nil
	case "EXPIRETIME":
		return c.eng.KV.ExpireTime(args[0]), nil
	case "PEXPIRETIME":
		return c.eng.KV.PExpireTime(args[0]), nil
	case "LMOVE":
		if len(args) < 4 {
			return nil, errors.New("LMOVE requires source destination LEFT|RIGHT LEFT|RIGHT")
		}
		v, ok, err := c.eng.KV.LMove(args[0], args[1], strings.EqualFold(args[2], "RIGHT"), strings.EqualFold(args[3], "RIGHT"))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		c.eng.RecordWrite("LMOVE", args)
		return v, nil
	case "ZMSCORE":
		scores, hits, err := c.eng.KV.ZMScore(args[0], args[1:]...)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(hits))
		for i, h := range hits {
			if !h {
				out[i] = nil
				continue
			}
			out[i] = strconv.FormatFloat(scores[i], 'f', -1, 64)
		}
		return out, nil
	case "ZREMRANGEBYRANK":
		s, _ := strconv.Atoi(args[1])
		e, _ := strconv.Atoi(args[2])
		n, err := c.eng.KV.ZRemRangeByRank(args[0], s, e)
		c.eng.RecordWrite("ZREMRANGEBYRANK", args)
		return int64(n), err
	case "ZREMRANGEBYSCORE":
		n, err := c.eng.KV.ZRemRangeByScore(args[0], args[1], args[2])
		c.eng.RecordWrite("ZREMRANGEBYSCORE", args)
		return int64(n), err
	case "ZREMRANGEBYLEX":
		n, err := c.eng.KV.ZRemRangeByLex(args[0], args[1], args[2])
		c.eng.RecordWrite("ZREMRANGEBYLEX", args)
		return int64(n), err

	// phase 2 hash extras — same shapes as the dispatcher handlers.
	// The reply value is the bare Go object so the gopher-lua bridge
	// translates it into a Lua table without an extra wrapper.
	case "HGETDEL":
		// Layout: HGETDEL key FIELDS n field [field ...]
		if len(args) < 4 || !strings.EqualFold(args[1], "FIELDS") {
			return nil, errors.New("HGETDEL key FIELDS numfields field [...]")
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n <= 0 || 3+n > len(args) {
			return nil, errors.New("ERR numfields must match the field count")
		}
		fields := args[3 : 3+n]
		values, hits, err := c.eng.KV.HGetDel(args[0], fields)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("HGETDEL", args)
		out := make([]any, len(fields))
		for i := range fields {
			if hits[i] {
				out[i] = values[i]
			}
		}
		return out, nil
	case "HEXPIRETIME", "HPEXPIRETIME":
		if len(args) < 4 || !strings.EqualFold(args[1], "FIELDS") {
			return nil, errors.New(cmd + " key FIELDS numfields field [...]")
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n <= 0 || 3+n > len(args) {
			return nil, errors.New("ERR numfields must match the field count")
		}
		ms := cmd == "HPEXPIRETIME"
		out, err := c.eng.KV.HExpireTime(args[0], args[3:3+n], ms)
		if err != nil {
			return nil, err
		}
		flat := make([]any, len(out))
		for i, v := range out {
			flat[i] = v
		}
		return flat, nil
	case "HGETEX":
		// Layout: HGETEX key [EX|PX|EXAT|PXAT v|PERSIST] FIELDS n field [...]
		if len(args) < 4 {
			return nil, errors.New("HGETEX requires key + TTL clause + FIELDS")
		}
		mode, value := "", int64(0)
		i := 1
	hexLoop:
		for ; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "EX", "PX", "EXAT", "PXAT":
				if i+1 >= len(args) {
					return nil, errors.New("syntax error")
				}
				mode = strings.ToUpper(args[i])
				v, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil {
					return nil, errors.New("value is not an integer")
				}
				value = v
				i++
			case "PERSIST":
				mode = "PERSIST"
			case "FIELDS":
				break hexLoop
			}
		}
		if i+2 >= len(args) || !strings.EqualFold(args[i], "FIELDS") {
			return nil, errors.New("HGETEX FIELDS numfields field [...]")
		}
		n, err := strconv.Atoi(args[i+1])
		if err != nil || n <= 0 || i+2+n > len(args) {
			return nil, errors.New("ERR numfields must match the field count")
		}
		fields := args[i+2 : i+2+n]
		values, hits, err := c.eng.KV.HGetEx(args[0], fields, mode, value)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("HGETEX", args)
		out := make([]any, len(fields))
		for j := range fields {
			if hits[j] {
				out[j] = values[j]
			}
		}
		return out, nil
	// phase 4 niche additions
	case "DELEX":
		if len(args) < 2 {
			return nil, errors.New("DELEX key value")
		}
		n, err := c.eng.KV.DelEx(args[0], args[1])
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("DELEX", args)
		return int64(n), nil
	case "DIGEST":
		out := make([]any, len(args))
		for i, k := range args {
			d, ok, err := c.eng.KV.Digest(k)
			if err != nil {
				return nil, err
			}
			if ok {
				out[i] = d
			}
		}
		return out, nil
	case "MSETEX":
		if len(args) < 3 {
			return nil, errors.New("MSETEX seconds key value [...]")
		}
		secs, err := strconv.Atoi(args[0])
		if err != nil || secs <= 0 {
			return nil, errors.New("invalid expire time in 'msetex'")
		}
		rest := args[1:]
		if len(rest)%2 != 0 {
			return nil, errors.New("wrong number of arguments for MSETEX")
		}
		if err := c.eng.KV.MSetEx(time.Duration(secs)*time.Second, rest...); err != nil {
			return nil, err
		}
		c.eng.RecordWrite("MSETEX", args)
		return "OK", nil
	case "XACKDEL":
		if len(args) < 3 {
			return nil, errors.New("XACKDEL key group id [...]")
		}
		n, err := c.eng.KV.XAckDel(args[0], args[1], args[2:]...)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("XACKDEL", args)
		return int64(n), nil
	// phase 5 — vector-set type. Cover the high-traffic V* commands
	// scripts actually reach for (read paths + the basic insert).
	case "VADD":
		if len(args) < 3 {
			return nil, errors.New("VADD key id vec [opts ...]")
		}
		key, id := args[0], args[1]
		dim, _, _ := c.eng.KV.VDim(key)
		opts := vectorindex.Options{Algo: vectorindex.AlgoHNSW, Metric: vectorindex.MetricCosine, Dim: dim}
		// Light option parser — same shape as the dispatcher handler
		// but limited to what scripts realistically pass.
		for i := 3; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "DIM":
				if i+1 < len(args) {
					opts.Dim, _ = strconv.Atoi(args[i+1])
					i++
				}
			case "METRIC":
				if i+1 < len(args) {
					opts.Metric = vectorindex.Metric(strings.ToUpper(args[i+1]))
					i++
				}
			case "TYPE":
				if i+1 < len(args) {
					opts.Algo = vectorindex.Algo(strings.ToUpper(args[i+1]))
					i++
				}
			}
		}
		if opts.Dim == 0 {
			return nil, errors.New("DIM is required for the first VADD on this key")
		}
		vec, err := vectorindex.ParseVector(args[2], opts.Dim)
		if err != nil {
			return nil, err
		}
		n, err := c.eng.KV.VAdd(key, id, vec, opts)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("VADD", args)
		return int64(n), nil
	case "VREM":
		if len(args) < 2 {
			return nil, errors.New("VREM key id [id ...]")
		}
		n, err := c.eng.KV.VRem(args[0], args[1:]...)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("VREM", args)
		return int64(n), nil
	case "VSIM":
		if len(args) < 2 {
			return nil, errors.New("VSIM key vec [COUNT n]")
		}
		count := 10
		for i := 2; i < len(args); i++ {
			if strings.EqualFold(args[i], "COUNT") && i+1 < len(args) {
				count, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
		dim, ok, _ := c.eng.KV.VDim(args[0])
		if !ok {
			return []any{}, nil
		}
		query, err := vectorindex.ParseVector(args[1], dim)
		if err != nil {
			return nil, err
		}
		results, err := c.eng.KV.VSim(args[0], query, count)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(results)*2)
		for _, r := range results {
			out = append(out, r.ID)
			out = append(out, strconv.FormatFloat(r.Distance, 'f', -1, 64))
		}
		return out, nil
	case "VCARD":
		if len(args) < 1 {
			return nil, errors.New("VCARD key")
		}
		n, err := c.eng.KV.VCard(args[0])
		return int64(n), err
	case "VDIM":
		if len(args) < 1 {
			return nil, errors.New("VDIM key")
		}
		d, ok, err := c.eng.KV.VDim(args[0])
		if err != nil || !ok {
			return nil, err
		}
		return int64(d), nil
	case "VEMB":
		if len(args) < 2 {
			return nil, errors.New("VEMB key id")
		}
		vec, ok, err := c.eng.KV.VEmb(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return vectorindex.EncodeVector(vec), nil

	case "XDELEX":
		if len(args) < 2 {
			return nil, errors.New("XDELEX key [REF|KEEPREF|ACKED] id [...]")
		}
		mode := store.XDelExKeepRef
		rest := args[1:]
		if len(rest) > 0 {
			switch strings.ToUpper(rest[0]) {
			case "KEEPREF", "REF", "ACKED":
				m, err := store.ParseXDelExMode(rest[0])
				if err != nil {
					return nil, err
				}
				mode = m
				rest = rest[1:]
			}
		}
		if len(rest) == 0 {
			return nil, errors.New("XDELEX requires at least one ID")
		}
		n, err := c.eng.KV.XDelEx(args[0], mode, rest...)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("XDELEX", args)
		return int64(n), nil

	case "HSETEX":
		// Layout: HSETEX key seconds [FNX|FXX] FIELDS n field value [...]
		if len(args) < 5 {
			return nil, errors.New("HSETEX key seconds [FNX|FXX] FIELDS n field value [...]")
		}
		secs, err := strconv.Atoi(args[1])
		if err != nil || secs < 0 {
			return nil, errors.New("invalid expire time in 'hsetex'")
		}
		cond := ""
		i := 2
		switch strings.ToUpper(args[i]) {
		case "FNX", "FXX":
			cond = strings.ToUpper(args[i])
			i++
		}
		if i >= len(args) || !strings.EqualFold(args[i], "FIELDS") {
			return nil, errors.New("HSETEX FIELDS clause required")
		}
		if i+2 >= len(args) {
			return nil, errors.New("ERR FIELDS numfields field value [...]")
		}
		n, err := strconv.Atoi(args[i+1])
		if err != nil || n <= 0 {
			return nil, errors.New("ERR numfields must be a positive integer")
		}
		rest := args[i+2:]
		if len(rest) != 2*n {
			return nil, errors.New("ERR numfields does not match the supplied field/value count")
		}
		res, err := c.eng.KV.HSetEx(args[0], time.Duration(secs)*time.Second, cond, rest)
		if err != nil {
			return nil, err
		}
		c.eng.RecordWrite("HSETEX", args)
		return int64(res), nil

	// pub/sub
	case "PUBLISH":
		return int64(c.eng.PubSub.Publish(args[0], args[1])), nil

	// keyspace introspection
	case "KEYS":
		out := c.eng.KV.Keys(args[0])
		ret := make([]any, len(out))
		for i, s := range out {
			ret[i] = s
		}
		return ret, nil
	case "DBSIZE":
		return int64(c.eng.KV.Size()), nil
	case "TIME":
		now := time.Now()
		return []any{strconv.FormatInt(now.Unix(), 10), strconv.FormatInt(int64(now.Nanosecond()/1000), 10)}, nil
	}
	return nil, errors.New("script: command not supported in scripting context: " + cmd)
}
