package http

// HTTP handlers for hybrid retrieval (BM25 + vector + RRF), GraphRAG,
// and the layered memory family. These mirror the RESP commands so the
// dashboard and external HTTP clients have first-class access without
// going through the RESP wire.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/retrieval"
)

// ─── retrieval ───

type retrieveCreateReq struct {
	Name string  `json:"name"`
	Dim  int     `json:"dim,omitempty"`
	K1   float64 `json:"k1,omitempty"`
	B    float64 `json:"b,omitempty"`
	HNSW bool    `json:"hnsw,omitempty"`
}

func (h *handlers) retrieveCreate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req retrieveCreateReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Name == "" {
		writeErr(w, 400, "name required")
		return
	}
	if _, err := h.eng.Retrieval.Create(req.Name, retrieval.Options{
		Dim: req.Dim, K1: req.K1, B: req.B, HNSW: req.HNSW,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("RETRIEVE.CREATE", []string{req.Name})
	writeJSON(w, 201, map[string]string{"status": "created", "name": req.Name})
	h.record("RETRIEVE.CREATE", start)
}

func (h *handlers) retrieveDrop(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	if !h.eng.Retrieval.Drop(name) {
		writeErr(w, 404, "no such index")
		return
	}
	h.eng.RecordWrite("RETRIEVE.DROP", []string{name})
	w.WriteHeader(204)
	h.record("RETRIEVE.DROP", start)
}

func (h *handlers) retrieveList(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	writeJSON(w, 200, map[string]any{"indexes": h.eng.Retrieval.Names()})
	h.record("RETRIEVE.LIST", start)
}

func (h *handlers) retrieveStats(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	ix, ok := h.eng.Retrieval.Get(name)
	if !ok {
		writeErr(w, 404, "no such index")
		return
	}
	writeJSON(w, 200, ix.Stats())
	h.record("RETRIEVE.STATS", start)
}

type retrieveAddReq struct {
	ID       string            `json:"id"`
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (h *handlers) retrieveAdd(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	var req retrieveAddReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.ID == "" || req.Text == "" {
		writeErr(w, 400, "id and text required")
		return
	}
	ix := h.eng.Retrieval.GetOrCreate(name)
	if err := ix.Add(retrieval.Document{ID: req.ID, Text: req.Text, Metadata: req.Metadata}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("RETRIEVE.ADD", []string{name, req.ID, req.Text})
	writeJSON(w, 201, map[string]string{"id": req.ID})
	h.record("RETRIEVE.ADD", start)
}

func (h *handlers) retrieveDel(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	id := r.PathValue("id")
	ix, ok := h.eng.Retrieval.Get(name)
	if !ok {
		writeErr(w, 404, "no such index")
		return
	}
	if !ix.Delete(id) {
		writeErr(w, 404, "no such document")
		return
	}
	h.eng.RecordWrite("RETRIEVE.DEL", []string{name, id})
	w.WriteHeader(204)
	h.record("RETRIEVE.DEL", start)
}

func (h *handlers) retrieveQuery(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	ix, ok := h.eng.Retrieval.Get(name)
	if !ok {
		writeErr(w, 404, "no such index")
		return
	}
	q := r.URL.Query()
	opts := retrieval.QueryOptions{K: 10, Alpha: 0.5}
	if v := q.Get("q"); v != "" {
		_ = v // q is the query text below
	}
	queryText := q.Get("q")
	if queryText == "" {
		writeErr(w, 400, "q parameter required")
		return
	}
	if v := q.Get("k"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.K = n
		}
	}
	if v := q.Get("alpha"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			opts.Alpha = f
		}
	}
	if v := q.Get("bm25"); v == "1" || v == "true" {
		opts.UseBM25 = true
	}
	if v := q.Get("vector"); v == "1" || v == "true" {
		opts.UseVect = true
	}
	hits := ix.Query(queryText, opts)
	writeJSON(w, 200, map[string]any{"hits": hits})
	h.record("RETRIEVE.QUERY", start)
}

// ─── RAG.QUERY (GraphRAG) ───

func (h *handlers) ragQuery(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	ix, ok := h.eng.Retrieval.Get(name)
	if !ok {
		writeErr(w, 404, "no such index")
		return
	}
	q := r.URL.Query()
	queryText := q.Get("q")
	if queryText == "" {
		writeErr(w, 400, "q parameter required")
		return
	}
	opts := retrieval.QueryOptions{K: 5, Alpha: 0.5}
	hops := 1
	predicate := q.Get("predicate")
	if v := q.Get("k"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.K = n
		}
	}
	if v := q.Get("hops"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			hops = n
		}
	}
	if v := q.Get("alpha"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			opts.Alpha = f
		}
	}
	hits := ix.Query(queryText, opts)
	context := expandContext(h, hits, hops, predicate)
	writeJSON(w, 200, map[string]any{
		"hits":    hits,
		"context": context,
	})
	h.record("RAG.QUERY", start)
}

