package llmstack

import (
	"math"
	"testing"
)

func TestRetrievalLearnWeightDefaults(t *testing.T) {
	r := NewRetrievalLearner()
	// Unseen chunk has neutral boost
	if w := r.Weight("ghost"); w != 1.0 {
		t.Fatalf("unseen weight = %f, want 1.0", w)
	}
}

func TestRetrievalLearnRecordCitedBoosts(t *testing.T) {
	r := NewRetrievalLearner()
	for i := 0; i < 20; i++ {
		r.Record("good-chunk", true, 0)
	}
	w := r.Weight("good-chunk")
	if w < 1.5 {
		t.Fatalf("repeatedly cited chunk should boost; weight=%f", w)
	}
	if w > 2.0 {
		t.Fatalf("weight should cap at 2.0; got %f", w)
	}
}

func TestRetrievalLearnRecordNotCitedDecays(t *testing.T) {
	r := NewRetrievalLearner()
	for i := 0; i < 20; i++ {
		r.Record("bad-chunk", false, 0)
	}
	w := r.Weight("bad-chunk")
	if w > 0.7 {
		t.Fatalf("repeatedly not-cited chunk should decay; weight=%f", w)
	}
	if w < 0.5 {
		t.Fatalf("weight should floor at 0.5; got %f", w)
	}
}

func TestRetrievalLearnQualityOverridesCited(t *testing.T) {
	r := NewRetrievalLearner()
	// Quality 0.8 should be used as the signal regardless of cited flag
	for i := 0; i < 20; i++ {
		r.Record("c", false, 0.8)
	}
	st, _ := r.Status("c")
	if math.Abs(st.CitedRate-0.8) > 0.05 {
		t.Fatalf("cited rate should converge to 0.8, got %f", st.CitedRate)
	}
}

func TestRetrievalLearnRerankReshufflesByLearnedWeight(t *testing.T) {
	r := NewRetrievalLearner()
	// "good" cited a lot; "bad" never cited
	for i := 0; i < 20; i++ {
		r.Record("good", true, 0)
		r.Record("bad", false, 0)
	}
	// Incoming retrieval ranks "bad" above "good" by cosine
	rows := []RerankRow{
		{ChunkID: "bad", Score: 0.90},
		{ChunkID: "good", Score: 0.80},
	}
	out := r.Rerank(rows)
	// After re-rank, good should beat bad despite worse cosine
	if out[0].ChunkID != "good" {
		t.Fatalf("rerank did not promote learned winner: %+v", out)
	}
}

func TestRetrievalLearnRerankUnseenChunksKeepOrder(t *testing.T) {
	r := NewRetrievalLearner()
	rows := []RerankRow{
		{ChunkID: "a", Score: 0.9},
		{ChunkID: "b", Score: 0.7},
	}
	out := r.Rerank(rows)
	if out[0].ChunkID != "a" || out[1].ChunkID != "b" {
		t.Fatalf("unseen chunks should preserve order: %+v", out)
	}
}

func TestRetrievalLearnStatusReportsSamples(t *testing.T) {
	r := NewRetrievalLearner()
	r.Record("c", true, 0)
	r.Record("c", true, 0)
	r.Record("c", false, 0)
	st, ok := r.Status("c")
	if !ok || st.Samples != 3 || st.CitedCount != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestRetrievalLearnTopAndBottom(t *testing.T) {
	r := NewRetrievalLearner()
	for i := 0; i < 20; i++ {
		r.Record("winner", true, 0)
		r.Record("middle", true, 0)
		r.Record("loser", false, 0)
	}
	// Drag "middle" down a bit
	for i := 0; i < 10; i++ {
		r.Record("middle", false, 0)
	}
	top := r.Top(1)
	if len(top) != 1 || top[0].ChunkID != "winner" {
		t.Fatalf("top = %+v", top)
	}
	bot := r.Bottom(1)
	if len(bot) != 1 || bot[0].ChunkID != "loser" {
		t.Fatalf("bottom = %+v", bot)
	}
}

func TestRetrievalLearnResetAll(t *testing.T) {
	r := NewRetrievalLearner()
	r.Record("a", true, 0)
	r.Record("b", true, 0)
	dropped := r.Reset("ALL")
	if dropped != 2 {
		t.Fatalf("reset all dropped %d, want 2", dropped)
	}
	if r.Stats().Chunks != 0 {
		t.Fatal("chunks remain after ALL reset")
	}
}

func TestRetrievalLearnResetOne(t *testing.T) {
	r := NewRetrievalLearner()
	r.Record("a", true, 0)
	r.Record("b", true, 0)
	if r.Reset("a") != 1 {
		t.Fatal("reset a did not drop 1")
	}
	if _, ok := r.Status("a"); ok {
		t.Fatal("a still present after reset")
	}
	if _, ok := r.Status("b"); !ok {
		t.Fatal("b dropped by mistake")
	}
}

func TestRetrievalLearnRejectsBadInput(t *testing.T) {
	r := NewRetrievalLearner()
	if err := r.Record("", true, 0); err == nil {
		t.Fatal("empty chunk_id should fail")
	}
	if err := r.Record("c", false, 1.5); err == nil {
		t.Fatal("quality > 1 should fail")
	}
	if err := r.SetAlpha(0); err == nil {
		t.Fatal("alpha=0 should fail")
	}
	if err := r.SetAlpha(1.5); err == nil {
		t.Fatal("alpha>1 should fail")
	}
}

func TestRetrievalLearnAlphaTunable(t *testing.T) {
	r := NewRetrievalLearner()
	r.SetAlpha(1.0) // fast learning — one sample dominates
	r.Record("c", true, 0)
	r.Record("c", false, 0) // with alpha=1, cited_rate jumps to 0 immediately
	st, _ := r.Status("c")
	if st.CitedRate > 0.01 {
		t.Fatalf("with alpha=1, latest should dominate: %f", st.CitedRate)
	}
}

func TestRetrievalLearnStatsAdvance(t *testing.T) {
	r := NewRetrievalLearner()
	r.Record("a", true, 0)
	r.Record("b", true, 0)
	r.Rerank([]RerankRow{{ChunkID: "a", Score: 0.9}})
	s := r.Stats()
	if s.Chunks != 2 || s.TotalRecords != 2 || s.TotalReranks != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.MeanWeight < 1.5 {
		t.Fatalf("mean weight = %f", s.MeanWeight)
	}
}

func BenchmarkRetrievalLearnRecord(b *testing.B) {
	r := NewRetrievalLearner()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Record("chunk-many", true, 0)
	}
}

func BenchmarkRetrievalLearnRerank10(b *testing.B) {
	r := NewRetrievalLearner()
	for i := 0; i < 10; i++ {
		r.Record("c"+itoaBench(i), true, 0)
	}
	rows := make([]RerankRow, 10)
	for i := range rows {
		rows[i] = RerankRow{ChunkID: "c" + itoaBench(i), Score: 0.8}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Rerank(rows)
	}
}
