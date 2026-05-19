package llmstack

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ToolBox is a tool-schema registry with semantic capability search.
// Modern agentic apps register dozens-to-hundreds of tools (each
// with a name, description, JSON-schema arguments, and maybe tags).
// Two production pains result:
//
//  1. The agent's function-call manifest balloons — passing 200
//     tool schemas to every LLM call costs tokens AND degrades the
//     model's tool-pick accuracy. The "give me only tools relevant
//     to THIS query" problem.
//
//  2. Different teams ship overlapping tools ("search-web",
//     "google-search", "web-search-v2"). No one knows which to use,
//     and tool discovery via human-readable docs is slow.
//
// TOOLBOX.* solves both with one set of commands:
//
//   TOOLBOX.REGISTER tool-id name description schema-json
//        [TAGS t1,t2,...] [EMBED v1,v2,...]
//        Register or replace a tool entry.
//
//   TOOLBOX.SEARCH query [K n] [TAGS t1,t2,...] [EMBED v1,v2,...]
//        Top-K tools by cosine similarity against `query`. Apps
//        feed THIS slim manifest into the LLM's tool list.
//
//   TOOLBOX.GET tool-id          → single entry
//   TOOLBOX.LIST [TAGS ...]      → every entry, optional tag filter
//   TOOLBOX.FORGET tool-id       → drop one tool
//   TOOLBOX.STATS                → totals + reg/search counters
//
// Embeddings: app passes real vectors (OpenAI / Cohere / local) via
// EMBED at register time, or the cache computes a deterministic
// 128-dim hashed-BoW vector from name + description. The fallback
// is good enough for "find me weather-ish tools" queries.
//
// SEARCH is the hot path: O(N) cosine over the registered tool set.
// At N=200 tools × 128 dims = 25.6k mul-add → <10 µs. Lock-free
// reads via RWMutex (one read-lock per SEARCH).
type ToolBox struct {
	mu    sync.RWMutex
	tools map[string]*toolboxEntry
	dim   int // active embedding dim; set on first register

	totalRegisters atomic.Int64
	totalSearches  atomic.Int64
	totalReturns   atomic.Int64
}

type toolboxEntry struct {
	id          string
	name        string
	description string
	schema      json.RawMessage // raw JSON for pass-through
	tags        map[string]bool
	vec         []float64
}

// NewToolBox returns an empty registry.
func NewToolBox() *ToolBox {
	return &ToolBox{tools: map[string]*toolboxEntry{}}
}

// RegisterOpts configures TOOLBOX.REGISTER.
type ToolboxOpts struct {
	Tags []string
	Vec  []float64
}

// Register stores (or replaces) a tool entry. Schema must be valid
// JSON — apps usually pass the function-calling args schema verbatim.
func (b *ToolBox) Register(id, name, description, schemaJSON string, opts ToolboxOpts) error {
	if id == "" || name == "" {
		return errors.New("id and name required")
	}
	if schemaJSON == "" {
		return errors.New("schema required")
	}
	if !json.Valid([]byte(schemaJSON)) {
		return errors.New("schema is not valid JSON")
	}
	tags := map[string]bool{}
	for _, t := range opts.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			tags[t] = true
		}
	}
	vec := opts.Vec
	if vec == nil {
		vec = embedFallback(name + " " + description)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dim == 0 {
		b.dim = len(vec)
	} else if len(vec) != b.dim {
		return errors.New("embedding dim mismatch with existing tools")
	}
	b.tools[id] = &toolboxEntry{
		id:          id,
		name:        name,
		description: description,
		schema:      json.RawMessage(schemaJSON),
		tags:        tags,
		vec:         vec,
	}
	b.totalRegisters.Add(1)
	return nil
}

// SearchHit is one row of SEARCH/LIST.
type SearchHit struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Schema      string   `json:"schema"`
	Tags        []string `json:"tags,omitempty"`
	Score       float64  `json:"score"`
}

// SearchOpts narrows the SEARCH query.
type SearchOpts struct {
	K    int
	Tags []string
	Vec  []float64
}

// Search returns top-K tools by cosine similarity. K defaults to 5.
// TAGS narrows the candidate set (ALL specified tags must be
// present).
func (b *ToolBox) Search(query string, opts SearchOpts) []SearchHit {
	b.totalSearches.Add(1)
	k := opts.K
	if k <= 0 {
		k = 5
	}
	queryVec := opts.Vec
	if queryVec == nil {
		queryVec = embedFallback(query)
	}
	want := map[string]bool{}
	for _, t := range opts.Tags {
		want[strings.ToLower(strings.TrimSpace(t))] = true
	}
	b.mu.RLock()
	if b.dim == 0 || len(queryVec) != b.dim {
		b.mu.RUnlock()
		return nil
	}
	hits := make([]SearchHit, 0, len(b.tools))
	for _, e := range b.tools {
		if !tagsMatch(e.tags, want) {
			continue
		}
		s := dotProduct(e.vec, queryVec)
		hits = append(hits, SearchHit{
			ID: e.id, Name: e.name, Description: e.description,
			Schema: string(e.schema), Tags: tagsList(e.tags),
			Score: s,
		})
	}
	b.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	b.totalReturns.Add(int64(len(hits)))
	return hits
}

// Get returns one tool by id.
func (b *ToolBox) Get(id string) (SearchHit, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	e, ok := b.tools[id]
	if !ok {
		return SearchHit{}, false
	}
	return SearchHit{
		ID: e.id, Name: e.name, Description: e.description,
		Schema: string(e.schema), Tags: tagsList(e.tags),
		Score: 1.0,
	}, true
}

// List returns every tool, optionally filtered by tags. Ordered by id.
func (b *ToolBox) List(filterTags []string) []SearchHit {
	want := map[string]bool{}
	for _, t := range filterTags {
		want[strings.ToLower(strings.TrimSpace(t))] = true
	}
	b.mu.RLock()
	out := make([]SearchHit, 0, len(b.tools))
	for _, e := range b.tools {
		if !tagsMatch(e.tags, want) {
			continue
		}
		out = append(out, SearchHit{
			ID: e.id, Name: e.name, Description: e.description,
			Schema: string(e.schema), Tags: tagsList(e.tags),
		})
	}
	b.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Forget drops a tool by id. Returns true if it existed.
func (b *ToolBox) Forget(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.tools[id]
	delete(b.tools, id)
	return ok
}

// ToolBoxStats is the global counters snapshot.
type ToolBoxStats struct {
	Tools          int   `json:"tools"`
	TotalRegisters int64 `json:"total_registers"`
	TotalSearches  int64 `json:"total_searches"`
	TotalReturns   int64 `json:"total_returns"`
}

func (b *ToolBox) Stats() ToolBoxStats {
	b.mu.RLock()
	n := len(b.tools)
	b.mu.RUnlock()
	return ToolBoxStats{
		Tools:          n,
		TotalRegisters: b.totalRegisters.Load(),
		TotalSearches:  b.totalSearches.Load(),
		TotalReturns:   b.totalReturns.Load(),
	}
}
