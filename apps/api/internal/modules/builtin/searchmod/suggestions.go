package searchmod

import (
	"sort"
	"strings"
	"sync"
)

// SuggestionDict is the autocomplete trie + score table backing
// FT.SUGADD / FT.SUGGET. Production deployments often have many such
// dicts (one per language or product line); we keep a per-process
// registry keyed by name, just like the underlying indexes table.
type SuggestionDict struct {
	mu      sync.RWMutex
	entries map[string]*suggestionEntry
	root    *trieNode
}

type suggestionEntry struct {
	str     string
	score   float64
	payload string
}

type trieNode struct {
	children map[byte]*trieNode
	terminal bool
	str      string // populated only at terminal nodes
}

func newTrieNode() *trieNode { return &trieNode{children: map[byte]*trieNode{}} }

func newSuggestionDict() *SuggestionDict {
	return &SuggestionDict{entries: map[string]*suggestionEntry{}, root: newTrieNode()}
}

// Add inserts or updates a suggestion. `incr` adds to the existing
// score instead of replacing it (matches FT.SUGADD INCR).
func (s *SuggestionDict) Add(str string, score float64, incr bool, payload string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	exists := s.entries[str] != nil
	if !exists {
		s.entries[str] = &suggestionEntry{str: str}
		s.insertTrie(str)
	}
	e := s.entries[str]
	if incr {
		e.score += score
	} else {
		e.score = score
	}
	if payload != "" {
		e.payload = payload
	}
	if exists {
		return 0
	}
	return 1
}

// Del removes a suggestion. Returns whether it existed.
func (s *SuggestionDict) Del(str string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[str]; !ok {
		return false
	}
	delete(s.entries, str)
	s.removeTrie(str)
	return true
}

// Len reports how many entries the dict holds.
func (s *SuggestionDict) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Get returns the top-N matches for `prefix`, sorted by score
// descending. fuzzy enables ±1 edit-distance matches.
func (s *SuggestionDict) Get(prefix string, max int, fuzzy, withScores, withPayloads bool) []SuggestionResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if max <= 0 {
		max = 5
	}
	matches := []*suggestionEntry{}
	collect := func(p string) {
		node := s.descend(p)
		if node == nil {
			return
		}
		s.collectTerminals(node, &matches)
	}
	collect(prefix)
	if fuzzy {
		// Try one-edit perturbations of the prefix: substitution at
		// each position + a single insertion. Real FT.SUGGET has a more
		// elaborate Levenshtein automaton; this gives reasonable recall
		// for typo-tolerant autocomplete without a full DFA.
		alphabet := "abcdefghijklmnopqrstuvwxyz0123456789"
		for i := 0; i < len(prefix); i++ {
			for _, c := range alphabet {
				if byte(c) == prefix[i] {
					continue
				}
				perturbed := prefix[:i] + string(c) + prefix[i+1:]
				collect(perturbed)
			}
		}
		for i := 0; i <= len(prefix); i++ {
			for _, c := range alphabet {
				perturbed := prefix[:i] + string(c) + prefix[i:]
				collect(perturbed)
			}
		}
	}
	// dedupe + sort
	seen := map[string]struct{}{}
	uniq := matches[:0]
	for _, m := range matches {
		if _, dup := seen[m.str]; dup {
			continue
		}
		seen[m.str] = struct{}{}
		uniq = append(uniq, m)
	}
	sort.Slice(uniq, func(i, j int) bool {
		if uniq[i].score != uniq[j].score {
			return uniq[i].score > uniq[j].score
		}
		return uniq[i].str < uniq[j].str
	})
	if max < len(uniq) {
		uniq = uniq[:max]
	}
	out := make([]SuggestionResult, 0, len(uniq))
	for _, m := range uniq {
		out = append(out, SuggestionResult{
			String: m.str, Score: m.score, Payload: m.payload,
			ReturnScore: withScores, ReturnPayload: withPayloads,
		})
	}
	return out
}

