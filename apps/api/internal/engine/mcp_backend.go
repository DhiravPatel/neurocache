package engine

// mcpBackend adapts the engine to aiops.ToolBackend. Each method
// translates one MCP tool call into the right subsystem invocation
// without coupling internal/aiops to the engine's full type graph
// (which would form an import cycle).

import (
	"errors"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/retrieval"
)

// mcpBackend is a thin pointer-to-engine wrapper. We use a separate
// type instead of methods on *Engine so the ToolBackend interface
// surface is small and explicit — the engine has hundreds of methods,
// most irrelevant to MCP.
type mcpBackend struct{ e *Engine }

func (b *mcpBackend) KVGet(key string) (string, bool) {
	v, ok, err := b.e.KV.GetTyped(key)
	if err != nil || !ok {
		return "", false
	}
	return v, true
}

func (b *mcpBackend) KVSet(key, value string) error {
	b.e.KV.Set(key, value, 0)
	b.e.RecordWrite("SET", []string{key, value})
	return nil
}

func (b *mcpBackend) SemanticGet(key string, threshold float32) (string, float32, bool) {
	return b.e.Semantic.Get(key, threshold)
}

func (b *mcpBackend) SemanticSet(key, value string) {
	b.e.Semantic.Set(key, value)
	b.e.RecordWrite("SEMANTIC_SET", []string{key, value})
}

func (b *mcpBackend) MemoryAdd(userID, text, layer string, importance float64) (string, error) {
	l := memory.Layer(strings.ToLower(layer))
	if l == "" {
		l = memory.LayerEpisodic
	}
	if !l.IsValid() {
		return "", errors.New("invalid layer; must be episodic|semantic|procedural")
	}
	e, _, err := b.e.Memory.AddWithOptions(userID, text, memory.AddOptions{
		Layer:      l,
		Importance: importance,
	})
	if err != nil {
		return "", err
	}
	b.e.RecordWrite("MEMORY.ADD", []string{userID, text, "LAYER", string(l)})
	return e.ID, nil
}

func (b *mcpBackend) MemoryQuery(userID, query, layer string, k int) ([]aiops.MemoryToolHit, error) {
	opts := memory.LayerQueryOptions{
		Layer:     memory.Layer(strings.ToLower(layer)),
		K:         k,
		Threshold: 0.2,
	}
	hits := b.e.Memory.QueryLayered(userID, query, opts)
	out := make([]aiops.MemoryToolHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, aiops.MemoryToolHit{
			ID:         h.Entry.ID,
			Text:       h.Entry.Text,
			Score:      float64(h.Score),
			Layer:      string(h.Entry.Layer),
			Importance: h.Entry.Importance,
		})
	}
	return out, nil
}

func (b *mcpBackend) GraphLink(subject, predicate, object string) bool {
	added := b.e.Graph.Link(subject, predicate, object)
	if added {
		b.e.RecordWrite("GRAPH.LINK", []string{subject, predicate, object})
	}
	return added
}

func (b *mcpBackend) GraphNeighbors(subject, predicate string) []aiops.GraphTriple {
	ns := b.e.Graph.Neighbors(subject, predicate)
	out := make([]aiops.GraphTriple, 0, len(ns))
	for _, n := range ns {
		out = append(out, aiops.GraphTriple{Predicate: n.Predicate, Object: n.Object})
	}
	return out
}

func (b *mcpBackend) RetrieveAdd(index, id, text string, meta map[string]string) error {
	ix := b.e.Retrieval.GetOrCreate(index)
	if err := ix.Add(retrieval.Document{ID: id, Text: text, Metadata: meta}); err != nil {
		return err
	}
	args := []string{index, id, text}
	if len(meta) > 0 {
		args = append(args, "META")
		for k, v := range meta {
			args = append(args, k, v)
		}
	}
	b.e.RecordWrite("RETRIEVE.ADD", args)
	return nil
}

func (b *mcpBackend) RetrieveQuery(index, query string, k int, alpha float64) ([]aiops.RetrievalToolHit, error) {
	ix, ok := b.e.Retrieval.Get(index)
	if !ok {
		return nil, errors.New("no such retrieval index")
	}
	hits := ix.Query(query, retrieval.QueryOptions{K: k, Alpha: alpha})
	out := make([]aiops.RetrievalToolHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, aiops.RetrievalToolHit{
			ID:    h.ID,
			Text:  h.Text,
			Score: h.Score,
			Meta:  h.Metadata,
		})
	}
	return out, nil
}

func (b *mcpBackend) RAGQuery(index, query string, k, hops int, alpha float64) (aiops.RAGToolResult, error) {
	ix, ok := b.e.Retrieval.Get(index)
	if !ok {
		return aiops.RAGToolResult{}, errors.New("no such retrieval index")
	}
	hits := ix.Query(query, retrieval.QueryOptions{K: k, Alpha: alpha})

	res := aiops.RAGToolResult{
		Hits: make([]aiops.RetrievalToolHit, 0, len(hits)),
	}
	for _, h := range hits {
		res.Hits = append(res.Hits, aiops.RetrievalToolHit{
			ID:    h.ID,
			Text:  h.Text,
			Score: h.Score,
			Meta:  h.Metadata,
		})
	}

	// Inline graph expansion — same logic as ragQueryCmd, kept here to
	// avoid exporting the helper from the resp package.
	if hops > 0 {
		seen := map[string]bool{}
		for _, h := range hits {
			anchor, ok := h.Metadata["entity"]
			if !ok {
				continue
			}
			type qNode struct {
				node  string
				depth int
			}
			queue := []qNode{{node: anchor, depth: 0}}
			visited := map[string]bool{anchor: true}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				if cur.depth >= hops {
					continue
				}
				for _, n := range b.e.Graph.Neighbors(cur.node, "") {
					key := cur.node + "\x00" + n.Predicate + "\x00" + n.Object
					if !seen[key] {
						seen[key] = true
						res.Context = append(res.Context, aiops.RAGContextRow{
							Subject:   cur.node,
							Predicate: n.Predicate,
							Object:    n.Object,
							Depth:     cur.depth + 1,
							SourceDoc: h.ID,
						})
					}
					if !visited[n.Object] {
						visited[n.Object] = true
						queue = append(queue, qNode{node: n.Object, depth: cur.depth + 1})
					}
				}
			}
		}
	}
	return res, nil
}

func (b *mcpBackend) ConvAppend(key, role, content string) (int, error) {
	n, err := b.e.Conversations.Append(key, role, content)
	if err != nil {
		return 0, err
	}
	b.e.RecordWrite("CONV.APPEND", []string{key, role, content})
	return n, nil
}

func (b *mcpBackend) ConvWindow(key string, maxTokens int) ([]aiops.ConvTurn, error) {
	turns := b.e.Conversations.Window(key, maxTokens)
	out := make([]aiops.ConvTurn, 0, len(turns))
	for _, t := range turns {
		out = append(out, aiops.ConvTurn{Role: t.Role, Content: t.Content, Tokens: t.Tokens})
	}
	return out, nil
}

// registerMCPCatalog wires the production MCP tool catalog. Called
// once from engine.New() after every subsystem the backend depends
// on is constructed.
func (e *Engine) registerMCPCatalog() {
	aiops.BuildToolCatalog(e.MCP, &mcpBackend{e: e})
}
