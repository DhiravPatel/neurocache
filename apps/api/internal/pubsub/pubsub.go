// Package pubsub implements the SUBSCRIBE / PUBLISH / PSUBSCRIBE family.
// Subscribers register a per-connection channel and receive Message values;
// publishers fan out asynchronously without blocking on slow consumers.
package pubsub

import (
	"strings"
	"sync"
	"sync/atomic"
)

// Message is what PUBLISH delivers to each subscriber. Pattern is empty for
// exact-channel subscribers; for PSUBSCRIBE callers it echoes the matched
// pattern so clients can format "pmessage" replies correctly.
type Message struct {
	Channel string
	Pattern string
	Payload string
}

// Subscription is a single subscriber's handle. Close to detach.
type Subscription struct {
	ID      uint64
	ch      chan Message
	closeFn func()
}

func (s *Subscription) Ch() <-chan Message { return s.ch }
func (s *Subscription) Close() {
	if s.closeFn != nil {
		s.closeFn()
	}
}

// Broker is a concurrent pub/sub registry.
type Broker struct {
	mu            sync.RWMutex
	nextID        uint64
	subscribers   map[string]map[uint64]*Subscription // channel -> subs
	psubscribers  map[string]map[uint64]*Subscription // pattern -> subs
	buffer        int

	// subCount is an atomic mirror of total subscriptions (exact +
	// pattern). The keyspace-notification path on every store write
	// reads this WITHOUT taking b.mu — when nobody's subscribed (the
	// steady state for any cache without a CONFIG SET notify-keyspace-
	// events 'AKE'), the entire publish path short-circuits before
	// any string concat or map lookup. Maintained under b.mu when
	// adding/removing subs; read lock-free everywhere else.
	subCount atomic.Int64
}

// New creates a broker with the given per-subscriber buffer size. A
// buffer of 0 means publishers block when a subscriber is slow; anything
// positive drops old messages for that one subscriber once full.
func New(buffer int) *Broker {
	if buffer <= 0 {
		buffer = 64
	}
	return &Broker{
		subscribers:  map[string]map[uint64]*Subscription{},
		psubscribers: map[string]map[uint64]*Subscription{},
		buffer:       buffer,
	}
}

// Subscribe attaches to exact channels. Returns one Subscription that
// receives messages from any of the given channels. Closing it detaches
// from every channel registered here.
func (b *Broker) Subscribe(channels ...string) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	sub := &Subscription{
		ID: b.nextID,
		ch: make(chan Message, b.buffer),
	}
	sub.closeFn = func() { b.unsubscribe(sub, channels, false) }
	added := 0
	for _, ch := range channels {
		if _, ok := b.subscribers[ch]; !ok {
			b.subscribers[ch] = map[uint64]*Subscription{}
		}
		if _, dup := b.subscribers[ch][sub.ID]; !dup {
			added++
		}
		b.subscribers[ch][sub.ID] = sub
	}
	b.subCount.Add(int64(added))
	return sub
}

// PSubscribe attaches by glob pattern.
func (b *Broker) PSubscribe(patterns ...string) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	sub := &Subscription{
		ID: b.nextID,
		ch: make(chan Message, b.buffer),
	}
	sub.closeFn = func() { b.unsubscribe(sub, patterns, true) }
	added := 0
	for _, p := range patterns {
		if _, ok := b.psubscribers[p]; !ok {
			b.psubscribers[p] = map[uint64]*Subscription{}
		}
		if _, dup := b.psubscribers[p][sub.ID]; !dup {
			added++
		}
		b.psubscribers[p][sub.ID] = sub
	}
	b.subCount.Add(int64(added))
	return sub
}

func (b *Broker) unsubscribe(sub *Subscription, names []string, pattern bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.subscribers
	if pattern {
		m = b.psubscribers
	}
	removed := 0
	for _, n := range names {
		if set, ok := m[n]; ok {
			if _, was := set[sub.ID]; was {
				removed++
			}
			delete(set, sub.ID)
			if len(set) == 0 {
				delete(m, n)
			}
		}
	}
	if removed > 0 {
		b.subCount.Add(int64(-removed))
	}
	// closing the channel is idempotent-safe behind this mutex
	select {
	case <-sub.ch:
	default:
	}
	close(sub.ch)
}

// HasSubscribers returns true if at least one (P)SUBSCRIBE is active.
// Hot-path safe — single atomic load, no mutex. Used by the keyspace-
// notification path on every store write to skip the entire publish
// pipeline (string concat + map lookup + RLock) when nobody's listening,
// which is the steady state for any cache without notify-keyspace-events
// configured.
func (b *Broker) HasSubscribers() bool { return b.subCount.Load() > 0 }

// Publish delivers a message to all matching subscribers and returns the
// number of receivers reached (both exact and pattern).
func (b *Broker) Publish(channel, payload string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	if subs, ok := b.subscribers[channel]; ok {
		for _, sub := range subs {
			if trySend(sub, Message{Channel: channel, Payload: payload}) {
				n++
			}
		}
	}
	for pat, subs := range b.psubscribers {
		if !globMatch(pat, channel) {
			continue
		}
		for _, sub := range subs {
			if trySend(sub, Message{Channel: channel, Pattern: pat, Payload: payload}) {
				n++
			}
		}
	}
	return n
}

// NumSub returns subscriber counts for each channel (PUBSUB NUMSUB).
func (b *Broker) NumSub(channels ...string) map[string]int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := map[string]int{}
	for _, ch := range channels {
		out[ch] = len(b.subscribers[ch])
	}
	return out
}

// Channels lists all active subscriber channels (PUBSUB CHANNELS).
func (b *Broker) Channels(pattern string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := []string{}
	for ch := range b.subscribers {
		if pattern == "" || pattern == "*" || globMatch(pattern, ch) {
			out = append(out, ch)
		}
	}
	return out
}

// NumPat returns the number of active patterns (PUBSUB NUMPAT).
func (b *Broker) NumPat() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.psubscribers)
}

// trySend sends without blocking; drops the message when the buffer is
// saturated so one slow subscriber cannot stall every publisher.
func trySend(sub *Subscription, m Message) bool {
	select {
	case sub.ch <- m:
		return true
	default:
		return false
	}
}

// globMatch is the same tiny matcher used by the store. Duplicated here
// to avoid an import cycle; the surface area is small enough.
func globMatch(pattern, s string) bool {
	return matchRunes([]rune(pattern), []rune(s))
}

func matchRunes(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchRunes(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

// _ silences potential unused-import warnings for strings if callers
// start using strings-based helpers here later.
var _ = strings.ToLower
