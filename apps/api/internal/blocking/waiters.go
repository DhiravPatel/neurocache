// Package blocking implements the per-key wait/notify primitive that
// powers BLPOP, BRPOP, BLMOVE, BZPOPMIN/MAX, and the BLOCK option of
// XREAD / XREADGROUP. Producers (any list/zset/stream write) call
// Notify(key); blocked consumers register a Waiter that wakes on the
// first matching key or its timeout, whichever comes first.
//
// The implementation is intentionally simple: a per-key fan-out of
// channels. Each waiter gets its own buffered chan struct{}; producers
// fan-out under a short critical section. Wake-ups are level-triggered
// — a woken waiter must re-poll the underlying state, since another
// consumer may have raced ahead and drained the value.
package blocking

import (
	"sync"
	"time"
)

// Hub is the broker. One per engine.
type Hub struct {
	mu    sync.Mutex
	keys  map[string]map[*Waiter]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{keys: map[string]map[*Waiter]struct{}{}} }

// Waiter is one pending consumer's handle.
type Waiter struct {
	ch     chan string // delivers the key name that woke the waiter
	keys   []string
	hub    *Hub
	closed bool
	mu     sync.Mutex
}

// Register subscribes to one or more keys and returns a Waiter the
// caller can Wait() on. Always pair with Cancel() in a defer.
func (h *Hub) Register(keys ...string) *Waiter {
	w := &Waiter{
		ch:   make(chan string, 1),
		keys: append([]string(nil), keys...),
		hub:  h,
	}
	h.mu.Lock()
	for _, k := range keys {
		set, ok := h.keys[k]
		if !ok {
			set = map[*Waiter]struct{}{}
			h.keys[k] = set
		}
		set[w] = struct{}{}
	}
	h.mu.Unlock()
	return w
}

// Cancel detaches the waiter from every key it subscribed to. Idempotent.
func (w *Waiter) Cancel() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	w.hub.mu.Lock()
	for _, k := range w.keys {
		if set, ok := w.hub.keys[k]; ok {
			delete(set, w)
			if len(set) == 0 {
				delete(w.hub.keys, k)
			}
		}
	}
	w.hub.mu.Unlock()
}

// Wait blocks until: (a) Notify wakes us — returns the key name; (b)
// timeout fires — returns ("", false). A zero timeout means "wait
// forever", matching Redis BLPOP timeout semantics.
func (w *Waiter) Wait(timeout time.Duration) (string, bool) {
	if timeout <= 0 {
		select {
		case k, ok := <-w.ch:
			if !ok {
				return "", false
			}
			return k, true
		}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case k, ok := <-w.ch:
		if !ok {
			return "", false
		}
		return k, true
	case <-t.C:
		return "", false
	}
}

// Notify wakes one waiter for the given key (FIFO is approximate — Go's
// map iteration is randomised, which matches Redis' "fairness over
// microseconds is not guaranteed" stance). Returns true if anyone was
// notified. Drops the wake when the recipient's channel is full —
// that's fine because a blocked consumer always reads exactly once.
func (h *Hub) Notify(key string) bool {
	h.mu.Lock()
	set, ok := h.keys[key]
	if !ok || len(set) == 0 {
		h.mu.Unlock()
		return false
	}
	for w := range set {
		select {
		case w.ch <- key:
			delete(set, w)
			if len(set) == 0 {
				delete(h.keys, key)
			}
			h.mu.Unlock()
			return true
		default:
			// recipient already woken by another producer — try the next.
			delete(set, w)
		}
	}
	if len(set) == 0 {
		delete(h.keys, key)
	}
	h.mu.Unlock()
	return false
}

// NotifyAll wakes every waiter for the given key. Used by FLUSH and DEL
// so blockers don't sit on a key that no longer exists.
func (h *Hub) NotifyAll(key string) {
	h.mu.Lock()
	set, ok := h.keys[key]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.keys, key)
	h.mu.Unlock()
	for w := range set {
		select {
		case w.ch <- key:
		default:
		}
	}
}

// Pending returns the count of currently registered waiters for a key
// (test/observability helper).
func (h *Hub) Pending(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.keys[key])
}
