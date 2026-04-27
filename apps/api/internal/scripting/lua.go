package scripting

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Caller is the bridge from Lua's redis.call back to the engine. The
// scripting package never imports the engine — it only depends on this
// function shape, which keeps the dependency graph clean.
type Caller func(cmd string, args []string) (any, error)

// Run interprets src with KEYS / ARGV pre-bound, returning the script's
// final value. The deadline aborts long-running scripts so a runaway
// loop can't pin the connection forever.
//
// Run delegates to RunFull (gopher-lua) so callers get full Lua 5.1
// semantics — string/math/table libraries, metatables, coroutines.
// The original subset interpreter remains accessible as RunSubset for
// the path-coverage test suite.
func Run(src string, keys, argv []string, call Caller, deadline time.Time) (any, error) {
	return RunFull(src, keys, argv, call, deadline)
}

// RunSubset is the hand-built interpreter from the early prototype.
// Kept so the subset test suite still exercises its parser; production
// scripts go through Run → RunFull.
func RunSubset(src string, keys, argv []string, call Caller, deadline time.Time) (any, error) {
	tokens, err := tokenize(src)
	if err != nil {
		return nil, fmt.Errorf("compile error: %w", err)
	}
	parser := newParser(tokens)
	block, err := parser.parseBlock(true)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}
	env := newEnv(nil)
	env.set("KEYS", makeStringTable(keys))
	env.set("ARGV", makeStringTable(argv))
	env.set("redis", buildRedisModule(call))

	interp := &interp{call: call, deadline: deadline}
	v, err := interp.execBlock(block, env)
	if err != nil {
		if rv, ok := err.(returnSignal); ok {
			resp := luaToResp(rv.value)
			if e, ok := resp.(error); ok {
				return nil, e
			}
			return resp, nil
		}
		return nil, err
	}
	resp := luaToResp(v)
	if e, ok := resp.(error); ok {
		return nil, e
	}
	return resp, nil
}

// ─── lexer ─────────────────────────────────────────────────────────────

type tokKind int

const (
	tkIdent tokKind = iota
	tkNumber
	tkString
	tkSymbol
	tkKeyword
	tkEOF
)

type token struct {
	kind tokKind
	val  string
	line int
}

var keywords = map[string]bool{
	"and": true, "break": true, "do": true, "else": true, "elseif": true,
	"end": true, "false": true, "for": true, "function": true, "if": true,
	"in": true, "local": true, "nil": true, "not": true, "or": true,
	"repeat": true, "return": true, "then": true, "true": true, "until": true,
	"while": true,
}

func tokenize(src string) ([]token, error) {
	var out []token
	line := 1
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\n':
			line++
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			// comment to end of line; "--[[ ]]" block comments not supported
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case isDigit(c):
			j := i
			for j < len(src) && (isDigit(src[j]) || src[j] == '.') {
				j++
			}
			out = append(out, token{tkNumber, src[i:j], line})
			i = j
		case c == '"' || c == '\'':
			q := c
			j := i + 1
			var sb strings.Builder
			for j < len(src) && src[j] != q {
				if src[j] == '\\' && j+1 < len(src) {
					switch src[j+1] {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					case 'r':
						sb.WriteByte('\r')
					case '\\':
						sb.WriteByte('\\')
					case q:
						sb.WriteByte(q)
					default:
						sb.WriteByte(src[j+1])
					}
					j += 2
					continue
				}
				sb.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string at line %d", line)
			}
			out = append(out, token{tkString, sb.String(), line})
			i = j + 1
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentCont(src[j]) {
				j++
			}
			word := src[i:j]
			if keywords[word] {
				out = append(out, token{tkKeyword, word, line})
			} else {
				out = append(out, token{tkIdent, word, line})
			}
			i = j
		default:
			// 2-char symbols first
			if i+1 < len(src) {
				two := src[i : i+2]
				switch two {
				case "==", "~=", "<=", ">=", "..":
					out = append(out, token{tkSymbol, two, line})
					i += 2
					continue
				}
			}
			out = append(out, token{tkSymbol, string(c), line})
			i++
		}
	}
	out = append(out, token{tkEOF, "", line})
	return out, nil
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' }
func isIdentCont(c byte) bool  { return isIdentStart(c) || isDigit(c) }

// ─── AST ───────────────────────────────────────────────────────────────

