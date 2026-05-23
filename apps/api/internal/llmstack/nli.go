package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// NLICache stores Natural Language Inference verdicts (entailment
// relationships) between text pairs. Real production pain it
// addresses: hallucination detection at the claim level requires
// asking "does this generated claim follow from the source?" —
// an NLI question. Apps pay for entailment scoring on every
// generated claim, but the same (premise, hypothesis) pair shows
// up repeatedly across users.
//
// Different from GROUND (lexical Jaccard) and VERIFY (consistency
// across N samples): NLI is the explicit logical-relationship
// check, computed via apps' own NLI model (HuggingFace
// roberta-nli, OpenAI structured-output, etc.) and cached here.
//
// Three relations:
//   - entails       — premise logically implies hypothesis
//   - contradicts   — premise contradicts hypothesis
//   - neutral       — no logical relationship
//
// Commands:
//
//   NLI.SET premise hypothesis relation [SCORE n] [EX sec]
//   NLI.GET premise hypothesis            → relation + score or nil
//   NLI.CHECK premise hypothesis [DEFAULT relation]
//        Returns cached relation if present, else the default.
//        Use when you want to gracefully degrade missing-cache
//        cases to a sensible default (e.g. "neutral").
//   NLI.MGET premise hypothesis1 hypothesis2 ...
//        Bulk lookup — single round-trip for fan-out grading.
//   NLI.FORGET premise hypothesis
//   NLI.PURGE
//   NLI.STATS — per-relation hit counts
//
// Storage: lock-free RWMutex map keyed by sha256(premise|hypothesis)
// prefix. Per-relation atomic hit counters. Soft cap (default 100k)
// with oldest-FIFO eviction.
type NLICache struct {
	mu      sync.RWMutex
	entries map[string]*nliEntry
	cap     int

	totalGets        atomic.Int64
	totalHits        atomic.Int64
	totalMisses      atomic.Int64
	totalSets        atomic.Int64
	totalEvicts      atomic.Int64
	hitsEntails      atomic.Int64
	hitsContradicts  atomic.Int64
	hitsNeutral      atomic.Int64
}

type nliEntry struct {
	relation  string  // entails | contradicts | neutral
	score     float64 // confidence 0..1
	expiresAt int64   // unix-nano; 0 = no expiry
	createdAt int64
}

// NewNLICache returns an empty cache.
func NewNLICache() *NLICache {
	return &NLICache{
		entries: map[string]*nliEntry{},
		cap:     100_000,
	}
}

// SetCap adjusts the soft eviction threshold.
func (n *NLICache) SetCap(cap int) {
	n.mu.Lock()
	n.cap = cap
	n.mu.Unlock()
}

