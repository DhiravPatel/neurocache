package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/sentinel"
)

// sentinelCmd implements the SENTINEL surface. Activated only when
// the engine is configured as a sentinel (NEUROCACHE_SENTINEL_ENABLED).
//
// Subcommands implemented:
//
//   SENTINEL MASTERS              — every monitored master's status
//   SENTINEL MASTER <name>        — one master
//   SENTINEL SLAVES <name>        — replicas of a master (legacy alias)
//   SENTINEL REPLICAS <name>      — same; modern name
//   SENTINEL SENTINELS <name>     — peer sentinels watching this master
//   SENTINEL GET-MASTER-ADDR-BY-NAME <name>  — used by clients to bootstrap
//   SENTINEL MONITOR name host port quorum   — start watching
//   SENTINEL REMOVE name          — stop watching
//   SENTINEL RESET <pattern>      — clear bookkeeping
//   SENTINEL FAILOVER <name>      — operator-driven promotion
//   SENTINEL CKQUORUM <name>      — does the configured quorum have enough live sentinels?
//   SENTINEL PING                 — liveness probe
func (c *conn) sentinelCmd(args []string) {
	if c.eng.Sentinel == nil {
		writeError(c.bw, "ERR sentinel mode not enabled on this instance")
		return
	}
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'sentinel'")
		return
	}
	s := c.eng.Sentinel
	switch strings.ToUpper(args[0]) {
	case "PING":
		writeSimple(c.bw, "PONG")
	case "MASTERS":
		out := []any{}
		for _, m := range s.Masters() {
			out = append(out, masterStatusToReply(m.Status()))
		}
		writeValue(c.bw, out)
	case "MASTER":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL MASTER name")
			return
		}
		m := s.Master(args[1])
		if m == nil {
			writeError(c.bw, "ERR No such master")
			return
		}
		writeValue(c.bw, masterStatusToReply(m.Status()))
	case "SLAVES", "REPLICAS":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL "+args[0]+" name")
			return
		}
		m := s.Master(args[1])
		if m == nil {
			writeError(c.bw, "ERR No such master")
			return
		}
		out := []any{}
		for _, r := range m.Replicas() {
			out = append(out, []any{
				"name", r.Host + ":" + r.Port,
				"ip", r.Host, "port", r.Port,
				"flags", "slave",
			})
		}
		writeValue(c.bw, out)
	case "SENTINELS":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL SENTINELS name")
			return
		}
		out := []any{}
		for _, p := range s.Peers() {
			out = append(out, []any{
				"name", p.ID, "ip", p.Host, "port", p.Port,
			})
		}
		writeValue(c.bw, out)
	case "GET-MASTER-ADDR-BY-NAME":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL GET-MASTER-ADDR-BY-NAME name")
			return
		}
		m := s.Master(args[1])
		if m == nil {
			writeNilArray(c.bw)
			return
		}
		st := m.Status()
		writeArray(c.bw, []string{st.Host, st.Port})
	case "MONITOR":
		if len(args) < 5 {
			writeError(c.bw, "SENTINEL MONITOR name host port quorum")
			return
		}
		quorum, err := strconv.Atoi(args[4])
		if err != nil {
			writeError(c.bw, "invalid quorum")
			return
		}
		if err := s.Monitor(args[1], args[2], args[3], quorum); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REMOVE":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL REMOVE name")
			return
		}
		if !s.Remove(args[1]) {
			writeError(c.bw, "ERR No such master")
			return
		}
		writeSimple(c.bw, "OK")
	case "RESET":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL RESET name")
			return
		}
		count := 0
		for _, m := range s.Masters() {
			if matchesPattern(args[1], m.Name) {
				if s.Reset(m.Name) {
					count++
				}
			}
		}
		writeInt(c.bw, int64(count))
	case "FAILOVER":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL FAILOVER name")
			return
		}
		m := s.Master(args[1])
		if m == nil {
			writeError(c.bw, "ERR No such master")
			return
		}
		replicas := m.Replicas()
		if len(replicas) == 0 {
			writeError(c.bw, "ERR No replicas to promote")
			return
		}
		// Operator-driven failover: pick the first replica + promote.
		chosen := replicas[0]
		m.PromoteReplica(chosen.Host, chosen.Port)
		writeSimple(c.bw, "OK")
	case "CKQUORUM":
		if len(args) < 2 {
			writeError(c.bw, "SENTINEL CKQUORUM name")
			return
		}
		m := s.Master(args[1])
		if m == nil {
			writeError(c.bw, "ERR No such master")
			return
		}
		// We have at least ourselves + every learned peer to draw from.
		alive := 1 + len(s.Peers())
		st := m.Status()
		if alive >= st.Quorum {
			writeSimple(c.bw, "OK "+strconv.Itoa(alive)+" usable Sentinels")
			return
		}
		writeError(c.bw, "NOQUORUM Not enough available Sentinels to reach the specified quorum")
	default:
		writeError(c.bw, "Unknown SENTINEL subcommand "+args[0])
	}
}

// masterStatusToReply renders a MasterStatus into the canonical
// sentinel array shape clients expect.
func masterStatusToReply(s sentinel.MasterStatus) []any {
	flags := []string{"master"}
	if s.SDOWN {
		flags = append(flags, "s_down")
	}
	if s.ODOWN {
		flags = append(flags, "o_down")
	}
	if s.FailingOver {
		flags = append(flags, "failing_over")
	}
	return []any{
		"name", s.Name,
		"ip", s.Host,
		"port", s.Port,
		"runid", "",
		"flags", strings.Join(flags, ","),
		"link-pending-commands", int64(0),
		"link-refcount", int64(1),
		"last-ping-sent", s.LastOKMs,
		"last-ok-ping-reply", s.LastOKMs,
		"last-ping-reply", s.LastOKMs,
		"down-after-milliseconds", int64(30_000),
		"info-refresh", int64(0),
		"role-reported", "master",
		"role-reported-time", int64(0),
		"config-epoch", int64(0),
		"num-slaves", int64(s.NumReplicas),
		"num-other-sentinels", int64(s.NumOtherSentinels),
		"quorum", int64(s.Quorum),
	}
}

// matchesPattern is a tiny `*` glob, enough for SENTINEL RESET.
func matchesPattern(pat, s string) bool {
	if pat == "" || pat == "*" {
		return true
	}
	if !strings.Contains(pat, "*") {
		return pat == s
	}
	// strip * suffix/prefix
	if strings.HasPrefix(pat, "*") && strings.HasSuffix(pat, "*") {
		return strings.Contains(s, strings.Trim(pat, "*"))
	}
	if strings.HasPrefix(pat, "*") {
		return strings.HasSuffix(s, strings.TrimPrefix(pat, "*"))
	}
	if strings.HasSuffix(pat, "*") {
		return strings.HasPrefix(s, strings.TrimSuffix(pat, "*"))
	}
	return false
}
