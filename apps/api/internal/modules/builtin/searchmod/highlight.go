package searchmod

import (
	"sort"
	"strings"
)

// HighlightOpts mirrors FT.SEARCH HIGHLIGHT [FIELDS n field ...]
// [TAGS open close]. When Fields is nil/empty every TEXT field is
// highlighted. open + close default to <b>...</b>.
type HighlightOpts struct {
	Fields []string
	Open   string
	Close  string
}

// SummarizeOpts mirrors FT.SEARCH SUMMARIZE
// [FIELDS n field ...] [FRAGS n] [LEN n] [SEPARATOR sep]. Defaults
// match Redis: 3 fragments per field, 20 tokens per fragment,
// "... " separator.
type SummarizeOpts struct {
	Fields    []string
	Frags     int
	Len       int
	Separator string
}

// applyHighlight wraps every query-term occurrence in the named
// fields with open/close tags. We walk character-by-character so
// the result preserves the original casing and whitespace — only
// the matched substring is wrapped.
//
// terms is the lower-cased list of query terms extracted from the
// parsed query tree. The match is case-insensitive.
func applyHighlight(doc *Document, opts HighlightOpts, terms []string) {
	if doc == nil {
		return
	}
	open, closeT := opts.Open, opts.Close
	if open == "" {
		open = "<b>"
	}
	if closeT == "" {
		closeT = "</b>"
	}
	candidate := func(field string) bool {
		if len(opts.Fields) == 0 {
			return true
		}
		for _, f := range opts.Fields {
			if strings.EqualFold(f, field) {
				return true
			}
		}
		return false
	}
	for f, v := range doc.Fields {
		if !candidate(f) {
			continue
		}
		doc.Fields[f] = wrapTerms(v, terms, open, closeT)
	}
}

// applySummarize replaces each named field's value with a short
// "snippet around the match" preview — opts.Frags fragments per
// field, opts.Len tokens per fragment, joined by opts.Separator.
//
// We pick fragments greedily: scan the field for the first match,
// take a ±Len/2 window around it, advance, repeat until Frags or
// the field ends.
func applySummarize(doc *Document, opts SummarizeOpts, terms []string) {
	if doc == nil {
		return
	}
	frags := opts.Frags
	if frags <= 0 {
		frags = 3
	}
	tokensLen := opts.Len
	if tokensLen <= 0 {
		tokensLen = 20
	}
	sep := opts.Separator
	if sep == "" {
		sep = "... "
	}
	candidate := func(field string) bool {
		if len(opts.Fields) == 0 {
			return true
		}
		for _, f := range opts.Fields {
			if strings.EqualFold(f, field) {
				return true
			}
		}
		return false
	}
	for f, v := range doc.Fields {
		if !candidate(f) {
			continue
		}
		doc.Fields[f] = summarizeText(v, terms, frags, tokensLen, sep)
	}
}

