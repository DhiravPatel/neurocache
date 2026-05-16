package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MemoryConflicts detects contradictions in long-lived agent memory.
//
// MEMORY.CONSOLIDATE deduplicates similar facts. It does nothing when
// a new fact *contradicts* an old one ("user prefers async
// communication" → later "user wants daily sync calls"). Long-running
// agent memory rots silently without contradiction detection, and no
// memory product handles it. MEMORY.CONFLICT.* is that layer:
//
//   CHECK    given a candidate fact for some key, score against every
//            stored fact for that key. A contradiction is "same
//            topic, opposite assertion" — semantically close
//            (cosine 0.4-0.85) AND showing a polarity flip
//            (negation, opposite-domain words).
//   ADD      register a known fact. The store learns *what's true now*
//            so future CHECKs catch divergence.
//   LIST     all open contradictions for a key.
//   RESOLVE  accept newer (default), older, or drop both. Keeps the
//            kept fact in the store and removes the conflict entry.
//
// Commands:
//
//   MEMORY.CONFLICT.ADD     key text [ID id]
//   MEMORY.CONFLICT.CHECK   key candidate-text [STRICT 0|1]
//        → {conflict, with, with_id, score, resolution_hint, ...}
//   MEMORY.CONFLICT.LIST    key
//   MEMORY.CONFLICT.RESOLVE key conflict-id KEEP newer|older|both
//   MEMORY.CONFLICT.PURGE   key
//   MEMORY.CONFLICT.KEYS
//   MEMORY.CONFLICT.STATS
//
// Hot path: CHECK is O(facts_for_key × cosine + small regex pass).
// ADD persists the fact + indexes its negation tokens. RESOLVE
// mutates the per-key fact list.
type MemoryConflicts struct {
	mu   sync.RWMutex
	keys map[string]*memConflictKey

	totalAdds       atomic.Int64
	totalChecks     atomic.Int64
	totalConflicts  atomic.Int64
	totalResolves   atomic.Int64
}

type memConflictKey struct {
	mu        sync.RWMutex
	facts     []memFact
	conflicts []memConflict
	seq       int
}

type memFact struct {
	ID        string
	Text      string
	Vec       []float64
	HasNeg    bool   // contains negation tokens
	Polarity  string // canonical polarity tag for known opposites; "" if none
	TS        int64
}

type memConflict struct {
	ID        string
	NewerID   string
	OlderID   string
	Score     float64
	Hint      string // supersede | replace | review
	TS        int64
}

// NewMemoryConflicts returns an empty contradiction store.
func NewMemoryConflicts() *MemoryConflicts {
	return &MemoryConflicts{keys: map[string]*memConflictKey{}}
}

// Add registers a known fact for a key. If id == "" an id is
// synthesised. Returns the fact id.
func (m *MemoryConflicts) Add(key, text, id string) (string, error) {
	if key == "" {
		return "", errors.New("key required")
	}
	if text == "" {
		return "", errors.New("text required")
	}
	m.totalAdds.Add(1)
	k := m.keyOrCreate(key)
	k.mu.Lock()
	defer k.mu.Unlock()
	if id == "" {
		k.seq++
		id = "f" + itoaBenchPub(k.seq)
	}
	k.facts = append(k.facts, memFact{
		ID: id, Text: text, Vec: embedFallback(text),
		HasNeg: containsNegation(text), Polarity: polarityOf(text),
		TS: time.Now().UnixNano(),
	})
	return id, nil
}

// MemConflictCheckResult is CHECK's return.
type MemConflictCheckResult struct {
	Conflict       bool    `json:"conflict"`
	With           string  `json:"with,omitempty"`     // text of the conflicting fact
	WithID         string  `json:"with_id,omitempty"`
	Score          float64 `json:"score"`              // 0..1 contradiction score
	ResolutionHint string  `json:"resolution_hint,omitempty"` // supersede|replace|review
	Reason         string  `json:"reason,omitempty"`
}

