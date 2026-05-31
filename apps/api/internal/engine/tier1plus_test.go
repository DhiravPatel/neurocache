package engine

import (
	"testing"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
	"github.com/dhiravpatel/neurocache/apps/api/internal/rembed"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// ─── XTXN participants ───────────────────────────────────────────────────

// TestQuotaParticipant2PC — Prepare peeks (no consume), Commit charges, Abort
// discards, and an over-budget Prepare aborts the whole txn.
func TestQuotaParticipant2PC(t *testing.T) {
	e := quotaEngine()
	e.Quota = aiops.NewQuotaManager()
	if err := e.CostBudgets.SetBudget("u", 1.0, 60000); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Quota.Define("p", []string{"cost"}, "all"); err != nil {
		t.Fatal(err)
	}
	qp := newQuotaParticipant(e)
	args := map[string]string{"policy": "p", "cost_scope": "u", "cost_usd": "0.5"}

	tok, err := qp.Prepare("admit", args)
	if err != nil {
		t.Fatal(err)
	}
	if used, _, _, _ := e.CostBudgets.Usage("u"); used != 0 {
		t.Fatalf("Prepare consumed budget: %v", used)
	}
	if err := qp.Commit(tok); err != nil {
		t.Fatal(err)
	}
	if used, _, _, _ := e.CostBudgets.Usage("u"); used != 0.5 {
		t.Fatalf("Commit charge = %v, want 0.5", used)
	}

	// Abort discards a prepared op without charging.
	tok2, err := qp.Prepare("admit", map[string]string{"policy": "p", "cost_scope": "u", "cost_usd": "0.3"})
	if err != nil {
		t.Fatal(err)
	}
	qp.Abort(tok2)
	if used, _, _, _ := e.CostBudgets.Usage("u"); used != 0.5 {
		t.Fatalf("Abort charged: %v", used)
	}

	// Over-budget Prepare aborts the txn (error, no token).
	if _, err := qp.Prepare("admit", map[string]string{"policy": "p", "cost_scope": "u", "cost_usd": "1.0"}); err == nil {
		t.Fatal("over-budget Prepare should error to abort the txn")
	}
	// Unknown policy aborts too.
	if _, err := qp.Prepare("admit", map[string]string{"policy": "ghost", "cost_scope": "u", "cost_usd": "0.1"}); err == nil {
		t.Fatal("unknown policy Prepare should error")
	}
}

// TestRiskParticipant2PC — a grounded answer prepares + commits (debits risk);
// an ungrounded answer aborts the txn.
func TestRiskParticipant2PC(t *testing.T) {
	e := &Engine{
		GroundVerify: llmstack.NewGroundVerifier(384),
		RiskBudgets:  llmstack.NewRiskBudgets(),
		Cfg:          config.Config{GroundMinSupport: 0.5},
	}
	if err := e.RiskBudgets.Set("s", 5.0, 1.0); err != nil {
		t.Fatal(err)
	}
	rp := newRiskParticipant(e)

	tok, err := rp.Prepare("require", map[string]string{
		"answer": "The capital of France is Paris.", "context": "The capital of France is Paris.",
		"session": "s", "min_support": "0.9",
	})
	if err != nil {
		t.Fatalf("grounded Prepare failed: %v", err)
	}
	if err := rp.Commit(tok); err != nil {
		t.Fatal(err)
	}

	// Ungrounded answer aborts the txn.
	if _, err := rp.Prepare("require", map[string]string{
		"answer": "Bananas orbit Jupiter.", "context": "The capital of France is Paris.",
		"session": "s", "min_support": "0.9",
	}); err == nil {
		t.Fatal("ungrounded Prepare should error to abort the txn")
	}
}

// ─── REMBED.EXTERN for VADD ──────────────────────────────────────────────

// TestEngineRembedExternVADD drives a full bring-your-own-re-embedder
// migration on a VADD key: export → ingest new-dim vectors → finalize → swap,
// and confirms the key's index dimension changed and VSIM still works.
func TestEngineRembedExternVADD(t *testing.T) {
	e := &Engine{
		KV:     store.New(),
		Rembed: rembed.New(),
		Cfg:    config.Config{EmbeddingDim: 8},
	}
	e.registerRembedResolver()

	if _, err := e.KV.VAdd("vk", "a", []float32{1, 0, 0, 0}, vectorindex.Options{Dim: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.KV.VAdd("vk", "b", []float32{0, 1, 0, 0}, vectorindex.Options{Dim: 4}); err != nil {
		t.Fatal(err)
	}

	id, err := e.Rembed.StartExtern("vector:vk", 6)
	if err != nil {
		t.Fatal(err)
	}
	entries, _, err := e.Rembed.Export(id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("export returned %d entries, want 2", len(entries))
	}

	// A wrong-dimension vector is rejected.
	if _, _, err := e.Rembed.Ingest(id, []rembed.ExternEntry{{ID: "a", Vec: []float32{1, 0, 0, 0}}}); err == nil {
		t.Fatal("ingesting a 4-dim vector into a 6-dim shadow should error")
	}

	done, total, err := e.Rembed.Ingest(id, []rembed.ExternEntry{
		{ID: "a", Vec: []float32{1, 0, 0, 0, 0, 0}},
		{ID: "b", Vec: []float32{0, 1, 0, 0, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if done != 2 || total != 2 {
		t.Fatalf("ingest = %d/%d, want 2/2", done, total)
	}

	if _, _, err := e.Rembed.Finalize(id); err != nil {
		t.Fatal(err)
	}
	if err := e.Rembed.Swap(id); err != nil {
		t.Fatal(err)
	}

	if d, _, _ := e.KV.VDim("vk"); d != 6 {
		t.Fatalf("post-swap VADD dim = %d, want 6", d)
	}
	res, err := e.KV.VSim("vk", []float32{1, 0, 0, 0, 0, 0}, 1)
	if err != nil || len(res) == 0 || res[0].ID != "a" {
		t.Fatalf("post-swap VSIM = %v (err %v), want nearest 'a'", res, err)
	}

	// Reservation released → a fresh migration on the same key is allowed.
	if _, err := e.Rembed.StartExtern("vector:vk", 4); err != nil {
		t.Fatalf("StartExtern after swap rejected (reservation leaked): %v", err)
	}
}

// TestRembedExternRejectsEmbedTargets — MODE extern only works for targets
// that actually support it (a vector.Index subsystem does not).
func TestRembedExternRejectsEmbedTargets(t *testing.T) {
	r := rembed.New()
	r.RegisterTarget(&fakeEmbedOnly{})
	if _, err := r.StartExtern("semantic", 16); err == nil {
		t.Fatal("StartExtern on an embed-only target should error")
	}
}

type fakeEmbedOnly struct{}

func (fakeEmbedOnly) Name() string           { return "semantic" }
func (fakeEmbedOnly) Count() int             { return 0 }
func (fakeEmbedOnly) Bytes() int64           { return 0 }
func (fakeEmbedOnly) Dim() int               { return 8 }
func (fakeEmbedOnly) SupportsDualRead() bool { return true }
func (fakeEmbedOnly) Staged() bool           { return false }
func (fakeEmbedOnly) Swap() error            { return nil }
func (fakeEmbedOnly) Rollback() error        { return nil }
func (fakeEmbedOnly) Stage(dim, batch int, dr bool, progress func(d, t int), cancel <-chan struct{}) error {
	return nil
}
