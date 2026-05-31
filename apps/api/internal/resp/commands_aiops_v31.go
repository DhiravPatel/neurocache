package resp

import (
	"strconv"
	"strings"
	"time"
)

// Phase 14 RESP handlers. Three families of primitives that complete
// the "more than one agent, less than full trust" tier of NeuroCache:
//
//   Multi-agent coordination — AGENT.BB.*, AGENT.BUS.*, HANDOFF.*
//   Governance / audit       — PROV.*, TRUST.*, ISOLATE.*, CONSENT.*
//   Health / feedback / ops  — VECSPACE.*, PREF.*, RISK.BUDGET.*,
//                              CFCACHE.*, BLAST.*, CAUSAL.*,
//                              CONTRACT.*, WHATIF.*, GRAPH.EXTRACT.*
//
// Each handler follows the same shape: subcommand switch, validate
// args, call into the llmstack manager, RESP-encode the result.

// agentBBCmd handles AGENT.BB.* — multi-agent blackboard.
func (c *conn) agentBBCmd(sub string, args []string) {
	switch sub {
	case "POST":
		if len(args) < 3 {
			writeError(c.bw, "usage: AGENT.BB.POST run agent text [TAGS t1,t2,...]")
			return
		}
		var tags []string
		for i := 3; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "TAGS" {
				writeError(c.bw, "unknown AGENT.BB.POST option: "+key)
				return
			}
			tags = strings.Split(args[i+1], ",")
		}
		r, err := c.eng.AgentBB.Post(args[0], args[1], args[2], tags)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, r.PostID)
	case "READ":
		if len(args) < 2 {
			writeError(c.bw, "usage: AGENT.BB.READ run query [K n] [MIN_SIM f]")
			return
		}
		k := 5
		minSim := 0.0
		for i := 2; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "K":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "K must be non-negative integer")
					return
				}
				k = n
			case "MIN_SIM":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "MIN_SIM must be float")
					return
				}
				minSim = f
			default:
				writeError(c.bw, "unknown AGENT.BB.READ option: "+key)
				return
			}
		}
		rows, ok := c.eng.AgentBB.Read(args[0], args[1], k, minSim)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"post_id", strconv.FormatInt(r.PostID, 10),
				"agent_id", r.AgentID,
				"text", r.Text,
				"tags", strings.Join(r.Tags, ","),
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "LIST":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BB.LIST run [LIMIT n] [TAG t]")
			return
		}
		limit := 20
		tag := ""
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "LIMIT":
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					writeError(c.bw, "LIMIT must be non-negative integer")
					return
				}
				limit = n
			case "TAG":
				tag = args[i+1]
			default:
				writeError(c.bw, "unknown AGENT.BB.LIST option: "+key)
				return
			}
		}
		rows, ok := c.eng.AgentBB.List(args[0], limit, tag)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"post_id", strconv.FormatInt(r.PostID, 10),
				"agent_id", r.AgentID,
				"text", r.Text,
				"tags", strings.Join(r.Tags, ","),
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "CLAIM":
		if len(args) < 3 {
			writeError(c.bw, "usage: AGENT.BB.CLAIM run task agent [TTL ms]")
			return
		}
		var ttl time.Duration
		for i := 3; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "TTL" {
				writeError(c.bw, "unknown AGENT.BB.CLAIM option: "+key)
				return
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || n < 0 {
				writeError(c.bw, "TTL must be non-negative integer (ms)")
				return
			}
			ttl = time.Duration(n) * time.Millisecond
		}
		r, err := c.eng.AgentBB.Claim(args[0], args[1], args[2], ttl)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		claimed := "0"
		if r.Claimed {
			claimed = "1"
		}
		writeArray(c.bw, []string{
			"claimed", claimed,
			"owner", r.Owner,
			"ttl_ms", strconv.FormatInt(r.TTLMS, 10),
		})
	case "RELEASE":
		if len(args) < 3 {
			writeError(c.bw, "usage: AGENT.BB.RELEASE run task agent")
			return
		}
		n, err := c.eng.AgentBB.Release(args[0], args[1], args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "CLAIMS":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BB.CLAIMS run")
			return
		}
		rows, ok := c.eng.AgentBB.Claims(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			expired := "0"
			if r.Expired {
				expired = "1"
			}
			out = append(out, []any{
				"task_id", r.TaskID,
				"agent_id", r.AgentID,
				"claimed_unix", strconv.FormatInt(r.ClaimedAt, 10),
				"ttl_ms", strconv.FormatInt(r.TTLMS, 10),
				"expired", expired,
			})
		}
		writeValue(c.bw, out)
	case "DROP":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BB.DROP run|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.AgentBB.Drop(args[0])))
	case "LIST_RUNS":
		writeArray(c.bw, c.eng.AgentBB.ListRuns())
	case "STATS":
		s := c.eng.AgentBB.Stats()
		writeArray(c.bw, []string{
			"runs", strconv.Itoa(s.Runs),
			"total_posts", strconv.FormatInt(s.TotalPosts, 10),
			"total_reads", strconv.FormatInt(s.TotalReads, 10),
			"total_claims", strconv.FormatInt(s.TotalClaims, 10),
			"claim_conflicts", strconv.FormatInt(s.ClaimConflicts, 10),
			"active_posts", strconv.Itoa(s.ActivePosts),
			"active_claims", strconv.Itoa(s.ActiveClaims),
		})
	default:
		writeError(c.bw, "unknown AGENT.BB subcommand: "+sub)
	}
}

