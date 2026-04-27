package searchmod

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// FT.SUGADD key string score [INCR] [PAYLOAD payload]
func ftSugAdd(c *modules.Ctx, args []string) error {
	if len(args) < 3 {
		c.Reply.Error("FT.SUGADD key string score [INCR] [PAYLOAD payload]")
		return nil
	}
	key, str := args[0], args[1]
	score, err := strconv.ParseFloat(args[2], 64)
	if err != nil {
		c.Reply.Error("invalid score")
		return nil
	}
	incr, payload := false, ""
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "INCR":
			incr = true
		case "PAYLOAD":
			if i+1 < len(args) {
				payload = args[i+1]
				i++
			}
		}
	}
	added := sugGet(key).Add(str, score, incr, payload)
	c.Reply.Int(int64(added + sugGet(key).Len()))
	return nil
}

// FT.SUGGET key prefix [FUZZY] [WITHSCORES] [WITHPAYLOADS] [MAX max]
func ftSugGet(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("FT.SUGGET key prefix [opts ...]")
		return nil
	}
	key, prefix := args[0], strings.ToLower(args[1])
	max := 5
	fuzzy, withScores, withPayloads := false, false, false
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "FUZZY":
			fuzzy = true
		case "WITHSCORES":
			withScores = true
		case "WITHPAYLOADS":
			withPayloads = true
		case "MAX":
			if i+1 < len(args) {
				max, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
	}
	results := sugGet(key).Get(prefix, max, fuzzy, withScores, withPayloads)
	out := []any{}
	for _, r := range results {
		out = append(out, r.String)
		if withScores {
			out = append(out, strconv.FormatFloat(r.Score, 'g', -1, 64))
		}
		if withPayloads {
			if r.Payload == "" {
				out = append(out, nil)
			} else {
				out = append(out, r.Payload)
			}
		}
	}
	c.Reply.Array(out)
	return nil
}

// FT.SUGDEL key string
func ftSugDel(c *modules.Ctx, args []string) error {
	if sugGet(args[0]).Del(args[1]) {
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

// FT.SUGLEN key
func ftSugLen(c *modules.Ctx, args []string) error {
	c.Reply.Int(int64(sugGet(args[0]).Len()))
	return nil
}

// FT.SYNUPDATE index group term [term ...]
func ftSynUpdate(c *modules.Ctx, args []string) error {
	if len(args) < 3 {
		c.Reply.Error("FT.SYNUPDATE index group term [term ...]")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	idx.Synonyms.Update(args[1], args[2:])
	c.Reply.SimpleString("OK")
	return nil
}

// FT.SYNDUMP index
func ftSynDump(c *modules.Ctx, args []string) error {
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	dump := idx.Synonyms.Dump()
	out := []any{}
	for term, groups := range dump {
		gs := make([]any, len(groups))
		for i, g := range groups {
			gs[i] = g
		}
		out = append(out, term, gs)
	}
	c.Reply.Array(out)
	return nil
}

// FT.SPELLCHECK index query [DISTANCE n] [TERMS INCLUDE|EXCLUDE dict ...]
func ftSpellCheck(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("FT.SPELLCHECK index query [DISTANCE n]")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	maxDist := 1
	for i := 2; i < len(args); i++ {
		if strings.EqualFold(args[i], "DISTANCE") && i+1 < len(args) {
			maxDist, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	results := idx.SpellCheck(args[1], maxDist, 5)
	out := []any{}
	for _, r := range results {
		matches := []any{}
		for _, m := range r.Matches {
			matches = append(matches, []any{strconv.FormatFloat(m.Score, 'g', -1, 64), m.Suggestion})
		}
		out = append(out, []any{"TERM", r.Term, matches})
	}
	c.Reply.Array(out)
	return nil
}

// FT.CURSOR READ idx cursor [COUNT n] | DEL idx cursor
func ftCursor(c *modules.Ctx, args []string) error {
	if len(args) < 3 {
		c.Reply.Error("FT.CURSOR READ|DEL index cursor [COUNT n]")
		return nil
	}
	switch strings.ToUpper(args[0]) {
	case "READ":
		cursor, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			c.Reply.Error("invalid cursor")
			return nil
		}
		count := 0
		for i := 3; i < len(args); i++ {
			if strings.EqualFold(args[i], "COUNT") && i+1 < len(args) {
				count, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
		page, next, ok := readCursor(cursor, count)
		if !ok {
			c.Reply.Error("Cursor not found")
			return nil
		}
		writeCursorPage(c, page, next)
	case "DEL":
		cursor, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			c.Reply.Error("invalid cursor")
			return nil
		}
		if delCursor(cursor) {
			c.Reply.SimpleString("OK")
		} else {
			c.Reply.Error("Cursor not found")
		}
	default:
		c.Reply.Error("Unknown FT.CURSOR subcommand")
	}
	return nil
}

func writeCursorPage(c *modules.Ctx, page []map[string]string, next uint64) {
	rows := []any{int64(len(page))}
	for _, row := range page {
		flat := []any{}
		for k, v := range row {
			flat = append(flat, k, v)
		}
		rows = append(rows, flat)
	}
	c.Reply.Array([]any{rows, int64(next)})
}

// FT.PROFILE index SEARCH|AGGREGATE QUERY ...
func ftProfile(c *modules.Ctx, args []string) error {
	if len(args) < 4 {
		c.Reply.Error("FT.PROFILE index SEARCH|AGGREGATE QUERY ...")
		return nil
	}
	idx, ok := resolveIndex(args[0])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	mode := strings.ToUpper(args[1])
	if !strings.EqualFold(args[2], "QUERY") {
		c.Reply.Error("FT.PROFILE expects QUERY keyword")
		return nil
	}
	parseStart := time.Now()
	q, err := ParseQuery(args[3])
	parseMs := float64(time.Since(parseStart).Microseconds()) / 1000
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	execStart := time.Now()
	switch mode {
	case "SEARCH":
		hits := idx.Search(q)
		execMs := float64(time.Since(execStart).Microseconds()) / 1000
		c.Reply.Array([]any{
			[]any{int64(len(hits))},
			[]any{
				"Type", "QUERY",
				"Parse time (ms)", strconv.FormatFloat(parseMs, 'g', -1, 64),
				"Exec time (ms)", strconv.FormatFloat(execMs, 'g', -1, 64),
				"Total docs scanned", int64(idx.DocCount()),
				"Hits returned", int64(len(hits)),
			},
		})
	case "AGGREGATE":
		hits := idx.Search(q)
		pipe, perr := ParseAggPipeline(args[4:])
		if perr != nil {
			c.Reply.Error(perr.Error())
			return nil
		}
		res := pipe.Run(HitsToAggResult(hits))
		execMs := float64(time.Since(execStart).Microseconds()) / 1000
		c.Reply.Array([]any{
			[]any{int64(len(res.Rows))},
			[]any{
				"Type", "AGGREGATE",
				"Parse time (ms)", strconv.FormatFloat(parseMs, 'g', -1, 64),
				"Exec time (ms)", strconv.FormatFloat(execMs, 'g', -1, 64),
				"Total docs scanned", int64(idx.DocCount()),
				"Result rows", int64(len(res.Rows)),
			},
		})
	default:
		c.Reply.Error("FT.PROFILE expects SEARCH or AGGREGATE")
	}
	return nil
}

// silence unused-import warnings in trimmed builds
var _ = sugDel
