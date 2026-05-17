package llmstack

import (
	"testing"
	"time"
)

// stubAutoCtx is a hand-rolled test context implementing AutoEvalContext.
type stubAutoCtx struct {
	verdict     string
	verdictSet  bool
	cosine      float64
	cosineSet   bool
	trustScore  float64
	trustN      int64
	trustSet    bool
	enforce     bool
	enforceSet  bool
	balance     float64
	balanceSet  bool
	price       float64
	priceSet    bool
	starved     int
	starvedSet  bool
	hitRate     float64
	hitRateSet  bool
}

func (s *stubAutoCtx) VecSpaceVerdict(_ string) (string, bool)    { return s.verdict, s.verdictSet }
func (s *stubAutoCtx) VecSpaceMeanCosine(_ string) (float64, bool) { return s.cosine, s.cosineSet }
func (s *stubAutoCtx) TrustScore(_ string) (float64, int64, bool) {
	return s.trustScore, s.trustN, s.trustSet
}
func (s *stubAutoCtx) RiskEnforce(_ string) (bool, bool)        { return s.enforce, s.enforceSet }
func (s *stubAutoCtx) RiskBalance(_ string) (float64, bool)     { return s.balance, s.balanceSet }
func (s *stubAutoCtx) MarketPrice(_ string) (float64, bool)     { return s.price, s.priceSet }
func (s *stubAutoCtx) MarketStarvedCount(_ string) (int, bool)  { return s.starved, s.starvedSet }
func (s *stubAutoCtx) CFCacheHitRate() (float64, bool)          { return s.hitRate, s.hitRateSet }

func TestAutoRuleRegistersAndFires(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{verdict: "COLLAPSED", verdictSet: true})
	if err := a.Rule("collapse",
		`vecspace.docs.verdict == "COLLAPSED"`,
		"PUBLISH alerts collapsed", 0); err != nil {
		t.Fatal(err)
	}
	fires := a.Evaluate(0)
	if len(fires) != 1 || fires[0].RuleID != "collapse" {
		t.Fatalf("fires = %+v", fires)
	}
}

func TestAutoEdgeTriggered(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{verdict: "COLLAPSED", verdictSet: true})
	a.Rule("r1", `vecspace.s.verdict == "COLLAPSED"`, "x", 0)
	a.Evaluate(0) // fires
	fires := a.Evaluate(0)
	if len(fires) != 0 {
		t.Fatalf("re-fire blocked by edge trigger: %+v", fires)
	}
}

func TestAutoRefiresAfterReturnToFalse(t *testing.T) {
	a := NewAutoRules()
	ctx := &stubAutoCtx{verdict: "COLLAPSED", verdictSet: true}
	a.SetContext(ctx)
	a.Rule("r1", `vecspace.s.verdict == "COLLAPSED"`, "x", 0)
	a.Evaluate(0)
	ctx.verdict = "HEALTHY"
	a.Evaluate(0) // condition flips back to false; resets edge
	ctx.verdict = "COLLAPSED"
	fires := a.Evaluate(0)
	if len(fires) != 1 {
		t.Fatalf("should re-fire after false: %+v", fires)
	}
}

func TestAutoCooldownBlocks(t *testing.T) {
	a := NewAutoRules()
	ctx := &stubAutoCtx{verdict: "COLLAPSED", verdictSet: true}
	a.SetContext(ctx)
	a.Rule("r1", `vecspace.s.verdict == "COLLAPSED"`, "x", time.Hour)
	a.Evaluate(0) // fires
	// Force re-evaluation by flipping back and forth quickly
	ctx.verdict = "HEALTHY"
	a.Evaluate(0)
	ctx.verdict = "COLLAPSED"
	fires := a.Evaluate(0)
	if len(fires) != 0 {
		t.Fatalf("cooldown should block: %+v", fires)
	}
}

func TestAutoPauseAndResume(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{verdict: "COLLAPSED", verdictSet: true})
	a.Rule("r1", `vecspace.s.verdict == "COLLAPSED"`, "x", 0)
	if err := a.Pause("r1"); err != nil {
		t.Fatal(err)
	}
	if fires := a.Evaluate(0); len(fires) != 0 {
		t.Fatal("paused rule should not fire")
	}
	a.Resume("r1")
	if fires := a.Evaluate(0); len(fires) != 1 {
		t.Fatal("resumed rule should fire")
	}
}

func TestAutoDryRun(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{verdict: "COLLAPSED", verdictSet: true})
	a.Rule("r1", `vecspace.s.verdict == "COLLAPSED"`, "x", 0)
	r, _ := a.DryRun("r1")
	if !r.WouldFire || !r.Truth {
		t.Fatalf("dryrun: %+v", r)
	}
	// Evaluate once; now the edge has triggered, so dryrun should say would_fire=false
	a.Evaluate(0)
	r2, _ := a.DryRun("r1")
	if r2.WouldFire {
		t.Fatalf("post-evaluation dryrun should not refire: %+v", r2)
	}
}

