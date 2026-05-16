package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// contractCmd handles CONTRACT.* — LLM tool-call signature validation.
//
//   CONTRACT.REGISTER tool-id schema-json
//   CONTRACT.UNREGISTER tool-id
//   CONTRACT.VALIDATE call-json
//        → [valid, tool_id, errors[]]
//   CONTRACT.LIST
//   CONTRACT.STATS
func (c *conn) contractCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'contract.register'")
			return
		}
		if err := c.eng.Contract.Register(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNREGISTER":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'contract.unregister'")
			return
		}
		if c.eng.Contract.Unregister(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "VALIDATE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'contract.validate'")
			return
		}
		r, err := c.eng.Contract.Validate(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		validInt := "0"
		if r.Valid {
			validInt = "1"
		}
		errs := make([]any, 0, len(r.Errors))
		for _, e := range r.Errors {
			errs = append(errs, []any{
				"path", e.Path,
				"message", e.Message,
			})
		}
		writeValue(c.bw, []any{
			"valid", validInt,
			"tool_id", r.ToolID,
			"errors", errs,
		})
	case "LIST":
		rows := c.eng.Contract.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"tool_id", r.ToolID,
				"schema", r.Schema,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Contract.Stats()
		writeArray(c.bw, []string{
			"tools", strconv.Itoa(s.Tools),
			"total_validates", strconv.FormatInt(s.TotalValidates, 10),
			"total_valid", strconv.FormatInt(s.TotalValid, 10),
			"total_invalid", strconv.FormatInt(s.TotalInvalid, 10),
		})
	default:
		writeError(c.bw, "unknown CONTRACT subcommand: "+sub)
	}
}

// timelineCmd handles TIMELINE.* — per-key event log.
//
//   TIMELINE.APPEND key event [TS unix-ms] [KIND k]
//   TIMELINE.RANGE key [SINCE ms] [UNTIL ms] [KIND k] [LIMIT n]
//   TIMELINE.RECENT key seconds [KIND k] [LIMIT n]
//   TIMELINE.LEN key
//   TIMELINE.FORGET key
//   TIMELINE.KEYS
//   TIMELINE.STATS
func (c *conn) timelineCmd(sub string, args []string) {
	switch sub {
	case "APPEND":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'timeline.append'")
			return
		}
		var tsMS int64
		kind := ""
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "TS":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "TS must be a non-negative integer")
					return
				}
				tsMS = n
			case "KIND":
				kind = val
			default:
				writeError(c.bw, "unknown TIMELINE.APPEND option: "+key)
				return
			}
			i += 2
		}
		if err := c.eng.Timeline.Append(args[0], args[1], tsMS, kind); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RANGE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'timeline.range'")
			return
		}
		opts := llmstack.RangeOpts{}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "SINCE":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil {
					writeError(c.bw, "SINCE must be an integer")
					return
				}
				opts.SinceMS = n
			case "UNTIL":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil {
					writeError(c.bw, "UNTIL must be an integer")
					return
				}
				opts.UntilMS = n
			case "KIND":
				opts.Kind = val
			case "LIMIT":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be a non-negative integer")
					return
				}
				opts.Limit = n
			default:
				writeError(c.bw, "unknown TIMELINE.RANGE option: "+key)
				return
			}
			i += 2
		}
		rows := c.eng.Timeline.Range(args[0], opts)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"ts", strconv.FormatInt(r.TS, 10),
				"kind", r.Kind,
				"event", r.Event,
			})
		}
		writeValue(c.bw, out)
	case "RECENT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'timeline.recent'")
			return
		}
		seconds, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || seconds <= 0 {
			writeError(c.bw, "seconds must be a positive integer")
			return
		}
		kind := ""
		limit := 0
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "KIND":
				kind = val
			case "LIMIT":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be a non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown TIMELINE.RECENT option: "+key)
				return
			}
			i += 2
		}
		rows := c.eng.Timeline.Recent(args[0], seconds, kind, limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"ts", strconv.FormatInt(r.TS, 10),
				"kind", r.Kind,
				"event", r.Event,
			})
		}
		writeValue(c.bw, out)
	case "LEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'timeline.len'")
			return
		}
		n, _ := c.eng.Timeline.Len(args[0])
		writeInt(c.bw, int64(n))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'timeline.forget'")
			return
		}
		if c.eng.Timeline.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "KEYS":
		writeArray(c.bw, c.eng.Timeline.Keys())
	case "STATS":
		s := c.eng.Timeline.Stats()
		writeArray(c.bw, []string{
			"keys", strconv.Itoa(s.Keys),
			"total_events", strconv.Itoa(s.TotalEvents),
			"total_appends", strconv.FormatInt(s.TotalAppends, 10),
			"total_ranges", strconv.FormatInt(s.TotalRanges, 10),
			"total_evicts", strconv.FormatInt(s.TotalEvicts, 10),
		})
	default:
		writeError(c.bw, "unknown TIMELINE subcommand: "+sub)
	}
}

