package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DocFreshTracker is FACT.STALE for the corpus, not the cache.
//
// FACT.STALE marks cached answers stale when the underlying world
// changes. DOC.FRESH.* is the corpus equivalent: an indexed RAG
// doc has an upstream source (a CMS article, an HTML page, a JIRA
// ticket). When the source changes, every retrieval that returns
// the stale indexed copy returns wrong context.
//
// The standard fix is a reindex pipeline that runs nightly — leaves
// up to 24h of staleness. DOC.FRESH.* gives apps a lightweight
// per-doc freshness layer so retrieval can:
//
//   - down-rank a known-stale chunk on the fly,
//   - flag the answer with a "freshness warning" note,
//   - or skip the chunk entirely and fall back to LLM-from-memory.
//
// Three signals trigger stale:
//   hash change   — the registered hash differs from the new one
//                   on re-STAMP. Caller supplies the new hash.
//   TTL expiry    — registered TTL elapsed since last STAMP.
//   explicit       — INVALIDATE for the "I just received a webhook
//                   that the source changed" case.
//
// Commands:
//
//   DOC.FRESH.REGISTER doc-id source-url [HASH h] [TTL seconds]
//        Idempotent — re-registering same doc-id updates source/ttl.
//   DOC.FRESH.STAMP doc-id [HASH h]
//        Re-stamp after indexing. If HASH differs from registered,
//        the doc is marked changed (status becomes "stale" until the
//        caller updates registered hash via REGISTER again).
//   DOC.FRESH.CHECK doc-id
//        → {status, age_seconds, hash, source, registered_hash}
//        status: fresh | stale | expired | missing
//   DOC.FRESH.INVALIDATE doc-id [REASON r]
//   DOC.FRESH.BULKCHECK doc-id1 doc-id2 ...
//   DOC.FRESH.STALE [LIMIT n]
//        → known-stale doc ids (newest stale first).
//   DOC.FRESH.LIST
//   DOC.FRESH.DROP doc-id|ALL
//   DOC.FRESH.STATS
//
// Hot path: CHECK is one map lookup + a TTL comparison. Sub-microsecond.
type DocFreshTracker struct {
	mu   sync.RWMutex
	docs map[string]*docFreshEntry

	totalRegisters   atomic.Int64
	totalStamps      atomic.Int64
	totalChecks      atomic.Int64
	totalInvalidates atomic.Int64
}

type docFreshEntry struct {
	mu             sync.RWMutex
	source         string
	registeredHash string
	lastHash       string
	ttl            time.Duration
	stampedAt      int64
	invalidatedAt  int64
	invalidReason  string
	staleSince     int64 // unix-nano; 0 if currently fresh
}

// NewDocFreshTracker returns an empty tracker.
func NewDocFreshTracker() *DocFreshTracker {
	return &DocFreshTracker{docs: map[string]*docFreshEntry{}}
}

// Register creates or updates a doc registration. ttl=0 means no
// TTL gate (only hash + INVALIDATE control freshness).
func (d *DocFreshTracker) Register(docID, source, hash string, ttl time.Duration) error {
	if docID == "" {
		return errors.New("doc_id required")
	}
	if source == "" {
		return errors.New("source required")
	}
	if ttl < 0 {
		return errors.New("ttl must be non-negative")
	}
	d.totalRegisters.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.docs[docID]
	if !ok {
		e = &docFreshEntry{}
		d.docs[docID] = e
	}
	e.mu.Lock()
	e.source = source
	e.registeredHash = hash
	if e.lastHash == "" {
		e.lastHash = hash
	}
	e.ttl = ttl
	e.stampedAt = time.Now().UnixNano()
	e.invalidatedAt = 0
	e.invalidReason = ""
	e.staleSince = 0
	e.mu.Unlock()
	return nil
}

// Stamp records a re-index. If the new hash differs from the
// registered hash, the doc flips to stale until the next REGISTER
// (which is how the caller signals "I have re-indexed and the new
// hash is now the truth").
func (d *DocFreshTracker) Stamp(docID, newHash string) error {
	if docID == "" {
		return errors.New("doc_id required")
	}
	d.totalStamps.Add(1)
	d.mu.RLock()
	e, ok := d.docs[docID]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown doc_id (call DOC.FRESH.REGISTER first): " + docID)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastHash = newHash
	e.stampedAt = time.Now().UnixNano()
	if newHash != "" && e.registeredHash != "" && newHash != e.registeredHash {
		e.staleSince = time.Now().UnixNano()
	} else if newHash == e.registeredHash {
		// Re-stamp with the same hash clears any TTL expiry.
		e.staleSince = 0
		e.invalidatedAt = 0
		e.invalidReason = ""
	}
	return nil
}

