package resp

import (
	"errors"
	"strconv"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// llmRouteCmd handles LLM.ROUTE.* — the provider failover ladder.
//
//   LLM.ROUTE.SET <name> <provider1> [<provider2> ...]
//                                  -- define the ladder, ordered
//                                     preferred-to-fallback
//   LLM.ROUTE.NEXT <name>          -- get the first healthy provider
//   LLM.ROUTE.MARKDOWN <provider>  -- flag a provider as down
//   LLM.ROUTE.MARKUP <provider>    -- flag a provider as recovered
//   LLM.ROUTE.HEALTHY <provider>   -- 1/0 atomic read
//   LLM.ROUTE.LIST                 -- every route + provider state
//   LLM.ROUTE.STATS                -- global next/failover counts
//   LLM.ROUTE.FORGET <name>        -- drop a route
func (c *conn) llmRouteCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'llm.route.set'")
			return
		}
		c.eng.LLMRouter.SetRoute(args[0], args[1:])
		writeSimple(c.bw, "OK")
	case "NEXT":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'llm.route.next'")
			return
		}
		p, err := c.eng.LLMRouter.Next(args[0])
		if err != nil {
			c.writeRouterErr(err)
			return
		}
		writeBulk(c.bw, p)
	case "MARKDOWN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'llm.route.markdown'")
			return
		}
		if err := c.eng.LLMRouter.MarkDown(args[0]); err != nil {
			c.writeRouterErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "MARKUP":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'llm.route.markup'")
			return
		}
		if err := c.eng.LLMRouter.MarkUp(args[0]); err != nil {
			c.writeRouterErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "HEALTHY":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'llm.route.healthy'")
			return
		}
		ok, err := c.eng.LLMRouter.IsHealthy(args[0])
		if err != nil {
			c.writeRouterErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "LIST":
		rows := c.eng.LLMRouter.List()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			provs := make([]any, 0, len(r.Providers))
			for _, p := range r.Providers {
				healthy := "0"
				if p.Healthy {
					healthy = "1"
				}
				provs = append(provs, []string{
					"name", p.Name,
					"healthy", healthy,
					"picks", strconv.FormatInt(p.Picks, 10),
					"skips", strconv.FormatInt(p.Skips, 10),
					"last_mark_ns", strconv.FormatInt(p.LastMarkNS, 10),
				})
			}
			out = append(out, []any{
				"name", r.Name,
				"picks", strconv.FormatInt(r.Picks, 10),
				"rotations", strconv.FormatInt(r.Rotations, 10),
				"providers", provs,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.LLMRouter.Stats()
		writeArray(c.bw, []string{
			"total_nexts", strconv.FormatInt(s.TotalNexts, 10),
			"total_failovers", strconv.FormatInt(s.TotalFailovers, 10),
			"unique_routes", strconv.Itoa(s.UniqueRoutes),
			"unique_providers", strconv.Itoa(s.UniqueProviders),
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'llm.route.forget'")
			return
		}
		if c.eng.LLMRouter.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	default:
		writeError(c.bw, "unknown LLM.ROUTE subcommand: "+sub)
	}
}

// injectCmd handles INJECT.* — the prompt-injection scanner.
//
//   INJECT.SCAN <text>                   -- first match wins; returns
//                                            severity + pattern name
//   INJECT.SCANALL <text>                -- every matching pattern
//   INJECT.PATTERN.ADD <name> <regex> <severity>
//                                        -- register a custom rule
//   INJECT.PATTERN.REMOVE <name>         -- drop a custom rule
//   INJECT.PATTERN.LIST                  -- every registered pattern
//   INJECT.STATS                         -- scan / hit counters
//   INJECT.RESET                         -- zero the counters
func (c *conn) injectCmd(sub string, args []string) {
	switch sub {
	case "SCAN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'inject.scan'")
			return
		}
		sev, name, hit := c.eng.InjectScanner.Scan(args[0])
		if !hit {
			writeArray(c.bw, []string{
				"hit", "0",
				"severity", "0",
				"pattern", "",
			})
			return
		}
		writeArray(c.bw, []string{
			"hit", "1",
			"severity", strconv.FormatFloat(sev, 'f', 2, 64),
			"pattern", name,
		})
	case "SCANALL":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'inject.scanall'")
			return
		}
		hits := c.eng.InjectScanner.ScanAll(args[0])
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []string{
				"name", h.Name,
				"severity", strconv.FormatFloat(h.Severity, 'f', 2, 64),
			})
		}
		writeValue(c.bw, out)
	case "PATTERN.ADD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'inject.pattern.add'")
			return
		}
		sev, err := strconv.ParseFloat(args[2], 64)
		if err != nil || sev < 0 || sev > 1 {
			writeError(c.bw, "severity must be a float between 0 and 1")
			return
		}
		if err := c.eng.InjectScanner.Add(args[0], args[1], sev); err != nil {
			c.writeInjectErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "PATTERN.REMOVE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'inject.pattern.remove'")
			return
		}
		if err := c.eng.InjectScanner.Remove(args[0]); err != nil {
			c.writeInjectErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "PATTERN.LIST":
		rows := c.eng.InjectScanner.Patterns()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			builtin := "0"
			if r.Builtin {
				builtin = "1"
			}
			out = append(out, []string{
				"name", r.Name,
				"source", r.Source,
				"severity", strconv.FormatFloat(r.Severity, 'f', 2, 64),
				"builtin", builtin,
				"hits", strconv.FormatInt(r.Hits, 10),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.InjectScanner.Stats()
		writeArray(c.bw, []string{
			"total_scans", strconv.FormatInt(s.TotalScans, 10),
			"total_hits", strconv.FormatInt(s.TotalHits, 10),
			"hit_rate", strconv.FormatFloat(s.HitRate, 'f', 4, 64),
			"total_patterns", strconv.Itoa(s.TotalPatterns),
		})
	case "RESET":
		c.eng.InjectScanner.Reset()
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "unknown INJECT subcommand: "+sub)
	}
}

// writeRouterErr maps typed router errors to RESP typed-error replies.
func (c *conn) writeRouterErr(err error) {
	switch {
	case errors.Is(err, llmstack.ErrUnknownRoute):
		writeTypedError(c.bw, "UNKNOWNROUTE", err.Error())
	case errors.Is(err, llmstack.ErrUnknownProvider):
		writeTypedError(c.bw, "UNKNOWNPROVIDER", err.Error())
	case errors.Is(err, llmstack.ErrNoHealthyProvider):
		writeTypedError(c.bw, "NOHEALTHY", err.Error())
	default:
		writeError(c.bw, err.Error())
	}
}

// writeInjectErr maps typed scanner errors to typed-error replies.
func (c *conn) writeInjectErr(err error) {
	switch {
	case errors.Is(err, llmstack.ErrPatternExists):
		writeTypedError(c.bw, "PATTERNEXISTS", err.Error())
	case errors.Is(err, llmstack.ErrPatternBuiltin):
		writeTypedError(c.bw, "PATTERNBUILTIN", err.Error())
	case errors.Is(err, llmstack.ErrUnknownPattern):
		writeTypedError(c.bw, "UNKNOWNPATTERN", err.Error())
	default:
		writeError(c.bw, err.Error())
	}
}
