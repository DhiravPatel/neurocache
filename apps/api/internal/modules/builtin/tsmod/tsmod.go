package tsmod

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

var typeID = modules.MakeTypeID("ts-rb1!")

// Module is the registration entry. main wires it via side-effect import.
var Module = modules.Module{
	Name:        "timeseries",
	Version:     "1.0.0",
	Description: "RedisTimeSeries-compatible TS.* command surface",
	Init:        initModule,
}

func init() { modules.RegisterAvailable(Module) }

func initModule(ctx *modules.RegisterCtx) error {
	if err := ctx.RegisterType(modules.CustomType{
		ID: typeID, Name: "TSDB-TYPE",
		Marshal: func(v any) ([]byte, error) { return v.(*Series).Marshal() },
		Unmarshal: func(b []byte) (any, error) { return Unmarshal(b) },
		MemUsage: func(v any) int64 { return v.(*Series).MemUsage() },
	}); err != nil {
		return err
	}

	r := []string{acl.CatRead, acl.CatFast}
	w := []string{acl.CatWrite, acl.CatFast}
	for _, c := range []modules.Cmd{
		{Name: "TS.CREATE", Arity: -2, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsCreate},
		{Name: "TS.ALTER", Arity: -2, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsAlter},
		{Name: "TS.ADD", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsAdd},
		{Name: "TS.MADD", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyRange(1, -1, 3), Run: tsMAdd},
		{Name: "TS.INCRBY", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsIncrBy},
		{Name: "TS.DECRBY", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsDecrBy},
		{Name: "TS.GET", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: tsGet},
		{Name: "TS.MGET", Arity: -3, Categories: r, KeyPosition: modules.KeyNone, Run: tsMGet},
		{Name: "TS.RANGE", Arity: -4, Categories: r, KeyPosition: modules.KeyAt(1), Run: tsRange(false)},
		{Name: "TS.REVRANGE", Arity: -4, Categories: r, KeyPosition: modules.KeyAt(1), Run: tsRange(true)},
		{Name: "TS.MRANGE", Arity: -4, Categories: r, KeyPosition: modules.KeyNone, Run: tsMRange(false)},
		{Name: "TS.MREVRANGE", Arity: -4, Categories: r, KeyPosition: modules.KeyNone, Run: tsMRange(true)},
		{Name: "TS.DEL", Arity: 4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsDel},
		{Name: "TS.QUERYINDEX", Arity: -2, Categories: r, KeyPosition: modules.KeyNone, Run: tsQueryIndex},
		{Name: "TS.INFO", Arity: -2, Categories: r, KeyPosition: modules.KeyAt(1), Run: tsInfo},
		{Name: "TS.CREATERULE", Arity: -5, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsCreateRule},
		{Name: "TS.DELETERULE", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: tsDeleteRule},
	} {
		if err := ctx.RegisterCmd(c); err != nil {
			return err
		}
	}
	return nil
}

// loadSeries fetches the series at key. ok=false when missing.
func loadSeries(c *modules.Ctx, key string) (*Series, bool, error) {
	v, ok, err := c.Engine.GetCustomValue(key, typeID)
	if err != nil || !ok {
		return nil, false, err
	}
	return v.(*Series), true, nil
}

func saveSeries(c *modules.Ctx, key string, s *Series) error {
	if err := c.Engine.SetCustomValue(key, typeID, s, 0); err != nil {
		return err
	}
	rememberKey(key)
	return nil
}

// parseLabels reads "key1 val1 key2 val2 …" pairs starting at args[i],
// returning the labels and how many tokens were consumed.
func parseLabels(args []string) (map[string]string, error) {
	if len(args)%2 != 0 {
		return nil, errors.New("LABELS expects key/value pairs")
	}
	out := map[string]string{}
	for i := 0; i+1 < len(args); i += 2 {
		out[args[i]] = args[i+1]
	}
	return out, nil
}

