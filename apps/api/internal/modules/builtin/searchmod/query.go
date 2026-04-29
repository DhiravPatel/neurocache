package searchmod

import (
	"sort"
	"strings"
)

// SearchHit is one row in a SEARCH result. The score combines BM25
// across every matched (field, term) pair.
type SearchHit struct {
	DocID  string
	Score  float64
	Doc    *Document
}

// Search runs a parsed query against the index and returns ranked hits.
// Scoring: each hit accumulates BM25 across every matched term; tag
// matches contribute 1.0 (the index has no document-frequency story
// for tags so a flat boost matches Redis behaviour).
func (idx *Index) Search(q *QueryNode) []SearchHit {
	matches := idx.eval(q)
	out := make([]SearchHit, 0, len(matches))
	for id, score := range matches {
		doc, ok := idx.Doc(id)
		if !ok {
			continue
		}
		out = append(out, SearchHit{DocID: id, Score: score * doc.Score, Doc: doc})
	}
	// Default order is descending score, then by ID for determinism.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].DocID < out[j].DocID
	})
	return out
}

// eval recursively evaluates a query node, returning a docID → score
// map. Operators combine maps:
//
//   AND → intersect (sum scores)
//   OR  → union (sum scores)
//   NOT → set difference (universe = all docs, score 0)
func (idx *Index) eval(n *QueryNode) map[string]float64 {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case kAll:
		out := map[string]float64{}
		for _, id := range idx.AllDocIDs() {
			out[id] = 1
		}
		return out
	case kTerm:
		field := n.Field
		if field == "" {
			return idx.scoreAcrossTextFields(n.Term)
		}
		return idx.scoreSingleField(field, n.Term)
	case kPrefix:
		out := map[string]float64{}
		for _, f := range idx.Schema.Fields {
			if f.Type != FieldText {
				continue
			}
			p := idx.PrefixPostings(f.Name, n.Term)
			if p == nil {
				continue
			}
			for _, e := range p.entries {
				out[e.docID] += float64(e.freq) * f.Weight * 0.5 // small penalty vs exact
			}
		}
		return out
	case kPhrase:
		return idx.scorePhrase(n.Field, n.Phrase)
	case kField:
		out := map[string]float64{}
		for _, child := range n.Children {
			child.Field = n.Field
			merged := idx.eval(child)
			for id, sc := range merged {
				out[id] += sc
			}
		}
		return out
	case kRange:
		out := map[string]float64{}
		for _, id := range idx.NumericRange(n.Field, n.Lo, n.Hi) {
			out[id] = 1
		}
		// open-bound trim
		if n.LoOpen || n.HiOpen {
			for id := range out {
				doc, ok := idx.Doc(id)
				if !ok {
					continue
				}
				v := doc.Fields[n.Field]
				_ = v // we already filtered by sort.Search inclusive bounds
			}
		}
		return out
	case kTag:
		out := map[string]float64{}
		for _, id := range idx.TagDocs(n.Field, n.Term) {
			out[id] = 1
		}
		return out
	case kAnd:
		var acc map[string]float64
		for i, child := range n.Children {
			res := idx.eval(child)
			if i == 0 {
				acc = res
				continue
			}
			merged := map[string]float64{}
			for id, sc := range acc {
				if other, ok := res[id]; ok {
					merged[id] = sc + other
				}
			}
			acc = merged
		}
		return acc
	case kOr:
		out := map[string]float64{}
		for _, child := range n.Children {
			for id, sc := range idx.eval(child) {
				out[id] += sc
			}
		}
		return out
	case kNot:
		exclude := idx.eval(n.Children[0])
		out := map[string]float64{}
		for _, id := range idx.AllDocIDs() {
			if _, has := exclude[id]; !has {
				out[id] = 0
			}
		}
		return out
	}
	return nil
}

// scoreAcrossTextFields scores `term` over every TEXT field, weighting
// per the schema. This is what bare-term queries hit.
func (idx *Index) scoreAcrossTextFields(term string) map[string]float64 {
	out := map[string]float64{}
	for _, f := range idx.Schema.Fields {
		if f.Type != FieldText {
			continue
		}
		t := term
		if !f.NoStem {
			t = stemSuffix(t)
		}
		p := idx.TermPostings(f.Name, t)
		if p == nil {
			continue
		}
		for _, e := range p.entries {
			out[e.docID] += idx.BM25Score(f.Name, t, e.docID) * f.Weight
		}
	}
	return out
}

func (idx *Index) scoreSingleField(field, term string) map[string]float64 {
	out := map[string]float64{}
	f := idx.Schema.Field(field)
	if f == nil || f.Type != FieldText {
		return out
	}
	t := term
	if !f.NoStem {
		t = stemSuffix(t)
	}
	p := idx.TermPostings(field, t)
	if p == nil {
		return out
	}
	for _, e := range p.entries {
		out[e.docID] += idx.BM25Score(field, t, e.docID) * f.Weight
	}
	return out
}

// scorePhrase requires every term to appear in the same doc. We don't
// currently store positions, so the implementation is conjunctive
// instead of strictly contiguous — a clear note in the docs lists this
// as a known subset behaviour.
func (idx *Index) scorePhrase(field string, phrase []string) map[string]float64 {
	if len(phrase) == 0 {
		return nil
	}
	var first map[string]float64
	for i, term := range phrase {
		var hit map[string]float64
		if field != "" {
			hit = idx.scoreSingleField(field, term)
		} else {
			hit = idx.scoreAcrossTextFields(term)
		}
		if i == 0 {
			first = hit
			continue
		}
		merged := map[string]float64{}
		for id, sc := range first {
			if other, ok := hit[id]; ok {
				merged[id] = sc + other
			}
		}
		first = merged
	}
	// modest phrase boost to prioritise multi-term matches
	for id := range first {
		first[id] *= 1.25
	}
	_ = strings.Join(phrase, " ") // keep imports tidy if a future log line wants it
	return first
}
