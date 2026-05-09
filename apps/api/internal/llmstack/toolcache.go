package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ToolCache memoizes the result of a tool/function call by
// (tool_name, normalized-args-hash). Built for AI agents: when an
// agent runs `get_weather("NYC")` repeatedly within a window, every
// hit after the first returns instantly from cache instead of
// round-tripping to the underlying tool (HTTP API, code interpreter,
// SQL query, etc.).
//
// Why this exists: every agent framework today (LangChain, LlamaIndex,
// CrewAI, custom stacks) reinvents this caching layer in application
// code. Doing it in the cache means:
//
//   - One cache shared across every agent process — no cold-start
//     penalty per worker
//   - Per-tool TTL + cost accounting, so you can answer "how much
//     money did the cache save us this month?"
//   - Survives process restarts when persistence is on
//   - Lock-free reads: GET hits a sync.Map, not a mutex
//
// Argument normalization: callers can pass raw JSON or a sorted
// canonical form. ToolCache canonicalizes by sorting top-level keys
// of an object body before hashing, so {"a":1,"b":2} and {"b":2,"a":1}
// hash identically. Nested values are NOT recursively sorted — apps
// that pass nested objects should pre-canonicalize.
//
// Hot path:
//   GET: sync.Map.Load → if miss, atomic.Int64.Add(missCounter); else
//        check expiry, atomic.Int64.Add(hitCounter), return value.
//   PUT: hash + sync.Map.Store + atomic counter bumps.
//
// No cgo, no per-call alloc beyond what hashing requires.
type ToolCache struct {
	// entries: key = hash(toolName, normalizedArgs), value = *toolEntry
	entries sync.Map

	hits      atomic.Int64
	misses    atomic.Int64
	stores    atomic.Int64
	costSaved atomic.Int64 // micro-USD; 1_000_000 = $1
	purges    atomic.Int64
}

// toolEntry holds one cached tool result.
type toolEntry struct {
	tool     string    // for inspection / TOOL.LIST
	value    string    // result payload (raw — caller decides JSON vs text)
	expireAt time.Time // zero = no expiry
	createdAt time.Time
	costMicroUSD int64 // estimated $ to recompute (0 = unknown)
}

// NewToolCache returns an empty cache.
func NewToolCache() *ToolCache { return &ToolCache{} }

// toolHashKey canonicalizes (tool, args) into a stable key. Args is
// expected to be either:
//   - a JSON object body (we sort top-level keys)
//   - any other string (treated opaquely, just hashed)
//
// The 256-bit sha256 keeps collision risk astronomical for any
// realistic agent traffic; we hex-encode only because RESP keys are
// strings. Distinct from the embcache `hashKey` so the two caches
// can coexist in this package.
func toolHashKey(tool, args string) string {
	canon := canonicalizeArgs(args)
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0}) // separator so tool="ab"+args="cd" != "abcd"
	h.Write([]byte(canon))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalizeArgs sorts top-level JSON object keys so semantically
// identical args produce the same hash. Returns the input unchanged
// if it doesn't look like a JSON object — strings, arrays, numbers
// are already deterministic.
//
// Deliberately shallow: we don't re-encode nested objects because
// the parsing cost would dominate for typical agent args. Apps with
// deep object args should pre-canonicalize on the client side.
func canonicalizeArgs(s string) string {
	t := strings.TrimSpace(s)
	if len(t) < 2 || t[0] != '{' || t[len(t)-1] != '}' {
		return s
	}
	// Cheap top-level key extraction without a full JSON parser:
	// split on top-level commas, sort by leading "key" string.
	body := t[1 : len(t)-1]
	parts := splitTopLevel(body, ',')
	if len(parts) <= 1 {
		return s
	}
	sort.Strings(parts)
	var b strings.Builder
	b.Grow(len(s))
	b.WriteByte('{')
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p)
	}
	b.WriteByte('}')
	return b.String()
}

