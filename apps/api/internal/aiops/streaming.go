package aiops

import (
	"sync"
	"time"
)

// StreamCache caches LLM token streams keyed by prompt hash. On a
// cache hit the engine can replay the tokens back to a client at the
// original cadence (or burst-fast), giving identical UX without
// hitting the upstream LLM.
//
// We store tokens as a slice of (text, delayMs) pairs so replay can
// honor the original timing — important when the chatbot UX leans on
// the streaming feel.
type StreamCache struct {
	mu      sync.RWMutex
	streams map[string]*streamEntry

	hits   uint64
	misses uint64
}

// StreamToken is one chunk of a streamed response.
type StreamToken struct {
	Text    string `json:"text"`
	DelayMs int64  `json:"delay_ms"`
}

type streamEntry struct {
	tokens   []StreamToken
	full     string // concatenation cache for cheap CACHE_LLM_GET-style lookups
	storedAt time.Time
	expireAt time.Time
	hits     int64
}

// NewStreamCache returns an empty cache.
func NewStreamCache() *StreamCache {
	return &StreamCache{streams: map[string]*streamEntry{}}
}

// Set records a complete token stream under the prompt's canonical
// hash. ttl=0 means no expiry. Concurrent Set calls on the same key
// last-writer-wins, matching the rest of the cache surface.
func (c *StreamCache) Set(promptHash string, tokens []StreamToken, ttl time.Duration) {
	full := concatTokens(tokens)
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	cp := make([]StreamToken, len(tokens))
	copy(cp, tokens)
	c.mu.Lock()
	c.streams[promptHash] = &streamEntry{
		tokens:   cp,
		full:     full,
		storedAt: time.Now(),
		expireAt: exp,
	}
	c.mu.Unlock()
}

// Get returns the full concatenated response (for non-streaming clients).
func (c *StreamCache) Get(promptHash string) (string, bool) {
	c.mu.RLock()
	e, ok := c.streams[promptHash]
	c.mu.RUnlock()
	if !ok || (e != nil && !e.expireAt.IsZero() && time.Now().After(e.expireAt)) {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return "", false
	}
	c.mu.Lock()
	c.hits++
	e.hits++
	c.mu.Unlock()
	return e.full, true
}

// Replay returns a copy of the token stream so a caller can pace it
// out to a client at the original cadence (or accelerate by ignoring
// DelayMs). The returned slice is independent — caller is free to
// mutate.
func (c *StreamCache) Replay(promptHash string) ([]StreamToken, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.streams[promptHash]
	if !ok || (!e.expireAt.IsZero() && time.Now().After(e.expireAt)) {
		return nil, false
	}
	out := make([]StreamToken, len(e.tokens))
	copy(out, e.tokens)
	return out, true
}

// Forget drops a stream.
func (c *StreamCache) Forget(promptHash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.streams[promptHash]
	delete(c.streams, promptHash)
	return ok
}

// Purge wipes the cache.
func (c *StreamCache) Purge() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.streams)
	c.streams = map[string]*streamEntry{}
	return n
}

// StreamStats snapshots the cache state.
type StreamStats struct {
	Streams int    `json:"streams"`
	Hits    uint64 `json:"hits"`
	Misses  uint64 `json:"misses"`
}

// Stats returns a snapshot.
func (c *StreamCache) Stats() StreamStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return StreamStats{
		Streams: len(c.streams),
		Hits:    c.hits,
		Misses:  c.misses,
	}
}

func concatTokens(tokens []StreamToken) string {
	n := 0
	for _, t := range tokens {
		n += len(t.Text)
	}
	buf := make([]byte, 0, n)
	for _, t := range tokens {
		buf = append(buf, t.Text...)
	}
	return string(buf)
}