type node interface{ astNode() }

type (
	nBlock     struct{ stmts []node }
	nLocal     struct{ names []string; values []node }
	nAssign    struct{ targets []node; values []node }
	nIf        struct{ cond node; then *nBlock; elseifs []nElseIf; els *nBlock }
	nElseIf    struct{ cond node; then *nBlock }
	nWhile     struct{ cond node; body *nBlock }
	nForNum    struct{ name string; start, stop, step node; body *nBlock }
	nForIn     struct{ names []string; exprs []node; body *nBlock }
	nReturn    struct{ values []node }
	nBreak     struct{}
	nExprStmt  struct{ e node }
	nNumber    struct{ v float64 }
	nString    struct{ v string }
	nBool      struct{ v bool }
	nNil       struct{}
	nIdent     struct{ name string }
	nIndex     struct{ obj, key node }
	nField     struct{ obj node; name string }
	nCall      struct{ fn node; args []node }
	nMethodCall struct{ obj node; name string; args []node }
	nBinop     struct{ op string; l, r node }
	nUnop      struct{ op string; e node }
	nTable     struct{ fields []tableField }
	nLength    struct{ e node }
)

type tableField struct {
	key   node // nil = array-style
	value node
}

func (*nBlock) astNode()      {}
func (*nLocal) astNode()      {}
func (*nAssign) astNode()     {}
func (*nIf) astNode()         {}
func (*nWhile) astNode()      {}
func (*nForNum) astNode()     {}
func (*nForIn) astNode()      {}
func (*nReturn) astNode()     {}
func (*nBreak) astNode()      {}
func (*nExprStmt) astNode()   {}
func (*nNumber) astNode()     {}
func (*nString) astNode()     {}
func (*nBool) astNode()       {}
func (*nNil) astNode()        {}
func (*nIdent) astNode()      {}
func (*nIndex) astNode()      {}
func (*nField) astNode()      {}
func (*nCall) astNode()       {}
func (*nMethodCall) astNode() {}
func (*nBinop) astNode()      {}
func (*nUnop) astNode()       {}
func (*nTable) astNode()      {}
func (*nLength) astNode()     {}

// ─── parser ────────────────────────────────────────────────────────────

type parser struct {
	toks []token
	pos  int
}

func newParser(toks []token) *parser { return &parser{toks: toks} }

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) advance() token {
	t := p.toks[p.pos]
	p.pos++
	return t
}
func (p *parser) check(kind tokKind, val string) bool {
	t := p.peek()
	if t.kind != kind {
		return false
	}
	return val == "" || t.val == val
}
func (p *parser) match(kind tokKind, val string) bool {
	if p.check(kind, val) {
		p.advance()
		return true
	}
	return false
}
func (p *parser) expect(kind tokKind, val string) (token, error) {
	t := p.peek()
	if t.kind != kind || (val != "" && t.val != val) {
		return t, fmt.Errorf("expected %q at line %d, got %q", val, t.line, t.val)
	}
	return p.advance(), nil
}

