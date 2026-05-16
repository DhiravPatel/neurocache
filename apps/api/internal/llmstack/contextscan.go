package llmstack

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ContextScanner is the back-door scanner for indirect prompt
// injection — the attack class where the malicious instruction is
// not in the user's input but inside a RAG-retrieved document, a
// tool's JSON response, or a scraped web page that the agent reads
// into its prompt and obeys.
//
// INJECT.* guards the *user's* input. CONTEXT.SCAN.* guards the
// content the agent is about to *read into context*. Two different
// trust boundaries, two different scanners. In 2025 ~90% of real
// agent-stack exploits come through this back door, and most cache /
// vector / agent platforms ship nothing for it — which is why this
// is the new credibility-grade gap rather than a nice-to-have.
//
// Detection classes:
//
//   role-flip       — embedded SYSTEM:/[INST]/<|im_start|> markers,
//                     "ignore previous instructions" verbatim, etc.
//   exfil           — "forward to attacker@…", "send your context to
//                     https://…", base64 dump prompts.
//   delayed-trigger — "when you read this", "if you see this", "do not
//                     mention this", "next time the user asks…".
//   hidden          — zero-width chars (U+200B/C/D, U+FEFF), bidi
//                     overrides (U+202E), homoglyphs disguising
//                     verbs ("ignоre" with Cyrillic о).
//   imperative      — naked imperatives addressed to an AI ("you
//                     must", "as an AI, you should", "respond only
//                     with…") that have no business being inside data.
//
// Commands:
//
//   CONTEXT.SCAN doc-id payload
//        → {hit, severity, spans, classes, sanitized}
//   CONTEXT.SCAN.BULK id1 payload1 id2 payload2 ...
//        → per-doc result; apps drop the hits before assembling.
//   CONTEXT.SCAN.SANITIZE payload
//        → just the cleaned text (no detection metadata).
//   CONTEXT.SCAN.RULES
//        → active rule list (class → pattern).
//   CONTEXT.SCAN.WHITELIST ADD|REMOVE pattern
//        → exempt a pattern (regex) from triggering.
//   CONTEXT.SCAN.RECENT [LIMIT n]
//        → recent detections for forensics.
//   CONTEXT.SCAN.RESET
//        → wipe whitelist + recent buffer (stats preserved).
//   CONTEXT.SCAN.STATS
//
// Hot path: SCAN runs ~6 compiled regexes against the payload + one
// Unicode-class sweep. ~3 µs on a 500-byte snippet. Apps run it on
// every RAG hit and every tool response.
type ContextScanner struct {
	mu        sync.RWMutex
	whitelist []*regexp.Regexp
	recent    []ContextScanRow
	recentCap int

	totalScans       atomic.Int64
	totalHits        atomic.Int64
	totalSanitized   atomic.Int64
	totalWhitelisted atomic.Int64
}

// NewContextScanner returns a scanner pre-loaded with the built-in
// detection rules.
func NewContextScanner() *ContextScanner {
	return &ContextScanner{recentCap: 200}
}

// ContextScanResult is what SCAN / SANITIZE return per payload.
type ContextScanResult struct {
	DocID     string             `json:"doc_id,omitempty"`
	Hit       bool               `json:"hit"`
	Severity  float64            `json:"severity"`  // max class weight that fired, 0..1
	Classes   []string           `json:"classes"`   // unique classes that fired
	Spans     []ContextScanSpan  `json:"spans"`     // ranges in the original payload
	Sanitized string             `json:"sanitized"` // hits replaced by spaces, hidden chars stripped
}

// ContextScanSpan is one offending substring.
type ContextScanSpan struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Class string `json:"class"`
	Match string `json:"match"`
}

// scanRule pairs a class name with its pattern + severity.
type scanRule struct {
	class    string
	pattern  *regexp.Regexp
	severity float64
}

