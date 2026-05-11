package llmstack

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// CitationExtractor parses citation markers from LLM output and
// validates them against a caller-supplied source set. Every RAG app
// instructs the model to cite its sources with markers like "[1]",
// "[Source-A]", or "[Wikipedia-2024]" — but apps then have to write
// the resolution code themselves. The same regex + lookup + "is this
// reference real?" logic gets rebuilt in every project, often badly:
//
//   - Off-by-one bugs ("[1]" matching index 0 vs index 1)
//   - Citation IDs the model invented that don't exist
//   - Duplicate or missing markers
//   - Citations buried in code fences vs. prose
//
// CITE.* gives the cache a single command set:
//
//   CITE.EXTRACT text [PATTERN regex]
//      → array of {marker, label, start, end}
//   CITE.RESOLVE text SOURCE id text SOURCE id text ...
//      → array of {marker, label, valid, source_text}
//   CITE.VALIDATE text SOURCE id text ...
//      → {valid: 0|1, total: N, valid_n, invalid_n, invalid_labels[]}
//   CITE.STATS
//
// The default pattern is `\[(\d+|[A-Za-z][A-Za-z0-9_-]*)\]` which
// matches "[1]", "[42]", "[Source-A]", "[wiki_2024]". Apps with
// non-standard markers (e.g. "<cite:1>") supply their own regex via
// PATTERN — the first capture group becomes the label.
//
// Storage: pure compute, no state per call. Counters are atomic so
// stats are lock-free on the hot path.
type CitationExtractor struct {
	defaultPattern *regexp.Regexp

	totalExtracts atomic.Int64
	totalResolves atomic.Int64
	totalCitations atomic.Int64
	totalInvalid  atomic.Int64

	// Cache of compiled custom patterns. Apps rarely change patterns
	// mid-flight, so this saves the recompile cost.
	mu       sync.RWMutex
	patterns map[string]*regexp.Regexp
}

// NewCitationExtractor returns an extractor with the standard
// "[label]" pattern compiled.
func NewCitationExtractor() *CitationExtractor {
	def := regexp.MustCompile(`\[(\d+|[A-Za-z][A-Za-z0-9_-]*)\]`)
	return &CitationExtractor{
		defaultPattern: def,
		patterns:       map[string]*regexp.Regexp{},
	}
}

// Citation is one parsed marker.
type Citation struct {
	Marker string `json:"marker"` // e.g. "[1]" — the whole match
	Label  string `json:"label"`  // e.g. "1" — the first capture group
	Start  int    `json:"start"`  // byte offset in text
	End    int    `json:"end"`
}

// Extract returns every citation found in `text`. PATTERN overrides
// the default regex; bad pattern returns an error.
func (c *CitationExtractor) Extract(text, pattern string) ([]Citation, error) {
	c.totalExtracts.Add(1)
	re, err := c.regexFor(pattern)
	if err != nil {
		return nil, err
	}
	matches := re.FindAllStringSubmatchIndex(text, -1)
	out := make([]Citation, 0, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		label := ""
		if m[2] >= 0 && m[3] <= len(text) {
			label = text[m[2]:m[3]]
		}
		out = append(out, Citation{
			Marker: text[m[0]:m[1]],
			Label:  label,
			Start:  m[0],
			End:    m[1],
		})
	}
	c.totalCitations.Add(int64(len(out)))
	return out, nil
}

// ResolvedCitation is one citation paired with its source text.
type ResolvedCitation struct {
	Marker     string `json:"marker"`
	Label      string `json:"label"`
	Valid      bool   `json:"valid"`
	SourceText string `json:"source_text,omitempty"`
}