// splitTopLevel splits s on `sep` outside of nested {} / [] / "".
func splitTopLevel(s string, sep byte) []string {
	depth := 0
	inStr := false
	esc := false
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// nothing
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// Set stores result under (tool, args). ttl=0 means "no expiry".
// costMicroUSD lets callers track how much each cached call would
// have cost — surfaced via TOOL.STATS as "saved_usd".
func (c *ToolCache) Set(tool, args, value string, ttl time.Duration, costMicroUSD int64) {
	now := time.Now()
	e := &toolEntry{
		tool:         tool,
		value:        value,
		createdAt:    now,
		costMicroUSD: costMicroUSD,
	}
	if ttl > 0 {
		e.expireAt = now.Add(ttl)
	}
	c.entries.Store(toolHashKey(tool, args), e)
	c.stores.Add(1)
}

// Get returns the cached value, true if found and unexpired. Bumps
// hit / miss counters atomically. Lock-free.
func (c *ToolCache) Get(tool, args string) (string, bool) {
	v, ok := c.entries.Load(toolHashKey(tool, args))
	if !ok {
		c.misses.Add(1)
		return "", false
	}
	e := v.(*toolEntry)
	if !e.expireAt.IsZero() && time.Now().After(e.expireAt) {
		c.entries.Delete(toolHashKey(tool, args))
		c.misses.Add(1)
		return "", false
	}
	c.hits.Add(1)
	if e.costMicroUSD > 0 {
		c.costSaved.Add(e.costMicroUSD)
	}
	return e.value, true
}

// Forget deletes a single (tool, args) entry. Returns true if it was
// present.
func (c *ToolCache) Forget(tool, args string) bool {
	_, was := c.entries.LoadAndDelete(toolHashKey(tool, args))
	if was {
		c.purges.Add(1)
	}
	return was
}

// Purge removes every entry for a given tool. Returns count removed.
// Used by ops paths ("clear cache for get_weather; the API changed").
func (c *ToolCache) Purge(tool string) int {
	n := 0
	c.entries.Range(func(k, v any) bool {
		if v.(*toolEntry).tool == tool {
			c.entries.Delete(k)
			n++
		}
		return true
	})
	c.purges.Add(int64(n))
	return n
}

// PurgeAll wipes the entire cache. Returns count removed.
func (c *ToolCache) PurgeAll() int {
	n := 0
	c.entries.Range(func(k, v any) bool {
		c.entries.Delete(k)
		n++
		return true
	})
	c.purges.Add(int64(n))
	return n
}

// ToolStats is a snapshot for the TOOL.STATS command + dashboard.
type ToolStats struct {
	Hits          int64   `json:"hits"`
	Misses        int64   `json:"misses"`
	Stores        int64   `json:"stores"`
	Purges        int64   `json:"purges"`
	HitRate       float64 `json:"hit_rate"`
	SavedUSD      float64 `json:"saved_usd"`
	UniqueEntries int     `json:"unique_entries"`
}

// Stats returns a snapshot. O(N) over entries because sync.Map has
// no cheap Len() — fine for an observability path.
func (c *ToolCache) Stats() ToolStats {
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	n := 0
	c.entries.Range(func(_, _ any) bool { n++; return true })
	return ToolStats{
		Hits: hits, Misses: misses,
		Stores: c.stores.Load(), Purges: c.purges.Load(),
		HitRate:       rate,
		SavedUSD:      float64(c.costSaved.Load()) / 1_000_000.0,
		UniqueEntries: n,
	}
}

// ToolListEntry is one row in the TOOL.LIST output. Reveals the
// tool name + key hash + age + remaining TTL — the result body is
// NOT included to keep the command cheap.
type ToolListEntry struct {
	Tool      string `json:"tool"`
	KeyHash   string `json:"key_hash"`
	AgeMs     int64  `json:"age_ms"`
	TTLMs     int64  `json:"ttl_ms"` // -1 = no expiry
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// List returns a snapshot of every cached entry. Filtered to a
// specific tool name when filter != "". Capped at limit; pass <=0
// for "no limit".
func (c *ToolCache) List(filter string, limit int) []ToolListEntry {
	now := time.Now()
	out := []ToolListEntry{}
	c.entries.Range(func(k, v any) bool {
		e := v.(*toolEntry)
		if filter != "" && e.tool != filter {
			return true
		}
		row := ToolListEntry{
			Tool:    e.tool,
			KeyHash: k.(string),
			AgeMs:   now.Sub(e.createdAt).Milliseconds(),
			TTLMs:   -1,
			CostMicroUSD: e.costMicroUSD,
		}
		if !e.expireAt.IsZero() {
			row.TTLMs = e.expireAt.Sub(now).Milliseconds()
		}
		out = append(out, row)
		if limit > 0 && len(out) >= limit {
			return false
		}
		return true
	})
	return out
}
