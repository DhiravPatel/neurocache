package llmstack

import (
	"testing"
)

func TestToolDriftNoBaseline(t *testing.T) {
	w := NewToolDriftWatcher()
	r, err := w.Check("weather", `{"temp":72,"unit":"F"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "no_baseline" {
		t.Fatalf("verdict = %s", r.Verdict)
	}
}

func TestToolDriftStableMatchesBaseline(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("weather", []string{
		`{"temp":72,"unit":"F","city":"SF"}`,
		`{"temp":68,"unit":"F","city":"NYC"}`,
	})
	r, _ := w.Check("weather", `{"temp":75,"unit":"F","city":"LA"}`)
	if r.Verdict != "stable" {
		t.Fatalf("same-shape payload not stable: %+v", r)
	}
}

func TestToolDriftDetectsAddedField(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("weather", []string{
		`{"temp":72,"unit":"F"}`,
		`{"temp":68,"unit":"F"}`,
	})
	// API added a new nested field
	r, _ := w.Check("weather", `{"temp":72,"unit":"F","precip":{"chance":0.4,"type":"rain"}}`)
	if r.Verdict == "stable" {
		t.Fatalf("new field should drift verdict away from stable: %+v", r)
	}
}

func TestToolDriftDetectsTypeChange(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("weather", []string{
		`{"temp":72,"unit":"F"}`,
		`{"temp":68,"unit":"F"}`,
	})
	// API changed temp from number to string
	r, _ := w.Check("weather", `{"temp":"72","unit":"F"}`)
	if r.Verdict == "stable" {
		t.Fatalf("type change should not be stable: %+v", r)
	}
}

func TestToolDriftFullyDifferentSchemaIsDrift(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("weather", []string{
		`{"temp":72,"unit":"F"}`,
	})
	r, _ := w.Check("weather", `{"completely":{"different":"schema","items":[1,2,3]}}`)
	if r.Verdict != "drift" && r.Verdict != "warning" {
		t.Fatalf("very different schema not flagged: %+v", r)
	}
}

func TestToolDriftSampleRollsRecent(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("api", []string{`{"x":1}`})
	for i := 0; i < 5; i++ {
		w.Sample("api", `{"x":2}`)
	}
	rec, ok := w.Recent("api", 0)
	if !ok || len(rec) != 5 {
		t.Fatalf("recent = %d", len(rec))
	}
}

func TestToolDriftSampleLimit(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("api", []string{`{"x":1}`})
	for i := 0; i < 5; i++ {
		w.Sample("api", `{"x":2}`)
	}
	rec, _ := w.Recent("api", 3)
	if len(rec) != 3 {
		t.Fatalf("limited recent = %d", len(rec))
	}
}

func TestToolDriftStatusReportsLast(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("api", []string{`{"x":1,"y":2}`})
	w.Sample("api", `{"x":3,"y":4}`)
	st, ok := w.Status("api")
	if !ok || st.LastVerdict != "stable" {
		t.Fatalf("status = %+v", st)
	}
	if st.BaselineSize != 1 {
		t.Fatalf("baseline size = %d", st.BaselineSize)
	}
}

func TestToolDriftListSorted(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("zeta", []string{`{}`})
	w.Baseline("alpha", []string{`{}`})
	w.Baseline("middle", []string{`{}`})
	l := w.List()
	if len(l) != 3 || l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestToolDriftReset(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("api", []string{`{}`})
	if !w.Reset("api") {
		t.Fatal("reset should report success")
	}
	if _, ok := w.Status("api"); ok {
		t.Fatal("after reset, tool should be gone")
	}
}

func TestToolDriftPlainText(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("scraper", []string{
		"Server: nginx/1.18\nDate: 2024",
		"Server: nginx/1.18\nDate: 2024",
	})
	r, _ := w.Check("scraper", "Server: nginx/1.18\nDate: 2024")
	if r.Verdict != "stable" {
		t.Fatalf("identical text not stable: %+v", r)
	}
	r2, _ := w.Check("scraper", "ERROR 500: gateway timeout cascade exception")
	if r2.Verdict == "stable" {
		t.Fatalf("error payload not flagged: %+v", r2)
	}
}

func TestToolDriftRejectsBadInput(t *testing.T) {
	w := NewToolDriftWatcher()
	if err := w.Baseline("", []string{`{}`}); err == nil {
		t.Fatal("empty tool_id should fail")
	}
	if err := w.Baseline("a", nil); err == nil {
		t.Fatal("nil baseline should fail")
	}
	if _, err := w.Sample("", "x"); err == nil {
		t.Fatal("empty tool_id should fail")
	}
}

func TestToolDriftStatsAdvance(t *testing.T) {
	w := NewToolDriftWatcher()
	w.Baseline("api", []string{`{"a":1}`})
	w.Sample("api", `{"a":2}`)
	w.Check("api", `{"b":99,"c":[1,2,3]}`) // drift
	s := w.Stats()
	if s.Tools != 1 {
		t.Fatalf("tools = %d", s.Tools)
	}
	if s.TotalSamples != 1 || s.TotalChecks < 1 {
		t.Fatalf("counters = %+v", s)
	}
}

func BenchmarkToolDriftCheck(b *testing.B) {
	w := NewToolDriftWatcher()
	w.Baseline("api", []string{
		`{"temp":72,"unit":"F","city":"SF","tags":["sunny","warm"]}`,
		`{"temp":68,"unit":"F","city":"NYC","tags":["cloudy"]}`,
	})
	payload := `{"temp":75,"unit":"F","city":"LA","tags":["sunny","hot"]}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Check("api", payload)
	}
}
