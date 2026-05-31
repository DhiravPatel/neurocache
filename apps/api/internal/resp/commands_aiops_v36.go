package resp

import (
	"strconv"
	"strings"
	"time"
)

// Phase 15 handlers — Part 2 of 4. TUNE + FED + DEBATE + QUORUM.

// tuneCmd handles TUNE.* — Bayesian/bandit self-tuning.
func (c *conn) tuneCmd(sub string, args []string) {
	switch sub {
	case "KNOB":
		if len(args) < 5 || strings.ToUpper(args[2]) != "RANGE" {
			writeError(c.bw, "usage: TUNE.KNOB id knob RANGE low high [BUCKETS n]")
			return
		}
		low, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "low must be float")
			return
		}
		high, err := strconv.ParseFloat(args[4], 64)
		if err != nil {
			writeError(c.bw, "high must be float")
			return
		}
		buckets := 10
		if len(args) >= 7 && strings.ToUpper(args[5]) == "BUCKETS" {
			n, err := strconv.Atoi(args[6])
			if err != nil || n < 1 {
				writeError(c.bw, "BUCKETS must be positive integer")
				return
			}
			buckets = n
		}
		if err := c.eng.Tune.Knob(args[0], args[1], low, high, buckets); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "OBJECTIVE":
		if len(args) < 3 {
			writeError(c.bw, "usage: TUNE.OBJECTIVE id MAXIMIZE|MINIMIZE \"expr\"")
			return
		}
		dir := strings.ToLower(args[1])
		switch dir {
		case "maximize":
			dir = "max"
		case "minimize":
			dir = "min"
		}
		if err := c.eng.Tune.Objective(args[0], dir, args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SUGGEST":
		if len(args) < 1 {
			writeError(c.bw, "usage: TUNE.SUGGEST id")
			return
		}
		v, ok := c.eng.Tune.Suggest(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeFloat(c.bw, v)
	case "OBSERVE":
		if len(args) < 4 || strings.ToUpper(args[2]) != "METRIC" {
			writeError(c.bw, "usage: TUNE.OBSERVE id value METRIC k v [METRIC k v ...]")
			return
		}
		value, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "value must be float")
			return
		}
		metrics := map[string]float64{}
		for i := 2; i+2 < len(args); i += 3 {
			if strings.ToUpper(args[i]) != "METRIC" {
				writeError(c.bw, "expected METRIC keyword at arg "+strconv.Itoa(i))
				return
			}
			f, err := strconv.ParseFloat(args[i+2], 64)
			if err != nil {
				writeError(c.bw, "metric value must be float")
				return
			}
			metrics[args[i+1]] = f
		}
		r, err := c.eng.Tune.Observe(args[0], value, metrics)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"bucket_center", strconv.FormatFloat(r.BucketCenter, 'f', 4, 64),
			"raw_reward", strconv.FormatFloat(r.RawReward, 'f', 4, 64),
			"norm_reward", strconv.FormatFloat(r.NormReward, 'f', 4, 64),
		})
	case "APPLY":
		if len(args) < 1 {
			writeError(c.bw, "usage: TUNE.APPLY id")
			return
		}
		r, ok := c.eng.Tune.Apply(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"best_value", strconv.FormatFloat(r.BestValue, 'f', 4, 64),
			"projected_lift_vs_random", strconv.FormatFloat(r.ProjectedLift, 'f', 4, 64),
			"trials", strconv.FormatInt(r.Trials, 10),
			"confidence", r.Confidence,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: TUNE.STATUS id")
			return
		}
		st, ok := c.eng.Tune.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		buckets := make([]any, 0, len(st.Buckets))
		for _, b := range st.Buckets {
			buckets = append(buckets, []any{
				"center", strconv.FormatFloat(b.Center, 'f', 4, 64),
				"alpha", strconv.FormatFloat(b.Alpha, 'f', 4, 64),
				"beta", strconv.FormatFloat(b.Beta, 'f', 4, 64),
				"n", strconv.FormatInt(b.N, 10),
				"mean", strconv.FormatFloat(b.Mean, 'f', 4, 64),
				"mean_reward", strconv.FormatFloat(b.MeanReward, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"tune_id", st.TuneID,
			"knob", st.Knob,
			"direction", st.Direction,
			"objective", st.Objective,
			"buckets", buckets,
		})
	case "HISTORY":
		if len(args) < 1 {
			writeError(c.bw, "usage: TUNE.HISTORY id [LIMIT n]")
			return
		}
		limit := 0
		for i := 1; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "LIMIT" {
				writeError(c.bw, "unknown TUNE.HISTORY option: "+key)
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				writeError(c.bw, "LIMIT must be non-negative integer")
				return
			}
			limit = n
		}
		rows, ok := c.eng.Tune.History(args[0], limit)
		if !ok {
			writeNilArray(c.bw)
			return
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"bucket_center", strconv.FormatFloat(r.BucketCenter, 'f', 4, 64),
				"value", strconv.FormatFloat(r.Value, 'f', 4, 64),
				"reward", strconv.FormatFloat(r.Reward, 'f', 4, 64),
				"at_unix", strconv.FormatInt(r.AtUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: TUNE.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Tune.Forget(args[0])))
	case "LIST":
		writeArray(c.bw, c.eng.Tune.List())
	case "STATS":
		s := c.eng.Tune.Stats()
		writeArray(c.bw, []string{
			"tuners", strconv.Itoa(s.Tuners),
			"total_suggests", strconv.FormatInt(s.TotalSuggests, 10),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
		})
	default:
		writeError(c.bw, "unknown TUNE subcommand: "+sub)
	}
}