// scanCreateOpts parses RETENTION/CHUNK_SIZE/DUPLICATE_POLICY/LABELS
// after the key argument. Returns the remaining (unconsumed) args slice
// in case the caller has trailing args of its own.
func scanCreateOpts(args []string) (retention int64, chunkSize int64, dup DuplicatePolicy, labels map[string]string, rest []string, err error) {
	chunkSize = 4096
	labels = map[string]string{}
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "RETENTION":
			if i+1 >= len(args) {
				err = errors.New("RETENTION needs a value")
				return
			}
			retention, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "CHUNK_SIZE":
			if i+1 >= len(args) {
				err = errors.New("CHUNK_SIZE needs a value")
				return
			}
			chunkSize, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "DUPLICATE_POLICY", "ON_DUPLICATE":
			if i+1 >= len(args) {
				err = errors.New("DUPLICATE_POLICY needs a value")
				return
			}
			d, e := ParseDuplicatePolicy(strings.ToUpper(args[i+1]))
			if e != nil {
				err = e
				return
			}
			dup = d
			i++
		case "LABELS":
			tail := args[i+1:]
			labels, err = parseLabels(tail)
			if err != nil {
				return
			}
			i = len(args)
		default:
			rest = append(rest, args[i])
		}
	}
	return
}

// TS.CREATE key [RETENTION ms] [CHUNK_SIZE n] [DUPLICATE_POLICY p] [LABELS k v ...]
func tsCreate(c *modules.Ctx, args []string) error {
	key := args[0]
	retention, chunkSize, dup, labels, _, err := scanCreateOpts(args[1:])
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if _, ok, _ := loadSeries(c, key); ok {
		c.Reply.Error("TSDB: key already exists")
		return nil
	}
	s := NewSeries(labels, retention)
	s.ChunkSize = chunkSize
	s.DuplicateMode = dup
	if err := saveSeries(c, key, s); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	c.Reply.SimpleString("OK")
	return nil
}

// TS.ALTER key [RETENTION ms] [CHUNK_SIZE n] [DUPLICATE_POLICY p] [LABELS k v ...]
func tsAlter(c *modules.Ctx, args []string) error {
	key := args[0]
	s, ok, _ := loadSeries(c, key)
	if !ok {
		c.Reply.Error("TSDB: the key does not exist")
		return nil
	}
	retention, chunkSize, dup, labels, _, err := scanCreateOpts(args[1:])
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	s.mu.Lock()
	if retention > 0 {
		s.RetentionMs = retention
	}
	if chunkSize > 0 {
		s.ChunkSize = chunkSize
	}
	if dup != DupBlock {
		s.DuplicateMode = dup
	}
	if len(labels) > 0 {
		s.Labels = labels
	}
	s.mu.Unlock()
	_ = saveSeries(c, key, s)
	c.Reply.SimpleString("OK")
	return nil
}

// TS.ADD key timestamp value [RETENTION ms] [CHUNK_SIZE n] [DUPLICATE_POLICY p] [LABELS k v ...]
// Auto-creates the series when missing (matching Redis behaviour).
func tsAdd(c *modules.Ctx, args []string) error {
	key := args[0]
	ts := parseTimestamp(args[1])
	val, err := strconv.ParseFloat(args[2], 64)
	if err != nil {
		c.Reply.Error("invalid value")
		return nil
	}
	s, ok, _ := loadSeries(c, key)
	if !ok {
		retention, chunkSize, dup, labels, _, err := scanCreateOpts(args[3:])
		if err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
		s = NewSeries(labels, retention)
		s.ChunkSize = chunkSize
		s.DuplicateMode = dup
	}
	stored, err := s.Add(ts, val)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if err := saveSeries(c, key, s); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	propagateRules(c, s, stored, val)
	c.Reply.Int(stored)
	return nil
}

