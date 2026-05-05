// Package qlist implements a "quicklist" — a doubly-linked list of
// fixed-capacity nodes, where each node stores up to nodeCap strings
// in a contiguous ring buffer. Modeled on Redis's quicklist (lists.c).
//
// Why this exists: the container/list-style "one allocation per
// element" pattern is the structural reason our LPUSH/RPUSH sat at
// 65-75% of Redis. Per-element nodes have a 32-byte (prev, next,
// list, value) overhead plus malloc/GC pressure. A quicklist node
// holds 128 strings in one heap object, so:
//
//   - 1 malloc per 128 pushes (vs 1 per push)
//   - cache-friendly: 128 contiguous strings vs 128 scattered nodes
//     reachable only by pointer chases
//   - 16 B/element overhead at steady state (the ring buffer slot)
//     vs container/list's 32 B/element pointer-soup
//
// API surface is intentionally narrow — exposes only what the store's
// list commands need (Push/Pop on both ends, indexed Get/Set, Insert,
// Remove, search-by-value, range/trim, forward/reverse iteration).
// Element handles deliberately don't escape: callers who need to
// modify an element by position use Set(i, v); callers who need to
// remove a specific found-by-value element use RemoveByValue.
package qlist

// nodeCap is the per-node element capacity. 128 is a reasonable middle
// ground:
//   - Lower (32, 64) → more allocs, worse cache behaviour
//   - Higher (256, 1024) → memory waste for short lists, slow LINSERT
//     in the middle (the shift cost scales linearly with cap)
// Redis's quicklist uses ziplist nodes with a configurable
// list-compress-depth; 128 mirrors their default fill behaviour.
const nodeCap = 128

// node is one packed-array unit in the quicklist. items is a ring
// buffer (head/tail circular indices) so PushFront and PushBack are
// both O(1) without shifting. count = (tail - head + nodeCap) % nodeCap
// when tail != head; the node is full when count == nodeCap (we
// distinguish "empty" from "full" via the QList.n total).
type node struct {
	prev, next *node
	items      [nodeCap]string
	head, tail int // ring indices in [0, nodeCap)
	count      int // number of valid elements (also derivable from head/tail+full-flag)
}

// QList is a quicklist. Zero value is unusable; use New() or Init().
type QList struct {
	head, tail *node
	n          int // total elements across all nodes
}

// New returns an empty QList ready for use.
func New() *QList {
	q := &QList{}
	return q
}

// Init resets the list to empty. Called by store.Init() to reuse an
// existing *QList without reallocating the wrapper struct.
func (q *QList) Init() *QList {
	q.head = nil
	q.tail = nil
	q.n = 0
	return q
}

// Len returns the total element count. O(1).
func (q *QList) Len() int { return q.n }

// ─── push / pop ─────────────────────────────────────────────────────

// PushBack appends v to the tail of the list. O(1) amortized: only
// allocates a new node when the current tail node fills up.
func (q *QList) PushBack(v string) {
	if q.tail == nil || q.tail.count == nodeCap {
		nn := &node{}
		if q.tail == nil {
			q.head = nn
			q.tail = nn
		} else {
			nn.prev = q.tail
			q.tail.next = nn
			q.tail = nn
		}
	}
	q.tail.items[q.tail.tail] = v
	q.tail.tail = (q.tail.tail + 1) % nodeCap
	q.tail.count++
	q.n++
}

// PushFront prepends v to the head of the list.
func (q *QList) PushFront(v string) {
	if q.head == nil || q.head.count == nodeCap {
		nn := &node{}
		if q.head == nil {
			q.head = nn
			q.tail = nn
		} else {
			nn.next = q.head
			q.head.prev = nn
			q.head = nn
		}
	}
	q.head.head = (q.head.head - 1 + nodeCap) % nodeCap
	q.head.items[q.head.head] = v
	q.head.count++
	q.n++
}

// PopFront removes and returns the head element. Returns ("", false)
// when the list is empty.
func (q *QList) PopFront() (string, bool) {
	if q.head == nil || q.head.count == 0 {
		return "", false
	}
	h := q.head
	v := h.items[h.head]
	h.items[h.head] = "" // release the string for GC
	h.head = (h.head + 1) % nodeCap
	h.count--
	q.n--
	if h.count == 0 {
		q.head = h.next
		if q.head == nil {
			q.tail = nil
		} else {
			q.head.prev = nil
		}
	}
	return v, true
}

// PopBack removes and returns the tail element.
func (q *QList) PopBack() (string, bool) {
	if q.tail == nil || q.tail.count == 0 {
		return "", false
	}
	t := q.tail
	t.tail = (t.tail - 1 + nodeCap) % nodeCap
	v := t.items[t.tail]
	t.items[t.tail] = ""
	t.count--
	q.n--
	if t.count == 0 {
		q.tail = t.prev
		if q.tail == nil {
			q.head = nil
		} else {
			q.tail.next = nil
		}
	}
	return v, true
}

// Front returns the head element without removing it.
func (q *QList) Front() (string, bool) {
	if q.head == nil || q.head.count == 0 {
		return "", false
	}
	return q.head.items[q.head.head], true
}

