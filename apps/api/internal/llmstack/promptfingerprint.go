package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// PromptAnalytics groups prompts by a normalization-robust
// fingerprint so production teams can answer questions like:
//
//   "How many distinct prompts are users actually sending?"
//   "Which prompt template is most expensive cumulatively?"
//   "Are users sending 50 minor variants of the same thing?"
//   "Did someone just slip in a prompt-injection variant?"
//
// The fingerprint normalizes whitespace, case, common punctuation,
// and digit runs (so "user 12345" and "user 67890" hash the same)
// before sha256-ing. Apps then call PROMPT.RECORD on every prompt
// they ship, and PROMPT.GROUPS surfaces the top-N clusters.
//
// Why not just use SEMANTIC_GET? Two reasons:
//   1. Fingerprinting is sub-microsecond (no embedding, no cosine
//      scan). Cheap enough to call before every LLM request.
//   2. It catches *literal* near-duplicates, where embeddings would
//      lump everything semantically related into one cluster. The
//      use cases above (cost analysis, injection detection) want
//      literal duplicates, not semantic.
//
// Lock-free reads on Fingerprint (pure function). Record uses a
// sync.Map keyed by fingerprint with atomic counters, so a fleet
// of conn goroutines all bumping the same group don't contend.
type PromptAnalytics struct {
	groups sync.Map // fingerprint -> *promptGroup

	totalRecords atomic.Int64
}

type promptGroup struct {
	fingerprint string
	count       atomic.Int64
	firstSeen   atomic.Int64 // unix-nano
	lastSeen    atomic.Int64 // unix-nano

	// sample is one example raw prompt that hashed to this group —
	// we store at most one to keep memory bounded under high cardinality.
	// Updated under sampleMu only on a CAS that loses (rare).
	sample   string
	sampleMu sync.Mutex
}

// NewPromptAnalytics returns an empty analyzer.
func NewPromptAnalytics() *PromptAnalytics { return &PromptAnalytics{} }

// Fingerprint returns a stable hash for a prompt that's robust to:
//
//   - leading/trailing whitespace
//   - case differences ("Hello" == "hello")
//   - runs of whitespace ("a b" == "a   b")
//   - "soft" punctuation: ! ? . , ; : - — _ ( ) [ ] " '
//   - long digit runs collapsed to a single placeholder ("D")
//   - URLs collapsed to a single placeholder ("U")
//
// These rules deliberately turn "Please summarize report-12345.txt
// for me!!" and "please summarize report-99999.txt for me" into the
// same fingerprint — the use case is "cluster near-duplicate prompts
// for ops/cost insight," not text identity. Apps that need
// stricter equality should use a plain sha256 directly.
func Fingerprint(prompt string) string {
	return fingerprintHex(canonicalize(prompt))
}

// canonicalize implements the normalization rules above. Allocates
// once for the result builder; everything else is in-place.
func canonicalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	digitRun := 0
	urlRun := 0
	i := 0
	for i < len(s) {
		// Detect URL prefix and collapse the whole thing into "U".
		// Only run the prefix check at word boundaries (start or
		// after whitespace) — saves the per-char cost.
		if (i == 0 || isSpace(s[i-1])) && hasURLPrefix(s, i) {
			b.WriteByte('U')
			urlRun++
			// skip until next whitespace
			for i < len(s) && !isSpace(s[i]) {
				i++
			}
			prevSpace = false
			continue
		}
		c := s[i]
		i++
		if isSpace(c) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			digitRun = 0
			continue
		}
		// Digit run collapsing — single 'D' for any contiguous digits.
		if c >= '0' && c <= '9' {
			if digitRun == 0 {
				b.WriteByte('D')
			}
			digitRun++
			prevSpace = false
			continue
		}
		digitRun = 0
		// Drop soft punctuation entirely.
		if isSoftPunct(c) {
			continue
		}
		// Lowercase ASCII fast path; defer to unicode for the rest.
		if c >= 'A' && c <= 'Z' {
			c += 32
			b.WriteByte(c)
			prevSpace = false
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			prevSpace = false
			continue
		}
		// Non-ASCII byte — peek at the rune for proper lowercase. Rare
		// in production prompts (mostly emoji + accented chars), so we
		// don't optimize this path heavily.
		r, size := decodeUTF8(s[i-1:])
		i += size - 1
		r = unicode.ToLower(r)
		b.WriteRune(r)
		prevSpace = false
	}
	out := b.String()
	return strings.TrimSpace(out)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isSoftPunct(c byte) bool {
	switch c {
	case '!', '?', '.', ',', ';', ':', '-', '_',
		'(', ')', '[', ']', '{', '}',
		'"', '\'', '`':
		return true
	}
	return false
}

