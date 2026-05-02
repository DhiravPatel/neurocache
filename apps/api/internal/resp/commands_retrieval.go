package resp

// Hybrid retrieval and GraphRAG command handlers. Backed by
// `internal/retrieval` (BM25 + vector + RRF fusion) and
// `aiops.Graph` for the graph-expansion arm. Writes (CREATE, ADD,
// DROP, DEL) flow through the engine's RecordWrite for AOF +
// replication parity.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/retrieval"
)

// retrieveCmd dispatches the RETRIEVE.<sub> family. Subcommands:
//
//	RETRIEVE.CREATE name [DIM n] [K1 f] [B f] [HNSW 0|1]
//	RETRIEVE.DROP name
//	RETRIEVE.LIST
//	RETRIEVE.STATS name
//	RETRIEVE.ADD name id text [META k v ...]
//	RETRIEVE.DEL name id
//	RETRIEVE.GET name id
//	RETRIEVE.QUERY name query [K n] [ALPHA f] [BM25 0|1] [VECTOR 0|1]
//
// QUERY returns an array of hits, each itself a flat array
//   [id, score, text, bm25_rank, vector_rank, "meta", k, v, ...]
// so RESP2 clients can consume without out-of-band schema.
func (c *conn) retrieveCmd(sub string, args []string) {
	switch sub {
	case "CREATE":
		if len(args) < 1 {
			writeError(c.bw, "RETRIEVE.CREATE name [DIM n] [K1 f] [B f] [HNSW 0|1]")
			return
		}
		opts := retrieval.Options{HNSW: true}
		for i := 1; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "DIM":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.Dim = n
				}
			case "K1":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.K1 = f
				}
			case "B":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.B = f
				}
			case "HNSW":
				opts.HNSW = args[i+1] != "0" && strings.ToLower(args[i+1]) != "false"
			}
		}
		if _, err := c.eng.Retrieval.Create(args[0], opts); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("RETRIEVE.CREATE", args)
		writeSimple(c.bw, "OK")
	case "DROP":
		if len(args) != 1 {
			writeError(c.bw, "RETRIEVE.DROP name")
			return
		}
		ok := c.eng.Retrieval.Drop(args[0])
		if ok {
			c.eng.RecordWrite("RETRIEVE.DROP", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "LIST":
		writeArray(c.bw, c.eng.Retrieval.Names())
	case "STATS":
		if len(args) != 1 {
			writeError(c.bw, "RETRIEVE.STATS name")
			return
		}
		ix, ok := c.eng.Retrieval.Get(args[0])
		if !ok {
			writeError(c.bw, "no such retrieval index")
			return
		}
		st := ix.Stats()
		writeValue(c.bw, []any{
			"documents", int64(st.Documents),
			"terms", int64(st.Terms),
			"total_length", st.TotalLen,
			"avg_length", st.AvgLen,
		})
	case "ADD":
		if len(args) < 3 {
			writeError(c.bw, "RETRIEVE.ADD name id text [META k v ...]")
			return
		}
		ix := c.eng.Retrieval.GetOrCreate(args[0])
		var meta map[string]string
		if len(args) > 3 {
			if strings.ToUpper(args[3]) != "META" {
				writeError(c.bw, "expected META")
				return
			}
			rest := args[4:]
			if len(rest)%2 != 0 {
				writeError(c.bw, "META expects even number of k/v")
				return
			}
			meta = make(map[string]string, len(rest)/2)
			for i := 0; i < len(rest); i += 2 {
				meta[rest[i]] = rest[i+1]
			}
		}
		if err := ix.Add(retrieval.Document{ID: args[1], Text: args[2], Metadata: meta}); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RecordWrite("RETRIEVE.ADD", args)
		writeSimple(c.bw, "OK")
	case "DEL":
		if len(args) != 2 {
			writeError(c.bw, "RETRIEVE.DEL name id")
			return
		}
		ix, ok := c.eng.Retrieval.Get(args[0])
		if !ok {
			writeInt(c.bw, 0)
			return
		}
		if ix.Delete(args[1]) {
			c.eng.RecordWrite("RETRIEVE.DEL", args)
			writeInt(c.bw, 1)
			return
		}
		writeInt(c.bw, 0)
	case "GET":
		if len(args) != 2 {
			writeError(c.bw, "RETRIEVE.GET name id")
			return
		}
		ix, ok := c.eng.Retrieval.Get(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		d, ok := ix.Get(args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		body, _ := json.Marshal(d)
		writeBulk(c.bw, string(body))
	case "QUERY":
		if len(args) < 2 {
			writeError(c.bw, "RETRIEVE.QUERY name query [K n] [ALPHA f] [BM25 0|1] [VECTOR 0|1]")
			return
		}
		ix, ok := c.eng.Retrieval.Get(args[0])
		if !ok {
			writeError(c.bw, "no such retrieval index")
			return
		}
		opts := retrieval.QueryOptions{K: 10, Alpha: 0.5}
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToUpper(args[i]) {
			case "K":
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					opts.K = n
				}
			case "ALPHA":
				if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					opts.Alpha = f
				}
			case "BM25":
				opts.UseBM25 = args[i+1] != "0" && strings.ToLower(args[i+1]) != "false"
			case "VECTOR":
				opts.UseVect = args[i+1] != "0" && strings.ToLower(args[i+1]) != "false"
			}
		}
		hits := ix.Query(args[1], opts)
		writeRetrievalHits(c, hits)
	default:
		writeError(c.bw, "Unknown RETRIEVE subcommand "+sub)
	}
}