// TS.MADD key ts value [key ts value ...]
func tsMAdd(c *modules.Ctx, args []string) error {
	if (len(args))%3 != 0 {
		c.Reply.Error("TS.MADD requires (key ts value) triples")
		return nil
	}
	out := make([]any, 0, len(args)/3)
	for i := 0; i+2 < len(args); i += 3 {
		key := args[i]
		ts := parseTimestamp(args[i+1])
		val, err := strconv.ParseFloat(args[i+2], 64)
		if err != nil {
			out = append(out, errors.New("invalid value").Error())
			continue
		}
		s, ok, _ := loadSeries(c, key)
		if !ok {
			s = NewSeries(nil, 0)
		}
		stored, err := s.Add(ts, val)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		_ = saveSeries(c, key, s)
		propagateRules(c, s, stored, val)
		out = append(out, stored)
	}
	c.Reply.Array(out)
	return nil
}

// TS.INCRBY / TS.DECRBY share most logic.
func tsIncrBy(c *modules.Ctx, args []string) error { return tsIncrCommon(c, args, 1) }
func tsDecrBy(c *modules.Ctx, args []string) error { return tsIncrCommon(c, args, -1) }

func tsIncrCommon(c *modules.Ctx, args []string, sign float64) error {
	key := args[0]
	delta, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		c.Reply.Error("invalid value")
		return nil
	}
	delta *= sign
	ts := int64(-1)
	for i := 2; i < len(args); i++ {
		if strings.ToUpper(args[i]) == "TIMESTAMP" && i+1 < len(args) {
			ts = parseTimestamp(args[i+1])
			i++
		}
	}
	s, ok, _ := loadSeries(c, key)
	if !ok {
		s = NewSeries(nil, 0)
	}
	stored, err := s.IncrBy(ts, delta)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	_ = saveSeries(c, key, s)
	c.Reply.Int(stored)
	return nil
}

func tsGet(c *modules.Ctx, args []string) error {
	s, ok, _ := loadSeries(c, args[0])
	if !ok {
		c.Reply.NilArray()
		return nil
	}
	last, ok := s.Get()
	if !ok {
		c.Reply.NilArray()
		return nil
	}
	c.Reply.Array([]any{last.TS, formatFloat(last.Value)})
	return nil
}

// TS.MGET FILTER label-filter [...]
func tsMGet(c *modules.Ctx, args []string) error {
	if !strings.EqualFold(args[0], "FILTER") {
		c.Reply.Error("syntax error: missing FILTER")
		return nil
	}
	matches := matchSeries(c, args[1:])
	out := make([]any, 0, len(matches))
	for _, m := range matches {
		last, ok := m.Series.Get()
		if !ok {
			continue
		}
		out = append(out, []any{m.Key, labelsToArray(m.Series.Labels), []any{last.TS, formatFloat(last.Value)}})
	}
	c.Reply.Array(out)
	return nil
}

// TS.RANGE / TS.REVRANGE key fromTimestamp toTimestamp
//   [LATEST] [COUNT count] [AGGREGATION aggregator bucketDuration] [ALIGN ts]
func tsRange(reverse bool) func(*modules.Ctx, []string) error {
	return func(c *modules.Ctx, args []string) error {
		key := args[0]
		from := parseTimestamp(args[1])
		to := parseTimestamp(args[2])
		if from < 0 {
			from = 0
		}
		count, agg, bucket, alignTS := scanRangeOpts(args[3:])
		s, ok, _ := loadSeries(c, key)
		if !ok {
			c.Reply.Array([]any{})
			return nil
		}
		samples := s.Range(from, to, false, 0)
		if agg != AggNone && bucket > 0 {
			samples = aggregate(samples, agg, bucket, alignTS)
		}
		if reverse {
			for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
				samples[i], samples[j] = samples[j], samples[i]
			}
		}
		if count > 0 && count < len(samples) {
			samples = samples[:count]
		}
		c.Reply.Array(samplesToReply(samples))
		return nil
	}
}

