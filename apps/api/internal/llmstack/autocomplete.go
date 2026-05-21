package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Autocomplete is a per-list radix-trie keyed on phrase, weighted by
// an optional score. Solves the "chat suggestion / command palette /
// gazetteer match / NER lookup" pain — every team rebuilds this with
// either an O(N) prefix scan (slow at scale) or a heavyweight search
// engine (overkill for "show top-10 completions").
//
// AUTOCOMPLETE.* gives the cache one command set:
//
//   AUTOCOMPLETE.ADD list-id phrase [SCORE n]
//   AUTOCOMPLETE.SUGGEST list-id prefix [K n]
//        → top-K phrases starting with `prefix`, ordered by score desc
//          then alphabetical
//   AUTOCOMPLETE.DEL list-id phrase
//   AUTOCOMPLETE.SIZE list-id
//   AUTOCOMPLETE.LIST list-id [PREFIX p]
//   AUTOCOMPLETE.FORGET list-id
//   AUTOCOMPLETE.STATS
//
// Implementation: simple sorted-string list per list-id with
// case-folded keys. Insert/delete are O(N) but rare; suggest is
// O(log N + K) via binary search + linear walk over matching
// prefixes. For typical autocomplete loads (10k-100k phrases × 1k
// QPS) this is sub-microsecond.
//
// Why not a full trie? A sorted slice of strings has better cache
// locality on modern CPUs and is dramatically simpler to keep in
// sync with score updates. Apps that need millions of phrases
// graduate to a real search engine; this is the lightweight path.
type Autocomplete struct {
	mu    sync.RWMutex
	lists map[string]*acList

	totalAdds     atomic.Int64
	totalSuggests atomic.Int64
	totalHits     atomic.Int64
}

type acList struct {
	mu       sync.RWMutex
	phrases  []acPhrase // sorted ascending by lowercase phrase
	byPhrase map[string]int
}

type acPhrase struct {
	lower    string
	original string
	score    float64
}

// NewAutocomplete returns an empty registry.
func NewAutocomplete() *Autocomplete {
	return &Autocomplete{lists: map[string]*acList{}}
}

// Add inserts or updates a phrase. Replacing an existing phrase
// updates its score in place.
func (a *Autocomplete) Add(listID, phrase string, score float64) error {
	if listID == "" {
		return errors.New("list_id required")
	}
	if phrase == "" {
		return errors.New("phrase required")
	}
	a.totalAdds.Add(1)
	list := a.listFor(listID)
	lower := strings.ToLower(phrase)

	list.mu.Lock()
	defer list.mu.Unlock()
	if idx, ok := list.byPhrase[lower]; ok {
		list.phrases[idx].score = score
		list.phrases[idx].original = phrase // refresh original casing
		return nil
	}
	// Binary-search insert position
	pos := sort.Search(len(list.phrases), func(i int) bool {
		return list.phrases[i].lower >= lower
	})
	list.phrases = append(list.phrases, acPhrase{})
	copy(list.phrases[pos+1:], list.phrases[pos:])
	list.phrases[pos] = acPhrase{lower: lower, original: phrase, score: score}
	// Rebuild byPhrase index (O(N) but only on insert; reads are O(1))
	for i, p := range list.phrases {
		list.byPhrase[p.lower] = i
	}
	return nil
}

// Suggestion is one row of SUGGEST.
type Suggestion struct {
	Phrase string  `json:"phrase"`
	Score  float64 `json:"score"`
}

