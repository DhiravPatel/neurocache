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

// TranslateCache memoises translations by (source_lang, target_lang,
// text). Translation is one of the most cacheable LLM-adjacent
// workloads — every text translates identically every time, queries
// repeat across users + tenants, and the upstream APIs are expensive
// ($20-25/M chars for Google / DeepL). Apps still pay for the same
// translation hundreds of times because nobody centralised the
// cache.
//
// TRANSLATE.* gives the cache a single (lang-pair, text) -> result
// store:
//
//   TRANSLATE.SET source target text translation [EX sec]
//   TRANSLATE.GET source target text                 -> translation or nil
//   TRANSLATE.MGET source target text1 text2 text3   -> array of strings/nil
//                  (bulk fan-out for paragraph-level apps)
//   TRANSLATE.FORGET source target text
//   TRANSLATE.PURGE [SOURCE x] [TARGET y]            -> int dropped
//   TRANSLATE.SETCAP n
//   TRANSLATE.SETCOST usd  (USD per upstream call)
//   TRANSLATE.STATS        -> per-pair hit rate + saved_usd
//
// Implementation: lock-free RWMutex-protected map keyed by
// sha256(source|0|target|0|text) prefix. Each entry stores the
// translation + expiry + lang-pair tag. Per-language-pair counters
// via sync.Map for lock-free hot-path updates. Soft cap (default
// 100k entries) with oldest-10% sweep eviction.
//
// Throughput target: GET in <300 ns. MGET amortises the map-lookup
// cost across N keys for paragraph-level batch translation.
type TranslateCache struct {
	mu      sync.RWMutex
	entries map[string]*translateEntry
	cap     int
	costUSD float64

	totalGets   atomic.Int64
	totalHits   atomic.Int64
	totalMisses atomic.Int64
	totalSets   atomic.Int64
	savedCalls  atomic.Int64
	totalEvicts atomic.Int64

	perPair sync.Map // "src|tgt" -> *pairStat
}

type translateEntry struct {
	pair      string // "en|es"
	value     string
	expiresAt int64
	createdAt int64
}

type pairStat struct {
	hits   atomic.Int64
	misses atomic.Int64
	sets   atomic.Int64
}

// NewTranslateCache returns an empty cache with 100k soft cap.
func NewTranslateCache() *TranslateCache {
	return &TranslateCache{
		entries: map[string]*translateEntry{},
		cap:     100_000,
	}
}

// SetCap adjusts soft eviction threshold.
func (t *TranslateCache) SetCap(n int) {
	t.mu.Lock()
	t.cap = n
	t.mu.Unlock()
}

// SetCostUSD records the $/upstream-call so STATS reports saved_usd.
func (t *TranslateCache) SetCostUSD(usd float64) {
	t.mu.Lock()
	t.costUSD = usd
	t.mu.Unlock()
}

