package resp

import (
	"strconv"
	"strings"
	"time"
)

// policySemCmd handles POLICY.SEM.* — semantic firewall by example.
func (c *conn) policySemCmd(sub string, args []string) {
	switch sub {
	case "DEFINE":
		// POLICY.SEM.DEFINE id ACTION action SEEDS s1 s2 ...
		if len(args) < 5 {
			writeError(c.bw, "wrong number of arguments for 'policy.sem.define'")
			return
		}
		if !strings.EqualFold(args[1], "ACTION") {
			writeError(c.bw, "expected ACTION keyword at arg 2")
			return
		}
		action := args[2]
		if !strings.EqualFold(args[3], "SEEDS") {
			writeError(c.bw, "expected SEEDS keyword at arg 4")
			return
		}
		seeds := args[4:]
		if err := c.eng.PolicySem.Define(args[0], action, seeds); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'policy.sem.add'")
			return
		}
		if err := c.eng.PolicySem.Add(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "REMOVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'policy.sem.remove'")
			return
		}
		idx, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "seed_idx must be an integer")
			return
		}
		if c.eng.PolicySem.Remove(args[0], idx) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "CHECK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'policy.sem.check'")
			return
		}
		threshold := 0.0
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "THRESHOLD" {
				writeError(c.bw, "unknown POLICY.SEM.CHECK option: "+key)
				return
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				writeError(c.bw, "THRESHOLD must be a float")
				return
			}
			threshold = f
			i += 2
		}
		r, ok := c.eng.PolicySem.Check(args[0], args[1], threshold)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNPOLICY", "no policy registered for that id")
			return
		}
		matchedInt := "0"
		if r.Matched {
			matchedInt = "1"
		}
		writeArray(c.bw, []string{
			"matched", matchedInt,
			"action", r.Action,
			"nearest_score", strconv.FormatFloat(r.NearestScore, 'f', 4, 64),
			"matched_seed_idx", strconv.Itoa(r.MatchedSeedIdx),
			"matched_seed", r.MatchedSeed,
		})
	case "LIST":
		policyID := ""
		if len(args) >= 1 {
			policyID = args[0]
		}
		rows := c.eng.PolicySem.List(policyID)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			seedsAny := make([]any, 0, len(r.Seeds))
			for _, s := range r.Seeds {
				seedsAny = append(seedsAny, s)
			}
			out = append(out, []any{
				"policy_id", r.PolicyID,
				"action", r.Action,
				"seeds", seedsAny,
				"seed_count", strconv.Itoa(r.SeedN),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'policy.sem.forget'")
			return
		}
		if c.eng.PolicySem.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.PolicySem.Stats()
		writeArray(c.bw, []string{
			"policies", strconv.Itoa(s.Policies),
			"total_defines", strconv.FormatInt(s.TotalDefines, 10),
			"total_checks", strconv.FormatInt(s.TotalChecks, 10),
			"total_blocks", strconv.FormatInt(s.TotalBlocks, 10),
			"total_allows", strconv.FormatInt(s.TotalAllows, 10),
			"total_escalates", strconv.FormatInt(s.TotalEscalates, 10),
		})
	default:
		writeError(c.bw, "unknown POLICY.SEM subcommand: "+sub)
	}
}

