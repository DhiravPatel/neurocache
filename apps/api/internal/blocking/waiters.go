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
	mu      sync.Mutex
	keys    map[string]map[*Waiter]struct{}
	clients map[uint64]map[*Waiter]struct{} // reverse index for CLIENT UNBLOCK
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{
		keys:    map[string]map[*Waiter]struct{}{},
		clients: map[uint64]map[*Waiter]struct{}{},
	}
}

// UnblockReason classifies why a waiter is being woken from outside.
// Used by CLIENT UNBLOCK and related operator paths.
type UnblockReason uint8

const (
	// UnblockTimeout wakes the waiter as if its timeout fired — the
	// blocking command will return the standard nil reply.
	UnblockTimeout UnblockReason = iota
	// UnblockError wakes the waiter and signals the dispatcher to emit
	// the canonical "UNBLOCKED client unblocked via CLIENT UNBLOCK" error.
	UnblockError
)

// Waiter is one pending consumer's handle.
type Waiter struct {
	ch       chan string // delivers the key name that woke the waiter
	keys     []string
	hub      *Hub
	closed   bool
	mu       sync.Mutex
	clientID uint64
	// unblockedExternal is set when CLIENT UNBLOCK woke us (vs. a normal
	// data Notify). Blocking commands check this after Wait returns to
	// know whether they should exit immediately rather than re-poll.
	unblockedExternal bool
	// unblockedErr is true when CLIENT UNBLOCK ... ERROR woke us — the
	// blocking command emits the canonical -UNBLOCKED error reply.
	unblockedErr bool
}

// Register subscribes to one or more keys and returns a Waiter the
// caller can Wait() on. Always pair with Cancel() in a defer.
func (h *Hub) Register(keys ...string) *Waiter {
	return h.RegisterFor(0, keys...)
}

// RegisterFor is Register with a non-zero client ID so the waiter can
// be targeted by CLIENT UNBLOCK. Pass 0 when the waiter isn't tied to
// a tracked client (background consumers, replication waits, …).
func (h *Hub) RegisterFor(clientID uint64, keys ...string) *Waiter {
	w := &Waiter{
		ch:       make(chan string, 1),
		keys:     append([]string(nil), keys...),
		hub:      h,
		clientID: clientID,
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
	if clientID != 0 {
		set, ok := h.clients[clientID]
		if !ok {
			set = map[*Waiter]struct{}{}
			h.clients[clientID] = set
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
	if w.clientID != 0 {
		if set, ok := w.hub.clients[w.clientID]; ok {
			delete(set, w)
			if len(set) == 0 {
				delete(w.hub.clients, w.clientID)
			}
		}
	}
	w.hub.mu.Unlock()
}

// UnblockedByError reports whether the waiter was woken by
// CLIENT UNBLOCK ... ERROR. Only meaningful after Wait returns.
func (w *Waiter) UnblockedByError() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.unblockedErr
}

// UnblockedExternal reports whether CLIENT UNBLOCK (with either
// reason) woke the waiter — as opposed to a normal data Notify. The
// blocking command should not re-poll the keyspace when this is true;
// it should exit and emit the appropriate reply.
func (w *Waiter) UnblockedExternal() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.unblockedExternal
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

// Unblock wakes every waiter belonging to clientID, simulating either a
// timeout (UnblockTimeout) or an error reply (UnblockError). Returns
// the count of waiters that were notified — 0 means the client wasn't
// blocked. Mirrors Redis CLIENT UNBLOCK <id> [TIMEOUT|ERROR] semantics.
//
// Notes:
//   - We deliberately do not Cancel() the waiter here; the consumer's
//     Wait() will return, observe the wake, then call Cancel() in its
//     defer. That keeps map bookkeeping inside one goroutine and
//     avoids racing the Wait/Cancel pair.
//   - Sending to a full channel is treated as already-woken — that
//     waiter has been notified by another path (Notify) and will
//     observe the unblock on its next loop iteration.
func (h *Hub) Unblock(clientID uint64, reason UnblockReason) int {
	h.mu.Lock()
	set, ok := h.clients[clientID]
	if !ok || len(set) == 0 {
		h.mu.Unlock()
		return 0
	}
	// Snapshot the waiters so we can release the hub lock before
	// touching their per-waiter mutex (avoids lock-order inversions).
	victims := make([]*Waiter, 0, len(set))
	for w := range set {
		victims = append(victims, w)
	}
	h.mu.Unlock()
	woken := 0
	for _, w := range victims {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			continue
		}
		w.unblockedExternal = true
		if reason == UnblockError {
			w.unblockedErr = true
		}
		w.mu.Unlock()
		select {
		case w.ch <- "":
			woken++
		default:
			// already woken via Notify — still counts as unblocked
			woken++
		}
	}
	return woken
}

// PendingClient reports how many keys clientID is currently blocking
// on (test/observability helper).
func (h *Hub) PendingClient(clientID uint64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients[clientID])
}