// TS.MRANGE / TS.MREVRANGE fromTimestamp toTimestamp
//   [LATEST] [COUNT count] [AGGREGATION agg bucket] [ALIGN ts]
//   [WITHLABELS | SELECTED_LABELS l ...] FILTER label-filter [...]
func tsMRange(reverse bool) func(*modules.Ctx, []string) error {
	return func(c *modules.Ctx, args []string) error {
		if len(args) < 4 {
			c.Reply.Error("wrong number of arguments")
			return nil
		}
		from := parseTimestamp(args[0])
		to := parseTimestamp(args[1])
		count, agg, bucket, alignTS := 0, AggNone, int64(0), int64(0)
		withLabels := false
		var selectedLabels []string
		filterAt := -1
		i := 2
		for i < len(args) {
			switch strings.ToUpper(args[i]) {
			case "COUNT":
				count, _ = strconv.Atoi(args[i+1])
				i += 2
			case "AGGREGATION":
				a, err := ParseAggType(args[i+1])
				if err != nil {
					c.Reply.Error(err.Error())
					return nil
				}
				agg = a
				bucket, _ = strconv.ParseInt(args[i+2], 10, 64)
				i += 3
			case "ALIGN":
				alignTS = parseAlign(args[i+1])
				i += 2
			case "LATEST":
				i++ // we always serve the latest sample; flag is informational
			case "WITHLABELS":
				withLabels = true
				i++
			case "SELECTED_LABELS":
				j := i + 1
				for j < len(args) && strings.ToUpper(args[j]) != "FILTER" {
					selectedLabels = append(selectedLabels, args[j])
					j++
				}
				i = j
			case "FILTER":
				filterAt = i + 1
				i = len(args)
			default:
				i++
			}
		}
		if filterAt < 0 {
			c.Reply.Error("missing FILTER")
			return nil
		}
		matches := matchSeries(c, args[filterAt:])
		out := make([]any, 0, len(matches))
		for _, m := range matches {
			samples := m.Series.Range(from, to, false, 0)
			if agg != AggNone && bucket > 0 {
				samples = aggregate(samples, agg, bucket, alignTS)
			}
			if reverse {
				for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
					samples[i], samples[j] = samples[j], samples[i]
				}
			}
			if count > 0 && count < len(samples) {
				samples = samples[:count]
			}
			labels := []any{}
			switch {
			case withLabels:
				labels = labelsToArray(m.Series.Labels)
			case selectedLabels != nil:
				labels = subsetLabels(m.Series.Labels, selectedLabels)
			}
			out = append(out, []any{m.Key, labels, samplesToReply(samples)})
		}
		c.Reply.Array(out)
		return nil
	}
}

// TS.DEL key fromTimestamp toTimestamp
func tsDel(c *modules.Ctx, args []string) error {
	s, ok, _ := loadSeries(c, args[0])
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	from := parseTimestamp(args[1])
	to := parseTimestamp(args[2])
	n := s.DeleteRange(from, to)
	if n > 0 {
		_ = saveSeries(c, args[0], s)
	}
	c.Reply.Int(int64(n))
	return nil
}

// TS.QUERYINDEX FILTER label-filter [...]
func tsQueryIndex(c *modules.Ctx, args []string) error {
	if !strings.EqualFold(args[0], "FILTER") {
		c.Reply.Error("syntax error: missing FILTER")
		return nil
	}
	matches := matchSeries(c, args[1:])
	keys := make([]any, 0, len(matches))
	for _, m := range matches {
		keys = append(keys, m.Key)
	}
	c.Reply.Array(keys)
	return nil
}

// TS.INFO key [DEBUG]
func tsInfo(c *modules.Ctx, args []string) error {
	s, ok, _ := loadSeries(c, args[0])
	if !ok {
		c.Reply.Error("TSDB: the key does not exist")
		return nil
	}
	rules := []any{}
	for _, r := range s.Rules {
		rules = append(rules, []any{r.DestKey, r.BucketMs, r.Aggregator.String()})
	}
	c.Reply.Array([]any{
		"totalSamples", int64(s.Len()),
		"memoryUsage", s.MemUsage(),
		"firstTimestamp", s.FirstTS(),
		"lastTimestamp", s.LastTS(),
		"retentionTime", s.RetentionMs,
		"chunkCount", int64(1),
		"chunkSize", s.ChunkSize,
		"chunkType", "uncompressed",
		"duplicatePolicy", s.DuplicateMode.String(),
		"labels", labelsToArray(s.Labels),
		"sourceKey", s.SourceKey,
		"rules", rules,
	})
	return nil
}