// fedCmd handles FED.* — federated meta-learning.
func (c *conn) fedCmd(sub string, args []string) {
	switch sub {
	case "NODE":
		if len(args) < 1 {
			writeError(c.bw, "usage: FED.NODE node-id")
			return
		}
		if err := c.eng.Fed.Node(args[0]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "EXPORT":
		kind := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "KIND" {
				writeError(c.bw, "unknown FED.EXPORT option: "+key)
				return
			}
			kind = args[i+1]
		}
		exp := c.eng.Fed.Export(kind)
		signals := make([]any, 0, len(exp.Signals))
		for _, s := range exp.Signals {
			signals = append(signals, []any{
				"kind", s.Kind, "key", s.Key,
				"alpha", strconv.FormatFloat(s.Alpha, 'f', 4, 64),
				"beta", strconv.FormatFloat(s.Beta, 'f', 4, 64),
				"n", strconv.FormatInt(s.N, 10),
				"updated_unix", strconv.FormatInt(s.UpdatedAt, 10),
			})
		}
		writeValue(c.bw, []any{
			"node_id", exp.NodeID,
			"signals", signals,
		})
	case "MERGE":
		// FED.MERGE peer-id k1 key1 alpha1 beta1 n1 k2 key2 alpha2 beta2 n2 ...
		// (5 args per signal after peer-id)
		if len(args) < 1 || (len(args)-1)%5 != 0 {
			writeError(c.bw, "usage: FED.MERGE peer-id kind1 key1 alpha1 beta1 n1 ...")
			return
		}
		peer := args[0]
		signals := []struct {
			Kind  string
			Key   string
			Alpha float64
			Beta  float64
			N     int64
		}{}
		for i := 1; i+4 < len(args); i += 5 {
			a, err := strconv.ParseFloat(args[i+2], 64)
			if err != nil {
				writeError(c.bw, "alpha must be float at arg "+strconv.Itoa(i+2))
				return
			}
			b, err := strconv.ParseFloat(args[i+3], 64)
			if err != nil {
				writeError(c.bw, "beta must be float at arg "+strconv.Itoa(i+3))
				return
			}
			nn, err := strconv.ParseInt(args[i+4], 10, 64)
			if err != nil {
				writeError(c.bw, "n must be integer at arg "+strconv.Itoa(i+4))
				return
			}
			signals = append(signals, struct {
				Kind  string
				Key   string
				Alpha float64
				Beta  float64
				N     int64
			}{args[i], args[i+1], a, b, nn})
		}
		// Convert to llmstack fedSignal type — but the type isn't exported.
		// Workaround: call MERGE with the constructed slice via the
		// helper-friendly Update API (one Update per signal).
		merged := 0
		for _, s := range signals {
			if err := c.eng.Fed.Update(s.Kind, s.Key, s.Alpha, s.Beta, s.N); err == nil {
				merged++
			}
		}
		// Also touch the peers map; do this through a self-merge of empty list
		_, _ = c.eng.Fed.Merge(peer, nil)
		writeInt(c.bw, int64(merged))
	case "SIGNAL":
		if len(args) < 4 {
			writeError(c.bw, "usage: FED.SIGNAL kind key alpha beta [N n]")
			return
		}
		alpha, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "alpha must be float")
			return
		}
		beta, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			writeError(c.bw, "beta must be float")
			return
		}
		var n int64
		if len(args) >= 6 && strings.ToUpper(args[4]) == "N" {
			nn, err := strconv.ParseInt(args[5], 10, 64)
			if err != nil {
				writeError(c.bw, "N must be integer")
				return
			}
			n = nn
		}
		if err := c.eng.Fed.Signal(args[0], args[1], alpha, beta, n); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "GET":
		if len(args) < 2 {
			writeError(c.bw, "usage: FED.GET kind key")
			return
		}
		s, ok := c.eng.Fed.Get(args[0], args[1])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		writeArray(c.bw, []string{
			"kind", s.Kind, "key", s.Key,
			"alpha", strconv.FormatFloat(s.Alpha, 'f', 4, 64),
			"beta", strconv.FormatFloat(s.Beta, 'f', 4, 64),
			"n", strconv.FormatInt(s.N, 10),
			"updated_unix", strconv.FormatInt(s.UpdatedAt, 10),
		})
	case "PEERS":
		rows := c.eng.Fed.Peers()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"node_id", r.NodeID,
				"last_merge_unix", strconv.FormatInt(r.LastMergeUnix, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: FED.FORGET kind key|ALL")
			return
		}
		if args[0] == "ALL" {
			writeInt(c.bw, int64(c.eng.Fed.Forget("ALL", "")))
			return
		}
		if len(args) < 2 {
			writeError(c.bw, "usage: FED.FORGET kind key")
			return
		}
		writeInt(c.bw, int64(c.eng.Fed.Forget(args[0], args[1])))
	case "STATS":
		s := c.eng.Fed.Stats()
		writeArray(c.bw, []string{
			"node_id", s.NodeID,
			"signals", strconv.Itoa(s.Signals),
			"peers", strconv.Itoa(s.Peers),
			"total_updates", strconv.FormatInt(s.TotalUpdates, 10),
			"total_exports", strconv.FormatInt(s.TotalExports, 10),
			"total_merges", strconv.FormatInt(s.TotalMerges, 10),
		})
	default:
		writeError(c.bw, "unknown FED subcommand: "+sub)
	}
}

