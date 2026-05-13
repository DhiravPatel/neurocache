package llmstack

import (
	"strings"
	"sync/atomic"
	"unicode"
)

// PromptShrinker compresses prompts before they're sent to an LLM.
// Every token saved is real money — even 10% reduction across
// millions of calls is significant — but the same shrinking logic
// (collapse whitespace, drop stopwords, truncate) gets reimplemented
// in every app with subtly different rules.
//
// SHRINK.* gives the cache one command:
//
//   SHRINK.TEXT text [STRATEGY whitespace|stopwords|truncate|all]
//                    [TARGET tokens] [MODEL m]
//
// Strategies (composable via "all" or comma-separated chain):
//
//   - whitespace: collapse runs of whitespace → single space,
//                 strip leading / trailing whitespace, normalise
//                 line endings. Typical savings: 5-15% on prose.
//   - stopwords:  drop common English stop words (the, a, an, is,
//                 are, was, were, be, been, being, have, has, had,
//                 do, does, did, will, would, could, should). Aware
//                 of common code patterns (don't touch `is_admin`).
//                 Typical savings: 10-25% on prose.
//   - truncate:   keep first TARGET tokens (or last, with
//                 `truncate:end`). For when context blew the budget
//                 entirely.
//   - all:        whitespace → stopwords → (truncate if TARGET set).
//
// Returns the shrunk text plus token-count delta + ratio so apps
// can monitor savings. Pure compute, atomic counters, lock-free
// on the hot path.
type PromptShrinker struct {
	// Optional reference to a Tokens estimator; nil falls back to
	// a 4-chars-per-token approximation which is good enough for
	// telemetry.
	tokens *Tokens

	totalRuns       atomic.Int64
	totalTokensIn   atomic.Int64
	totalTokensOut  atomic.Int64
	totalTokensSaved atomic.Int64
}

// NewPromptShrinker returns a shrinker. Passing a Tokens estimator
// is optional; without one, the model parameter is ignored and the
// 4-char approximation is used.
func NewPromptShrinker(tk *Tokens) *PromptShrinker {
	return &PromptShrinker{tokens: tk}
}

// ShrinkOpts configures SHRINK.TEXT.
type ShrinkOpts struct {
	Strategy string // "whitespace" | "stopwords" | "truncate" | "all" | "whitespace,stopwords"
	Target   int    // for truncate
	Model    string // for token counting
	FromEnd  bool   // truncate keeps last N tokens instead of first N
}

// ShrinkResult is the SHRINK.TEXT return.
type ShrinkResult struct {
	Text         string  `json:"text"`
	OriginalChars int    `json:"original_chars"`
	ShrunkChars  int     `json:"shrunk_chars"`
	OriginalToks int     `json:"original_tokens"`
	ShrunkToks   int     `json:"shrunk_tokens"`
	Ratio        float64 `json:"ratio"`          // shrunk / original (lower is better)
	Saved        int     `json:"tokens_saved"`
	Strategy     string  `json:"strategy"`
}

// Shrink applies the requested strategy and returns the result.
// Strategy defaults to "all".
func (s *PromptShrinker) Shrink(text string, opts ShrinkOpts) ShrinkResult {
	s.totalRuns.Add(1)
	strategy := opts.Strategy
	if strategy == "" {
		strategy = "all"
	}
	steps := expandStrategy(strategy, opts.Target > 0)

	originalChars := len(text)
	originalToks := s.countTokens(opts.Model, text)

	out := text
	for _, step := range steps {
		switch step {
		case "whitespace":
			out = shrinkWhitespace(out)
		case "stopwords":
			out = shrinkStopwordsApply(out)
		case "truncate":
			out = shrinkTruncate(s, opts.Model, out, opts.Target, opts.FromEnd)
		}
	}

	shrunkToks := s.countTokens(opts.Model, out)
	saved := originalToks - shrunkToks
	if saved < 0 {
		saved = 0
	}
	ratio := 1.0
	if originalToks > 0 {
		ratio = float64(shrunkToks) / float64(originalToks)
	}
	s.totalTokensIn.Add(int64(originalToks))
	s.totalTokensOut.Add(int64(shrunkToks))
	s.totalTokensSaved.Add(int64(saved))

	return ShrinkResult{
		Text:          out,
		OriginalChars: originalChars,
		ShrunkChars:   len(out),
		OriginalToks:  originalToks,
		ShrunkToks:    shrunkToks,
		Ratio:         ratio,
		Saved:         saved,
		Strategy:      strings.Join(steps, ","),
	}
}

