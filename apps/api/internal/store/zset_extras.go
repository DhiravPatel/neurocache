package store

import (
	"errors"
	"math/rand"
)

// ZMScore returns a parallel score slice for members. Missing members
// are reported via the returned hits[] slice (true = present, score
// valid; false = absent, score is zero). Mirrors Redis 6.2 ZMSCORE.
func (s *Store) ZMScore(key string, members ...string) ([]float64, []bool, error) {
	scores := make([]float64, len(members))
	hits := make([]bool, len(members))
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return scores, hits, err
	}
	for i, m := range members {
		sc, had := e.ZSet.Score(m)
		scores[i] = sc
		hits[i] = had
	}
	return scores, hits, nil
}

// ZRandMember returns up to count members, optionally with scores.
// Behaviour mirrors Redis 6.2:
//
//   count == 0 (sentinel for "no count given"): one random member, no
//                                               array — caller picks.
//   count > 0:  unique members, capped at the set length.
//   count < 0:  may repeat; |count| samples drawn with replacement.
//
// Returns (members, scores, ok). When the key is missing or empty the
// result slices are empty and ok=false.
func (s *Store) ZRandMember(key string, count int, withScores bool) ([]string, []float64, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return nil, nil, false, err
	}
	all := e.ZSet.members()
	if len(all) == 0 {
		return nil, nil, false, nil
	}
	scoreOf := func(m string) float64 {
		sc, _ := e.ZSet.Score(m)
		return sc
	}
	// Single-member form: count == 0 is the dispatcher's "no count" flag.
	if count == 0 {
		m := all[rand.Intn(len(all))]
		out := []string{m}
		var scs []float64
		if withScores {
			scs = []float64{scoreOf(m)}
		}
		return out, scs, true, nil
	}
	if count > 0 {
		// without replacement — Fisher–Yates over a copy
		n := count
		if n > len(all) {
			n = len(all)
		}
		shuffled := append([]string(nil), all...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		out := shuffled[:n]
		var scs []float64
		if withScores {
			scs = make([]float64, n)
			for i, m := range out {
				scs[i] = scoreOf(m)
			}
		}
		return out, scs, true, nil
	}
	// count < 0 — sample with replacement
	n := -count
	out := make([]string, n)
	var scs []float64
	if withScores {
		scs = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		m := all[rand.Intn(len(all))]
		out[i] = m
		if withScores {
			scs[i] = scoreOf(m)
		}
	}
	return out, scs, true, nil
}

// ZRemRangeByRank deletes every member whose 0-based rank sits in
// [start, stop] (negatives count from the tail, inclusive stop).
// Returns the number of removed members. Empty range is not an error.
func (s *Store) ZRemRangeByRank(key string, start, stop int) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, err
	}
	n := e.ZSet.Len()
	a, b, empty := normalizeRange(start, stop, n)
	if empty {
		return 0, nil
	}
	// Materialise the slice of victims first — we can't safely mutate
	// the skiplist while iterating it in parallel.
	victims := make([]string, 0, b-a+1)
	idx := 0
	for cur := e.ZSet.head.levels[0].forward; cur != nil; cur = cur.levels[0].forward {
		if idx > b {
			break
		}
		if idx >= a {
			victims = append(victims, cur.member)
		}
		idx++
	}
	for _, m := range victims {
		e.ZSet.Remove(m)
	}
	s.recomputeBytes(e)
	s.removeIfEmpty(sh, e)
	s.fire("zrem", key)
	return len(victims), nil
}

// ZRemRangeByScore deletes members whose score sits in [min, max] using
// the same bound syntax as ZRANGEBYSCORE ("(5", "+inf", "-inf").
// Returns the number of removed members.
func (s *Store) ZRemRangeByScore(key, minStr, maxStr string) (int, error) {
	min, minEx, err := parseZScoreBound(minStr)
	if err != nil {
		return 0, err
	}
	max, maxEx, err := parseZScoreBound(maxStr)
	if err != nil {
		return 0, err
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, err
	}
	check := func(sc float64) bool {
		if minEx && sc <= min || !minEx && sc < min {
			return false
		}
		if maxEx && sc >= max || !maxEx && sc > max {
			return false
		}
		return true
	}
	victims := []string{}
	for cur := e.ZSet.head.levels[0].forward; cur != nil; cur = cur.levels[0].forward {
		if check(cur.score) {
			victims = append(victims, cur.member)
		}
	}
	for _, m := range victims {
		e.ZSet.Remove(m)
	}
	s.recomputeBytes(e)
	s.removeIfEmpty(sh, e)
	s.fire("zrem", key)
	return len(victims), nil
}

// ZRemRangeByLex deletes members whose name sits in the lex range
// [min, max]. Lex range tokens accept "-"/"+" for ±∞ and "[v"/"(v" for
// inclusive/exclusive bounds — same syntax as ZRANGEBYLEX.
func (s *Store) ZRemRangeByLex(key, minStr, maxStr string) (int, error) {
	min, minInc, minInf, err := parseLexBound(minStr, true)
	if err != nil {
		return 0, err
	}
	max, maxInc, maxInf, err := parseLexBound(maxStr, false)
	if err != nil {
		return 0, err
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, err
	}
	cmp := func(a, b string) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	victims := []string{}
	for _, m := range e.ZSet.members() {
		if !minInf {
			c := cmp(m, min)
			if c < 0 || (c == 0 && !minInc) {
				continue
			}
		}
		if !maxInf {
			c := cmp(m, max)
			if c > 0 || (c == 0 && !maxInc) {
				continue
			}
		}
		victims = append(victims, m)
	}
	for _, m := range victims {
		e.ZSet.Remove(m)
	}
	s.recomputeBytes(e)
	s.removeIfEmpty(sh, e)
	s.fire("zrem", key)
	return len(victims), nil
}

// silence "imported and not used" if a refactor drops errors usage.
var _ = errors.New