// agentBusCmd handles AGENT.BUS.* — semantic message bus.
func (c *conn) agentBusCmd(sub string, args []string) {
	switch sub {
	case "REGISTER":
		if len(args) < 2 {
			writeError(c.bw, "usage: AGENT.BUS.REGISTER agent capability")
			return
		}
		if err := c.eng.AgentBus.Register(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "UNREGISTER":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BUS.UNREGISTER agent")
			return
		}
		writeInt(c.bw, int64(c.eng.AgentBus.Unregister(args[0])))
	case "SEND":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BUS.SEND message [MIN_SIM f] [FROM agent]")
			return
		}
		minSim := 0.0
		from := ""
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			switch key {
			case "MIN_SIM":
				f, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					writeError(c.bw, "MIN_SIM must be float")
					return
				}
				minSim = f
			case "FROM":
				from = args[i+1]
			default:
				writeError(c.bw, "unknown AGENT.BUS.SEND option: "+key)
				return
			}
		}
		r, err := c.eng.AgentBus.Send(args[0], minSim, from)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"msg_id", strconv.FormatInt(r.MsgID, 10),
			"routed_to", r.RoutedTo,
			"match", strconv.FormatFloat(r.Match, 'f', 4, 64),
		})
	case "RECV":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BUS.RECV agent [LIMIT n]")
			return
		}
		limit := 32
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown AGENT.BUS.RECV option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.AgentBus.Recv(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"msg_id", strconv.FormatInt(r.MsgID, 10),
				"from", r.From,
				"text", r.Text,
				"match", strconv.FormatFloat(r.MatchScore, 'f', 4, 64),
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "ACK":
		if len(args) < 2 {
			writeError(c.bw, "usage: AGENT.BUS.ACK agent msg-id")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "msg-id must be integer")
			return
		}
		n, err := c.eng.AgentBus.Ack(args[0], id)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeInt(c.bw, int64(n))
	case "AGENTS":
		rows := c.eng.AgentBus.Agents()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"agent_id", r.AgentID,
				"capability", r.Capability,
				"pending", strconv.Itoa(r.Pending),
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "PENDING":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BUS.PENDING agent")
			return
		}
		n, ok := c.eng.AgentBus.Pending(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeInt(c.bw, int64(n))
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "usage: AGENT.BUS.RESET agent|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.AgentBus.Reset(args[0])))
	case "STATS":
		s := c.eng.AgentBus.Stats()
		writeArray(c.bw, []string{
			"agents", strconv.Itoa(s.Agents),
			"total_sent", strconv.FormatInt(s.TotalSent, 10),
			"total_recv", strconv.FormatInt(s.TotalRecv, 10),
			"total_acks", strconv.FormatInt(s.TotalAcks, 10),
			"unrouted", strconv.FormatInt(s.Unrouted, 10),
			"total_pending", strconv.Itoa(s.TotalPending),
		})
	default:
		writeError(c.bw, "unknown AGENT.BUS subcommand: "+sub)
	}
}
