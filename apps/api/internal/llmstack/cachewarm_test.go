package llmstack

import (
	"testing"
)

func TestCacheWarmRecordAndPlan(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("launch", "summarize the doc", 0)
	c.Record("launch", "translate to french", 0)
	plan, ok := c.Plan("launch", 0)
	if !ok || len(plan) != 2 {
		t.Fatalf("plan = %d", len(plan))
	}
}

func TestCacheWarmMergesParaphrases(t *testing.T) {
	c := NewCacheWarmer()
	// Three paraphrases of the same intent — should collapse with default MIN_SIM
	c.SetMinSim("launch", 0.50) // tuned for hashed-BoW
	c.Record("launch", "cancel subscription billing mid-cycle refund", 3)
	c.Record("launch", "subscription cancel billing mid-cycle refund", 5)
	c.Record("launch", "billing cancel subscription mid-cycle refund process", 7)
	plan, _ := c.Plan("launch", 0)
	if len(plan) != 1 {
		t.Fatalf("paraphrases should merge: got %d entries", len(plan))
	}
	if plan[0].Weight != 15 {
		t.Fatalf("merged weight = %f, want 15", plan[0].Weight)
	}
}

func TestCacheWarmSortsByWeightDesc(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("launch", "low", 1)
	c.Record("launch", "high", 100)
	c.Record("launch", "mid", 10)
	plan, _ := c.Plan("launch", 0)
	if plan[0].Query != "high" || plan[2].Query != "low" {
		t.Fatalf("not weight-sorted: %+v", plan)
	}
}

func TestCacheWarmPlanLimit(t *testing.T) {
	c := NewCacheWarmer()
	for i := 0; i < 10; i++ {
		c.Record("launch", "query "+itoaBench(i), float64(i+1))
	}
	plan, _ := c.Plan("launch", 3)
	if len(plan) != 3 {
		t.Fatalf("limit not respected: %d", len(plan))
	}
}

func TestCacheWarmMarkAndProgress(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("launch", "a", 0)
	c.Record("launch", "b", 0)
	c.Record("launch", "c", 0)
	c.Mark("launch", "a")
	c.Mark("launch", "b")
	p, _ := c.Progress("launch")
	if p.Total != 3 || p.Warmed != 2 || p.Remaining != 1 {
		t.Fatalf("progress = %+v", p)
	}
	if p.PctComplete < 0.66 || p.PctComplete > 0.67 {
		t.Fatalf("pct = %f", p.PctComplete)
	}
}

func TestCacheWarmMarkIsIdempotent(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("launch", "a", 0)
	c.Mark("launch", "a")
	c.Mark("launch", "a")
	p, _ := c.Progress("launch")
	if p.Warmed != 1 {
		t.Fatalf("double mark counted twice: %d", p.Warmed)
	}
}

func TestCacheWarmPlanPutsUnwarmedFirst(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("launch", "low-unwarmed", 1)
	c.Record("launch", "high-warmed", 100)
	c.Mark("launch", "high-warmed")
	plan, _ := c.Plan("launch", 0)
	// Unwarmed first, even though high-warmed has higher weight
	if plan[0].Query != "low-unwarmed" {
		t.Fatalf("unwarmed should come first: %+v", plan)
	}
}

func TestCacheWarmMarkUnknownWarmID(t *testing.T) {
	c := NewCacheWarmer()
	if err := c.Mark("ghost", "x"); err == nil {
		t.Fatal("mark on unknown warm_id should fail")
	}
}

func TestCacheWarmListSorted(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("zeta", "x", 0)
	c.Record("alpha", "x", 0)
	c.Record("mid", "x", 0)
	l := c.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestCacheWarmResetOne(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("a", "x", 0)
	c.Record("b", "x", 0)
	if c.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestCacheWarmResetAll(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("a", "x", 0)
	c.Record("b", "x", 0)
	if c.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestCacheWarmRejectsBadInput(t *testing.T) {
	c := NewCacheWarmer()
	if err := c.Record("", "q", 0); err == nil {
		t.Fatal("empty warm_id should fail")
	}
	if err := c.Record("w", "", 0); err == nil {
		t.Fatal("empty query should fail")
	}
	if err := c.Record("w", "q", -1); err == nil {
		t.Fatal("negative weight should fail")
	}
	if err := c.SetMinSim("w", 1.5); err == nil {
		t.Fatal("min_sim > 1 should fail")
	}
}

func TestCacheWarmStatsAdvance(t *testing.T) {
	c := NewCacheWarmer()
	c.Record("w", "x", 0)
	c.Plan("w", 0)
	c.Mark("w", "x")
	st := c.Stats()
	if st.Plans != 1 || st.TotalEntries != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalRecords != 1 || st.TotalPlans != 1 || st.TotalMarks != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkCacheWarmRecord(b *testing.B) {
	c := NewCacheWarmer()
	// Pre-seed with 50 entries so dedup has work to do
	for i := 0; i < 50; i++ {
		c.Record("w", "query "+itoaBench(i), 1)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Record("w", "summarize the doc briefly", 1)
	}
}
