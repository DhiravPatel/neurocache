package engine

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dhiravpatel/neurocache/apps/api/internal/rembed"
	"github.com/dhiravpatel/neurocache/apps/api/internal/retrieval"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// registerRembedTargets wires the engine's vector-bearing subsystems into the
// REMBED orchestrator. Called once at boot. Semantic / LLM / Memory share the
// vector.Index type and get full dual-read; Retrieve's dense arm is a
// vectorindex.Index (no text) so it rebuilds + swaps without query-time
// dual-read — the BM25 arm keeps serving exact terms during the migration.
func (e *Engine) registerRembedTargets() {
	if e.Rembed == nil {
		return
	}
	if e.Semantic != nil {
		e.Rembed.RegisterTarget(&vectorIndexTarget{name: "semantic", ix: e.Semantic.Index()})
	}
	if e.LLM != nil {
		e.Rembed.RegisterTarget(&vectorIndexTarget{name: "llm", ix: e.LLM.Index()})
	}
	if e.Memory != nil {
		e.Rembed.RegisterTarget(&vectorIndexTarget{name: "memory", ix: e.Memory.Index()})
	}
	if e.Retrieval != nil {
		e.Rembed.RegisterTarget(&retrievalTarget{mgr: e.Retrieval, defDim: e.Cfg.EmbeddingDim})
	}
}

// vectorIndexTarget adapts a *vector.Index (semantic / llm / memory) to
// rembed.Target. The shadow/dual-read primitives live on vector.Index, so all
// three subsystems share one implementation.
type vectorIndexTarget struct {
	name string
	ix   *vector.Index
}

func (t *vectorIndexTarget) Name() string           { return t.name }
func (t *vectorIndexTarget) Count() int             { return t.ix.Size() }
func (t *vectorIndexTarget) Bytes() int64           { return t.ix.ApproxBytes() }
func (t *vectorIndexTarget) Dim() int               { return t.ix.Dim() }
func (t *vectorIndexTarget) SupportsDualRead() bool { return true }

func (t *vectorIndexTarget) Stage(dim, batch int, dualRead bool, progress func(done, total int), cancel <-chan struct{}) error {
	if dim <= 0 {
		return errors.New("rembed dim must be positive")
	}
	if batch <= 0 {
		batch = 512
	}
	snap := t.ix.Snapshot()
	total := len(snap)
	shadow := vector.NewIndex(dim)
	for i, ent := range snap {
		select {
		case <-cancel:
			return errors.New("rembed cancelled")
		default:
		}
		shadow.Upsert(ent.ID, ent.Text, ent.Meta)
		if progress != nil && ((i+1)%batch == 0 || i+1 == total) {
			progress(i+1, total)
		}
	}
	if progress != nil && total == 0 {
		progress(0, 0)
	}
	t.ix.AttachShadow(shadow)
	if dualRead {
		t.ix.SetDualRead(true)
	}
	return nil
}

func (t *vectorIndexTarget) Staged() bool {
	present, _, _, _ := t.ix.ShadowInfo()
	return present
}

func (t *vectorIndexTarget) Swap() error {
	if !t.ix.SwapShadow() {
		return errors.New("no staged shadow to swap")
	}
	return nil
}

func (t *vectorIndexTarget) Rollback() error {
	t.ix.DropShadow()
	return nil
}

// retrievalTarget adapts the whole retrieval.Manager — every hybrid index's
// dense arm is rebuilt at the new dimension.
type retrievalTarget struct {
	mgr    *retrieval.Manager
	defDim int
}

func (t *retrievalTarget) Name() string           { return "retrieve" }
func (t *retrievalTarget) Dim() int               { return t.defDim }
func (t *retrievalTarget) SupportsDualRead() bool { return false }

func (t *retrievalTarget) Count() int {
	n := 0
	for _, name := range t.mgr.Names() {
		if ix, ok := t.mgr.Get(name); ok {
			c, _ := ix.RembedStat()
			n += c
		}
	}
	return n
}

func (t *retrievalTarget) Bytes() int64 {
	var b int64
	for _, name := range t.mgr.Names() {
		if ix, ok := t.mgr.Get(name); ok {
			c, d := ix.RembedStat()
			b += int64(c) * int64(d) * 4
		}
	}
	return b
}

func (t *retrievalTarget) Stage(dim, batch int, _ bool, progress func(done, total int), cancel <-chan struct{}) error {
	names := t.mgr.Names()
	total := 0
	for _, name := range names {
		if ix, ok := t.mgr.Get(name); ok {
			c, _ := ix.RembedStat()
			total += c
		}
	}
	base := 0
	for _, name := range names {
		ix, ok := t.mgr.Get(name)
		if !ok {
			continue
		}
		offset := base
		err := ix.StageRembed(dim, batch, func(done, _ int) {
			if progress != nil {
				progress(offset+done, total)
			}
		}, cancel)
		if err != nil {
			// Abort: discard any shadows already staged across the manager.
			for _, n2 := range names {
				if ix2, ok := t.mgr.Get(n2); ok {
					ix2.RollbackRembed()
				}
			}
			return err
		}
		c, _ := ix.RembedStat()
		base += c
	}
	if progress != nil && total == 0 {
		progress(0, 0)
	}
	return nil
}

