package resp

import (
	"strconv"
	"strings"
	"time"
)

// docFreshCmd handles DOC.FRESH.* — RAG-corpus freshness tracker.
func (c *conn) docFreshCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "usage: DOC.FRESH.REGISTER doc-id source [HASH h] [TTL seconds]")
			return
		}
		hash := ""
		var ttl time.Duration
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "HASH":
				hash = val
			case "TTL":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n < 0 {
					writeError(c.bw, "TTL must be non-negative integer (seconds)")
					return
				}
				ttl = time.Duration(n) * time.Second
			default:
				writeError(c.bw, "unknown DOC.FRESH.REGISTER option: "+key)
				return
			}
		}
		if err := c.eng.DocFresh.Register(args[0], args[1], hash, ttl); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STAMP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'doc.fresh.stamp'")
			return
		}
		hash := ""
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "HASH" {
				writeError(c.bw, "unknown DOC.FRESH.STAMP option: "+key)
				return
			}
			hash = args[i+1]
		}
		if err := c.eng.DocFresh.Stamp(args[0], hash); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CHECK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'doc.fresh.check'")
			return
		}
		r := c.eng.DocFresh.Check(args[0])
		writeArray(c.bw, []string{
			"doc_id", r.DocID,
			"status", r.Status,
			"age_seconds", strconv.FormatInt(r.AgeSeconds, 10),
			"hash", r.Hash,
			"registered_hash", r.RegisteredHash,
			"source", r.Source,
			"reason", r.Reason,
		})
	case "INVALIDATE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'doc.fresh.invalidate'")
			return
		}
		reason := ""
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "REASON" {
				writeError(c.bw, "unknown DOC.FRESH.INVALIDATE option: "+key)
				return
			}
			reason = args[i+1]
		}
		if err := c.eng.DocFresh.Invalidate(args[0], reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BULKCHECK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'doc.fresh.bulkcheck'")
			return
		}
		rows := c.eng.DocFresh.BulkCheck(args)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"doc_id", r.DocID,
				"status", r.Status,
				"age_seconds", strconv.FormatInt(r.AgeSeconds, 10),
				"hash", r.Hash,
				"registered_hash", r.RegisteredHash,
				"source", r.Source,
				"reason", r.Reason,
			})
		}
		writeValue(c.bw, out)
	case "STALE":
		limit := 0
		for i := 0; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown DOC.FRESH.STALE option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows := c.eng.DocFresh.Stale(limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"doc_id", r.DocID,
				"status", r.Status,
				"reason", r.Reason,
				"stale_since_unix", strconv.FormatInt(r.StaleSince, 10),
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		writeArray(c.bw, c.eng.DocFresh.List())
	case "DROP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'doc.fresh.drop' (use ALL or doc-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.DocFresh.Drop(args[0])))
	case "STATS":
		s := c.eng.DocFresh.Stats()
		writeArray(c.bw, []string{
			"docs", strconv.Itoa(s.Docs),
			"stale_docs", strconv.Itoa(s.StaleDocs),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
			"total_stamps", strconv.FormatInt(s.TotalStamps, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_invalidates", strconv.FormatInt(s.TotalInvalidates, 10),
		})
	default:
		writeError(c.bw, "unknown DOC.FRESH subcommand: "+sub)
	}
}

// cacheWarmCmd handles CACHE.WARM.* — cache warming dataset.
func (c *conn) cacheWarmCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 2 {
			writeError(c.bw, "usage: CACHE.WARM.RECORD warm-id query [WEIGHT w]")
			return
		}
		weight := 0.0
		for i := 2; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "WEIGHT" {
				writeError(c.bw, "unknown CACHE.WARM.RECORD option: "+key)
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				writeError(c.bw, "WEIGHT must be float")
				return
			}
			weight = f
		}
		if err := c.eng.CacheWarm.Record(args[0], args[1], weight); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PLAN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.warm.plan'")
			return
		}
		limit := 0
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown CACHE.WARM.PLAN option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.CacheWarm.Plan(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			warmedInt := "0"
			if r.Warmed {
				warmedInt = "1"
			}
			out = append(out, []any{
				"query", r.Query,
				"weight", strconv.FormatFloat(r.Weight, 'f', 4, 64),
				"warmed", warmedInt,
			})
		}
		writeValue(c.bw, out)
	case "MARK":
		if len(args) < 2 {
			writeError(c.bw, "usage: CACHE.WARM.MARK warm-id query")
			return
		}
		if err := c.eng.CacheWarm.Mark(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PROGRESS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.warm.progress'")
			return
		}
		p, ok := c.eng.CacheWarm.Progress(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"warm_id", p.WarmID,
			"total", strconv.Itoa(p.Total),
			"warmed", strconv.Itoa(p.Warmed),
			"remaining", strconv.Itoa(p.Remaining),
			"pct_complete", strconv.FormatFloat(p.PctComplete, 'f', 4, 64),
		})
	case "MINSIM":
		if len(args) < 2 {
			writeError(c.bw, "usage: CACHE.WARM.MINSIM warm-id f")
			return
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "min_sim must be float")
			return
		}
		if err := c.eng.CacheWarm.SetMinSim(args[0], f); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "LIST":
		writeArray(c.bw, c.eng.CacheWarm.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.warm.reset' (use ALL or warm-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.CacheWarm.Reset(args[0])))
	case "STATS":
		s := c.eng.CacheWarm.Stats()
		writeArray(c.bw, []string{
			"plans", strconv.Itoa(s.Plans),
			"total_entries", strconv.Itoa(s.TotalEntries),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_plans", strconv.FormatInt(s.TotalPlans, 10),
			"total_marks", strconv.FormatInt(s.TotalMarks, 10),
		})
	default:
		writeError(c.bw, "unknown CACHE.WARM subcommand: "+sub)
	}
}