// builtinRules is the default ruleset.
var builtinRules = []scanRule{
	// ── role-flip ────────────────────────────────────────────────────
	{class: "role-flip", severity: 0.95, pattern: regexp.MustCompile(`(?i)\[SYSTEM\s*:[^\]]{0,400}\]`)},
	{class: "role-flip", severity: 0.95, pattern: regexp.MustCompile(`(?i)<\|im_start\|>\s*system`)},
	{class: "role-flip", severity: 0.90, pattern: regexp.MustCompile(`(?i)###\s*(system|instruction)\b`)},
	{class: "role-flip", severity: 0.90, pattern: regexp.MustCompile(`(?i)<system>[\s\S]{0,400}?</system>`)},
	{class: "role-flip", severity: 0.95, pattern: regexp.MustCompile(`(?i)\[INST\]`)},
	{class: "role-flip", severity: 0.95, pattern: regexp.MustCompile(`(?i)\bignore\s+(all\s+)?(previous|prior|above)\s+(instructions|prompts?|rules?|messages?)\b`)},
	{class: "role-flip", severity: 0.90, pattern: regexp.MustCompile(`(?i)\bdisregard\s+(all\s+)?(previous|prior|above)\b`)},
	{class: "role-flip", severity: 0.85, pattern: regexp.MustCompile(`(?i)\b(new|updated|revised)\s+(system\s+)?instructions?\s*:`)},

	// ── exfil ────────────────────────────────────────────────────────
	{class: "exfil", severity: 0.95, pattern: regexp.MustCompile(`(?i)\b(forward|send|email|exfiltrate|leak|post|upload)\s+(all|every|the|prior|previous|this|your)\s+(messages?|conversation|context|prompt|history|tokens?|chat|content)\b`)},
	{class: "exfil", severity: 0.85, pattern: regexp.MustCompile(`(?i)\bbase64[- ]?encode\s+(the\s+)?(conversation|context|prompt|messages?)\b`)},
	{class: "exfil", severity: 0.85, pattern: regexp.MustCompile(`(?i)\bmake\s+an?\s+(http|web|api|fetch)\s+request\s+to\b`)},

	// ── delayed-trigger ─────────────────────────────────────────────
	{class: "delayed-trigger", severity: 0.85, pattern: regexp.MustCompile(`(?i)\bwhen\s+you\s+(see|read|encounter|reach)\s+this\b`)},
	{class: "delayed-trigger", severity: 0.85, pattern: regexp.MustCompile(`(?i)\bif\s+you\s+(see|read|encounter|reach)\s+this\b`)},
	{class: "delayed-trigger", severity: 0.75, pattern: regexp.MustCompile(`(?i)\bdo\s+not\s+mention\s+this\s+(message|instruction|note)\b`)},
	{class: "delayed-trigger", severity: 0.80, pattern: regexp.MustCompile(`(?i)\bnext\s+time\s+the\s+user\s+(asks?|says?|writes?)\b`)},

	// ── imperative (assistant-addressed) ────────────────────────────
	{class: "imperative", severity: 0.70, pattern: regexp.MustCompile(`(?i)\b(?:you\s+(?:must|shall|are\s+required\s+to|are\s+now)|as\s+an\s+ai,?\s+you)\b`)},
	{class: "imperative", severity: 0.65, pattern: regexp.MustCompile(`(?i)\brespond\s+only\s+with\b`)},
	{class: "imperative", severity: 0.65, pattern: regexp.MustCompile(`(?i)\boutput\s+(?:only|exactly)\s+the\s+following\b`)},

	// ── hidden (explicit bidi overrides; zero-width handled separately) ─
	{class: "hidden", severity: 0.90, pattern: regexp.MustCompile(`[\x{202A}-\x{202E}\x{2066}-\x{2069}]`)},
}

// Scan runs the full ruleset against the payload and returns a
// detailed result. The payload is never mutated; the sanitized
// field is a derived copy.
func (s *ContextScanner) Scan(docID, payload string) ContextScanResult {
	s.totalScans.Add(1)
	out := ContextScanResult{DocID: docID}
	if payload == "" {
		out.Sanitized = ""
		return out
	}

	// Apply whitelist exemptions
	s.mu.RLock()
	whitelist := s.whitelist
	s.mu.RUnlock()
	exempt := func(match string) bool {
		for _, re := range whitelist {
			if re.MatchString(match) {
				s.totalWhitelisted.Add(1)
				return true
			}
		}
		return false
	}

	classSet := map[string]bool{}

	// 1) Regex rule sweep
	for _, rule := range builtinRules {
		for _, idx := range rule.pattern.FindAllStringIndex(payload, -1) {
			match := payload[idx[0]:idx[1]]
			if exempt(match) {
				continue
			}
			classSet[rule.class] = true
			if rule.severity > out.Severity {
				out.Severity = rule.severity
			}
			out.Spans = append(out.Spans, ContextScanSpan{
				Start: idx[0], End: idx[1], Class: rule.class, Match: match,
			})
		}
	}

	// 2) Hidden-character sweep — zero-width and BOM, byte by byte
	for i, r := range payload {
		if isHiddenRune(r) {
			classSet["hidden"] = true
			if 0.85 > out.Severity {
				out.Severity = 0.85
			}
			// Each hidden rune is a single-rune span
			out.Spans = append(out.Spans, ContextScanSpan{
				Start: i, End: i + len(string(r)), Class: "hidden", Match: string(r),
			})
		}
	}

	// 3) Homoglyph sweep for "ignore" and "system" disguised with
	// non-ASCII look-alikes. Catches the common Cyrillic 'о' / 'е' /
	// 'а' / 'у' substitutions that defeat the regex pass above.
	for _, hg := range homoglyphTargets {
		start, end := homoglyphFindRange(payload, hg.normalised)
		if start < 0 {
			continue
		}
		classSet["hidden"] = true
		if 0.90 > out.Severity {
			out.Severity = 0.90
		}
		out.Spans = append(out.Spans, ContextScanSpan{
			Start: start, End: end, Class: "hidden", Match: "homoglyph:" + hg.normalised,
		})
	}

	out.Hit = len(out.Spans) > 0
	if out.Hit {
		s.totalHits.Add(1)
	}
	for c := range classSet {
		out.Classes = append(out.Classes, c)
	}
	sort.Strings(out.Classes)
	sort.Slice(out.Spans, func(i, j int) bool { return out.Spans[i].Start < out.Spans[j].Start })

	out.Sanitized = sanitize(payload, out.Spans)
	if out.Hit {
		s.totalSanitized.Add(1)
		s.recordHit(out)
	}
	return out
}

