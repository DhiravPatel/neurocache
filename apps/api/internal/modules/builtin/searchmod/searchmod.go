package searchmod

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// Module is the registration entry. main wires it via side-effect
// import of internal/modules/builtin/searchmod.
var Module = modules.Module{
	Name:        "search",
	Version:     "2.0.0",
	Description: "RediSearch-compatible FT.* surface (subset — see package doc)",
	Init:        initModule,
}

func init() { modules.RegisterAvailable(Module) }

// indexes is the per-process registry of FT indexes. Indexes live
// here (not as module-typed engine values) because they cross-cut
// many keys: an index spans every document key matching its prefix.
var (
	indexMu sync.RWMutex
	indexes = map[string]*Index{}
)

// indexNames returns every known index name in stable order.
func indexNames() []string {
	indexMu.RLock()
	defer indexMu.RUnlock()
	out := make([]string, 0, len(indexes))
	for n := range indexes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func getIndex(name string) (*Index, bool) {
	indexMu.RLock()
	defer indexMu.RUnlock()
	idx, ok := indexes[name]
	return idx, ok
}

func setIndex(name string, idx *Index) {
	indexMu.Lock()
	indexes[name] = idx
	indexMu.Unlock()
}

func dropIndex(name string) bool {
	indexMu.Lock()
	defer indexMu.Unlock()
	if _, ok := indexes[name]; !ok {
		return false
	}
	delete(indexes, name)
	return true
}

func initModule(ctx *modules.RegisterCtx) error {
	r := []string{acl.CatRead, acl.CatSlow}
	w := []string{acl.CatWrite, acl.CatSlow}
	for _, c := range []modules.Cmd{
		{Name: "FT.CREATE", Arity: -5, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftCreate},
		{Name: "FT.DROPINDEX", Arity: -2, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftDropIndex},
		{Name: "FT.ALTER", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftAlter},
		{Name: "FT.ADD", Arity: -5, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftAdd},
		{Name: "FT.DEL", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftDel},
		{Name: "FT.GET", Arity: 3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftGet},
		{Name: "FT.SEARCH", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftSearch},
		{Name: "FT.AGGREGATE", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftAggregate},
		{Name: "FT.EXPLAIN", Arity: 3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftExplain},
		{Name: "FT.INFO", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftInfo},
		{Name: "FT._LIST", Arity: 1, Categories: r, KeyPosition: modules.KeyNone, Run: ftList},

		// Suggestions
		{Name: "FT.SUGADD", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftSugAdd},
		{Name: "FT.SUGGET", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftSugGet},
		{Name: "FT.SUGDEL", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftSugDel},
		{Name: "FT.SUGLEN", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftSugLen},

		// Synonyms
		{Name: "FT.SYNUPDATE", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: ftSynUpdate},
		{Name: "FT.SYNDUMP", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftSynDump},

		// Spellcheck
		{Name: "FT.SPELLCHECK", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftSpellCheck},

		// Cursor
		{Name: "FT.CURSOR", Arity: -3, Categories: r, KeyPosition: modules.KeyNone, Run: ftCursor},

		// Profile
		{Name: "FT.PROFILE", Arity: -4, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftProfile},

		// Aliases
		{Name: "FT.ALIASADD", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyNone, Run: ftAliasAdd},
		{Name: "FT.ALIASUPDATE", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyNone, Run: ftAliasUpdate},
		{Name: "FT.ALIASDEL", Arity: 2, Write: true, Categories: w, KeyPosition: modules.KeyNone, Run: ftAliasDel},

		// Custom dictionaries (for spellcheck INCLUDE/EXCLUDE)
		{Name: "FT.DICTADD", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyNone, Run: ftDictAdd},
		{Name: "FT.DICTDEL", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyNone, Run: ftDictDel},
		{Name: "FT.DICTDUMP", Arity: 2, Categories: r, KeyPosition: modules.KeyNone, Run: ftDictDump},

		// Tag enumeration
		{Name: "FT.TAGVALS", Arity: 3, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftTagVals},

		// Runtime config
		{Name: "FT.CONFIG", Arity: -2, Write: true, Categories: w, KeyPosition: modules.KeyNone, Run: ftConfig},

		// Hybrid sparse+dense retrieval
		{Name: "FT.HYBRID", Arity: -7, Categories: r, KeyPosition: modules.KeyAt(1), Run: ftHybrid},
	} {
		if err := ctx.RegisterCmd(c); err != nil {
			return err
		}
	}
	return nil
}

// FT.CREATE index ON HASH PREFIX 1 prefix SCHEMA name TYPE [flags] ...
//
// The subset honours `ON HASH` (always implied), `PREFIX n p1 ... pn`,
// and `SCHEMA ...`. JSON-on indexing is deferred to a later session.
func ftCreate(c *modules.Ctx, args []string) error {
	if len(args) < 4 {
		c.Reply.Error("wrong number of arguments for 'ft.create'")
		return nil
	}
	name := args[0]
	if _, exists := getIndex(name); exists {
		c.Reply.Error("Index already exists")
		return nil
	}
	i := 1
	prefixes := []string{}
	schemaAt := -1
	for i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "ON":
			i += 2 // accept HASH (or anything else; we don't enforce)
		case "PREFIX":
			if i+1 >= len(args) {
				c.Reply.Error("PREFIX needs count")
				return nil
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+2+n > len(args) {
				c.Reply.Error("PREFIX too few args")
				return nil
			}
			prefixes = append(prefixes, args[i+2:i+2+n]...)
			i += 2 + n
		case "SCHEMA":
			schemaAt = i + 1
			i = len(args)
		case "LANGUAGE", "STOPWORDS", "SCORE":
			i += 2 // accept and ignore
		case "MAXTEXTFIELDS", "TEMPORARY":
			i++
		default:
			i++
		}
	}
	if schemaAt < 0 {
		c.Reply.Error("missing SCHEMA")
		return nil
	}
	schema, err := ParseSchema(args[schemaAt:])
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	idx := NewIndex(name, schema)
	setIndex(name, idx)
	_ = prefixes // wired into the upcoming auto-index hook; today we
	// require explicit FT.ADD calls.
	c.Reply.SimpleString("OK")
	return nil
}

