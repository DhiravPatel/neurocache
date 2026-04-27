package searchmod

import (
	"reflect"
	"sort"
	"testing"
)

// resetGlobals clears the module-level maps so tests don't bleed state.
// (FT.* admin state lives in package-level singletons by design — the
// real engine only loads the module once per process.)
func resetSearchAdmin() {
	indexMu.Lock()
	indexes = map[string]*Index{}
	indexMu.Unlock()
	aliasMu.Lock()
	aliases = map[string]string{}
	aliasMu.Unlock()
	dictMu.Lock()
	dicts = map[string]map[string]struct{}{}
	dictMu.Unlock()
	cfgMu.Lock()
	cfg = func() map[string]string {
		out := map[string]string{}
		for k, v := range defaultFT {
			out[k] = v
		}
		return out
	}()
	cfgMu.Unlock()
}

// ── alias resolution ──────────────────────────────────────────────

func TestAliasResolve(t *testing.T) {
	resetSearchAdmin()
	idx := NewIndex("real", &Schema{})
	setIndex("real", idx)

	aliasMu.Lock()
	aliases["pretty"] = "real"
	aliasMu.Unlock()

	got, ok := resolveIndex("pretty")
	if !ok || got != idx {
		t.Fatalf("alias should resolve to the canonical index, ok=%v same=%v", ok, got == idx)
	}
	// Canonical name still works.
	got, ok = resolveIndex("real")
	if !ok || got != idx {
		t.Fatal("canonical name should still resolve")
	}
	// Unknown name fails.
	if _, ok := resolveIndex("ghost"); ok {
		t.Fatal("unknown name must not resolve")
	}
}

// ── dictionaries ──────────────────────────────────────────────────

func TestDictAddDelDump(t *testing.T) {
	resetSearchAdmin()
	if added := DictAdd("d", []string{"a", "b", "c"}); added != 3 {
		t.Fatalf("first add = %d, want 3", added)
	}
	if added := DictAdd("d", []string{"a", "d"}); added != 1 {
		t.Fatalf("repeat add should only count new, got %d", added)
	}
	if removed := DictDel("d", []string{"a", "missing"}); removed != 1 {
		t.Fatalf("del = %d, want 1", removed)
	}
	got := DictDump("d")
	want := []string{"b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dump = %v, want %v", got, want)
	}
}

func TestDictDeleteEmptiesDict(t *testing.T) {
	resetSearchAdmin()
	DictAdd("solo", []string{"only"})
	DictDel("solo", []string{"only"})
	if got := DictDump("solo"); got != nil {
		t.Fatalf("emptied dict should report nil, got %v", got)
	}
}

// ── runtime config ────────────────────────────────────────────────

func TestConfigRoundtrip(t *testing.T) {
	resetSearchAdmin()
	got := ConfigGet("MAXEXPANSIONS")
	if len(got) != 1 || got[0][0] != "MAXEXPANSIONS" || got[0][1] != "200" {
		t.Fatalf("default MAXEXPANSIONS not surfaced: %v", got)
	}
	ConfigSet("MAXEXPANSIONS", "500")
	got = ConfigGet("MAXEXPANSIONS")
	if got[0][1] != "500" {
		t.Fatalf("after SET, got %v", got)
	}
	// Star pattern returns every key.
	all := ConfigGet("*")
	if len(all) < len(defaultFT) {
		t.Fatalf("* should return at least %d keys, got %d", len(defaultFT), len(all))
	}
}

func TestConfigUnknownKeyRoundtrips(t *testing.T) {
	resetSearchAdmin()
	ConfigSet("EXPERIMENTAL_KNOB", "yes")
	got := ConfigGet("EXPERIMENTAL_KNOB")
	if len(got) != 1 || got[0][1] != "yes" {
		t.Fatalf("unknown key should round-trip, got %v", got)
	}
}

// ── tag values ────────────────────────────────────────────────────

func TestTagValuesEnumeratesDistinct(t *testing.T) {
	resetSearchAdmin()
	idx := NewIndex("books", &Schema{
		Fields: []*FieldSpec{
			{Name: "genre", Type: FieldTag, TagSep: ","},
		},
	})
	setIndex("books", idx)
	idx.AddDoc("d1", map[string]string{"genre": "scifi,horror"}, 1)
	idx.AddDoc("d2", map[string]string{"genre": "scifi"}, 1)
	idx.AddDoc("d3", map[string]string{"genre": "drama"}, 1)

	got, ok := TagValues("books", "genre")
	if !ok {
		t.Fatal("index lookup failed")
	}
	sort.Strings(got)
	want := []string{"drama", "horror", "scifi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tag values = %v, want %v", got, want)
	}
}

func TestTagValuesUnknownIndex(t *testing.T) {
	resetSearchAdmin()
	if _, ok := TagValues("ghost", "genre"); ok {
		t.Fatal("unknown index should return ok=false")
	}
}
