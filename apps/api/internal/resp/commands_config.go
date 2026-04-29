package resp

import (
	"strings"
	"time"
)

// configCmd routes CONFIG GET / SET / REWRITE / RESETSTAT. The
// runtime cfg holder lives on engine.RuntimeCfg so settings persist
// across reconnects without a process restart.
func (c *conn) configCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'config'")
		return
	}
	rt := c.eng.RuntimeCfg
	switch strings.ToUpper(args[0]) {
	case "GET":
		pattern := "*"
		if len(args) >= 2 {
			pattern = args[1]
		}
		out := rt.Get(pattern)
		writeArray(c.bw, out)
	case "SET":
		// Redis 7+ accepts multi-pair: CONFIG SET k1 v1 k2 v2 ...
		if len(args) < 3 || (len(args)-1)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for 'config|set'")
			return
		}
		for i := 1; i+1 < len(args); i += 2 {
			if err := rt.Set(args[i], args[i+1]); err != nil {
				writeError(c.bw, err.Error())
				return
			}
		}
		// Some live-tunable settings have side effects on the engine
		// (slowlog threshold, latency len, …). Re-apply them here so
		// the change takes effect without a restart.
		c.eng.SlowLog.SetThreshold(c.slowlogThreshold())
		writeSimple(c.bw, "OK")
	case "REWRITE":
		if err := rt.Rewrite(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESETSTAT":
		c.eng.Metrics.Stop()
		c.eng.SlowLog.Reset()
		c.eng.Latency.Reset()
		rt.ResetStat()
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown CONFIG subcommand "+args[0])
	}
}

// slowlogThreshold reads the live config value as a time.Duration so
// CONFIG SET slowlog-log-slower-than has immediate effect.
func (c *conn) slowlogThreshold() time.Duration {
	return time.Duration(c.eng.Cfg.SlowLogThreshold) * time.Microsecond
}