// Resolve maps every citation marker to its source. Sources are a
// label → text map; markers whose label isn't in the map are
// returned with valid=false.
//
// Numeric labels also match against the position-indexed source
// list — apps can pass sources by 1-based position ("1", "2", ...)
// or by string ID ("wiki-2024") interchangeably.
func (c *CitationExtractor) Resolve(text, pattern string, sources map[string]string, order []string) ([]ResolvedCitation, error) {
	c.totalResolves.Add(1)
	cites, err := c.Extract(text, pattern)
	if err != nil {
		return nil, err
	}
	out := make([]ResolvedCitation, 0, len(cites))
	for _, ct := range cites {
		r := ResolvedCitation{Marker: ct.Marker, Label: ct.Label}
		if src, ok := sources[ct.Label]; ok {
			r.Valid = true
			r.SourceText = src
		} else if pos, ok := positionLookup(ct.Label, order); ok {
			r.Valid = true
			r.SourceText = sources[order[pos]]
		} else {
			c.totalInvalid.Add(1)
		}
		out = append(out, r)
	}
	return out, nil
}

// ValidateResult is CITE.VALIDATE return.
type ValidateCitationsResult struct {
	Valid           bool     `json:"valid"`
	Total           int      `json:"total"`
	ValidN          int      `json:"valid_n"`
	InvalidN        int      `json:"invalid_n"`
	InvalidLabels   []string `json:"invalid_labels,omitempty"`
	UnreferencedIDs []string `json:"unreferenced_ids,omitempty"`
}

// Validate is the binary-verdict version of Resolve. Returns the
// invalid markers + any source IDs the LLM never cited (sometimes
// useful telemetry: the model ignored a source that was relevant).
func (c *CitationExtractor) Validate(text, pattern string, sources map[string]string, order []string) (ValidateCitationsResult, error) {
	resolved, err := c.Resolve(text, pattern, sources, order)
	if err != nil {
		return ValidateCitationsResult{}, err
	}
	out := ValidateCitationsResult{
		Total: len(resolved),
		Valid: true,
	}
	cited := map[string]bool{}
	for _, r := range resolved {
		if r.Valid {
			out.ValidN++
			cited[r.Label] = true
		} else {
			out.InvalidN++
			out.InvalidLabels = append(out.InvalidLabels, r.Marker)
			out.Valid = false
		}
	}
	for _, id := range order {
		if !cited[id] {
			// Check position-style citation too: if id is at index i,
			// the model could have cited "[i+1]"; if neither name nor
			// position was cited, it's unreferenced.
			pos := ""
			for i, name := range order {
				if name == id {
					pos = intToString(i + 1)
					break
				}
			}
			if pos != "" && !cited[pos] {
				out.UnreferencedIDs = append(out.UnreferencedIDs, id)
			}
		}
	}
	sort.Strings(out.UnreferencedIDs)
	return out, nil
}

// CitationStats is the global counters snapshot.
type CitationStats struct {
	TotalExtracts  int64 `json:"total_extracts"`
	TotalResolves  int64 `json:"total_resolves"`
	TotalCitations int64 `json:"total_citations"`
	TotalInvalid   int64 `json:"total_invalid"`
}

func (c *CitationExtractor) Stats() CitationStats {
	return CitationStats{
		TotalExtracts:  c.totalExtracts.Load(),
		TotalResolves:  c.totalResolves.Load(),
		TotalCitations: c.totalCitations.Load(),
		TotalInvalid:   c.totalInvalid.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func (c *CitationExtractor) regexFor(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return c.defaultPattern, nil
	}
	c.mu.RLock()
	re, ok := c.patterns[pattern]
	c.mu.RUnlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.patterns[pattern] = re
	c.mu.Unlock()
	return re, nil
}

// positionLookup converts "1"/"2"/... to a zero-based index. Returns
// (idx, true) if the label is a 1-based position within `order`.
func positionLookup(label string, order []string) (int, bool) {
	n := 0
	for _, r := range label {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	if n < 1 || n > len(order) {
		return 0, false
	}
	return n - 1, true
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// CitationsToText is a small helper used in tests: dump citations in
// order, useful for visual diffs.
func CitationsToText(cites []Citation) string {
	var b strings.Builder
	for i, c := range cites {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c.Marker)
	}
	return b.String()
}