// hasURLPrefix returns true when s[i:] starts with http:// or https://.
func hasURLPrefix(s string, i int) bool {
	if i+7 <= len(s) && s[i] == 'h' && s[i+1] == 't' && s[i+2] == 't' && s[i+3] == 'p' {
		if s[i+4] == ':' && s[i+5] == '/' && s[i+6] == '/' {
			return true
		}
		if i+8 <= len(s) && s[i+4] == 's' && s[i+5] == ':' && s[i+6] == '/' && s[i+7] == '/' {
			return true
		}
	}
	return false
}

// decodeUTF8 is a minimal reimpl that returns (rune, byte-size) so
// we don't need to import unicode/utf8 just for this hot path. For
// invalid sequences it returns (0xFFFD, 1) — the standard handling.
func decodeUTF8(s string) (rune, int) {
	if len(s) == 0 {
		return 0xFFFD, 1
	}
	c0 := s[0]
	if c0 < 0x80 {
		return rune(c0), 1
	}
	switch {
	case c0&0xE0 == 0xC0 && len(s) >= 2:
		return rune(c0&0x1F)<<6 | rune(s[1]&0x3F), 2
	case c0&0xF0 == 0xE0 && len(s) >= 3:
		return rune(c0&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	case c0&0xF8 == 0xF0 && len(s) >= 4:
		return rune(c0&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	}
	return 0xFFFD, 1
}

// fingerprintHex hashes the canonicalized prompt with sha256 and
// returns the first 16 hex chars (64-bit prefix). Collision risk at
// 64 bits is ~1 in 2³² distinct prompts — comfortable for any
// realistic prompt-cluster cardinality.
func fingerprintHex(canon string) string {
	h := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(h[:8])
}

// Record bumps the count for prompt's fingerprint group. Lock-free:
// sync.Map.LoadOrStore + atomic.Add. The first record for a new
// group atomically initializes firstSeen + the sample.
func (p *PromptAnalytics) Record(prompt string) string {
	fp := Fingerprint(prompt)
	now := nowUnixNano()
	v, loaded := p.groups.LoadOrStore(fp, &promptGroup{fingerprint: fp})
	g := v.(*promptGroup)
	if !loaded {
		g.firstSeen.Store(now)
		g.sampleMu.Lock()
		g.sample = prompt
		g.sampleMu.Unlock()
	}
	g.lastSeen.Store(now)
	g.count.Add(1)
	p.totalRecords.Add(1)
	return fp
}

// Sample returns the canonical example prompt stored for a
// fingerprint, or "" if no group exists.
func (p *PromptAnalytics) Sample(fp string) string {
	v, ok := p.groups.Load(fp)
	if !ok {
		return ""
	}
	g := v.(*promptGroup)
	g.sampleMu.Lock()
	defer g.sampleMu.Unlock()
	return g.sample
}

// PromptGroup is one cluster row (PROMPT.GROUPS output).
type PromptGroup struct {
	Fingerprint string `json:"fingerprint"`
	Count       int64  `json:"count"`
	FirstSeenNS int64  `json:"first_seen_ns"`
	LastSeenNS  int64  `json:"last_seen_ns"`
	Sample      string `json:"sample"`
}

// Groups returns the top-N most-frequent groups. limit<=0 returns
// every group. Sorted descending by count; ties broken by lastSeen
// (recently active first).
func (p *PromptAnalytics) Groups(limit int) []PromptGroup {
	var rows []PromptGroup
	p.groups.Range(func(k, v any) bool {
		g := v.(*promptGroup)
		g.sampleMu.Lock()
		sample := g.sample
		g.sampleMu.Unlock()
		rows = append(rows, PromptGroup{
			Fingerprint: k.(string),
			Count:       g.count.Load(),
			FirstSeenNS: g.firstSeen.Load(),
			LastSeenNS:  g.lastSeen.Load(),
			Sample:      sample,
		})
		return true
	})
	// Insertion-sort keeps it tight for the typical limit < 1000 case.
	for i := 1; i < len(rows); i++ {
		j := i
		for j > 0 {
			less := rows[j].Count > rows[j-1].Count ||
				(rows[j].Count == rows[j-1].Count && rows[j].LastSeenNS > rows[j-1].LastSeenNS)
			if !less {
				break
			}
			rows[j], rows[j-1] = rows[j-1], rows[j]
			j--
		}
	}
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows
}

// PromptStats is the global counters snapshot.
type PromptStats struct {
	TotalRecords int64 `json:"total_records"`
	UniqueGroups int   `json:"unique_groups"`
}

func (p *PromptAnalytics) Stats() PromptStats {
	count := 0
	p.groups.Range(func(_, _ any) bool { count++; return true })
	return PromptStats{
		TotalRecords: p.totalRecords.Load(),
		UniqueGroups: count,
	}
}

// Reset wipes every group + global counters. Used by ops paths.
func (p *PromptAnalytics) Reset() {
	p.groups.Range(func(k, _ any) bool {
		p.groups.Delete(k)
		return true
	})
	p.totalRecords.Store(0)
}

// nowUnixNano is split out so tests can stub if needed.
func nowUnixNano() int64 {
	return timeNowUnixNano()
}
