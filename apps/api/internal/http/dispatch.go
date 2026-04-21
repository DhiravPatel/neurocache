package http

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// dispatch runs a Redis-style command from the HTTP /api/exec endpoint.
// Return types are JSON-friendly (strings, numbers, slices) so the
// dashboard playground can render them directly.
func (h *handlers) dispatch(cmd string, args []string) (any, error) {
	switch cmd {

	// ─── connection / server ────────────────────────────────────────
	case "PING":
		return "PONG", nil
	case "DBSIZE":
		return h.eng.KV.Size(), nil
	case "INFO":
		return h.eng.Info(), nil
	case "TIME":
		now := time.Now()
		return []string{
			strconv.FormatInt(now.Unix(), 10),
			strconv.FormatInt(int64(now.Nanosecond()/1000), 10),
		}, nil
	case "FLUSHDB", "FLUSHALL":
		h.eng.KV.FlushAll()
		return "OK", nil

	// ─── keys / TTL ─────────────────────────────────────────────────
	case "DEL", "UNLINK":
		return h.eng.KV.Del(args...), nil
	case "EXISTS":
		return h.eng.KV.Exists(args...), nil
	case "TYPE":
		if len(args) < 1 {
			return nil, errors.New("TYPE key")
		}
		return h.eng.KV.Type(args[0]).String(), nil
	case "EXPIRE":
		if len(args) < 2 {
			return nil, errors.New("EXPIRE key seconds")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		return h.eng.KV.Expire(args[0], time.Duration(n)*time.Second), nil
	case "PEXPIRE":
		if len(args) < 2 {
			return nil, errors.New("PEXPIRE key ms")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		return h.eng.KV.Expire(args[0], time.Duration(n)*time.Millisecond), nil
	case "PERSIST":
		if len(args) < 1 {
			return nil, errors.New("PERSIST key")
		}
		return h.eng.KV.Persist(args[0]), nil
	case "TTL":
		if len(args) < 1 {
			return nil, errors.New("TTL key")
		}
		d := h.eng.KV.TTL(args[0])
		if d < 0 {
			return int64(d), nil
		}
		return int64(d.Seconds()), nil
	case "PTTL":
		if len(args) < 1 {
			return nil, errors.New("PTTL key")
		}
		d := h.eng.KV.TTL(args[0])
		if d < 0 {
			return int64(d), nil
		}
		return d.Milliseconds(), nil
	case "KEYS":
		pat := "*"
		if len(args) >= 1 {
			pat = args[0]
		}
		return h.eng.KV.Keys(pat), nil
	case "RENAME":
		if len(args) < 2 {
			return nil, errors.New("RENAME src dst")
		}
		if !h.eng.KV.Rename(args[0], args[1]) {
			return nil, errors.New("no such key")
		}
		return "OK", nil
	case "RENAMENX":
		if len(args) < 2 {
			return nil, errors.New("RENAMENX src dst")
		}
		return h.eng.KV.RenameNX(args[0], args[1]), nil
	case "SCAN":
		cursor := "0"
		if len(args) >= 1 {
			cursor = args[0]
		}
		match, typ, count := parseScanOpts(args[1:])
		next, keys := h.eng.KV.Scan(cursor, match, typ, count)
		return map[string]any{"cursor": next, "keys": keys}, nil

	// ─── strings ───────────────────────────────────────────────────
	case "SET":
		if len(args) < 2 {
			return nil, errors.New("SET key value [EX seconds | PX ms | NX | XX]")
		}
		key, value := args[0], args[1]
		var ttl time.Duration
		nx, xx := false, false
		for i := 2; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "EX":
				if i+1 >= len(args) {
					return nil, errors.New("syntax error")
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					return nil, err
				}
				ttl = time.Duration(n) * time.Second
				i++
			case "PX":
				if i+1 >= len(args) {
					return nil, errors.New("syntax error")
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					return nil, err
				}
				ttl = time.Duration(n) * time.Millisecond
				i++
			case "NX":
				nx = true
			case "XX":
				xx = true
			}
		}
		if nx {
			if !h.eng.KV.SetNX(key, value, ttl) {
				return nil, nil
			}
			return "OK", nil
		}
		if xx && h.eng.KV.Exists(key) == 0 {
			return nil, nil
		}
		h.eng.KV.Set(key, value, ttl)
		return "OK", nil
	case "SETNX":
		if len(args) < 2 {
			return nil, errors.New("SETNX key value")
		}
		return h.eng.KV.SetNX(args[0], args[1], 0), nil
	case "SETEX":
		if len(args) < 3 {
			return nil, errors.New("SETEX key seconds value")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		h.eng.KV.Set(args[0], args[2], time.Duration(n)*time.Second)
		return "OK", nil
	case "GET":
		if len(args) < 1 {
			return nil, errors.New("GET key")
		}
		v, ok, err := h.eng.KV.GetTyped(args[0])
		h.eng.Metrics.RecordKVHit(args[0], ok)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		return v, nil
	case "GETSET":
		if len(args) < 2 {
			return nil, errors.New("GETSET key value")
		}
		prev, had, err := h.eng.KV.GetSet(args[0], args[1])
		if err != nil {
			return nil, err
		}
		if !had {
			return nil, nil
		}
		return prev, nil
	case "MSET":
		if err := h.eng.KV.MSet(args...); err != nil {
			return nil, err
		}
		return "OK", nil
	case "MSETNX":
		ok, err := h.eng.KV.MSetNX(args...)
		return ok, err
	case "MGET":
		vals, hits, _ := h.eng.KV.MGet(args...)
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			}
		}
		return out, nil
	case "APPEND":
		if len(args) < 2 {
			return nil, errors.New("APPEND key value")
		}
		return h.eng.KV.Append(args[0], args[1])
	case "STRLEN":
		if len(args) < 1 {
			return nil, errors.New("STRLEN key")
		}
		return h.eng.KV.StrLen(args[0])
	case "GETRANGE", "SUBSTR":
		if len(args) < 3 {
			return nil, errors.New("GETRANGE key start end")
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		return h.eng.KV.GetRange(args[0], a, b)
	case "SETRANGE":
		if len(args) < 3 {
			return nil, errors.New("SETRANGE key offset value")
		}
		off, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		return h.eng.KV.SetRange(args[0], off, args[2])
	case "INCR":
		if len(args) < 1 {
			return nil, errors.New("INCR key")
		}
		return h.eng.KV.Incr(args[0], 1)
	case "DECR":
		if len(args) < 1 {
			return nil, errors.New("DECR key")
		}
		return h.eng.KV.Incr(args[0], -1)
	case "INCRBY":
		if len(args) < 2 {
			return nil, errors.New("INCRBY key delta")
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.Incr(args[0], d)
	case "DECRBY":
		if len(args) < 2 {
			return nil, errors.New("DECRBY key delta")
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.Incr(args[0], -d)
	case "INCRBYFLOAT":
		if len(args) < 2 {
			return nil, errors.New("INCRBYFLOAT key delta")
		}
		d, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.IncrByFloat(args[0], d)

	// ─── lists ─────────────────────────────────────────────────────
	case "LPUSH":
		if len(args) < 2 {
			return nil, errors.New("LPUSH key value [value ...]")
		}
		return h.eng.KV.LPush(args[0], args[1:]...)
	case "RPUSH":
		if len(args) < 2 {
			return nil, errors.New("RPUSH key value [value ...]")
		}
		return h.eng.KV.RPush(args[0], args[1:]...)
	case "LPOP":
		if len(args) < 1 {
			return nil, errors.New("LPOP key")
		}
		v, ok, err := h.eng.KV.LPop(args[0])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "RPOP":
		if len(args) < 1 {
			return nil, errors.New("RPOP key")
		}
		v, ok, err := h.eng.KV.RPop(args[0])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "LLEN":
		if len(args) < 1 {
			return nil, errors.New("LLEN key")
		}
		return h.eng.KV.LLen(args[0])
	case "LINDEX":
		if len(args) < 2 {
			return nil, errors.New("LINDEX key index")
		}
		i, _ := strconv.Atoi(args[1])
		v, ok, err := h.eng.KV.LIndex(args[0], i)
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "LRANGE":
		if len(args) < 3 {
			return nil, errors.New("LRANGE key start stop")
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		return h.eng.KV.LRange(args[0], a, b)
	case "LSET":
		if len(args) < 3 {
			return nil, errors.New("LSET key index value")
		}
		i, _ := strconv.Atoi(args[1])
		return "OK", h.eng.KV.LSet(args[0], i, args[2])
	case "LREM":
		if len(args) < 3 {
			return nil, errors.New("LREM key count value")
		}
		c, _ := strconv.Atoi(args[1])
		return h.eng.KV.LRem(args[0], c, args[2])
	case "LTRIM":
		if len(args) < 3 {
			return nil, errors.New("LTRIM key start stop")
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		return "OK", h.eng.KV.LTrim(args[0], a, b)
	case "LINSERT":
		if len(args) < 4 {
			return nil, errors.New("LINSERT key BEFORE|AFTER pivot value")
		}
		before := strings.EqualFold(args[1], "BEFORE")
		return h.eng.KV.LInsert(args[0], before, args[2], args[3])
	case "RPOPLPUSH":
		if len(args) < 2 {
			return nil, errors.New("RPOPLPUSH src dst")
		}
		v, ok, err := h.eng.KV.RPopLPush(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil

	// ─── hashes ────────────────────────────────────────────────────
	case "HSET", "HMSET":
		if len(args) < 3 || (len(args)-1)%2 != 0 {
			return nil, errors.New("HSET key field value [field value ...]")
		}
		n, err := h.eng.KV.HSet(args[0], args[1:]...)
		if cmd == "HMSET" {
			return "OK", err
		}
		return n, err
	case "HSETNX":
		if len(args) < 3 {
			return nil, errors.New("HSETNX key field value")
		}
		return h.eng.KV.HSetNX(args[0], args[1], args[2])
	case "HGET":
		if len(args) < 2 {
			return nil, errors.New("HGET key field")
		}
		v, ok, err := h.eng.KV.HGet(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "HMGET":
		if len(args) < 2 {
			return nil, errors.New("HMGET key field [field ...]")
		}
		vals, hits, err := h.eng.KV.HMGet(args[0], args[1:]...)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			}
		}
		return out, nil
	case "HGETALL":
		if len(args) < 1 {
			return nil, errors.New("HGETALL key")
		}
		flat, err := h.eng.KV.HGetAll(args[0])
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for i := 0; i+1 < len(flat); i += 2 {
			out[flat[i]] = flat[i+1]
		}
		return out, nil
	case "HDEL":
		if len(args) < 2 {
			return nil, errors.New("HDEL key field [field ...]")
		}
		return h.eng.KV.HDel(args[0], args[1:]...)
	case "HEXISTS":
		if len(args) < 2 {
			return nil, errors.New("HEXISTS key field")
		}
		return h.eng.KV.HExists(args[0], args[1])
	case "HLEN":
		if len(args) < 1 {
			return nil, errors.New("HLEN key")
		}
		return h.eng.KV.HLen(args[0])
	case "HKEYS":
		if len(args) < 1 {
			return nil, errors.New("HKEYS key")
		}
		return h.eng.KV.HKeys(args[0])
	case "HVALS":
		if len(args) < 1 {
			return nil, errors.New("HVALS key")
		}
		return h.eng.KV.HVals(args[0])
	case "HINCRBY":
		if len(args) < 3 {
			return nil, errors.New("HINCRBY key field delta")
		}
		d, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.HIncrBy(args[0], args[1], d)
	case "HINCRBYFLOAT":
		if len(args) < 3 {
			return nil, errors.New("HINCRBYFLOAT key field delta")
		}
		d, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.HIncrByFloat(args[0], args[1], d)
	case "HSTRLEN":
		if len(args) < 2 {
			return nil, errors.New("HSTRLEN key field")
		}
		return h.eng.KV.HStrLen(args[0], args[1])

	// ─── sets ──────────────────────────────────────────────────────
	case "SADD":
		if len(args) < 2 {
			return nil, errors.New("SADD key member [member ...]")
		}
		return h.eng.KV.SAdd(args[0], args[1:]...)
	case "SREM":
		if len(args) < 2 {
			return nil, errors.New("SREM key member [member ...]")
		}
		return h.eng.KV.SRem(args[0], args[1:]...)
	case "SISMEMBER":
		if len(args) < 2 {
			return nil, errors.New("SISMEMBER key member")
		}
		return h.eng.KV.SIsMember(args[0], args[1])
	case "SMEMBERS":
		if len(args) < 1 {
			return nil, errors.New("SMEMBERS key")
		}
		return h.eng.KV.SMembers(args[0])
	case "SCARD":
		if len(args) < 1 {
			return nil, errors.New("SCARD key")
		}
		return h.eng.KV.SCard(args[0])
	case "SPOP":
		if len(args) < 1 {
			return nil, errors.New("SPOP key")
		}
		v, ok, err := h.eng.KV.SPop(args[0])
		if err != nil || !ok {
			return nil, err
		}
		return v, nil
	case "SRANDMEMBER":
		if len(args) < 1 {
			return nil, errors.New("SRANDMEMBER key [count]")
		}
		c := 1
		if len(args) >= 2 {
			c, _ = strconv.Atoi(args[1])
		}
		return h.eng.KV.SRandMember(args[0], c)
	case "SMOVE":
		if len(args) < 3 {
			return nil, errors.New("SMOVE src dst member")
		}
		return h.eng.KV.SMove(args[0], args[1], args[2])
	case "SINTER":
		return h.eng.KV.SInter(args...)
	case "SUNION":
		return h.eng.KV.SUnion(args...)
	case "SDIFF":
		return h.eng.KV.SDiff(args...)
	case "SINTERSTORE":
		if len(args) < 2 {
			return nil, errors.New("SINTERSTORE dst key [key ...]")
		}
		return h.eng.KV.SInterStore(args[0], args[1:]...)
	case "SUNIONSTORE":
		if len(args) < 2 {
			return nil, errors.New("SUNIONSTORE dst key [key ...]")
		}
		return h.eng.KV.SUnionStore(args[0], args[1:]...)
	case "SDIFFSTORE":
		if len(args) < 2 {
			return nil, errors.New("SDIFFSTORE dst key [key ...]")
		}
		return h.eng.KV.SDiffStore(args[0], args[1:]...)

	// ─── sorted sets ───────────────────────────────────────────────
	case "ZADD":
		if len(args) < 3 || len(args)%2 == 0 {
			return nil, errors.New("ZADD key score member [score member ...]")
		}
		pairs := make([]store.ZPair, 0, (len(args)-1)/2)
		for i := 1; i+1 < len(args); i += 2 {
			sc, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, store.ZPair{Score: sc, Member: args[i+1]})
		}
		return h.eng.KV.ZAdd(args[0], pairs...)
	case "ZSCORE":
		if len(args) < 2 {
			return nil, errors.New("ZSCORE key member")
		}
		sc, ok, err := h.eng.KV.ZScore(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return sc, nil
	case "ZREM":
		if len(args) < 2 {
			return nil, errors.New("ZREM key member [member ...]")
		}
		return h.eng.KV.ZRem(args[0], args[1:]...)
	case "ZCARD":
		if len(args) < 1 {
			return nil, errors.New("ZCARD key")
		}
		return h.eng.KV.ZCard(args[0])
	case "ZINCRBY":
		if len(args) < 3 {
			return nil, errors.New("ZINCRBY key delta member")
		}
		d, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.ZIncrBy(args[0], d, args[2])
	case "ZRANK":
		if len(args) < 2 {
			return nil, errors.New("ZRANK key member")
		}
		r, ok, err := h.eng.KV.ZRank(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return r, nil
	case "ZREVRANK":
		if len(args) < 2 {
			return nil, errors.New("ZREVRANK key member")
		}
		r, ok, err := h.eng.KV.ZRevRank(args[0], args[1])
		if err != nil || !ok {
			return nil, err
		}
		return r, nil
	case "ZRANGE":
		return zrangeHTTP(h, args, false)
	case "ZREVRANGE":
		return zrangeHTTP(h, args, true)
	case "ZRANGEBYSCORE":
		return zrangeByScoreHTTP(h, args, false)
	case "ZREVRANGEBYSCORE":
		return zrangeByScoreHTTP(h, args, true)
	case "ZCOUNT":
		if len(args) < 3 {
			return nil, errors.New("ZCOUNT key min max")
		}
		return h.eng.KV.ZCount(args[0], args[1], args[2])
	case "ZPOPMIN":
		if len(args) < 1 {
			return nil, errors.New("ZPOPMIN key")
		}
		m, sc, ok, err := h.eng.KV.ZPopMin(args[0])
		if err != nil || !ok {
			return []any{}, err
		}
		return []any{m, sc}, nil
	case "ZPOPMAX":
		if len(args) < 1 {
			return nil, errors.New("ZPOPMAX key")
		}
		m, sc, ok, err := h.eng.KV.ZPopMax(args[0])
		if err != nil || !ok {
			return []any{}, err
		}
		return []any{m, sc}, nil

	// ─── pub/sub ───────────────────────────────────────────────────
	case "PUBLISH":
		if len(args) < 2 {
			return nil, errors.New("PUBLISH channel message")
		}
		return h.eng.PubSub.Publish(args[0], args[1]), nil
	case "PUBSUB":
		if len(args) < 1 {
			return nil, errors.New("PUBSUB CHANNELS|NUMSUB|NUMPAT [...]")
		}
		switch strings.ToUpper(args[0]) {
		case "CHANNELS":
			p := "*"
			if len(args) >= 2 {
				p = args[1]
			}
			return h.eng.PubSub.Channels(p), nil
		case "NUMSUB":
			return h.eng.PubSub.NumSub(args[1:]...), nil
		case "NUMPAT":
			return h.eng.PubSub.NumPat(), nil
		}
		return nil, errors.New("unknown PUBSUB subcommand")

	// ─── AI-native ─────────────────────────────────────────────────
	case "SEMANTIC_SET":
		if len(args) < 2 {
			return nil, errors.New("SEMANTIC_SET key value")
		}
		h.eng.Semantic.Set(args[0], args[1])
		return "OK", nil
	case "SEMANTIC_GET":
		if len(args) < 1 {
			return nil, errors.New("SEMANTIC_GET query")
		}
		v, score, ok := h.eng.Semantic.Get(args[0], float32(h.cfg.SemThreshold))
		h.eng.Metrics.RecordSemantic(ok)
		if !ok {
			return map[string]any{"hit": false, "score": score}, nil
		}
		return map[string]any{"hit": true, "value": v, "score": score}, nil
	case "CACHE_LLM":
		if len(args) < 2 {
			return nil, errors.New("CACHE_LLM prompt response")
		}
		h.eng.LLM.Set(args[0], args[1])
		return "OK", nil
	case "CACHE_LLM_GET":
		if len(args) < 1 {
			return nil, errors.New("CACHE_LLM_GET prompt")
		}
		v, score, ok := h.eng.LLM.Get(args[0], 0.88)
		h.eng.Metrics.RecordLLM(ok)
		if !ok {
			return map[string]any{"hit": false, "score": score}, nil
		}
		return map[string]any{"hit": true, "response": v, "score": score}, nil
	case "CACHE_LLM_STATS":
		return h.eng.LLM.Stats(), nil
	case "MEMORY_ADD":
		if len(args) < 2 {
			return nil, errors.New("MEMORY_ADD user text")
		}
		return h.eng.Memory.Add(args[0], strings.Join(args[1:], " "), nil), nil
	case "MEMORY_QUERY":
		if len(args) < 2 {
			return nil, errors.New("MEMORY_QUERY user query")
		}
		hits := h.eng.Memory.Query(args[0], strings.Join(args[1:], " "), 5, 0.3)
		return map[string]any{"hits": hits, "context": memory.Synthesize(hits)}, nil
	case "MEMORY_LIST":
		if len(args) < 1 {
			return nil, errors.New("MEMORY_LIST user")
		}
		return h.eng.Memory.List(args[0]), nil
	}

	return nil, errors.New("unknown command: " + cmd)
}

// ─── helpers shared between HTTP ZRANGE variants ───────────────────────

func zrangeHTTP(h *handlers, args []string, reverse bool) (any, error) {
	if len(args) < 3 {
		return nil, errors.New("ZRANGE key start stop [WITHSCORES]")
	}
	a, _ := strconv.Atoi(args[1])
	b, _ := strconv.Atoi(args[2])
	withScores := false
	for _, t := range args[3:] {
		if strings.EqualFold(t, "WITHSCORES") {
			withScores = true
		}
	}
	out, err := h.eng.KV.ZRange(args[0], a, b, withScores, reverse)
	if err != nil {
		return nil, err
	}
	return formatZRange(out, withScores), nil
}

func zrangeByScoreHTTP(h *handlers, args []string, reverse bool) (any, error) {
	if len(args) < 3 {
		return nil, errors.New("ZRANGEBYSCORE key min max [WITHSCORES] [LIMIT offset count]")
	}
	withScores := false
	offset, count := 0, -1
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "WITHSCORES":
			withScores = true
		case "LIMIT":
			if i+2 < len(args) {
				offset, _ = strconv.Atoi(args[i+1])
				count, _ = strconv.Atoi(args[i+2])
				i += 2
			}
		}
	}
	minArg, maxArg := args[1], args[2]
	if reverse {
		minArg, maxArg = args[2], args[1]
	}
	out, err := h.eng.KV.ZRangeByScore(args[0], minArg, maxArg, offset, count, reverse)
	if err != nil {
		return nil, err
	}
	return formatZRange(out, withScores), nil
}

func formatZRange(out []store.ZRangeResult, withScores bool) any {
	if !withScores {
		members := make([]string, len(out))
		for i, r := range out {
			members[i] = r.Member
		}
		return members
	}
	pairs := make([]map[string]any, len(out))
	for i, r := range out {
		pairs[i] = map[string]any{"member": r.Member, "score": r.Score}
	}
	return pairs
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