// Back returns the tail element without removing it.
func (q *QList) Back() (string, bool) {
	if q.tail == nil || q.tail.count == 0 {
		return "", false
	}
	t := q.tail
	idx := (t.tail - 1 + nodeCap) % nodeCap
	return t.items[idx], true
}

// ─── indexed access ────────────────────────────────────────────────

// locate returns the (node, slotIdx) pointing at element i (0-based,
// validated by caller). Walks node-by-node so cost is O(i / nodeCap).
func (q *QList) locate(i int) (*node, int) {
	for nd := q.head; nd != nil; nd = nd.next {
		if i < nd.count {
			return nd, (nd.head + i) % nodeCap
		}
		i -= nd.count
	}
	return nil, 0
}

// Index returns the element at i (0-based). Negative indices unsupported
// — caller normalizes (the store's normalizeRange does this).
func (q *QList) Index(i int) (string, bool) {
	if i < 0 || i >= q.n {
		return "", false
	}
	nd, slot := q.locate(i)
	if nd == nil {
		return "", false
	}
	return nd.items[slot], true
}

// Set overwrites the element at i. Returns false if i is out of range.
// Returns the previous value so callers can update byte accounting.
func (q *QList) Set(i int, v string) (string, bool) {
	if i < 0 || i >= q.n {
		return "", false
	}
	nd, slot := q.locate(i)
	if nd == nil {
		return "", false
	}
	old := nd.items[slot]
	nd.items[slot] = v
	return old, true
}

// ─── insert / remove ───────────────────────────────────────────────

// shiftRightAt moves every slot at-or-after position p one slot to
// the right within the same node, opening a free slot at p. Caller
// must guarantee count < nodeCap (room exists).
func (n *node) shiftRightAt(p int) {
	// Walk from the last valid slot backward to p, copying each
	// entry into the slot one to its right. The loop is INCLUSIVE
	// of p — the element at p must be moved so that p becomes free.
	end := (n.tail - 1 + nodeCap) % nodeCap // last valid slot
	for cur := end; ; cur = (cur - 1 + nodeCap) % nodeCap {
		next := (cur + 1) % nodeCap
		n.items[next] = n.items[cur]
		if cur == p {
			break
		}
	}
	n.tail = (n.tail + 1) % nodeCap
}

// nodeShiftLeft moves every slot strictly after position p one slot
// to the left within the same node, closing the slot at p.
func (n *node) shiftLeftAt(p int) {
	for cur := p; ; {
		next := (cur + 1) % nodeCap
		if next == n.tail {
			n.items[cur] = ""
			break
		}
		n.items[cur] = n.items[next]
		cur = next
	}
	n.tail = (n.tail - 1 + nodeCap) % nodeCap
}

// Insert inserts v before position i. i may equal q.n (append).
// Returns false on out-of-range.
func (q *QList) Insert(i int, v string) bool {
	if i < 0 || i > q.n {
		return false
	}
	if i == 0 {
		q.PushFront(v)
		return true
	}
	if i == q.n {
		q.PushBack(v)
		return true
	}
	nd, slot := q.locate(i)
	if nd == nil {
		return false
	}
	if nd.count < nodeCap {
		nd.shiftRightAt(slot)
		nd.items[slot] = v
		nd.count++
		q.n++
		return true
	}
	// Node is full — split: spill the current node's tail half into a
	// new node inserted after, then insert into whichever half holds
	// the target slot. This keeps Insert amortized O(nodeCap).
	q.splitAfter(nd)
	// Re-locate i since the structure changed.
	nd2, slot2 := q.locate(i)
	if nd2 == nil {
		return false
	}
	nd2.shiftRightAt(slot2)
	nd2.items[slot2] = v
	nd2.count++
	q.n++
	return true
}

// splitAfter splits a full node into two halves, with the second half
// becoming a new node linked in immediately after.
func (q *QList) splitAfter(nd *node) {
	right := &node{}
	half := nd.count / 2
	// Move the trailing `nd.count - half` elements into the right node.
	moved := nd.count - half
	for i := 0; i < moved; i++ {
		// Read from nd at logical position (half + i)
		srcSlot := (nd.head + half + i) % nodeCap
		right.items[i] = nd.items[srcSlot]
		nd.items[srcSlot] = ""
	}
	right.count = moved
	right.head = 0
	right.tail = moved % nodeCap
	nd.count = half
	nd.tail = (nd.head + half) % nodeCap
	// Splice into the linked list.
	right.prev = nd
	right.next = nd.next
	if nd.next != nil {
		nd.next.prev = right
	} else {
		q.tail = right
	}
	nd.next = right
}

// RemoveAt removes the element at i. Returns the old value.
func (q *QList) RemoveAt(i int) (string, bool) {
	if i < 0 || i >= q.n {
		return "", false
	}
	if i == 0 {
		return q.PopFront()
	}
	if i == q.n-1 {
		return q.PopBack()
	}
	nd, slot := q.locate(i)
	if nd == nil {
		return "", false
	}
	old := nd.items[slot]
	nd.shiftLeftAt(slot)
	nd.count--
	q.n--
	if nd.count == 0 {
		q.unlink(nd)
	}
	return old, true
}