func (t *retrievalTarget) Staged() bool {
	names := t.mgr.Names()
	if len(names) == 0 {
		return true // nothing to swap is vacuously ready
	}
	for _, name := range names {
		if ix, ok := t.mgr.Get(name); ok {
			if staged, _ := ix.RembedStaged(); staged {
				return true
			}
		}
	}
	return false
}

func (t *retrievalTarget) Swap() error {
	names := t.mgr.Names()
	if len(names) == 0 {
		return nil // nothing to swap
	}
	swapped := false
	for _, name := range names {
		if ix, ok := t.mgr.Get(name); ok {
			if ix.SwapRembed() {
				swapped = true
			}
		}
	}
	if !swapped {
		return errors.New("no staged dense index to swap")
	}
	return nil
}

func (t *retrievalTarget) Rollback() error {
	for _, name := range t.mgr.Names() {
		if ix, ok := t.mgr.Get(name); ok {
			ix.RollbackRembed()
		}
	}
	return nil
}

// registerRembedResolver lets REMBED address a single VADD key as
// "vector:<key>", resolved on demand to a vaddTarget. This is what enables
// REMBED.EXTERN on a client-vector store.
func (e *Engine) registerRembedResolver() {
	if e.Rembed == nil {
		return
	}
	e.Rembed.SetResolver(func(scope string) (rembed.Target, error) {
		if !strings.HasPrefix(scope, "vector:") {
			return nil, nil // not handled here
		}
		key := strings.TrimPrefix(scope, "vector:")
		if key == "" {
			return nil, errors.New("vector scope needs a key: vector:<key>")
		}
		if _, ok, err := e.KV.VInfo(key); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("no vector set at key %q", key)
		}
		return &vaddTarget{store: e.KV, key: key}, nil
	})
}

// vaddTarget adapts one VADD key (a store.VectorSet) to rembed's ExternTarget.
// VADD keys store client-supplied raw vectors with no source text, so they
// can't be re-embedded internally — only via the bring-your-own-re-embedder
// (EXTERN) flow. The shadow lives here until SWAP atomically replaces the
// key's live index.
type vaddTarget struct {
	store *store.Store
	key   string

	mu        sync.Mutex
	shadow    *vectorindex.Index
	shadowDim int
	attrs     map[string]string // id → original attr, carried into the shadow on ingest
}

func (t *vaddTarget) Name() string           { return "vector:" + t.key }
func (t *vaddTarget) SupportsDualRead() bool { return false }

func (t *vaddTarget) Count() int {
	n, _ := t.store.VCard(t.key)
	return n
}

func (t *vaddTarget) Bytes() int64 {
	n, _ := t.store.VCard(t.key)
	d, _, _ := t.store.VDim(t.key)
	return int64(n) * int64(d) * 4
}

func (t *vaddTarget) Dim() int {
	d, _, _ := t.store.VDim(t.key)
	return d
}

// Stage (embed mode) is unsupported — VADD has no source text.
func (t *vaddTarget) Stage(dim, batch int, dualRead bool, progress func(done, total int), cancel <-chan struct{}) error {
	return errors.New("vector sets require MODE extern (no source text to re-embed internally)")
}

func (t *vaddTarget) Staged() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.shadow != nil
}

func (t *vaddTarget) Swap() error {
	t.mu.Lock()
	sh := t.shadow
	t.mu.Unlock()
	if sh == nil {
		return errors.New("no staged index to swap")
	}
	ok, err := t.store.VReplaceIndex(t.key, sh)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("vector key vanished before swap")
	}
	t.mu.Lock()
	t.shadow, t.shadowDim, t.attrs = nil, 0, nil
	t.mu.Unlock()
	return nil
}

func (t *vaddTarget) Rollback() error {
	t.mu.Lock()
	t.shadow, t.shadowDim, t.attrs = nil, 0, nil
	t.mu.Unlock()
	return nil
}

func (t *vaddTarget) ExportEntries() ([]rembed.ExternEntry, error) {
	exp, _, ok, err := t.store.VExport(t.key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("vector key not found")
	}
	out := make([]rembed.ExternEntry, 0, len(exp))
	for _, e := range exp {
		out = append(out, rembed.ExternEntry{ID: e.ID, Vec: e.Vec, Attr: e.Attr})
	}
	return out, nil
}

func (t *vaddTarget) StageExtern(dim int) error {
	exp, opts, ok, err := t.store.VExport(t.key)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("vector key not found")
	}
	idx, err := vectorindex.New(vectorindex.Options{
		Algo: opts.Algo, Metric: opts.Metric, Dim: dim,
		M: opts.M, EFC: opts.EFC, EFR: opts.EFR,
	})
	if err != nil {
		return err
	}
	attrs := make(map[string]string, len(exp))
	for _, e := range exp {
		if e.Attr != "" {
			attrs[e.ID] = e.Attr
		}
	}
	t.mu.Lock()
	t.shadow, t.shadowDim, t.attrs = idx, dim, attrs
	t.mu.Unlock()
	return nil
}

func (t *vaddTarget) IngestVector(id string, vec []float32) error {
	t.mu.Lock()
	sh := t.shadow
	attr := t.attrs[id]
	t.mu.Unlock()
	if sh == nil {
		return errors.New("no staged index; STAGE first")
	}
	if err := sh.Set(id, vec); err != nil { // rejects a dimension mismatch
		return err
	}
	if attr != "" {
		sh.SetAttr(id, attr)
	}
	return nil
}
