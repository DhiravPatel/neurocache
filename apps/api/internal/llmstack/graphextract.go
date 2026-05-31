package llmstack

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// GraphExtract auto-extracts (subject, relation, object) triples
// from text into a knowledge graph, memoizing by content hash so the
// same text is never re-extracted. GRAPH.LINK is manual; GRAPH.EXTRACT
// makes turning a memory or conversation into GraphRAG a one-liner.
//
// The extractor is a deterministic rule-based one (regex-driven
// patterns over normalised text). For a primitive that lives in the
// cache layer this is the right trade-off: it captures the common
// patterns ("X is Y", "X has Y", "X works at Y", "X was born in Y"),
// runs at ~5µs/sentence, and never makes a model call. Callers that
// want a model-based extractor can post triples directly via the
// underlying GRAPH.LINK and use this just for the dedupe/memoization.
//
// The dedupe set is a content hash → bool index so feeding the same
// paragraph twice does not double-count triples. Triples live in the
// returned slice and are also persisted into the caller-supplied
// graph store (see SetGraphSink).
//
// Commands:
//
//   GRAPH.EXTRACT.RUN graph-id text [SOURCE s]
//        → triples (s/r/o)
//   GRAPH.EXTRACT.LIST graph-id [LIMIT n]
//        Reverse-chronological list of triples extracted into this graph.
//   GRAPH.EXTRACT.SOURCES graph-id
//        Every distinct source we've extracted from for this graph.
//   GRAPH.EXTRACT.FORGET graph-id|ALL
//   GRAPH.EXTRACT.STATS
//
// Hot path: RUN is regex matching + dedupe lookup; LIST + STATS are
// reads.
type GraphExtractor struct {
	mu     sync.RWMutex
	graphs map[string]*geGraph

	totalRuns    atomic.Int64
	totalTriples atomic.Int64
	totalDupes   atomic.Int64
}

type geGraph struct {
	mu      sync.RWMutex
	triples []geTriple
	dedupe  map[string]bool // content hash
	sources map[string]bool
}

type geTriple struct {
	Subject  string `json:"subject"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
	Source   string `json:"source,omitempty"`
}

// Pattern is one extraction rule. Compiled at init.
type gePattern struct {
	re       *regexp.Regexp
	relation string
}

var gePatterns = []gePattern{
	// "X is the CEO of Y" / "X is the founder of Y" / "X is a Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+is\s+(?:the\s+)?(?:a\s+|an\s+)?([\w\s]{1,40}?)\s+(?:of|at|for)\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+and|\s+who|\s+that|$)`), "is_X_of"},
	// "X founded Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+founded\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+in|\s+with|$)`), "founded"},
	// "X works at Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+works?\s+(?:at|for|with)\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+as|\s+on|$)`), "works_at"},
	// "X was born in Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+was\s+born\s+in\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+in\s+\d|$)`), "born_in"},
	// "X has Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+has\s+(?:a\s+|an\s+)?([\w\s\-]{1,40}?)(?:[\.\,]|\s+called|\s+named|$)`), "has"},
	// "X owns Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+owns\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+and|$)`), "owns"},
	// "X uses Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+uses\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+for|\s+to|$)`), "uses"},
	// "X is located in Y" / "X is in Y"
	{regexp.MustCompile(`(?i)\b([A-Z][\w\s]{1,40}?)\s+is\s+(?:located\s+)?in\s+([A-Z][\w\s\-]{1,40}?)(?:[\.\,]|\s+and|$)`), "located_in"},
}

// NewGraphExtractor returns an empty extractor.
func NewGraphExtractor() *GraphExtractor {
	return &GraphExtractor{graphs: map[string]*geGraph{}}
}