func (p *parser) parseBlock(top bool) (*nBlock, error) {
	b := &nBlock{}
	for {
		t := p.peek()
		if t.kind == tkEOF {
			return b, nil
		}
		if t.kind == tkKeyword {
			switch t.val {
			case "end", "else", "elseif", "until":
				return b, nil
			}
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		if s != nil {
			b.stmts = append(b.stmts, s)
		}
		if !top {
			// inside nested blocks we may have more on this line
		}
	}
}

func (p *parser) parseStmt() (node, error) {
	t := p.peek()
	if t.kind == tkSymbol && t.val == ";" {
		p.advance()
		return nil, nil
	}
	if t.kind == tkKeyword {
		switch t.val {
		case "local":
			return p.parseLocal()
		case "if":
			return p.parseIf()
		case "while":
			return p.parseWhile()
		case "for":
			return p.parseFor()
		case "return":
			return p.parseReturn()
		case "break":
			p.advance()
			return &nBreak{}, nil
		case "do":
			p.advance()
			b, err := p.parseBlock(false)
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tkKeyword, "end"); err != nil {
				return nil, err
			}
			return b, nil
		}
	}
	// assignment or expression statement.
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.check(tkSymbol, "=") {
		p.advance()
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		return &nAssign{targets: []node{e}, values: []node{val}}, nil
	}
	if p.check(tkSymbol, ",") {
		// multi-assign target list
		targets := []node{e}
		for p.match(tkSymbol, ",") {
			t, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			targets = append(targets, t)
		}
		if _, err := p.expect(tkSymbol, "="); err != nil {
			return nil, err
		}
		values := []node{}
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		for p.match(tkSymbol, ",") {
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		return &nAssign{targets: targets, values: values}, nil
	}
	return &nExprStmt{e: e}, nil
}

func (p *parser) parseLocal() (node, error) {
	p.advance() // 'local'
	names := []string{}
	first, err := p.expect(tkIdent, "")
	if err != nil {
		return nil, err
	}
	names = append(names, first.val)
	for p.match(tkSymbol, ",") {
		t, err := p.expect(tkIdent, "")
		if err != nil {
			return nil, err
		}
		names = append(names, t.val)
	}
	values := []node{}
	if p.match(tkSymbol, "=") {
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		for p.match(tkSymbol, ",") {
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
	}
	return &nLocal{names: names, values: values}, nil
}

func (p *parser) parseIf() (node, error) {
	p.advance() // 'if'
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tkKeyword, "then"); err != nil {
		return nil, err
	}
	then, err := p.parseBlock(false)
	if err != nil {
		return nil, err
	}
	out := &nIf{cond: cond, then: then}
	for p.check(tkKeyword, "elseif") {
		p.advance()
		c, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tkKeyword, "then"); err != nil {
			return nil, err
		}
		b, err := p.parseBlock(false)
		if err != nil {
			return nil, err
		}
		out.elseifs = append(out.elseifs, nElseIf{cond: c, then: b})
	}
	if p.match(tkKeyword, "else") {
		els, err := p.parseBlock(false)
		if err != nil {
			return nil, err
		}
		out.els = els
	}
	if _, err := p.expect(tkKeyword, "end"); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *parser) parseWhile() (node, error) {
	p.advance()
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tkKeyword, "do"); err != nil {
		return nil, err
	}
	body, err := p.parseBlock(false)
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tkKeyword, "end"); err != nil {
		return nil, err
	}
	return &nWhile{cond: cond, body: body}, nil
}

func (p *parser) parseFor() (node, error) {
	p.advance()
	first, err := p.expect(tkIdent, "")
	if err != nil {
		return nil, err
	}
	if p.match(tkSymbol, "=") {
		// numeric for
		start, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tkSymbol, ","); err != nil {
			return nil, err
		}
		stop, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		var step node
		if p.match(tkSymbol, ",") {
			step, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
		if _, err := p.expect(tkKeyword, "do"); err != nil {
			return nil, err
		}
		body, err := p.parseBlock(false)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tkKeyword, "end"); err != nil {
			return nil, err
		}
		return &nForNum{name: first.val, start: start, stop: stop, step: step, body: body}, nil
	}
	// for-in: names ',' ident* in exprs do body end
	names := []string{first.val}
	for p.match(tkSymbol, ",") {
		t, err := p.expect(tkIdent, "")
		if err != nil {
			return nil, err
		}
		names = append(names, t.val)
	}
	if _, err := p.expect(tkKeyword, "in"); err != nil {
		return nil, err
	}
	exprs := []node{}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	exprs = append(exprs, e)
	for p.match(tkSymbol, ",") {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, e)
	}
	if _, err := p.expect(tkKeyword, "do"); err != nil {
		return nil, err
	}
	body, err := p.parseBlock(false)
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tkKeyword, "end"); err != nil {
		return nil, err
	}
	return &nForIn{names: names, exprs: exprs, body: body}, nil
}

func (p *parser) parseReturn() (node, error) {
	p.advance()
	values := []node{}
	if p.peek().kind == tkEOF {
		return &nReturn{values: values}, nil
	}
	if p.peek().kind == tkKeyword && (p.peek().val == "end" || p.peek().val == "else" || p.peek().val == "elseif" || p.peek().val == "until") {
		return &nReturn{values: values}, nil
	}
	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	values = append(values, v)
	for p.match(tkSymbol, ",") {
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return &nReturn{values: values}, nil
}

// expression precedence climbing
var precedence = map[string]int{
	"or": 1, "and": 2,
	"==": 3, "~=": 3, "<": 3, ">": 3, "<=": 3, ">=": 3,
	"..": 4,
	"+": 5, "-": 5,
	"*": 6, "/": 6, "%": 6,
}

func (p *parser) parseExpr() (node, error) { return p.parseBinop(0) }

func (p *parser) parseBinop(min int) (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		op, isOp := p.binopName()
		prec, ok := precedence[op]
		if !isOp || !ok || prec < min {
			break
		}
		p.advance()
		right, err := p.parseBinop(prec + 1)
		if err != nil {
			return nil, err
		}
		left = &nBinop{op: op, l: left, r: right}
	}
	return left, nil
}