// ScanBulk runs Scan over each (id, payload) pair and returns one
// result per doc, preserving input order.
func (s *ContextScanner) ScanBulk(ids, payloads []string) ([]ContextScanResult, error) {
	if len(ids) != len(payloads) {
		return nil, errors.New("ids and payloads length mismatch")
	}
	out := make([]ContextScanResult, len(ids))
	for i, id := range ids {
		out[i] = s.Scan(id, payloads[i])
	}
	return out, nil
}

// Sanitize returns just the cleaned payload (hits replaced by
// spaces, hidden runes stripped) — convenience for the common case
// where the caller doesn't want the detection metadata.
func (s *ContextScanner) Sanitize(payload string) string {
	return s.Scan("", payload).Sanitized
}

// WhitelistAdd registers a regex pattern that exempts matching
// substrings from triggering. Useful for known-good fragments like
// "[SYSTEM: maintenance window]" that legitimately appear in
// monitoring docs.
func (s *ContextScanner) WhitelistAdd(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.whitelist = append(s.whitelist, re)
	s.mu.Unlock()
	return nil
}

// WhitelistRemove drops a pattern by its source string.
func (s *ContextScanner) WhitelistRemove(pattern string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, re := range s.whitelist {
		if re.String() == pattern {
			s.whitelist = append(s.whitelist[:i], s.whitelist[i+1:]...)
			return true
		}
	}
	return false
}

// ScanRuleRow is one row of RULES output.
type ScanRuleRow struct {
	Class    string  `json:"class"`
	Pattern  string  `json:"pattern"`
	Severity float64 `json:"severity"`
}

// Rules returns the active detection rules — built-ins only; the
// whitelist is reported separately.
func (s *ContextScanner) Rules() []ScanRuleRow {
	out := make([]ScanRuleRow, len(builtinRules))
	for i, r := range builtinRules {
		out[i] = ScanRuleRow{Class: r.class, Pattern: r.pattern.String(), Severity: r.severity}
	}
	return out
}

// Whitelist returns the active whitelist patterns.
func (s *ContextScanner) Whitelist() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.whitelist))
	for i, re := range s.whitelist {
		out[i] = re.String()
	}
	return out
}

// ContextScanRow is one item in RECENT output.
type ContextScanRow struct {
	TS       int64    `json:"ts"`
	DocID    string   `json:"doc_id"`
	Severity float64  `json:"severity"`
	Classes  []string `json:"classes"`
	Sample   string   `json:"sample"`
}

// Recent returns the most-recent detections (newest last).
func (s *ContextScanner) Recent(limit int) []ContextScanRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.recent
	if limit > 0 && limit < len(rows) {
		rows = rows[len(rows)-limit:]
	}
	out := make([]ContextScanRow, len(rows))
	copy(out, rows)
	return out
}

// Reset clears the whitelist and recent buffer (lifetime counters
// preserved so STATS keep their meaning across resets).
func (s *ContextScanner) Reset() {
	s.mu.Lock()
	s.whitelist = nil
	s.recent = nil
	s.mu.Unlock()
}

// ContextScanStats is the global snapshot.
type ContextScanStats struct {
	TotalScans       int64 `json:"total_scans"`
	TotalHits        int64 `json:"total_hits"`
	TotalSanitized   int64 `json:"total_sanitized"`
	TotalWhitelisted int64 `json:"total_whitelisted"`
	WhitelistSize    int   `json:"whitelist_size"`
	RecentSize       int   `json:"recent_size"`
}

