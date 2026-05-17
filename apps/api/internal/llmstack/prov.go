package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Provenance records "why did the system say this" as a queryable
// DAG. CITE, GROUND, EXTRACT.TRACE, and REPLAY each capture one slice
// of provenance — but no one structure answers, for a single
// delivered answer: which query rewrite → which retrieved chunks →
// which doc versions → which prompt version → which model → which
// guardrails fired.
//
// Every regulated buyer (finance, health, legal) requires this graph
// and ends up rebuilding it badly. PROV.* makes it a first-class
// primitive — small, fast, and queryable both forwards ("WHY did we
// say this?") and backwards ("which answers depended on this source?").
//
// Data model: a per-answer DAG of nodes. Each node has a kind
// (query/rewrite/chunk/llm/answer/...), a label (free text), and
// zero-or-more inbound edges (FROM other nodes in the same answer)
// plus zero-or-more source refs (arbitrary IDs — doc:44@v3,
// prompt:v5, source:blog-x — that connect cross-answer).
//
// Commands:
//
//   PROV.BEGIN answer-id [META k v ...]
//   PROV.NODE answer-id node-id KIND k label [FROM n1 n2 ...] [REFS r1 r2 ...]
//        Edges are validated: FROM nodes must already exist in the same answer.
//   PROV.WHY answer-id node-id [DEPTH n]
//        Full lineage path ending at node-id (defaults to the answer node
//        if node-id is empty or matches the answer's leaf).
//   PROV.IMPACT ref            — every answer that ever referenced this source ref
//   PROV.ANSWER answer-id      — full DAG dump
//   PROV.LIST [LIMIT n]        — most recent answers
//   PROV.FORGET answer-id|ALL  — drop one or all
//   PROV.STATS
//
// The hot path:
//
//   BEGIN: O(1) map insert.
//   NODE:  O(in-degree) — validate each FROM exists in the same answer.
//          Each REF is also indexed in a global ref→answer-set so
//          IMPACT is O(answers-touching-ref).
//   WHY:   reverse BFS from the target node up the edges. Bounded
//          by depth (default 64 — deep enough for any realistic chain).
//   IMPACT: direct read of the ref→answer set.
type Provenance struct {
	mu      sync.RWMutex
	answers map[string]*provAnswer
	refIdx  map[string]map[string]bool // ref → set of answer-ids

	totalBegins atomic.Int64
	totalNodes  atomic.Int64
	totalWhys   atomic.Int64
	totalImpacts atomic.Int64
}

type provAnswer struct {
	mu        sync.RWMutex
	id        string
	meta      map[string]string
	nodes     map[string]*provNode
	order     []string // insertion order
	createdAt time.Time
}

type provNode struct {
	ID    string
	Kind  string
	Label string
	From  []string
	Refs  []string
	At    time.Time
}

// NewProvenance returns an empty store.
func NewProvenance() *Provenance {
	return &Provenance{
		answers: map[string]*provAnswer{},
		refIdx:  map[string]map[string]bool{},
	}
}

// Begin opens a new answer DAG. Re-opening an existing answer-id is
// rejected to keep the lineage immutable.
func (p *Provenance) Begin(answerID string, meta map[string]string) error {
	if answerID == "" {
		return errors.New("answer_id required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.answers[answerID]; ok {
		return errors.New("answer already exists: " + answerID)
	}
	cp := map[string]string{}
	for k, v := range meta {
		cp[k] = v
	}
	p.answers[answerID] = &provAnswer{
		id:        answerID,
		meta:      cp,
		nodes:     map[string]*provNode{},
		createdAt: time.Now(),
	}
	p.totalBegins.Add(1)
	return nil
}

// Node appends a node to an answer DAG. Each FROM must already exist
// in this answer. REFs are arbitrary opaque IDs used for IMPACT
// queries (e.g. "doc:44@v3", "prompt:v5", "source:blog-x").
func (p *Provenance) Node(answerID, nodeID, kind, label string, from, refs []string) error {
	if answerID == "" {
		return errors.New("answer_id required")
	}
	if nodeID == "" {
		return errors.New("node_id required")
	}
	if kind == "" {
		return errors.New("kind required")
	}
	p.mu.RLock()
	a, ok := p.answers[answerID]
	p.mu.RUnlock()
	if !ok {
		return errors.New("unknown answer_id: " + answerID)
	}
	a.mu.Lock()
	if _, exists := a.nodes[nodeID]; exists {
		a.mu.Unlock()
		return errors.New("node already exists: " + nodeID)
	}
	// Validate every FROM
	for _, f := range from {
		if f == "" {
			a.mu.Unlock()
			return errors.New("empty FROM node id")
		}
		if _, ok := a.nodes[f]; !ok {
			a.mu.Unlock()
			return errors.New("unknown FROM node: " + f)
		}
	}
	n := &provNode{
		ID:    nodeID,
		Kind:  kind,
		Label: label,
		From:  append([]string{}, from...),
		Refs:  dedupSorted(refs),
		At:    time.Now(),
	}
	a.nodes[nodeID] = n
	a.order = append(a.order, nodeID)
	a.mu.Unlock()
	// Index refs → answer (for IMPACT)
	if len(n.Refs) > 0 {
		p.mu.Lock()
		for _, r := range n.Refs {
			set, ok := p.refIdx[r]
			if !ok {
				set = map[string]bool{}
				p.refIdx[r] = set
			}
			set[answerID] = true
		}
		p.mu.Unlock()
	}
	p.totalNodes.Add(1)
	return nil
}

// ProvNodeView is the serialisable form of one node.
type ProvNodeView struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Label  string   `json:"label"`
	From   []string `json:"from,omitempty"`
	Refs   []string `json:"refs,omitempty"`
	AtUnix int64    `json:"at_unix"`
}