// unlink detaches an empty node from the list.
func (q *QList) unlink(nd *node) {
	if nd.prev != nil {
		nd.prev.next = nd.next
	} else {
		q.head = nd.next
	}
	if nd.next != nil {
		nd.next.prev = nd.prev
	} else {
		q.tail = nd.prev
	}
}

// RemoveByValue removes up to |count| occurrences of v and returns
// how many were actually removed.
//   count > 0 — scan from head, remove the first count matches
//   count < 0 — scan from tail, remove the first -count matches
//   count == 0 — remove every match
//
// Used by LREM. Each removal is O(nodeCap) for the in-node shift;
// the whole walk is O(N).
func (q *QList) RemoveByValue(v string, count int) int {
	limit := count
	if limit < 0 {
		limit = -limit
	}
	removed := 0
	if count >= 0 {
		// forward
		for nd := q.head; nd != nil; {
			next := nd.next
			i := 0
			for i < nd.count {
				slot := (nd.head + i) % nodeCap
				if nd.items[slot] == v {
					nd.shiftLeftAt(slot)
					nd.count--
					q.n--
					removed++
					if count != 0 && removed >= limit {
						if nd.count == 0 {
							q.unlink(nd)
						}
						return removed
					}
					// don't advance i — the next element is now at slot
					continue
				}
				i++
			}
			if nd.count == 0 {
				q.unlink(nd)
			}
			nd = next
		}
		return removed
	}
	// backward
	for nd := q.tail; nd != nil; {
		prev := nd.prev
		i := nd.count - 1
		for i >= 0 {
			slot := (nd.head + i) % nodeCap
			if nd.items[slot] == v {
				nd.shiftLeftAt(slot)
				nd.count--
				q.n--
				removed++
				if removed >= limit {
					if nd.count == 0 {
						q.unlink(nd)
					}
					return removed
				}
				// the slot at i is now the next element; check it again
				i--
				continue
			}
			i--
		}
		if nd.count == 0 {
			q.unlink(nd)
		}
		nd = prev
	}
	return removed
}

// FindAndInsert scans the list for the first occurrence of pivot,
// inserts v before it (before=true) or after it (before=false), and
// returns the new length. Returns -1 when pivot wasn't found, leaving
// the list unchanged. Used by LINSERT.
func (q *QList) FindAndInsert(pivot string, before bool, v string) int {
	idx := 0
	for nd := q.head; nd != nil; nd = nd.next {
		for i := 0; i < nd.count; i++ {
			slot := (nd.head + i) % nodeCap
			if nd.items[slot] == pivot {
				insertAt := idx
				if !before {
					insertAt = idx + 1
				}
				if !q.Insert(insertAt, v) {
					return -1
				}
				return q.n
			}
			idx++
		}
	}
	return -1
}

// Trim restricts the list to elements [start, stop] inclusive (0-based,
// caller normalizes). After Trim, every element outside the range is
// gone. Empty range → list cleared.
func (q *QList) Trim(start, stop int) {
	if start > stop || start >= q.n {
		q.Init()
		return
	}
	if stop >= q.n {
		stop = q.n - 1
	}
	if start < 0 {
		start = 0
	}
	// Drop from front
	for i := 0; i < start; i++ {
		q.PopFront()
	}
	// Drop from back
	keep := stop - start + 1
	for q.n > keep {
		q.PopBack()
	}
}

// ─── traversal ─────────────────────────────────────────────────────

// ForEach walks the list head→tail, calling fn for each value. fn
// returning false stops the walk early.
func (q *QList) ForEach(fn func(v string) bool) {
	for nd := q.head; nd != nil; nd = nd.next {
		for i := 0; i < nd.count; i++ {
			slot := (nd.head + i) % nodeCap
			if !fn(nd.items[slot]) {
				return
			}
		}
	}
}

// ForEachReverse walks the list tail→head.
func (q *QList) ForEachReverse(fn func(v string) bool) {
	for nd := q.tail; nd != nil; nd = nd.prev {
		for i := nd.count - 1; i >= 0; i-- {
			slot := (nd.head + i) % nodeCap
			if !fn(nd.items[slot]) {
				return
			}
		}
	}
}

// Range returns the elements in [a, b] inclusive as a fresh slice.
// Caller has already validated indices via normalizeRange.
func (q *QList) Range(a, b int) []string {
	if a < 0 || b < a || a >= q.n {
		return []string{}
	}
	if b >= q.n {
		b = q.n - 1
	}
	out := make([]string, 0, b-a+1)
	idx := 0
	for nd := q.head; nd != nil && idx <= b; nd = nd.next {
		if idx+nd.count <= a {
			idx += nd.count
			continue
		}
		for i := 0; i < nd.count && idx <= b; i++ {
			if idx >= a {
				slot := (nd.head + i) % nodeCap
				out = append(out, nd.items[slot])
			}
			idx++
		}
	}
	return out
}