// lshCmd handles HASH.LSH.* — random-hyperplane LSH index.
//
//   HASH.LSH.CREATE bucket-id dim [BITS k]
//   HASH.LSH.SET bucket-id row-id v,v,v,...
//   HASH.LSH.DEL bucket-id row-id
//   HASH.LSH.SIGN bucket-id v,v,v,...        → hex signature
//   HASH.LSH.NEIGHBORS bucket-id v,v,v,... [RADIUS r] [K k]
//   HASH.LSH.LEN bucket-id
//   HASH.LSH.FORGET bucket-id
//   HASH.LSH.STATS
func (c *conn) lshCmd(sub string, args []string) {
	switch sub {
	case "CREATE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.create'")
			return
		}
		dim, err := strconv.Atoi(args[1])
		if err != nil || dim < 1 {
			writeError(c.bw, "dim must be a positive integer")
			return
		}
		bits := 0
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "BITS" {
				writeError(c.bw, "unknown HASH.LSH.CREATE option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil {
				writeError(c.bw, "BITS must be an integer")
				return
			}
			bits = n
			i += 2
		}
		if err := c.eng.LSH.Create(args[0], dim, bits); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.set'")
			return
		}
		vec, err := parseVecCSV(args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.LSH.Set(args[0], args[1], vec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DEL":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.del'")
			return
		}
		if c.eng.LSH.Del(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SIGN":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.sign'")
			return
		}
		vec, err := parseVecCSV(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		sig, ok := c.eng.LSH.Sign(args[0], vec)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, strconv.FormatUint(sig, 16))
	case "NEIGHBORS":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.neighbors'")
			return
		}
		vec, err := parseVecCSV(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		opts := llmstack.NeighborsOpts{}
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "K":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "K must be a positive integer")
					return
				}
				opts.K = n
			case "RADIUS":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "RADIUS must be a non-negative integer")
					return
				}
				opts.Radius = n
			default:
				writeError(c.bw, "unknown HASH.LSH.NEIGHBORS option: "+key)
				return
			}
			i += 2
		}
		hits := c.eng.LSH.Neighbors(args[0], vec, opts)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"row_id", h.RowID,
				"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
			})
		}
		writeValue(c.bw, out)
	case "LEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.len'")
			return
		}
		n, _ := c.eng.LSH.Len(args[0])
		writeInt(c.bw, int64(n))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'hash.lsh.forget'")
			return
		}
		writeInt(c.bw, int64(c.eng.LSH.Forget(args[0])))
	case "STATS":
		s := c.eng.LSH.Stats()
		bucketsAny := make([]any, 0, len(s.Buckets))
		for _, b := range s.Buckets {
			bucketsAny = append(bucketsAny, []any{
				"bucket_id", b.BucketID,
				"rows", strconv.Itoa(b.Rows),
				"dim", strconv.Itoa(b.Dim),
				"bits", strconv.Itoa(b.Bits),
				"occupied_signatures", strconv.Itoa(b.OccupiedSigs),
				"avg_rows_per_bucket", strconv.FormatFloat(b.AvgPerBucket, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"buckets", bucketsAny,
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_neighbors", strconv.FormatInt(s.TotalNeighbors, 10),
			"total_rows", strconv.FormatInt(s.TotalRows, 10),
		})
	default:
		writeError(c.bw, "unknown HASH.LSH subcommand: "+sub)
	}
}
