package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// translateCmd handles TRANSLATE.* — multi-language translation cache.
//
//   TRANSLATE.SET source target text translation [EX sec | PX ms]
//   TRANSLATE.GET source target text                 -> bulk or nil
//   TRANSLATE.MGET source target text1 text2 text3
//        -> array of {text, translation, hit}
//   TRANSLATE.FORGET source target text              -> int
//   TRANSLATE.PURGE [SOURCE s] [TARGET t]            -> int dropped
//   TRANSLATE.SETCAP n
//   TRANSLATE.SETCOST usd
//   TRANSLATE.STATS                                  -> per-pair hit rate
func (c *conn) translateCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'translate.set'")
			return
		}
		ttl, err := parseTransTTL(args[4:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.Translate.Set(args[0], args[1], args[2], args[3], ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'translate.get'")
			return
		}
		v, ok := c.eng.Translate.Get(args[0], args[1], args[2])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "MGET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'translate.mget'")
			return
		}
		rows := c.eng.Translate.MGet(args[0], args[1], args[2:])
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			hitInt := "0"
			if r.Hit {
				hitInt = "1"
			}
			out = append(out, []any{
				"text", r.Text,
				"translation", r.Translation,
				"hit", hitInt,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'translate.forget'")
			return
		}
		if c.eng.Translate.Forget(args[0], args[1], args[2]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		source := ""
		target := ""
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "SOURCE":
				source = val
			case "TARGET":
				target = val
			default:
				writeError(c.bw, "unknown TRANSLATE.PURGE option: "+key)
				return
			}
			i += 2
		}
		writeInt(c.bw, int64(c.eng.Translate.Purge(source, target)))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'translate.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil {
			writeError(c.bw, "cap must be an integer")
			return
		}
		c.eng.Translate.SetCap(n)
		writeSimple(c.bw, "OK")
	case "SETCOST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'translate.setcost'")
			return
		}
		usd, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "cost must be a float")
			return
		}
		c.eng.Translate.SetCostUSD(usd)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.Translate.Stats()
		pairsAny := make([]any, 0, len(s.Pairs))
		for _, p := range s.Pairs {
			pairsAny = append(pairsAny, []any{
				"pair", p.Pair,
				"hits", strconv.FormatInt(p.Hits, 10),
				"misses", strconv.FormatInt(p.Misses, 10),
				"sets", strconv.FormatInt(p.Sets, 10),
				"hit_rate", strconv.FormatFloat(p.HitRate, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"entries", strconv.Itoa(s.Entries),
			"cap", strconv.Itoa(s.Cap),
			"total_gets", strconv.FormatInt(s.TotalGets, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"saved_calls", strconv.FormatInt(s.SavedCalls, 10),
			"saved_usd", strconv.FormatFloat(s.SavedUSD, 'f', 4, 64),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
			"total_evicts", strconv.FormatInt(s.TotalEvicts, 10),
			"cost_usd", strconv.FormatFloat(s.CostUSD, 'f', 4, 64),
			"pairs", pairsAny,
		})
	default:
		writeError(c.bw, "unknown TRANSLATE subcommand: "+sub)
	}
}

func parseTransTTL(rest []string) (time.Duration, error) {
	if len(rest) == 0 {
		return 0, nil
	}
	if len(rest) < 2 {
		return 0, &v12Err{msg: "TTL option needs a value"}
	}
	switch strings.ToUpper(rest[0]) {
	case "EX":
		n, err := strconv.Atoi(rest[1])
		if err != nil || n <= 0 {
			return 0, &v12Err{msg: "EX must be a positive integer"}
		}
		return time.Duration(n) * time.Second, nil
	case "PX":
		n, err := strconv.Atoi(rest[1])
		if err != nil || n <= 0 {
			return 0, &v12Err{msg: "PX must be a positive integer"}
		}
		return time.Duration(n) * time.Millisecond, nil
	}
	return 0, &v12Err{msg: "TTL option must be EX <sec> or PX <ms>"}
}

// embedMatCmd handles EMBED.MAT.* — inline embedding matrix.
//
//   EMBED.MAT.SET matrix-id row-id v1,v2,v3,...
//   EMBED.MAT.DEL matrix-id row-id            -> int
//   EMBED.MAT.TOPK matrix-id query-vec K [FILTER prefix]
//        -> array of {row_id, score}
//   EMBED.MAT.DOT matrix-id row-a row-b       -> bulk float
//   EMBED.MAT.COSINE matrix-id row-a row-b    -> bulk float
//   EMBED.MAT.LEN matrix-id                   -> int
//   EMBED.MAT.LIST matrix-id [PREFIX p]       -> row-ids
//   EMBED.MAT.FORGET matrix-id                -> int
//   EMBED.MAT.STATS
func (c *conn) embedMatCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'embed.mat.set'")
			return
		}
		vec, err := parseVecCSV(args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.EmbedMat.Set(args[0], args[1], vec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DEL":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'embed.mat.del'")
			return
		}
		if c.eng.EmbedMat.Del(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "TOPK":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'embed.mat.topk'")
			return
		}
		query, err := parseVecCSV(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		k, err := strconv.Atoi(args[2])
		if err != nil || k <= 0 {
			writeError(c.bw, "K must be a positive integer")
			return
		}
		opts := llmstack.TopKOpts{K: k}
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "FILTER" {
				writeError(c.bw, "unknown EMBED.MAT.TOPK option: "+key)
				return
			}
			opts.Filter = val
			i += 2
		}
		hits := c.eng.EmbedMat.TopK(args[0], query, opts)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"row_id", h.RowID,
				"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
			})
		}
		writeValue(c.bw, out)
	case "DOT", "COSINE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments")
			return
		}
		v, ok := c.eng.EmbedMat.Cosine(args[0], args[1], args[2])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', 6, 64))
	case "LEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'embed.mat.len'")
			return
		}
		n, ok := c.eng.EmbedMat.Len(args[0])
		if !ok {
			writeInt(c.bw, 0)
			return
		}
		writeInt(c.bw, int64(n))
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'embed.mat.list'")
			return
		}
		prefix := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "PREFIX" {
				writeError(c.bw, "unknown EMBED.MAT.LIST option: "+key)
				return
			}
			prefix = val
			i += 2
		}
		writeArray(c.bw, c.eng.EmbedMat.List(args[0], prefix))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'embed.mat.forget'")
			return
		}
		writeInt(c.bw, int64(c.eng.EmbedMat.Forget(args[0])))
	case "STATS":
		s := c.eng.EmbedMat.Stats()
		matsAny := make([]any, 0, len(s.Matrices))
		for _, m := range s.Matrices {
			matsAny = append(matsAny, []any{
				"matrix_id", m.MatrixID,
				"rows", strconv.Itoa(m.Rows),
				"dim", strconv.Itoa(m.Dim),
			})
		}
		writeValue(c.bw, []any{
			"matrices", matsAny,
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_topks", strconv.FormatInt(s.TotalTopKs, 10),
			"total_rows", strconv.FormatInt(s.TotalRows, 10),
		})
	default:
		writeError(c.bw, "unknown EMBED.MAT subcommand: "+sub)
	}
}