// Set stores a translation with optional TTL.
func (t *TranslateCache) Set(source, target, text, translation string, ttl time.Duration) error {
	if source == "" || target == "" || text == "" {
		return errors.New("source, target, and text are required")
	}
	t.totalSets.Add(1)
	pair := source + "|" + target
	t.pairStatFor(pair).sets.Add(1)
	k := translateKey(source, target, text)
	now := time.Now().UnixNano()
	exp := int64(0)
	if ttl > 0 {
		exp = now + ttl.Nanoseconds()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cap > 0 && len(t.entries) >= t.cap {
		t.evictOldestLocked(t.cap / 10)
	}
	t.entries[k] = &translateEntry{
		pair:      pair,
		value:     translation,
		expiresAt: exp,
		createdAt: now,
	}
	return nil
}

// Get returns the cached translation or (empty, false) on miss.
func (t *TranslateCache) Get(source, target, text string) (string, bool) {
	t.totalGets.Add(1)
	pair := source + "|" + target
	stat := t.pairStatFor(pair)
	k := translateKey(source, target, text)
	t.mu.RLock()
	e, ok := t.entries[k]
	t.mu.RUnlock()
	if !ok {
		t.totalMisses.Add(1)
		stat.misses.Add(1)
		return "", false
	}
	if e.expiresAt != 0 && time.Now().UnixNano() > e.expiresAt {
		t.mu.Lock()
		delete(t.entries, k)
		t.mu.Unlock()
		t.totalMisses.Add(1)
		stat.misses.Add(1)
		return "", false
	}
	t.totalHits.Add(1)
	t.savedCalls.Add(1)
	stat.hits.Add(1)
	return e.value, true
}

// MGetResult is one bulk-fetch row.
type MGetResult struct {
	Text        string `json:"text"`
	Translation string `json:"translation,omitempty"`
	Hit         bool   `json:"hit"`
}

// MGet bulk-fetches translations for multiple texts under the same
// language pair. Returns one row per input text in the same order.
// Single round trip + amortised map-lookup overhead.
func (t *TranslateCache) MGet(source, target string, texts []string) []MGetResult {
	out := make([]MGetResult, len(texts))
	for i, text := range texts {
		out[i].Text = text
		v, ok := t.Get(source, target, text)
		if ok {
			out[i].Translation = v
			out[i].Hit = true
		}
	}
	return out
}

// Forget drops one entry.
func (t *TranslateCache) Forget(source, target, text string) bool {
	k := translateKey(source, target, text)
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.entries[k]
	delete(t.entries, k)
	return ok
}

// Purge wipes entries. Optional source/target filter (empty = all).
func (t *TranslateCache) Purge(source, target string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if source == "" && target == "" {
		n := len(t.entries)
		t.entries = map[string]*translateEntry{}
		return n
	}
	n := 0
	for k, e := range t.entries {
		parts := []string{}
		if source != "" {
			parts = append(parts, source)
		}
		if target != "" {
			parts = append(parts, target)
		}
		matches := true
		if source != "" && !pairStartsWith(e.pair, source+"|") {
			matches = false
		}
		if target != "" && !pairEndsWith(e.pair, "|"+target) {
			matches = false
		}
		if matches {
			delete(t.entries, k)
			n++
		}
	}
	return n
}

// TranslatePairStatsRow is one row of TRANSLATE.STATS pairs list.
type TranslatePairStatsRow struct {
	Pair    string  `json:"pair"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Sets    int64   `json:"sets"`
	HitRate float64 `json:"hit_rate"`
}

// TranslateStats is the global counters snapshot.
type TranslateStats struct {
	Entries     int                     `json:"entries"`
	Cap         int                     `json:"cap"`
	TotalGets   int64                   `json:"total_gets"`
	TotalHits   int64                   `json:"total_hits"`
	TotalMisses int64                   `json:"total_misses"`
	TotalSets   int64                   `json:"total_sets"`
	SavedCalls  int64                   `json:"saved_calls"`
	SavedUSD    float64                 `json:"saved_usd"`
	HitRate     float64                 `json:"hit_rate"`
	TotalEvicts int64                   `json:"total_evicts"`
	CostUSD     float64                 `json:"cost_usd"`
	Pairs       []TranslatePairStatsRow `json:"pairs,omitempty"`
}

func (t *TranslateCache) Stats() TranslateStats {
	t.mu.RLock()
	n := len(t.entries)
	cap := t.cap
	cost := t.costUSD
	t.mu.RUnlock()
	gets := t.totalGets.Load()
	hits := t.totalHits.Load()
	rate := 0.0
	if gets > 0 {
		rate = float64(hits) / float64(gets)
	}
	saved := t.savedCalls.Load()
	out := TranslateStats{
		Entries:     n,
		Cap:         cap,
		TotalGets:   gets,
		TotalHits:   hits,
		TotalMisses: t.totalMisses.Load(),
		TotalSets:   t.totalSets.Load(),
		SavedCalls:  saved,
		SavedUSD:    float64(saved) * cost,
		HitRate:     rate,
		TotalEvicts: t.totalEvicts.Load(),
		CostUSD:     cost,
	}
	t.perPair.Range(func(k, v any) bool {
		ps := v.(*pairStat)
		h := ps.hits.Load()
		m := ps.misses.Load()
		hr := 0.0
		if h+m > 0 {
			hr = float64(h) / float64(h+m)
		}
		out.Pairs = append(out.Pairs, TranslatePairStatsRow{
			Pair: k.(string), Hits: h, Misses: m,
			Sets: ps.sets.Load(), HitRate: hr,
		})
		return true
	})
	sort.Slice(out.Pairs, func(i, j int) bool { return out.Pairs[i].Pair < out.Pairs[j].Pair })
	return out
}

// ─── helpers ───────────────────────────────────────────────────

func (t *TranslateCache) pairStatFor(pair string) *pairStat {
	if v, ok := t.perPair.Load(pair); ok {
		return v.(*pairStat)
	}
	fresh := &pairStat{}
	actual, _ := t.perPair.LoadOrStore(pair, fresh)
	return actual.(*pairStat)
}

func translateKey(source, target, text string) string {
	h := sha256.New()
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write([]byte(target))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (t *TranslateCache) evictOldestLocked(n int) {
	if n <= 0 || len(t.entries) == 0 {
		return
	}
	type kv struct {
		k  string
		at int64
	}
	all := make([]kv, 0, len(t.entries))
	for k, e := range t.entries {
		all = append(all, kv{k, e.createdAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
	if n > len(all) {
		n = len(all)
	}
	for i := 0; i < n; i++ {
		delete(t.entries, all[i].k)
	}
	t.totalEvicts.Add(int64(n))
}

func pairStartsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func pairEndsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