// Set stores a relation with optional confidence score + TTL.
func (n *NLICache) Set(premise, hypothesis, relation string, score float64, ttl time.Duration) error {
	if !validNLIRelation(relation) {
		return errors.New("relation must be one of: entails, contradicts, neutral")
	}
	if score < 0 || score > 1 {
		return errors.New("score must be in [0, 1]")
	}
	n.totalSets.Add(1)
	k := nliKey(premise, hypothesis)
	now := time.Now().UnixNano()
	exp := int64(0)
	if ttl > 0 {
		exp = now + ttl.Nanoseconds()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cap > 0 && len(n.entries) >= n.cap {
		n.evictOldestLocked(n.cap / 10)
	}
	n.entries[k] = &nliEntry{
		relation: relation, score: score,
		expiresAt: exp, createdAt: now,
	}
	return nil
}

// NLIResult is what GET returns.
type NLIResult struct {
	Relation string  `json:"relation"`
	Score    float64 `json:"score"`
	Cached   bool    `json:"cached"`
}

// Get returns the cached relation or (zero, false).
func (n *NLICache) Get(premise, hypothesis string) (NLIResult, bool) {
	n.totalGets.Add(1)
	k := nliKey(premise, hypothesis)
	n.mu.RLock()
	e, ok := n.entries[k]
	n.mu.RUnlock()
	if !ok {
		n.totalMisses.Add(1)
		return NLIResult{}, false
	}
	if e.expiresAt != 0 && time.Now().UnixNano() > e.expiresAt {
		n.mu.Lock()
		delete(n.entries, k)
		n.mu.Unlock()
		n.totalMisses.Add(1)
		return NLIResult{}, false
	}
	n.totalHits.Add(1)
	switch e.relation {
	case "entails":
		n.hitsEntails.Add(1)
	case "contradicts":
		n.hitsContradicts.Add(1)
	case "neutral":
		n.hitsNeutral.Add(1)
	}
	return NLIResult{Relation: e.relation, Score: e.score, Cached: true}, true
}

// Check returns the cached relation if present, else the default.
// Apps use this when they want to skip the upstream NLI call and
// accept a default verdict (e.g. "neutral" = "don't gate on this
// claim").
func (n *NLICache) Check(premise, hypothesis, defaultRelation string) NLIResult {
	r, ok := n.Get(premise, hypothesis)
	if ok {
		return r
	}
	return NLIResult{Relation: defaultRelation, Score: 0, Cached: false}
}

// MGetResult is one row of MGET.
type NLIMGetRow struct {
	Hypothesis string  `json:"hypothesis"`
	Relation   string  `json:"relation,omitempty"`
	Score      float64 `json:"score"`
	Cached     bool    `json:"cached"`
}

// MGet bulk-fetches verdicts for one premise vs N hypotheses.
// Single round-trip + amortised hashing.
func (n *NLICache) MGet(premise string, hypotheses []string) []NLIMGetRow {
	out := make([]NLIMGetRow, len(hypotheses))
	for i, h := range hypotheses {
		r, ok := n.Get(premise, h)
		out[i] = NLIMGetRow{
			Hypothesis: h,
			Cached:     ok,
		}
		if ok {
			out[i].Relation = r.Relation
			out[i].Score = r.Score
		}
	}
	return out
}

// Forget drops one entry.
func (n *NLICache) Forget(premise, hypothesis string) bool {
	k := nliKey(premise, hypothesis)
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.entries[k]
	delete(n.entries, k)
	return ok
}

// Purge wipes the cache. Returns the number of entries dropped.
func (n *NLICache) Purge() int {
	n.mu.Lock()
	count := len(n.entries)
	n.entries = map[string]*nliEntry{}
	n.mu.Unlock()
	return count
}

// NLIStats is the global counters snapshot.
type NLIStats struct {
	Entries         int     `json:"entries"`
	Cap             int     `json:"cap"`
	TotalGets       int64   `json:"total_gets"`
	TotalHits       int64   `json:"total_hits"`
	TotalMisses     int64   `json:"total_misses"`
	TotalSets       int64   `json:"total_sets"`
	HitsEntails     int64   `json:"hits_entails"`
	HitsContradicts int64   `json:"hits_contradicts"`
	HitsNeutral     int64   `json:"hits_neutral"`
	TotalEvicts     int64   `json:"total_evicts"`
	HitRate         float64 `json:"hit_rate"`
}

func (n *NLICache) Stats() NLIStats {
	n.mu.RLock()
	num := len(n.entries)
	cap := n.cap
	n.mu.RUnlock()
	gets := n.totalGets.Load()
	hits := n.totalHits.Load()
	rate := 0.0
	if gets > 0 {
		rate = float64(hits) / float64(gets)
	}
	return NLIStats{
		Entries:         num,
		Cap:             cap,
		TotalGets:       gets,
		TotalHits:       hits,
		TotalMisses:     n.totalMisses.Load(),
		TotalSets:       n.totalSets.Load(),
		HitsEntails:     n.hitsEntails.Load(),
		HitsContradicts: n.hitsContradicts.Load(),
		HitsNeutral:     n.hitsNeutral.Load(),
		TotalEvicts:     n.totalEvicts.Load(),
		HitRate:         rate,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func nliKey(premise, hypothesis string) string {
	h := sha256.New()
	h.Write([]byte(premise))
	h.Write([]byte{0})
	h.Write([]byte(hypothesis))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func validNLIRelation(r string) bool {
	return r == "entails" || r == "contradicts" || r == "neutral"
}

func (n *NLICache) evictOldestLocked(num int) {
	if num <= 0 || len(n.entries) == 0 {
		return
	}
	type kv struct {
		k  string
		at int64
	}
	all := make([]kv, 0, len(n.entries))
	for k, e := range n.entries {
		all = append(all, kv{k, e.createdAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
	if num > len(all) {
		num = len(all)
	}
	for i := 0; i < num; i++ {
		delete(n.entries, all[i].k)
	}
	n.totalEvicts.Add(int64(num))
}
