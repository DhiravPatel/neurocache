package engine

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
	"github.com/dhiravpatel/neurocache/apps/api/internal/primitives"
	"github.com/dhiravpatel/neurocache/apps/api/internal/rembed"
	"github.com/dhiravpatel/neurocache/apps/api/internal/semcache"
)

// minimal builds an engine with just the gate subsystems QuotaEvaluate needs —
// no New(), so no background goroutines.
func quotaEngine() *Engine {
	return &Engine{
		CostBudgets: aiops.NewCostBudgets(),
		Carbon:      llmstack.NewCarbonLedger(),
		RiskBudgets: llmstack.NewRiskBudgets(),
		RateLimit:   primitives.NewRateLimiter(),
		Market:      llmstack.NewMarket(),
	}
}

// TestQuotaEvaluateTwoPhase is the crux: SIMULATE consumes nothing, ADMIT
// consumes only when admitted, and a request that fails one gate must not
// consume the others (peek-all → commit-all).
func TestQuotaEvaluateTwoPhase(t *testing.T) {
	e := quotaEngine()
	if err := e.CostBudgets.SetBudget("u1", 1.0, 60000); err != nil { // $1.00 / 60s
		t.Fatal(err)
	}

	pol := aiops.QuotaPolicy{
		Name:  "p",
		Gates: []string{aiops.GateCost, aiops.GateRate},
		Mode:  aiops.QuotaModeAll,
	}
	dims := aiops.QuotaDims{
		HasCost: true, CostScope: "u1", CostUSD: 0.5,
		HasRate: true, RateKey: "k", RateWindowMs: 1000, RateMax: 2, RateCost: 1,
	}

	// SIMULATE: admitted, nothing consumed.
	dec, err := e.QuotaEvaluate(pol, dims, false)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Admitted || dec.Committed {
		t.Fatalf("simulate: admitted=%v committed=%v", dec.Admitted, dec.Committed)
	}
	if used, _, _, _ := e.CostBudgets.Usage("u1"); used != 0 {
		t.Fatalf("simulate charged cost: %v", used)
	}

	// ADMIT: admitted + committed, cost charged once.
	dec, err = e.QuotaEvaluate(pol, dims, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Admitted || !dec.Committed {
		t.Fatalf("admit not committed: %+v", dec)
	}
	if used, _, _, _ := e.CostBudgets.Usage("u1"); used != 0.5 {
		t.Fatalf("admit cost=%v, want 0.5", used)
	}

	// Two-phase safety: 0.5 + 0.6 > 1.0 → cost gate fails → NOTHING consumed.
	over := dims
	over.CostUSD = 0.6
	dec, err = e.QuotaEvaluate(pol, over, true)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Admitted || dec.Committed {
		t.Fatalf("over-budget request admitted: %+v", dec)
	}
	if len(dec.DeniedBy) == 0 || dec.DeniedBy[0] != "cost" {
		t.Fatalf("denied_by = %v, want [cost]", dec.DeniedBy)
	}
	if used, _, _, _ := e.CostBudgets.Usage("u1"); used != 0.5 {
		t.Fatalf("failed admit charged cost: %v (want 0.5 unchanged)", used)
	}
	// The rate gate must still have its second slot — only ADMIT#1 spent one.
	if ok, _, _, _ := e.RateLimit.Peek("k", time.Second, 2, 1); !ok {
		t.Fatal("rate gate consumed on a denied admit — two-phase violated")
	}
}

// TestQuotaEvaluateAnyMode — ANY mode admits when at least one gate has room,
// and consumes only the gates that individually passed.
func TestQuotaEvaluateAnyMode(t *testing.T) {
	e := quotaEngine()
	if err := e.CostBudgets.SetBudget("u1", 0.1, 60000); err != nil { // $0.10 cap — a $0.50 charge is denied
		t.Fatal(err)
	}

	pol := aiops.QuotaPolicy{
		Name:  "p",
		Gates: []string{aiops.GateCost, aiops.GateRate},
		Mode:  aiops.QuotaModeAny,
	}
	dims := aiops.QuotaDims{
		HasCost: true, CostScope: "u1", CostUSD: 0.5, // denied
		HasRate: true, RateKey: "k", RateWindowMs: 1000, RateMax: 5, RateCost: 1, // allowed
	}
	dec, err := e.QuotaEvaluate(pol, dims, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Admitted || !dec.Committed {
		t.Fatalf("any-mode should admit on the rate gate: %+v", dec)
	}
	// The denied cost gate must not be charged even on a committed admit.
	if used, _, _, _ := e.CostBudgets.Usage("u1"); used != 0 {
		t.Fatalf("denied cost gate charged in any mode: %v", used)
	}
}

// TestQuotaEvaluateMissingDims — a policy requiring a gate the request didn't
// supply is a caller error, not a silent pass.
func TestQuotaEvaluateMissingDims(t *testing.T) {
	e := quotaEngine()
	pol := aiops.QuotaPolicy{Name: "p", Gates: []string{aiops.GateRisk}, Mode: aiops.QuotaModeAll}
	if _, err := e.QuotaEvaluate(pol, aiops.QuotaDims{}, false); err == nil {
		t.Fatal("missing RISK dims accepted")
	}
}

// TestQuotaConcurrentNoOvershoot — many concurrent ADMITs each wanting the
// whole budget must admit exactly once; the admission mutex makes peek+commit
// atomic so the budget can't be overshot or double-reported.
func TestQuotaConcurrentNoOvershoot(t *testing.T) {
	e := quotaEngine()
	if err := e.CostBudgets.SetBudget("u", 1.0, 60000); err != nil {
		t.Fatal(err)
	}
	pol := aiops.QuotaPolicy{Name: "p", Gates: []string{aiops.GateCost}, Mode: aiops.QuotaModeAll}
	dims := aiops.QuotaDims{HasCost: true, CostScope: "u", CostUSD: 1.0} // each wants the full $1

	var admitted int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dec, err := e.QuotaEvaluate(pol, dims, true)
			if err == nil && dec.Admitted && dec.Committed {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("concurrent admits committed = %d, want exactly 1 (budget fits one)", admitted)
	}
	if used, _, _, _ := e.CostBudgets.Usage("u"); used != 1.0 {
		t.Fatalf("cost used = %v, want 1.0 (no overshoot)", used)
	}
}

// TestQuotaConcurrentCarbonNoOvershoot — the carbon gate's consume path never
// re-validates, so without the admission mutex N concurrent admits would all
// charge and blow past the CO₂ ceiling. With it, only one commits.
func TestQuotaConcurrentCarbonNoOvershoot(t *testing.T) {
	e := quotaEngine()
	_ = e.Carbon.Intensity("m", 1000) // 1000 Wh / 1k tokens → 1000 tokens = 430 gCO₂
	_ = e.Carbon.Budget("t", 430.0)   // room for exactly one call
	pol := aiops.QuotaPolicy{Name: "p", Gates: []string{aiops.GateCarbon}, Mode: aiops.QuotaModeAll}
	dims := aiops.QuotaDims{HasCarbon: true, CarbonTenant: "t", CarbonTokens: 1000, CarbonModel: "m", CarbonFeature: "f", CarbonRegion: "default"}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.QuotaEvaluate(pol, dims, true) }()
	}
	wg.Wait()
	over, _ := e.Carbon.Over("t")
	if over.UsedG > 500 { // one call ≈ 430g; two would be ≈860g
		t.Fatalf("carbon overshoot: used=%v g, budget=430 g (only one call should have committed)", over.UsedG)
	}
}