// SuggestionResult is one row in the FT.SUGGET reply.
type SuggestionResult struct {
	String        string
	Score         float64
	Payload       string
	ReturnScore   bool
	ReturnPayload bool
}

func (s *SuggestionDict) insertTrie(str string) {
	cur := s.root
	for i := 0; i < len(str); i++ {
		c := str[i]
		next, ok := cur.children[c]
		if !ok {
			next = newTrieNode()
			cur.children[c] = next
		}
		cur = next
	}
	cur.terminal = true
	cur.str = str
}

func (s *SuggestionDict) removeTrie(str string) {
	cur := s.root
	path := []*trieNode{cur}
	for i := 0; i < len(str); i++ {
		next, ok := cur.children[str[i]]
		if !ok {
			return
		}
		path = append(path, next)
		cur = next
	}
	cur.terminal = false
	cur.str = ""
	// Prune empty branches bottom-up.
	for i := len(path) - 1; i > 0; i-- {
		n := path[i]
		if n.terminal || len(n.children) > 0 {
			break
		}
		parent := path[i-1]
		for ch, child := range parent.children {
			if child == n {
				delete(parent.children, ch)
				break
			}
		}
	}
}

func (s *SuggestionDict) descend(prefix string) *trieNode {
	cur := s.root
	for i := 0; i < len(prefix); i++ {
		next, ok := cur.children[prefix[i]]
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func (s *SuggestionDict) collectTerminals(start *trieNode, out *[]*suggestionEntry) {
	if start.terminal {
		if e, ok := s.entries[start.str]; ok {
			*out = append(*out, e)
		}
	}
	for _, child := range start.children {
		s.collectTerminals(child, out)
	}
}

// ── synonym groups ──────────────────────────────────────────────────

// SynonymManager keeps named groups of equivalent terms. FT.SYNUPDATE
// adds terms to a group; the search executor expands every term by
// any group it belongs to before scoring.
type SynonymManager struct {
	mu    sync.RWMutex
	terms map[string][]string // term -> group ids it belongs to
	groups map[string][]string // group id -> all terms in the group
}

func newSynonymManager() *SynonymManager {
	return &SynonymManager{terms: map[string][]string{}, groups: map[string][]string{}}
}

func (m *SynonymManager) Update(group string, terms []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range terms {
		t = strings.ToLower(t)
		if !contains(m.groups[group], t) {
			m.groups[group] = append(m.groups[group], t)
		}
		if !contains(m.terms[t], group) {
			m.terms[t] = append(m.terms[t], group)
		}
	}
}

func (m *SynonymManager) Dump() map[string][]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string][]string{}
	for term, groups := range m.terms {
		out[term] = append([]string{}, groups...)
	}
	return out
}

// Expand returns every term equivalent to `term` (including itself).
func (m *SynonymManager) Expand(term string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	groups, ok := m.terms[strings.ToLower(term)]
	if !ok {
		return []string{term}
	}
	seen := map[string]struct{}{strings.ToLower(term): {}}
	out := []string{term}
	for _, g := range groups {
		for _, sib := range m.groups[g] {
			if _, dup := seen[sib]; dup {
				continue
			}
			seen[sib] = struct{}{}
			out = append(out, sib)
		}
	}
	return out
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ── per-process suggestion registry ─────────────────────────────────

var (
	sugMu  sync.RWMutex
	sugDicts = map[string]*SuggestionDict{}
)

func sugGet(name string) *SuggestionDict {
	sugMu.RLock()
	d, ok := sugDicts[name]
	sugMu.RUnlock()
	if ok {
		return d
	}
	sugMu.Lock()
	defer sugMu.Unlock()
	if d, ok := sugDicts[name]; ok {
		return d
	}
	d = newSuggestionDict()
	sugDicts[name] = d
	return d
}

func sugDel(name string) bool {
	sugMu.Lock()
	defer sugMu.Unlock()
	if _, ok := sugDicts[name]; !ok {
		return false
	}
	delete(sugDicts, name)
	return true
}
