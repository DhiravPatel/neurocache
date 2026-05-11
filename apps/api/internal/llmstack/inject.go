package llmstack

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// InjectScanner detects prompt-injection attempts in incoming text.
// Ships with a built-in pattern library covering the canonical
// vectors (instruction overrides, role-flip attempts, system-prompt
// extraction, jailbreak preamble like "DAN mode") and lets operators
// extend with custom regex patterns at runtime.
//
// Why this exists: every LLM-facing API endpoint should scan its
// inputs for known injection patterns before forwarding to the
// model. Apps build this in client code today; building it into the
// cache means:
//
//   - Pattern updates roll out instantly across all worker processes
//     (no library bump + redeploy)
//   - Detection happens at the cache boundary, not deep in app code,
//     so it's hard to forget
//   - Atomic counters per pattern surface "which attacks are we
//     seeing?" via INJECT.STATS
//
// Hot path:
//   Scan: walk the compiled patterns; first regex match wins. The
//         pattern slice is RLock-protected to allow at-runtime adds.
//         Per-call cost: O(N_patterns * len(text)) for pure-regex
//         matchers; we keep the built-in library small and use
//         literal-prefix anchors so most patterns reject in O(1).
//
// Returns a (severity 0.0-1.0, matched-pattern-name, true) so apps
// can choose: hard-block on severity ≥ 0.8, log+continue below.
type InjectScanner struct {
	mu       sync.RWMutex
	patterns []injectPattern

	totalScans   atomic.Int64
	totalHits    atomic.Int64
}

// injectPattern is one detection rule.
type injectPattern struct {
	name     string         // unique per scanner; INJECT.PATTERN.LIST keys on this
	severity float64        // 0.0 (advisory) ... 1.0 (definitely malicious)
	re       *regexp.Regexp // compiled at registration; case-insensitive
	hits     atomic.Int64
	source   string // raw pattern body (for INJECT.PATTERN.LIST display)
	builtin  bool   // true == ships with the engine; can't be removed
}

// NewInjectScanner returns a scanner pre-loaded with the built-in
// pattern library. Operators add custom patterns via Add.
func NewInjectScanner() *InjectScanner {
	s := &InjectScanner{}
	for _, b := range defaultPatterns {
		_ = s.add(b.name, b.source, b.severity, true)
	}
	return s
}

// builtin pattern definitions. Severity scale:
//
//   1.0 — explicit instruction override / role flip
//   0.9 — system-prompt extraction attempts
//   0.8 — known jailbreak preambles ("DAN mode", "ignore all previous")
//   0.5 — suspicious but legitimate-sounding requests
//
// Each pattern is case-insensitive and matches anywhere in the input
// (no anchors). We compile with `(?i)` for ASCII case-folding —
// Unicode-aware case folding is rarely needed for English-language
// injection patterns and adds significant cost.
var defaultPatterns = []struct {
	name     string
	source   string
	severity float64
}{
	{
		name: "ignore-previous",
		// Match "ignore [all] {previous,prior,the above,the prior} {instruction(s),message(s),prompt(s),rule(s),context}"
		// — singular and plural noun forms, with optional "all" qualifier.
		source:   `(?i)ignore (all |everything )?(previous|prior|the (above|prior)) (instruction|message|prompt|rule|context)s?`,
		severity: 1.0,
	},
	{
		name: "role-flip",
		// "you are now ___" / "act as ___" / "pretend to be ___"
		source:   `(?i)\b(you are now|act as|pretend to be|roleplay as|simulate (a|an)) (a|an) ?[a-z][a-z\- ]{2,}`,
		severity: 0.9,
	},
	{
		name:     "system-prompt-leak",
		source:   `(?i)(reveal|show|print|repeat|output) (your|the) (system|initial|hidden) (prompt|instructions|message)`,
		severity: 0.9,
	},
	{
		name:     "dan-jailbreak",
		source:   `(?i)\b(DAN|do anything now|developer mode enabled|jailbroken|unfiltered mode)\b`,
		severity: 0.95,
	},
	{
		name:     "instruction-override",
		source:   `(?i)(disregard|forget|override) (all|every|your) (rules|guidelines|instructions|safety)`,
		severity: 1.0,
	},
	{
		name:     "encoded-payload",
		source:   `(?i)(base64|rot13|hex)[\s:-]+[A-Za-z0-9+/=]{40,}`,
		severity: 0.7,
	},
	{
		name:     "delimiter-confusion",
		source:   `(?i)\[/?(SYSTEM|USER|INST|ASSISTANT)\]|<\|/?(im_start|im_end|system|user)\|>`,
		severity: 0.85,
	},
}

