package searchmod

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// AggResult is the row stream produced by an aggregation pipeline.
// Each row is a property bag — pipeline stages transform them in
// order (GROUPBY, REDUCE, APPLY, SORTBY, LIMIT).
type AggResult struct {
	Rows []map[string]string
}

// AggPipeline parses + executes an FT.AGGREGATE pipeline on the hits
// produced by the initial query.
type AggPipeline struct {
	Stages []aggStage
}

type aggStage interface {
	apply(in *AggResult) *AggResult
}

// ── pipeline parser ──────────────────────────────────────────────

// ParseAggPipeline reads pipeline tokens after the query string.
// Recognised stages: GROUPBY, REDUCE, APPLY, SORTBY, LIMIT.
func ParseAggPipeline(args []string) (*AggPipeline, error) {
	p := &AggPipeline{}
	i := 0
	for i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "GROUPBY":
			if i+1 >= len(args) {
				return nil, errors.New("GROUPBY: missing nargs")
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+2+n > len(args) {
				return nil, errors.New("GROUPBY: too few keys")
			}
			keys := args[i+2 : i+2+n]
			gs := &groupByStage{keys: keys}
			i += 2 + n
			// consume any inline REDUCE clauses
			for i < len(args) && strings.EqualFold(args[i], "REDUCE") {
				if i+3 >= len(args) {
					return nil, errors.New("REDUCE: too few args")
				}
				fn := strings.ToUpper(args[i+1])
				rn, _ := strconv.Atoi(args[i+2])
				rargs := args[i+3 : i+3+rn]
				i += 3 + rn
				alias := fn
				if i+1 < len(args) && strings.EqualFold(args[i], "AS") {
					alias = args[i+1]
					i += 2
				}
				gs.reducers = append(gs.reducers, reducer{fn: fn, args: rargs, alias: alias})
			}
			p.Stages = append(p.Stages, gs)
		case "REDUCE":
			return nil, errors.New("REDUCE outside of GROUPBY")
		case "APPLY":
			if i+1 >= len(args) {
				return nil, errors.New("APPLY: missing expression")
			}
			expr := args[i+1]
			i += 2
			alias := ""
			if i+1 < len(args) && strings.EqualFold(args[i], "AS") {
				alias = args[i+1]
				i += 2
			}
			if alias == "" {
				return nil, errors.New("APPLY: missing AS alias")
			}
			p.Stages = append(p.Stages, &applyStage{expr: expr, alias: alias})
		case "SORTBY":
			if i+1 >= len(args) {
				return nil, errors.New("SORTBY: missing nargs")
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+2+n > len(args) {
				return nil, errors.New("SORTBY: too few args")
			}
			tokens := args[i+2 : i+2+n]
			ss := &sortByStage{}
			for j := 0; j < len(tokens); j++ {
				field := strings.TrimPrefix(tokens[j], "@")
				dir := true // ASC default
				if j+1 < len(tokens) && (strings.EqualFold(tokens[j+1], "ASC") || strings.EqualFold(tokens[j+1], "DESC")) {
					dir = strings.EqualFold(tokens[j+1], "ASC")
					j++
				}
				ss.fields = append(ss.fields, sortField{name: field, asc: dir})
			}
			i += 2 + n
		case "LIMIT":
			if i+2 >= len(args) {
				return nil, errors.New("LIMIT: needs offset + count")
			}
			off, _ := strconv.Atoi(args[i+1])
			cnt, _ := strconv.Atoi(args[i+2])
			p.Stages = append(p.Stages, &limitStage{offset: off, count: cnt})
			i += 3
		case "FILTER":
			// FT.AGGREGATE FILTER expr — drop every row where the
			// expression evaluates to 0 / empty / false. The expression
			// language reuses the APPLY evaluator (field refs, numeric
			// literals, arithmetic), with comparison operators added.
			if i+1 >= len(args) {
				return nil, errors.New("FILTER: missing expression")
			}
			p.Stages = append(p.Stages, &filterStage{expr: args[i+1]})
			i += 2
		default:
			return nil, errors.New("unknown aggregate stage: " + args[i])
		}
	}
	return p, nil
}