func TestAutoFiresAuditTrail(t *testing.T) {
	a := NewAutoRules()
	ctx := &stubAutoCtx{verdict: "COLLAPSED", verdictSet: true}
	a.SetContext(ctx)
	a.Rule("r1", `vecspace.s.verdict == "COLLAPSED"`, "x", 0)
	a.Evaluate(0)
	ctx.verdict = "HEALTHY"
	a.Evaluate(0)
	ctx.verdict = "COLLAPSED"
	a.Evaluate(0)
	fires := a.Fires("", 0)
	if len(fires) != 2 {
		t.Fatalf("fires log = %d", len(fires))
	}
}

func TestAutoConditionParsesOperators(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{trustScore: 0.3, trustN: 50, trustSet: true})
	for _, expr := range []string{
		`trust.x.score < 0.5`,
		`trust.x.score <= 0.3`,
		`trust.x.score != 0.5`,
		`trust.x.n >= 50`,
		`trust.x.n > 10`,
	} {
		if err := a.Rule("r-"+expr, expr, "act", 0); err != nil {
			t.Fatalf("parse %s: %v", expr, err)
		}
	}
	fires := a.Evaluate(0)
	if len(fires) != 5 {
		t.Fatalf("expected 5 fires, got %d", len(fires))
	}
}

func TestAutoRejectsMalformedCondition(t *testing.T) {
	a := NewAutoRules()
	if err := a.Rule("r", "no operator here", "x", 0); err == nil {
		t.Fatal("malformed condition should fail")
	}
}

func TestAutoEvaluatesAllMetricTypes(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{
		verdict: "COLLAPSED", verdictSet: true,
		cosine: 0.9, cosineSet: true,
		trustScore: 0.3, trustN: 100, trustSet: true,
		enforce: true, enforceSet: true,
		balance: -1, balanceSet: true,
		price: 0.6, priceSet: true,
		starved: 5, starvedSet: true,
		hitRate: 0.2, hitRateSet: true,
	})
	rules := map[string]string{
		"vs":    `vecspace.s.verdict == "COLLAPSED"`,
		"cos":   `vecspace.s.cosine > 0.8`,
		"trust": `trust.x.score < 0.5`,
		"enf":   `risk.s.enforce == "1"`,
		"bal":   `risk.s.balance < 0`,
		"price": `market.m.price > 0.5`,
		"starv": `market.m.starved >= 3`,
		"hit":   `cfcache.hit_rate < 0.5`,
	}
	for id, cond := range rules {
		a.Rule(id, cond, "x", 0)
	}
	fires := a.Evaluate(0)
	if len(fires) != len(rules) {
		t.Fatalf("expected %d fires, got %d", len(rules), len(fires))
	}
}

func TestAutoEvaluatesNothingWithoutContext(t *testing.T) {
	a := NewAutoRules()
	a.Rule("r", `vecspace.s.verdict == "COLLAPSED"`, "x", 0)
	if fires := a.Evaluate(0); len(fires) != 0 {
		t.Fatal("no context should not fire")
	}
}

func TestAutoEvaluatesNothingForUnknownMetric(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{})
	if err := a.Rule("r", `vecspace.s.verdict == "COLLAPSED"`, "x", 0); err != nil {
		t.Fatal(err)
	}
	if fires := a.Evaluate(0); len(fires) != 0 {
		t.Fatal("missing metric should not fire")
	}
}

func TestAutoListGetUnrule(t *testing.T) {
	a := NewAutoRules()
	a.Rule("r1", `vecspace.s.verdict == "X"`, "act", 0)
	a.Rule("r2", `vecspace.s.verdict == "X"`, "act", 0)
	if len(a.List()) != 2 {
		t.Fatal("list")
	}
	v, ok := a.Get("r1")
	if !ok || v.ID != "r1" {
		t.Fatalf("get: %+v", v)
	}
	if a.Unrule("r1") != 1 {
		t.Fatal("unrule")
	}
}

func TestAutoStats(t *testing.T) {
	a := NewAutoRules()
	a.SetContext(&stubAutoCtx{verdict: "X", verdictSet: true})
	a.Rule("r", `vecspace.s.verdict == "X"`, "act", 0)
	a.Evaluate(0)
	s := a.Stats()
	if s.TotalEvals != 1 || s.TotalFires != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestAutoRejectsBadInput(t *testing.T) {
	a := NewAutoRules()
	if err := a.Rule("", "x == 1", "y", 0); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := a.Rule("r", "", "y", 0); err == nil {
		t.Fatal("empty condition should fail")
	}
	if err := a.Rule("r", "x == 1", "", 0); err == nil {
		t.Fatal("empty action should fail")
	}
	if err := a.Rule("r", "x == 1", "y", -1); err == nil {
		t.Fatal("negative cooldown should fail")
	}
}