// debateCmd handles DEBATE.* — multi-agent consensus.
func (c *conn) debateCmd(sub string, args []string) {
	switch sub {
	case "START":
		if len(args) < 3 {
			writeError(c.bw, "usage: DEBATE.START id proposer \"proposal\"")
			return
		}
		if err := c.eng.Debate.Start(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CRITIQUE":
		if len(args) < 3 {
			writeError(c.bw, "usage: DEBATE.CRITIQUE id agent \"text\"")
			return
		}
		if err := c.eng.Debate.Critique(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REVISE":
		if len(args) < 3 {
			writeError(c.bw, "usage: DEBATE.REVISE id proposer \"proposal\"")
			return
		}
		if err := c.eng.Debate.Revise(args[0], args[1], args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "VOTE":
		if len(args) < 3 {
			writeError(c.bw, "usage: DEBATE.VOTE id agent approve|reject [REASON r]")
			return
		}
		var approve bool
		switch strings.ToLower(args[2]) {
		case "approve", "yes", "y", "true", "1":
			approve = true
		case "reject", "no", "n", "false", "0":
			approve = false
		default:
			writeError(c.bw, "vote must be approve|reject")
			return
		}
		reason := ""
		if len(args) >= 5 && strings.ToUpper(args[3]) == "REASON" {
			reason = args[4]
		}
		if err := c.eng.Debate.Vote(args[0], args[1], approve, reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RESOLVE":
		if len(args) < 1 {
			writeError(c.bw, "usage: DEBATE.RESOLVE id [QUORUM n]")
			return
		}
		quorum := 0
		if len(args) >= 3 && strings.ToUpper(args[1]) == "QUORUM" {
			n, err := strconv.Atoi(args[2])
			if err != nil || n < 0 {
				writeError(c.bw, "QUORUM must be non-negative integer")
				return
			}
			quorum = n
		}
		r, err := c.eng.Debate.Resolve(args[0], quorum)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		approved := "0"
		if r.Approved {
			approved = "1"
		}
		writeArray(c.bw, []string{
			"debate_id", r.DebateID,
			"approved", approved,
			"votes", strconv.Itoa(r.Votes),
			"approve_n", strconv.Itoa(r.ApproveN),
			"reject_n", strconv.Itoa(r.RejectN),
			"quorum", strconv.Itoa(r.Quorum),
			"dissent", strings.Join(r.Dissent, ","),
		})
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "usage: DEBATE.GET id")
			return
		}
		v, ok := c.eng.Debate.Get(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		critiques := make([]any, 0, len(v.Critiques))
		for _, cr := range v.Critiques {
			critiques = append(critiques, []any{
				"agent", cr.Agent, "text", cr.Text,
				"on_rev", strconv.Itoa(cr.OnRev),
				"at_unix", strconv.FormatInt(cr.AtUnix, 10),
			})
		}
		votes := make([]any, 0, len(v.Votes))
		for _, vt := range v.Votes {
			ap := "0"
			if vt.Approve {
				ap = "1"
			}
			votes = append(votes, []any{
				"agent", vt.Agent, "approve", ap, "reason", vt.Reason,
				"on_rev", strconv.Itoa(vt.OnRev),
				"at_unix", strconv.FormatInt(vt.AtUnix, 10),
			})
		}
		ap := "0"
		if v.Approved {
			ap = "1"
		}
		writeValue(c.bw, []any{
			"debate_id", v.DebateID,
			"proposer", v.Proposer,
			"state", v.State,
			"revision", strconv.Itoa(v.Revision),
			"proposal", v.Proposal,
			"approved", ap,
			"dissent", strings.Join(v.Dissent, ","),
			"started_unix", strconv.FormatInt(v.StartedUnix, 10),
			"critiques", critiques,
			"votes", votes,
		})
	case "LIST":
		state := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STATE" {
				writeError(c.bw, "unknown DEBATE.LIST option: "+key)
				return
			}
			state = args[i+1]
		}
		rows := c.eng.Debate.List(state)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"debate_id", r.DebateID,
				"state", r.State,
				"revision", strconv.Itoa(r.Revision),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: DEBATE.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Debate.Forget(args[0])))
	case "STATS":
		s := c.eng.Debate.Stats()
		writeArray(c.bw, []string{
			"debates", strconv.Itoa(s.Debates),
			"total_starts", strconv.FormatInt(s.TotalStarts, 10),
			"total_critiques", strconv.FormatInt(s.TotalCritiques, 10),
			"total_votes", strconv.FormatInt(s.TotalVotes, 10),
			"total_resolves", strconv.FormatInt(s.TotalResolves, 10),
		})
	default:
		writeError(c.bw, "unknown DEBATE subcommand: "+sub)
	}
}

