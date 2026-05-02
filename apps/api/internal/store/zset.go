package store

import (
	"errors"
	"math"
	"strconv"
)

// ZPair is a (score, member) tuple used on the write path.
type ZPair struct {
	Score  float64
	Member string
}

// ZAdd inserts or updates members. Returns the number of *new* members.
func (s *Store) ZAdd(key string, pairs ...ZPair) (int, error) {
	if len(pairs) == 0 {
		return 0, errors.New("ZADD requires at least one score/member pair")
	}
	sh := s.shardForKey(key)
	sh.mu.Lock()
	e, err := s.getOrCreate(sh, key, TypeZSet)
	if err != nil {
		sh.mu.Unlock()
		return 0, err
	}
	added := 0
	delta := 0
	for _, p := range pairs {
		if e.ZSet.AddNew(p.Score, p.Member) {
			added++
			delta += len(p.Member) + 8
		}
	}
	s.addBytes(e, delta)
	sh.mu.Unlock()
	s.fire("zadd", key)
	return added, nil
}

// ZScore returns the score for a member.
func (s *Store) ZScore(key, member string) (float64, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, false, err
	}
	sc, had := e.ZSet.Score(member)
	return sc, had, nil
}

// ZRem deletes members; returns the count actually removed.
func (s *Store) ZRem(key string, members ...string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, err
	}
	removed := 0
	delta := 0
	for _, m := range members {
		if e.ZSet.Remove(m) {
			removed++
			delta -= len(m) + 8
		}
	}
	s.addBytes(e, delta)
	s.removeIfEmpty(sh, e)
	return removed, nil
}

// ZCard returns the number of members.
func (s *Store) ZCard(key string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, err
	}
	return e.ZSet.Len(), nil
}

// ZIncrBy adds delta to member's score and returns the new score.
func (s *Store) ZIncrBy(key string, delta float64, member string) (float64, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, err := s.getOrCreate(sh, key, TypeZSet)
	if err != nil {
		return 0, err
	}
	_, existed := e.ZSet.Score(member)
	sc := e.ZSet.IncrBy(delta, member)
	if !existed {
		s.addBytes(e, len(member)+8)
	}
	return sc, nil
}

// ZRank returns member's 0-based rank (ascending).
func (s *Store) ZRank(key, member string) (int, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, false, err
	}
	r, had := e.ZSet.Rank(member)
	return r, had, nil
}

// ZRevRank returns member's 0-based rank (descending).
func (s *Store) ZRevRank(key, member string) (int, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, false, err
	}
	r, had := e.ZSet.RevRank(member)
	return r, had, nil
}

// ZRange returns [start,stop] by index; reverse walks from the tail.
func (s *Store) ZRange(key string, start, stop int, withScores, reverse bool) ([]ZRangeResult, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return []ZRangeResult{}, err
	}
	return e.ZSet.Range(start, stop, withScores, reverse), nil
}

// ZRangeByScore filters by score. minStr / maxStr accept "(exclusive" and
// "+inf"/"-inf" per Redis syntax.
func (s *Store) ZRangeByScore(key, minStr, maxStr string, offset, count int, reverse bool) ([]ZRangeResult, error) {
	min, minEx, err := parseZScoreBound(minStr)
	if err != nil {
		return nil, err
	}
	max, maxEx, err := parseZScoreBound(maxStr)
	if err != nil {
		return nil, err
	}
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return []ZRangeResult{}, err
	}
	return e.ZSet.RangeByScore(min, max, minEx, maxEx, offset, count, reverse), nil
}

// ZCount counts members with score in [min,max].
func (s *Store) ZCount(key, minStr, maxStr string) (int, error) {
	min, _, err := parseZScoreBound(minStr)
	if err != nil {
		return 0, err
	}
	max, _, err := parseZScoreBound(maxStr)
	if err != nil {
		return 0, err
	}
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, err
	}
	return e.ZSet.Count(min, max), nil
}

// ZPopMin / ZPopMax remove and return ends.
func (s *Store) ZPopMin(key string) (string, float64, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return "", 0, false, err
	}
	m, sc, had := e.ZSet.PopMin()
	if !had {
		return "", 0, false, nil
	}
	s.addBytes(e, -(len(m) + 8))
	s.removeIfEmpty(sh, e)
	return m, sc, true, nil
}

func (s *Store) ZPopMax(key string) (string, float64, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return "", 0, false, err
	}
	m, sc, had := e.ZSet.PopMax()
	if !had {
		return "", 0, false, nil
	}
	s.addBytes(e, -(len(m) + 8))
	s.removeIfEmpty(sh, e)
	return m, sc, true, nil
}

// parseZScoreBound decodes a Redis ZRANGEBYSCORE bound.
//
//	"(5"  -> 5, exclusive
//	"+inf" / "-inf" -> math.Inf(±1), inclusive
//	"2.5" -> 2.5, inclusive
func parseZScoreBound(s string) (float64, bool, error) {
	ex := false
	if len(s) > 0 && s[0] == '(' {
		ex = true
		s = s[1:]
	}
	switch s {
	case "+inf", "+INF", "inf", "INF":
		return posInf, ex, nil
	case "-inf", "-INF":
		return negInf, ex, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, errors.New("ERR min or max is not a float")
	}
	return v, ex, nil
}

var (
	posInf = math.Inf(1)
	negInf = math.Inf(-1)
)