// Run executes every stage in order against the seed rows.
func (p *AggPipeline) Run(seed *AggResult) *AggResult {
	cur := seed
	for _, s := range p.Stages {
		cur = s.apply(cur)
	}
	return cur
}

// HitsToAggResult turns SearchHits into the row representation the
// pipeline operates on.
func HitsToAggResult(hits []SearchHit) *AggResult {
	out := &AggResult{Rows: make([]map[string]string, 0, len(hits))}
	for _, h := range hits {
		row := map[string]string{"__id": h.DocID, "__score": strconv.FormatFloat(h.Score, 'f', -1, 64)}
		for k, v := range h.Doc.Fields {
			row[k] = v
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// ── stages ────────────────────────────────────────────────────────

type reducer struct {
	fn    string
	args  []string
	alias string
}

type groupByStage struct {
	keys     []string
	reducers []reducer
}

func (g *groupByStage) apply(in *AggResult) *AggResult {
	if len(g.keys) == 0 {
		// global aggregation: one bucket
		bucket := map[string]string{}
		for _, r := range g.reducers {
			bucket[r.alias] = computeReducer(in.Rows, r)
		}
		return &AggResult{Rows: []map[string]string{bucket}}
	}
	groups := map[string][]map[string]string{}
	order := []string{}
	for _, row := range in.Rows {
		key := makeGroupKey(row, g.keys)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	out := &AggResult{Rows: make([]map[string]string, 0, len(order))}
	for _, k := range order {
		rows := groups[k]
		bucket := map[string]string{}
		for _, kf := range g.keys {
			field := strings.TrimPrefix(kf, "@")
			bucket[field] = rows[0][field]
		}
		for _, r := range g.reducers {
			bucket[r.alias] = computeReducer(rows, r)
		}
		out.Rows = append(out.Rows, bucket)
	}
	return out
}

func makeGroupKey(row map[string]string, keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = row[strings.TrimPrefix(k, "@")]
	}
	return strings.Join(parts, "\x00")
}

func computeReducer(rows []map[string]string, r reducer) string {
	switch r.fn {
	case "COUNT":
		return strconv.Itoa(len(rows))
	case "SUM":
		f := strings.TrimPrefix(r.args[0], "@")
		var sum float64
		for _, row := range rows {
			v, _ := strconv.ParseFloat(row[f], 64)
			sum += v
		}
		return strconv.FormatFloat(sum, 'f', -1, 64)
	case "MIN":
		f := strings.TrimPrefix(r.args[0], "@")
		var min float64
		first := true
		for _, row := range rows {
			v, _ := strconv.ParseFloat(row[f], 64)
			if first || v < min {
				min = v
				first = false
			}
		}
		return strconv.FormatFloat(min, 'f', -1, 64)
	case "MAX":
		f := strings.TrimPrefix(r.args[0], "@")
		var max float64
		first := true
		for _, row := range rows {
			v, _ := strconv.ParseFloat(row[f], 64)
			if first || v > max {
				max = v
				first = false
			}
		}
		return strconv.FormatFloat(max, 'f', -1, 64)
	case "AVG":
		f := strings.TrimPrefix(r.args[0], "@")
		var sum float64
		var n int
		for _, row := range rows {
			v, err := strconv.ParseFloat(row[f], 64)
			if err == nil {
				sum += v
				n++
			}
		}
		if n == 0 {
			return "0"
		}
		return strconv.FormatFloat(sum/float64(n), 'f', -1, 64)
	case "COUNT_DISTINCT":
		f := strings.TrimPrefix(r.args[0], "@")
		seen := map[string]struct{}{}
		for _, row := range rows {
			seen[row[f]] = struct{}{}
		}
		return strconv.Itoa(len(seen))
	case "FIRST_VALUE":
		f := strings.TrimPrefix(r.args[0], "@")
		if len(rows) == 0 {
			return ""
		}
		return rows[0][f]
	case "TOLIST":
		f := strings.TrimPrefix(r.args[0], "@")
		seen := map[string]struct{}{}
		out := []string{}
		for _, row := range rows {
			v := row[f]
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
		return strings.Join(out, ",")
	}
	return ""
}

type sortField struct {
	name string
	asc  bool
}

type sortByStage struct {
	fields []sortField
}

func (s *sortByStage) apply(in *AggResult) *AggResult {
	sort.SliceStable(in.Rows, func(i, j int) bool {
		for _, sf := range s.fields {
			a, b := in.Rows[i][sf.name], in.Rows[j][sf.name]
			af, errA := strconv.ParseFloat(a, 64)
			bf, errB := strconv.ParseFloat(b, 64)
			if errA == nil && errB == nil {
				if af != bf {
					if sf.asc {
						return af < bf
					}
					return af > bf
				}
				continue
			}
			if a != b {
				if sf.asc {
					return a < b
				}
				return a > b
			}
		}
		return false
	})
	return in
}

type limitStage struct {
	offset, count int
}

func (l *limitStage) apply(in *AggResult) *AggResult {
	if l.offset >= len(in.Rows) {
		return &AggResult{Rows: []map[string]string{}}
	}
	end := l.offset + l.count
	if end > len(in.Rows) {
		end = len(in.Rows)
	}
	in.Rows = in.Rows[l.offset:end]
	return in
}

// filterStage drops rows where evalFilterExpr returns false. The
// expression grammar is APPLY's arithmetic + comparison ops + the
// boolean connectives && and ||; we don't need full SQL semantics, just
// enough to express "@price > 10 && @qty > 0".
type filterStage struct {
	expr string
}

func (f *filterStage) apply(in *AggResult) *AggResult {
	out := &AggResult{Rows: in.Rows[:0]}
	for _, row := range in.Rows {
		if evalFilterBool(f.expr, row) {
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

// evalFilterBool returns true when the expression is non-zero / true.
// Comparisons return 1 / 0; arithmetic results > 0 are truthy.
func evalFilterBool(expr string, row map[string]string) bool {
	v := evalFilterExpr(expr, row)
	return v != 0
}

// evalFilterExpr extends the APPLY evaluator with comparison + boolean
// operators. We add a small layer above the existing exprParser rather
// than rewrite the whole grammar.
func evalFilterExpr(expr string, row map[string]string) float64 {
	p := &filterExprParser{base: exprParser{src: strings.TrimSpace(expr), row: row}}
	return p.parseOr()
}

type filterExprParser struct {
	base exprParser
}

func (p *filterExprParser) parseOr() float64 {
	v := p.parseAnd()
	for {
		p.base.skipSpace()
		if p.base.pos+1 < len(p.base.src) && p.base.src[p.base.pos] == '|' && p.base.src[p.base.pos+1] == '|' {
			p.base.pos += 2
			r := p.parseAnd()
			if v != 0 || r != 0 {
				v = 1
			} else {
				v = 0
			}
			continue
		}
		return v
	}
}

func (p *filterExprParser) parseAnd() float64 {
	v := p.parseCompare()
	for {
		p.base.skipSpace()
		if p.base.pos+1 < len(p.base.src) && p.base.src[p.base.pos] == '&' && p.base.src[p.base.pos+1] == '&' {
			p.base.pos += 2
			r := p.parseCompare()
			if v != 0 && r != 0 {
				v = 1
			} else {
				v = 0
			}
			continue
		}
		return v
	}
}

func (p *filterExprParser) parseCompare() float64 {
	left := p.base.parseExpr()
	p.base.skipSpace()
	if p.base.pos >= len(p.base.src) {
		return left
	}
	op := ""
	switch {
	case p.base.pos+1 < len(p.base.src) && p.base.src[p.base.pos] == '=' && p.base.src[p.base.pos+1] == '=':
		op = "=="
		p.base.pos += 2
	case p.base.pos+1 < len(p.base.src) && p.base.src[p.base.pos] == '!' && p.base.src[p.base.pos+1] == '=':
		op = "!="
		p.base.pos += 2
	case p.base.pos+1 < len(p.base.src) && p.base.src[p.base.pos] == '<' && p.base.src[p.base.pos+1] == '=':
		op = "<="
		p.base.pos += 2
	case p.base.pos+1 < len(p.base.src) && p.base.src[p.base.pos] == '>' && p.base.src[p.base.pos+1] == '=':
		op = ">="
		p.base.pos += 2
	case p.base.src[p.base.pos] == '<':
		op = "<"
		p.base.pos++
	case p.base.src[p.base.pos] == '>':
		op = ">"
		p.base.pos++
	default:
		return left
	}
	right := p.base.parseExpr()
	switch op {
	case "==":
		if left == right {
			return 1
		}
	case "!=":
		if left != right {
			return 1
		}
	case "<":
		if left < right {
			return 1
		}
	case "<=":
		if left <= right {
			return 1
		}
	case ">":
		if left > right {
			return 1
		}
	case ">=":
		if left >= right {
			return 1
		}
	}
	return 0
}

type applyStage struct {
	expr  string
	alias string
}

// apply evaluates the expression for each row and stores the result
// under alias. The expression language is intentionally tiny — field
// references (@field), numeric literals, + - * / and parentheses.
func (a *applyStage) apply(in *AggResult) *AggResult {
	for _, row := range in.Rows {
		val := evalApplyExpr(a.expr, row)
		row[a.alias] = val
	}
	return in
}

// evalApplyExpr parses + evaluates the APPLY expression. We use a tiny
// recursive descent parser so we don't pull in a dependency.
func evalApplyExpr(expr string, row map[string]string) string {
	p := &exprParser{src: strings.TrimSpace(expr), row: row}
	v := p.parseExpr()
	if v == 0 && p.lastIsString != "" {
		return p.lastIsString
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

type exprParser struct {
	src           string
	pos           int
	row           map[string]string
	lastIsString  string
}

func (p *exprParser) parseExpr() float64 { return p.parseSum() }

func (p *exprParser) parseSum() float64 {
	v := p.parseProduct()
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return v
		}
		op := p.src[p.pos]
		if op != '+' && op != '-' {
			return v
		}
		p.pos++
		right := p.parseProduct()
		if op == '+' {
			v += right
		} else {
			v -= right
		}
	}
}

func (p *exprParser) parseProduct() float64 {
	v := p.parseAtom()
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return v
		}
		op := p.src[p.pos]
		if op != '*' && op != '/' {
			return v
		}
		p.pos++
		right := p.parseAtom()
		if op == '*' {
			v *= right
		} else if right != 0 {
			v /= right
		}
	}
}

func (p *exprParser) parseAtom() float64 {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return 0
	}
	c := p.src[p.pos]
	if c == '(' {
		p.pos++
		v := p.parseExpr()
		p.skipSpace()
		if p.pos < len(p.src) && p.src[p.pos] == ')' {
			p.pos++
		}
		return v
	}
	if c == '@' {
		p.pos++
		start := p.pos
		for p.pos < len(p.src) && (isIdentByte(p.src[p.pos])) {
			p.pos++
		}
		field := p.src[start:p.pos]
		v, err := strconv.ParseFloat(p.row[field], 64)
		if err != nil {
			p.lastIsString = p.row[field]
		}
		return v
	}
	start := p.pos
	for p.pos < len(p.src) && (isNumByte(p.src[p.pos]) || p.src[p.pos] == '.') {
		p.pos++
	}
	v, _ := strconv.ParseFloat(p.src[start:p.pos], 64)
	return v
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}
func isNumByte(c byte) bool { return c >= '0' && c <= '9' }
