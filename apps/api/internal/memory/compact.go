package memory

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
)

// Compactor folds a growing memory store into bounded, auditable summaries —
// and keeps the folding REVERSIBLE.
//
// It is distinct from MEMORY.CONSOLIDATE (which clusters+synthesizes within a
// user but discards the originals irrecoverably) and from RECALL (which only
// flags staleness): COMPACT writes a synthetic summary that records exactly
// which originals it folded (Entry.SourceIDs) AND retains a copy of those
// originals' payloads, so COMPACT.EXPAND can restore them. The id linkage is
// also surfaced to LINEAGE at the command layer, giving an audit trail of
// "this summary was built from those sources".
//
// Why retained copies: memory.Store only persists the folded ids in
// SourceIDs, never the originals' Text/Layer/Importance — Delete is a hard
// delete. So a Compactor that wants true reversibility must keep the payloads
// itself. They live here in a side store keyed by summary id (runtime state,
// like the other AI families).
type Compactor struct {
	store *Store

	mu     sync.Mutex
	folded map[string][]Entry // summary id → copies of the originals it folded

	totalApplied  atomic.Int64
	totalFolded   atomic.Int64
	totalExpanded atomic.Int64
}

// NewCompactor binds a compactor to a memory store.
func NewCompactor(store *Store) *Compactor {
	return &Compactor{store: store, folded: map[string][]Entry{}}
}

// CompactOptions tunes which entries fold and how.
type CompactOptions struct {
	Layer       Layer   // source layer to fold (default episodic)
	Threshold   float64 // cosine cluster threshold (default 0.85)
	MinSize     int     // minimum cluster size to fold (default 3)
	MaxAgeSec   int64   // only fold entries older than this many seconds (0 = no age filter)
	TargetBytes int64   // stop once ~this many source bytes are folded (0 = fold all eligible)
	Importance  float64 // importance stamped on the summary (default 0.5)
	Drop        bool    // delete the originals after folding (APPLY only)
}

func (o *CompactOptions) applyDefaults() {
	if o.Layer == "" {
		o.Layer = LayerEpisodic
	}
	if o.Threshold <= 0 || o.Threshold > 1 {
		o.Threshold = 0.85
	}
	if o.MinSize <= 0 {
		o.MinSize = 3
	}
	if o.Importance <= 0 {
		o.Importance = 0.5
	}
}

// PlanCluster is one proposed fold.
type PlanCluster struct {
	SourceIDs []string `json:"source_ids"`
	Summary   string   `json:"summary"`
	Bytes     int64    `json:"bytes"`
}

// CompactPlan is COMPACT.PLAN's read-only proposal.
type CompactPlan struct {
	User         string         `json:"user"`
	Layer        string         `json:"layer"`
	Clusters     []PlanCluster  `json:"clusters"`
	Entries      int            `json:"entries"`       // total originals that would fold
	BytesFolded  int64          `json:"bytes_folded"`  // source bytes removed
	SummaryBytes int64          `json:"summary_bytes"` // bytes the summaries add back
	NetBytes     int64          `json:"net_bytes"`     // bytes_folded - summary_bytes (savings)
}

// Plan proposes folds without mutating the store.
func (c *Compactor) Plan(userID string, opts CompactOptions) CompactPlan {
	opts.applyDefaults()
	clusters := c.cluster(userID, opts)
	plan := CompactPlan{User: userID, Layer: string(opts.Layer)}
	for _, cl := range clusters {
		summary := summarize(cl.entries, opts.Importance)
		pc := PlanCluster{Summary: summary, Bytes: cl.bytes}
		for _, e := range cl.entries {
			pc.SourceIDs = append(pc.SourceIDs, e.ID)
		}
		plan.Clusters = append(plan.Clusters, pc)
		plan.Entries += len(cl.entries)
		plan.BytesFolded += cl.bytes
		plan.SummaryBytes += int64(len(summary))
	}
	plan.NetBytes = plan.BytesFolded - plan.SummaryBytes
	return plan
}

// SummaryInfo is one written summary.
type SummaryInfo struct {
	SummaryID string   `json:"summary_id"`
	SourceIDs []string `json:"source_ids"`
}

// CompactResult is COMPACT.APPLY's outcome.
type CompactResult struct {
	Summaries   []SummaryInfo `json:"summaries"`
	Folded      int           `json:"folded"`  // originals folded
	Dropped     int           `json:"dropped"` // originals deleted (Drop=true)
	BytesFolded int64         `json:"bytes_folded"`
}

// Apply executes the plan: write a summary per cluster (recording the folded
// ids in SourceIDs + retaining the originals for EXPAND), then optionally
// delete the originals.
func (c *Compactor) Apply(userID string, opts CompactOptions) CompactResult {
	opts.applyDefaults()
	clusters := c.cluster(userID, opts)
	var res CompactResult
	for _, cl := range clusters {
		ids := make([]string, 0, len(cl.entries))
		copies := make([]Entry, 0, len(cl.entries))
		for _, e := range cl.entries {
			ids = append(ids, e.ID)
			copies = append(copies, copyEntry(e))
		}
		summary, _, err := c.store.AddWithOptions(userID, summarize(cl.entries, opts.Importance), AddOptions{
			Layer:      LayerSemantic,
			Importance: opts.Importance,
			Meta:       map[string]string{"derived": "compact", "source_layer": string(opts.Layer)},
			SourceIDs:  ids,
			// DedupThreshold 0 → always write (a compaction summary must land).
		})
		if err != nil || summary == nil {
			continue
		}
		c.mu.Lock()
		c.folded[summary.ID] = copies
		c.mu.Unlock()
		res.Summaries = append(res.Summaries, SummaryInfo{SummaryID: summary.ID, SourceIDs: ids})
		res.Folded += len(ids)
		res.BytesFolded += cl.bytes
		if opts.Drop {
			for _, id := range ids {
				if c.store.Delete(userID, id) {
					res.Dropped++
				}
			}
		}
	}
	if len(res.Summaries) > 0 {
		c.totalApplied.Add(1)
		c.totalFolded.Add(int64(res.Folded))
	}
	return res
}

