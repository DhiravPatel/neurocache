package aiops

import (
	"sync"
)

// Graph is a lightweight knowledge graph keyed on (subject, predicate,
// object) triples. Designed for the agentic-app use case: tracking
// entities a user mentioned, the relationships between them, and
// answering "what does the agent know about X?".
//
// Not a full Cypher engine — we deliberately keep the surface small:
// LINK, UNLINK, NEIGHBORS (one-hop), PATH (BFS-bounded), SUBJECTS,
// OBJECTS, COUNT. Anything more sophisticated belongs in a dedicated
// graph DB; this is the 90% case of agentic memory.
type Graph struct {
	mu    sync.RWMutex
	out   map[string]map[string]map[string]bool // subject → predicate → set of objects
	in    map[string]map[string]map[string]bool // object  → predicate → set of subjects
	count int
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{
		out: map[string]map[string]map[string]bool{},
		in:  map[string]map[string]map[string]bool{},
	}
}

// Link adds (subject, predicate, object). Idempotent — duplicates are
// silently ignored. Returns true when this is a new edge.
func (g *Graph) Link(subject, predicate, object string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.out[subject]; !ok {
		g.out[subject] = map[string]map[string]bool{}
	}
	if _, ok := g.out[subject][predicate]; !ok {
		g.out[subject][predicate] = map[string]bool{}
	}
	if g.out[subject][predicate][object] {
		return false
	}
	g.out[subject][predicate][object] = true
	if _, ok := g.in[object]; !ok {
		g.in[object] = map[string]map[string]bool{}
	}
	if _, ok := g.in[object][predicate]; !ok {
		g.in[object][predicate] = map[string]bool{}
	}
	g.in[object][predicate][subject] = true
	g.count++
	return true
}

// Unlink removes a single edge. Returns true if it existed.
func (g *Graph) Unlink(subject, predicate, object string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.out[subject] == nil || g.out[subject][predicate] == nil {
		return false
	}
	if !g.out[subject][predicate][object] {
		return false
	}
	delete(g.out[subject][predicate], object)
	if len(g.out[subject][predicate]) == 0 {
		delete(g.out[subject], predicate)
	}
	if len(g.out[subject]) == 0 {
		delete(g.out, subject)
	}
	delete(g.in[object][predicate], subject)
	if len(g.in[object][predicate]) == 0 {
		delete(g.in[object], predicate)
	}
	if len(g.in[object]) == 0 {
		delete(g.in, object)
	}
	g.count--
	return true
}

// Neighbor is one outgoing edge.
type Neighbor struct {
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// Neighbors returns every outgoing edge from `subject`. Filter by
// predicate (empty = all).
func (g *Graph) Neighbors(subject, predicate string) []Neighbor {
	g.mu.RLock()
	defer g.mu.RUnlock()
	preds, ok := g.out[subject]
	if !ok {
		return nil
	}
	out := []Neighbor{}
	for p, objs := range preds {
		if predicate != "" && predicate != p {
			continue
		}
		for o := range objs {
			out = append(out, Neighbor{Predicate: p, Object: o})
		}
	}
	return out
}

// In returns the inbound edges to an `object` — "who points at me?".
// Useful for "every person who works at this company".
func (g *Graph) In(object, predicate string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	preds, ok := g.in[object]
	if !ok {
		return nil
	}
	subj := map[string]bool{}
	for p, ss := range preds {
		if predicate != "" && predicate != p {
			continue
		}
		for s := range ss {
			subj[s] = true
		}
	}
	out := make([]string, 0, len(subj))
	for s := range subj {
		out = append(out, s)
	}
	return out
}

// Path returns the shortest predicate chain from `from` to `to` via
// BFS, capped at maxDepth hops. Returns nil + false on no path.
// `predicateFilter` (when non-empty) restricts traversal to that one
// predicate — useful for "all colleagues of X" via WORKS_AT chains.
func (g *Graph) Path(from, to string, maxDepth int, predicateFilter string) ([]Neighbor, bool) {
	if from == to {
		return []Neighbor{}, true
	}
	if maxDepth <= 0 {
		maxDepth = 6
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	type qNode struct {
		node string
		path []Neighbor
	}
	queue := []qNode{{node: from}}
	seen := map[string]bool{from: true}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if len(cur.path) >= maxDepth {
			continue
		}
		preds, ok := g.out[cur.node]
		if !ok {
			continue
		}
		for p, objs := range preds {
			if predicateFilter != "" && p != predicateFilter {
				continue
			}
			for o := range objs {
				if seen[o] {
					continue
				}
				newPath := append([]Neighbor{}, cur.path...)
				newPath = append(newPath, Neighbor{Predicate: p, Object: o})
				if o == to {
					return newPath, true
				}
				seen[o] = true
				queue = append(queue, qNode{node: o, path: newPath})
			}
		}
	}
	return nil, false
}

// Subjects returns every node that has at least one outgoing edge.
func (g *Graph) Subjects() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.out))
	for k := range g.out {
		out = append(out, k)
	}
	return out
}

// GraphStats snapshots state.
type GraphStats struct {
	Subjects int `json:"subjects"`
	Objects  int `json:"objects"`
	Edges    int `json:"edges"`
}

// Stats returns a snapshot.
func (g *Graph) Stats() GraphStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return GraphStats{
		Subjects: len(g.out),
		Objects:  len(g.in),
		Edges:    g.count,
	}
}
