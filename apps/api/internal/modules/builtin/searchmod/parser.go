package searchmod

import (
	"errors"
	"strconv"
	"strings"
)

// QueryNode is the parse tree for a search expression. Six node kinds
// cover the supported subset.
type QueryNode struct {
	Kind     QueryKind
	Field    string    // for kField, kRange, kTag
	Term     string    // for kTerm, kPrefix, kPhrase
	Phrase   []string  // for kPhrase (token sequence, post-stem)
	Lo, Hi   float64   // for kRange (numeric)
	LoOpen   bool      // ( -> exclusive
	HiOpen   bool      // ( -> exclusive
	Children []*QueryNode
}

type QueryKind int

const (
	kAll    QueryKind = iota // "*" — match everything
	kTerm                    // single token
	kPrefix                  // token*
	kPhrase                  // "exact phrase"
	kField                   // @field:<inner>
	kRange                   // @field:[lo hi] (numeric)
	kTag                     // @field:{tag1|tag2}
	kAnd
	kOr
	kNot
)

// ParseQuery turns a query string into a tree. We keep the grammar
// deliberately small but unambiguous; mismatched delimiters surface as
// errors so the dispatcher can return a clean -ERR.
func ParseQuery(q string) (*QueryNode, error) {
	q = strings.TrimSpace(q)
	if q == "" || q == "*" {
		return &QueryNode{Kind: kAll}, nil
	}
	p := &queryParser{src: q}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.src) {
		return nil, errors.New("unexpected trailing input: " + p.src[p.pos:])
	}
	return node, nil
}

type queryParser struct {
	src string
	pos int
}

// parseOr handles `A | B | C` (lowest precedence).
func (p *queryParser) parseOr() (*QueryNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != '|' {
			return left, nil
		}
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if left.Kind == kOr {
			left.Children = append(left.Children, right)
		} else {
			left = &QueryNode{Kind: kOr, Children: []*QueryNode{left, right}}
		}
	}
}

// parseAnd handles juxtaposed terms — whitespace = AND.
func (p *queryParser) parseAnd() (*QueryNode, error) {
	parts := []*QueryNode{}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] == '|' || p.src[p.pos] == ')' {
			break
		}
		node, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		parts = append(parts, node)
	}
	switch len(parts) {
	case 0:
		return nil, errors.New("empty query")
	case 1:
		return parts[0], nil
	default:
		return &QueryNode{Kind: kAnd, Children: parts}, nil
	}
}

// parseUnary handles `-foo` (NOT) and falls through to atoms.
func (p *queryParser) parseUnary() (*QueryNode, error) {
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == '-' {
		p.pos++
		inner, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		return &QueryNode{Kind: kNot, Children: []*QueryNode{inner}}, nil
	}
	return p.parseAtom()
}

// parseAtom: parens, field qualifier, phrase, or term.
func (p *queryParser) parseAtom() (*QueryNode, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, errors.New("unexpected end of query")
	}
	switch p.src[p.pos] {
	case '(':
		p.pos++
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return nil, errors.New("missing )")
		}
		p.pos++
		return node, nil
	case '@':
		return p.parseFieldQualifier()
	case '"':
		return p.parsePhrase()
	}
	return p.parseTermOrPrefix()
}

func (p *queryParser) parseFieldQualifier() (*QueryNode, error) {
	p.pos++ // consume @
	name := p.readIdent()
	if name == "" {
		return nil, errors.New("expected field name after @")
	}
	if p.pos >= len(p.src) || p.src[p.pos] != ':' {
		return nil, errors.New("expected : after field name")
	}
	p.pos++
	p.skipSpace()
	if p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '[':
			return p.parseRange(name)
		case '{':
			return p.parseTagSet(name)
		}
	}
	inner, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	// stamp the field down through the inner node
	inner.Field = name
	if inner.Kind == kTerm {
		inner.Kind = kField
		inner.Children = []*QueryNode{{Kind: kTerm, Term: inner.Term}}
		inner.Term = ""
	}
	return inner, nil
}

func (p *queryParser) parseRange(field string) (*QueryNode, error) {
	p.pos++ // consume [
	lo, loOpen := p.readNumericBound()
	p.skipSpace()
	hi, hiOpen := p.readNumericBound()
	p.skipSpace()
	if p.pos >= len(p.src) || p.src[p.pos] != ']' {
		return nil, errors.New("missing ] in range")
	}
	p.pos++
	return &QueryNode{Kind: kRange, Field: field, Lo: lo, Hi: hi, LoOpen: loOpen, HiOpen: hiOpen}, nil
}