func (p *parser) binopName() (string, bool) {
	t := p.peek()
	if t.kind == tkSymbol {
		switch t.val {
		case "+", "-", "*", "/", "%", "..", "==", "~=", "<", ">", "<=", ">=":
			return t.val, true
		}
	}
	if t.kind == tkKeyword && (t.val == "and" || t.val == "or") {
		return t.val, true
	}
	return "", false
}

func (p *parser) parseUnary() (node, error) {
	t := p.peek()
	if t.kind == tkKeyword && t.val == "not" {
		p.advance()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &nUnop{op: "not", e: e}, nil
	}
	if t.kind == tkSymbol && t.val == "-" {
		p.advance()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &nUnop{op: "-", e: e}, nil
	}
	if t.kind == tkSymbol && t.val == "#" {
		p.advance()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &nLength{e: e}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (node, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tkSymbol && t.val == "." {
			p.advance()
			name, err := p.expect(tkIdent, "")
			if err != nil {
				return nil, err
			}
			e = &nField{obj: e, name: name.val}
			continue
		}
		if t.kind == tkSymbol && t.val == "[" {
			p.advance()
			k, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tkSymbol, "]"); err != nil {
				return nil, err
			}
			e = &nIndex{obj: e, key: k}
			continue
		}
		if t.kind == tkSymbol && t.val == ":" {
			p.advance()
			name, err := p.expect(tkIdent, "")
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tkSymbol, "("); err != nil {
				return nil, err
			}
			args, err := p.parseCallArgs()
			if err != nil {
				return nil, err
			}
			e = &nMethodCall{obj: e, name: name.val, args: args}
			continue
		}
		if t.kind == tkSymbol && t.val == "(" {
			p.advance()
			args, err := p.parseCallArgs()
			if err != nil {
				return nil, err
			}
			e = &nCall{fn: e, args: args}
			continue
		}
		if t.kind == tkString {
			// foo "bar" — single-string call sugar
			s := p.advance()
			e = &nCall{fn: e, args: []node{&nString{v: s.val}}}
			continue
		}
		if t.kind == tkSymbol && t.val == "{" {
			// foo{...} — table-arg call sugar
			tbl, err := p.parseTable()
			if err != nil {
				return nil, err
			}
			e = &nCall{fn: e, args: []node{tbl}}
			continue
		}
		break
	}
	return e, nil
}

func (p *parser) parseCallArgs() ([]node, error) {
	out := []node{}
	if p.match(tkSymbol, ")") {
		return out, nil
	}
	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	out = append(out, v)
	for p.match(tkSymbol, ",") {
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if _, err := p.expect(tkSymbol, ")"); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *parser) parsePrimary() (node, error) {
	t := p.peek()
	switch t.kind {
	case tkNumber:
		p.advance()
		f, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return nil, err
		}
		return &nNumber{v: f}, nil
	case tkString:
		p.advance()
		return &nString{v: t.val}, nil
	case tkKeyword:
		switch t.val {
		case "true":
			p.advance()
			return &nBool{v: true}, nil
		case "false":
			p.advance()
			return &nBool{v: false}, nil
		case "nil":
			p.advance()
			return &nNil{}, nil
		}
		return nil, fmt.Errorf("unexpected keyword %q", t.val)
	case tkIdent:
		p.advance()
		return &nIdent{name: t.val}, nil
	case tkSymbol:
		if t.val == "(" {
			p.advance()
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tkSymbol, ")"); err != nil {
				return nil, err
			}
			return e, nil
		}
		if t.val == "{" {
			return p.parseTable()
		}
	}
	return nil, fmt.Errorf("unexpected token %q at line %d", t.val, t.line)
}

