package aiops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeBackend is a hand-rolled stand-in for engine. Each method records
// the last call so assertions can verify the catalog routes correctly.
type fakeBackend struct {
	kv       map[string]string
	semantic map[string]string

	memoryAddCall struct {
		userID, text, layer string
		importance          float64
	}
	memoryQueryCall struct {
		userID, query, layer string
		k                    int
	}

	graphLinks []GraphTriple // (predicate, object) pairs we received

	retrieveDocs map[string]map[string]string // index → id → text
	retrieveQs   []struct {
		index, query string
		k            int
		alpha        float64
	}

	convAppendN int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		kv:           map[string]string{},
		semantic:     map[string]string{},
		retrieveDocs: map[string]map[string]string{},
	}
}

func (f *fakeBackend) KVGet(k string) (string, bool)                { v, ok := f.kv[k]; return v, ok }
func (f *fakeBackend) KVSet(k, v string) error                       { f.kv[k] = v; return nil }
func (f *fakeBackend) SemanticGet(k string, threshold float32) (string, float32, bool) {
	v, ok := f.semantic[k]
	if !ok {
		return "", 0, false
	}
	return v, 0.95, true
}
func (f *fakeBackend) SemanticSet(k, v string)                       { f.semantic[k] = v }
func (f *fakeBackend) MemoryAdd(userID, text, layer string, importance float64) (string, error) {
	f.memoryAddCall.userID, f.memoryAddCall.text = userID, text
	f.memoryAddCall.layer, f.memoryAddCall.importance = layer, importance
	if userID == "" || text == "" {
		return "", errors.New("missing args")
	}
	return "fake-id-1", nil
}
func (f *fakeBackend) MemoryQuery(userID, query, layer string, k int) ([]MemoryToolHit, error) {
	f.memoryQueryCall.userID, f.memoryQueryCall.query = userID, query
	f.memoryQueryCall.layer, f.memoryQueryCall.k = layer, k
	return []MemoryToolHit{
		{ID: "m1", Text: "hello", Score: 0.9, Layer: layer, Importance: 0.5},
	}, nil
}
func (f *fakeBackend) GraphLink(s, p, o string) bool {
	f.graphLinks = append(f.graphLinks, GraphTriple{Predicate: p, Object: o})
	return true
}
func (f *fakeBackend) GraphNeighbors(subject, predicate string) []GraphTriple {
	return []GraphTriple{{Predicate: "knows", Object: "alice"}}
}
func (f *fakeBackend) RetrieveAdd(index, id, text string, meta map[string]string) error {
	if _, ok := f.retrieveDocs[index]; !ok {
		f.retrieveDocs[index] = map[string]string{}
	}
	f.retrieveDocs[index][id] = text
	return nil
}
func (f *fakeBackend) RetrieveQuery(index, query string, k int, alpha float64) ([]RetrievalToolHit, error) {
	f.retrieveQs = append(f.retrieveQs, struct {
		index, query string
		k            int
		alpha        float64
	}{index, query, k, alpha})
	return []RetrievalToolHit{{ID: "r1", Text: "hi", Score: 0.5}}, nil
}
func (f *fakeBackend) RAGQuery(index, query string, k, hops int, alpha float64) (RAGToolResult, error) {
	return RAGToolResult{
		Hits:    []RetrievalToolHit{{ID: "r1", Text: "hi", Score: 0.5}},
		Context: []RAGContextRow{{Subject: "a", Predicate: "knows", Object: "b", Depth: 1, SourceDoc: "r1"}},
	}, nil
}
func (f *fakeBackend) ConvAppend(key, role, content string) (int, error) {
	f.convAppendN++
	return f.convAppendN, nil
}
func (f *fakeBackend) ConvWindow(key string, maxTokens int) ([]ConvTurn, error) {
	return []ConvTurn{{Role: "user", Content: "hi", Tokens: 1}}, nil
}

