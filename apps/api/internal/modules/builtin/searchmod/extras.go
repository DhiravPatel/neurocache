package searchmod

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── server-side cursors ─────────────────────────────────────────────
//
// FT.AGGREGATE WITHCURSOR returns a cursor token instead of a result
// page; subsequent FT.CURSOR READ <idx> <cursor> [COUNT n] pulls the
// next batch. Cursors live in a per-process registry with TTL — the
// idle timeout matches RedisSearch's `MAXIDLE` parameter.

type cursorState struct {
	rows    []map[string]string
	pos     int
	pageSize int
	expires time.Time
}

var (
	cursorMu      sync.Mutex
	cursorStore   = map[uint64]*cursorState{}
	cursorNextID  atomic.Uint64
)

// newCursor parks a result set under a fresh ID and returns it. ttl ≤ 0
// uses 5 minutes (RedisSearch default).
func newCursor(rows []map[string]string, pageSize int, ttl time.Duration) uint64 {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if pageSize <= 0 {
		pageSize = 1000
	}
	cursorMu.Lock()
	defer cursorMu.Unlock()
	id := cursorNextID.Add(1)
	cursorStore[id] = &cursorState{rows: rows, pageSize: pageSize, expires: time.Now().Add(ttl)}
	return id
}

// readCursor pops the next page; returns the page rows and the
// next-cursor (0 when exhausted).
func readCursor(id uint64, count int) ([]map[string]string, uint64, bool) {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	c, ok := cursorStore[id]
	if !ok {
		return nil, 0, false
	}
	if time.Now().After(c.expires) {
		delete(cursorStore, id)
		return nil, 0, false
	}
	if count <= 0 {
		count = c.pageSize
	}
	end := c.pos + count
	if end > len(c.rows) {
		end = len(c.rows)
	}
	page := c.rows[c.pos:end]
	c.pos = end
	c.expires = time.Now().Add(5 * time.Minute) // refresh on read
	if c.pos >= len(c.rows) {
		delete(cursorStore, id)
		return page, 0, true
	}
	return page, id, true
}

func delCursor(id uint64) bool {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	if _, ok := cursorStore[id]; !ok {
		return false
	}
	delete(cursorStore, id)
	return true
}

// ── spellcheck ──────────────────────────────────────────────────────
//
// FT.SPELLCHECK runs each non-stopword query term through Levenshtein
// against every indexed term. Suggestions come back grouped per term,
// scored by inverse edit distance scaled by document frequency.

type SpellCheckResult struct {
	Term    string
	Matches []SpellCheckMatch
}

type SpellCheckMatch struct {
	Score      float64
	Suggestion string
}

// SpellCheck runs the engine against `query`, returning at most
// `topN` suggestions per term within `maxDistance` edits.
func (idx *Index) SpellCheck(query string, maxDistance, topN int) []SpellCheckResult {
	if maxDistance <= 0 {
		maxDistance = 1
	}
	if topN <= 0 {
		topN = 5
	}
	tokens := Tokenize(query, true)
	out := []SpellCheckResult{}
	for _, t := range tokens {
		// If the term is already known across any TEXT field, skip it.
		known := false
		for _, f := range idx.Schema.Fields {
			if f.Type != FieldText {
				continue
			}
			if idx.TermPostings(f.Name, t) != nil {
				known = true
				break
			}
		}
		if known {
			continue
		}
		matches := []SpellCheckMatch{}
		for _, f := range idx.Schema.Fields {
			if f.Type != FieldText {
				continue
			}
			for term, p := range idx.postings[f.Name] {
				d := levenshtein(term, t, maxDistance+1)
				if d > maxDistance {
					continue
				}
				score := float64(len(p.entries)) / float64(idx.DocCount()+1) / float64(d+1)
				matches = append(matches, SpellCheckMatch{Score: score, Suggestion: term})
			}
		}
		// Top-N by score.
		dedup := map[string]float64{}
		for _, m := range matches {
			if cur, ok := dedup[m.Suggestion]; !ok || m.Score > cur {
				dedup[m.Suggestion] = m.Score
			}
		}
		flat := make([]SpellCheckMatch, 0, len(dedup))
		for s, sc := range dedup {
			flat = append(flat, SpellCheckMatch{Suggestion: s, Score: sc})
		}
		// best first
		for i := 1; i < len(flat); i++ {
			for j := i; j > 0 && flat[j-1].Score < flat[j].Score; j-- {
				flat[j-1], flat[j] = flat[j], flat[j-1]
			}
		}
		if topN < len(flat) {
			flat = flat[:topN]
		}
		out = append(out, SpellCheckResult{Term: t, Matches: flat})
	}
	return out
}

// ── profile ─────────────────────────────────────────────────────────
//
// FT.PROFILE wraps a SEARCH or AGGREGATE invocation with timing data.
// We measure the parser, executor, and overall wall-clock — enough to
// diagnose "is the query slow because of parsing or scoring?".

type ProfileTiming struct {
	ParseMs   float64
	ExecMs    float64
	TotalMs   float64
	NumDocs   int
}

// silence unused-import warnings in trimmed builds.
var _ = strconv.Atoi
var _ = strings.ToLower
