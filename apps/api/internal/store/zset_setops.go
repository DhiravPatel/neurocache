package store

import (
	"errors"
	"math"
	"sort"
	"strings"
)

// ZSetOpAggregate is the per-aggregator function passed to the
// combinatorial ops. SUM is the default; MIN/MAX are commonly used
// for "best score across multiple sources" semantics.
type ZSetOpAggregate int

const (
	ZAggSum ZSetOpAggregate = iota
	ZAggMin
	ZAggMax
)

// ParseZSetOpAggregate decodes the AGGREGATE token.
func ParseZSetOpAggregate(s string) (ZSetOpAggregate, error) {
	switch strings.ToUpper(s) {
	case "SUM":
		return ZAggSum, nil
	case "MIN":
		return ZAggMin, nil
	case "MAX":
		return ZAggMax, nil
	}
	return 0, errors.New("syntax error: AGGREGATE must be SUM | MIN | MAX")
}

// zsetSource is one input to the combinatorial ops. Either Key or
// Members will be populated; weight scales every contribution.
type zsetSource struct {
	key     string
	weight  float64
	members []ZPair // populated for Set sources (treat each member as score 1)
}

// collectZSetMembers returns (member -> score) for one input. Sets
// behave as zsets-with-score-1, mirroring Redis. Missing keys return
// an empty map. Caller must already hold the read lock on the shard
// owning `key`.
func (s *Store) collectZSetMembers(key string) (map[string]float64, error) {
	sh := s.shardForKey(key)
	e, ok := sh.data[key]
	if !ok {
		return map[string]float64{}, nil
	}
	switch e.Type {
	case TypeZSet:
		out := map[string]float64{}
		for _, m := range e.ZSet.members() {
			sc, _ := e.ZSet.Score(m)
			out[m] = sc
		}
		return out, nil
	case TypeSet:
		out := map[string]float64{}
		for m := range e.Set {
			out[m] = 1
		}
		return out, nil
	}
	return nil, ErrWrongType
}

// ZUnionStore writes the weighted union into dest. Returns its size.
// weights == nil means every input contributes 1×.
func (s *Store) ZUnionStore(dest string, keys []string, weights []float64, agg ZSetOpAggregate) (int, error) {
	all := append([]string{dest}, keys...)
	involved := s.shardsFor(all)
	unlock := s.lockShardsW(involved)
	defer unlock()
	merged, err := s.zsetUnion(keys, weights, agg)
	if err != nil {
		return 0, err
	}
	return s.replaceZSetLocked(dest, merged)
}

// ZInterStore writes the weighted intersection into dest.
func (s *Store) ZInterStore(dest string, keys []string, weights []float64, agg ZSetOpAggregate) (int, error) {
	all := append([]string{dest}, keys...)
	involved := s.shardsFor(all)
	unlock := s.lockShardsW(involved)
	defer unlock()
	merged, err := s.zsetInter(keys, weights, agg)
	if err != nil {
		return 0, err
	}
	return s.replaceZSetLocked(dest, merged)
}

// ZDiffStore writes (keys[0] minus the union of keys[1:]) into dest.
func (s *Store) ZDiffStore(dest string, keys []string) (int, error) {
	all := append([]string{dest}, keys...)
	involved := s.shardsFor(all)
	unlock := s.lockShardsW(involved)
	defer unlock()
	if len(keys) == 0 {
		return s.replaceZSetLocked(dest, nil)
	}
	primary, err := s.collectZSetMembers(keys[0])
	if err != nil {
		return 0, err
	}
	for _, k := range keys[1:] {
		other, err := s.collectZSetMembers(k)
		if err != nil {
			return 0, err
		}
		for m := range other {
			delete(primary, m)
		}
	}
	return s.replaceZSetLocked(dest, primary)
}

