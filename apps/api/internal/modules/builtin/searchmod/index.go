package searchmod

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Index is one full-text search index — schema + per-field postings,
// numeric trees, and tag sets. All fields are populated under the same
// document-ID space so a query can intersect across types.
type Index struct {
	Name   string
	Schema *Schema
	mu     sync.RWMutex

	docs     map[string]*Document // by document ID (caller's key)
	postings map[string]map[string]*postingList // field name -> term -> list
	numeric  map[string]*numericIndex
	tags     map[string]map[string]map[string]struct{} // field -> tag -> docID set
	docLen   map[string]map[string]int                 // field -> docID -> term count (for BM25 length norm)
	totalLen map[string]int                            // field -> sum of term counts
}

// Document is one indexed record. Fields hold the *raw* values so
// FT.SEARCH can RETURN them verbatim; the index tracks tokenised
// forms separately.
type Document struct {
	ID     string
	Fields map[string]string
	Score  float64
}

// postingList is a sorted slice of (docID, frequency) entries — small
// and cache-friendly. Map-of-maps would burn allocations; this stays
// tight and lets us merge AND queries via two-finger scans.
type postingList struct {
	entries []posting
}

type posting struct {
	docID string
	freq  int
}

// numericIndex stores (value, docID) pairs sorted by value so range
// queries can binary-search both bounds.
type numericIndex struct {
	entries []numericEntry
	dirty   bool
}

type numericEntry struct {
	value float64
	docID string
}

// NewIndex builds a fresh empty index for the given schema.
func NewIndex(name string, schema *Schema) *Index {
	idx := &Index{
		Name: name, Schema: schema,
		docs:     map[string]*Document{},
		postings: map[string]map[string]*postingList{},
		numeric:  map[string]*numericIndex{},
		tags:     map[string]map[string]map[string]struct{}{},
		docLen:   map[string]map[string]int{},
		totalLen: map[string]int{},
	}
	for _, f := range schema.Fields {
		switch f.Type {
		case FieldText:
			idx.postings[f.Name] = map[string]*postingList{}
			idx.docLen[f.Name] = map[string]int{}
		case FieldNumeric:
			idx.numeric[f.Name] = &numericIndex{}
		case FieldTag:
			idx.tags[f.Name] = map[string]map[string]struct{}{}
		}
	}
	return idx
}

// AddDoc indexes (or replaces) a document. Removing the old version
// first keeps postings + numeric trees + tag sets consistent.
func (idx *Index) AddDoc(id string, fields map[string]string, score float64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, ok := idx.docs[id]; ok {
		idx.removeLocked(id)
	}
	doc := &Document{ID: id, Fields: cloneStringMap(fields), Score: score}
	idx.docs[id] = doc
	for _, f := range idx.Schema.Fields {
		raw, ok := fields[f.Name]
		if !ok || f.NoIndex {
			continue
		}
		switch f.Type {
		case FieldText:
			tokens := Tokenize(raw, !f.NoStem)
			counts := map[string]int{}
			for _, t := range tokens {
				counts[t]++
			}
			fieldPost := idx.postings[f.Name]
			for term, freq := range counts {
				p, ok := fieldPost[term]
				if !ok {
					p = &postingList{}
					fieldPost[term] = p
				}
				p.add(id, freq)
			}
			idx.docLen[f.Name][id] = len(tokens)
			idx.totalLen[f.Name] += len(tokens)
		case FieldNumeric:
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			ni := idx.numeric[f.Name]
			ni.entries = append(ni.entries, numericEntry{value: v, docID: id})
			ni.dirty = true
		case FieldTag:
			tags := SplitTags(raw, f.TagSep)
			fieldTags := idx.tags[f.Name]
			for _, t := range tags {
				set, ok := fieldTags[t]
				if !ok {
					set = map[string]struct{}{}
					fieldTags[t] = set
				}
				set[id] = struct{}{}
			}
		}
	}
}

// DelDoc removes a document. Returns true if it was present.
func (idx *Index) DelDoc(id string) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, ok := idx.docs[id]; !ok {
		return false
	}
	idx.removeLocked(id)
	delete(idx.docs, id)
	return true
}

func (idx *Index) removeLocked(id string) {
	for _, f := range idx.Schema.Fields {
		switch f.Type {
		case FieldText:
			fieldPost := idx.postings[f.Name]
			for term, p := range fieldPost {
				p.remove(id)
				if len(p.entries) == 0 {
					delete(fieldPost, term)
				}
			}
			if l, ok := idx.docLen[f.Name][id]; ok {
				idx.totalLen[f.Name] -= l
				delete(idx.docLen[f.Name], id)
			}
		case FieldNumeric:
			ni := idx.numeric[f.Name]
			kept := ni.entries[:0]
			for _, e := range ni.entries {
				if e.docID != id {
					kept = append(kept, e)
				}
			}
			ni.entries = kept
		case FieldTag:
			for tag, set := range idx.tags[f.Name] {
				delete(set, id)
				if len(set) == 0 {
					delete(idx.tags[f.Name], tag)
				}
			}
		}
	}
}

// DocCount returns the number of indexed documents.
func (idx *Index) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// Doc fetches a document by ID.
func (idx *Index) Doc(id string) (*Document, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	d, ok := idx.docs[id]
	return d, ok
}

