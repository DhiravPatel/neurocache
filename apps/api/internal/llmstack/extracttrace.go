package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ExtractTraceStore is the field-level provenance layer for
// structured extraction.
//
// Pipelines that pull structured fields out of unstructured docs
// (legal: parties + amounts from a contract; medical: diagnosis +
// medication from a discharge summary; finance: line items from
// an invoice) are *required* to show their work in any audited
// setting. The output isn't just {amount: 42000}, it's
// {amount: 42000, span: [start, end], source_text: "$42,000.00"}.
// Every team rolls this glue by hand and gets it badly wrong
// somewhere — usually the span doesn't survive a JSON round-trip
// or the LLM hallucinates a value that isn't anywhere in the source.
//
// EXTRACT.TRACE.* makes provenance a first-class primitive:
//
//   NEW    create an extraction record bound to a specific source
//          text.
//   SET    record one field with its substantiating span +
//          confidence. The span MUST point into the source text.
//   GET    retrieve one field's value + span + confidence.
//   ALL    every field at once.
//   VERIFY check that each field's span actually contains the
//          claimed value (catches LLM hallucinations).
//   LIST / DROP / STATS
//
// Commands:
//
//   EXTRACT.TRACE.NEW    extract-id source-text
//   EXTRACT.TRACE.SET    extract-id field VALUE value SPAN start end
//        [CONFIDENCE c]
//   EXTRACT.TRACE.GET    extract-id field
//        → {value, span_start, span_end, source_span, confidence}
//   EXTRACT.TRACE.ALL    extract-id
//   EXTRACT.TRACE.VERIFY extract-id
//        → {valid, issues:[{field, code, message}], n_fields}
//        codes: hallucination | bad-span
//   EXTRACT.TRACE.LIST
//   EXTRACT.TRACE.DROP   extract-id|ALL
//   EXTRACT.TRACE.STATS
//
// Hot path: SET is one map insert. VERIFY is O(fields × span_len).
type ExtractTraceStore struct {
	mu       sync.RWMutex
	records  map[string]*extractRecord

	totalNew    atomic.Int64
	totalSets   atomic.Int64
	totalVerifies atomic.Int64
}

type extractRecord struct {
	mu     sync.RWMutex
	source string
	fields map[string]extractField
	order  []string // for ALL output
}

type extractField struct {
	Value      string
	Start      int
	End        int
	Confidence float64
}

// NewExtractTraceStore returns an empty store.
func NewExtractTraceStore() *ExtractTraceStore {
	return &ExtractTraceStore{records: map[string]*extractRecord{}}
}

// New creates an extraction bound to a source. Calling on an existing
// extract-id resets it.
func (e *ExtractTraceStore) New(extractID, source string) error {
	if extractID == "" {
		return errors.New("extract_id required")
	}
	e.totalNew.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.records[extractID] = &extractRecord{
		source: source,
		fields: map[string]extractField{},
	}
	return nil
}