// TS.CREATERULE source dest AGGREGATION agg bucket [alignTimestamp]
func tsCreateRule(c *modules.Ctx, args []string) error {
	if len(args) < 5 {
		c.Reply.Error("wrong number of arguments for 'ts.createrule'")
		return nil
	}
	src, dest := args[0], args[1]
	if !strings.EqualFold(args[2], "AGGREGATION") {
		c.Reply.Error("syntax error: expected AGGREGATION")
		return nil
	}
	agg, err := ParseAggType(args[3])
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	bucket, err := strconv.ParseInt(args[4], 10, 64)
	if err != nil {
		c.Reply.Error("invalid bucket")
		return nil
	}
	align := int64(0)
	if len(args) >= 6 {
		align = parseAlign(args[5])
	}
	srcS, ok, _ := loadSeries(c, src)
	if !ok {
		c.Reply.Error("TSDB: the source key does not exist")
		return nil
	}
	destS, ok, _ := loadSeries(c, dest)
	if !ok {
		c.Reply.Error("TSDB: the destination key does not exist")
		return nil
	}
	rule := &Rule{DestKey: dest, Aggregator: agg, BucketMs: bucket, AlignTS: align}
	if err := srcS.AddRule(rule); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	destS.SourceKey = src
	_ = saveSeries(c, src, srcS)
	_ = saveSeries(c, dest, destS)
	c.Reply.SimpleString("OK")
	return nil
}

// TS.DELETERULE source dest
func tsDeleteRule(c *modules.Ctx, args []string) error {
	src, dest := args[0], args[1]
	srcS, ok, _ := loadSeries(c, src)
	if !ok {
		c.Reply.Error("TSDB: the source key does not exist")
		return nil
	}
	if !srcS.DeleteRule(dest) {
		c.Reply.Error("TSDB: compaction rule does not exist")
		return nil
	}
	_ = saveSeries(c, src, srcS)
	c.Reply.SimpleString("OK")
	return nil
}

// propagateRules applies a freshly inserted sample to every downsampling
// rule on the source series. The destination series receives the
// aggregated sample at the bucket close — Redis does this lazily, we
// match by accumulating in the rule and emitting on bucket transition.
func propagateRules(c *modules.Ctx, src *Series, ts int64, val float64) {
	src.mu.Lock()
	defer src.mu.Unlock()
	for _, r := range src.Rules {
		bucket := alignBucket(ts, r.BucketMs, r.AlignTS)
		if r.curBucket == 0 {
			r.curBucket = bucket
			r.acc = accumulator{}
		}
		if bucket != r.curBucket {
			// Close out the previous bucket — flush to the destination.
			result := r.acc.Result(r.Aggregator)
			dest, ok, _ := loadSeries(c, r.DestKey)
			if ok {
				_, _ = dest.Add(r.curBucket, result)
				_ = saveSeries(c, r.DestKey, dest)
			}
			r.curBucket = bucket
			r.acc = accumulator{}
		}
		r.acc.Add(val)
	}
}

// ── helpers ─────────────────────────────────────────────────────

type seriesMatch struct {
	Key    string
	Series *Series
}

