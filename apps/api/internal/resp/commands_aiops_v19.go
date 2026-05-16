package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// factCmd handles FACT.* — versioned fact registry + stamps.
func (c *conn) factCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'fact.set'")
			return
		}
		if err := c.eng.Facts.Set(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BUMP":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'fact.bump'")
			return
		}
		v, err := c.eng.Facts.Bump(args[0], args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, v)
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fact.get'")
			return
		}
		r, ok := c.eng.Facts.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"version", strconv.FormatInt(r.Version, 10),
			"content", r.Content,
			"updated_at", strconv.FormatInt(r.UpdatedAt, 10),
		})
	case "STAMP":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'fact.stamp'")
			return
		}
		if err := c.eng.Facts.Stamp(args[0], args[1:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STALE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fact.stale'")
			return
		}
		if c.eng.Facts.Stale(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STALE_KEYS":
		limit := 0
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "LIMIT" {
				writeError(c.bw, "unknown FACT.STALE_KEYS option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be a non-negative integer")
				return
			}
			limit = n
			i += 2
		}
		writeArray(c.bw, c.eng.Facts.StaleKeys(limit))
	case "UNSTAMP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fact.unstamp'")
			return
		}
		if c.eng.Facts.Unstamp(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		rows := c.eng.Facts.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"fact_id", r.FactID,
				"version", strconv.FormatInt(r.Version, 10),
				"updated_at", strconv.FormatInt(r.UpdatedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'fact.forget'")
			return
		}
		if c.eng.Facts.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Facts.Stats()
		writeArray(c.bw, []string{
			"facts", strconv.Itoa(s.Facts),
			"stamped_keys", strconv.Itoa(s.StampedKeys),
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_bumps", strconv.FormatInt(s.TotalBumps, 10),
			"total_stamps", strconv.FormatInt(s.TotalStamps, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_stale_detected", strconv.FormatInt(s.TotalStale, 10),
		})
	default:
		writeError(c.bw, "unknown FACT subcommand: "+sub)
	}
}

// cacheInvalidateCmd handles CACHE.INVALIDATE.* and CACHE.STALE.LIST.
func (c *conn) cacheInvalidateCmd(sub string, args []string) {
	switch sub {
	case "INVALIDATE.TRACK":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'cache.invalidate.track'")
			return
		}
		var vec []float64
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "EMBED" {
				writeError(c.bw, "unknown CACHE.INVALIDATE.TRACK option: "+key)
				return
			}
			v, err := parseVecCSV(val)
			if err != nil {
				writeError(c.bw, err.Error())
				return
			}
			vec = v
			i += 2
		}
		if err := c.eng.Invalidator.Track(args[0], args[1], args[2], vec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "INVALIDATE.UNTRACK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cache.invalidate.untrack'")
			return
		}
		if c.eng.Invalidator.Untrack(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "INVALIDATE.SEMANTIC":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.invalidate.semantic'")
			return
		}
		opts := llmstack.SemanticOpts{}
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "THRESHOLD":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil {
					writeError(c.bw, "THRESHOLD must be a float")
					return
				}
				opts.Threshold = f
			case "LAYERS":
				for _, p := range strings.Split(val, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						opts.Layers = append(opts.Layers, p)
					}
				}
			case "EMBED":
				v, err := parseVecCSV(val)
				if err != nil {
					writeError(c.bw, err.Error())
					return
				}
				opts.Vec = v
			default:
				writeError(c.bw, "unknown CACHE.INVALIDATE.SEMANTIC option: "+key)
				return
			}
			i += 2
		}
		r := c.eng.Invalidator.Semantic(args[0], opts)
		hitsAny := make([]any, 0, len(r.Hits))
		for _, h := range r.Hits {
			hitsAny = append(hitsAny, []any{
				"layer", h.Layer,
				"key", h.Key,
				"text", h.Text,
				"score", strconv.FormatFloat(h.Score, 'f', 4, 64),
			})
		}
		perLayerAny := make([]any, 0, len(r.PerLayer)*2)
		for k, v := range r.PerLayer {
			perLayerAny = append(perLayerAny, k, strconv.Itoa(v))
		}
		writeValue(c.bw, []any{
			"total", strconv.Itoa(r.Total),
			"per_layer", perLayerAny,
			"hits", hitsAny,
		})
	case "INVALIDATE.PURGE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'cache.invalidate.purge'")
			return
		}
		writeInt(c.bw, int64(c.eng.Invalidator.PurgeLayer(args[0])))
	case "STALE.LIST":
		layer := ""
		limit := 0
		i := 0
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "LAYER":
				layer = val
			case "LIMIT":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be a non-negative integer")
					return
				}
				limit = n
			default:
				writeError(c.bw, "unknown CACHE.STALE.LIST option: "+key)
				return
			}
			i += 2
		}
		rows := c.eng.Invalidator.List(layer, limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"layer", r.Layer,
				"key", r.Key,
				"text", r.Text,
				"tracked_at", strconv.FormatInt(r.TrackedAt, 10),
			})
		}
		writeValue(c.bw, out)
	case "INVALIDATE.STATS":
		s := c.eng.Invalidator.Stats()
		writeArray(c.bw, []string{
			"layers", strconv.Itoa(s.Layers),
			"total_tracked", strconv.Itoa(s.TotalTracked),
			"total_tracks", strconv.FormatInt(s.TotalTracks, 10),
			"total_scans", strconv.FormatInt(s.TotalScans, 10),
			"total_invalidations", strconv.FormatInt(s.TotalInvalidations, 10),
		})
	default:
		writeError(c.bw, "unknown CACHE subcommand: "+sub)
	}
}

