package llmstack

import (
	"strings"
	"testing"
	"time"
)

func TestRAGGapObserveAndReport(t *testing.T) {
	g := NewRAGGap()
	g.Observe("docs", "how do I cancel mid-cycle", 0.31)
	g.Observe("docs", "refund for annual plan", 0.28)
	g.Observe("docs", "what is your uptime SLA", 0.88) // good hit, excluded
	rows, err := g.Report("docs", RAGGapFilter{Threshold: 0.40, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one gap row")
	}
	for _, r := range rows {
		if strings.Contains(r.SampleQuery, "uptime SLA") {
			t.Fatalf("good hit leaked into report: %+v", r)
		}
	}
}

func TestRAGGapClustersBySimilarity(t *testing.T) {
	g := NewRAGGap()
	// Three billing-cancellation paraphrases with heavy lexical overlap
	// (hashed-BoW is coarse; real sentence-transformer embeddings would
	// cluster looser paraphrases too).
	g.Observe("docs", "cancel subscription billing mid-cycle refund", 0.20)
	g.Observe("docs", "subscription cancel billing mid-cycle refund", 0.22)
	g.Observe("docs", "billing cancel subscription mid-cycle refund process", 0.18)
	// One unrelated low-score query
	g.Observe("docs", "weather forecast api token rotation", 0.30)
	rows, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40, Limit: 10})
	// Cancellation paraphrases should collapse → 2 clusters total
	if len(rows) > 2 {
		t.Fatalf("expected ≤2 clusters from 4 paraphrases; got %d: %+v", len(rows), rows)
	}
	// Top cluster should be the cancellation one (n=3 > weather's n=1)
	if rows[0].N < 2 {
		t.Fatalf("top cluster n = %d, want ≥2", rows[0].N)
	}
}

func TestRAGGapRanksByGapWeight(t *testing.T) {
	g := NewRAGGap()
	// "billing" cluster: many queries, moderate miss
	for i := 0; i < 10; i++ {
		g.Observe("docs", "billing cancellation question", 0.30)
	}
	// "obscure" cluster: few queries, huge miss
	for i := 0; i < 2; i++ {
		g.Observe("docs", "totally unrelated obscure topic", 0.05)
	}
	rows, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40})
	// Billing wins: 10 × 0.10 = 1.0  vs  obscure: 2 × 0.35 = 0.70
	if rows[0].N != 10 {
		t.Fatalf("top by weight should be billing (n=10): %+v", rows)
	}
}

func TestRAGGapWindowFiltersOldObservations(t *testing.T) {
	g := NewRAGGap()
	g.Observe("docs", "old query", 0.30)
	time.Sleep(3 * time.Millisecond)
	g.Observe("docs", "new query", 0.20)
	rows, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40, Window: 1 * time.Millisecond})
	for _, r := range rows {
		if strings.Contains(r.SampleQuery, "old query") {
			t.Fatalf("window filter let old query through: %+v", r)
		}
	}
}

func TestRAGGapResolveMarksCluster(t *testing.T) {
	g := NewRAGGap()
	g.Observe("docs", "billing cancellation question one", 0.30)
	g.Observe("docs", "billing cancellation question two", 0.32)
	rows, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40})
	if len(rows) == 0 {
		t.Fatal("expected a cluster")
	}
	id := rows[0].ClusterID
	g.Resolve("docs", id)
	rows2, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40})
	for _, r := range rows2 {
		if r.ClusterID == id && !r.Resolved {
			t.Fatalf("cluster not marked resolved: %+v", r)
		}
	}
}

func TestRAGGapReportSortsUnresolvedFirst(t *testing.T) {
	g := NewRAGGap()
	// Big resolved cluster
	for i := 0; i < 20; i++ {
		g.Observe("docs", "addressed question on billing", 0.30)
	}
	rows, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40})
	g.Resolve("docs", rows[0].ClusterID)
	// Now add a tiny unresolved cluster
	g.Observe("docs", "obscure unrelated query", 0.20)
	rows2, _ := g.Report("docs", RAGGapFilter{Threshold: 0.40})
	if rows2[0].Resolved {
		t.Fatalf("unresolved cluster should come first: %+v", rows2)
	}
}

func TestRAGGapQueriesReturnsRaw(t *testing.T) {
	g := NewRAGGap()
	g.Observe("docs", "a", 0.20)
	g.Observe("docs", "b", 0.50) // above threshold
	g.Observe("docs", "c", 0.10)
	q, ok := g.Queries("docs", 0.40, 0)
	if !ok || len(q) != 2 {
		t.Fatalf("queries = %d", len(q))
	}
	// Newest first
	if q[0].Query != "c" {
		t.Fatalf("not newest-first: %+v", q)
	}
}

func TestRAGGapQueriesLimit(t *testing.T) {
	g := NewRAGGap()
	for i := 0; i < 10; i++ {
		g.Observe("docs", "q", 0.20)
	}
	q, _ := g.Queries("docs", 0.40, 3)
	if len(q) != 3 {
		t.Fatalf("limit not respected: %d", len(q))
	}
}

func TestRAGGapIndexesSorted(t *testing.T) {
	g := NewRAGGap()
	g.Observe("zeta", "q", 0.1)
	g.Observe("alpha", "q", 0.1)
	g.Observe("mid", "q", 0.1)
	ix := g.Indexes()
	if len(ix) != 3 || ix[0] != "alpha" || ix[2] != "zeta" {
		t.Fatalf("indexes = %v", ix)
	}
}

func TestRAGGapResetOne(t *testing.T) {
	g := NewRAGGap()
	g.Observe("a", "q", 0.1)
	g.Observe("b", "q", 0.1)
	if g.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestRAGGapResetAll(t *testing.T) {
	g := NewRAGGap()
	g.Observe("a", "q", 0.1)
	g.Observe("b", "q", 0.1)
	if g.Reset("ALL") != 2 {
		t.Fatal("reset ALL should drop 2")
	}
}

func TestRAGGapRejectsBadInput(t *testing.T) {
	g := NewRAGGap()
	if err := g.Observe("", "q", 0.5); err == nil {
		t.Fatal("empty index should fail")
	}
	if err := g.Observe("a", "", 0.5); err == nil {
		t.Fatal("empty query should fail")
	}
	if err := g.Observe("a", "q", -1); err == nil {
		t.Fatal("negative score should fail")
	}
	if err := g.Resolve("", "c"); err == nil {
		t.Fatal("empty index for resolve should fail")
	}
}

func TestRAGGapStatsAdvance(t *testing.T) {
	g := NewRAGGap()
	g.Observe("a", "q", 0.1)
	g.Report("a", RAGGapFilter{})
	g.Resolve("a", "gap-deadbeef")
	st := g.Stats()
	if st.Indexes != 1 || st.TotalObserves != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalReports != 1 || st.TotalResolves != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkRAGGapObserve(b *testing.B) {
	g := NewRAGGap()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Observe("docs", "how do I cancel mid-cycle", 0.31)
	}
}

func BenchmarkRAGGapReport100(b *testing.B) {
	g := NewRAGGap()
	for i := 0; i < 100; i++ {
		g.Observe("docs", "billing cancellation paraphrase "+itoaBench(i), 0.30)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Report("docs", RAGGapFilter{Threshold: 0.40})
	}
}
