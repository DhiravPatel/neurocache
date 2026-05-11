package llmstack

import (
	"testing"
)

const weatherSchema = `{"type":"object","properties":{"city":{"type":"string"}}}`
const searchSchema = `{"type":"object","properties":{"q":{"type":"string"}}}`

func TestToolBoxRegisterAndGet(t *testing.T) {
	b := NewToolBox()
	if err := b.Register("weather", "get_weather", "Fetch current weather for a city",
		weatherSchema, ToolboxOpts{Tags: []string{"weather", "travel"}}); err != nil {
		t.Fatal(err)
	}
	got, ok := b.Get("weather")
	if !ok {
		t.Fatal("get returned false")
	}
	if got.Name != "get_weather" || got.Description == "" {
		t.Fatalf("got = %+v", got)
	}
}

func TestToolBoxRejectsBadSchema(t *testing.T) {
	b := NewToolBox()
	if err := b.Register("x", "y", "z", "{not json", ToolboxOpts{}); err == nil {
		t.Fatal("expected error for invalid JSON schema")
	}
}

func TestToolBoxRejectsEmptyFields(t *testing.T) {
	b := NewToolBox()
	if err := b.Register("", "name", "desc", "{}", ToolboxOpts{}); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := b.Register("id", "", "desc", "{}", ToolboxOpts{}); err == nil {
		t.Fatal("empty name should fail")
	}
	if err := b.Register("id", "name", "desc", "", ToolboxOpts{}); err == nil {
		t.Fatal("empty schema should fail")
	}
}

func TestToolBoxSearchFindsRelevant(t *testing.T) {
	b := NewToolBox()
	b.Register("weather", "get_weather", "Fetch current weather conditions for a city",
		weatherSchema, ToolboxOpts{})
	b.Register("search", "web_search", "Search the web for general queries",
		searchSchema, ToolboxOpts{})
	b.Register("calc", "calculator", "Evaluate arithmetic expressions",
		`{"type":"object","properties":{"expr":{"type":"string"}}}`, ToolboxOpts{})

	hits := b.Search("what is the weather in paris", SearchOpts{K: 1})
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].ID != "weather" {
		t.Fatalf("expected weather tool top, got %s", hits[0].ID)
	}
}

func TestToolBoxSearchDefaultK(t *testing.T) {
	b := NewToolBox()
	for i := 0; i < 10; i++ {
		b.Register(string(rune('a'+i)), "name", "desc", `{}`, ToolboxOpts{})
	}
	hits := b.Search("anything", SearchOpts{})
	if len(hits) != 5 {
		t.Fatalf("default K should be 5, got %d", len(hits))
	}
}

func TestToolBoxSearchTagFilter(t *testing.T) {
	b := NewToolBox()
	b.Register("weather", "get_weather", "weather", weatherSchema,
		ToolboxOpts{Tags: []string{"travel"}})
	b.Register("search", "web_search", "search", searchSchema,
		ToolboxOpts{Tags: []string{"research"}})
	hits := b.Search("anything", SearchOpts{K: 5, Tags: []string{"research"}})
	if len(hits) != 1 || hits[0].ID != "search" {
		t.Fatalf("tag filter failed: %+v", hits)
	}
}

func TestToolBoxExplicitEmbedding(t *testing.T) {
	b := NewToolBox()
	b.Register("a", "name", "desc", `{}`, ToolboxOpts{Vec: []float64{1, 0, 0}})
	b.Register("b", "name", "desc", `{}`, ToolboxOpts{Vec: []float64{0, 1, 0}})
	b.Register("c", "name", "desc", `{}`, ToolboxOpts{Vec: []float64{0.9, 0.1, 0}})

	hits := b.Search("anything", SearchOpts{K: 1, Vec: []float64{1, 0, 0}})
	if hits[0].ID != "a" {
		t.Fatalf("expected a, got %s", hits[0].ID)
	}
}

func TestToolBoxDimMismatchRejected(t *testing.T) {
	b := NewToolBox()
	b.Register("a", "n", "d", `{}`, ToolboxOpts{Vec: []float64{1, 0, 0}})
	if err := b.Register("b", "n", "d", `{}`, ToolboxOpts{Vec: []float64{1, 0}}); err == nil {
		t.Fatal("expected dim mismatch error")
	}
}

func TestToolBoxListAndForget(t *testing.T) {
	b := NewToolBox()
	b.Register("a", "n", "d", `{}`, ToolboxOpts{})
	b.Register("b", "n", "d", `{}`, ToolboxOpts{})
	if list := b.List(nil); len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	if !b.Forget("a") {
		t.Fatal("forget should return true")
	}
	if list := b.List(nil); len(list) != 1 || list[0].ID != "b" {
		t.Fatalf("list after forget = %+v", list)
	}
}

func TestToolBoxStatsAdvance(t *testing.T) {
	b := NewToolBox()
	b.Register("a", "n", "d", `{}`, ToolboxOpts{})
	b.Search("x", SearchOpts{K: 1})
	b.Search("y", SearchOpts{K: 1})
	s := b.Stats()
	if s.Tools != 1 || s.TotalRegisters != 1 || s.TotalSearches != 2 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestToolBoxSchemaRoundTrip(t *testing.T) {
	b := NewToolBox()
	const sch = `{"type":"object","properties":{"x":{"type":"integer"}}}`
	b.Register("t", "name", "desc", sch, ToolboxOpts{})
	got, _ := b.Get("t")
	if got.Schema != sch {
		t.Fatalf("schema round-trip failed:\n got: %s\nwant: %s", got.Schema, sch)
	}
}