func (p *parser) parseTable() (node, error) {
	if _, err := p.expect(tkSymbol, "{"); err != nil {
		return nil, err
	}
	tbl := &nTable{}
	for !p.check(tkSymbol, "}") {
		f := tableField{}
		if p.check(tkSymbol, "[") {
			p.advance()
			k, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tkSymbol, "]"); err != nil {
				return nil, err
			}
			if _, err := p.expect(tkSymbol, "="); err != nil {
				return nil, err
			}
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			f.key = k
			f.value = v
		} else if p.peek().kind == tkIdent && p.toks[p.pos+1].kind == tkSymbol && p.toks[p.pos+1].val == "=" {
			name := p.advance().val
			p.advance() // '='
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			f.key = &nString{v: name}
			f.value = v
		} else {
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			f.value = v
		}
		tbl.fields = append(tbl.fields, f)
		if !p.match(tkSymbol, ",") && !p.match(tkSymbol, ";") {
			break
		}
	}
	if _, err := p.expect(tkSymbol, "}"); err != nil {
		return nil, err
	}
	return tbl, nil
}

// ─── runtime values ────────────────────────────────────────────────────

// luaTable is a hybrid array+hash. Array elements live in array (1-based
// in Lua semantics; we shift in lookups) and named keys in hash.
type luaTable struct {
	array []any
	hash  map[any]any
}

func newTable() *luaTable {
	return &luaTable{hash: map[any]any{}}
}

func (t *luaTable) get(k any) any {
	if f, ok := k.(float64); ok {
		i := int(f)
		if float64(i) == f && i >= 1 && i <= len(t.array) {
			return t.array[i-1]
		}
	}
	return t.hash[k]
}

func (t *luaTable) set(k, v any) {
	if f, ok := k.(float64); ok {
		i := int(f)
		if float64(i) == f && i >= 1 {
			for len(t.array) < i {
				t.array = append(t.array, nil)
			}
			t.array[i-1] = v
			return
		}
	}
	if v == nil {
		delete(t.hash, k)
		return
	}
	t.hash[k] = v
}

func (t *luaTable) length() int { return len(t.array) }

func makeStringTable(items []string) *luaTable {
	t := newTable()
	for _, s := range items {
		t.array = append(t.array, s)
	}
	return t
}

// env is a single lexical scope; nested blocks chain via parent.
type env struct {
	vars   map[string]any
	parent *env
}

func newEnv(p *env) *env { return &env{vars: map[string]any{}, parent: p} }

func (e *env) get(name string) (any, bool) {
	if v, ok := e.vars[name]; ok {
		return v, true
	}
	if e.parent != nil {
		return e.parent.get(name)
	}
	return nil, false
}

func (e *env) set(name string, v any) {
	for s := e; s != nil; s = s.parent {
		if _, ok := s.vars[name]; ok {
			s.vars[name] = v
			return
		}
	}
	e.vars[name] = v
}

func (e *env) declare(name string, v any) { e.vars[name] = v }

// ─── interpreter ───────────────────────────────────────────────────────

type interp struct {
	call     Caller
	deadline time.Time
}

type returnSignal struct{ value any }

func (returnSignal) Error() string { return "return" }

type breakSignal struct{}

func (breakSignal) Error() string { return "break" }