func TestBuildToolCatalogRegistersExpectedTools(t *testing.T) {
	m := NewMCP()
	BuildToolCatalog(m, newFakeBackend())
	names := ToolNames(m)
	expected := []string{
		"neurocache.kv_get", "neurocache.kv_set",
		"neurocache.semantic_get", "neurocache.semantic_set",
		"neurocache.memory_add", "neurocache.memory_query",
		"neurocache.graph_link", "neurocache.graph_neighbors",
		"neurocache.retrieve_add", "neurocache.retrieve_query",
		"neurocache.rag_query",
		"neurocache.conv_append", "neurocache.conv_window",
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range expected {
		if !have[want] {
			t.Errorf("missing tool %q (got %d tools)", want, len(names))
		}
	}
}

func TestKVToolsRoundTrip(t *testing.T) {
	m := NewMCP()
	fake := newFakeBackend()
	BuildToolCatalog(m, fake)

	// Set, then get.
	resp := callTool(t, m, "neurocache.kv_set", `{"key":"k1","value":"v1"}`)
	if !strings.Contains(resp, "OK") {
		t.Errorf("kv_set reply: %s", resp)
	}
	if fake.kv["k1"] != "v1" {
		t.Errorf("backend not updated; got %q", fake.kv["k1"])
	}

	resp = callTool(t, m, "neurocache.kv_get", `{"key":"k1"}`)
	if !strings.Contains(resp, "v1") {
		t.Errorf("kv_get reply did not contain value; got %s", resp)
	}
}

func TestMemoryToolsPassLayerAndImportance(t *testing.T) {
	m := NewMCP()
	fake := newFakeBackend()
	BuildToolCatalog(m, fake)

	callTool(t, m, "neurocache.memory_add", `{"user_id":"u1","text":"hi","layer":"semantic","importance":0.8}`)
	if fake.memoryAddCall.layer != "semantic" || fake.memoryAddCall.importance != 0.8 {
		t.Errorf("memory_add did not pass layer/importance: %+v", fake.memoryAddCall)
	}

	callTool(t, m, "neurocache.memory_query", `{"user_id":"u1","query":"hello","layer":"episodic","k":3}`)
	if fake.memoryQueryCall.layer != "episodic" || fake.memoryQueryCall.k != 3 {
		t.Errorf("memory_query did not pass options: %+v", fake.memoryQueryCall)
	}
}

func TestRetrieveAndRAGTools(t *testing.T) {
	m := NewMCP()
	fake := newFakeBackend()
	BuildToolCatalog(m, fake)

	callTool(t, m, "neurocache.retrieve_add", `{"index":"docs","id":"a","text":"hello world","metadata":{"tenant":"acme"}}`)
	if fake.retrieveDocs["docs"]["a"] != "hello world" {
		t.Errorf("retrieve_add did not route: %+v", fake.retrieveDocs)
	}

	resp := callTool(t, m, "neurocache.retrieve_query", `{"index":"docs","query":"hello","k":3,"alpha":0.7}`)
	if len(fake.retrieveQs) != 1 || fake.retrieveQs[0].alpha != 0.7 {
		t.Errorf("retrieve_query did not pass alpha: %+v", fake.retrieveQs)
	}
	if !strings.Contains(resp, `"r1"`) {
		t.Errorf("retrieve_query reply missing hit: %s", resp)
	}

	resp = callTool(t, m, "neurocache.rag_query", `{"index":"docs","query":"hello","k":3,"hops":2}`)
	if !strings.Contains(resp, `"context"`) {
		t.Errorf("rag_query reply missing context: %s", resp)
	}
}

func TestUnknownToolReturnsRPCError(t *testing.T) {
	m := NewMCP()
	BuildToolCatalog(m, newFakeBackend())
	resp := rawCallTool(t, m, "neurocache.does_not_exist", `{}`)
	if !strings.Contains(resp, "tool not found") {
		t.Errorf("expected 'tool not found' error, got: %s", resp)
	}
}

// callTool invokes a tool via the JSON-RPC layer (the same path real
// MCP clients hit), unwraps the `result.content[0].text` field, and
// returns it. Test failures here mean either the tool isn't routing
// or the JSON-RPC envelope is wrong — both production-relevant.
func callTool(t *testing.T, m *MCP, name, args string) string {
	t.Helper()
	raw := rawCallTool(t, m, name, args)
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *JSONRPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse: %v\nraw=%s", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result.Content) == 0 {
		return ""
	}
	return resp.Result.Content[0].Text
}

func rawCallTool(t *testing.T, m *MCP, name, args string) string {
	t.Helper()
	frame := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      name,
			"arguments": json.RawMessage(args),
		},
	}
	body, _ := json.Marshal(frame)
	return string(m.HandleBytes(body))
}