// DocFreshResult is CHECK's return.
type DocFreshResult struct {
	DocID          string `json:"doc_id"`
	Status         string `json:"status"` // fresh | stale | expired | missing
	AgeSeconds     int64  `json:"age_seconds"`
	Hash           string `json:"hash,omitempty"`
	RegisteredHash string `json:"registered_hash,omitempty"`
	Source         string `json:"source,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Check returns the per-doc freshness status.
func (d *DocFreshTracker) Check(docID string) DocFreshResult {
	d.totalChecks.Add(1)
	d.mu.RLock()
	e, ok := d.docs[docID]
	d.mu.RUnlock()
	if !ok {
		return DocFreshResult{DocID: docID, Status: "missing"}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	now := time.Now().UnixNano()
	out := DocFreshResult{
		DocID: docID, Source: e.source,
		Hash: e.lastHash, RegisteredHash: e.registeredHash,
		AgeSeconds: (now - e.stampedAt) / int64(time.Second),
	}
	switch {
	case e.invalidatedAt > 0:
		out.Status = "stale"
		out.Reason = "explicitly invalidated"
		if e.invalidReason != "" {
			out.Reason += ": " + e.invalidReason
		}
	case e.staleSince > 0:
		out.Status = "stale"
		out.Reason = "hash mismatch with registered"
	case e.ttl > 0 && (now-e.stampedAt) > e.ttl.Nanoseconds():
		out.Status = "expired"
		out.Reason = "exceeded TTL"
	default:
		out.Status = "fresh"
	}
	return out
}

// BulkCheck is the multi-doc variant.
func (d *DocFreshTracker) BulkCheck(docIDs []string) []DocFreshResult {
	out := make([]DocFreshResult, len(docIDs))
	for i, id := range docIDs {
		out[i] = d.Check(id)
	}
	return out
}

// Invalidate flips a doc to stale without changing the source. Used
// when a webhook says "upstream changed" and the caller wants the
// next CHECK to surface stale before re-indexing.
func (d *DocFreshTracker) Invalidate(docID, reason string) error {
	if docID == "" {
		return errors.New("doc_id required")
	}
	d.totalInvalidates.Add(1)
	d.mu.RLock()
	e, ok := d.docs[docID]
	d.mu.RUnlock()
	if !ok {
		return errors.New("unknown doc_id: " + docID)
	}
	e.mu.Lock()
	e.invalidatedAt = time.Now().UnixNano()
	e.invalidReason = reason
	e.mu.Unlock()
	return nil
}

// StaleRow is one row of STALE output.
type StaleRow struct {
	DocID       string `json:"doc_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	StaleSince  int64  `json:"stale_since_unix"`
}

// Stale returns the known-stale doc ids, newest stale first.
func (d *DocFreshTracker) Stale(limit int) []StaleRow {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// Track each row's nanosecond stale-timestamp separately for sort
	// ordering — the StaleSince field is exposed as seconds for the
	// dashboard, but rapid INVALIDATEs would collide on that resolution.
	type pair struct {
		row    StaleRow
		nanoTS int64
	}
	pairs := make([]pair, 0, len(d.docs))
	now := time.Now().UnixNano()
	for id, e := range d.docs {
		e.mu.RLock()
		stale := false
		row := StaleRow{DocID: id}
		var ns int64
		switch {
		case e.invalidatedAt > 0:
			stale = true
			row.Status = "stale"
			row.Reason = "invalidated"
			ns = e.invalidatedAt
		case e.staleSince > 0:
			stale = true
			row.Status = "stale"
			row.Reason = "hash mismatch"
			ns = e.staleSince
		case e.ttl > 0 && (now-e.stampedAt) > e.ttl.Nanoseconds():
			stale = true
			row.Status = "expired"
			row.Reason = "ttl exceeded"
			ns = e.stampedAt + e.ttl.Nanoseconds()
		}
		e.mu.RUnlock()
		if stale {
			row.StaleSince = ns / int64(time.Second)
			pairs = append(pairs, pair{row: row, nanoTS: ns})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].nanoTS > pairs[j].nanoTS })
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}
	out := make([]StaleRow, len(pairs))
	for i, p := range pairs {
		out[i] = p.row
	}
	return out
}

// List returns every doc id, sorted.
func (d *DocFreshTracker) List() []string {
	d.mu.RLock()
	out := make([]string, 0, len(d.docs))
	for k := range d.docs {
		out = append(out, k)
	}
	d.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Drop removes a doc. docID="ALL" wipes all.
func (d *DocFreshTracker) Drop(docID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if docID == "ALL" {
		n := len(d.docs)
		d.docs = map[string]*docFreshEntry{}
		return n
	}
	if _, ok := d.docs[docID]; ok {
		delete(d.docs, docID)
		return 1
	}
	return 0
}

// DocFreshStats is the global snapshot.
type DocFreshStats struct {
	Docs             int   `json:"docs"`
	StaleDocs        int   `json:"stale_docs"`
	TotalRegisters   int64 `json:"total_registers"`
	TotalStamps      int64 `json:"total_stamps"`
	TotalChecks      int64 `json:"total_checks"`
	TotalInvalidates int64 `json:"total_invalidates"`
}

func (d *DocFreshTracker) Stats() DocFreshStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	stale := 0
	now := time.Now().UnixNano()
	for _, e := range d.docs {
		e.mu.RLock()
		if e.invalidatedAt > 0 || e.staleSince > 0 ||
			(e.ttl > 0 && (now-e.stampedAt) > e.ttl.Nanoseconds()) {
			stale++
		}
		e.mu.RUnlock()
	}
	return DocFreshStats{
		Docs:             len(d.docs),
		StaleDocs:        stale,
		TotalRegisters:   d.totalRegisters.Load(),
		TotalStamps:      d.totalStamps.Load(),
		TotalChecks:      d.totalChecks.Load(),
		TotalInvalidates: d.totalInvalidates.Load(),
	}
}
