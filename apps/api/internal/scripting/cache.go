// Package scripting implements EVAL / EVALSHA / SCRIPT LOAD/EXISTS/FLUSH
// using a small, self-contained Lua-subset interpreter. It targets the
// patterns real-world Redis scripts use — multi-step atomic mutations
// over redis.call() — without pulling in a heavyweight Lua runtime.
//
// Supported subset:
//   - local declarations, assignments
//   - numbers, strings, booleans, nil
//   - tables (KEYS / ARGV are pre-populated with 1-based indexing)
//   - if / elseif / else / end
//   - while / for-numeric / for-in (limited)
//   - return (single value or table)
//   - redis.call / redis.pcall / redis.error_reply / redis.status_reply
//   - tonumber, tostring, type, table.insert, string.format, #len op
//   - common arithmetic + string concat (..) + comparisons + and/or/not
//
// Anything outside the subset returns a clean error so callers know to
// rewrite or wait for the full interpreter.
package scripting

import (
	"crypto/sha1"
	"encoding/hex"
	"sync"
)

// Cache holds SCRIPT LOAD'd source by sha1. EVALSHA looks up here.
type Cache struct {
	mu      sync.RWMutex
	scripts map[string]string // sha1 -> source
}

// NewCache returns an empty script cache.
func NewCache() *Cache { return &Cache{scripts: map[string]string{}} }

// Load stores src under its sha1 hash and returns the hash.
func (c *Cache) Load(src string) string {
	sum := sha1.Sum([]byte(src))
	hash := hex.EncodeToString(sum[:])
	c.mu.Lock()
	c.scripts[hash] = src
	c.mu.Unlock()
	return hash
}

// Get returns the source for a hash, or "" + false if unknown.
func (c *Cache) Get(hash string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	src, ok := c.scripts[hash]
	return src, ok
}

// Exists reports which of the given hashes are loaded.
func (c *Cache) Exists(hashes ...string) []bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]bool, len(hashes))
	for i, h := range hashes {
		_, out[i] = c.scripts[h]
	}
	return out
}

// Flush drops every cached script.
func (c *Cache) Flush() {
	c.mu.Lock()
	c.scripts = map[string]string{}
	c.mu.Unlock()
}

// Len reports how many scripts are loaded (used by metrics).
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.scripts)
}
