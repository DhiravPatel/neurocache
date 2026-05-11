package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// dedupSemCmd handles DEDUP.SEM.* — semantic dedup for streams.
//
//   DEDUP.SEM.SEEN bucket text [THRESHOLD f] [WINDOW n] [EMBED v,v,...]
//        -> [is_dup, similar_id, similar_text, score, new_id]
//   DEDUP.SEM.PEEK bucket text [THRESHOLD f] [EMBED v,v,...]
//   DEDUP.SEM.ADD bucket id text [EMBED v,v,...]
//   DEDUP.SEM.RECENT bucket [N n]
//   DEDUP.SEM.FORGET bucket
//   DEDUP.SEM.BUCKETS
//   DEDUP.SEM.STATS
func (c *conn) dedupSemCmd(sub string, args []string) {
	switch sub {
	case "SEEN", "PEEK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments")
			return
		}
		opts, err := parseSemSeenOpts(args[2:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		var r llmstack.SeenResult
		if sub == "SEEN" {
			r = c.eng.SemDedup.Seen(args[0], args[1], opts)
		} else {
			r = c.eng.SemDedup.Peek(args[0], args[1], opts)
		}
		dupInt := "0"
		if r.IsDup {
			dupInt = "1"
		}
		writeArray(c.bw, []string{
			"is_dup", dupInt,
			"similar_id", r.SimilarID,
			"similar_text", r.SimilarText,
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"new_id", r.NewID,
		})
	case "ADD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'dedup.sem.add'")
			return
		}
		opts, err := parseSemSeenOpts(args[3:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.SemDedup.Add(args[0], args[1], args[2], opts.Vec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RECENT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'dedup.sem.recent'")
			return
		}
		n := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "N" {
				writeError(c.bw, "unknown DEDUP.SEM.RECENT option: "+key)
				return
			}
			v, err := strconv.Atoi(val)
			if err != nil || v < 0 {
				writeError(c.bw, "N must be a non-negative integer")
				return
			}
			n = v
			i += 2
		}
		rows := c.eng.SemDedup.Recent(args[0], n)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"id", r.ID,
				"text", r.Text,
				"at_ms", strconv.FormatInt(r.AtMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'dedup.sem.forget'")
			return
		}
		if c.eng.SemDedup.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "BUCKETS":
		rows := c.eng.SemDedup.Buckets()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"bucket", r.Bucket,
				"size", strconv.Itoa(r.Size),
				"window", strconv.Itoa(r.Window),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.SemDedup.Stats()
		writeArray(c.bw, []string{
			"buckets", strconv.Itoa(s.Buckets),
			"total_seens", strconv.FormatInt(s.TotalSeens, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_adds", strconv.FormatInt(s.TotalAdds, 10),
			"total_evictions", strconv.FormatInt(s.TotalEvictions, 10),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown DEDUP.SEM subcommand: "+sub)
	}
}

func parseSemSeenOpts(rest []string) (llmstack.SeenOpts, error) {
	out := llmstack.SeenOpts{}
	i := 0
	for i+1 < len(rest)+1 && i < len(rest) {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return out, &v11Err{msg: key + " needs a value"}
		}
		val := rest[i+1]
		switch key {
		case "THRESHOLD":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return out, &v11Err{msg: "THRESHOLD must be a float"}
			}
			out.Threshold = f
		case "WINDOW":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return out, &v11Err{msg: "WINDOW must be a positive integer"}
			}
			out.Window = n
		case "EMBED":
			parts := strings.Split(val, ",")
			vec := make([]float64, 0, len(parts))
			for _, p := range parts {
				f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
				if err != nil {
					return out, &v11Err{msg: "EMBED component not parseable: " + p}
				}
				vec = append(vec, f)
			}
			out.Vec = vec
		default:
			return out, &v11Err{msg: "unknown DEDUP.SEM option: " + key}
		}
		i += 2
	}
	return out, nil
}