// opcacheCmd handles OPCACHE.* — deterministic LLM op memoisation.
//
//   OPCACHE.SET op-id input output [MODEL m] [PARAMS json] [EX sec | PX ms]
//   OPCACHE.GET op-id input [MODEL m] [PARAMS json]   -> bulk or nil
//   OPCACHE.FORGET op-id input [MODEL m] [PARAMS json] -> int
//   OPCACHE.PURGE [OP op-id]                           -> int dropped
//   OPCACHE.SETCAP n
//   OPCACHE.SETCOST usd
//   OPCACHE.STATS
func (c *conn) opcacheCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'opcache.set'")
			return
		}
		key := llmstack.OpKey{OpID: args[0], Input: args[1]}
		ttl, err := parseOpcacheTail(args[3:], &key)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.OpCache.Set(key, args[2], ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'opcache.get'")
			return
		}
		key := llmstack.OpKey{OpID: args[0], Input: args[1]}
		if _, err := parseOpcacheTail(args[2:], &key); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		v, ok := c.eng.OpCache.Get(key)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'opcache.forget'")
			return
		}
		key := llmstack.OpKey{OpID: args[0], Input: args[1]}
		if _, err := parseOpcacheTail(args[2:], &key); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if c.eng.OpCache.Forget(key) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PURGE":
		opID := ""
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "OP" {
				writeError(c.bw, "unknown OPCACHE.PURGE option: "+key)
				return
			}
			opID = val
			i += 2
		}
		writeInt(c.bw, int64(c.eng.OpCache.Purge(opID)))
	case "SETCAP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'opcache.setcap'")
			return
		}
		n, err := strconv.Atoi(args[0])
		if err != nil {
			writeError(c.bw, "cap must be an integer")
			return
		}
		c.eng.OpCache.SetCap(n)
		writeSimple(c.bw, "OK")
	case "SETCOST":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'opcache.setcost'")
			return
		}
		usd, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "cost must be a float")
			return
		}
		c.eng.OpCache.SetCostUSD(usd)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.OpCache.Stats()
		opsAny := make([]any, 0, len(s.Ops))
		for _, o := range s.Ops {
			opsAny = append(opsAny, []any{
				"op_id", o.OpID,
				"hits", strconv.FormatInt(o.Hits, 10),
				"misses", strconv.FormatInt(o.Misses, 10),
				"sets", strconv.FormatInt(o.Sets, 10),
				"hit_rate", strconv.FormatFloat(o.HitRate, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"entries", strconv.Itoa(s.Entries),
			"cap", strconv.Itoa(s.Cap),
			"total_gets", strconv.FormatInt(s.TotalGets, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"saved_calls", strconv.FormatInt(s.SavedCalls, 10),
			"saved_usd", strconv.FormatFloat(s.SavedUSD, 'f', 4, 64),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
			"total_evicts", strconv.FormatInt(s.TotalEvicts, 10),
			"cost_usd", strconv.FormatFloat(s.CostUSD, 'f', 4, 64),
			"ops", opsAny,
		})
	default:
		writeError(c.bw, "unknown OPCACHE subcommand: "+sub)
	}
}

// parseOpcacheTail extracts MODEL / PARAMS / EX|PX options. The
// MODEL and PARAMS values are stamped into `key`; the TTL is
// returned.
func parseOpcacheTail(rest []string, key *llmstack.OpKey) (time.Duration, error) {
	var ttl time.Duration
	i := 0
	for i+1 < len(rest)+1 && i < len(rest) {
		k := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return 0, &v12Err{msg: k + " needs a value"}
		}
		v := rest[i+1]
		switch k {
		case "MODEL":
			key.Model = v
		case "PARAMS":
			key.Params = v
		case "EX":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return 0, &v12Err{msg: "EX must be positive integer"}
			}
			ttl = time.Duration(n) * time.Second
		case "PX":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return 0, &v12Err{msg: "PX must be positive integer"}
			}
			ttl = time.Duration(n) * time.Millisecond
		default:
			return 0, &v12Err{msg: "unknown OPCACHE option: " + k}
		}
		i += 2
	}
	return ttl, nil
}

func parseVecCSV(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, &v12Err{msg: "vector component not parseable: " + p}
		}
		out = append(out, f)
	}
	return out, nil
}

type v12Err struct{ msg string }

func (e *v12Err) Error() string { return e.msg }