// banditCmd handles BANDIT.* — adaptive routing.
func (c *conn) banditCmd(sub string, args []string) {
	switch sub {
	case "CREATE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'bandit.create' (need: id ARMS arm1 arm2 ...)")
			return
		}
		// Args: bandit-id ARMS arm1 arm2 ... [STRATEGY s]
		if !strings.EqualFold(args[1], "ARMS") {
			writeError(c.bw, "expected ARMS keyword at arg 2")
			return
		}
		var arms []string
		strategy := ""
		i := 2
		for i < len(args) {
			if strings.EqualFold(args[i], "STRATEGY") {
				if i+1 >= len(args) {
					writeError(c.bw, "STRATEGY needs a value")
					return
				}
				strategy = args[i+1]
				i += 2
				continue
			}
			arms = append(arms, args[i])
			i++
		}
		if err := c.eng.Bandit.Create(args[0], arms, strategy); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PICK":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'bandit.pick'")
			return
		}
		var seed int64
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "SEED" {
				writeError(c.bw, "unknown BANDIT.PICK option: "+key)
				return
			}
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				writeError(c.bw, "SEED must be an integer")
				return
			}
			seed = n
			i += 2
		}
		r, ok := c.eng.Bandit.Pick(args[0], seed)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNBANDIT", "no bandit registered for that id")
			return
		}
		writeArray(c.bw, []string{
			"arm", r.Arm,
			"sampled_score", strconv.FormatFloat(r.SampledScore, 'f', 4, 64),
			"total_pulls", strconv.FormatInt(r.TotalPulls, 10),
		})
	case "RECORD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'bandit.record'")
			return
		}
		score, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "score must be a float")
			return
		}
		if err := c.eng.Bandit.Record(args[0], args[1], score); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "STATS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'bandit.stats'")
			return
		}
		s, ok := c.eng.Bandit.Stats(args[0])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNBANDIT", "no bandit registered for that id")
			return
		}
		armsAny := make([]any, 0, len(s.Arms))
		for _, a := range s.Arms {
			armsAny = append(armsAny, []any{
				"arm", a.Arm,
				"alpha", strconv.FormatFloat(a.Alpha, 'f', 4, 64),
				"beta", strconv.FormatFloat(a.Beta, 'f', 4, 64),
				"posterior_mean", strconv.FormatFloat(a.PosteriorMean, 'f', 4, 64),
				"pulls", strconv.FormatInt(a.Pulls, 10),
				"share", strconv.FormatFloat(a.Share, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"bandit_id", s.BanditID,
			"strategy", s.Strategy,
			"arms", armsAny,
			"total_pulls", strconv.FormatInt(s.TotalPulls, 10),
		})
	case "ARMS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'bandit.arms'")
			return
		}
		arms, ok := c.eng.Bandit.Arms(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, arms)
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'bandit.reset'")
			return
		}
		if c.eng.Bandit.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'bandit.forget'")
			return
		}
		if c.eng.Bandit.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		writeArray(c.bw, c.eng.Bandit.List())
	case "GLOBAL_STATS":
		s := c.eng.Bandit.GlobalStats()
		writeArray(c.bw, []string{
			"bandits", strconv.Itoa(s.Bandits),
			"total_creates", strconv.FormatInt(s.TotalCreates, 10),
			"total_picks", strconv.FormatInt(s.TotalPicks, 10),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
		})
	default:
		writeError(c.bw, "unknown BANDIT subcommand: "+sub)
	}
}
