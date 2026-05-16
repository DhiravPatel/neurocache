package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// streamParseCmd handles STREAM.PARSE.* — incremental JSON parser.
//
//   STREAM.PARSE.OPEN stream-id
//   STREAM.PARSE.PUSH stream-id chunk
//        → array of {key, value, json_type} for completed fields
//   STREAM.PARSE.COMPLETE stream-id
//        → [unparsed_bytes, buffer, fields_emitted]
//   STREAM.PARSE.STATUS stream-id
//   STREAM.PARSE.FORGET stream-id
//   STREAM.PARSE.STATS
func (c *conn) streamParseCmd(sub string, args []string) {
	switch sub {
	case "OPEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.parse.open'")
			return
		}
		if err := c.eng.StreamParser.Open(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PUSH":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'stream.parse.push'")
			return
		}
		fields, err := c.eng.StreamParser.Push(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out := make([]any, 0, len(fields))
		for _, f := range fields {
			out = append(out, []any{
				"key", f.Key,
				"value", f.Value,
				"json_type", f.JSONType,
			})
		}
		writeValue(c.bw, out)
	case "COMPLETE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.parse.complete'")
			return
		}
		r, ok := c.eng.StreamParser.Complete(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"unparsed_bytes", strconv.Itoa(r.UnparsedBytes),
			"buffer", r.Buffer,
			"fields_emitted", strconv.Itoa(r.FieldsEmitted),
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.parse.status'")
			return
		}
		r, ok := c.eng.StreamParser.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		doneInt := "0"
		if r.Done {
			doneInt = "1"
		}
		writeArray(c.bw, []string{
			"pos", strconv.Itoa(r.Pos),
			"bytes", strconv.Itoa(r.Bytes),
			"depth", strconv.Itoa(r.Depth),
			"done", doneInt,
			"fields_emitted", strconv.Itoa(r.FieldsEmitted),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'stream.parse.forget'")
			return
		}
		if c.eng.StreamParser.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.StreamParser.Stats()
		writeArray(c.bw, []string{
			"active_streams", strconv.Itoa(s.ActiveStreams),
			"total_opens", strconv.FormatInt(s.TotalOpens, 10),
			"total_pushes", strconv.FormatInt(s.TotalPushes, 10),
			"total_completes", strconv.FormatInt(s.TotalCompletes, 10),
			"total_fields", strconv.FormatInt(s.TotalFields, 10),
		})
	default:
		writeError(c.bw, "unknown STREAM.PARSE subcommand: "+sub)
	}
}

// llmLimiterCmd handles LIMITER.LLM.* — token-aware rate limiter.
//
//   LIMITER.LLM.CONFIG provider tokens-per-min [TENANT t]
//   LIMITER.LLM.RESERVE provider tokens [TENANT t]
//        → [allowed, reserved, remaining, reset_ms]
//   LIMITER.LLM.RECORD provider actual [TENANT t] [RESERVED n]
//   LIMITER.LLM.USAGE provider [TENANT t]
//   LIMITER.LLM.RESET [PROVIDER p] [TENANT t]
//   LIMITER.LLM.ALL
//   LIMITER.LLM.STATS
func (c *conn) llmLimiterCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'limiter.llm.config'")
			return
		}
		tpm, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || tpm <= 0 {
			writeError(c.bw, "tokens-per-min must be a positive integer")
			return
		}
		tenant := ""
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TENANT" {
				writeError(c.bw, "unknown LIMITER.LLM.CONFIG option: "+key)
				return
			}
			tenant = val
			i += 2
		}
		if err := c.eng.LLMLimiter.Config(args[0], tenant, tpm); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESERVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'limiter.llm.reserve'")
			return
		}
		tokens, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || tokens < 0 {
			writeError(c.bw, "tokens must be a non-negative integer")
			return
		}
		tenant := ""
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TENANT" {
				writeError(c.bw, "unknown LIMITER.LLM.RESERVE option: "+key)
				return
			}
			tenant = val
			i += 2
		}
		r, err := c.eng.LLMLimiter.Reserve(args[0], tenant, tokens)
		if err != nil {
			writeTypedError(c.bw, "NOCAP", err.Error())
			return
		}
		allowedInt := "0"
		if r.Allowed {
			allowedInt = "1"
		}
		writeArray(c.bw, []string{
			"allowed", allowedInt,
			"reserved", strconv.FormatInt(r.Reserved, 10),
			"remaining", strconv.FormatInt(r.Remaining, 10),
			"reset_ms", strconv.FormatInt(r.ResetMS, 10),
		})
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'limiter.llm.record'")
			return
		}
		actual, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || actual < 0 {
			writeError(c.bw, "actual must be a non-negative integer")
			return
		}
		tenant := ""
		reserved := int64(0)
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TENANT":
				tenant = val
			case "RESERVED":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "RESERVED must be non-negative")
					return
				}
				reserved = n
			default:
				writeError(c.bw, "unknown LIMITER.LLM.RECORD option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.LLMLimiter.Record(args[0], tenant, actual, reserved); err != nil {
			writeTypedError(c.bw, "NOCAP", err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "USAGE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'limiter.llm.usage'")
			return
		}
		tenant := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TENANT" {
				writeError(c.bw, "unknown LIMITER.LLM.USAGE option: "+key)
				return
			}
			tenant = val
			i += 2
		}
		r, ok := c.eng.LLMLimiter.Usage(args[0], tenant)
		if !ok {
			writeTypedError(c.bw, "NOCAP", "no cap configured")
			return
		}
		writeArray(c.bw, []string{
			"provider", r.Provider,
			"tenant", r.Tenant,
			"cap_per_min", strconv.FormatInt(r.CapPerMin, 10),
			"used", strconv.FormatInt(r.Used, 10),
			"remaining", strconv.FormatInt(r.Remaining, 10),
			"reset_ms", strconv.FormatInt(r.ResetMS, 10),
		})
	case "RESET":
		provider, tenant := "", ""
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "PROVIDER":
				provider = val
			case "TENANT":
				tenant = val
			default:
				writeError(c.bw, "unknown LIMITER.LLM.RESET option: "+key)
				return
			}
			i += 2
		}
		writeInt(c.bw, int64(c.eng.LLMLimiter.Reset(provider, tenant)))
	case "ALL":
		rows := c.eng.LLMLimiter.All()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"provider", r.Provider,
				"tenant", r.Tenant,
				"cap_per_min", strconv.FormatInt(r.CapPerMin, 10),
				"used", strconv.FormatInt(r.Used, 10),
				"remaining", strconv.FormatInt(r.Remaining, 10),
				"reset_ms", strconv.FormatInt(r.ResetMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.LLMLimiter.Stats()
		writeArray(c.bw, []string{
			"configured_keys", strconv.Itoa(s.ConfiguredKeys),
			"total_reserves", strconv.FormatInt(s.TotalReserves, 10),
			"total_allowed", strconv.FormatInt(s.TotalAllowed, 10),
			"total_rejected", strconv.FormatInt(s.TotalRejected, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
		})
	default:
		writeError(c.bw, "unknown LIMITER.LLM subcommand: "+sub)
	}
}

