package llmstack

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// EmbMigrator implements dual-index shadow migration between
// embedding models. Almost nobody talks about this and it's
// brutal: the day you upgrade from MiniLM to BGE, every cached
// vector and every RAG index becomes incompatible — and you can't
// atomically reindex a live system. EMB.MIGRATE.* lets apps
// write to BOTH models during the migration window, COMPARE them
// on real traffic, then CUTOVER once recall has been verified.
//
// Lifecycle:
//
//   START   →  shadow phase begins; apps WRITE both vectors
//              every time they cache or index a doc.
//   WRITE   →  records (old_vec, new_vec) for one row.
//   STATUS  →  reindexed count / progress / recall delta.
//   COMPARE →  side-by-side top-K for a query under both models.
//              Apps run on a held-out test set to verify recall
//              before cutover.
//   CUTOVER →  flips the "live" indicator to the new model.
//              Apps now read from the new vectors only; the old
//              ones can be dropped at the app's discretion.
//   ABORT   →  drops the migration, keeps old vectors.
//
// Commands:
//
//   EMB.MIGRATE.START migration-id FROM old-model TO new-model
//   EMB.MIGRATE.WRITE migration-id row-id
//        OLD v1,v2,v3,... NEW v1,v2,v3,...
//   EMB.MIGRATE.STATUS migration-id
//   EMB.MIGRATE.COMPARE migration-id
//        OLD v,v,v,... NEW v,v,v,...  K n
//        → [old_topk, new_topk, overlap_at_k]
//   EMB.MIGRATE.CUTOVER migration-id
//   EMB.MIGRATE.ABORT migration-id
//   EMB.MIGRATE.LIST
//   EMB.MIGRATE.STATS
//
// Storage: per-migration {old_vecs, new_vecs} maps. COMPARE is
// O(N) cosines × 2 (one pass per model) — same shape as EMBED.MAT.
type EmbMigrator struct {
	mu         sync.RWMutex
	migrations map[string]*embMigration

	totalStarts   atomic.Int64
	totalWrites   atomic.Int64
	totalCompares atomic.Int64
	totalCutovers atomic.Int64
	totalAborts   atomic.Int64
}

type embMigration struct {
	id        string
	fromModel string
	toModel   string
	startedAt int64
	cutOver   atomic.Bool

	mu      sync.RWMutex
	oldVecs map[string][]float64 // row_id -> normalised old vec
	newVecs map[string][]float64 // row_id -> normalised new vec
	oldDim  int
	newDim  int
}

// NewEmbMigrator returns an empty migrator.
func NewEmbMigrator() *EmbMigrator {
	return &EmbMigrator{migrations: map[string]*embMigration{}}
}