// ShrinkStats is the global counters snapshot.
type ShrinkStats struct {
	TotalRuns       int64   `json:"total_runs"`
	TotalTokensIn   int64   `json:"total_tokens_in"`
	TotalTokensOut  int64   `json:"total_tokens_out"`
	TotalTokensSaved int64  `json:"total_tokens_saved"`
	AvgRatio        float64 `json:"avg_ratio"`
}

func (s *PromptShrinker) Stats() ShrinkStats {
	in := s.totalTokensIn.Load()
	out := s.totalTokensOut.Load()
	ratio := 1.0
	if in > 0 {
		ratio = float64(out) / float64(in)
	}
	return ShrinkStats{
		TotalRuns:       s.totalRuns.Load(),
		TotalTokensIn:   in,
		TotalTokensOut:  out,
		TotalTokensSaved: s.totalTokensSaved.Load(),
		AvgRatio:        ratio,
	}
}

// ─── strategies ────────────────────────────────────────────────

func expandStrategy(strategy string, hasTarget bool) []string {
	switch strings.ToLower(strategy) {
	case "all":
		if hasTarget {
			return []string{"whitespace", "stopwords", "truncate"}
		}
		return []string{"whitespace", "stopwords"}
	default:
		parts := strings.Split(strategy, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(strings.ToLower(p))
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
}

func shrinkWhitespace(text string) string {
	// Single pass: collapse any run of whitespace into one space,
	// trim leading/trailing.
	var b strings.Builder
	b.Grow(len(text))
	inWS := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !inWS && b.Len() > 0 {
				b.WriteByte(' ')
				inWS = true
			}
			continue
		}
		inWS = false
		b.WriteRune(r)
	}
	out := b.String()
	return strings.TrimRight(out, " ")
}

// stopwords list — common English filler. Casing-tolerant lookup.
// Deliberately conservative: includes only words that almost never
// carry semantic content in context. "not" / "no" are EXCLUDED
// because they flip meaning.
var shrinkStopwords = map[string]bool{
	"a": true, "an": true, "the": true,
	"is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "am": true,
	"have": true, "has": true, "had": true, "having": true,
	"do": true, "does": true, "did": true, "doing": true,
	"will": true, "would": true, "could": true, "should": true, "shall": true,
	"may": true, "might": true, "must": true, "can": true,
	"of": true, "in": true, "on": true, "at": true, "by": true,
	"for": true, "with": true, "from": true, "to": true,
	"as": true, "that": true, "which": true,
}

func shrinkStopwordsApply(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	tokens := splitPreservePunct(text)
	first := true
	for _, t := range tokens {
		if shrinkStopwords[strings.ToLower(t.word)] && !t.preserve {
			continue
		}
		if !first && !isPunctOnly(t.word) {
			b.WriteByte(' ')
		}
		b.WriteString(t.word)
		first = false
	}
	return strings.TrimSpace(b.String())
}

type splitToken struct {
	word     string
	preserve bool // identifier-like words we shouldn't drop
}

// splitPreservePunct splits on whitespace but keeps inline
// punctuation attached to the preceding word. Identifier-style
// tokens (containing _, contains uppercase, or digits) are flagged
// as preserve so stopword-removal doesn't strip them.
func splitPreservePunct(text string) []splitToken {
	out := make([]splitToken, 0, 32)
	var cur strings.Builder
	flush := func() {
		w := cur.String()
		if w == "" {
			return
		}
		out = append(out, splitToken{
			word:     w,
			preserve: looksIdentifier(w),
		})
		cur.Reset()
	}
	for _, r := range text {
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

func looksIdentifier(w string) bool {
	hasDigit := false
	hasUnder := false
	// Internal upper-case (CamelCase) is identifier-y; a single
	// leading-cap (sentence start) isn't.
	internalUpper := false
	for i, r := range w {
		if i > 0 && unicode.IsUpper(r) {
			internalUpper = true
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
		if r == '_' {
			hasUnder = true
		}
	}
	return internalUpper || hasDigit || hasUnder
}

func isPunctOnly(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func shrinkTruncate(ps *PromptShrinker, model, text string, target int, fromEnd bool) string {
	if target <= 0 {
		return text
	}
	cur := ps.countTokens(model, text)
	if cur <= target {
		return text
	}
	// Binary-search the character boundary that fits in target tokens.
	// Token counting is a fast estimate so this is microseconds even
	// on long inputs.
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		var part string
		if fromEnd {
			part = text[len(text)-mid:]
		} else {
			part = text[:mid]
		}
		if ps.countTokens(model, part) <= target {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if fromEnd {
		return text[len(text)-lo:]
	}
	return text[:lo]
}

// countTokens dispatches to the Tokens estimator if available, else
// approximates as len(text)/4.
func (s *PromptShrinker) countTokens(model, text string) int {
	if s.tokens != nil {
		return s.tokens.Count(model, text)
	}
	return (len(text) + 3) / 4
}