// Why returns the full reverse-lineage path that produced nodeID.
// The result is bottom-up: target node first, then its ancestors.
// depth=0 means default (64 hops).
func (p *Provenance) Why(answerID, nodeID string, depth int) ([]ProvNodeView, bool) {
	if answerID == "" {
		return nil, false
	}
	if depth <= 0 {
		depth = 64
	}
	p.totalWhys.Add(1)
	p.mu.RLock()
	a, ok := p.answers[answerID]
	p.mu.RUnlock()
	if !ok {
		return nil, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	// Default to the last-added node if caller didn't specify
	target := nodeID
	if target == "" {
		if len(a.order) == 0 {
			return []ProvNodeView{}, true
		}
		target = a.order[len(a.order)-1]
	}
	root, ok := a.nodes[target]
	if !ok {
		return nil, false
	}
	visited := map[string]bool{}
	out := make([]ProvNodeView, 0, 8)
	queue := []*provNode{root}
	for len(queue) > 0 && len(out) < depth {
		n := queue[0]
		queue = queue[1:]
		if visited[n.ID] {
			continue
		}
		visited[n.ID] = true
		out = append(out, viewNode(n))
		for _, f := range n.From {
			if parent, ok := a.nodes[f]; ok && !visited[parent.ID] {
				queue = append(queue, parent)
			}
		}
	}
	return out, true
}

// Impact returns every answer-id that ever referenced this opaque
// ref. The killer feature: when a source turns out to be wrong, you
// can name every answer you ever shipped that depended on it.
func (p *Provenance) Impact(ref string) []string {
	if ref == "" {
		return nil
	}
	p.totalImpacts.Add(1)
	p.mu.RLock()
	set, ok := p.refIdx[ref]
	p.mu.RUnlock()
	if !ok {
		return []string{}
	}
	p.mu.RLock()
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	p.mu.RUnlock()
	sort.Strings(out)
	return out
}

// AnswerView is the full dump of one answer.
type AnswerView struct {
	AnswerID  string            `json:"answer_id"`
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt int64             `json:"created_unix"`
	Nodes     []ProvNodeView    `json:"nodes"`
}

// Answer returns the full DAG dump (nodes in insertion order).
func (p *Provenance) Answer(answerID string) (AnswerView, bool) {
	if answerID == "" {
		return AnswerView{}, false
	}
	p.mu.RLock()
	a, ok := p.answers[answerID]
	p.mu.RUnlock()
	if !ok {
		return AnswerView{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := AnswerView{
		AnswerID:  a.id,
		Meta:      copyMetaProv(a.meta),
		CreatedAt: a.createdAt.Unix(),
		Nodes:     make([]ProvNodeView, 0, len(a.order)),
	}
	for _, id := range a.order {
		out.Nodes = append(out.Nodes, viewNode(a.nodes[id]))
	}
	return out, true
}

// AnswerSummary is one row of LIST.
type AnswerSummary struct {
	AnswerID  string `json:"answer_id"`
	Nodes     int    `json:"nodes"`
	CreatedAt int64  `json:"created_unix"`
}

// List returns the most-recent answers.
func (p *Provenance) List(limit int) []AnswerSummary {
	if limit <= 0 {
		limit = 50
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]AnswerSummary, 0, len(p.answers))
	for _, a := range p.answers {
		a.mu.RLock()
		out = append(out, AnswerSummary{
			AnswerID: a.id, Nodes: len(a.nodes), CreatedAt: a.createdAt.Unix(),
		})
		a.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Forget drops an answer. "ALL" wipes everything.
func (p *Provenance) Forget(answerID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if answerID == "ALL" {
		n := len(p.answers)
		p.answers = map[string]*provAnswer{}
		p.refIdx = map[string]map[string]bool{}
		return n
	}
	a, ok := p.answers[answerID]
	if !ok {
		return 0
	}
	// Tear down the ref index for this answer
	a.mu.RLock()
	for _, n := range a.nodes {
		for _, r := range n.Refs {
			if set, ok := p.refIdx[r]; ok {
				delete(set, answerID)
				if len(set) == 0 {
					delete(p.refIdx, r)
				}
			}
		}
	}
	a.mu.RUnlock()
	delete(p.answers, answerID)
	return 1
}

// ProvStats is the global snapshot.
type ProvStats struct {
	Answers       int   `json:"answers"`
	IndexedRefs   int   `json:"indexed_refs"`
	TotalBegins   int64 `json:"total_begins"`
	TotalNodes    int64 `json:"total_nodes"`
	TotalWhys     int64 `json:"total_whys"`
	TotalImpacts  int64 `json:"total_impacts"`
}

func (p *Provenance) Stats() ProvStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ProvStats{
		Answers:      len(p.answers),
		IndexedRefs:  len(p.refIdx),
		TotalBegins:  p.totalBegins.Load(),
		TotalNodes:   p.totalNodes.Load(),
		TotalWhys:    p.totalWhys.Load(),
		TotalImpacts: p.totalImpacts.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func viewNode(n *provNode) ProvNodeView {
	return ProvNodeView{
		ID: n.ID, Kind: n.Kind, Label: n.Label,
		From: append([]string{}, n.From...),
		Refs: append([]string{}, n.Refs...),
		AtUnix: n.At.Unix(),
	}
}

func copyMetaProv(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