// AllDocIDs returns every indexed document ID. Used by the wildcard
// query "*" and by FT.AGGREGATE when no query is specified.
func (idx *Index) AllDocIDs() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]string, 0, len(idx.docs))
	for id := range idx.docs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TermPostings returns the posting list for (field, term), or nil.
func (idx *Index) TermPostings(field, term string) *postingList {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if fp, ok := idx.postings[field]; ok {
		return fp[term]
	}
	return nil
}

// PrefixPostings returns the union posting list for every term in
// `field` that begins with `prefix`. Used by `term*` queries.
func (idx *Index) PrefixPostings(field, prefix string) *postingList {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	fp, ok := idx.postings[field]
	if !ok {
		return nil
	}
	merged := &postingList{}
	for term, p := range fp {
		if strings.HasPrefix(term, prefix) {
			merged.union(p)
		}
	}
	return merged
}

// NumericRange returns the doc IDs whose `field` value sits in [lo, hi].
// Sorted as a side effect on first call so subsequent ranges are O(log n).
func (idx *Index) NumericRange(field string, lo, hi float64) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	ni, ok := idx.numeric[field]
	if !ok {
		return nil
	}
	if ni.dirty {
		sort.Slice(ni.entries, func(i, j int) bool { return ni.entries[i].value < ni.entries[j].value })
		ni.dirty = false
	}
	loIdx := sort.Search(len(ni.entries), func(i int) bool { return ni.entries[i].value >= lo })
	hiIdx := sort.Search(len(ni.entries), func(i int) bool { return ni.entries[i].value > hi })
	if loIdx == hiIdx {
		return nil
	}
	out := make([]string, 0, hiIdx-loIdx)
	for _, e := range ni.entries[loIdx:hiIdx] {
		out = append(out, e.docID)
	}
	return out
}

// TagDocs returns the doc IDs tagged with `tag` on `field`.
func (idx *Index) TagDocs(field, tag string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	set, ok := idx.tags[field][tag]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// AvgFieldLen is the average length (in tokens) of `field` across the
// index — needed for BM25's length-norm component.
func (idx *Index) AvgFieldLen(field string) float64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	docs := len(idx.docLen[field])
	if docs == 0 {
		return 0
	}
	return float64(idx.totalLen[field]) / float64(docs)
}

// FieldLen returns the token count of doc `id` in `field`.
func (idx *Index) FieldLen(field, id string) int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.docLen[field][id]
}

// ── posting list helpers ──────────────────────────────────────────

func (p *postingList) add(docID string, freq int) {
	// Maintain sorted order so AND/OR merges are linear.
	pos := sort.Search(len(p.entries), func(i int) bool { return p.entries[i].docID >= docID })
	if pos < len(p.entries) && p.entries[pos].docID == docID {
		p.entries[pos].freq += freq
		return
	}
	p.entries = append(p.entries, posting{})
	copy(p.entries[pos+1:], p.entries[pos:])
	p.entries[pos] = posting{docID: docID, freq: freq}
}

func (p *postingList) remove(docID string) {
	pos := sort.Search(len(p.entries), func(i int) bool { return p.entries[i].docID >= docID })
	if pos < len(p.entries) && p.entries[pos].docID == docID {
		p.entries = append(p.entries[:pos], p.entries[pos+1:]...)
	}
}

func (p *postingList) docIDs() []string {
	out := make([]string, len(p.entries))
	for i, e := range p.entries {
		out[i] = e.docID
	}
	return out
}

func (p *postingList) freq(docID string) int {
	pos := sort.Search(len(p.entries), func(i int) bool { return p.entries[i].docID >= docID })
	if pos < len(p.entries) && p.entries[pos].docID == docID {
		return p.entries[pos].freq
	}
	return 0
}

// union merges other into p (preserves sort order, sums frequencies).
func (p *postingList) union(other *postingList) {
	if other == nil || len(other.entries) == 0 {
		return
	}
	merged := make([]posting, 0, len(p.entries)+len(other.entries))
	i, j := 0, 0
	for i < len(p.entries) && j < len(other.entries) {
		switch strings.Compare(p.entries[i].docID, other.entries[j].docID) {
		case -1:
			merged = append(merged, p.entries[i])
			i++
		case 1:
			merged = append(merged, other.entries[j])
			j++
		default:
			merged = append(merged, posting{
				docID: p.entries[i].docID,
				freq:  p.entries[i].freq + other.entries[j].freq,
			})
			i++
			j++
		}
	}
	merged = append(merged, p.entries[i:]...)
	merged = append(merged, other.entries[j:]...)
	p.entries = merged
}

// ── BM25 scoring ─────────────────────────────────────────────────

// BM25Score computes the BM25 contribution of one (field, term) hit
// against a document. Caller sums these across all matched terms.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

func (idx *Index) BM25Score(field, term, docID string) float64 {
	p := idx.TermPostings(field, term)
	if p == nil {
		return 0
	}
	tf := float64(p.freq(docID))
	if tf == 0 {
		return 0
	}
	docCount := float64(idx.DocCount())
	df := float64(len(p.entries))
	idf := math.Log(1 + (docCount-df+0.5)/(df+0.5))
	avgLen := idx.AvgFieldLen(field)
	if avgLen == 0 {
		avgLen = 1
	}
	dl := float64(idx.FieldLen(field, docID))
	norm := tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*dl/avgLen))
	return idf * norm
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
