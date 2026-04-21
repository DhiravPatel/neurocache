package store

import (
	"math"
	"math/rand"
)

// Skiplist implementation backing sorted sets. Uses the standard
// Pugh-style design with an O(log n) expected cost for search / insert /
// delete. Ordering key is (score asc, member asc) — identical to Redis.
const (
	zskMaxLevel = 32
	zskP        = 0.25
)

// zskNode is a single skiplist cell.
type zskNode struct {
	member   string
	score    float64
	backward *zskNode
	levels   []zskLevel
}

type zskLevel struct {
	forward *zskNode
	span    uint32 // number of nodes traversed by this forward pointer
}

// ZSet is a combined skiplist + hash-for-O(1)-lookup.
type ZSet struct {
	head   *zskNode
	tail   *zskNode
	level  int
	length int
	dict   map[string]float64 // member -> score for O(1) ZSCORE / ZRANK
}

func newZSet() *ZSet {
	return &ZSet{
		head:  makeNode(zskMaxLevel, 0, ""),
		level: 1,
		dict:  map[string]float64{},
	}
}

func makeNode(lvl int, score float64, member string) *zskNode {
	return &zskNode{
		member: member,
		score:  score,
		levels: make([]zskLevel, lvl),
	}
}

func randLevel() int {
	lvl := 1
	for rand.Float64() < zskP && lvl < zskMaxLevel {
		lvl++
	}
	return lvl
}

// Len returns the number of members.
func (z *ZSet) Len() int { return z.length }

// members returns every member (in no particular order) — used by the
// byte-size estimator.
func (z *ZSet) members() []string {
	out := make([]string, 0, z.length)
	for n := z.head.levels[0].forward; n != nil; n = n.levels[0].forward {
		out = append(out, n.member)
	}
	return out
}

// Add inserts or updates a member. Returns true when it was newly added.
func (z *ZSet) Add(score float64, member string) bool {
	if cur, ok := z.dict[member]; ok {
		if cur == score {
			return false
		}
		z.remove(cur, member)
	}
	z.insert(score, member)
	z.dict[member] = score
	_, existed := z.dict[member]
	return !existed // cannot happen, but kept for symmetry
}

// AddNew is Add with a clear "was this new?" boolean result.
func (z *ZSet) AddNew(score float64, member string) (newMember bool) {
	if cur, ok := z.dict[member]; ok {
		if cur == score {
			return false
		}
		z.remove(cur, member)
		z.insert(score, member)
		z.dict[member] = score
		return false
	}
	z.insert(score, member)
	z.dict[member] = score
	return true
}

// Score returns the score of member.
func (z *ZSet) Score(member string) (float64, bool) {
	s, ok := z.dict[member]
	return s, ok
}

// Remove deletes member, returning whether it existed.
func (z *ZSet) Remove(member string) bool {
	score, ok := z.dict[member]
	if !ok {
		return false
	}
	z.remove(score, member)
	delete(z.dict, member)
	return true
}

// IncrBy adds delta to member's score (creating it at 0 if absent) and
// returns the new score.
func (z *ZSet) IncrBy(delta float64, member string) float64 {
	cur, ok := z.dict[member]
	if ok {
		z.remove(cur, member)
	}
	s := cur + delta
	z.insert(s, member)
	z.dict[member] = s
	return s
}

// Rank returns the 0-based rank of member (ascending order).
func (z *ZSet) Rank(member string) (int, bool) {
	score, ok := z.dict[member]
	if !ok {
		return 0, false
	}
	rank := uint32(0)
	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		for x.levels[i].forward != nil && lessOrEqual(x.levels[i].forward.score, x.levels[i].forward.member, score, member) {
			rank += x.levels[i].span
			x = x.levels[i].forward
			if x.member == member {
				return int(rank) - 1, true
			}
		}
	}
	return 0, false
}

// RevRank is Rank from the tail.
func (z *ZSet) RevRank(member string) (int, bool) {
	r, ok := z.Rank(member)
	if !ok {
		return 0, false
	}
	return z.length - 1 - r, true
}

// ZRangeResult pairs a member with its score.
type ZRangeResult struct {
	Member string
	Score  float64
}

// Range returns members in [start,stop] (0-based, negatives supported).
func (z *ZSet) Range(start, stop int, withScores, reverse bool) []ZRangeResult {
	a, b, empty := normalizeRange(start, stop, z.length)
	if empty {
		return nil
	}
	out := make([]ZRangeResult, 0, b-a+1)
	if reverse {
		// walk from tail
		i := 0
		for n := z.tail; n != nil; n = n.backward {
			if i > b {
				break
			}
			if i >= a {
				out = append(out, ZRangeResult{n.member, scoreOrZero(n.score, withScores)})
			}
			i++
		}
	} else {
		i := 0
		for n := z.head.levels[0].forward; n != nil; n = n.levels[0].forward {
			if i > b {
				break
			}
			if i >= a {
				out = append(out, ZRangeResult{n.member, scoreOrZero(n.score, withScores)})
			}
			i++
		}
	}
	return out
}