// Scan runs every registered pattern against text. Returns the first
// match (severity, name, true), or (0, "", false) when no pattern
// matches. Bumps the per-pattern + global hit counters atomically.
//
// "First match wins" rather than "highest-severity match wins"
// because patterns are registered in priority order and the regex
// engine cost is O(patterns × text_len); short-circuiting keeps the
// hot-path fast for the common no-match case.
func (s *InjectScanner) Scan(text string) (severity float64, name string, hit bool) {
	s.totalScans.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.patterns {
		p := &s.patterns[i]
		if p.re.MatchString(text) {
			p.hits.Add(1)
			s.totalHits.Add(1)
			return p.severity, p.name, true
		}
	}
	return 0, "", false
}

// ScanAll returns every matching pattern (not just the first). Used
// when callers want to log all attack signatures rather than just
// the highest-severity one. Slower because we don't short-circuit.
func (s *InjectScanner) ScanAll(text string) []ScanHit {
	s.totalScans.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ScanHit
	for i := range s.patterns {
		p := &s.patterns[i]
		if p.re.MatchString(text) {
			p.hits.Add(1)
			out = append(out, ScanHit{Name: p.name, Severity: p.severity})
		}
	}
	if len(out) > 0 {
		s.totalHits.Add(1)
	}
	return out
}

// ScanHit is one matched pattern in ScanAll output.
type ScanHit struct {
	Name     string  `json:"name"`
	Severity float64 `json:"severity"`
}

// Add registers a custom pattern. Returns ErrPatternExists if the
// name is already taken (built-in or custom). Compile errors return
// the regex package's parse error verbatim.
func (s *InjectScanner) Add(name, source string, severity float64) error {
	return s.add(name, source, severity, false)
}

func (s *InjectScanner) add(name, source string, severity float64, builtin bool) error {
	if name == "" || source == "" {
		return errors.New("name and source required")
	}
	re, err := regexp.Compile(source)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.patterns {
		if s.patterns[i].name == name {
			return ErrPatternExists
		}
	}
	s.patterns = append(s.patterns, injectPattern{
		name:     name,
		severity: severity,
		re:       re,
		source:   source,
		builtin:  builtin,
	})
	return nil
}

// Remove drops a custom pattern. Built-in patterns can't be
// removed — use a Disable / severity-0 mechanism instead if a
// built-in turns out to false-positive in your traffic.
func (s *InjectScanner) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.patterns {
		if s.patterns[i].name == name {
			if s.patterns[i].builtin {
				return ErrPatternBuiltin
			}
			s.patterns = append(s.patterns[:i], s.patterns[i+1:]...)
			return nil
		}
	}
	return ErrUnknownPattern
}

// PatternRow is one row in INJECT.PATTERN.LIST output.
type PatternRow struct {
	Name     string  `json:"name"`
	Source   string  `json:"source"`
	Severity float64 `json:"severity"`
	Builtin  bool    `json:"builtin"`
	Hits     int64   `json:"hits"`
}

// Patterns returns a snapshot of every registered pattern.
func (s *InjectScanner) Patterns() []PatternRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PatternRow, 0, len(s.patterns))
	for i := range s.patterns {
		p := &s.patterns[i]
		out = append(out, PatternRow{
			Name:     p.name,
			Source:   p.source,
			Severity: p.severity,
			Builtin:  p.builtin,
			Hits:     p.hits.Load(),
		})
	}
	return out
}

// InjectStats is the global counters snapshot.
type InjectStats struct {
	TotalScans    int64   `json:"total_scans"`
	TotalHits     int64   `json:"total_hits"`
	HitRate       float64 `json:"hit_rate"`
	TotalPatterns int     `json:"total_patterns"`
}

func (s *InjectScanner) Stats() InjectStats {
	scans := s.totalScans.Load()
	hits := s.totalHits.Load()
	rate := 0.0
	if scans > 0 {
		rate = float64(hits) / float64(scans)
	}
	s.mu.RLock()
	n := len(s.patterns)
	s.mu.RUnlock()
	return InjectStats{
		TotalScans:    scans,
		TotalHits:     hits,
		HitRate:       rate,
		TotalPatterns: n,
	}
}

// Reset wipes the per-pattern + global counters. Custom patterns
// stay registered.
func (s *InjectScanner) Reset() {
	s.mu.RLock()
	for i := range s.patterns {
		s.patterns[i].hits.Store(0)
	}
	s.mu.RUnlock()
	s.totalScans.Store(0)
	s.totalHits.Store(0)
}

var (
	ErrPatternExists  = errors.New("PATTERNEXISTS pattern with this name already registered")
	ErrPatternBuiltin = errors.New("PATTERNBUILTIN built-in patterns cannot be removed")
	ErrUnknownPattern = errors.New("UNKNOWNPATTERN no pattern with this name")
)

// formatScanResult builds a stable string representation for the
// RESP layer when ScanAll has multiple hits. Kept here so the test
// can verify formatting without importing the resp package.
func formatScanResult(hits []ScanHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(h.Name)
	}
	return b.String()
}
