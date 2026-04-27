package jsonmod

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// predicate is the boolean expression carried by a `[?(...)]` filter
// segment. We support the patterns Redis JSON v2's filter syntax
// surfaces in the wild:
//
//   @field op value         e.g. @price > 10
//   @field.sub op value     e.g. @meta.kind == "book"
//   value op @field         (commutative variant)
//   @field                  (truthy test)
//   ! @field                (negated truthy test)
//   <pred> && <pred>        conjunction
//   <pred> || <pred>        disjunction
//
// Operators: == != < <= > >= =~ (regex). Values are JSON literals
// (numbers, strings, true/false/null). Whitespace is liberal.
type predicate struct {
	left   *predicate
	right  *predicate
	op     string
	field  string  // populated for leaf comparisons (left side)
	value  any     // RHS literal
	negate bool
}

// Eval applies the predicate against one element. Missing fields are
// treated as nil — comparing nil to anything except nil is false.
func (p *predicate) Eval(elem any) bool {
	if p == nil {
		return true
	}
	if p.op == "&&" || p.op == "||" {
		l := p.left.Eval(elem)
		if p.op == "&&" {
			if !l {
				return false
			}
			return p.right.Eval(elem)
		}
		if l {
			return true
		}
		return p.right.Eval(elem)
	}
	got := lookupField(elem, p.field)
	switch p.op {
	case "":
		return truthy(got) != p.negate
	case "==":
		return jsonEqual(got, p.value)
	case "!=":
		return !jsonEqual(got, p.value)
	case "<", "<=", ">", ">=":
		return numCompare(got, p.value, p.op)
	case "=~":
		s, _ := got.(string)
		pat, _ := p.value.(string)
		return regexLike(s, pat)
	}
	return false
}

// parsePredicate is a tiny shunting-yard parser for the supported
// grammar. We tokenise on whitespace + the special chars for ops, then
// build a left-deep AST honouring precedence (== < > etc. above && and
// ||). The grammar is small enough that we hand-roll instead of pulling
// in a full lexer.
func parsePredicate(s string) (*predicate, error) {
	tokens, err := tokeniseFilter(s)
	if err != nil {
		return nil, err
	}
	p := &filterParser{tokens: tokens}
	root, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.tokens) {
		return nil, errors.New("trailing input in filter expression")
	}
	return root, nil
}

type filterParser struct {
	tokens []string
	pos    int
}

func (p *filterParser) peek() string {
	if p.pos >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.pos]
}
func (p *filterParser) advance() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *filterParser) parseOr() (*predicate, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek() == "||" {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &predicate{op: "||", left: left, right: right}
	}
	return left, nil
}

func (p *filterParser) parseAnd() (*predicate, error) {
	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	for p.peek() == "&&" {
		p.advance()
		right, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		left = &predicate{op: "&&", left: left, right: right}
	}
	return left, nil
}

func (p *filterParser) parseAtom() (*predicate, error) {
	negate := false
	if p.peek() == "!" {
		negate = true
		p.advance()
	}
	if p.peek() == "(" {
		p.advance()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, errors.New("expected ) in filter")
		}
		p.advance()
		if negate {
			return &predicate{op: "&&", left: &predicate{negate: true, field: ""}, right: inner}, nil
		}
		return inner, nil
	}
	// expect @field [op value]
	tok := p.advance()
	if !strings.HasPrefix(tok, "@") {
		return nil, errors.New("expected @field reference")
	}
	field := strings.TrimPrefix(tok, "@.")
	field = strings.TrimPrefix(field, "@")
	if isOp(p.peek()) {
		op := p.advance()
		valTok := p.advance()
		val, err := parseLiteral(valTok)
		if err != nil {
			return nil, err
		}
		return &predicate{op: op, field: field, value: val, negate: negate}, nil
	}
	return &predicate{field: field, negate: negate}, nil
}

func isOp(s string) bool {
	switch s {
	case "==", "!=", "<", "<=", ">", ">=", "=~":
		return true
	}
	return false
}