// Check scores a candidate fact against every stored fact for the key.
// strict=true raises the contradiction bar (requires both polarity
// flip AND negation differential).
func (m *MemoryConflicts) Check(key, candidate string, strict bool) (MemConflictCheckResult, error) {
	if key == "" {
		return MemConflictCheckResult{}, errors.New("key required")
	}
	if candidate == "" {
		return MemConflictCheckResult{}, errors.New("text required")
	}
	m.totalChecks.Add(1)
	m.mu.RLock()
	k, ok := m.keys[key]
	m.mu.RUnlock()
	if !ok {
		return MemConflictCheckResult{}, nil
	}
	candVec := embedFallback(candidate)
	candNeg := containsNegation(candidate)
	candPol := polarityOf(candidate)

	k.mu.RLock()
	defer k.mu.RUnlock()
	var best memFact
	var bestScore float64
	var bestReason string
	var bestPolFlip, bestNegDiff bool
	for _, f := range k.facts {
		cos := dotProduct(candVec, f.Vec)
		// Identical fact — never a contradiction
		if cos >= 0.95 {
			continue
		}
		polFlip := candPol != "" && f.Polarity != "" && candPol != f.Polarity
		negDiff := candNeg != f.HasNeg

		// Need *some* signal: either a canonical polarity flip, a
		// negation differential on topically-overlapping text, or
		// strong same-topic similarity without identity.
		if !polFlip && !negDiff && cos < 0.55 {
			continue
		}

		// Composite score: polarity flip is the strongest signal,
		// negation differential next, topical similarity supporting.
		score := cos * 0.20
		reason := ""
		if polFlip {
			score += 0.60
			reason = "polarity flip (" + f.Polarity + " ↔ " + candPol + ")"
		}
		if negDiff {
			// Negation only counts when there's at least some topical overlap
			if cos >= 0.30 {
				score += 0.45
				if reason == "" {
					reason = "negation differential"
				} else {
					reason += "; negation differential"
				}
			}
		}
		if score > 1 {
			score = 1
		}
		if score > bestScore {
			bestScore = score
			best = f
			bestReason = reason
			bestPolFlip = polFlip
			bestNegDiff = negDiff
		}
	}
	out := MemConflictCheckResult{Score: bestScore}
	// Lenient: 0.55. Strict: 0.75 AND must have polarity-flip or negation signal.
	threshold := 0.55
	if strict {
		threshold = 0.75
		if !bestPolFlip && !bestNegDiff {
			return out, nil
		}
	}
	if bestScore < threshold || best.ID == "" {
		return out, nil
	}
	m.totalConflicts.Add(1)
	out.Conflict = true
	out.With = best.Text
	out.WithID = best.ID
	out.Reason = bestReason
	if bestReason == "" {
		out.ResolutionHint = "review"
	} else {
		out.ResolutionHint = "supersede"
	}
	return out, nil
}

// MemConflictRow is one row of LIST output.
type MemConflictRow struct {
	ConflictID string  `json:"conflict_id"`
	NewerID    string  `json:"newer_id"`
	OlderID    string  `json:"older_id"`
	NewerText  string  `json:"newer_text"`
	OlderText  string  `json:"older_text"`
	Score      float64 `json:"score"`
	Hint       string  `json:"hint"`
	TS         int64   `json:"ts"`
}

