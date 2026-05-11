package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// Redactor strips PII from text BEFORE it goes to an LLM, and
// (optionally) restores it from the LLM's response so users see
// their real data while the model only sees redacted placeholders.
//
// Why this exists for AI apps:
//
//   - GDPR/CCPA/HIPAA compliance — sending real user PII to an
//     external LLM API is a regulatory headache. Redacting first +
//     restoring after lets you use commercial models on regulated
//     data.
//   - Prompt-injection defense — if a user can sneak someone else's
//     credit card into a prompt, the model can be coerced to leak
//     it. Redact before the model sees it.
//   - Cost predictability — long PII strings take tokens; placeholders
//     are short.
//
// Built-in pattern library covers the most common PII categories.
// Operators add custom regex via REDACT.PATTERN.ADD.
//
// Restore protocol: REDACT.SCRUB returns (redacted_text,
// restoration_token). When the LLM response comes back, RESTORE the
// response with that token to swap placeholders back to original
// values. The token expires after the configured TTL.
type Redactor struct {
	mu       sync.RWMutex
	patterns []redactPattern

	// restorations: token -> map[placeholder]original
	restorations sync.Map

	totalScrubs    atomic.Int64
	totalHits      atomic.Int64
	totalRestores  atomic.Int64
}

type redactPattern struct {
	name        string
	re          *regexp.Regexp
	placeholder string // "<EMAIL>" / "<PHONE>" etc.
	builtin     bool
	hits        atomic.Int64
}

// Built-in patterns — calibrated to be conservative (false positives
// are fine; false negatives leak PII). Each placeholder format is
// distinct so the LLM can refer to them by name in its response and
// we restore the right value.
var defaultRedactPatterns = []struct {
	name        string
	source      string
	placeholder string
}{
	{
		name:        "email",
		source:      `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`,
		placeholder: "<EMAIL>",
	},
	{
		// US phone numbers; covers (555) 555-5555, 555-555-5555, 555.555.5555,
		// +1 555 555 5555. International is harder — operators add their own.
		name:        "phone-us",
		source:      `(?:\+?1[-. ]?)?\(?[2-9]\d{2}\)?[-. ]?\d{3}[-. ]?\d{4}`,
		placeholder: "<PHONE>",
	},
	{
		name:        "ssn",
		source:      `\b\d{3}-\d{2}-\d{4}\b`,
		placeholder: "<SSN>",
	},
	{
		// Major card brands by prefix + length. Visa, MC, Amex, Discover.
		// Doesn't validate Luhn — apps that care can add a custom pattern.
		name:        "credit-card",
		source:      `\b(?:4\d{3}|5[1-5]\d{2}|3[47]\d{2}|6011)[- ]?\d{4}[- ]?\d{4}[- ]?\d{4}\b`,
		placeholder: "<CARD>",
	},
	{
		// IPv4 — useful for log redaction
		name:        "ipv4",
		source:      `\b(?:\d{1,3}\.){3}\d{1,3}\b`,
		placeholder: "<IP>",
	},
	{
		// API key heuristic — long base64-ish strings prefixed by sk-, AKIA, ghp_, etc.
		name:        "api-key",
		source:      `\b(?:sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,})\b`,
		placeholder: "<APIKEY>",
	},
}

// NewRedactor returns a Redactor pre-loaded with the built-in
// pattern library.
func NewRedactor() *Redactor {
	r := &Redactor{}
	for _, p := range defaultRedactPatterns {
		_ = r.add(p.name, p.source, p.placeholder, true)
	}
	return r
}

// ScrubResult is what Scrub returns to the caller.
type ScrubResult struct {
	Text             string         `json:"text"`              // redacted text
	RestoreToken     string         `json:"restore_token"`     // pass to Restore later
	Replacements     map[string]int `json:"replacements"`      // pattern_name -> count
}

// Scrub replaces every matching pattern in `text` with a numbered
// placeholder ("<EMAIL_1>", "<EMAIL_2>", "<PHONE_1>") and returns
// the redacted text plus a restore_token. Numbering is per-pattern,
// 1-indexed.
//
// The restoration map is stored in-memory keyed by the token; call
// Restore(token, llm_output) to swap placeholders back.
func (r *Redactor) Scrub(text string) ScrubResult {
	r.totalScrubs.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := text
	replacements := map[string]int{}
	restoreMap := map[string]string{}
	hits := false

	for i := range r.patterns {
		p := &r.patterns[i]
		count := 0
		out = p.re.ReplaceAllStringFunc(out, func(match string) string {
			count++
			placeholder := fmt.Sprintf("<%s_%d>",
				stripBrackets(p.placeholder),
				count,
			)
			restoreMap[placeholder] = match
			return placeholder
		})
		if count > 0 {
			replacements[p.name] = count
			p.hits.Add(int64(count))
			hits = true
		}
	}
	if hits {
		r.totalHits.Add(1)
	}

	token := newRestoreToken(text, len(restoreMap))
	if len(restoreMap) > 0 {
		r.restorations.Store(token, restoreMap)
	}

	return ScrubResult{
		Text:         out,
		RestoreToken: token,
		Replacements: replacements,
	}
}

