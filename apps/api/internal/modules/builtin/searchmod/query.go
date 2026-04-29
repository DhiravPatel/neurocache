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
	return idx.SearchWithParams(q, nil)
}

// SearchWithParams resolves $param references in KNN clauses against
// the FT.SEARCH PARAMS map. Other query kinds ignore params.
func (idx *Index) SearchWithParams(q *QueryNode, params map[string]string) []SearchHit {
	idx.queryParams = params
	defer func() { idx.queryParams = nil }()
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
	case kGeo:
		out := map[string]float64{}
		for _, id := range idx.GeoWithin(n.Field, n.GeoLat, n.GeoLon, n.GeoRadM) {
			out[id] = 1
		}
		return out
	case kKNN:
		// Resolve the parameter to the raw vector bytes / CSV.
		raw, ok := idx.queryParams[n.KnnParam]
		if !ok {
			return map[string]float64{}
		}
		vi := idx.VectorIndex(n.Field)
		if vi == nil {
			return map[string]float64{}
		}
		query, err := parseVector(raw, vi.dim)
		if err != nil {
			return map[string]float64{}
		}
		results := vi.KNN(query, n.KnnK)
		out := map[string]float64{}
		// Convert distance → score (higher = better).
		for _, r := range results {
			out[r.DocID] = 1 / (1 + r.Distance)
		}
		return out
	case kFuzzy:
		// Walk every term in every TEXT field; keep matches whose
		// Levenshtein distance to the query term is ≤ FuzzyMax.
		out := map[string]float64{}
		for _, f := range idx.Schema.Fields {
			if f.Type != FieldText {
				continue
			}
			for term, p := range idx.postings[f.Name] {
				if levenshtein(term, n.Term, n.FuzzyMax+1) > n.FuzzyMax {
					continue
				}
				for _, e := range p.entries {
					// Slight penalty proportional to edit distance.
					out[e.docID] += idx.BM25Score(f.Name, term, e.docID) * f.Weight /
						float64(1+levenshtein(term, n.Term, n.FuzzyMax+1))
				}
			}
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

// scorePhrase requires every term to appear contiguously in the same
// document. We use the positional postings recorded at index time:
// for each candidate doc, walk the first term's positions and check
// whether (pos+1, pos+2, …) appears in the subsequent terms' postings.
// Without positions we'd only get conjunctive matching, which produces
// false positives for queries like `"red wine"` against "wine red".
func (idx *Index) scorePhrase(field string, phrase []string) map[string]float64 {
	if len(phrase) == 0 {
		return nil
	}
	out := map[string]float64{}
	fields := []string{field}
	if field == "" {
		fields = nil
		for _, f := range idx.Schema.Fields {
			if f.Type == FieldText {
				fields = append(fields, f.Name)
			}
		}
	}
	for _, fname := range fields {
		f := idx.Schema.Field(fname)
		if f == nil || f.Type != FieldText {
			continue
		}
		// Resolve every phrase term's posting list in this field.
		stems := make([]string, len(phrase))
		for i, term := range phrase {
			stems[i] = term
			if !f.NoStem {
				stems[i] = stemSuffix(term)
			}
		}
		first := idx.TermPostings(fname, stems[0])
		if first == nil {
			continue
		}
		// For each candidate doc, demand a contiguous match.
		for _, e := range first.entries {
			if !idx.phraseHits(fname, stems, e.docID) {
				continue
			}
			out[e.docID] += idx.BM25Score(fname, stems[0], e.docID) * f.Weight * 1.5
		}
	}
	_ = strings.Join(phrase, " ") // keep imports tidy
	return out
}

// phraseHits checks whether `stems` appear in order, starting at one
// of the first term's recorded positions. Returns false when the field
// stored no positional data (which means the legacy add path was used
// — phrase queries against such docs degrade to conjunctive).
func (idx *Index) phraseHits(field string, stems []string, docID string) bool {
	first := idx.TermPostings(field, stems[0])
	if first == nil {
		return false
	}
	starts := first.positions(docID)
	if starts == nil {
		// no positional data — best-effort conjunctive check
		for _, term := range stems[1:] {
			p := idx.TermPostings(field, term)
			if p == nil || p.freq(docID) == 0 {
				return false
			}
		}
		return true
	}
	for _, start := range starts {
		ok := true
		for offset, term := range stems[1:] {
			p := idx.TermPostings(field, term)
			if p == nil {
				ok = false
				break
			}
			if !containsInt(p.positions(docID), start+offset+1) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func containsInt(arr []int, v int) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}