// ZUnion returns the weighted union without storing — backs the
// non-STORE forms ZUNION / ZINTER / ZDIFF added in Redis 6.2.
func (s *Store) ZUnion(keys []string, weights []float64, agg ZSetOpAggregate) ([]ZRangeResult, error) {
	involved := s.shardsFor(keys)
	unlock := s.lockShardsR(involved)
	defer unlock()
	merged, err := s.zsetUnion(keys, weights, agg)
	if err != nil {
		return nil, err
	}
	return sortMembers(merged), nil
}

func (s *Store) ZInter(keys []string, weights []float64, agg ZSetOpAggregate) ([]ZRangeResult, error) {
	involved := s.shardsFor(keys)
	unlock := s.lockShardsR(involved)
	defer unlock()
	merged, err := s.zsetInter(keys, weights, agg)
	if err != nil {
		return nil, err
	}
	return sortMembers(merged), nil
}

func (s *Store) ZDiff(keys []string) ([]ZRangeResult, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	involved := s.shardsFor(keys)
	unlock := s.lockShardsR(involved)
	defer unlock()
	primary, err := s.collectZSetMembers(keys[0])
	if err != nil {
		return nil, err
	}
	for _, k := range keys[1:] {
		other, err := s.collectZSetMembers(k)
		if err != nil {
			return nil, err
		}
		for m := range other {
			delete(primary, m)
		}
	}
	return sortMembers(primary), nil
}

// ZInterCard counts the intersection without materialising it. limit > 0
// short-circuits the scan as soon as the count is reached.
func (s *Store) ZInterCard(keys []string, limit int) (int, error) {
	involved := s.shardsFor(keys)
	unlock := s.lockShardsR(involved)
	defer unlock()
	merged, err := s.zsetInter(keys, nil, ZAggSum)
	if err != nil {
		return 0, err
	}
	if limit > 0 && len(merged) > limit {
		return limit, nil
	}
	return len(merged), nil
}

// zsetUnion is the shared internal — returns the merged map. Default
// aggregator SUM, default weight 1. Caller must hold read locks on
// every shard owning an input key.
func (s *Store) zsetUnion(keys []string, weights []float64, agg ZSetOpAggregate) (map[string]float64, error) {
	out := map[string]float64{}
	seen := map[string]bool{}
	for i, k := range keys {
		w := 1.0
		if i < len(weights) {
			w = weights[i]
		}
		members, err := s.collectZSetMembers(k)
		if err != nil {
			return nil, err
		}
		for m, sc := range members {
			contribution := sc * w
			if cur, ok := out[m]; ok {
				out[m] = combine(cur, contribution, agg)
			} else if !seen[m] {
				out[m] = contribution
				seen[m] = true
			}
		}
	}
	return out, nil
}

func (s *Store) zsetInter(keys []string, weights []float64, agg ZSetOpAggregate) (map[string]float64, error) {
	if len(keys) == 0 {
		return map[string]float64{}, nil
	}
	maps := make([]map[string]float64, len(keys))
	for i, k := range keys {
		m, err := s.collectZSetMembers(k)
		if err != nil {
			return nil, err
		}
		maps[i] = m
	}
	// Pick the smallest input as the iteration base.
	base := 0
	for i := range maps {
		if len(maps[i]) < len(maps[base]) {
			base = i
		}
	}
	out := map[string]float64{}
next:
	for m := range maps[base] {
		acc := 0.0
		first := true
		for i, src := range maps {
			sc, ok := src[m]
			if !ok {
				continue next
			}
			w := 1.0
			if i < len(weights) {
				w = weights[i]
			}
			contribution := sc * w
			if first {
				acc = contribution
				first = false
				continue
			}
			acc = combine(acc, contribution, agg)
		}
		out[m] = acc
	}
	return out, nil
}

func combine(a, b float64, agg ZSetOpAggregate) float64 {
	switch agg {
	case ZAggMin:
		return math.Min(a, b)
	case ZAggMax:
		return math.Max(a, b)
	}
	return a + b
}