// Start registers a new migration. Replacing an existing id is
// rejected (apps must ABORT first).
func (e *EmbMigrator) Start(migrationID, fromModel, toModel string) error {
	if migrationID == "" {
		return errors.New("migration_id required")
	}
	if fromModel == "" || toModel == "" {
		return errors.New("from_model and to_model required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.migrations[migrationID]; exists {
		return errors.New("migration already exists: " + migrationID)
	}
	e.migrations[migrationID] = &embMigration{
		id:        migrationID,
		fromModel: fromModel,
		toModel:   toModel,
		startedAt: time.Now().UnixNano(),
		oldVecs:   map[string][]float64{},
		newVecs:   map[string][]float64{},
	}
	e.totalStarts.Add(1)
	return nil
}

// Write records both the old-model and new-model vectors for a
// single row. Vectors are L2-normalised on insert so subsequent
// COMPARE / cosine ops reduce to dot products.
func (e *EmbMigrator) Write(migrationID, rowID string, oldVec, newVec []float64) error {
	if rowID == "" {
		return errors.New("row_id required")
	}
	if len(oldVec) == 0 || len(newVec) == 0 {
		return errors.New("oldVec and newVec required")
	}
	e.totalWrites.Add(1)
	e.mu.RLock()
	m, ok := e.migrations[migrationID]
	e.mu.RUnlock()
	if !ok {
		return errors.New("unknown migration_id: " + migrationID)
	}
	oldN := math.Sqrt(dotProduct(oldVec, oldVec))
	newN := math.Sqrt(dotProduct(newVec, newVec))
	if oldN == 0 || newN == 0 {
		return errors.New("zero-norm vector")
	}
	oldNorm := make([]float64, len(oldVec))
	newNorm := make([]float64, len(newVec))
	for i, v := range oldVec {
		oldNorm[i] = v / oldN
	}
	for i, v := range newVec {
		newNorm[i] = v / newN
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.oldDim == 0 {
		m.oldDim = len(oldVec)
	} else if m.oldDim != len(oldVec) {
		return errors.New("old vec dim mismatch")
	}
	if m.newDim == 0 {
		m.newDim = len(newVec)
	} else if m.newDim != len(newVec) {
		return errors.New("new vec dim mismatch")
	}
	m.oldVecs[rowID] = oldNorm
	m.newVecs[rowID] = newNorm
	return nil
}

// MigrateStatusResult is STATUS's return.
type MigrateStatusResult struct {
	MigrationID  string `json:"migration_id"`
	FromModel    string `json:"from_model"`
	ToModel      string `json:"to_model"`
	StartedAt    int64  `json:"started_at_unix"`
	RowsWritten  int    `json:"rows_written"`
	OldDim       int    `json:"old_dim"`
	NewDim       int    `json:"new_dim"`
	CutOver      bool   `json:"cut_over"`
}

// Status returns the migration state.
func (e *EmbMigrator) Status(migrationID string) (MigrateStatusResult, bool) {
	e.mu.RLock()
	m, ok := e.migrations[migrationID]
	e.mu.RUnlock()
	if !ok {
		return MigrateStatusResult{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MigrateStatusResult{
		MigrationID: migrationID,
		FromModel:   m.fromModel,
		ToModel:     m.toModel,
		StartedAt:   m.startedAt / int64(time.Second),
		RowsWritten: len(m.oldVecs),
		OldDim:      m.oldDim,
		NewDim:      m.newDim,
		CutOver:     m.cutOver.Load(),
	}, true
}

// CompareResult is COMPARE's return.
type MigrateCompareResult struct {
	OldTopK     []TopKHit `json:"old_topk"`
	NewTopK     []TopKHit `json:"new_topk"`
	OverlapAtK  int       `json:"overlap_at_k"`
	JaccardAtK  float64   `json:"jaccard_at_k"`
}

// Compare runs the same query under both models and reports how
// the top-K lists overlap. Apps run on a held-out test set to
// decide whether the new model is ready for cutover.
//
// overlap_at_k = count of row_ids appearing in both top-K lists
// jaccard_at_k = |intersect| / |union|
func (e *EmbMigrator) Compare(migrationID string, oldQuery, newQuery []float64, k int) (MigrateCompareResult, bool) {
	e.totalCompares.Add(1)
	if k <= 0 {
		k = 10
	}
	e.mu.RLock()
	m, ok := e.migrations[migrationID]
	e.mu.RUnlock()
	if !ok {
		return MigrateCompareResult{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.oldDim == 0 || m.newDim == 0 ||
		len(oldQuery) != m.oldDim || len(newQuery) != m.newDim {
		return MigrateCompareResult{}, false
	}
	oldQN := math.Sqrt(dotProduct(oldQuery, oldQuery))
	newQN := math.Sqrt(dotProduct(newQuery, newQuery))
	if oldQN == 0 || newQN == 0 {
		return MigrateCompareResult{}, false
	}
	oldQ := make([]float64, len(oldQuery))
	newQ := make([]float64, len(newQuery))
	for i, v := range oldQuery {
		oldQ[i] = v / oldQN
	}
	for i, v := range newQuery {
		newQ[i] = v / newQN
	}
	oldHits := make([]TopKHit, 0, len(m.oldVecs))
	newHits := make([]TopKHit, 0, len(m.newVecs))
	for id, vec := range m.oldVecs {
		oldHits = append(oldHits, TopKHit{RowID: id, Score: dotProduct(oldQ, vec)})
	}
	for id, vec := range m.newVecs {
		newHits = append(newHits, TopKHit{RowID: id, Score: dotProduct(newQ, vec)})
	}
	sort.Slice(oldHits, func(i, j int) bool { return oldHits[i].Score > oldHits[j].Score })
	sort.Slice(newHits, func(i, j int) bool { return newHits[i].Score > newHits[j].Score })
	if len(oldHits) > k {
		oldHits = oldHits[:k]
	}
	if len(newHits) > k {
		newHits = newHits[:k]
	}

	oldSet := map[string]bool{}
	for _, h := range oldHits {
		oldSet[h.RowID] = true
	}
	overlap := 0
	for _, h := range newHits {
		if oldSet[h.RowID] {
			overlap++
		}
	}
	union := len(oldHits) + len(newHits) - overlap
	jaccard := 0.0
	if union > 0 {
		jaccard = float64(overlap) / float64(union)
	}
	return MigrateCompareResult{
		OldTopK:    oldHits,
		NewTopK:    newHits,
		OverlapAtK: overlap,
		JaccardAtK: jaccard,
	}, true
}

// Cutover flips the migration to "new model is live." Idempotent.
func (e *EmbMigrator) Cutover(migrationID string) bool {
	e.mu.RLock()
	m, ok := e.migrations[migrationID]
	e.mu.RUnlock()
	if !ok {
		return false
	}
	if m.cutOver.Swap(true) {
		return true // already cut over
	}
	e.totalCutovers.Add(1)
	return true
}

// Abort drops the migration entirely.
func (e *EmbMigrator) Abort(migrationID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.migrations[migrationID]
	delete(e.migrations, migrationID)
	if ok {
		e.totalAborts.Add(1)
	}
	return ok
}

// List returns every active migration.
type MigrateListRow struct {
	MigrationID string `json:"migration_id"`
	FromModel   string `json:"from_model"`
	ToModel     string `json:"to_model"`
	Rows        int    `json:"rows"`
	CutOver     bool   `json:"cut_over"`
	StartedAt   int64  `json:"started_at_unix"`
}

func (e *EmbMigrator) List() []MigrateListRow {
	e.mu.RLock()
	out := make([]MigrateListRow, 0, len(e.migrations))
	for id, m := range e.migrations {
		m.mu.RLock()
		out = append(out, MigrateListRow{
			MigrationID: id,
			FromModel:   m.fromModel,
			ToModel:     m.toModel,
			Rows:        len(m.oldVecs),
			CutOver:     m.cutOver.Load(),
			StartedAt:   m.startedAt / int64(time.Second),
		})
		m.mu.RUnlock()
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].MigrationID < out[j].MigrationID })
	return out
}

// EmbMigrateStats is the global snapshot.
type EmbMigrateStats struct {
	Active        int   `json:"active"`
	TotalStarts   int64 `json:"total_starts"`
	TotalWrites   int64 `json:"total_writes"`
	TotalCompares int64 `json:"total_compares"`
	TotalCutovers int64 `json:"total_cutovers"`
	TotalAborts   int64 `json:"total_aborts"`
}

func (e *EmbMigrator) Stats() EmbMigrateStats {
	e.mu.RLock()
	n := len(e.migrations)
	e.mu.RUnlock()
	return EmbMigrateStats{
		Active:        n,
		TotalStarts:   e.totalStarts.Load(),
		TotalWrites:   e.totalWrites.Load(),
		TotalCompares: e.totalCompares.Load(),
		TotalCutovers: e.totalCutovers.Load(),
		TotalAborts:   e.totalAborts.Load(),
	}
}
