package llmstack

import (
	"strings"
	"sync/atomic"
)

// Chunker splits text into overlapping chunks for RAG ingestion.
// Every RAG app reimplements this glue: take a doc, chunk it,
// embed each chunk, store in vector DB. Doing it RESP-side means:
//
//   - Same chunking semantics across every worker / language client
//   - Chunking happens before the embedding cache layer, so apps
//     can pipeline CHUNK → EMB.CACHE_GET → EMB.CACHE_SET cleanly
//   - One source of truth for the chunk-overlap parameter that
//     determines retrieval quality
//
// Strategies (`STRATEGY` arg of CHUNK.TEXT):
//
//   char     — fixed-size byte windows; fastest, naive
//   sentence — split on .!?\n boundaries, group up to size
//   paragraph— split on \n\n, group up to size
//   token    — uses Tokens.Count for per-chunk token budgeting
//              (model arg required)
//
// Overlap (`OVERLAP` arg) carries the last N chars/tokens of one
// chunk into the next, so context isn't lost across boundaries.
// Recommended for most RAG: ~10-20% overlap.
type Chunker struct {
	tokens       *Tokens // for the "token" strategy
	totalChunks  atomic.Int64
}

// NewChunker links to a Tokens instance for token-aware chunking.
// Char/sentence/paragraph strategies don't require it.
func NewChunker(t *Tokens) *Chunker { return &Chunker{tokens: t} }

// ChunkOpts is what callers pass to Chunk.
type ChunkOpts struct {
	Strategy string // "char" | "sentence" | "paragraph" | "token"
	Size     int    // chars (or tokens for "token" strategy)
	Overlap  int    // chars (or tokens for "token" strategy)
	Model    string // required for "token" strategy
}

// Chunk applies the strategy and returns the chunks. Empty input
// returns empty slice. Invalid opts default to char/1024/0.
func (c *Chunker) Chunk(text string, opts ChunkOpts) []string {
	if text == "" {
		return nil
	}
	if opts.Size <= 0 {
		opts.Size = 1024
	}
	if opts.Overlap < 0 {
		opts.Overlap = 0
	}
	if opts.Overlap >= opts.Size {
		// Overlap larger than chunk size makes no sense — clamp
		// to half the chunk size, the largest sensible value.
		opts.Overlap = opts.Size / 2
	}
	var chunks []string
	switch strings.ToLower(opts.Strategy) {
	case "sentence":
		chunks = c.chunkBy(text, opts.Size, opts.Overlap, splitSentences)
	case "paragraph":
		chunks = c.chunkBy(text, opts.Size, opts.Overlap, splitParagraphs)
	case "token":
		if c.tokens != nil && opts.Model != "" {
			chunks = c.tokens.Split(opts.Model, text, opts.Size)
		} else {
			chunks = c.chunkChar(text, opts.Size, opts.Overlap)
		}
	default: // "char" or unknown
		chunks = c.chunkChar(text, opts.Size, opts.Overlap)
	}
	c.totalChunks.Add(int64(len(chunks)))
	return chunks
}

// chunkChar — fixed-byte windows with overlap. Fast path.
func (c *Chunker) chunkChar(text string, size, overlap int) []string {
	var out []string
	step := size - overlap
	if step <= 0 {
		step = size
	}
	for i := 0; i < len(text); i += step {
		end := i + size
		if end > len(text) {
			end = len(text)
		}
		out = append(out, text[i:end])
		if end == len(text) {
			break
		}
	}
	return out
}

// chunkBy groups units (sentences/paragraphs) until the byte budget
// is reached. Overlap is realized by re-prepending the last N chars
// of the prior chunk.
func (c *Chunker) chunkBy(
	text string,
	size, overlap int,
	splitter func(string) []string,
) []string {
	units := splitter(text)
	if len(units) == 0 {
		return nil
	}
	var out []string
	var cur strings.Builder
	for _, u := range units {
		if cur.Len()+len(u) > size && cur.Len() > 0 {
			chunk := cur.String()
			out = append(out, chunk)
			cur.Reset()
			// Re-seed with overlap from end of previous chunk.
			if overlap > 0 && len(chunk) > overlap {
				cur.WriteString(chunk[len(chunk)-overlap:])
				if !strings.HasSuffix(chunk, " ") {
					cur.WriteString(" ")
				}
			}
		}
		cur.WriteString(u)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// splitSentences breaks on .!? followed by whitespace. Naive but
// covers >90% of well-formed prose. Code/data inputs should use
// the char strategy instead.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c == '.' || c == '!' || c == '?') && i+1 < len(text) {
			next := text[i+1]
			if next == ' ' || next == '\n' || next == '\t' {
				// Keep the trailing punctuation + space with the sentence
				out = append(out, text[start:i+2])
				start = i + 2
				i++ // skip the whitespace we just consumed
			}
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// splitParagraphs breaks on blank lines (\n\n).
func splitParagraphs(text string) []string {
	parts := strings.Split(text, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p+"\n\n")
		}
	}
	return out
}

// ChunkStats is the global counters snapshot.
type ChunkStats struct {
	TotalChunks int64 `json:"total_chunks"`
}

func (c *Chunker) Stats() ChunkStats {
	return ChunkStats{TotalChunks: c.totalChunks.Load()}
}

// ─── CONTEXT.ASSEMBLE ─────────────────────────────────────────

// ContextSection is one piece the assembler considers including.
type ContextSection struct {
	ID       string
	Text     string
	Priority int    // higher = include first
	Tokens   int    // pre-computed; 0 means "compute via tokens"
}

// AssembleResult — what fit, what didn't, total tokens used.
type AssembleResult struct {
	Used       []string `json:"used"`         // section IDs included, in priority order
	Skipped    []string `json:"skipped"`      // section IDs that didn't fit
	TotalToks  int      `json:"total_tokens"`
	BudgetToks int      `json:"budget_tokens"`
	Combined   string   `json:"combined"`     // concatenated text, separator '\n\n---\n\n'
}

// AssembleContext implements priority-greedy fitting under a token
// budget. Sections are sorted by priority descending; we add each
// while it fits, skip otherwise. Final output is the concatenated
// text — apps drop it straight into a model's context.
//
// Use case: "I have a system prompt (priority 100), 5 RAG hits
// (priority 50-90), and 10 conversation turns (priority 30-70).
// Give me the best 100k-token combination."
func (c *Chunker) AssembleContext(
	model string,
	budgetTokens int,
	sections []ContextSection,
) AssembleResult {
	// Priority desc; stable to preserve caller order on ties.
	sorted := make([]ContextSection, len(sections))
	copy(sorted, sections)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Priority > sorted[j-1].Priority; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	res := AssembleResult{BudgetToks: budgetTokens}
	used := 0
	var pieces []string
	for i := range sorted {
		s := &sorted[i]
		toks := s.Tokens
		if toks == 0 && c.tokens != nil {
			toks = c.tokens.Count(model, s.Text)
		}
		if used+toks > budgetTokens {
			res.Skipped = append(res.Skipped, s.ID)
			continue
		}
		used += toks
		res.Used = append(res.Used, s.ID)
		pieces = append(pieces, s.Text)
	}
	res.TotalToks = used
	res.Combined = strings.Join(pieces, "\n\n---\n\n")
	return res
}
