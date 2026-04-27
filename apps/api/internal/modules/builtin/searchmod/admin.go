package searchmod

import (
	"sort"
	"strings"
	"sync"
)

// ── aliases ───────────────────────────────────────────────────────
//
// Aliases are alternate names that resolve to the same Index. Indexes
// are referenced by callers via either the canonical name (set by
// FT.CREATE) or any alias added via FT.ALIASADD.

var (
	aliasMu sync.RWMutex
	aliases = map[string]string{} // alias -> canonical index name
)

// resolveIndex tries the alias map first, then falls back to the
// canonical name. Lookups inside the search module that should respect
// aliases call resolveIndex(name) instead of getIndex(name) directly.
func resolveIndex(name string) (*Index, bool) {
	aliasMu.RLock()
	canonical, hasAlias := aliases[name]
	aliasMu.RUnlock()
	if hasAlias {
		name = canonical
	}
	return getIndex(name)
}

// listAliases returns every (alias, canonical) pair in stable order,
// for FT.INFO and operational dumps.
func listAliases() []struct{ Alias, Canonical string } {
	aliasMu.RLock()
	defer aliasMu.RUnlock()
	out := make([]struct{ Alias, Canonical string }, 0, len(aliases))
	for a, c := range aliases {
		out = append(out, struct{ Alias, Canonical string }{a, c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

// ── dictionaries ──────────────────────────────────────────────────
//
// FT.DICTADD/DEL/DUMP maintain user-defined custom term dictionaries.
// Spellcheck consults them via FT.SPELLCHECK ... TERMS INCLUDE/EXCLUDE
// dict — the existing spellcheck path already takes a dictionary list
// argument, so we just need to register/lookup terms here.

var (
	dictMu sync.RWMutex
	dicts  = map[string]map[string]struct{}{} // dict name -> set of terms
)

// DictAdd inserts terms into the named dictionary, creating it if
// needed. Returns the count of *new* terms added (Redis semantics).
func DictAdd(name string, terms []string) int {
	dictMu.Lock()
	defer dictMu.Unlock()
	d, ok := dicts[name]
	if !ok {
		d = map[string]struct{}{}
		dicts[name] = d
	}
	added := 0
	for _, t := range terms {
		if _, exists := d[t]; !exists {
			d[t] = struct{}{}
			added++
		}
	}
	return added
}

// DictDel removes terms from the named dictionary. Returns the count
// actually deleted; an empty dict is removed entirely so subsequent
// FT.DICTDUMP doesn't report a ghost.
func DictDel(name string, terms []string) int {
	dictMu.Lock()
	defer dictMu.Unlock()
	d, ok := dicts[name]
	if !ok {
		return 0
	}
	removed := 0
	for _, t := range terms {
		if _, exists := d[t]; exists {
			delete(d, t)
			removed++
		}
	}
	if len(d) == 0 {
		delete(dicts, name)
	}
	return removed
}

// DictDump returns every term in the named dictionary, sorted.
func DictDump(name string) []string {
	dictMu.RLock()
	defer dictMu.RUnlock()
	d, ok := dicts[name]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(d))
	for t := range d {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ── runtime config ────────────────────────────────────────────────
//
// FT.CONFIG GET/SET surface a small set of tunables. We follow Redis's
// "set anything, return what's set" model — the config table is a
// generic string->string map; only the listed keys ship with sensible
// defaults. Unknown keys still round-trip so drivers using
// FT.CONFIG SET <new-knob> <v> followed by FT.CONFIG GET <new-knob>
// see their value back.

var (
	cfgMu     sync.RWMutex
	defaultFT = map[string]string{
		"MAXEXPANSIONS":      "200",
		"MAXSEARCHRESULTS":   "1000000",
		"MAXAGGREGATERESULTS": "1000000",
		"DEFAULT_DIALECT":    "1",
		"TIMEOUT":            "500",
		"MIN_PHONETIC_TERM_LEN": "3",
		"FORK_GC_RUN_INTERVAL": "30",
	}
	cfg = func() map[string]string {
		out := map[string]string{}
		for k, v := range defaultFT {
			out[k] = v
		}
		return out
	}()
)

// ConfigGet returns the values for the requested keys. "*" matches every
// known key. Each result is a [k, v] tuple in stable order.
func ConfigGet(pattern string) [][2]string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	keys := []string{}
	for k := range cfg {
		if pattern == "" || pattern == "*" || strings.EqualFold(pattern, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, cfg[k]})
	}
	return out
}

// ConfigSet records key=value. Returns true if the key was accepted —
// today every key is accepted (Redis follows the same model so client
// libraries can experiment with new knobs without errors).
func ConfigSet(key, value string) bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg[strings.ToUpper(key)] = value
	return true
}

// ── tag values ────────────────────────────────────────────────────

// TagValues returns every distinct tag value present on `field` in
// `index`. Stable-sorted so callers see deterministic output.
func TagValues(index, field string) ([]string, bool) {
	idx, ok := resolveIndex(index)
	if !ok {
		return nil, false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	bucket, ok := idx.tags[field]
	if !ok {
		return nil, true // index exists, field has no tags — empty array
	}
	out := make([]string, 0, len(bucket))
	for v := range bucket {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, true
}