// AddConflict registers an unresolved conflict for a key. Used after
// CHECK reports conflict=true so the orchestrator can revisit it.
func (m *MemoryConflicts) AddConflict(key, newerID, olderID string, score float64, hint string) (string, error) {
	if key == "" {
		return "", errors.New("key required")
	}
	m.mu.RLock()
	k, ok := m.keys[key]
	m.mu.RUnlock()
	if !ok {
		return "", errors.New("unknown key: " + key)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seq++
	id := "c" + itoaBenchPub(k.seq)
	k.conflicts = append(k.conflicts, memConflict{
		ID: id, NewerID: newerID, OlderID: olderID,
		Score: score, Hint: hint, TS: time.Now().UnixNano(),
	})
	return id, nil
}

// List returns open conflicts for a key (newest first).
func (m *MemoryConflicts) List(key string) ([]MemConflictRow, bool) {
	m.mu.RLock()
	k, ok := m.keys[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]MemConflictRow, 0, len(k.conflicts))
	for _, c := range k.conflicts {
		row := MemConflictRow{
			ConflictID: c.ID, NewerID: c.NewerID, OlderID: c.OlderID,
			Score: c.Score, Hint: c.Hint, TS: c.TS / int64(time.Second),
		}
		for _, f := range k.facts {
			if f.ID == c.NewerID {
				row.NewerText = f.Text
			}
			if f.ID == c.OlderID {
				row.OlderText = f.Text
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	return out, true
}

// Resolve closes one conflict. keep = "newer" | "older" | "both".
// Drops the non-kept fact(s) and removes the conflict entry.
func (m *MemoryConflicts) Resolve(key, conflictID, keep string) error {
	if key == "" || conflictID == "" {
		return errors.New("key and conflict_id required")
	}
	switch keep {
	case "newer", "older", "both":
	default:
		return errors.New("keep must be newer | older | both")
	}
	m.totalResolves.Add(1)
	m.mu.RLock()
	k, ok := m.keys[key]
	m.mu.RUnlock()
	if !ok {
		return errors.New("unknown key: " + key)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var target memConflict
	idx := -1
	for i, c := range k.conflicts {
		if c.ID == conflictID {
			target = c
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("unknown conflict_id: " + conflictID)
	}
	// Drop conflict entry
	k.conflicts = append(k.conflicts[:idx], k.conflicts[idx+1:]...)
	// Drop fact(s) per resolution
	dropIDs := map[string]bool{}
	switch keep {
	case "newer":
		dropIDs[target.OlderID] = true
	case "older":
		dropIDs[target.NewerID] = true
	}
	if len(dropIDs) > 0 {
		filtered := k.facts[:0]
		for _, f := range k.facts {
			if dropIDs[f.ID] {
				continue
			}
			filtered = append(filtered, f)
		}
		k.facts = filtered
	}
	return nil
}

// Purge drops all facts and conflicts for a key.
func (m *MemoryConflicts) Purge(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.keys[key]
	delete(m.keys, key)
	return ok
}

// Keys returns every key, sorted.
func (m *MemoryConflicts) Keys() []string {
	m.mu.RLock()
	out := make([]string, 0, len(m.keys))
	for k := range m.keys {
		out = append(out, k)
	}
	m.mu.RUnlock()
	sort.Strings(out)
	return out
}

// MemConflictStats is the global snapshot.
type MemConflictStats struct {
	Keys           int   `json:"keys"`
	TotalFacts     int   `json:"total_facts"`
	OpenConflicts  int   `json:"open_conflicts"`
	TotalAdds      int64 `json:"total_adds"`
	TotalChecks    int64 `json:"total_checks"`
	TotalConflicts int64 `json:"total_conflicts_detected"`
	TotalResolves  int64 `json:"total_resolves"`
}

func (m *MemoryConflicts) Stats() MemConflictStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	facts, open := 0, 0
	for _, k := range m.keys {
		k.mu.RLock()
		facts += len(k.facts)
		open += len(k.conflicts)
		k.mu.RUnlock()
	}
	return MemConflictStats{
		Keys:           len(m.keys),
		TotalFacts:     facts,
		OpenConflicts:  open,
		TotalAdds:      m.totalAdds.Load(),
		TotalChecks:    m.totalChecks.Load(),
		TotalConflicts: m.totalConflicts.Load(),
		TotalResolves:  m.totalResolves.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (m *MemoryConflicts) keyOrCreate(key string) *memConflictKey {
	m.mu.RLock()
	k, ok := m.keys[key]
	m.mu.RUnlock()
	if ok {
		return k
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[key]; ok {
		return k
	}
	k = &memConflictKey{}
	m.keys[key] = k
	return k
}

// containsNegation returns true if the text has explicit negation
// tokens that flip a fact's polarity.
func containsNegation(s string) bool {
	lower := " " + strings.ToLower(s) + " "
	for _, w := range []string{" not ", " no ", " never ", " won't ", " can't ",
		" doesn't ", " don't ", " isn't ", " aren't ", " wasn't ", " shouldn't ",
		" cannot ", "n't "} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// polarityPairs are domain-canonical opposite-pair tags. Each map
// entry value is the polarity tag; both members of a pair share the
// same key so polarityOf reports "diet:vegetarian" vs "diet:meat".
var polarityPairs = map[string]string{
	// communication preference
	"async":         "comms:async",
	"asynchronous":  "comms:async",
	"sync":          "comms:sync",
	"synchronous":   "comms:sync",
	"daily standup": "comms:sync",

	// diet
	"vegetarian": "diet:veg",
	"vegan":      "diet:veg",
	"steak":      "diet:meat",
	"meat":       "diet:meat",
	"chicken":    "diet:meat",

	// time of day
	"morning":   "tod:morning",
	"am":        "tod:morning",
	"evening":   "tod:evening",
	"night":     "tod:evening",
	"pm":        "tod:evening",

	// agreement
	"approve":    "verdict:yes",
	"approved":   "verdict:yes",
	"accept":     "verdict:yes",
	"reject":     "verdict:no",
	"rejected":   "verdict:no",
	"decline":    "verdict:no",
	"deny":       "verdict:no",

	// activity preference
	"remote":     "loc:remote",
	"office":     "loc:office",
	"onsite":     "loc:office",
}

// polarityOrder is polarityPairs keys sorted longest-first, so the
// longest matching token wins (avoids "async" containing "sync" →
// mis-tagging as comms:sync). Initialised lazily by polarityOf.
var (
	polarityOrderOnce sync.Once
	polarityOrder     []string
)

// polarityOf returns the polarity tag of the longest token in the
// text that maps to a polarity. Word-boundary matched so "asynchronous"
// matches "async" cleanly without later "sync" overriding it.
func polarityOf(text string) string {
	polarityOrderOnce.Do(func() {
		polarityOrder = make([]string, 0, len(polarityPairs))
		for k := range polarityPairs {
			polarityOrder = append(polarityOrder, k)
		}
		sort.Slice(polarityOrder, func(i, j int) bool {
			return len(polarityOrder[i]) > len(polarityOrder[j])
		})
	})
	lower := strings.ToLower(text)
	for _, token := range polarityOrder {
		if hasWord(lower, token) {
			return polarityPairs[token]
		}
	}
	return ""
}

// hasWord returns true if `lower` contains `token` flanked by either
// string boundaries or non-letter characters — prevents "sync" from
// matching inside "asynchronous".
func hasWord(lower, token string) bool {
	for {
		idx := strings.Index(lower, token)
		if idx < 0 {
			return false
		}
		// Boundary on the left
		leftOK := idx == 0 || !isWordChar(lower[idx-1])
		// Boundary on the right
		rightOK := idx+len(token) == len(lower) || !isWordChar(lower[idx+len(token)])
		if leftOK && rightOK {
			return true
		}
		lower = lower[idx+1:]
	}
}

func isWordChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	}
	return false
}