// TestEngineRembedSemanticDualReadSwap drives a real semantic cache through a
// full migration: plan → start (dual-read) → serve from both spaces → swap.
func TestEngineRembedSemanticDualReadSwap(t *testing.T) {
	e := &Engine{
		Semantic: semcache.New(8, "semantic"),
		Rembed:   rembed.New(),
		Cfg:      config.Config{EmbeddingDim: 8},
	}
	e.Semantic.Set("the capital of france is paris", "Paris")
	e.Semantic.Set("the speed of light in vacuum", "299792458")
	e.registerRembedTargets()

	plan, err := e.Rembed.Plan("semantic", 16)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalCount != 2 {
		t.Fatalf("plan count = %d, want 2", plan.TotalCount)
	}

	id, err := e.Rembed.Start("semantic", 16, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		p, _ := e.Rembed.Progress(id)
		if p.State == rembed.StateStaged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migration stuck in %s", p.State)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// During the staged dual-read window the cache still answers (old 8-dim
	// space + 16-dim shadow are unioned).
	if v, _, ok := e.Semantic.Get("capital of france", 0.0); !ok || v != "Paris" {
		t.Fatalf("dual-read Get = (%q,%v), want (Paris,true)", v, ok)
	}

	if err := e.Rembed.Swap(id); err != nil {
		t.Fatal(err)
	}
	if got := e.Semantic.Index().Dim(); got != 16 {
		t.Fatalf("post-swap dim = %d, want 16", got)
	}
	// And it still answers from the committed 16-dim space.
	if v, _, ok := e.Semantic.Get("capital of france", 0.0); !ok || v != "Paris" {
		t.Fatalf("post-swap Get = (%q,%v), want (Paris,true)", v, ok)
	}
}