func expandContext(h *handlers, hits []retrieval.Hit, hops int, predicate string) []aiops.RAGContextRow {
	if hops <= 0 || h.eng.Graph == nil {
		return nil
	}
	out := []aiops.RAGContextRow{}
	seen := map[string]bool{}
	for _, hit := range hits {
		anchor, ok := hit.Metadata["entity"]
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
			for _, n := range h.eng.Graph.Neighbors(cur.node, predicate) {
				key := cur.node + "\x00" + n.Predicate + "\x00" + n.Object
				if !seen[key] {
					seen[key] = true
					out = append(out, aiops.RAGContextRow{
						Subject:   cur.node,
						Predicate: n.Predicate,
						Object:    n.Object,
						Depth:     cur.depth + 1,
						SourceDoc: hit.ID,
					})
				}
				if !visited[n.Object] {
					visited[n.Object] = true
					queue = append(queue, qNode{node: n.Object, depth: cur.depth + 1})
				}
			}
		}
	}
	return out
}

// ─── memory layers ───

type memoryLayerAddReq struct {
	Text           string            `json:"text"`
	Layer          string            `json:"layer,omitempty"`
	Importance     float64           `json:"importance,omitempty"`
	DedupThreshold float64           `json:"dedup_threshold,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

func (h *handlers) memoryLayerAdd(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user := r.PathValue("user")
	var req memoryLayerAddReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Text == "" {
		writeErr(w, 400, "text required")
		return
	}
	layer := memory.Layer(req.Layer)
	if layer == "" {
		layer = memory.LayerEpisodic
	}
	if !layer.IsValid() {
		writeErr(w, 400, "layer must be episodic|semantic|procedural")
		return
	}
	e, isNew, err := h.eng.Memory.AddWithOptions(user, req.Text, memory.AddOptions{
		Layer: layer, Importance: req.Importance,
		DedupThreshold: req.DedupThreshold, Meta: req.Metadata,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("MEMORY.ADD", []string{user, req.Text, "LAYER", string(layer)})
	writeJSON(w, 201, map[string]any{
		"id": e.ID, "new": isNew, "layer": string(e.Layer),
	})
	h.record("MEMORY.ADD", start)
}

func (h *handlers) memoryLayerQuery(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user := r.PathValue("user")
	q := r.URL.Query()
	queryText := q.Get("q")
	if queryText == "" {
		writeErr(w, 400, "q parameter required")
		return
	}
	opts := memory.LayerQueryOptions{K: 5, Threshold: 0.2}
	if v := q.Get("layer"); v != "" {
		opts.Layer = memory.Layer(v)
	}
	if v := q.Get("k"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.K = n
		}
	}
	if v := q.Get("threshold"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			opts.Threshold = float32(f)
		}
	}
	if v := q.Get("recency"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			opts.RecencyBias = f
		}
	}
	if q.Get("touch") == "1" {
		opts.TouchHits = true
	}
	hits := h.eng.Memory.QueryLayered(user, queryText, opts)
	writeJSON(w, 200, map[string]any{"hits": hits})
	h.record("MEMORY.QUERY", start)
}

func (h *handlers) memoryLayerStats(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user := r.PathValue("user")
	writeJSON(w, 200, h.eng.Memory.LayerStats(user))
	h.record("MEMORY.STATS", start)
}

type memoryDecayReq struct {
	Layer       string `json:"layer,omitempty"`
	HalfLife    int    `json:"half_life_seconds,omitempty"`
	MaxAge      int    `json:"max_age_seconds,omitempty"`
	Untouched   int    `json:"untouched_for_seconds,omitempty"`
	MinScore    float64 `json:"min_score,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
}

func (h *handlers) memoryDecay(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user := r.PathValue("user")
	var req memoryDecayReq
	_ = readJSON(r, &req)
	opts := memory.DecayOptions{
		Layer:        memory.Layer(req.Layer),
		HalfLife:     time.Duration(req.HalfLife) * time.Second,
		MaxAge:       time.Duration(req.MaxAge) * time.Second,
		UntouchedFor: time.Duration(req.Untouched) * time.Second,
		MinScore:     req.MinScore,
		DryRun:       req.DryRun,
	}
	if opts.Layer == "" {
		opts.Layer = memory.LayerEpisodic
	}
	res := h.eng.Memory.Decay(user, opts)
	if !opts.DryRun {
		h.eng.RecordWrite("MEMORY.DECAY", []string{user})
	}
	writeJSON(w, 200, res)
	h.record("MEMORY.DECAY", start)
}

type memoryConsolidateReq struct {
	Threshold  float64 `json:"threshold,omitempty"`
	MinSize    int     `json:"min_size,omitempty"`
	Drop       bool    `json:"drop,omitempty"`
	Importance float64 `json:"importance,omitempty"`
}

func (h *handlers) memoryConsolidate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user := r.PathValue("user")
	var req memoryConsolidateReq
	_ = readJSON(r, &req)
	res := h.eng.Memory.Consolidate(memory.ConsolidateOptions{
		UserID:     user,
		Threshold:  req.Threshold,
		MinSize:    req.MinSize,
		Drop:       req.Drop,
		Importance: req.Importance,
	})
	h.eng.RecordWrite("MEMORY.CONSOLIDATE", []string{user})
	writeJSON(w, 200, res)
	h.record("MEMORY.CONSOLIDATE", start)
}