// prefixCmd handles PREFIX.* — KV-cache-aware prefix routing.
//
//   PREFIX.REGISTER prefix-hash worker [TTL ms]
//   PREFIX.LOOKUP prefix-hash       -> array of {worker, registered_at_ms, age_ms}
//   PREFIX.HASH text                -> 16-hex prefix hash
//   PREFIX.FORGET prefix-hash [WORKER w]
//   PREFIX.EVICT worker             -> int dropped
//   PREFIX.LIST                     -> registered prefixes + worker counts
//   PREFIX.STATS
func (c *conn) prefixCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'prefix.register'")
			return
		}
		var ttl time.Duration
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "TTL" {
				writeError(c.bw, "unknown PREFIX.REGISTER option: "+key)
				return
			}
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n <= 0 {
				writeError(c.bw, "TTL must be a positive integer (ms)")
				return
			}
			ttl = time.Duration(n) * time.Millisecond
			i += 2
		}
		c.eng.PrefixRouter.Register(args[0], args[1], ttl)
		writeSimple(c.bw, "OK")
	case "LOOKUP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefix.lookup'")
			return
		}
		rows := c.eng.PrefixRouter.Lookup(args[0])
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"worker", r.Worker,
				"registered_at_ms", strconv.FormatInt(r.RegisteredAtMS, 10),
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "HASH":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefix.hash'")
			return
		}
		writeBulk(c.bw, llmstack.HashPrefix(args[0]))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefix.forget'")
			return
		}
		worker := ""
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "WORKER" {
				writeError(c.bw, "unknown PREFIX.FORGET option: "+key)
				return
			}
			worker = val
			i += 2
		}
		if c.eng.PrefixRouter.Forget(args[0], worker) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "EVICT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'prefix.evict'")
			return
		}
		writeInt(c.bw, int64(c.eng.PrefixRouter.EvictWorker(args[0])))
	case "LIST":
		rows := c.eng.PrefixRouter.Prefixes()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"prefix_hash", r.PrefixHash,
				"workers", strconv.Itoa(r.Workers),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.PrefixRouter.Stats()
		writeArray(c.bw, []string{
			"prefixes", strconv.Itoa(s.Prefixes),
			"total_lookups", strconv.FormatInt(s.TotalLookups, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"total_misses", strconv.FormatInt(s.TotalMisses, 10),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
			"total_evictions", strconv.FormatInt(s.TotalEvictions, 10),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown PREFIX subcommand: "+sub)
	}
}

// toolboxCmd handles TOOLBOX.* — tool schema registry + semantic search.
//
//   TOOLBOX.REGISTER tool-id name description schema-json
//        [TAGS t1,t2,...] [EMBED v,v,...]
//   TOOLBOX.SEARCH query [K n] [TAGS t1,t2,...] [EMBED v,v,...]
//   TOOLBOX.GET tool-id
//   TOOLBOX.LIST [TAGS t1,t2,...]
//   TOOLBOX.FORGET tool-id
//   TOOLBOX.STATS
func (c *conn) toolboxCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 4 {
			writeError(c.bw, "wrong number of arguments for 'toolbox.register'")
			return
		}
		opts, err := parseToolboxOpts(args[4:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.Toolbox.Register(args[0], args[1], args[2], args[3],
			llmstack.ToolboxOpts{Tags: opts.tags, Vec: opts.vec}); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SEARCH":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'toolbox.search'")
			return
		}
		opts, err := parseToolboxOpts(args[1:])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		hits := c.eng.Toolbox.Search(args[0], llmstack.SearchOpts{
			K: opts.k, Tags: opts.tags, Vec: opts.vec,
		})
		writeValue(c.bw, toolboxHitsReply(hits))
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'toolbox.get'")
			return
		}
		hit, ok := c.eng.Toolbox.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeValue(c.bw, []any{toolboxHitReply(hit)})
	case "LIST":
		opts, err := parseToolboxOpts(args)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeValue(c.bw, toolboxHitsReply(c.eng.Toolbox.List(opts.tags)))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'toolbox.forget'")
			return
		}
		if c.eng.Toolbox.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Toolbox.Stats()
		writeArray(c.bw, []string{
			"tools", strconv.Itoa(s.Tools),
			"total_registers", strconv.FormatInt(s.TotalRegisters, 10),
			"total_searches", strconv.FormatInt(s.TotalSearches, 10),
			"total_returns", strconv.FormatInt(s.TotalReturns, 10),
		})
	default:
		writeError(c.bw, "unknown TOOLBOX subcommand: "+sub)
	}
}

type toolboxParsed struct {
	tags []string
	vec  []float64
	k    int
}

func parseToolboxOpts(rest []string) (toolboxParsed, error) {
	out := toolboxParsed{}
	i := 0
	for i+1 < len(rest)+1 && i < len(rest) {
		key := strings.ToUpper(rest[i])
		if i+1 >= len(rest) {
			return out, &v11Err{msg: key + " needs a value"}
		}
		val := rest[i+1]
		switch key {
		case "TAGS":
			for _, p := range strings.Split(val, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					out.tags = append(out.tags, p)
				}
			}
		case "EMBED":
			parts := strings.Split(val, ",")
			vec := make([]float64, 0, len(parts))
			for _, p := range parts {
				f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
				if err != nil {
					return out, &v11Err{msg: "EMBED component not parseable: " + p}
				}
				vec = append(vec, f)
			}
			out.vec = vec
		case "K":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return out, &v11Err{msg: "K must be a positive integer"}
			}
			out.k = n
		default:
			return out, &v11Err{msg: "unknown TOOLBOX option: " + key}
		}
		i += 2
	}
	return out, nil
}

func toolboxHitsReply(hits []llmstack.SearchHit) []any {
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, toolboxHitReply(h))
	}
	return out
}

func toolboxHitReply(h llmstack.SearchHit) []any {
	tagsAny := make([]any, 0, len(h.Tags))
	for _, t := range h.Tags {
		tagsAny = append(tagsAny, t)
	}
	return []any{
		"id", h.ID,
		"name", h.Name,
		"description", h.Description,
		"schema", h.Schema,
		"tags", tagsAny,
		"score", strconv.FormatFloat(h.Score, 'f', 4, 64),
	}
}

type v11Err struct{ msg string }

func (e *v11Err) Error() string { return e.msg }