// wrapTerms scans s for case-insensitive whole-word occurrences of
// any term and returns a copy with each match surrounded by open /
// close. The "whole word" check prevents "go" from highlighting
// inside "google".
func wrapTerms(s string, terms []string, open, closeT string) string {
	if len(terms) == 0 {
		return s
	}
	lc := strings.ToLower(s)
	type span struct{ start, end int }
	hits := []span{}
	for _, t := range terms {
		if t == "" {
			continue
		}
		off := 0
		for {
			idx := strings.Index(lc[off:], t)
			if idx < 0 {
				break
			}
			pos := off + idx
			endPos := pos + len(t)
			if isWordBoundary(lc, pos, endPos) {
				hits = append(hits, span{start: pos, end: endPos})
			}
			off = pos + len(t)
		}
	}
	if len(hits) == 0 {
		return s
	}
	// Sort + dedupe overlapping spans so adjacent matches don't
	// collide (e.g. "redis" and "edis" overlap).
	sort.Slice(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
	merged := hits[:0]
	cur := hits[0]
	for _, h := range hits[1:] {
		if h.start <= cur.end {
			if h.end > cur.end {
				cur.end = h.end
			}
			continue
		}
		merged = append(merged, cur)
		cur = h
	}
	merged = append(merged, cur)
	// Reassemble.
	var b strings.Builder
	prev := 0
	for _, h := range merged {
		b.WriteString(s[prev:h.start])
		b.WriteString(open)
		b.WriteString(s[h.start:h.end])
		b.WriteString(closeT)
		prev = h.end
	}
	b.WriteString(s[prev:])
	return b.String()
}

// summarizeText extracts up to fragsCount snippets from s where
// each snippet is a window of approximately tokensPerFrag tokens
// centred on a match.
func summarizeText(s string, terms []string, fragsCount, tokensPerFrag int, sep string) string {
	if len(terms) == 0 {
		return s
	}
	tokens := tokenizeFlat(s)
	if len(tokens) == 0 {
		return s
	}
	matches := findMatchPositions(tokens, terms)
	if len(matches) == 0 {
		// No match — fall back to the head of the field, capped at
		// (frags × len) tokens. Mirrors Redis: SUMMARIZE always
		// returns *something* truncated rather than the whole field.
		end := fragsCount * tokensPerFrag
		if end > len(tokens) {
			end = len(tokens)
		}
		return strings.Join(tokens[:end], " ") + sep
	}
	half := tokensPerFrag / 2
	if half < 1 {
		half = 1
	}
	picked := []string{}
	taken := 0
	used := map[int]bool{}
	for _, mPos := range matches {
		if taken >= fragsCount {
			break
		}
		// Skip positions that fall inside an already-emitted window.
		if used[mPos] {
			continue
		}
		start := mPos - half
		if start < 0 {
			start = 0
		}
		end := mPos + half
		if end > len(tokens) {
			end = len(tokens)
		}
		for i := start; i < end; i++ {
			used[i] = true
		}
		picked = append(picked, strings.Join(tokens[start:end], " "))
		taken++
	}
	return strings.Join(picked, sep) + sep
}

// findMatchPositions returns the token indices where any term
// matches (case-insensitive whole-token).
func findMatchPositions(tokens []string, terms []string) []int {
	wanted := map[string]bool{}
	for _, t := range terms {
		wanted[strings.ToLower(t)] = true
	}
	out := []int{}
	for i, tok := range tokens {
		if wanted[strings.ToLower(tok)] {
			out = append(out, i)
		}
	}
	return out
}

// tokenizeFlat splits s on whitespace + common punctuation,
// preserving the original tokens (no lowercasing). Used by
// summarize so the output reads naturally.
func tokenizeFlat(s string) []string {
	out := []string{}
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			flush()
		case ',', '.', ';', ':', '!', '?', '(', ')', '[', ']', '"', '\'':
			flush()
		default:
			cur += string(r)
		}
	}
	flush()
	return out
}

// isWordBoundary reports whether s[pos:end] sits at a word boundary
// (start/end of string or surrounded by non-word runes).
func isWordBoundary(s string, pos, end int) bool {
	leftOK := pos == 0 || !isWordRune(s[pos-1])
	rightOK := end == len(s) || !isWordRune(s[end])
	return leftOK && rightOK
}

func isWordRune(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}

// extractQueryTerms walks the parsed query tree and collects every
// term + phrase token — used to seed HIGHLIGHT / SUMMARIZE.
func extractQueryTerms(q *QueryNode) []string {
	if q == nil {
		return nil
	}
	out := []string{}
	walkQueryTerms(q, &out)
	return out
}

func walkQueryTerms(q *QueryNode, out *[]string) {
	if q == nil {
		return
	}
	switch q.Kind {
	case kTerm, kPrefix, kFuzzy:
		if q.Term != "" {
			*out = append(*out, q.Term)
		}
	case kPhrase:
		for _, p := range q.Phrase {
			if p != "" {
				*out = append(*out, p)
			}
		}
	}
	for _, child := range q.Children {
		walkQueryTerms(child, out)
	}
}