// ExpandResult is COMPACT.EXPAND's outcome.
type ExpandResult struct {
	Restored   int      `json:"restored"`
	RestoredIDs []string `json:"restored_ids"`
}

// Expand reverses a compaction: re-add the retained originals and drop the
// summary. Returns ok=false if the summary id isn't a known compaction.
// Restored entries receive fresh ids (the store mints ids on write); their
// content/layer/importance/meta are preserved.
func (c *Compactor) Expand(userID, summaryID string) (ExpandResult, bool) {
	c.mu.Lock()
	copies, ok := c.folded[summaryID]
	if ok {
		delete(c.folded, summaryID)
	}
	c.mu.Unlock()
	if !ok {
		return ExpandResult{}, false
	}
	var res ExpandResult
	for _, e := range copies {
		restored, _, err := c.store.AddWithOptions(userID, e.Text, AddOptions{
			Layer:      e.Layer,
			Importance: e.Importance,
			Meta:       e.Meta,
			SourceIDs:  e.SourceIDs,
		})
		if err == nil && restored != nil {
			res.Restored++
			res.RestoredIDs = append(res.RestoredIDs, restored.ID)
		}
	}
	c.store.Delete(userID, summaryID)
	c.totalExpanded.Add(1)
	return res, true
}

// CompactStats is COMPACT.STATS' snapshot.
type CompactStats struct {
	ReversibleSummaries int   `json:"reversible_summaries"`
	TotalApplied        int64 `json:"total_applied"`
	TotalFolded         int64 `json:"total_folded"`
	TotalExpanded       int64 `json:"total_expanded"`
}

func (c *Compactor) Stats() CompactStats {
	c.mu.Lock()
	n := len(c.folded)
	c.mu.Unlock()
	return CompactStats{
		ReversibleSummaries: n,
		TotalApplied:        c.totalApplied.Load(),
		TotalFolded:         c.totalFolded.Load(),
		TotalExpanded:       c.totalExpanded.Load(),
	}
}

// ─── clustering ──────────────────────────────────────────────────────────

type compactCluster struct {
	entries []*Entry
	bytes   int64
}

// cluster groups the user's source-layer entries (optionally age-filtered) by
// cosine similarity, greedy single-pass, keeping clusters ≥ MinSize. When
// TargetBytes > 0 it stops once that many source bytes are accumulated.
func (c *Compactor) cluster(userID string, opts CompactOptions) []compactCluster {
	all := c.store.ListByLayer(userID, opts.Layer)
	// Age filter.
	now := time.Now()
	rows := make([]*Entry, 0, len(all))
	for _, e := range all {
		if opts.MaxAgeSec > 0 && now.Sub(e.CreatedAt).Seconds() < float64(opts.MaxAgeSec) {
			continue
		}
		rows = append(rows, e)
	}
	if len(rows) < opts.MinSize {
		return nil
	}
	// Stable order so PLAN and APPLY agree.
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })

	embeds := make([][]float32, len(rows))
	for i, e := range rows {
		embeds[i] = vector.Embed(e.Text, 384)
	}
	assigned := make([]bool, len(rows))
	var out []compactCluster
	var accBytes int64
	for i := range rows {
		if assigned[i] {
			continue
		}
		cl := compactCluster{entries: []*Entry{rows[i]}, bytes: entryBytes(rows[i])}
		assigned[i] = true
		for j := i + 1; j < len(rows); j++ {
			if assigned[j] {
				continue
			}
			if float64(vector.Cosine(embeds[i], embeds[j])) >= opts.Threshold {
				assigned[j] = true
				cl.entries = append(cl.entries, rows[j])
				cl.bytes += entryBytes(rows[j])
			}
		}
		if len(cl.entries) < opts.MinSize {
			continue
		}
		out = append(out, cl)
		accBytes += cl.bytes
		if opts.TargetBytes > 0 && accBytes >= opts.TargetBytes {
			break
		}
	}
	return out
}

func summarize(entries []*Entry, _ float64) string {
	ordered := append([]*Entry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Importance != ordered[j].Importance {
			return ordered[i].Importance > ordered[j].Importance
		}
		return ordered[i].CreatedAt.After(ordered[j].CreatedAt)
	})
	bits := make([]string, 0, 5)
	for _, e := range ordered {
		t := strings.TrimSpace(e.Text)
		if t == "" {
			continue
		}
		bits = append(bits, t)
		if len(bits) >= 5 {
			break
		}
	}
	return strings.Join(bits, "; ")
}

// entryBytes estimates one entry's footprint — there's no store accessor, so
// mirror the index's ApproxBytes convention (text + id + meta).
func entryBytes(e *Entry) int64 {
	b := int64(len(e.Text) + len(e.ID))
	for k, v := range e.Meta {
		b += int64(len(k) + len(v))
	}
	return b
}

func copyEntry(e *Entry) Entry {
	cp := *e
	if e.Meta != nil {
		cp.Meta = make(map[string]string, len(e.Meta))
		for k, v := range e.Meta {
			cp.Meta[k] = v
		}
	}
	if e.SourceIDs != nil {
		cp.SourceIDs = append([]string(nil), e.SourceIDs...)
	}
	return cp
}