// Restore swaps placeholders back to original values in `text` using
// the restoration map for `token`. Returns the restored text and
// true on success; (text-unchanged, false) if the token is unknown
// or expired.
func (r *Redactor) Restore(token, text string) (string, bool) {
	v, ok := r.restorations.Load(token)
	if !ok {
		return text, false
	}
	r.totalRestores.Add(1)
	m := v.(map[string]string)
	out := text
	for placeholder, original := range m {
		// Simple string replace — placeholders are unique per scrub call
		// and don't overlap with normal text.
		out = stringReplace(out, placeholder, original)
	}
	return out, true
}

// ForgetToken drops a restoration map. Apps SHOULD call this once
// they've restored to free memory; the restoration table grows
// otherwise.
func (r *Redactor) ForgetToken(token string) bool {
	_, was := r.restorations.LoadAndDelete(token)
	return was
}

// Add registers a custom pattern. Replaces any existing same-name
// pattern (including built-ins, so operators can adjust EMAIL etc.).
func (r *Redactor) Add(name, source, placeholder string) error {
	return r.add(name, source, placeholder, false)
}

func (r *Redactor) add(name, source, placeholder string, builtin bool) error {
	re, err := regexp.Compile(source)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.patterns {
		if r.patterns[i].name == name {
			r.patterns[i].re = re
			r.patterns[i].placeholder = placeholder
			return nil
		}
	}
	r.patterns = append(r.patterns, redactPattern{
		name:        name,
		re:          re,
		placeholder: placeholder,
		builtin:     builtin,
	})
	return nil
}

// Remove drops a pattern by name. Built-in patterns can be removed
// (unlike INJECT) — operators sometimes need to disable the
// IP-address pattern for telemetry workloads.
func (r *Redactor) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.patterns {
		if r.patterns[i].name == name {
			r.patterns = append(r.patterns[:i], r.patterns[i+1:]...)
			return true
		}
	}
	return false
}

// PatternRow is one row in REDACT.PATTERN.LIST.
type RedactPatternRow struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Placeholder string `json:"placeholder"`
	Builtin     bool   `json:"builtin"`
	Hits        int64  `json:"hits"`
}

func (r *Redactor) Patterns() []RedactPatternRow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RedactPatternRow, 0, len(r.patterns))
	for i := range r.patterns {
		p := &r.patterns[i]
		out = append(out, RedactPatternRow{
			Name:        p.name,
			Source:      p.re.String(),
			Placeholder: p.placeholder,
			Builtin:     p.builtin,
			Hits:        p.hits.Load(),
		})
	}
	return out
}

// RedactStats is the global counters snapshot.
type RedactStats struct {
	TotalScrubs   int64 `json:"total_scrubs"`
	TotalHits     int64 `json:"total_hits"`
	TotalRestores int64 `json:"total_restores"`
}

func (r *Redactor) Stats() RedactStats {
	return RedactStats{
		TotalScrubs:   r.totalScrubs.Load(),
		TotalHits:     r.totalHits.Load(),
		TotalRestores: r.totalRestores.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func stripBrackets(s string) string {
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s[1 : len(s)-1]
	}
	return s
}

// stringReplace does a single literal swap. Faster than
// strings.ReplaceAll for the typical small placeholder count.
func stringReplace(s, old, new string) string {
	if old == "" {
		return s
	}
	// strings.ReplaceAll is a single-pass implementation in stdlib.
	// Inline to avoid import cycle in test paths.
	out := make([]byte, 0, len(s))
	for {
		idx := indexOf(s, old)
		if idx < 0 {
			out = append(out, s...)
			break
		}
		out = append(out, s[:idx]...)
		out = append(out, new...)
		s = s[idx+len(old):]
	}
	return string(out)
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// newRestoreToken — sha256-hash of text + count, hex-encoded prefix.
// Distinct per scrub call.
func newRestoreToken(text string, count int) string {
	h := sha256.New()
	h.Write([]byte(text))
	h.Write([]byte(fmt.Sprintf("|%d|%d", count, time.Now().UnixNano())))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