// writeRetrievalHits emits a uniform RESP shape for every retrieval
// reply so RAG.QUERY and RETRIEVE.QUERY share one decoder client-side.
func writeRetrievalHits(c *conn, hits []retrieval.Hit) {
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		entry := []any{
			"id", h.ID,
			"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
			"text", h.Text,
			"bm25_rank", int64(h.BM25Rank),
			"vector_rank", int64(h.VectorRank),
			"bm25_score", strconv.FormatFloat(h.BM25Score, 'f', 4, 64),
			"vector_dist", strconv.FormatFloat(h.VectorDist, 'f', 4, 64),
		}
		if len(h.Metadata) > 0 {
			meta := make([]any, 0, len(h.Metadata)*2)
			for k, v := range h.Metadata {
				meta = append(meta, k, v)
			}
			entry = append(entry, "meta", meta)
		}
		out = append(out, entry)
	}
	writeValue(c.bw, out)
}

// ─── RAG.QUERY (GraphRAG) ─────────────────────────────────────────

// ragQueryCmd implements GraphRAG: hybrid retrieval, then expand each
// top hit's metadata-attached entity through the knowledge graph by N
// hops, returning both the original hits AND expanded context. This
// is the one-call command Redis genuinely cannot match.
//
//	RAG.QUERY index query [K n] [HOPS n] [ALPHA f] [PREDICATE p]
//	          [ENTITY_KEY key]
//
// Documents added with `META entity <subject>` get their `entity`
// metadata used as a graph anchor; for every top hit we walk
// outgoing edges (optionally filtered by predicate) up to HOPS deep
// and emit each visited triple as a `context` row in the reply.
//
// Reply shape:
//
//	[
//	  "hits",   [...retrieval hits, same shape as RETRIEVE.QUERY...],
//	  "context",[
//	     [subject, predicate, object, depth, source_doc_id],
//	     ...
//	  ]
//	]
func (c *conn) ragQueryCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "RAG.QUERY index query [K n] [HOPS n] [ALPHA f] [PREDICATE p] [ENTITY_KEY key]")
		return
	}
	ix, ok := c.eng.Retrieval.Get(args[0])
	if !ok {
		writeError(c.bw, "no such retrieval index")
		return
	}
	opts := retrieval.QueryOptions{K: 5, Alpha: 0.5}
	hops := 1
	predicate := ""
	entityKey := "entity"
	for i := 2; i+1 < len(args); i += 2 {
		switch strings.ToUpper(args[i]) {
		case "K":
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				opts.K = n
			}
		case "HOPS":
			if n, err := strconv.Atoi(args[i+1]); err == nil && n >= 0 {
				hops = n
			}
		case "ALPHA":
			if f, err := strconv.ParseFloat(args[i+1], 64); err == nil {
				opts.Alpha = f
			}
		case "PREDICATE":
			predicate = args[i+1]
		case "ENTITY_KEY":
			entityKey = args[i+1]
		}
	}
	hits := ix.Query(args[1], opts)
	context := graphExpand(c.eng.Graph, hits, hops, predicate, entityKey)

	hitArr := make([]any, 0, len(hits))
	for _, h := range hits {
		entry := []any{
			"id", h.ID,
			"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
			"text", h.Text,
			"bm25_rank", int64(h.BM25Rank),
			"vector_rank", int64(h.VectorRank),
		}
		if len(h.Metadata) > 0 {
			meta := make([]any, 0, len(h.Metadata)*2)
			for k, v := range h.Metadata {
				meta = append(meta, k, v)
			}
			entry = append(entry, "meta", meta)
		}
		hitArr = append(hitArr, entry)
	}
	ctxArr := make([]any, 0, len(context))
	for _, t := range context {
		ctxArr = append(ctxArr, []any{
			"subject", t.Subject,
			"predicate", t.Predicate,
			"object", t.Object,
			"depth", int64(t.Depth),
			"source_doc", t.SourceDoc,
		})
	}
	writeValue(c.bw, []any{
		"hits", hitArr,
		"context", ctxArr,
	})
}

// expandedTriple is one triple emitted by graph expansion, annotated
// with the depth at which it was discovered and the retrieval hit
// that anchored it. Depth=1 means a direct neighbour of an anchor;
// depth=2 means a neighbour-of-neighbour.
type expandedTriple struct {
	Subject   string
	Predicate string
	Object    string
	Depth     int
	SourceDoc string
}

// graphExpand walks each hit's `entity` metadata anchor outward
// through the knowledge graph up to maxHops, deduping triples we've
// already emitted. Hits without an anchor are silently skipped — the
// retrieval result still appears in `hits`.
func graphExpand(g *aiops.Graph, hits []retrieval.Hit, maxHops int, predicate, entityKey string) []expandedTriple {
	if g == nil || maxHops <= 0 {
		return nil
	}
	seen := map[string]bool{}
	out := []expandedTriple{}
	for _, h := range hits {
		anchor, ok := h.Metadata[entityKey]
		if !ok {
			continue
		}
		// BFS from anchor.
		type qNode struct {
			node  string
			depth int
		}
		queue := []qNode{{node: anchor, depth: 0}}
		visited := map[string]bool{anchor: true}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if cur.depth >= maxHops {
				continue
			}
			for _, n := range g.Neighbors(cur.node, predicate) {
				key := cur.node + "\x00" + n.Predicate + "\x00" + n.Object
				if !seen[key] {
					seen[key] = true
					out = append(out, expandedTriple{
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
	return out
}