func (p *queryParser) readNumericBound() (float64, bool) {
	open := false
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		open = true
		p.pos++
	}
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ' ' || c == ']' {
			break
		}
		p.pos++
	}
	tok := p.src[start:p.pos]
	switch strings.ToLower(tok) {
	case "-inf", "-infinity":
		return -1e308, open
	case "+inf", "+infinity", "inf":
		return 1e308, open
	}
	v, _ := strconv.ParseFloat(tok, 64)
	return v, open
}

func (p *queryParser) parseTagSet(field string) (*QueryNode, error) {
	p.pos++ // consume {
	tokens := []*QueryNode{}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, errors.New("missing } in tag set")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			break
		}
		tag := p.readTagToken()
		tokens = append(tokens, &QueryNode{Kind: kTag, Field: field, Term: tag})
		p.skipSpace()
		if p.pos < len(p.src) && p.src[p.pos] == '|' {
			p.pos++
		}
	}
	if len(tokens) == 1 {
		return tokens[0], nil
	}
	return &QueryNode{Kind: kOr, Children: tokens}, nil
}

func (p *queryParser) readTagToken() string {
	sb := strings.Builder{}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '|' || c == '}' || c == ' ' {
			break
		}
		sb.WriteByte(c)
		p.pos++
	}
	return strings.ToLower(strings.TrimSpace(sb.String()))
}

func (p *queryParser) parsePhrase() (*QueryNode, error) {
	p.pos++ // consume opening "
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != '"' {
		p.pos++
	}
	if p.pos >= len(p.src) {
		return nil, errors.New("unterminated phrase")
	}
	body := p.src[start:p.pos]
	p.pos++ // closing "
	tokens := Tokenize(body, true)
	return &QueryNode{Kind: kPhrase, Phrase: tokens}, nil
}

func (p *queryParser) parseTermOrPrefix() (*QueryNode, error) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ' ' || c == '|' || c == ')' || c == '(' || c == '"' {
			break
		}
		p.pos++
	}
	tok := p.src[start:p.pos]
	if tok == "" {
		return nil, errors.New("empty term")
	}
	prefix := false
	if strings.HasSuffix(tok, "*") {
		prefix = true
		tok = tok[:len(tok)-1]
	}
	tok = strings.ToLower(tok)
	if prefix {
		return &QueryNode{Kind: kPrefix, Term: tok}, nil
	}
	return &QueryNode{Kind: kTerm, Term: tok}, nil
}

func (p *queryParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *queryParser) readIdent() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			p.pos++
		} else {
			break
		}
	}
	return p.src[start:p.pos]
}

// Explain renders the parse tree as a human-readable string. Used by
// FT.EXPLAIN.
func Explain(n *QueryNode, depth int) string {
	if n == nil {
		return ""
	}
	pad := strings.Repeat("  ", depth)
	switch n.Kind {
	case kAll:
		return pad + "<ALL>\n"
	case kTerm:
		if n.Field != "" {
			return pad + "@" + n.Field + ":" + n.Term + "\n"
		}
		return pad + n.Term + "\n"
	case kPrefix:
		return pad + n.Term + "*\n"
	case kPhrase:
		return pad + "\"" + strings.Join(n.Phrase, " ") + "\"\n"
	case kField:
		out := pad + "@" + n.Field + ":\n"
		for _, c := range n.Children {
			out += Explain(c, depth+1)
		}
		return out
	case kRange:
		return pad + "@" + n.Field + ":[" + ftoa(n.Lo) + " " + ftoa(n.Hi) + "]\n"
	case kTag:
		return pad + "@" + n.Field + ":{" + n.Term + "}\n"
	case kAnd:
		out := pad + "AND\n"
		for _, c := range n.Children {
			out += Explain(c, depth+1)
		}
		return out
	case kOr:
		out := pad + "OR\n"
		for _, c := range n.Children {
			out += Explain(c, depth+1)
		}
		return out
	case kNot:
		out := pad + "NOT\n"
		for _, c := range n.Children {
			out += Explain(c, depth+1)
		}
		return out
	}
	return ""
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