// replaceZSet wipes dest and writes merged into it. Returns the
// resulting cardinality. Locks the destination's shard internally —
// for use by callers that don't hold any locks (GeoSearchStore,
// ZRangeStore).
func (s *Store) replaceZSet(dest string, merged map[string]float64) (int, error) {
	sh := s.shardForKey(dest)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return s.replaceZSetLocked(dest, merged)
}

// replaceZSetLocked is the internal variant — caller must already
// hold the destination shard's write lock. Used by the *Store ops
// that take all involved shards up front for atomicity.
func (s *Store) replaceZSetLocked(dest string, merged map[string]float64) (int, error) {
	sh := s.shardForKey(dest)
	if old, ok := sh.data[dest]; ok {
		s.bytes.Add(-int64(old.Bytes))
		delete(sh.data, dest)
	}
	if len(merged) == 0 {
		return 0, nil
	}
	e, err := s.getOrCreate(sh, dest, TypeZSet)
	if err != nil {
		return 0, err
	}
	for m, sc := range merged {
		e.ZSet.AddNew(sc, m)
	}
	s.recomputeBytes(e)
	s.fire("zadd", dest)
	return e.ZSet.Len(), nil
}

func sortMembers(m map[string]float64) []ZRangeResult {
	out := make([]ZRangeResult, 0, len(m))
	for member, sc := range m {
		out = append(out, ZRangeResult{Member: member, Score: sc})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Member < out[j].Member
	})
	return out
}

// ── lex ranges ─────────────────────────────────────────────────────

// ZRangeByLex returns members whose names sit in the lex range
// [min, max]. The range tokens accept "-" / "+" as -∞ / +∞ and
// "[abc" / "(abc" as inclusive / exclusive bounds — Redis syntax.
//
// Lex order is the byte-comparison order; this only makes semantic
// sense when every member shares the same score (Redis documents this).
func (s *Store) ZRangeByLex(key, minStr, maxStr string, offset, count int, reverse bool) ([]string, error) {
	min, minInc, minInf, err := parseLexBound(minStr, true)
	if err != nil {
		return nil, err
	}
	max, maxInc, maxInf, err := parseLexBound(maxStr, false)
	if err != nil {
		return nil, err
	}
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return []string{}, err
	}
	all := e.ZSet.members()
	sort.Strings(all)
	out := []string{}
	for _, m := range all {
		if !minInf {
			cmp := strings.Compare(m, min)
			if cmp < 0 || (cmp == 0 && !minInc) {
				continue
			}
		}
		if !maxInf {
			cmp := strings.Compare(m, max)
			if cmp > 0 || (cmp == 0 && !maxInc) {
				continue
			}
		}
		out = append(out, m)
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	if offset > len(out) {
		return []string{}, nil
	}
	out = out[offset:]
	if count > 0 && count < len(out) {
		out = out[:count]
	}
	return out, nil
}

// ZLexCount counts members in a lex range.
func (s *Store) ZLexCount(key, minStr, maxStr string) (int, error) {
	out, err := s.ZRangeByLex(key, minStr, maxStr, 0, 0, false)
	if err != nil {
		return 0, err
	}
	return len(out), nil
}

// ZRangeStore copies the result of a range query into a destination zset.
// rangeBy controls the source scan: "INDEX" / "BYSCORE" / "BYLEX".
func (s *Store) ZRangeStore(dest, src, startStr, stopStr, rangeBy string, offset, count int, reverse bool) (int, error) {
	merged := map[string]float64{}
	switch strings.ToUpper(rangeBy) {
	case "BYSCORE":
		minArg, maxArg := startStr, stopStr
		if reverse {
			minArg, maxArg = stopStr, startStr
		}
		results, err := s.ZRangeByScore(src, minArg, maxArg, offset, count, reverse)
		if err != nil {
			return 0, err
		}
		for _, r := range results {
			merged[r.Member] = r.Score
		}
	case "BYLEX":
		members, err := s.ZRangeByLex(src, startStr, stopStr, offset, count, reverse)
		if err != nil {
			return 0, err
		}
		// Lex range — every member keeps its original score.
		shS := s.shardForKey(src)
		shS.mu.RLock()
		e, ok, _ := shS.get(src, TypeZSet)
		if ok {
			for _, m := range members {
				sc, _ := e.ZSet.Score(m)
				merged[m] = sc
			}
		}
		shS.mu.RUnlock()
	default:
		// INDEX
		start, _ := atoiSafe(startStr)
		stop, _ := atoiSafe(stopStr)
		results, err := s.ZRange(src, start, stop, true, reverse)
		if err != nil {
			return 0, err
		}
		for _, r := range results {
			merged[r.Member] = r.Score
		}
	}
	return s.replaceZSet(dest, merged)
}

