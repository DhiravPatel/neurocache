package aiops

// Production MCP tool catalog. Without this, `MCP.TOOLS` returns an
// empty list — MCP clients (Claude Desktop, Cursor, IDE wrappers)
// connect, ask "what tools are there?", get nothing, and disconnect.
// This file registers the real NeuroCache primitives an LLM agent
// actually wants: KV, semantic cache, layered memory, hybrid
// retrieval, GraphRAG, knowledge graph, conversations.
//
// Each tool has a JSON Schema for its arguments so the model knows
// the call shape, plus a handler that delegates to the engine
// subsystem. The handlers return either a string (which becomes the
// MCP "text" content) or a JSON-marshalable value.
//
// Wiring path: engine.New() instantiates this with NewMCP() →
// engine.RegisterMCPCatalog(catalog) plugs it in. Adding a new tool
// is one entry in BuildToolCatalog and (if it has state) an engine
// pointer on ToolBackend.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ToolBackend is the minimal surface the MCP catalog needs from the
// engine. We keep this small so engine and aiops don't form an import
// cycle — the engine adapts itself to this interface and passes it
// in. Each method is one MCP tool's worth of capability.
type ToolBackend interface {
	// KV
	KVGet(key string) (string, bool)
	KVSet(key, value string) error

	// Semantic cache
	SemanticGet(key string, threshold float32) (string, float32, bool)
	SemanticSet(key, value string)

	// Memory (layered)
	MemoryAdd(userID, text, layer string, importance float64) (id string, err error)
	MemoryQuery(userID, query, layer string, k int) ([]MemoryToolHit, error)

	// Knowledge graph
	GraphLink(subject, predicate, object string) bool
	GraphNeighbors(subject, predicate string) []GraphTriple

	// Hybrid retrieval / RAG
	RetrieveAdd(index, id, text string, meta map[string]string) error
	RetrieveQuery(index, query string, k int, alpha float64) ([]RetrievalToolHit, error)
	RAGQuery(index, query string, k, hops int, alpha float64) (RAGToolResult, error)

	// Conversations
	ConvAppend(key, role, content string) (int, error)
	ConvWindow(key string, maxTokens int) ([]ConvTurn, error)
}

// MemoryToolHit is the cross-package shape we return for memory hits;
// the actual `memory.QueryHit` lives in another package, and we don't
// want this catalog package to import it (would create a cycle through
// engine).
type MemoryToolHit struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Score      float64 `json:"score"`
	Layer      string  `json:"layer"`
	Importance float64 `json:"importance"`
}

// GraphTriple is the simple (predicate, object) outgoing edge.
type GraphTriple struct {
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// RetrievalToolHit mirrors retrieval.Hit minus the rank-debug fields
// LLM clients don't need.
type RetrievalToolHit struct {
	ID    string            `json:"id"`
	Text  string            `json:"text"`
	Score float64           `json:"score"`
	Meta  map[string]string `json:"meta,omitempty"`
}

// RAGToolResult is the GraphRAG tool's reply shape.
type RAGToolResult struct {
	Hits    []RetrievalToolHit `json:"hits"`
	Context []RAGContextRow    `json:"context"`
}

// RAGContextRow is one expanded triple in a GraphRAG reply.
type RAGContextRow struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	Depth     int    `json:"depth"`
	SourceDoc string `json:"source_doc"`
}

// ConvTurn is one turn in a conversation. Mirrors llmstack.Turn so
// the catalog package doesn't have to import it.
type ConvTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Tokens  int    `json:"tokens"`
}