// Suggest returns the top-K phrases starting with `prefix`, ordered
// by score desc, then alphabetical asc. K defaults to 10.
// Case-insensitive prefix match.
func (a *Autocomplete) Suggest(listID, prefix string, k int) []Suggestion {
	a.totalSuggests.Add(1)
	if k <= 0 {
		k = 10
	}
	a.mu.RLock()
	list, ok := a.lists[listID]
	a.mu.RUnlock()
	if !ok {
		return nil
	}
	lower := strings.ToLower(prefix)
	list.mu.RLock()
	// Binary-search the lower-bound of phrases >= prefix
	start := sort.Search(len(list.phrases), func(i int) bool {
		return list.phrases[i].lower >= lower
	})
	// Collect matching phrases until prefix no longer applies
	matches := make([]acPhrase, 0, k*2)
	for i := start; i < len(list.phrases); i++ {
		if !strings.HasPrefix(list.phrases[i].lower, lower) {
			break
		}
		matches = append(matches, list.phrases[i])
	}
	list.mu.RUnlock()

	// Sort: score desc, then phrase asc
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].lower < matches[j].lower
	})
	if len(matches) > k {
		matches = matches[:k]
	}
	if len(matches) > 0 {
		a.totalHits.Add(int64(len(matches)))
	}
	out := make([]Suggestion, len(matches))
	for i, m := range matches {
		out[i] = Suggestion{Phrase: m.original, Score: m.score}
	}
	return out
}

// Del removes a phrase. Returns true if it existed.
func (a *Autocomplete) Del(listID, phrase string) bool {
	a.mu.RLock()
	list, ok := a.lists[listID]
	a.mu.RUnlock()
	if !ok {
		return false
	}
	lower := strings.ToLower(phrase)
	list.mu.Lock()
	defer list.mu.Unlock()
	idx, ok := list.byPhrase[lower]
	if !ok {
		return false
	}
	list.phrases = append(list.phrases[:idx], list.phrases[idx+1:]...)
	for i, p := range list.phrases {
		list.byPhrase[p.lower] = i
	}
	delete(list.byPhrase, lower)
	return true
}

// Size returns the number of phrases in a list.
func (a *Autocomplete) Size(listID string) (int, bool) {
	a.mu.RLock()
	list, ok := a.lists[listID]
	a.mu.RUnlock()
	if !ok {
		return 0, false
	}
	list.mu.RLock()
	defer list.mu.RUnlock()
	return len(list.phrases), true
}

// List returns every phrase in a list, sorted alphabetically.
func (a *Autocomplete) List(listID, prefix string) []Suggestion {
	a.mu.RLock()
	list, ok := a.lists[listID]
	a.mu.RUnlock()
	if !ok {
		return nil
	}
	lower := strings.ToLower(prefix)
	list.mu.RLock()
	defer list.mu.RUnlock()
	out := make([]Suggestion, 0, len(list.phrases))
	for _, p := range list.phrases {
		if prefix != "" && !strings.HasPrefix(p.lower, lower) {
			continue
		}
		out = append(out, Suggestion{Phrase: p.original, Score: p.score})
	}
	return out
}

// Forget drops a whole list. Returns the number of phrases removed.
func (a *Autocomplete) Forget(listID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	list, ok := a.lists[listID]
	if !ok {
		return 0
	}
	n := len(list.phrases)
	delete(a.lists, listID)
	return n
}

// AutocompleteStats is the global snapshot.
type AutocompleteStats struct {
	Lists         int   `json:"lists"`
	TotalPhrases  int   `json:"total_phrases"`
	TotalAdds     int64 `json:"total_adds"`
	TotalSuggests int64 `json:"total_suggests"`
	TotalHits     int64 `json:"total_hits"`
}

func (a *Autocomplete) Stats() AutocompleteStats {
	a.mu.RLock()
	n := len(a.lists)
	total := 0
	for _, l := range a.lists {
		l.mu.RLock()
		total += len(l.phrases)
		l.mu.RUnlock()
	}
	a.mu.RUnlock()
	return AutocompleteStats{
		Lists:         n,
		TotalPhrases:  total,
		TotalAdds:     a.totalAdds.Load(),
		TotalSuggests: a.totalSuggests.Load(),
		TotalHits:     a.totalHits.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (a *Autocomplete) listFor(listID string) *acList {
	a.mu.RLock()
	list, ok := a.lists[listID]
	a.mu.RUnlock()
	if ok {
		return list
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if list, ok := a.lists[listID]; ok {
		return list
	}
	fresh := &acList{
		phrases:  make([]acPhrase, 0, 64),
		byPhrase: map[string]int{},
	}
	a.lists[listID] = fresh
	return fresh
}