// noveltyCmd handles NOVELTY.* — per-query OOD gate.
func (c *conn) noveltyCmd(sub string, args []string) {
	switch sub {
	case "BASELINE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'novelty.baseline'")
			return
		}
		if err := c.eng.Novelty.Baseline(args[0], args[1:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "ADD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'novelty.add'")
			return
		}
		if err := c.eng.Novelty.Add(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SCORE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'novelty.score'")
			return
		}
		r, ok := c.eng.Novelty.Score(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNDETECTOR", "no detector registered for that id")
			return
		}
		writeArray(c.bw, []string{
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"verdict", r.Verdict,
			"nearest_score", strconv.FormatFloat(r.NearestScore, 'f', 4, 64),
			"nearest_text", r.NearestText,
		})
	case "SET_THRESHOLDS":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'novelty.set_thresholds'")
			return
		}
		ok, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "ok threshold must be a float")
			return
		}
		bad, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "bad threshold must be a float")
			return
		}
		if err := c.eng.Novelty.SetThresholds(args[0], ok, bad); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SIZE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'novelty.size'")
			return
		}
		n, _ := c.eng.Novelty.Size(args[0])
		writeInt(c.bw, int64(n))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'novelty.forget'")
			return
		}
		if c.eng.Novelty.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "DETECTORS":
		rows := c.eng.Novelty.Detectors()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"detector_id", r.DetectorID,
				"baseline_size", strconv.Itoa(r.BaselineSize),
				"threshold_ok", strconv.FormatFloat(r.ThresholdOK, 'f', 4, 64),
				"threshold_bad", strconv.FormatFloat(r.ThresholdBad, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Novelty.Stats()
		writeArray(c.bw, []string{
			"detectors", strconv.Itoa(s.Detectors),
			"total_scores", strconv.FormatInt(s.TotalScores, 10),
			"total_in_distribution", strconv.FormatInt(s.TotalInDist, 10),
			"total_borderline", strconv.FormatInt(s.TotalBorderline, 10),
			"total_novel", strconv.FormatInt(s.TotalNovel, 10),
		})
	default:
		writeError(c.bw, "unknown NOVELTY subcommand: "+sub)
	}
}

// locksemCmd handles LOCK.SEM.* — semantic dedup-locks.
func (c *conn) locksemCmd(sub string, args []string) {
	switch sub {
	case "ACQUIRE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'lock.sem.acquire'")
			return
		}
		threshold := 0.0
		var ttl time.Duration
		i := 2
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
				threshold = f
			case "TTL":
				n, err := strconv.ParseInt(val, 10, 64)
				if err != nil || n <= 0 {
					writeError(c.bw, "TTL must be a positive integer (ms)")
					return
				}
				ttl = time.Duration(n) * time.Millisecond
			default:
				writeError(c.bw, "unknown LOCK.SEM.ACQUIRE option: "+key)
				return
			}
			i += 2
		}
		r, err := c.eng.SemLocks.Acquire(args[0], args[1], threshold, ttl)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		acqInt := "0"
		if r.Acquired {
			acqInt = "1"
		}
		writeArray(c.bw, []string{
			"acquired", acqInt,
			"token", r.Token,
			"similar_text", r.SimilarText,
			"similar_score", strconv.FormatFloat(r.SimilarScore, 'f', 4, 64),
		})
	case "RELEASE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'lock.sem.release'")
			return
		}
		if c.eng.SemLocks.Release(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'lock.sem.status'")
			return
		}
		limit := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "LIMIT" {
				writeError(c.bw, "unknown LOCK.SEM.STATUS option: "+key)
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
		rows := c.eng.SemLocks.Status(args[0], limit)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"token", r.Token,
				"text", r.Text,
				"age_ms", strconv.FormatInt(r.AgeMS, 10),
				"remain_ms", strconv.FormatInt(r.RemainMS, 10),
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'lock.sem.forget'")
			return
		}
		writeInt(c.bw, int64(c.eng.SemLocks.ForgetByText(args[0], args[1])))
	case "FORGET_NAMESPACE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'lock.sem.forget_namespace'")
			return
		}
		writeInt(c.bw, int64(c.eng.SemLocks.ForgetNamespace(args[0])))
	case "STATS":
		s := c.eng.SemLocks.Stats()
		writeArray(c.bw, []string{
			"namespaces", strconv.Itoa(s.Namespaces),
			"held_now", strconv.Itoa(s.HeldNow),
			"total_acquires", strconv.FormatInt(s.TotalAcquires, 10),
			"total_acquired", strconv.FormatInt(s.TotalAcquired, 10),
			"total_rejected", strconv.FormatInt(s.TotalRejected, 10),
			"total_releases", strconv.FormatInt(s.TotalReleases, 10),
			"total_expiries", strconv.FormatInt(s.TotalExpiries, 10),
		})
	default:
		writeError(c.bw, "unknown LOCK.SEM subcommand: "+sub)
	}
}
