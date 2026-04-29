package aiops

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Moderation caches OpenAI/Anthropic moderation responses (or any
// 3rd-party safety API) so we don't pay 90% of the time for
// re-classifying the same text. Plus a built-in heuristic injection
// detector — regex-based, never as good as a model, but catches the
// obvious "ignore previous instructions" attempts at zero latency.
type Moderation struct {
	mu     sync.RWMutex
	cache  map[string]ModerationResult

	hits   uint64
	misses uint64
}

// ModerationResult is the cached verdict shape.
type ModerationResult struct {
	Safe       bool      `json:"safe"`
	Score      float64   `json:"score"`
	Categories []string  `json:"categories,omitempty"`
	StoredAt   time.Time `json:"stored_at"`
	ExpireAt   time.Time `json:"expire_at,omitempty"`
}

// NewModeration returns an empty cache.
func NewModeration() *Moderation {
	return &Moderation{cache: map[string]ModerationResult{}}
}

// canonicalize normalizes text for cache-key collisions: trim,
// lowercase, collapse whitespace runs to single spaces.
func canonicalize(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	var sb strings.Builder
	prevSpace := false
	for _, r := range t {
		if r == ' ' || r == '\t' || r == '\n' {
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		sb.WriteRune(r)
		prevSpace = false
	}
	return sb.String()
}

// hashOf returns the canonical sha256-hex of text.
func hashOf(text string) string {
	sum := sha256.Sum256([]byte(canonicalize(text)))
	return hex.EncodeToString(sum[:])
}

// Set records a moderation result. ttl=0 means no expiry.
func (m *Moderation) Set(text string, result ModerationResult, ttl time.Duration) {
	if ttl > 0 {
		result.ExpireAt = time.Now().Add(ttl)
	}
	result.StoredAt = time.Now()
	m.mu.Lock()
	m.cache[hashOf(text)] = result
	m.mu.Unlock()
}

// Check returns the cached verdict for text (canonicalized). The
// second return value is the cache-hit signal — false means callers
// should call the upstream moderation API and Set() the result.
func (m *Moderation) Check(text string) (ModerationResult, bool) {
	key := hashOf(text)
	m.mu.RLock()
	r, ok := m.cache[key]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		m.misses++
		m.mu.Unlock()
		return ModerationResult{}, false
	}
	if !r.ExpireAt.IsZero() && time.Now().After(r.ExpireAt) {
		m.mu.Lock()
		delete(m.cache, key)
		m.misses++
		m.mu.Unlock()
		return ModerationResult{}, false
	}
	m.mu.Lock()
	m.hits++
	m.mu.Unlock()
	return r, true
}

// Forget drops a single cache entry.
func (m *Moderation) Forget(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hashOf(text)
	_, ok := m.cache[key]
	delete(m.cache, key)
	return ok
}

// Purge wipes the cache.
func (m *Moderation) Purge() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.cache)
	m.cache = map[string]ModerationResult{}
	return n
}

// ModerationStats is the cache snapshot.
type ModerationStats struct {
	Entries int    `json:"entries"`
	Hits    uint64 `json:"hits"`
	Misses  uint64 `json:"misses"`
}

// Stats snapshots state.
func (m *Moderation) Stats() ModerationStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return ModerationStats{
		Entries: len(m.cache),
		Hits:    m.hits,
		Misses:  m.misses,
	}
}

// injectionPatterns are the obvious "prompt injection" attempts. Not
// exhaustive — a real ML detector wins on subtle cases — but cheap to
// run and stops 80% of the script-kiddie attempts.
var injectionPatterns = []string{
	"ignore previous instructions",
	"ignore prior instructions",
	"disregard the above",
	"forget everything",
	"new instructions:",
	"you are now",
	"act as if",
	"system:",
	"</system>",
	"<|im_start|>system",
	"jailbreak",
	"reveal your system prompt",
	"reveal your instructions",
	"developer mode",
	"dan mode",
}

// InjectionScore returns a 0-1 score for how injection-y the text
// looks. >= 0.5 is suspicious; >= 0.8 is almost certainly a probe.
// Cheap regex-free substring matching against canonicalized text.
func InjectionScore(text string) float64 {
	t := canonicalize(text)
	hits := 0
	for _, p := range injectionPatterns {
		if strings.Contains(t, p) {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	score := float64(hits) / 3.0 // 3 hits = 100%
	if score > 1 {
		score = 1
	}
	return score
}

// MatchedPatterns returns which injection patterns the text matched —
// used by SAFE.INJECT to surface the specific phrases.
func MatchedPatterns(text string) []string {
	t := canonicalize(text)
	out := []string{}
	for _, p := range injectionPatterns {
		if strings.Contains(t, p) {
			out = append(out, p)
		}
	}
	return out
}
