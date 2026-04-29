package aiops

import (
	"sync"
	"time"
)

// Lineage tracks provenance: which source documents informed which
// generated outputs. Critical for AI compliance (EU AI Act, healthcare,
// finance) where auditors need to answer "where did this paragraph
// come from?". The cache is append-only — records never overwrite —
// because compliance demands an immutable trail.
type Lineage struct {
	mu      sync.RWMutex
	byOut   map[string][]Citation // output_id → citations
	bySrc   map[string][]string   // source_id → output_ids that cited it
}

// Citation is a single (output → source) provenance edge.
type Citation struct {
	OutputID    string    `json:"output_id"`
	SourceID    string    `json:"source_id"`
	Confidence  float64   `json:"confidence,omitempty"` // 0-1 if the caller computed one
	Snippet     string    `json:"snippet,omitempty"`    // optional excerpt
	RecordedAt  time.Time `json:"recorded_at"`
}

// NewLineage returns an empty manager.
func NewLineage() *Lineage {
	return &Lineage{
		byOut: map[string][]Citation{},
		bySrc: map[string][]string{},
	}
}

// Record links output → source. confidence is optional (pass 0 if the
// caller has no signal); snippet is an optional excerpt.
func (l *Lineage) Record(outputID, sourceID, snippet string, confidence float64) {
	c := Citation{
		OutputID:   outputID,
		SourceID:   sourceID,
		Confidence: confidence,
		Snippet:    snippet,
		RecordedAt: time.Now(),
	}
	l.mu.Lock()
	l.byOut[outputID] = append(l.byOut[outputID], c)
	l.bySrc[sourceID] = append(l.bySrc[sourceID], outputID)
	l.mu.Unlock()
}

// List returns every citation for an output, in insertion order.
func (l *Lineage) List(outputID string) []Citation {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Citation, len(l.byOut[outputID]))
	copy(out, l.byOut[outputID])
	return out
}

// Sources returns just the unique source IDs cited by an output.
func (l *Lineage) Sources(outputID string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	seen := map[string]bool{}
	for _, c := range l.byOut[outputID] {
		seen[c.SourceID] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// Consumers returns the output IDs that have cited a particular source
// — used to answer "if I retract this document, which generated
// outputs need a re-check?".
func (l *Lineage) Consumers(sourceID string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, len(l.bySrc[sourceID]))
	copy(out, l.bySrc[sourceID])
	return out
}

// Forget drops every citation for an output. Use with care — this is
// intended for retention-window cleanup, not normal operations.
// Returns the count of citations removed.
func (l *Lineage) Forget(outputID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.byOut[outputID])
	for _, c := range l.byOut[outputID] {
		// also clean reverse index
		filtered := l.bySrc[c.SourceID][:0]
		for _, oid := range l.bySrc[c.SourceID] {
			if oid != outputID {
				filtered = append(filtered, oid)
			}
		}
		if len(filtered) == 0 {
			delete(l.bySrc, c.SourceID)
		} else {
			l.bySrc[c.SourceID] = filtered
		}
	}
	delete(l.byOut, outputID)
	return n
}

// LineageStats is the snapshot shape.
type LineageStats struct {
	Outputs    int `json:"outputs"`
	Sources    int `json:"unique_sources"`
	Citations  int `json:"total_citations"`
}

// Stats snapshots state.
func (l *Lineage) Stats() LineageStats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	total := 0
	for _, cs := range l.byOut {
		total += len(cs)
	}
	return LineageStats{
		Outputs:   len(l.byOut),
		Sources:   len(l.bySrc),
		Citations: total,
	}
}