// Set records one field. start/end are byte offsets into the source.
// Same field is replaced on re-Set.
func (e *ExtractTraceStore) Set(extractID, field, value string, start, end int, confidence float64) error {
	if extractID == "" {
		return errors.New("extract_id required")
	}
	if field == "" {
		return errors.New("field name required")
	}
	if confidence < 0 || confidence > 1 {
		return errors.New("confidence must be in [0,1]")
	}
	e.totalSets.Add(1)
	e.mu.RLock()
	r, ok := e.records[extractID]
	e.mu.RUnlock()
	if !ok {
		return errors.New("unknown extract_id (call EXTRACT.TRACE.NEW first): " + extractID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if start < 0 || end < start || end > len(r.source) {
		return errors.New("span out of range")
	}
	if _, exists := r.fields[field]; !exists {
		r.order = append(r.order, field)
	}
	r.fields[field] = extractField{
		Value: value, Start: start, End: end, Confidence: confidence,
	}
	return nil
}

// ExtractFieldRow is one row of GET / ALL output.
type ExtractFieldRow struct {
	Field      string  `json:"field"`
	Value      string  `json:"value"`
	SpanStart  int     `json:"span_start"`
	SpanEnd    int     `json:"span_end"`
	SourceSpan string  `json:"source_span"`
	Confidence float64 `json:"confidence"`
}

// Get returns one field's full row.
func (e *ExtractTraceStore) Get(extractID, field string) (ExtractFieldRow, bool) {
	e.mu.RLock()
	r, ok := e.records[extractID]
	e.mu.RUnlock()
	if !ok {
		return ExtractFieldRow{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.fields[field]
	if !ok {
		return ExtractFieldRow{}, false
	}
	return ExtractFieldRow{
		Field: field, Value: f.Value,
		SpanStart: f.Start, SpanEnd: f.End,
		SourceSpan: r.source[f.Start:f.End],
		Confidence: f.Confidence,
	}, true
}

// All returns every field in insertion order.
func (e *ExtractTraceStore) All(extractID string) ([]ExtractFieldRow, bool) {
	e.mu.RLock()
	r, ok := e.records[extractID]
	e.mu.RUnlock()
	if !ok {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ExtractFieldRow, 0, len(r.order))
	for _, name := range r.order {
		f := r.fields[name]
		out = append(out, ExtractFieldRow{
			Field: name, Value: f.Value,
			SpanStart: f.Start, SpanEnd: f.End,
			SourceSpan: r.source[f.Start:f.End],
			Confidence: f.Confidence,
		})
	}
	return out, true
}

// ExtractVerifyIssue is one row of VERIFY output.
type ExtractVerifyIssue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`    // hallucination | bad-span
	Message string `json:"message"`
}

// ExtractVerifyResult is VERIFY's return.
type ExtractVerifyResult struct {
	ExtractID string               `json:"extract_id"`
	Valid     bool                 `json:"valid"`
	NFields   int                  `json:"n_fields"`
	Issues    []ExtractVerifyIssue `json:"issues"`
}

// Verify checks that every field's span actually contains the claimed
// value. If the value is purely numeric (no letters), comparison is
// substring-after-normalisation (strip non-digit chars from both
// sides). Otherwise a case-insensitive substring check.
func (e *ExtractTraceStore) Verify(extractID string) (ExtractVerifyResult, error) {
	if extractID == "" {
		return ExtractVerifyResult{}, errors.New("extract_id required")
	}
	e.totalVerifies.Add(1)
	e.mu.RLock()
	r, ok := e.records[extractID]
	e.mu.RUnlock()
	if !ok {
		return ExtractVerifyResult{}, errors.New("unknown extract_id: " + extractID)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := ExtractVerifyResult{
		ExtractID: extractID,
		NFields:   len(r.order),
		Valid:     true,
	}
	for _, name := range r.order {
		f := r.fields[name]
		if f.Start < 0 || f.End > len(r.source) || f.End < f.Start {
			out.Issues = append(out.Issues, ExtractVerifyIssue{
				Field: name, Code: "bad-span",
				Message: "span out of range",
			})
			out.Valid = false
			continue
		}
		span := r.source[f.Start:f.End]
		if !valueInSpan(f.Value, span) {
			out.Issues = append(out.Issues, ExtractVerifyIssue{
				Field: name, Code: "hallucination",
				Message: "value '" + f.Value + "' not found in span '" + span + "'",
			})
			out.Valid = false
		}
	}
	return out, nil
}

// List returns every extract id, sorted.
func (e *ExtractTraceStore) List() []string {
	e.mu.RLock()
	out := make([]string, 0, len(e.records))
	for k := range e.records {
		out = append(out, k)
	}
	e.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Drop removes an extract. extractID="ALL" wipes everything.
func (e *ExtractTraceStore) Drop(extractID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if extractID == "ALL" {
		n := len(e.records)
		e.records = map[string]*extractRecord{}
		return n
	}
	if _, ok := e.records[extractID]; ok {
		delete(e.records, extractID)
		return 1
	}
	return 0
}

// ExtractTraceStats is the global snapshot.
type ExtractTraceStats struct {
	Extracts      int   `json:"extracts"`
	TotalNew      int64 `json:"total_new"`
	TotalSets     int64 `json:"total_sets"`
	TotalVerifies int64 `json:"total_verifies"`
}

func (e *ExtractTraceStore) Stats() ExtractTraceStats {
	e.mu.RLock()
	n := len(e.records)
	e.mu.RUnlock()
	return ExtractTraceStats{
		Extracts:      n,
		TotalNew:      e.totalNew.Load(),
		TotalSets:     e.totalSets.Load(),
		TotalVerifies: e.totalVerifies.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

// valueInSpan returns true if the span substantiates the value.
// Numeric values are compared after stripping non-digit characters
// from both sides (so "$42,000" in source substantiates "42000").
// Other values use case-insensitive substring.
func valueInSpan(value, span string) bool {
	if value == "" {
		return true // empty value is trivially substantiated
	}
	lowSpan := strings.ToLower(span)
	lowVal := strings.ToLower(value)
	if strings.Contains(lowSpan, lowVal) {
		return true
	}
	// Numeric fallback: strip non-digits and compare
	stripVal := stripNonDigits(value)
	if stripVal == "" {
		return false
	}
	stripSpan := stripNonDigits(span)
	return strings.Contains(stripSpan, stripVal)
}

func stripNonDigits(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
