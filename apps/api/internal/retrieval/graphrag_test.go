package retrieval

import (
	"testing"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// TestGraphRAGEndToEnd exercises the full hybrid-retrieval +
// graph-expansion path that backs both the RESP RAG.QUERY and the MCP
// neurocache.rag_query tool. We drive the underlying primitives
// directly (the wiring layer just adapts them to the protocol) so the
// test stays fast and protocol-agnostic.
func TestGraphRAGEndToEnd(t *testing.T) {
	ix, err := New(Options{Dim: 256})
	if err != nil {
		t.Fatal(err)
	}
	// Three documents, each with an `entity` metadata anchor that
	// links into the knowledge graph below.
	docs := []Document{
		{ID: "d1", Text: "Anthropic released Claude with new tool-use abilities", Metadata: map[string]string{"entity": "Anthropic"}},
		{ID: "d2", Text: "OpenAI launched GPT-4 with vision support", Metadata: map[string]string{"entity": "OpenAI"}},
		{ID: "d3", Text: "Google DeepMind merged research and product teams", Metadata: map[string]string{"entity": "Google"}},
	}
	for _, d := range docs {
		if err := ix.Add(d); err != nil {
			t.Fatalf("add %s: %v", d.ID, err)
		}
	}

	g := aiops.NewGraph()
	g.Link("Anthropic", "founded_by", "Dario Amodei")
	g.Link("Anthropic", "headquartered_in", "San Francisco")
	g.Link("Dario Amodei", "previously_at", "OpenAI")
	g.Link("OpenAI", "headquartered_in", "San Francisco")
	g.Link("Google", "headquartered_in", "Mountain View")

	// Query for Claude — should return d1, then expand Anthropic's
	// outgoing edges (1 hop): founded_by Dario Amodei,
	// headquartered_in San Francisco.
	hits := ix.Query("Claude tool use", QueryOptions{K: 3})
	if len(hits) == 0 || hits[0].ID != "d1" {
		t.Fatalf("expected d1 as top hit, got %v", hits)
	}

	// Walk the graph 1 hop from each hit's entity anchor.
	context := walkOneHop(g, hits, "")
	want := map[string]string{
		"founded_by":         "Dario Amodei",
		"headquartered_in":   "San Francisco",
	}
	for predicate, object := range want {
		found := false
		for _, c := range context {
			if c.Predicate == predicate && c.Object == object {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected (Anthropic, %s, %s) in expansion, got %+v", predicate, object, context)
		}
	}
}

func TestGraphRAGFiltersByPredicate(t *testing.T) {
	ix, _ := New(Options{Dim: 128})
	ix.Add(Document{ID: "d1", Text: "company alpha", Metadata: map[string]string{"entity": "alpha"}})

	g := aiops.NewGraph()
	g.Link("alpha", "knows", "beta")
	g.Link("alpha", "founded_by", "person1")
	g.Link("alpha", "rival_of", "gamma")

	hits := ix.Query("company alpha", QueryOptions{K: 2})
	context := walkOneHop(g, hits, "knows")
	if len(context) != 1 || context[0].Predicate != "knows" || context[0].Object != "beta" {
		t.Errorf("predicate filter failed; got %+v", context)
	}
}

// walkOneHop is a self-contained re-implementation of the graph
// expansion the engine + RESP command does. Kept here so this test
// doesn't pull in the engine package (which would produce an import
// cycle since engine imports retrieval).
type ctxRow struct {
	Subject   string
	Predicate string
	Object    string
}

func walkOneHop(g *aiops.Graph, hits []Hit, predicate string) []ctxRow {
	out := []ctxRow{}
	seen := map[string]bool{}
	for _, h := range hits {
		anchor, ok := h.Metadata["entity"]
		if !ok {
			continue
		}
		for _, n := range g.Neighbors(anchor, predicate) {
			key := anchor + "\x00" + n.Predicate + "\x00" + n.Object
			if !seen[key] {
				seen[key] = true
				out = append(out, ctxRow{Subject: anchor, Predicate: n.Predicate, Object: n.Object})
			}
		}
	}
	return out
}