// fairQueueCmd handles FAIRQUEUE.* — weighted-fair tenant queue.
func (c *conn) fairQueueCmd(sub string, args []string) {
	switch sub {
	case "CONFIG":
		if len(args) < 1 {
			writeError(c.bw, "usage: FAIRQUEUE.CONFIG queue-id [TENANT t WEIGHT n]+")
			return
		}
		// Walk args in 4-element TENANT/<name>/WEIGHT/<value> groups
		weights := map[string]float64{}
		for i := 1; i < len(args); i += 4 {
			if i+3 >= len(args) {
				writeError(c.bw, "incomplete TENANT/WEIGHT group at arg "+strconv.Itoa(i))
				return
			}
			if !strings.EqualFold(args[i], "TENANT") || !strings.EqualFold(args[i+2], "WEIGHT") {
				writeError(c.bw, "expected TENANT t WEIGHT n at arg "+strconv.Itoa(i))
				return
			}
			f, err := strconv.ParseFloat(args[i+3], 64)
			if err != nil {
				writeError(c.bw, "WEIGHT must be float at arg "+strconv.Itoa(i+3))
				return
			}
			weights[args[i+1]] = f
		}
		if err := c.eng.FairQueue.Configure(args[0], weights); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ENQUEUE":
		if len(args) < 3 {
			writeError(c.bw, "usage: FAIRQUEUE.ENQUEUE queue-id tenant request-id [PAYLOAD p]")
			return
		}
		payload := ""
		for i := 3; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "PAYLOAD" {
				writeError(c.bw, "unknown FAIRQUEUE.ENQUEUE option: "+key)
				return
			}
			payload = args[i+1]
		}
		depth, err := c.eng.FairQueue.Enqueue(args[0], args[1], args[2], payload)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(depth))
	case "DEQUEUE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fairqueue.dequeue'")
			return
		}
		r, ok := c.eng.FairQueue.Dequeue(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"tenant", r.Tenant,
			"request_id", r.RequestID,
			"payload", r.Payload,
			"waited_ms", strconv.FormatInt(r.WaitedMS, 10),
		})
	case "PEEK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fairqueue.peek'")
			return
		}
		limit := 10
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown FAIRQUEUE.PEEK option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.FairQueue.Peek(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"tenant", r.Tenant,
				"request_id", r.RequestID,
				"waited_ms", strconv.FormatInt(r.WaitedMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "LEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fairqueue.len'")
			return
		}
		tenant := ""
		for i := 1; i+1 < len(args)+1 && i < len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "TENANT" {
				writeError(c.bw, "unknown FAIRQUEUE.LEN option: "+key)
				return
			}
			tenant = args[i+1]
		}
		n, ok := c.eng.FairQueue.Len(args[0], tenant)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeInt(c.bw, int64(n))
	case "DROPTENANT":
		if len(args) < 2 {
			writeError(c.bw, "usage: FAIRQUEUE.DROPTENANT queue-id tenant")
			return
		}
		dropped, ok := c.eng.FairQueue.DropTenant(args[0], args[1])
		if !ok {
			writeInt(c.bw, 0)
			return
		}
		writeInt(c.bw, int64(dropped))
	case "LIST":
		writeArray(c.bw, c.eng.FairQueue.List())
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fairqueue.reset' (use ALL or queue-id)")
			return
		}
		writeInt(c.bw, int64(c.eng.FairQueue.Reset(args[0])))
	case "STATS":
		s := c.eng.FairQueue.Stats()
		writeArray(c.bw, []string{
			"queues", strconv.Itoa(s.Queues),
			"total_parked", strconv.Itoa(s.TotalParked),
			"total_enqueues", strconv.FormatInt(s.TotalEnqueues, 10),
			"total_dequeues", strconv.FormatInt(s.TotalDequeues, 10),
		})
	default:
		writeError(c.bw, "unknown FAIRQUEUE subcommand: "+sub)
	}
}