func (s *ContextScanner) Stats() ContextScanStats {
	s.mu.RLock()
	wl := len(s.whitelist)
	rc := len(s.recent)
	s.mu.RUnlock()
	return ContextScanStats{
		TotalScans:       s.totalScans.Load(),
		TotalHits:        s.totalHits.Load(),
		TotalSanitized:   s.totalSanitized.Load(),
		TotalWhitelisted: s.totalWhitelisted.Load(),
		WhitelistSize:    wl,
		RecentSize:       rc,
	}
}

// ─── helpers ────────────────────────────────────────────────────

func (s *ContextScanner) recordHit(r ContextScanResult) {
	sample := r.Sanitized
	if len(sample) > 120 {
		sample = sample[:120] + "…"
	}
	row := ContextScanRow{
		TS:       time.Now().UnixNano() / int64(time.Second),
		DocID:    r.DocID,
		Severity: r.Severity,
		Classes:  r.Classes,
		Sample:   sample,
	}
	s.mu.Lock()
	if len(s.recent) >= s.recentCap {
		s.recent = s.recent[1:]
	}
	s.recent = append(s.recent, row)
	s.mu.Unlock()
}

// sanitize replaces every offending span with a single space and
// strips hidden runes (since their spans may be byte-aligned).
func sanitize(payload string, spans []ContextScanSpan) string {
	if len(spans) == 0 {
		// Strip hidden runes even if no span matched the regex pass
		return stripHidden(payload)
	}
	// Build a mask: bytes covered by any span become space.
	b := []byte(payload)
	mask := make([]bool, len(b))
	for _, sp := range spans {
		for i := sp.Start; i < sp.End && i < len(mask); i++ {
			mask[i] = true
		}
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if mask[i] {
			out = append(out, ' ')
			continue
		}
		out = append(out, b[i])
	}
	return stripHidden(string(out))
}

func stripHidden(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isHiddenRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isHiddenRune(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0xFEFF: // zero-width / BOM
		return true
	case 0x2028, 0x2029: // line / paragraph separators
		return true
	}
	if r >= 0x202A && r <= 0x202E {
		return true // bidi overrides
	}
	if r >= 0x2066 && r <= 0x2069 {
		return true // isolate controls
	}
	return false
}

// ── homoglyph detection: catches Cyrillic-disguised "ignore" / "system" ──

type homoglyphTarget struct{ normalised string }

var homoglyphTargets = []homoglyphTarget{
	{"ignore"}, {"system"}, {"instruction"}, {"forget"}, {"override"},
}

// homoglyphFindRange scans payload for a match of target where each
// character is either the exact ASCII or a known confusable, AND at
// least one character was a non-ASCII confusable (i.e., the regex
// pass would have missed it). Returns the [start, end) byte range
// or -1, -1 on miss.
func homoglyphFindRange(payload, target string) (int, int) {
	lower := strings.ToLower(payload)
	idx := 0
	for idx < len(lower) {
		matched := true
		usedNonAscii := false
		j := idx
		for _, want := range target {
			r, sz := nextRune(lower[j:])
			if sz == 0 {
				matched = false
				break
			}
			if r == want {
				j += sz
				continue
			}
			if !isConfusableOf(want, r) {
				matched = false
				break
			}
			usedNonAscii = true
			j += sz
		}
		if matched && usedNonAscii {
			return idx, j
		}
		_, sz := nextRune(lower[idx:])
		if sz == 0 {
			break
		}
		idx += sz
	}
	return -1, -1
}

func nextRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

// isConfusableOf returns true if `got` is a known visual confusable
// of the ASCII `want`.
func isConfusableOf(want, got rune) bool {
	confusables := map[rune][]rune{
		'a': {0x0430, 0x03B1},                 // Cyrillic а, Greek alpha
		'e': {0x0435, 0x03B5},                 // Cyrillic е
		'i': {0x0456, 0x03B9, 0x0269},         // Cyrillic і
		'o': {0x043E, 0x03BF, 0x0FF},          // Cyrillic о
		'p': {0x0440},                         // Cyrillic р
		's': {0x0455},                         // Cyrillic ѕ
		't': {0x03C4},                         // Greek tau
		'u': {0x0443},                         // Cyrillic у
		'r': {0x0433},                         // Cyrillic г looks like r in some fonts
		'n': {0x043F, 0x03C0},                 // Cyrillic п, Greek π
		'g': {0x0261},
		'f': {0x0192},
		'c': {0x0441},                         // Cyrillic с
		'm': {0x043C},                         // Cyrillic м
	}
	for _, alt := range confusables[want] {
		if got == alt {
			return true
		}
	}
	return false
}

