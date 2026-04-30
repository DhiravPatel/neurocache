package searchmod

import (
	"sort"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// ftHybrid implements FT.HYBRID — a single command that runs a sparse
// (BM25 text) query and a dense (vector KNN) query against the same
// index, then blends the two scores into one ranking.
//
// Wire shape:
//
//   FT.HYBRID index "<text query>" KNN k @field $param
//             [WEIGHTS sparse_w dense_w]
//             [NORMALIZE rrf|minmax|none]
//             [LIMIT off n]
//             [PARAMS n k v ...]
//             [DIALECT n]
//             [RETURN n field [field ...]]
//             [WITHSCORES]
//
// Defaults:
//   WEIGHTS 0.5 0.5
//   NORMALIZE rrf  — Reciprocal Rank Fusion (rank-based, no
//                    cross-modality scale problems)
//   LIMIT 0 10
//
// Why a single command rather than two FT.SEARCH calls + client-side
// blend: doing it server-side avoids two round-trips and lets the
// blend operate on the full result sets (clients usually only see
// LIMIT'd subsets, which biases the merge).
func ftHybrid(c *modules.Ctx, args []string) error {
	if len(args) < 5 {
		c.Reply.Error("FT.HYBRID index query KNN k @field $param [opts ...]")
		return nil
	}
	indexName, queryStr := args[0], args[1]
	idx, ok := resolveIndex(indexName)
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	if !strings.EqualFold(args[2], "KNN") {
		c.Reply.Error("FT.HYBRID expects KNN clause after the text query")
		return nil
	}
	if len(args) < 6 {
		c.Reply.Error("FT.HYBRID KNN k @field $param required")
		return nil
	}
	k, err := strconv.Atoi(args[3])
	if err != nil || k <= 0 {
		c.Reply.Error("KNN k must be a positive integer")
		return nil
	}
	field := strings.TrimPrefix(args[4], "@")
	paramName := strings.TrimPrefix(args[5], "$")

	sparseW, denseW := 0.5, 0.5
	normalize := "rrf"
	limitOff, limitCount := 0, 10
	params := map[string]string{}
	withScores := false
	var returnFields []string

	for i := 6; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "WEIGHTS":
			if i+2 >= len(args) {
				c.Reply.Error("WEIGHTS sparse dense")
				return nil
			}
			sw, err1 := strconv.ParseFloat(args[i+1], 64)
			dw, err2 := strconv.ParseFloat(args[i+2], 64)
			if err1 != nil || err2 != nil {
				c.Reply.Error("WEIGHTS must be two floats")
				return nil
			}
			sparseW, denseW = sw, dw
			i += 2
		case "NORMALIZE":
			if i+1 >= len(args) {
				c.Reply.Error("NORMALIZE rrf|minmax|none")
				return nil
			}
			n := strings.ToLower(args[i+1])
			if n != "rrf" && n != "minmax" && n != "none" {
				c.Reply.Error("NORMALIZE must be rrf | minmax | none")
				return nil
			}
			normalize = n
			i++
		case "LIMIT":
			if i+2 >= len(args) {
				c.Reply.Error("LIMIT offset count")
				return nil
			}
			limitOff, _ = strconv.Atoi(args[i+1])
			limitCount, _ = strconv.Atoi(args[i+2])
			i += 2
		case "PARAMS":
			if i+1 >= len(args) {
				c.Reply.Error("PARAMS n k v ...")
				return nil
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+1+n > len(args) {
				c.Reply.Error("PARAMS: too few args")
				return nil
			}
			for j := i + 2; j+1 <= i+1+n; j += 2 {
				params[args[j]] = args[j+1]
			}
			i += 1 + n
		case "DIALECT":
			if i+1 < len(args) {
				i++ // accepted, ignored — we have one dialect
			}
		case "WITHSCORES":
			withScores = true
		case "RETURN":
			if i+1 >= len(args) {
				c.Reply.Error("RETURN n field [...]")
				return nil
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+2+n > len(args) {
				c.Reply.Error("RETURN: too few field names")
				return nil
			}
			returnFields = append(returnFields, args[i+2:i+2+n]...)
			i += 1 + n
		}
	}

	// Sparse leg — parse and run the text query like FT.SEARCH would.
	sparseHits := []SearchHit{}
	if queryStr != "" && queryStr != "*" {
		q, err := ParseQuery(queryStr)
		if err != nil {
			c.Reply.Error("Syntax error in text query: " + err.Error())
			return nil
		}
		sparseHits = idx.SearchWithParams(q, params)
	}

	// Dense leg — pull the vector parameter and run KNN.
	denseHits := []SearchHit{}
	if vec, ok := params[paramName]; ok {
		vi := idx.VectorIndex(field)
		if vi == nil {
			c.Reply.Error("vector field '" + field + "' not present in schema")
			return nil
		}
		query, err := parseVector(vec, vi.dim)
		if err != nil {
			c.Reply.Error("invalid vector parameter: " + err.Error())
			return nil
		}
		results := vi.KNN(query, k)
		for _, r := range results {
			doc, ok := idx.Doc(r.DocID)
			if !ok {
				continue
			}
			// Convert distance → similarity (higher = better) so the
			// blender treats both legs in the same direction.
			denseHits = append(denseHits, SearchHit{
				DocID: r.DocID,
				Score: 1 / (1 + r.Distance),
				Doc:   doc,
			})
		}
	}

	merged := blendHybridScores(sparseHits, denseHits, sparseW, denseW, normalize)

	end := limitOff + limitCount
	if limitOff > len(merged) {
		limitOff = len(merged)
	}
	if end > len(merged) {
		end = len(merged)
	}
	page := merged[limitOff:end]

	out := []any{int64(len(merged))}
	for _, h := range page {
		out = append(out, h.DocID)
		if withScores {
			out = append(out, strconv.FormatFloat(h.Score, 'f', -1, 64))
		}
		out = append(out, docFieldsAsArray(h.Doc, returnFields))
	}
	c.Reply.Array(out)
	return nil
}

// blendHybridScores fuses the sparse + dense lists into a single
// ranking. The normalize parameter chooses the strategy:
//
//   rrf     — Reciprocal Rank Fusion (Cormack et al. 2009). Rank-based,
//             so the two legs don't need to share a score scale. The
//             standard rank-fusion default for hybrid retrieval.
//   minmax  — Min/max normalize each leg's scores to [0, 1] then take
//             the weighted sum. Sensitive to outliers but preserves
//             the magnitude relationship within each leg.
//   none    — Weighted sum on the raw scores. Use when both legs are
//             already on a comparable scale.
func blendHybridScores(sparse, dense []SearchHit, sparseW, denseW float64, normalize string) []SearchHit {
	scores := map[string]float64{}
	docs := map[string]*Document{}

	switch normalize {
	case "rrf":
		// RRF constant — 60 is the value the original paper recommends
		// and what most production hybrid stacks ship with.
		const rrfK = 60.0
		for rank, h := range sparse {
			scores[h.DocID] += sparseW * (1.0 / (rrfK + float64(rank+1)))
			docs[h.DocID] = h.Doc
		}
		for rank, h := range dense {
			scores[h.DocID] += denseW * (1.0 / (rrfK + float64(rank+1)))
			if _, has := docs[h.DocID]; !has {
				docs[h.DocID] = h.Doc
			}
		}
	case "minmax":
		sparseN := minMaxNormalize(sparse)
		denseN := minMaxNormalize(dense)
		for id, s := range sparseN {
			scores[id] += sparseW * s
		}
		for id, s := range denseN {
			scores[id] += denseW * s
		}
		for _, h := range sparse {
			docs[h.DocID] = h.Doc
		}
		for _, h := range dense {
			if _, has := docs[h.DocID]; !has {
				docs[h.DocID] = h.Doc
			}
		}
	default: // "none"
		for _, h := range sparse {
			scores[h.DocID] += sparseW * h.Score
			docs[h.DocID] = h.Doc
		}
		for _, h := range dense {
			scores[h.DocID] += denseW * h.Score
			if _, has := docs[h.DocID]; !has {
				docs[h.DocID] = h.Doc
			}
		}
	}

	out := make([]SearchHit, 0, len(scores))
	for id, s := range scores {
		out = append(out, SearchHit{DocID: id, Score: s, Doc: docs[id]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].DocID < out[j].DocID
	})
	return out
}

// minMaxNormalize maps each hit's score into [0, 1] using the leg's
// observed min and max. Empty input or zero range collapses to all-zero.
func minMaxNormalize(hits []SearchHit) map[string]float64 {
	out := map[string]float64{}
	if len(hits) == 0 {
		return out
	}
	min, max := hits[0].Score, hits[0].Score
	for _, h := range hits {
		if h.Score < min {
			min = h.Score
		}
		if h.Score > max {
			max = h.Score
		}
	}
	span := max - min
	if span == 0 {
		for _, h := range hits {
			out[h.DocID] = 0
		}
		return out
	}
	for _, h := range hits {
		out[h.DocID] = (h.Score - min) / span
	}
	return out
}