// matchSeries scans label filters of the form "label=value", "label!=value",
// "label=", "label!=", "label=(v1,v2,...)" against every TS key the
// engine knows about. We can't enumerate keys without engine help, so
// we expose the index via the engine handle's published events instead
// — for now, we walk the label-equals filter and use the engine's
// Get/Del API. (A future optimisation is a label-value reverse index;
// the current scan is bounded by series count, which is small in
// practice for TSDB workloads.)
func matchSeries(c *modules.Ctx, filters []string) []seriesMatch {
	keys := keyspaceForType(c)
	out := []seriesMatch{}
	for _, k := range keys {
		v, ok, err := c.Engine.GetCustomValue(k, typeID)
		if err != nil || !ok {
			continue
		}
		s := v.(*Series)
		if matchAll(s, filters) {
			out = append(out, seriesMatch{Key: k, Series: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// matchAll evaluates the conjunction of label filters.
func matchAll(s *Series, filters []string) bool {
	for _, f := range filters {
		if !matchOne(s, f) {
			return false
		}
	}
	return true
}

func matchOne(s *Series, f string) bool {
	// label=(v1,v2,...) or label!=(v1,v2,...)
	if i := strings.Index(f, "!="); i >= 0 {
		k, v := f[:i], strings.TrimSpace(f[i+2:])
		if v == "" {
			return s.HasLabel(k)
		}
		if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			parts := strings.Split(strings.Trim(v, "()"), ",")
			return !s.LabelValueIn(k, parts)
		}
		return !s.LabelEquals(k, v)
	}
	if i := strings.Index(f, "="); i >= 0 {
		k, v := f[:i], strings.TrimSpace(f[i+1:])
		if v == "" {
			return !s.HasLabel(k)
		}
		if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			parts := strings.Split(strings.Trim(v, "()"), ",")
			return s.LabelValueIn(k, parts)
		}
		return s.LabelEquals(k, v)
	}
	return false
}

// keyspaceForType returns every key in the engine that holds a
// timeseries value. We rely on a label-based scan via the engine
// handle. Since the modules.EngineHandle doesn't expose KEYS today,
// we cache key names locally — TS.CREATE / TS.ADD register them, TS
// does not yet observe foreign deletes (a future hook on the engine
// notifier closes that gap).
func keyspaceForType(c *modules.Ctx) []string {
	keysMu.RLock()
	defer keysMu.RUnlock()
	out := make([]string, 0, len(knownKeys))
	for k := range knownKeys {
		out = append(out, k)
	}
	return out
}

var (
	keysMu    sync.RWMutex
	knownKeys = map[string]struct{}{}
)

func rememberKey(key string)  { keysMu.Lock(); knownKeys[key] = struct{}{}; keysMu.Unlock() }
func forgetKey(key string)    { keysMu.Lock(); delete(knownKeys, key); keysMu.Unlock() }

// scanRangeOpts parses TS.RANGE/REVRANGE optional clauses.
func scanRangeOpts(args []string) (count int, agg AggType, bucket int64, alignTS int64) {
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			if i+1 < len(args) {
				count, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "AGGREGATION":
			if i+2 < len(args) {
				agg, _ = ParseAggType(args[i+1])
				bucket, _ = strconv.ParseInt(args[i+2], 10, 64)
				i += 2
			}
		case "ALIGN":
			if i+1 < len(args) {
				alignTS = parseAlign(args[i+1])
				i++
			}
		}
	}
	return
}

func parseAlign(s string) int64 {
	switch strings.ToLower(s) {
	case "start", "-":
		return 0
	case "end", "+":
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func parseTimestamp(s string) int64 {
	switch strings.ToLower(s) {
	case "*":
		return -1
	case "-":
		return 0
	case "+":
		return int64(1) << 62
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func samplesToReply(samples []Sample) []any {
	out := make([]any, len(samples))
	for i, s := range samples {
		out[i] = []any{s.TS, formatFloat(s.Value)}
	}
	return out
}

func labelsToArray(labels map[string]string) []any {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, []any{k, labels[k]})
	}
	return out
}

func subsetLabels(labels map[string]string, want []string) []any {
	out := make([]any, 0, len(want))
	for _, k := range want {
		v, ok := labels[k]
		if !ok {
			continue
		}
		out = append(out, []any{k, v})
	}
	return out
}

// silence the forgetKey reference until we wire an engine-side
// keyspace-event hook for module-typed deletes.
var _ = forgetKey