func (in *interp) execBlock(b *nBlock, e *env) (any, error) {
	for _, s := range b.stmts {
		if !in.deadline.IsZero() && time.Now().After(in.deadline) {
			return nil, errors.New("script timed out")
		}
		_, err := in.execStmt(s, e)
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (in *interp) execStmt(s node, e *env) (any, error) {
	switch n := s.(type) {
	case *nLocal:
		for i, name := range n.names {
			var v any
			if i < len(n.values) {
				vv, err := in.eval(n.values[i], e)
				if err != nil {
					return nil, err
				}
				v = vv
			}
			e.declare(name, v)
		}
		return nil, nil
	case *nAssign:
		// compute right side first
		vals := make([]any, len(n.values))
		for i, v := range n.values {
			vv, err := in.eval(v, e)
			if err != nil {
				return nil, err
			}
			vals[i] = vv
		}
		for i, target := range n.targets {
			var v any
			if i < len(vals) {
				v = vals[i]
			}
			if err := in.assign(target, v, e); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case *nIf:
		c, err := in.eval(n.cond, e)
		if err != nil {
			return nil, err
		}
		if truthy(c) {
			return in.execBlock(n.then, newEnv(e))
		}
		for _, eif := range n.elseifs {
			cc, err := in.eval(eif.cond, e)
			if err != nil {
				return nil, err
			}
			if truthy(cc) {
				return in.execBlock(eif.then, newEnv(e))
			}
		}
		if n.els != nil {
			return in.execBlock(n.els, newEnv(e))
		}
		return nil, nil
	case *nWhile:
		for {
			c, err := in.eval(n.cond, e)
			if err != nil {
				return nil, err
			}
			if !truthy(c) {
				return nil, nil
			}
			_, err = in.execBlock(n.body, newEnv(e))
			if err != nil {
				if _, ok := err.(breakSignal); ok {
					return nil, nil
				}
				return nil, err
			}
		}
	case *nForNum:
		startV, err := in.eval(n.start, e)
		if err != nil {
			return nil, err
		}
		stopV, err := in.eval(n.stop, e)
		if err != nil {
			return nil, err
		}
		step := 1.0
		if n.step != nil {
			sv, err := in.eval(n.step, e)
			if err != nil {
				return nil, err
			}
			step = toNumber(sv)
		}
		i := toNumber(startV)
		stop := toNumber(stopV)
		for (step > 0 && i <= stop) || (step < 0 && i >= stop) {
			scope := newEnv(e)
			scope.declare(n.name, i)
			_, err := in.execBlock(n.body, scope)
			if err != nil {
				if _, ok := err.(breakSignal); ok {
					return nil, nil
				}
				return nil, err
			}
			i += step
		}
		return nil, nil
	case *nForIn:
		// minimal: support pairs(t) and ipairs(t).
		if len(n.exprs) == 0 {
			return nil, errors.New("for-in needs iterator")
		}
		iterArg, err := in.eval(n.exprs[0], e)
		if err != nil {
			return nil, err
		}
		t, ok := iterArg.(*luaTable)
		if !ok {
			return nil, errors.New("for-in: only table iteration supported")
		}
		// ipairs-style array iteration
		for i, v := range t.array {
			scope := newEnv(e)
			if len(n.names) >= 1 {
				scope.declare(n.names[0], float64(i+1))
			}
			if len(n.names) >= 2 {
				scope.declare(n.names[1], v)
			}
			_, err := in.execBlock(n.body, scope)
			if err != nil {
				if _, ok := err.(breakSignal); ok {
					return nil, nil
				}
				return nil, err
			}
		}
		// then hash entries (pairs-style)
		for k, v := range t.hash {
			scope := newEnv(e)
			if len(n.names) >= 1 {
				scope.declare(n.names[0], k)
			}
			if len(n.names) >= 2 {
				scope.declare(n.names[1], v)
			}
			_, err := in.execBlock(n.body, scope)
			if err != nil {
				if _, ok := err.(breakSignal); ok {
					return nil, nil
				}
				return nil, err
			}
		}
		return nil, nil
	case *nReturn:
		if len(n.values) == 0 {
			return nil, returnSignal{value: nil}
		}
		// multi-value returns aren't part of the subset; collapse to first.
		v, err := in.eval(n.values[0], e)
		if err != nil {
			return nil, err
		}
		return nil, returnSignal{value: v}
	case *nBreak:
		return nil, breakSignal{}
	case *nExprStmt:
		_, err := in.eval(n.e, e)
		return nil, err
	case *nBlock:
		return in.execBlock(n, newEnv(e))
	}
	return nil, fmt.Errorf("unsupported statement %T", s)
}

func (in *interp) assign(target node, v any, e *env) error {
	switch t := target.(type) {
	case *nIdent:
		e.set(t.name, v)
	case *nIndex:
		obj, err := in.eval(t.obj, e)
		if err != nil {
			return err
		}
		k, err := in.eval(t.key, e)
		if err != nil {
			return err
		}
		tbl, ok := obj.(*luaTable)
		if !ok {
			return errors.New("attempt to index non-table")
		}
		tbl.set(k, v)
	case *nField:
		obj, err := in.eval(t.obj, e)
		if err != nil {
			return err
		}
		tbl, ok := obj.(*luaTable)
		if !ok {
			return errors.New("attempt to index non-table")
		}
		tbl.set(t.name, v)
	default:
		return errors.New("invalid assignment target")
	}
	return nil
}

func (in *interp) eval(n node, e *env) (any, error) {
	switch x := n.(type) {
	case *nNumber:
		return x.v, nil
	case *nString:
		return x.v, nil
	case *nBool:
		return x.v, nil
	case *nNil:
		return nil, nil
	case *nIdent:
		v, _ := e.get(x.name)
		return v, nil
	case *nLength:
		v, err := in.eval(x.e, e)
		if err != nil {
			return nil, err
		}
		if t, ok := v.(*luaTable); ok {
			return float64(t.length()), nil
		}
		if s, ok := v.(string); ok {
			return float64(len(s)), nil
		}
		return float64(0), nil
	case *nField:
		obj, err := in.eval(x.obj, e)
		if err != nil {
			return nil, err
		}
		t, ok := obj.(*luaTable)
		if !ok {
			return nil, nil
		}
		return t.get(x.name), nil
	case *nIndex:
		obj, err := in.eval(x.obj, e)
		if err != nil {
			return nil, err
		}
		k, err := in.eval(x.key, e)
		if err != nil {
			return nil, err
		}
		t, ok := obj.(*luaTable)
		if !ok {
			return nil, nil
		}
		return t.get(k), nil
	case *nCall:
		fn, err := in.eval(x.fn, e)
		if err != nil {
			return nil, err
		}
		args := make([]any, len(x.args))
		for i, a := range x.args {
			v, err := in.eval(a, e)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		return invoke(fn, args)
	case *nMethodCall:
		obj, err := in.eval(x.obj, e)
		if err != nil {
			return nil, err
		}
		t, ok := obj.(*luaTable)
		if !ok {
			return nil, errors.New("method call on non-table")
		}
		fn := t.get(x.name)
		args := make([]any, 0, len(x.args)+1)
		args = append(args, obj)
		for _, a := range x.args {
			v, err := in.eval(a, e)
			if err != nil {
				return nil, err
			}
			args = append(args, v)
		}
		return invoke(fn, args)
	case *nBinop:
		return in.evalBinop(x, e)
	case *nUnop:
		v, err := in.eval(x.e, e)
		if err != nil {
			return nil, err
		}
		switch x.op {
		case "-":
			return -toNumber(v), nil
		case "not":
			return !truthy(v), nil
		}
		return nil, fmt.Errorf("unknown unary %q", x.op)
	case *nTable:
		t := newTable()
		idx := 1
		for _, f := range x.fields {
			val, err := in.eval(f.value, e)
			if err != nil {
				return nil, err
			}
			if f.key == nil {
				t.set(float64(idx), val)
				idx++
				continue
			}
			k, err := in.eval(f.key, e)
			if err != nil {
				return nil, err
			}
			t.set(k, val)
		}
		return t, nil
	}
	return nil, fmt.Errorf("eval: unsupported %T", n)
}

func (in *interp) evalBinop(b *nBinop, e *env) (any, error) {
	if b.op == "and" {
		l, err := in.eval(b.l, e)
		if err != nil {
			return nil, err
		}
		if !truthy(l) {
			return l, nil
		}
		return in.eval(b.r, e)
	}
	if b.op == "or" {
		l, err := in.eval(b.l, e)
		if err != nil {
			return nil, err
		}
		if truthy(l) {
			return l, nil
		}
		return in.eval(b.r, e)
	}
	l, err := in.eval(b.l, e)
	if err != nil {
		return nil, err
	}
	r, err := in.eval(b.r, e)
	if err != nil {
		return nil, err
	}
	switch b.op {
	case "+":
		return toNumber(l) + toNumber(r), nil
	case "-":
		return toNumber(l) - toNumber(r), nil
	case "*":
		return toNumber(l) * toNumber(r), nil
	case "/":
		rn := toNumber(r)
		if rn == 0 {
			return nil, errors.New("division by zero")
		}
		return toNumber(l) / rn, nil
	case "%":
		rn := toNumber(r)
		if rn == 0 {
			return nil, errors.New("modulo by zero")
		}
		return float64(int64(toNumber(l)) % int64(rn)), nil
	case "..":
		return toString(l) + toString(r), nil
	case "==":
		return equal(l, r), nil
	case "~=":
		return !equal(l, r), nil
	case "<":
		return compare(l, r) < 0, nil
	case ">":
		return compare(l, r) > 0, nil
	case "<=":
		return compare(l, r) <= 0, nil
	case ">=":
		return compare(l, r) >= 0, nil
	}
	return nil, fmt.Errorf("unknown binop %q", b.op)
}

// ─── value helpers ─────────────────────────────────────────────────────

func truthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func toNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

func equal(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	switch x := a.(type) {
	case float64:
		return x == toNumber(b)
	case string:
		if y, ok := b.(string); ok {
			return x == y
		}
	case bool:
		if y, ok := b.(bool); ok {
			return x == y
		}
	}
	return false
}

func compare(a, b any) int {
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return strings.Compare(as, bs)
		}
	}
	an, bn := toNumber(a), toNumber(b)
	if an < bn {
		return -1
	}
	if an > bn {
		return 1
	}
	return 0
}

// ─── functions / standard library ──────────────────────────────────────

// goFunc is the bridge type for Go-implemented functions invoked from Lua.
type goFunc func(args []any) (any, error)

func invoke(fn any, args []any) (any, error) {
	if g, ok := fn.(goFunc); ok {
		return g(args)
	}
	return nil, errors.New("attempt to call a non-function")
}

// buildRedisModule creates the redis.* table with .call, .pcall,
// .error_reply, .status_reply, .sha1hex, .log.
func buildRedisModule(call Caller) *luaTable {
	t := newTable()
	t.set("call", goFunc(func(args []any) (any, error) {
		return doRedisCall(call, args, false)
	}))
	t.set("pcall", goFunc(func(args []any) (any, error) {
		v, err := doRedisCall(call, args, true)
		if err != nil {
			et := newTable()
			et.set("err", err.Error())
			return et, nil
		}
		return v, nil
	}))
	t.set("error_reply", goFunc(func(args []any) (any, error) {
		et := newTable()
		if len(args) > 0 {
			et.set("err", toString(args[0]))
		}
		return et, nil
	}))
	t.set("status_reply", goFunc(func(args []any) (any, error) {
		st := newTable()
		if len(args) > 0 {
			st.set("ok", toString(args[0]))
		}
		return st, nil
	}))
	t.set("sha1hex", goFunc(func(args []any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		c := NewCache()
		return c.Load(toString(args[0])), nil
	}))
	t.set("log", goFunc(func(args []any) (any, error) { return nil, nil }))
	t.set("LOG_DEBUG", float64(0))
	t.set("LOG_VERBOSE", float64(1))
	t.set("LOG_NOTICE", float64(2))
	t.set("LOG_WARNING", float64(3))
	return t
}

func doRedisCall(call Caller, args []any, pcall bool) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("redis.call: missing command")
	}
	cmd := strings.ToUpper(toString(args[0]))
	rest := make([]string, 0, len(args)-1)
	for _, a := range args[1:] {
		rest = append(rest, toString(a))
	}
	v, err := call(cmd, rest)
	if err != nil {
		if pcall {
			return nil, err
		}
		return nil, err
	}
	return respToLua(v), nil
}

// respToLua adapts the engine's reply types into Lua values. Slices
// become 1-indexed tables; booleans become 0/1 (matching Redis).
func respToLua(v any) any {
	switch x := v.(type) {
	case nil:
		return false // Redis nil reply is mapped to false in Lua
	case bool:
		if x {
			return float64(1)
		}
		return false
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	case string:
		return x
	case []string:
		t := newTable()
		for _, s := range x {
			t.array = append(t.array, s)
		}
		return t
	case []any:
		t := newTable()
		for _, e := range x {
			t.array = append(t.array, respToLua(e))
		}
		return t
	}
	return v
}

// luaToResp converts a script's return value into a shape the RESP
// layer can encode.
func luaToResp(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool:
		if x {
			return int64(1)
		}
		return nil
	case float64:
		// integers stay integers in RESP land
		if x == float64(int64(x)) {
			return int64(x)
		}
		return toString(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		return x
	case *luaTable:
		// error/status table?
		if e := x.get("err"); e != nil {
			return errors.New(toString(e))
		}
		if s := x.get("ok"); s != nil {
			return toString(s)
		}
		// dense array → []any
		out := make([]any, len(x.array))
		for i, v := range x.array {
			out[i] = luaToResp(v)
		}
		return out
	}
	return v
}
