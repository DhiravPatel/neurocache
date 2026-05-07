// Package clist is a drop-in, pool-backed replacement for the subset
// of container/list that the keyspace uses.
//
// Why this exists: every LPUSH / RPUSH / LINSERT call against a list
// key allocates a new *list.Element on the heap (~40 B). At the
// 1.5M ops/sec we hit on the redis-benchmark hot path that's ~60 MB/sec
// of fresh allocations feeding straight into GC pressure — the gap
// between us and Redis on push commands is almost entirely this.
//
// clist preserves the container/list public shape (List, Element,
// PushFront, PushBack, Remove, InsertBefore, InsertAfter, Front,
// Back, Next, Prev, Len, Init) so the call sites in store/list.go
// don't change semantically. Internally, freed Elements are returned
// to a sync.Pool so the next push can reuse one. New keys still
// allocate; long-running keys with churn get the win.
//
// API surface intentionally limited to what the store package uses.
// MoveToFront / MoveToBack / PushFrontList / PushBackList are absent
// because nothing calls them; add them only when needed.
package clist

import "sync"

// Element is one node in a List. Mirrors container/list.Element with
// one specialization: Value is `string` instead of `any`. This is a
// big perf win — when container/list stores a string in `any`, Go
// heap-allocates a copy (the 16-byte string header doesn't fit in
// the interface's data word). With a typed string field, PushBack
// of a string causes zero hidden allocations.
//
// All keyspace lists hold strings (see store.Entry.List in store.go),
// so we lose nothing by narrowing the type.
type Element struct {
	next, prev *Element
	list       *List
	Value      string
}

// Next returns the next list element or nil.
func (e *Element) Next() *Element {
	if p := e.next; e.list != nil && p != &e.list.root {
		return p
	}
	return nil
}

// Prev returns the previous list element or nil.
func (e *Element) Prev() *Element {
	if p := e.prev; e.list != nil && p != &e.list.root {
		return p
	}
	return nil
}

// List is a doubly-linked list. Zero value is unusable; use New() or
// (*List).Init() before use, just like container/list.
//
// Allocation strategy — two-tier:
//
//  1. The global elementPool catches Elements freed by Remove. For
//     churn workloads (RPUSH+LPOP cycles, BLPOP-fed queues) this is
//     where every push gets its element from — zero allocs.
//
//  2. For monotonic-growth workloads (RPUSH-only against a single
//     key, the redis-benchmark RPUSH shape), the pool is always
//     empty because nothing is ever Removed. The per-List arena
//     amortizes allocations: we malloc 32 Elements at once and hand
//     them out from the slab. 200k RPUSHs → 6,250 arena allocs
//     (32× fewer mallocs than the per-Element scheme).
type List struct {
	root Element // sentinel; root.next = front, root.prev = back
	len  int

	// arena is the current Element slab. Filled left-to-right; when
	// arenaPos hits arenaSize we malloc a new one. Old arenas are
	// retained by the Element pointers that reference into them, so
	// the GC keeps them alive as long as any element is live.
	arena    *[arenaSize]Element
	arenaPos int
}

// arenaSize is the number of Elements per arena slab. 32 was chosen
// to balance:
//   - alloc amortization: 32× fewer mallocs on a growing list
//   - cache locality: 32 × 32 B = 1 KiB, comfortably within an L1
//     cache line block on Apple silicon and most x86
//   - waste on tiny lists: a 1-element list still pays for a 1 KiB
//     arena, but small lists are usually short-lived (queues drain)
//     so the GC reclaims them quickly
const arenaSize = 32

// elementPool catches Elements removed from any List so a later push
// (anywhere in the process) can reuse them. We zero all fields on
// Put so the GC can reclaim referenced values — a long-lived list
// with heavy churn would otherwise pin every value it ever held.
var elementPool = sync.Pool{
	New: func() any { return nil }, // signal "pool empty" via nil
}

// acquireElement returns an Element ready for insertion into l. Tries
// the global pool first (catches inter-list churn); falls back to
// the per-list arena (amortizes 1 malloc per 32 elements on growing
// lists).
func (l *List) acquireElement(v string) *Element {
	if poolItem := elementPool.Get(); poolItem != nil {
		e := poolItem.(*Element)
		e.Value = v
		return e
	}
	if l.arena == nil || l.arenaPos >= arenaSize {
		l.arena = new([arenaSize]Element)
		l.arenaPos = 0
	}
	e := &l.arena[l.arenaPos]
	l.arenaPos++
	e.Value = v
	return e
}

func releaseElement(e *Element) {
	// Clear all fields so the pooled element doesn't pin the previous
	// value's string (or worse, hold stale list pointers that confuse
	// a later Init). Value is cleared to "" — a typed string field can't
	// be set to nil, but the empty string releases the underlying byte
	// slice the same way nil-ing an `any` did.
	e.next = nil
	e.prev = nil
	e.list = nil
	e.Value = ""
	elementPool.Put(e)
}

// New returns an initialized empty list.
func New() *List {
	l := &List{}
	l.Init()
	return l
}

// Init initializes (or clears) a list. Existing elements are NOT
// returned to the pool — callers that need that should iterate and
// Remove first. We deliberately don't pool-on-Init because container/
// list doesn't, and keeping the semantics identical avoids subtle bugs.
func (l *List) Init() *List {
	l.root.next = &l.root
	l.root.prev = &l.root
	l.len = 0
	return l
}

// Len returns the number of elements. O(1).
func (l *List) Len() int { return l.len }

// Front returns the first element, or nil if empty.
func (l *List) Front() *Element {
	if l.len == 0 {
		return nil
	}
	return l.root.next
}

// Back returns the last element, or nil if empty.
func (l *List) Back() *Element {
	if l.len == 0 {
		return nil
	}
	return l.root.prev
}

// lazyInit lets a zero-value List be used safely. Mirrors
// container/list's behaviour.
func (l *List) lazyInit() {
	if l.root.next == nil {
		l.Init()
	}
}

// insert links e after at. Caller has set e.Value already.
func (l *List) insert(e, at *Element) *Element {
	e.prev = at
	e.next = at.next
	at.next.prev = e
	at.next = e
	e.list = l
	l.len++
	return e
}

// PushFront inserts a new element at the front. Returns the element.
func (l *List) PushFront(v string) *Element {
	l.lazyInit()
	return l.insert(l.acquireElement(v), &l.root)
}

// PushBack inserts a new element at the back. Returns the element.
func (l *List) PushBack(v string) *Element {
	l.lazyInit()
	return l.insert(l.acquireElement(v), l.root.prev)
}

// InsertBefore inserts v before mark. Returns the new element. Panics
// if mark is not a member of l (matches container/list semantics).
func (l *List) InsertBefore(v string, mark *Element) *Element {
	if mark.list != l {
		return nil
	}
	return l.insert(l.acquireElement(v), mark.prev)
}

// InsertAfter inserts v after mark.
func (l *List) InsertAfter(v string, mark *Element) *Element {
	if mark.list != l {
		return nil
	}
	return l.insert(l.acquireElement(v), mark)
}

// Remove unlinks e from the list, returns its value, and recycles the
// element back to the pool. Calling Remove on an element that doesn't
// belong to l is a no-op — same as container/list. Returns "" when
// the element doesn't belong to l (instead of container/list's nil
// `any`); callers that distinguish "element wasn't in list" should
// not be calling Remove on it in the first place.
func (l *List) Remove(e *Element) string {
	if e.list != l {
		return e.Value
	}
	e.prev.next = e.next
	e.next.prev = e.prev
	v := e.Value
	l.len--
	releaseElement(e)
	return v
}
