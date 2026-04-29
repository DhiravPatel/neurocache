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
	c.Reply.SimpleString("OK")
	return nil
}

// FT.ALTER index SCHEMA ADD field type [flags ...]
func ftAlter(c *modules.Ctx, args []string) error {
	if len(args) < 4 || !strings.EqualFold(args[1], "SCHEMA") || !strings.EqualFold(args[2], "ADD") {
		c.Reply.Error("FT.ALTER index SCHEMA ADD field type [flags ...]")
		return nil
	}
	idx, ok := getIndex(args[0])
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
	idx, ok := getIndex(args[0])
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
	idx, ok := getIndex(args[0])
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
	idx, ok := getIndex(args[0])
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
	idx, ok := getIndex(args[0])
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
	params := map[string]string{}
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "NOCONTENT":
			noContent = true
		case "WITHSCORES":
			withScores = true
		case "PARAMS":
			// PARAMS n k v k v ...
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
			returnFields = append(returnFields, args[i+2:i+2+n]...)
			i += 1 + n
		}
	}
	hits := idx.SearchWithParams(q, params)
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
	for _, h := range page {
		out = append(out, h.DocID)
		if withScores {
			out = append(out, strconv.FormatFloat(h.Score, 'f', -1, 64))
		}
		if !noContent {
			out = append(out, docFieldsAsArray(h.Doc, returnFields))
		}
	}
	c.Reply.Array(out)
	return nil
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
	idx, ok := getIndex(args[0])
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
	idx, ok := getIndex(args[0])
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
	idx, ok := getIndex(args[0])
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