// RangeByScore returns members whose score is in [min,max], with optional
// exclusive bounds, LIMIT offset / count, and reverse iteration.
func (z *ZSet) RangeByScore(min, max float64, minEx, maxEx bool, offset, count int, reverse bool) []ZRangeResult {
	out := []ZRangeResult{}
	add := func(n *zskNode) {
		out = append(out, ZRangeResult{n.member, n.score})
	}
	check := func(s float64) bool {
		if minEx && s <= min || !minEx && s < min {
			return false
		}
		if maxEx && s >= max || !maxEx && s > max {
			return false
		}
		return true
	}
	skip := 0
	if reverse {
		for n := z.tail; n != nil; n = n.backward {
			if !check(n.score) {
				continue
			}
			if skip < offset {
				skip++
				continue
			}
			add(n)
			if count > 0 && len(out) >= count {
				break
			}
		}
	} else {
		for n := z.head.levels[0].forward; n != nil; n = n.levels[0].forward {
			if !check(n.score) {
				continue
			}
			if skip < offset {
				skip++
				continue
			}
			add(n)
			if count > 0 && len(out) >= count {
				break
			}
		}
	}
	return out
}

// Count counts members whose score is in [min,max] (inclusive).
func (z *ZSet) Count(min, max float64) int {
	n := 0
	for cur := z.head.levels[0].forward; cur != nil; cur = cur.levels[0].forward {
		if cur.score < min {
			continue
		}
		if cur.score > max {
			break
		}
		n++
	}
	return n
}

// PopMin removes and returns the lowest-score member.
func (z *ZSet) PopMin() (string, float64, bool) {
	n := z.head.levels[0].forward
	if n == nil {
		return "", 0, false
	}
	z.remove(n.score, n.member)
	delete(z.dict, n.member)
	return n.member, n.score, true
}

// PopMax removes and returns the highest-score member.
func (z *ZSet) PopMax() (string, float64, bool) {
	if z.tail == nil {
		return "", 0, false
	}
	n := z.tail
	z.remove(n.score, n.member)
	delete(z.dict, n.member)
	return n.member, n.score, true
}

// ─── internal ──────────────────────────────────────────────────────────

func (z *ZSet) insert(score float64, member string) {
	update := make([]*zskNode, zskMaxLevel)
	rank := make([]uint32, zskMaxLevel)
	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		if i == z.level-1 {
			rank[i] = 0
		} else {
			rank[i] = rank[i+1]
		}
		for x.levels[i].forward != nil && less(x.levels[i].forward.score, x.levels[i].forward.member, score, member) {
			rank[i] += x.levels[i].span
			x = x.levels[i].forward
		}
		update[i] = x
	}

	lvl := randLevel()
	if lvl > z.level {
		for i := z.level; i < lvl; i++ {
			rank[i] = 0
			update[i] = z.head
			update[i].levels[i].span = uint32(z.length)
		}
		z.level = lvl
	}

	n := makeNode(lvl, score, member)
	for i := 0; i < lvl; i++ {
		n.levels[i].forward = update[i].levels[i].forward
		update[i].levels[i].forward = n
		n.levels[i].span = update[i].levels[i].span - (rank[0] - rank[i])
		update[i].levels[i].span = (rank[0] - rank[i]) + 1
	}
	for i := lvl; i < z.level; i++ {
		update[i].levels[i].span++
	}
	if update[0] == z.head {
		n.backward = nil
	} else {
		n.backward = update[0]
	}
	if n.levels[0].forward != nil {
		n.levels[0].forward.backward = n
	} else {
		z.tail = n
	}
	z.length++
}

func (z *ZSet) remove(score float64, member string) {
	update := make([]*zskNode, zskMaxLevel)
	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		for x.levels[i].forward != nil && less(x.levels[i].forward.score, x.levels[i].forward.member, score, member) {
			x = x.levels[i].forward
		}
		update[i] = x
	}
	target := x.levels[0].forward
	if target == nil || target.score != score || target.member != member {
		return
	}
	for i := 0; i < z.level; i++ {
		if update[i].levels[i].forward == target {
			update[i].levels[i].span += target.levels[i].span - 1
			update[i].levels[i].forward = target.levels[i].forward
		} else {
			update[i].levels[i].span--
		}
	}
	if target.levels[0].forward != nil {
		target.levels[0].forward.backward = target.backward
	} else {
		z.tail = target.backward
	}
	for z.level > 1 && z.head.levels[z.level-1].forward == nil {
		z.level--
	}
	z.length--
}

// less implements (score asc, member asc) comparison used by the skiplist.
func less(as float64, am string, bs float64, bm string) bool {
	if as != bs {
		return as < bs
	}
	return am < bm
}

func lessOrEqual(as float64, am string, bs float64, bm string) bool {
	if as != bs {
		return as < bs
	}
	return am <= bm
}

func scoreOrZero(s float64, with bool) float64 {
	if with {
		return s
	}
	return math.NaN()
}