// quorumCmd handles QUORUM.* — N-of-M approval gate.
func (c *conn) quorumCmd(sub string, args []string) {
	switch sub {
	case "PROPOSE":
		// QUORUM.PROPOSE id payload QUORUM n VOTERS a1,a2,... [DEADLINE ms]
		if len(args) < 6 || strings.ToUpper(args[2]) != "QUORUM" || strings.ToUpper(args[4]) != "VOTERS" {
			writeError(c.bw, "usage: QUORUM.PROPOSE id payload QUORUM n VOTERS a1,a2,... [DEADLINE ms]")
			return
		}
		n, err := strconv.Atoi(args[3])
		if err != nil {
			writeError(c.bw, "QUORUM must be integer")
			return
		}
		voters := strings.Split(args[5], ",")
		var deadline time.Duration
		if len(args) >= 8 && strings.ToUpper(args[6]) == "DEADLINE" {
			ms, err := strconv.ParseInt(args[7], 10, 64)
			if err != nil || ms < 0 {
				writeError(c.bw, "DEADLINE must be non-negative integer (ms)")
				return
			}
			deadline = time.Duration(ms) * time.Millisecond
		}
		if err := c.eng.Quorum.Propose(args[0], args[1], n, voters, deadline); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "APPROVE":
		if len(args) < 2 {
			writeError(c.bw, "usage: QUORUM.APPROVE id agent [REASON r]")
			return
		}
		reason := ""
		if len(args) >= 4 && strings.ToUpper(args[2]) == "REASON" {
			reason = args[3]
		}
		if err := c.eng.Quorum.Approve(args[0], args[1], reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REJECT":
		if len(args) < 2 {
			writeError(c.bw, "usage: QUORUM.REJECT id agent [REASON r]")
			return
		}
		reason := ""
		if len(args) >= 4 && strings.ToUpper(args[2]) == "REASON" {
			reason = args[3]
		}
		if err := c.eng.Quorum.Reject(args[0], args[1], reason); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "COMMIT":
		if len(args) < 1 {
			writeError(c.bw, "usage: QUORUM.COMMIT id")
			return
		}
		r, err := c.eng.Quorum.Commit(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeArray(c.bw, []string{
			"gate_id", r.GateID,
			"state", r.State,
			"approvals", strconv.Itoa(r.Approvals),
			"quorum_n", strconv.Itoa(r.QuorumN),
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "usage: QUORUM.STATUS id")
			return
		}
		st, ok := c.eng.Quorum.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		approvals := make([]any, 0, len(st.Approvals))
		for _, a := range st.Approvals {
			approvals = append(approvals, []any{
				"agent", a.Agent, "reason", a.Reason,
				"at_unix", strconv.FormatInt(a.AtUnix, 10),
			})
		}
		rejects := make([]any, 0, len(st.Rejects))
		for _, r := range st.Rejects {
			rejects = append(rejects, []any{
				"agent", r.Agent, "reason", r.Reason,
				"at_unix", strconv.FormatInt(r.AtUnix, 10),
			})
		}
		writeValue(c.bw, []any{
			"gate_id", st.GateID,
			"state", st.State,
			"payload", st.Payload,
			"quorum_n", strconv.Itoa(st.QuorumN),
			"voters", strings.Join(st.Voters, ","),
			"deadline_unix", strconv.FormatInt(st.DeadlineUnix, 10),
			"approvals", approvals,
			"rejects", rejects,
		})
	case "LIST":
		state := ""
		for i := 0; i+1 <= len(args); i += 2 {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			if key != "STATE" {
				writeError(c.bw, "unknown QUORUM.LIST option: "+key)
				return
			}
			state = args[i+1]
		}
		rows := c.eng.Quorum.List(state)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"gate_id", r.GateID, "state", r.State,
				"approvals", strconv.Itoa(r.Approvals),
				"quorum_n", strconv.Itoa(r.QuorumN),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "usage: QUORUM.FORGET id|ALL")
			return
		}
		writeInt(c.bw, int64(c.eng.Quorum.Forget(args[0])))
	case "STATS":
		s := c.eng.Quorum.Stats()
		writeArray(c.bw, []string{
			"gates", strconv.Itoa(s.Gates),
			"total_proposals", strconv.FormatInt(s.TotalProposals, 10),
			"total_approvals", strconv.FormatInt(s.TotalApprovals, 10),
			"total_rejects", strconv.FormatInt(s.TotalRejects, 10),
			"total_commits", strconv.FormatInt(s.TotalCommits, 10),
		})
	default:
		writeError(c.bw, "unknown QUORUM subcommand: "+sub)
	}
}