func atoiSafe(s string) (int, error) {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, errors.New("not an int")
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

// parseLexBound decodes Redis's lex-range token: "-" / "+" / "[<v>" / "(<v>".
// Returns (value, inclusive, isInfinity, error).
func parseLexBound(s string, isMin bool) (string, bool, bool, error) {
	if s == "-" {
		if !isMin {
			return "", false, false, errors.New("syntax error in lex range")
		}
		return "", false, true, nil
	}
	if s == "+" {
		if isMin {
			return "", false, false, errors.New("syntax error in lex range")
		}
		return "", false, true, nil
	}
	if len(s) < 2 {
		return "", false, false, errors.New("min/max must be prefixed by [ or (")
	}
	switch s[0] {
	case '[':
		return s[1:], true, false, nil
	case '(':
		return s[1:], false, false, nil
	}
	return "", false, false, errors.New("min/max must be prefixed by [ or (")
}

// ── multi-key pop helpers ──────────────────────────────────────────

// ZMPop pops up to count members from the first non-empty zset among
// keys, preferring max scores when reverse is true. Returns
// (key, popped) — or ("", nil) when every key is empty.
func (s *Store) ZMPop(keys []string, reverse bool, count int) (string, []ZRangeResult, error) {
	if count <= 0 {
		count = 1
	}
	for _, k := range keys {
		sh := s.shardForKey(k)
		sh.mu.Lock()
		e, ok, err := sh.get(k, TypeZSet)
		if err != nil {
			sh.mu.Unlock()
			return "", nil, err
		}
		if !ok || e.ZSet.Len() == 0 {
			sh.mu.Unlock()
			continue
		}
		out := []ZRangeResult{}
		for i := 0; i < count && e.ZSet.Len() > 0; i++ {
			var m string
			var sc float64
			if reverse {
				m, sc, _ = e.ZSet.PopMax()
			} else {
				m, sc, _ = e.ZSet.PopMin()
			}
			out = append(out, ZRangeResult{Member: m, Score: sc})
		}
		s.recomputeBytes(e)
		s.removeIfEmpty(sh, e)
		sh.mu.Unlock()
		s.fire("zpop", k)
		return k, out, nil
	}
	return "", nil, nil
}

// LMPop pops up to count elements from the first non-empty list,
// preferring head when fromTail is false.
func (s *Store) LMPop(keys []string, fromTail bool, count int) (string, []string, error) {
	if count <= 0 {
		count = 1
	}
	for _, k := range keys {
		sh := s.shardForKey(k)
		sh.mu.Lock()
		e, ok, err := sh.get(k, TypeList)
		if err != nil {
			sh.mu.Unlock()
			return "", nil, err
		}
		if !ok || e.List.Len() == 0 {
			sh.mu.Unlock()
			continue
		}
		out := []string{}
		for i := 0; i < count && e.List.Len() > 0; i++ {
			var v string
			var ok bool
			if fromTail {
				v, ok = e.List.PopBack()
			} else {
				v, ok = e.List.PopFront()
			}
			if !ok {
				break
			}
			out = append(out, v)
		}
		s.recomputeBytes(e)
		s.removeIfEmpty(sh, e)
		sh.mu.Unlock()
		s.fire("lpop", k)
		return k, out, nil
	}
	return "", nil, nil
}