// cacheLayersCmd handles CACHE.LAYERS.* — multi-layer lookup.
//
//   CACHE.LAYERS.SET layer key value [EX sec | PX ms] [EMBED v,v,...]
//   CACHE.LAYERS.LOOKUP key [TEXT semantic-text] [EMBED v,v,...]
//        → [hit_layer, value, score]
//   CACHE.LAYERS.FORGET key [LAYER l]
//   CACHE.LAYERS.PURGE [LAYER l]
//   CACHE.LAYERS.SET_THRESHOLD t
//   CACHE.LAYERS.STATS
func (c *conn) cacheLayersCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'cache.layers.set'")
			return
		}
		opts := llmstack.LayerSetOpts{}
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "EX":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "EX must be a positive integer")
					return
				}
				opts.TTL = time.Duration(n) * time.Second
			case "PX":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "PX must be a positive integer")
					return
				}
				opts.TTL = time.Duration(n) * time.Millisecond
			case "EMBED":
				vec, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, err.Error())
					return
				}
				opts.Vec = vec
			default:
				writeError(c.bw, "unknown CACHE.LAYERS.SET option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.CacheLayers.Set(args[0], args[1], args[2], opts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LOOKUP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.layers.lookup'")
			return
		}
		opts := llmstack.LookupOpts{}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TEXT":
				opts.Text = val
			case "EMBED":
				vec, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, err.Error())
					return
				}
				opts.Vec = vec
			default:
				writeError(c.bw, "unknown CACHE.LAYERS.LOOKUP option: "+key)
				return
			}
			i += 2
		}
		r := c.eng.CacheLayers.Lookup(args[0], opts)
		writeArray(c.bw, []string{
			"hit_layer", r.HitLayer,
			"value", r.Value,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.layers.forget'")
			return
		}
		layer := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "LAYER" {
				writeError(c.bw, "unknown CACHE.LAYERS.FORGET option: "+key)
				return
			}
			layer = val
			i += 2
		}
		writeInt(c.bw, int64(c.eng.CacheLayers.Forget(layer, args[0])))
	case "PURGE":
		layer := ""
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "LAYER" {
				writeError(c.bw, "unknown CACHE.LAYERS.PURGE option: "+key)
				return
			}
			layer = val
			i += 2
		}
		writeInt(c.bw, int64(c.eng.CacheLayers.Purge(layer)))
	case "SET_THRESHOLD":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.layers.set_threshold'")
			return
		}
		t, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			writeError(c.bw, "threshold must be a float")
			return
		}
		c.eng.CacheLayers.SetThreshold(t)
		writeSimple(c.bw, "OK")
	case "STATS":
		s := c.eng.CacheLayers.Stats()
		writeArray(c.bw, []string{
			"exact_size", strconv.Itoa(s.ExactSize),
			"semantic_size", strconv.Itoa(s.SemanticSize),
			"negative_size", strconv.Itoa(s.NegativeSize),
			"semantic_threshold", strconv.FormatFloat(s.Threshold, 'f', 4, 64),
			"total_lookups", strconv.FormatInt(s.TotalLookups, 10),
			"exact_hits", strconv.FormatInt(s.ExactHits, 10),
			"semantic_hits", strconv.FormatInt(s.SemanticHits, 10),
			"negative_hits", strconv.FormatInt(s.NegativeHits, 10),
			"misses", strconv.FormatInt(s.Misses, 10),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown CACHE.LAYERS subcommand: "+sub)
	}
}