// FT.DROPINDEX index [DD]
func ftDropIndex(c *modules.Ctx, args []string) error {
	name := args[0]
	if !dropIndex(name) {
		c.Reply.Error("Unknown index")
		return nil
	}
	// Sweep dangling aliases — they would otherwise resolve to a
	// non-existent index and surface a confusing "Unknown index"
	// error on the next FT.SEARCH.
	aliasMu.Lock()
	for a, target := range aliases {
		if target == name {
			delete(aliases, a)
		}
	}
	aliasMu.Unlock()
	c.Reply.SimpleString("OK")
	return nil
}

// FT.ALTER index SCHEMA ADD field type [flags ...]
func ftAlter(c *modules.Ctx, args []string) error {
	if len(args) < 4 || !strings.EqualFold(args[1], "SCHEMA") || !strings.EqualFold(args[2], "ADD") {
		c.Reply.Error("FT.ALTER index SCHEMA ADD field type [flags ...]")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	schema, err := ParseSchema(args[3:])
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	indexMu.Lock()
	for _, f := range schema.Fields {
		idx.Schema.Fields = append(idx.Schema.Fields, f)
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
	indexMu.Unlock()
	c.Reply.SimpleString("OK")
	return nil
}

// FT.ADD index docID score [REPLACE] FIELDS field value [field value ...]
//
// We model documents as flat hashes (FT.ADD) so callers don't need a
// separate data type. A future enhancement will auto-index Hash keys
// matching the index PREFIX clause without an explicit FT.ADD.
func ftAdd(c *modules.Ctx, args []string) error {
	if len(args) < 5 {
		c.Reply.Error("wrong number of arguments for 'ft.add'")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	docID := args[1]
	score, err := strconv.ParseFloat(args[2], 64)
	if err != nil {
		c.Reply.Error("invalid score")
		return nil
	}
	fieldsAt := -1
	for i := 3; i < len(args); i++ {
		if strings.EqualFold(args[i], "FIELDS") {
			fieldsAt = i + 1
			break
		}
	}
	if fieldsAt < 0 {
		c.Reply.Error("missing FIELDS")
		return nil
	}
	rest := args[fieldsAt:]
	if len(rest)%2 != 0 {
		c.Reply.Error("FIELDS expects key/value pairs")
		return nil
	}
	fields := map[string]string{}
	for i := 0; i+1 < len(rest); i += 2 {
		fields[rest[i]] = rest[i+1]
	}
	idx.AddDoc(docID, fields, score)
	c.Reply.SimpleString("OK")
	return nil
}

// FT.DEL index docID
func ftDel(c *modules.Ctx, args []string) error {
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	if idx.DelDoc(args[1]) {
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

// FT.GET index docID
func ftGet(c *modules.Ctx, args []string) error {
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.NilArray()
		return nil
	}
	doc, ok := idx.Doc(args[1])
	if !ok {
		c.Reply.NilArray()
		return nil
	}
	out := []any{}
	for k, v := range doc.Fields {
		out = append(out, k, v)
	}
	c.Reply.Array(out)
	return nil
}

// FT.SEARCH index query [NOCONTENT] [WITHSCORES]
//   [LIMIT offset num] [SORTBY field [ASC|DESC]] [RETURN n field ...]
func ftSearch(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("wrong number of arguments for 'ft.search'")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	q, err := ParseQuery(args[1])
	if err != nil {
		c.Reply.Error("Syntax error: " + err.Error())
		return nil
	}
	noContent, withScores := false, false
	limitOff, limitCount := 0, 10
	var sortField string
	sortAsc := true
	var returnFields []string
	returnAliases := map[string]string{} // src field → alias
	inKeys := map[string]bool{}          // empty = don't restrict
	inFields := map[string]bool{}        // empty = don't restrict
	var summarize *SummarizeOpts
	var highlight *HighlightOpts
	params := map[string]string{}
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "NOCONTENT":
			noContent = true
		case "WITHSCORES":
			withScores = true
		case "PARAMS":
			if i+1 >= len(args) {
				c.Reply.Error("PARAMS needs a count")
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
				i++
			}
		case "SLOP":
			// Phrase proximity tolerance — accepted for compatibility;
			// our scorer requires adjacency for phrase matches today.
			if i+1 < len(args) {
				i++
			}
		case "LIMIT":
			if i+2 >= len(args) {
				c.Reply.Error("LIMIT needs offset + num")
				return nil
			}
			limitOff, _ = strconv.Atoi(args[i+1])
			limitCount, _ = strconv.Atoi(args[i+2])
			i += 2
		case "SORTBY":
			if i+1 >= len(args) {
				c.Reply.Error("SORTBY needs a field")
				return nil
			}
			sortField = args[i+1]
			i++
			if i+1 < len(args) && (strings.EqualFold(args[i+1], "ASC") || strings.EqualFold(args[i+1], "DESC")) {
				sortAsc = strings.EqualFold(args[i+1], "ASC")
				i++
			}
		case "RETURN":
			if i+1 >= len(args) {
				c.Reply.Error("RETURN needs count")
				return nil
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+2+n > len(args) {
				c.Reply.Error("RETURN: too few args")
				return nil
			}
			specs := args[i+2 : i+2+n]
			j := 0
			for j < len(specs) {
				field := specs[j]
				if j+2 < len(specs) && strings.EqualFold(specs[j+1], "AS") {
					returnAliases[field] = specs[j+2]
					returnFields = append(returnFields, field)
					j += 3
					continue
				}
				returnFields = append(returnFields, field)
				j++
			}
			i += 1 + n
		case "INKEYS":
			if i+1 >= len(args) {
				c.Reply.Error("INKEYS needs a count")
				return nil
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+1+n > len(args) {
				c.Reply.Error("INKEYS: too few args")
				return nil
			}
			for _, k := range args[i+2 : i+2+n] {
				inKeys[k] = true
			}
			i += 1 + n
		case "INFIELDS":
			if i+1 >= len(args) {
				c.Reply.Error("INFIELDS needs a count")
				return nil
			}
			n, _ := strconv.Atoi(args[i+1])
			if i+1+n > len(args) {
				c.Reply.Error("INFIELDS: too few args")
				return nil
			}
			for _, f := range args[i+2 : i+2+n] {
				inFields[f] = true
			}
			i += 1 + n
		case "SUMMARIZE":
			summarize = &SummarizeOpts{}
			i = parseSummarizeOpts(args, i+1, summarize) - 1
		case "HIGHLIGHT":
			highlight = &HighlightOpts{}
			i = parseHighlightOpts(args, i+1, highlight) - 1
		}
	}
	hits := idx.SearchWithParams(q, params)
	if len(inKeys) > 0 {
		filtered := hits[:0]
		for _, h := range hits {
			if inKeys[h.DocID] {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}
	if len(inFields) > 0 {
		// INFIELDS narrows TEXT-field matches. We post-filter hits
		// where every matched term appears in at least one allowed
		// field. Requires re-walking the doc; a tighter fix would
		// thread inFields through the eval, but post-filtering keeps
		// this self-contained.
		filtered := hits[:0]
		terms := extractQueryTerms(q)
		for _, h := range hits {
			if hitTouchesAllowedField(h.Doc, terms, inFields) {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}
	if sortField != "" {
		sort.SliceStable(hits, func(i, j int) bool {
			a := hits[i].Doc.Fields[sortField]
			b := hits[j].Doc.Fields[sortField]
			af, errA := strconv.ParseFloat(a, 64)
			bf, errB := strconv.ParseFloat(b, 64)
			if errA == nil && errB == nil {
				if sortAsc {
					return af < bf
				}
				return af > bf
			}
			if sortAsc {
				return a < b
			}
			return a > b
		})
	}
	end := limitOff + limitCount
	if end > len(hits) {
		end = len(hits)
	}
	if limitOff > len(hits) {
		limitOff = len(hits)
	}
	page := hits[limitOff:end]

	out := []any{int64(len(hits))}
	terms := extractQueryTerms(q)
	for _, h := range page {
		out = append(out, h.DocID)
		if withScores {
			out = append(out, strconv.FormatFloat(h.Score, 'f', -1, 64))
		}
		if !noContent {
			// HIGHLIGHT / SUMMARIZE mutate the in-memory doc copy —
			// clone first so we don't poison subsequent reads of the
			// same key.
			view := h.Doc
			if highlight != nil || summarize != nil {
				view = cloneDocFields(h.Doc)
				if summarize != nil {
					applySummarize(view, *summarize, terms)
				}
				if highlight != nil {
					applyHighlight(view, *highlight, terms)
				}
			}
			out = append(out, docFieldsAsArrayAliased(view, returnFields, returnAliases))
		}
	}
	c.Reply.Array(out)
	return nil
}

// hitTouchesAllowedField returns true when at least one query term
// appears in at least one of the INFIELDS-allowed fields of doc.
// Whole-token, case-insensitive match.
func hitTouchesAllowedField(doc *Document, terms []string, allowed map[string]bool) bool {
	if doc == nil {
		return false
	}
	for f, v := range doc.Fields {
		if !allowed[f] {
			continue
		}
		lc := strings.ToLower(v)
		for _, t := range terms {
			if t == "" {
				continue
			}
			if strings.Contains(lc, strings.ToLower(t)) {
				return true
			}
		}
	}
	return false
}

// cloneDocFields makes an independent copy of doc.Fields so
// HIGHLIGHT / SUMMARIZE rewrites don't bleed across queries.
func cloneDocFields(doc *Document) *Document {
	out := &Document{ID: doc.ID, Score: doc.Score, Fields: make(map[string]string, len(doc.Fields))}
	for k, v := range doc.Fields {
		out.Fields[k] = v
	}
	return out
}

// docFieldsAsArrayAliased honours the RETURN [n] field [AS alias]
// surface — every emitted (key, value) pair uses the alias when one
// was supplied for that field.
func docFieldsAsArrayAliased(doc *Document, only []string, aliases map[string]string) []any {
	if len(only) == 0 {
		return docFieldsAsArray(doc, nil)
	}
	out := []any{}
	for _, f := range only {
		v, ok := doc.Fields[f]
		if !ok {
			continue
		}
		name := f
		if alias, present := aliases[f]; present {
			name = alias
		}
		out = append(out, name, v)
	}
	return out
}

// parseSummarizeOpts walks the SUMMARIZE clause starting at args[at]
// and fills opts. Returns the index just past the clause.
//
// SUMMARIZE [FIELDS num field [field ...]] [FRAGS n] [LEN n] [SEPARATOR s]
func parseSummarizeOpts(args []string, at int, opts *SummarizeOpts) int {
	for at < len(args) {
		switch strings.ToUpper(args[at]) {
		case "FIELDS":
			if at+1 >= len(args) {
				return at
			}
			n, _ := strconv.Atoi(args[at+1])
			if at+1+n > len(args) {
				return at
			}
			opts.Fields = append(opts.Fields, args[at+2:at+2+n]...)
			at += 2 + n
		case "FRAGS":
			if at+1 < len(args) {
				opts.Frags, _ = strconv.Atoi(args[at+1])
				at += 2
				continue
			}
			return at
		case "LEN":
			if at+1 < len(args) {
				opts.Len, _ = strconv.Atoi(args[at+1])
				at += 2
				continue
			}
			return at
		case "SEPARATOR":
			if at+1 < len(args) {
				opts.Separator = args[at+1]
				at += 2
				continue
			}
			return at
		default:
			// Hit an option that belongs to FT.SEARCH proper — return
			// without consuming. The caller's outer switch picks up.
			return at
		}
	}
	return at
}

// parseHighlightOpts walks the HIGHLIGHT clause:
//
// HIGHLIGHT [FIELDS num field [field ...]] [TAGS open close]
func parseHighlightOpts(args []string, at int, opts *HighlightOpts) int {
	for at < len(args) {
		switch strings.ToUpper(args[at]) {
		case "FIELDS":
			if at+1 >= len(args) {
				return at
			}
			n, _ := strconv.Atoi(args[at+1])
			if at+1+n > len(args) {
				return at
			}
			opts.Fields = append(opts.Fields, args[at+2:at+2+n]...)
			at += 2 + n
		case "TAGS":
			if at+2 < len(args) {
				opts.Open = args[at+1]
				opts.Close = args[at+2]
				at += 3
				continue
			}
			return at
		default:
			return at
		}
	}
	return at
}

func docFieldsAsArray(doc *Document, only []string) []any {
	out := []any{}
	if len(only) > 0 {
		for _, f := range only {
			if v, ok := doc.Fields[f]; ok {
				out = append(out, f, v)
			}
		}
		return out
	}
	keys := make([]string, 0, len(doc.Fields))
	for k := range doc.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k, doc.Fields[k])
	}
	return out
}

// FT.AGGREGATE index query [LOAD ...] [pipeline-stages ...]
func ftAggregate(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("wrong number of arguments for 'ft.aggregate'")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	q, err := ParseQuery(args[1])
	if err != nil {
		c.Reply.Error("Syntax error: " + err.Error())
		return nil
	}
	hits := idx.Search(q)
	pipelineArgs := args[2:]
	// LOAD clause is accepted and ignored — every loaded field is
	// already on the row from HitsToAggResult.
	if len(pipelineArgs) >= 2 && strings.EqualFold(pipelineArgs[0], "LOAD") {
		n, err := strconv.Atoi(pipelineArgs[1])
		if err != nil || 2+n > len(pipelineArgs) {
			c.Reply.Error("LOAD: bad arg count")
			return nil
		}
		pipelineArgs = pipelineArgs[2+n:]
	}
	pipe, err := ParseAggPipeline(pipelineArgs)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	result := pipe.Run(HitsToAggResult(hits))
	out := []any{int64(len(result.Rows))}
	for _, row := range result.Rows {
		flat := []any{}
		keys := make([]string, 0, len(row))
		for k := range row {
			if k == "__id" || k == "__score" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			flat = append(flat, k, row[k])
		}
		out = append(out, flat)
	}
	c.Reply.Array(out)
	return nil
}

// FT.EXPLAIN index query
func ftExplain(c *modules.Ctx, args []string) error {
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	_ = idx
	q, err := ParseQuery(args[1])
	if err != nil {
		c.Reply.Error("Syntax error: " + err.Error())
		return nil
	}
	c.Reply.Bulk(Explain(q, 0))
	return nil
}

// FT.INFO index
func ftInfo(c *modules.Ctx, args []string) error {
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	fields := []any{}
	for _, f := range idx.Schema.Fields {
		fields = append(fields, []any{
			"identifier", f.Name,
			"type", f.Type.String(),
			"weight", strconv.FormatFloat(f.Weight, 'f', -1, 64),
			"sortable", f.Sortable,
			"noindex", f.NoIndex,
		})
	}
	c.Reply.Array([]any{
		"index_name", idx.Name,
		"num_docs", int64(idx.DocCount()),
		"num_fields", int64(len(idx.Schema.Fields)),
		"fields", fields,
	})
	return nil
}

// FT._LIST
func ftList(c *modules.Ctx, _ []string) error {
	names := indexNames()
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	c.Reply.Array(out)
	return nil
}

// silence unused-import on errors when a future refactor moves error
// construction out of the file.
var _ = errors.New