// BuildToolCatalog registers every NeuroCache primitive that's useful
// to an LLM client. Call from engine.New() after MCP and the rest of
// the subsystems are constructed.
func BuildToolCatalog(m *MCP, b ToolBackend) {
	register := func(t *MCPTool) {
		m.RegisterTool(t)
	}

	register(&MCPTool{
		Name:        "neurocache.kv_get",
		Description: "Read a key from the NeuroCache KV store. Returns the value as a string, or empty if absent.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key": map[string]interface{}{"type": "string"},
			},
			"required": []string{"key"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return nil, errors.New("key is required")
			}
			v, ok := b.KVGet(key)
			if !ok {
				return "", nil
			}
			return v, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.kv_set",
		Description: "Write a string value to the NeuroCache KV store.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key":   map[string]interface{}{"type": "string"},
				"value": map[string]interface{}{"type": "string"},
			},
			"required": []string{"key", "value"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" {
				return nil, errors.New("key is required")
			}
			if err := b.KVSet(key, value); err != nil {
				return nil, err
			}
			return "OK", nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.semantic_get",
		Description: "Look up a semantic cache value by meaning. Returns the cached value when a stored key crosses the cosine threshold against the query.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":     map[string]interface{}{"type": "string"},
				"threshold": map[string]interface{}{"type": "number", "default": 0.6},
			},
			"required": []string{"query"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			q, _ := args["query"].(string)
			if q == "" {
				return nil, errors.New("query is required")
			}
			threshold := float32(0.6)
			if t, ok := args["threshold"].(float64); ok {
				threshold = float32(t)
			}
			v, score, hit := b.SemanticGet(q, threshold)
			if !hit {
				return map[string]interface{}{"hit": false}, nil
			}
			return map[string]interface{}{
				"hit":   true,
				"value": v,
				"score": score,
			}, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.semantic_set",
		Description: "Store a value under a semantic key. Future queries that mean the same thing will return this value.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key":   map[string]interface{}{"type": "string"},
				"value": map[string]interface{}{"type": "string"},
			},
			"required": []string{"key", "value"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" || value == "" {
				return nil, errors.New("key and value are required")
			}
			b.SemanticSet(key, value)
			return "OK", nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.memory_add",
		Description: "Record a memory for a user. Layer must be one of: episodic (events), semantic (distilled facts), procedural (preferences/rules). Importance in [0,1] tilts retention.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"user_id":    map[string]interface{}{"type": "string"},
				"text":       map[string]interface{}{"type": "string"},
				"layer":      map[string]interface{}{"type": "string", "enum": []string{"episodic", "semantic", "procedural"}, "default": "episodic"},
				"importance": map[string]interface{}{"type": "number", "default": 0.5},
			},
			"required": []string{"user_id", "text"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			user, _ := args["user_id"].(string)
			text, _ := args["text"].(string)
			if user == "" || text == "" {
				return nil, errors.New("user_id and text required")
			}
			layer, _ := args["layer"].(string)
			if layer == "" {
				layer = "episodic"
			}
			imp := 0.5
			if v, ok := args["importance"].(float64); ok {
				imp = v
			}
			id, err := b.MemoryAdd(user, text, layer, imp)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"id": id, "layer": layer}, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.memory_query",
		Description: "Retrieve a user's memories by semantic similarity. Filter by layer to get only facts (semantic), only events (episodic), or only preferences (procedural).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"user_id": map[string]interface{}{"type": "string"},
				"query":   map[string]interface{}{"type": "string"},
				"layer":   map[string]interface{}{"type": "string", "enum": []string{"episodic", "semantic", "procedural", ""}, "default": ""},
				"k":       map[string]interface{}{"type": "integer", "default": 5},
			},
			"required": []string{"user_id", "query"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			user, _ := args["user_id"].(string)
			q, _ := args["query"].(string)
			if user == "" || q == "" {
				return nil, errors.New("user_id and query required")
			}
			layer, _ := args["layer"].(string)
			k := 5
			if n, ok := args["k"].(float64); ok {
				k = int(n)
			}
			hits, err := b.MemoryQuery(user, q, layer, k)
			if err != nil {
				return nil, err
			}
			return hits, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.graph_link",
		Description: "Add a (subject, predicate, object) triple to the knowledge graph. Idempotent.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"subject":   map[string]interface{}{"type": "string"},
				"predicate": map[string]interface{}{"type": "string"},
				"object":    map[string]interface{}{"type": "string"},
			},
			"required": []string{"subject", "predicate", "object"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			s, _ := args["subject"].(string)
			p, _ := args["predicate"].(string)
			o, _ := args["object"].(string)
			if s == "" || p == "" || o == "" {
				return nil, errors.New("subject, predicate, object are all required")
			}
			added := b.GraphLink(s, p, o)
			return map[string]interface{}{"added": added}, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.graph_neighbors",
		Description: "Walk outgoing edges from a graph subject. Optionally filter by predicate.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"subject":   map[string]interface{}{"type": "string"},
				"predicate": map[string]interface{}{"type": "string", "default": ""},
			},
			"required": []string{"subject"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			s, _ := args["subject"].(string)
			p, _ := args["predicate"].(string)
			if s == "" {
				return nil, errors.New("subject is required")
			}
			return b.GraphNeighbors(s, p), nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.retrieve_add",
		Description: "Add a document to a hybrid retrieval index (BM25 + vector). Index is created on first use.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"index":    map[string]interface{}{"type": "string"},
				"id":       map[string]interface{}{"type": "string"},
				"text":     map[string]interface{}{"type": "string"},
				"metadata": map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"index", "id", "text"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			index, _ := args["index"].(string)
			id, _ := args["id"].(string)
			text, _ := args["text"].(string)
			if index == "" || id == "" || text == "" {
				return nil, errors.New("index, id, text required")
			}
			meta := map[string]string{}
			if m, ok := args["metadata"].(map[string]interface{}); ok {
				for k, v := range m {
					if vs, ok := v.(string); ok {
						meta[k] = vs
					} else {
						meta[k] = fmt.Sprint(v)
					}
				}
			}
			if err := b.RetrieveAdd(index, id, text, meta); err != nil {
				return nil, err
			}
			return "OK", nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.retrieve_query",
		Description: "Hybrid (lexical + semantic) search over a retrieval index. Returns top-k with fused scores. Alpha=0 is BM25-only, alpha=1 is vector-only, alpha=0.5 is balanced.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"index": map[string]interface{}{"type": "string"},
				"query": map[string]interface{}{"type": "string"},
				"k":     map[string]interface{}{"type": "integer", "default": 5},
				"alpha": map[string]interface{}{"type": "number", "default": 0.5},
			},
			"required": []string{"index", "query"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			index, _ := args["index"].(string)
			q, _ := args["query"].(string)
			if index == "" || q == "" {
				return nil, errors.New("index and query required")
			}
			k := 5
			if n, ok := args["k"].(float64); ok {
				k = int(n)
			}
			alpha := 0.5
			if v, ok := args["alpha"].(float64); ok {
				alpha = v
			}
			hits, err := b.RetrieveQuery(index, q, k, alpha)
			if err != nil {
				return nil, err
			}
			return hits, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.rag_query",
		Description: "GraphRAG: hybrid search PLUS knowledge-graph expansion of the entities attached to top hits. Use when the answer requires combining retrieved passages with structured relationships. `hops` controls how deep to walk the graph (default 1).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"index": map[string]interface{}{"type": "string"},
				"query": map[string]interface{}{"type": "string"},
				"k":     map[string]interface{}{"type": "integer", "default": 5},
				"hops":  map[string]interface{}{"type": "integer", "default": 1},
				"alpha": map[string]interface{}{"type": "number", "default": 0.5},
			},
			"required": []string{"index", "query"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			index, _ := args["index"].(string)
			q, _ := args["query"].(string)
			if index == "" || q == "" {
				return nil, errors.New("index and query required")
			}
			k := 5
			if n, ok := args["k"].(float64); ok {
				k = int(n)
			}
			hops := 1
			if n, ok := args["hops"].(float64); ok {
				hops = int(n)
			}
			alpha := 0.5
			if v, ok := args["alpha"].(float64); ok {
				alpha = v
			}
			res, err := b.RAGQuery(index, q, k, hops, alpha)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.conv_append",
		Description: "Append a turn (system/user/assistant/tool) to a named conversation. Returns the new total turn count.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key":     map[string]interface{}{"type": "string"},
				"role":    map[string]interface{}{"type": "string", "enum": []string{"system", "user", "assistant", "tool"}},
				"content": map[string]interface{}{"type": "string"},
			},
			"required": []string{"key", "role", "content"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			role, _ := args["role"].(string)
			content, _ := args["content"].(string)
			if key == "" || role == "" {
				return nil, errors.New("key and role required")
			}
			n, err := b.ConvAppend(key, role, content)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"turns": n}, nil
		},
	})

	register(&MCPTool{
		Name:        "neurocache.conv_window",
		Description: "Read a conversation's recent turns back, capped at a token budget. Includes the rolling summary as a synthetic system turn when present.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key":        map[string]interface{}{"type": "string"},
				"max_tokens": map[string]interface{}{"type": "integer", "default": 0},
			},
			"required": []string{"key"},
		},
		Handler: func(args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return nil, errors.New("key required")
			}
			max := 0
			if n, ok := args["max_tokens"].(float64); ok {
				max = int(n)
			}
			turns, err := b.ConvWindow(key, max)
			if err != nil {
				return nil, err
			}
			return turns, nil
		},
	})
}

// ToolNames returns the canonical list of registered tool names so
// docs / dashboards stay in sync without hard-coding.
func ToolNames(m *MCP) []string {
	tools := m.Tools()
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// JSONString is a small helper for tests / debug printers — encodes
// a tool reply the same way the MCP layer does.
func JSONString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	out, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
	return string(out)
}
