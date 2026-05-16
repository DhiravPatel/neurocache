package llmstack

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestLedgerRecordAndSpend(t *testing.T) {
	l := NewCostLedger()
	l.Record("acme", "summarizer", "gpt-4", 0.012, 1200, 300)
	l.Record("acme", "summarizer", "gpt-4", 0.018, 1500, 400)
	l.Record("acme", "tagger", "gpt-3.5", 0.001, 200, 50)
	r := l.Spend("acme", LedgerFilter{})
	want := 0.012 + 0.018 + 0.001
	if math.Abs(r.TotalCostUSD-want) > 1e-9 {
		t.Fatalf("spend = %f, want %f", r.TotalCostUSD, want)
	}
	if r.Calls != 3 {
		t.Fatalf("calls = %d", r.Calls)
	}
}

func TestLedgerSpendFilteredByFeature(t *testing.T) {
	l := NewCostLedger()
	l.Record("acme", "summarizer", "gpt-4", 0.01, 100, 50)
	l.Record("acme", "tagger", "gpt-4", 0.02, 200, 100)
	r := l.Spend("acme", LedgerFilter{Feature: "summarizer"})
	if math.Abs(r.TotalCostUSD-0.01) > 1e-9 {
		t.Fatalf("filtered spend = %f, want 0.01", r.TotalCostUSD)
	}
}

func TestLedgerReportByTenant(t *testing.T) {
	l := NewCostLedger()
	l.Record("acme", "f1", "m1", 0.01, 0, 0)
	l.Record("acme", "f2", "m1", 0.02, 0, 0)
	l.Record("globex", "f1", "m1", 0.10, 0, 0) // clearly higher
	rows, err := l.Report("tenant", LedgerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("groups = %d", len(rows))
	}
	// Sorted by spend desc — globex (0.10) first
	if rows[0].Key != "globex" {
		t.Fatalf("rows[0].Key = %s", rows[0].Key)
	}
}

func TestLedgerReportByModel(t *testing.T) {
	l := NewCostLedger()
	l.Record("a", "f", "gpt-4", 0.10, 0, 0)
	l.Record("b", "f", "gpt-4", 0.20, 0, 0)
	l.Record("a", "f", "claude", 0.05, 0, 0)
	rows, _ := l.Report("model", LedgerFilter{})
	byKey := map[string]LedgerReportRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if math.Abs(byKey["gpt-4"].TotalCostUSD-0.30) > 1e-9 {
		t.Fatalf("gpt-4 total = %f", byKey["gpt-4"].TotalCostUSD)
	}
}

func TestLedgerTop(t *testing.T) {
	l := NewCostLedger()
	for i, t := range []string{"a", "b", "c", "d"} {
		l.Record(t, "f", "m", float64(i+1)*0.01, 0, 0)
	}
	rows, _ := l.Top("tenant", 0, 2)
	if len(rows) != 2 {
		t.Fatalf("top 2 = %d rows", len(rows))
	}
	// Top spender: d (0.04)
	if rows[0].Key != "d" {
		t.Fatalf("top[0] = %s", rows[0].Key)
	}
}

func TestLedgerWindowFilter(t *testing.T) {
	l := NewCostLedger()
	l.Record("a", "f", "m", 0.10, 0, 0)
	time.Sleep(2 * time.Millisecond)
	r := l.Spend("a", LedgerFilter{Window: 1 * time.Millisecond})
	// The record is older than 1ms now
	if r.Calls != 0 {
		t.Fatalf("window filter should exclude old record: calls=%d", r.Calls)
	}
}

func TestLedgerExportCSV(t *testing.T) {
	l := NewCostLedger()
	l.Record("acme", "summarizer", "gpt-4", 0.012, 1200, 300)
	csv, err := l.Export(LedgerFilter{Tenant: "acme"}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv, "tenant,feature,model") {
		t.Fatal("CSV header missing")
	}
	if !strings.Contains(csv, "acme,summarizer,gpt-4") {
		t.Fatal("CSV row missing")
	}
}

func TestLedgerExportJSON(t *testing.T) {
	l := NewCostLedger()
	l.Record("acme", "summarizer", "gpt-4", 0.012, 1200, 300)
	js, _ := l.Export(LedgerFilter{Tenant: "acme"}, "json")
	if !strings.Contains(js, `"tenant":"acme"`) {
		t.Fatalf("JSON output missing tenant: %s", js)
	}
}

func TestLedgerPurgeByTenant(t *testing.T) {
	l := NewCostLedger()
	l.Record("a", "f", "m", 0.01, 0, 0)
	l.Record("a", "f", "m", 0.02, 0, 0)
	l.Record("b", "f", "m", 0.03, 0, 0)
	dropped := l.Purge("a", 0)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	r := l.Spend("b", LedgerFilter{})
	if r.Calls != 1 {
		t.Fatal("tenant b records should survive")
	}
}

func TestLedgerRejectsBadDimension(t *testing.T) {
	l := NewCostLedger()
	if _, err := l.Report("magic", LedgerFilter{}); err == nil {
		t.Fatal("unknown dimension should fail")
	}
}

func TestLedgerRejectsBadRecord(t *testing.T) {
	l := NewCostLedger()
	if err := l.Record("", "f", "m", 0.01, 0, 0); err == nil {
		t.Fatal("empty tenant should fail")
	}
	if err := l.Record("t", "f", "m", -0.01, 0, 0); err == nil {
		t.Fatal("negative cost should fail")
	}
}

func TestLedgerStatsAdvance(t *testing.T) {
	l := NewCostLedger()
	l.Record("a", "f", "m", 0.05, 0, 0)
	l.Report("tenant", LedgerFilter{})
	s := l.Stats()
	if s.TotalRecords != 1 || s.TotalReports != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if math.Abs(s.TotalSpendUSD-0.05) > 1e-9 {
		t.Fatalf("total = %f", s.TotalSpendUSD)
	}
}