// tokeniseFilter splits the expression on whitespace + multi-char
// operators. Quoted strings stay intact.
func tokeniseFilter(s string) ([]string, error) {
	tokens := []string{}
	cur := strings.Builder{}
	inStr := byte(0)
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	emit := func(t string) {
		flush()
		tokens = append(tokens, t)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			cur.WriteByte(c)
			if c == inStr && (i == 0 || s[i-1] != '\\') {
				flush()
				inStr = 0
			}
			continue
		}
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '"' || c == '\'':
			flush()
			cur.WriteByte(c)
			inStr = c
		case c == '(' || c == ')' || c == '!':
			flush()
			emit(string(c))
		case (c == '&' || c == '|') && i+1 < len(s) && s[i+1] == c:
			emit(string([]byte{c, c}))
			i++
		case c == '=' && i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '~'):
			emit(string([]byte{c, s[i+1]}))
			i++
		case c == '!' && i+1 < len(s) && s[i+1] == '=':
			emit("!=")
			i++
		case c == '<' || c == '>':
			if i+1 < len(s) && s[i+1] == '=' {
				emit(string([]byte{c, '='}))
				i++
			} else {
				emit(string(c))
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	if inStr != 0 {
		return nil, errors.New("unterminated string in filter")
	}
	return tokens, nil
}

func parseLiteral(tok string) (any, error) {
	if tok == "" {
		return nil, errors.New("missing literal")
	}
	if (tok[0] == '"' || tok[0] == '\'') && tok[len(tok)-1] == tok[0] {
		return tok[1 : len(tok)-1], nil
	}
	switch tok {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
		return json.Number(strconv.FormatInt(i, 10)), nil
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	}
	return tok, nil
}

// lookupField walks a dotted path under the supplied element.
func lookupField(elem any, field string) any {
	if field == "" {
		return elem
	}
	cur := elem
	for _, part := range strings.Split(field, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[part]
	}
	return cur
}

func truthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	if n, _, isInt, _ := asNumber(v); isInt {
		return n != 0
	}
	if _, f, _, err := asNumber(v); err == nil {
		return f != 0
	}
	if s, ok := v.(string); ok {
		return s != ""
	}
	return true
}

func jsonEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Numbers compare by value regardless of int/float shape.
	if ai, af, aIsInt, aErr := asNumber(a); aErr == nil {
		if bi, bf, bIsInt, bErr := asNumber(b); bErr == nil {
			if aIsInt && bIsInt {
				return ai == bi
			}
			ax := af
			if aIsInt {
				ax = float64(ai)
			}
			bx := bf
			if bIsInt {
				bx = float64(bi)
			}
			return ax == bx
		}
	}
	as, aOk := a.(string)
	bs, bOk := b.(string)
	if aOk && bOk {
		return as == bs
	}
	if ab, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			return ab == bb
		}
	}
	return false
}

func numCompare(a, b any, op string) bool {
	ai, af, aIsInt, aErr := asNumber(a)
	bi, bf, bIsInt, bErr := asNumber(b)
	if aErr != nil || bErr != nil {
		return false
	}
	ax := af
	if aIsInt {
		ax = float64(ai)
	}
	bx := bf
	if bIsInt {
		bx = float64(bi)
	}
	switch op {
	case "<":
		return ax < bx
	case "<=":
		return ax <= bx
	case ">":
		return ax > bx
	case ">=":
		return ax >= bx
	}
	return false
}

// regexLike is a tiny `*` glob matcher — full regex would pull in the
// `regexp` package, which is fine but heavier than what `=~` typically
// needs in JSON filters.
func regexLike(s, pat string) bool {
	if pat == "" || pat == "*" {
		return true
	}
	parts := strings.Split(pat, "*")
	pos := 0
	for i, p := range parts {
		if p == "" {
			continue
		}
		j := strings.Index(s[pos:], p)
		if j < 0 {
			return false
		}
		if i == 0 && j != 0 && !strings.HasPrefix(pat, "*") {
			return false
		}
		pos += j + len(p)
	}
	if !strings.HasSuffix(pat, "*") && pos != len(s) {
		return false
	}
	return true
}