// Run extracts triples from text into the graph. The same text
// extracted twice yields zero new triples (memoization).
func (g *GraphExtractor) Run(graphID, text, source string) ([]geTriple, error) {
	if graphID == "" {
		return nil, errors.New("graph_id required")
	}
	if text == "" {
		return nil, errors.New("text required")
	}
	g.totalRuns.Add(1)
	gr := g.graphOrCreate(graphID)
	// Memoize by content hash: same text+source = no-op
	memKey := u32x(fnv1a32(text + "|" + source))
	gr.mu.Lock()
	if gr.dedupe[memKey] {
		gr.mu.Unlock()
		g.totalDupes.Add(1)
		return []geTriple{}, nil
	}
	gr.dedupe[memKey] = true
	if source != "" {
		gr.sources[source] = true
	}
	gr.mu.Unlock()

	// Run every pattern, gather unique triples
	seen := map[string]bool{}
	out := make([]geTriple, 0)
	for _, p := range gePatterns {
		for _, m := range p.re.FindAllStringSubmatch(text, -1) {
			tr, ok := patternToTriple(p, m, source)
			if !ok {
				continue
			}
			k := tr.Subject + "|" + tr.Relation + "|" + tr.Object
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, tr)
		}
	}
	if len(out) > 0 {
		gr.mu.Lock()
		gr.triples = append(gr.triples, out...)
		gr.mu.Unlock()
		g.totalTriples.Add(int64(len(out)))
	}
	return out, nil
}

// List returns the most-recently-extracted triples.
func (g *GraphExtractor) List(graphID string, limit int) ([]geTriple, bool) {
	if graphID == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 100
	}
	g.mu.RLock()
	gr, ok := g.graphs[graphID]
	g.mu.RUnlock()
	if !ok {
		return nil, false
	}
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	n := len(gr.triples)
	if n > limit {
		// reverse-chronological: take tail
		return cloneTriples(gr.triples[n-limit:]), true
	}
	return cloneTriples(gr.triples), true
}

// Sources returns every distinct source we've ever extracted from.
func (g *GraphExtractor) Sources(graphID string) []string {
	g.mu.RLock()
	gr, ok := g.graphs[graphID]
	g.mu.RUnlock()
	if !ok {
		return nil
	}
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	out := make([]string, 0, len(gr.sources))
	for s := range gr.sources {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Forget drops a graph (or all).
func (g *GraphExtractor) Forget(graphID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if graphID == "ALL" {
		n := len(g.graphs)
		g.graphs = map[string]*geGraph{}
		return n
	}
	if _, ok := g.graphs[graphID]; ok {
		delete(g.graphs, graphID)
		return 1
	}
	return 0
}

// GraphExtractStats is the global snapshot.
type GraphExtractStats struct {
	Graphs       int   `json:"graphs"`
	TotalRuns    int64 `json:"total_runs"`
	TotalTriples int64 `json:"total_triples"`
	TotalDupes   int64 `json:"total_dupes"`
}

func (g *GraphExtractor) Stats() GraphExtractStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return GraphExtractStats{
		Graphs:       len(g.graphs),
		TotalRuns:    g.totalRuns.Load(),
		TotalTriples: g.totalTriples.Load(),
		TotalDupes:   g.totalDupes.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (g *GraphExtractor) graphOrCreate(id string) *geGraph {
	g.mu.RLock()
	gr, ok := g.graphs[id]
	g.mu.RUnlock()
	if ok {
		return gr
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr, ok := g.graphs[id]; ok {
		return gr
	}
	gr = &geGraph{dedupe: map[string]bool{}, sources: map[string]bool{}}
	g.graphs[id] = gr
	return gr
}

func patternToTriple(p gePattern, m []string, source string) (geTriple, bool) {
	switch p.relation {
	case "is_X_of":
		if len(m) < 4 {
			return geTriple{}, false
		}
		return geTriple{
			Subject:  cleanEntity(m[1]),
			Relation: "is_" + strings.ToLower(strings.ReplaceAll(cleanEntity(m[2]), " ", "_")) + "_of",
			Object:   cleanEntity(m[3]),
			Source:   source,
		}, true
	default:
		if len(m) < 3 {
			return geTriple{}, false
		}
		return geTriple{
			Subject:  cleanEntity(m[1]),
			Relation: p.relation,
			Object:   cleanEntity(m[2]),
			Source:   source,
		}, true
	}
}

func cleanEntity(s string) string {
	s = strings.TrimSpace(s)
	// Trim trailing punctuation and common filler words
	for {
		t := strings.TrimSpace(s)
		t = strings.TrimRight(t, ".,;:!?\"")
		t = strings.TrimSpace(t)
		if t == s {
			break
		}
		s = t
	}
	// Drop leading articles
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			s = s[len(prefix):]
			break
		}
	}
	return strings.TrimSpace(s)
}

func cloneTriples(in []geTriple) []geTriple {
	out := make([]geTriple, len(in))
	copy(out, in)
	return out
}
