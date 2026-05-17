package llmstack

import (
	"strings"
	"testing"
)

func TestPrefRecordAndStats(t *testing.T) {
	p := NewPreferences()
	p.Record("summarizer", "P", "A", "B", "jury", 0.3)
	p.Record("summarizer", "P2", "A2", "B2", "jury", 0.4)
	s, ok := p.Stats("summarizer")
	if !ok || s.Pairs != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if s.MeanMargin < 0.34 || s.MeanMargin > 0.36 {
		t.Fatalf("mean margin = %f", s.MeanMargin)
	}
}

func TestPrefDedupesIdenticalTriples(t *testing.T) {
	p := NewPreferences()
	r1, _ := p.Record("d", "P", "A", "B", "jury", 0.5)
	r2, _ := p.Record("d", "P", "A", "B", "jury", 0.9) // duplicate
	if !r1.Recorded || r2.Recorded || !r2.Duplicate {
		t.Fatalf("dedup: %+v %+v", r1, r2)
	}
	s, _ := p.Stats("d")
	if s.Pairs != 1 {
		t.Fatalf("pairs = %d", s.Pairs)
	}
}

func TestPrefRejectsBadInput(t *testing.T) {
	p := NewPreferences()
	if _, err := p.Record("", "P", "A", "B", "", 0); err == nil {
		t.Fatal("empty dataset should fail")
	}
	if _, err := p.Record("d", "", "A", "B", "", 0); err == nil {
		t.Fatal("empty prompt should fail")
	}
	if _, err := p.Record("d", "P", "", "B", "", 0); err == nil {
		t.Fatal("empty chosen should fail")
	}
	if _, err := p.Record("d", "P", "A", "", "", 0); err == nil {
		t.Fatal("empty rejected should fail")
	}
	if _, err := p.Record("d", "P", "A", "A", "", 0); err == nil {
		t.Fatal("chosen == rejected should fail")
	}
}

func TestPrefExportDPO(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P", "A", "B", "jury", 0.5)
	p.Record("d", "P2", "A2", "B2", "thumbs", 0.05)
	ex, ok := p.Export("d", "dpo", 0.1, "", 0)
	if !ok || ex.Pairs != 1 {
		t.Fatalf("export filtered low-margin: %+v", ex)
	}
	if !strings.Contains(ex.JSONL, "\"chosen\":\"A\"") {
		t.Fatalf("dpo format missing: %s", ex.JSONL)
	}
}

func TestPrefExportSourceFilter(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P", "A", "B", "jury", 0.5)
	p.Record("d", "P2", "A2", "B2", "thumbs", 0.5)
	ex, _ := p.Export("d", "dpo", 0, "thumbs", 0)
	if ex.Pairs != 1 {
		t.Fatalf("source filter = %d", ex.Pairs)
	}
}

func TestPrefExportFormats(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P", "A", "B", "j", 0.5)
	for _, f := range []string{"dpo", "sft", "rlhf"} {
		ex, ok := p.Export("d", f, 0, "", 0)
		if !ok {
			t.Fatalf("format %s failed", f)
		}
		if ex.JSONL == "" {
			t.Fatalf("format %s empty", f)
		}
	}
	if _, ok := p.Export("d", "bogus", 0, "", 0); ok {
		t.Fatal("bogus format should fail")
	}
}

func TestPrefExportLimit(t *testing.T) {
	p := NewPreferences()
	for i := 0; i < 10; i++ {
		p.Record("d", "P-"+itoaBench(i), "A", "B", "j", 0.5)
	}
	ex, _ := p.Export("d", "dpo", 0, "", 3)
	if ex.Pairs != 3 {
		t.Fatalf("limit not respected: %d", ex.Pairs)
	}
}

func TestPrefStatsBreakdown(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P1", "A", "B", "jury", 0.5)
	p.Record("d", "P2", "A", "B", "jury", 0.5)
	p.Record("d", "P3", "A", "B", "thumbs", 0.5)
	s, _ := p.Stats("d")
	if s.BySource["jury"] != 2 || s.BySource["thumbs"] != 1 {
		t.Fatalf("breakdown: %+v", s.BySource)
	}
}

func TestPrefStatsCleanPairs(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P1", "A", "B", "j", 0.5)
	p.Record("d", "P2", "A", "B", "j", 0.05)
	s, _ := p.Stats("d")
	if s.CleanPairs != 1 {
		t.Fatalf("clean = %d", s.CleanPairs)
	}
}

func TestPrefReset(t *testing.T) {
	p := NewPreferences()
	p.Record("a", "P", "A", "B", "", 0)
	p.Record("b", "P", "A", "B", "", 0)
	if p.Reset("a") != 1 {
		t.Fatal("reset a")
	}
	if p.Reset("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestPrefGlobalStats(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P", "A", "B", "", 0)
	p.Record("d", "P", "A", "B", "", 0) // dupe
	gs := p.GlobalStats()
	if gs.TotalRecords != 2 || gs.TotalDupes != 1 {
		t.Fatalf("global = %+v", gs)
	}
	if gs.TotalPairs != 1 {
		t.Fatalf("pairs = %d", gs.TotalPairs)
	}
}

func TestPrefMarginClamp(t *testing.T) {
	p := NewPreferences()
	p.Record("d", "P", "A", "B", "", 5.0) // clamps to 1
	s, _ := p.Stats("d")
	if s.MeanMargin > 1 {
		t.Fatalf("not clamped: %f", s.MeanMargin)
	}
}

func BenchmarkPrefRecord(b *testing.B) {
	p := NewPreferences()
	for i := 0; i < b.N; i++ {
		p.Record("d", "prompt-"+itoaBench(i), "A", "B", "j", 0.5)
	}
}
