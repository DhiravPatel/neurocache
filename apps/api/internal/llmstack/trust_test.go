package llmstack

import (
	"testing"
)

func TestTrustRecordAndScore(t *testing.T) {
	tr := NewTrustRegistry()
	tr.Record("source:blog-x", "grounded", 0)
	tr.Record("source:blog-x", "grounded", 0)
	tr.Record("source:blog-x", "hallucinated", 0)
	s := tr.Score("source:blog-x")
	if s.N != 3 {
		t.Fatalf("n = %d", s.N)
	}
	// Should be > 0.5 (two grounded, one halluc) and < 1.0 (Jeffreys prior pulls toward 0.5)
	if s.Trust <= 0.5 || s.Trust >= 1.0 {
		t.Fatalf("trust = %f", s.Trust)
	}
	if s.CILow >= s.CIHigh {
		t.Fatalf("ci wrong: %f >= %f", s.CILow, s.CIHigh)
	}
}

func TestTrustUnknownEntityReturnsPrior(t *testing.T) {
	tr := NewTrustRegistry()
	s := tr.Score("nope")
	if s.N != 0 || s.Trust != 0.5 {
		t.Fatalf("unknown not at prior: %+v", s)
	}
}

func TestTrustAllOutcomeTypes(t *testing.T) {
	tr := NewTrustRegistry()
	outcomes := []string{"grounded", "hallucinated", "citation_used", "contradicted", "neutral"}
	for _, o := range outcomes {
		if err := tr.Record("e", o, 0); err != nil {
			t.Fatalf("outcome %s failed: %v", o, err)
		}
	}
	s := tr.Score("e")
	if s.Grounded != 1 || s.Hallucinated != 1 || s.Cited != 1 || s.Contradicted != 1 || s.Neutral != 1 {
		t.Fatalf("breakdown wrong: %+v", s)
	}
}

func TestTrustRejectsUnknownOutcome(t *testing.T) {
	tr := NewTrustRegistry()
	if err := tr.Record("e", "made-up", 0); err == nil {
		t.Fatal("unknown outcome should fail")
	}
}

func TestTrustRejectsNegativeWeight(t *testing.T) {
	tr := NewTrustRegistry()
	if err := tr.Record("e", "grounded", -1); err == nil {
		t.Fatal("negative weight should fail")
	}
}

func TestTrustRankTop(t *testing.T) {
	tr := NewTrustRegistry()
	for i := 0; i < 20; i++ {
		tr.Record("source:good", "grounded", 0)
	}
	tr.Record("source:good", "hallucinated", 0)
	for i := 0; i < 20; i++ {
		tr.Record("source:bad", "hallucinated", 0)
	}
	tr.Record("source:bad", "grounded", 0)
	rows := tr.Rank("sources", "top", 10, 0)
	if len(rows) == 0 || rows[0].Entity != "source:good" {
		t.Fatalf("top rank wrong: %+v", rows)
	}
}

func TestTrustRankBottom(t *testing.T) {
	tr := NewTrustRegistry()
	for i := 0; i < 20; i++ {
		tr.Record("source:good", "grounded", 0)
	}
	for i := 0; i < 20; i++ {
		tr.Record("source:bad", "hallucinated", 0)
	}
	rows := tr.Rank("sources", "bottom", 10, 0)
	if rows[0].Entity != "source:bad" {
		t.Fatalf("bottom rank wrong: %+v", rows)
	}
}

func TestTrustRankMinNFilter(t *testing.T) {
	tr := NewTrustRegistry()
	for i := 0; i < 50; i++ {
		tr.Record("source:big", "hallucinated", 0)
	}
	tr.Record("source:tiny", "hallucinated", 0) // n=1
	rows := tr.Rank("sources", "bottom", 10, 10)
	for _, r := range rows {
		if r.N < 10 {
			t.Fatalf("min_n filter failed: %+v", r)
		}
	}
}

func TestTrustRankKindFilter(t *testing.T) {
	tr := NewTrustRegistry()
	tr.Record("source:x", "grounded", 0)
	tr.Record("tool:y", "grounded", 0)
	tr.Record("tool:z", "hallucinated", 0)
	rows := tr.Rank("tools", "top", 10, 0)
	for _, r := range rows {
		if r.Entity[:5] != "tool:" {
			t.Fatalf("kind filter leaked source: %s", r.Entity)
		}
	}
}

func TestTrustDecayShrinksTowardPrior(t *testing.T) {
	tr := NewTrustRegistry()
	for i := 0; i < 100; i++ {
		tr.Record("e", "grounded", 0)
	}
	pre := tr.Score("e")
	tr.Decay(86400) // one shrink
	post := tr.Score("e")
	// Mean stays roughly the same (both shrink) but CI widens (less evidence)
	if post.CIHigh-post.CILow <= pre.CIHigh-pre.CILow {
		t.Fatalf("decay should widen CI: pre=%f post=%f", pre.CIHigh-pre.CILow, post.CIHigh-post.CILow)
	}
}

func TestTrustDecayRejectsNonPositive(t *testing.T) {
	tr := NewTrustRegistry()
	if err := tr.Decay(0); err == nil {
		t.Fatal("zero half_life should fail")
	}
}

func TestTrustReset(t *testing.T) {
	tr := NewTrustRegistry()
	tr.Record("a", "grounded", 0)
	tr.Record("b", "grounded", 0)
	if tr.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
	if tr.Reset("ALL") != 1 {
		t.Fatal("ALL should drop 1 remaining")
	}
}

func TestTrustList(t *testing.T) {
	tr := NewTrustRegistry()
	tr.Record("zeta", "grounded", 0)
	tr.Record("alpha", "grounded", 0)
	l := tr.List()
	if l[0] != "alpha" || l[1] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestTrustStats(t *testing.T) {
	tr := NewTrustRegistry()
	tr.Record("e", "grounded", 0)
	tr.Score("e")
	tr.Rank("", "top", 10, 0)
	s := tr.Stats()
	if s.TotalRecords != 1 || s.TotalScores != 1 || s.TotalRanks != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestTrustHigherNTightensCI(t *testing.T) {
	a := NewTrustRegistry()
	b := NewTrustRegistry()
	for i := 0; i < 5; i++ {
		a.Record("e", "grounded", 0)
	}
	for i := 0; i < 5000; i++ {
		b.Record("e", "grounded", 0)
	}
	sa := a.Score("e")
	sb := b.Score("e")
	if sa.CIHigh-sa.CILow <= sb.CIHigh-sb.CILow {
		t.Fatalf("higher n should tighten CI: small=%f large=%f",
			sa.CIHigh-sa.CILow, sb.CIHigh-sb.CILow)
	}
}

func BenchmarkTrustRecord(b *testing.B) {
	tr := NewTrustRegistry()
	for i := 0; i < b.N; i++ {
		tr.Record("source:bench", "grounded", 0)
	}
}
