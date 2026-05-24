package llmstack

import (
	"testing"
)

func TestExtractTraceNewAndGet(t *testing.T) {
	e := NewExtractTraceStore()
	source := "Invoice total: $42,000.00 USD"
	e.New("inv-1", source)
	if err := e.Set("inv-1", "amount", "42000", 15, 25, 0.95); err != nil {
		t.Fatal(err)
	}
	row, ok := e.Get("inv-1", "amount")
	if !ok {
		t.Fatal("amount field missing")
	}
	if row.SourceSpan != "$42,000.00" {
		t.Fatalf("source span = %q", row.SourceSpan)
	}
	if row.Confidence != 0.95 {
		t.Fatalf("confidence = %f", row.Confidence)
	}
}

func TestExtractTraceSetRejectsBadSpan(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("x", "short")
	if err := e.Set("x", "f", "v", -1, 3, 0.5); err == nil {
		t.Fatal("negative start should fail")
	}
	if err := e.Set("x", "f", "v", 0, 100, 0.5); err == nil {
		t.Fatal("end past source should fail")
	}
	if err := e.Set("x", "f", "v", 3, 1, 0.5); err == nil {
		t.Fatal("end < start should fail")
	}
}

func TestExtractTraceAllReturnsInsertionOrder(t *testing.T) {
	e := NewExtractTraceStore()
	source := "Name: Alice. Email: alice@example.com. ID: 4242"
	e.New("u-1", source)
	e.Set("u-1", "name", "Alice", 6, 11, 0.99)
	e.Set("u-1", "email", "alice@example.com", 20, 37, 0.98)
	e.Set("u-1", "id", "4242", 43, 47, 0.97)
	rows, ok := e.All("u-1")
	if !ok || len(rows) != 3 {
		t.Fatalf("all = %d", len(rows))
	}
	if rows[0].Field != "name" || rows[2].Field != "id" {
		t.Fatalf("order broken: %+v", rows)
	}
}

func TestExtractTraceVerifyValidExtraction(t *testing.T) {
	e := NewExtractTraceStore()
	source := "Patient: John Doe. Date: 2026-05-17. Dx: pneumonia."
	e.New("rec-1", source)
	e.Set("rec-1", "patient", "John Doe", 9, 17, 0.99)
	e.Set("rec-1", "date", "2026-05-17", 25, 35, 0.99)
	e.Set("rec-1", "diagnosis", "pneumonia", 41, 50, 0.95)
	r, err := e.Verify("rec-1")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Fatalf("valid extraction flagged: %+v", r.Issues)
	}
	if r.NFields != 3 {
		t.Fatalf("n_fields = %d", r.NFields)
	}
}

func TestExtractTraceVerifyCatchesHallucination(t *testing.T) {
	e := NewExtractTraceStore()
	source := "Invoice total: $42,000.00 USD"
	e.New("inv-1", source)
	// LLM claimed the amount is $99,999 but the span is the real one
	e.Set("inv-1", "amount", "99999", 15, 25, 0.7)
	r, _ := e.Verify("inv-1")
	if r.Valid {
		t.Fatal("hallucinated value should fail verification")
	}
	if r.Issues[0].Code != "hallucination" {
		t.Fatalf("code = %s", r.Issues[0].Code)
	}
}

func TestExtractTraceVerifyNumericNormalisation(t *testing.T) {
	e := NewExtractTraceStore()
	source := "Invoice total: $42,000.00 USD"
	e.New("inv-1", source)
	// "42000" should match because non-digits are stripped from both sides
	e.Set("inv-1", "amount", "42000", 15, 25, 0.95)
	r, _ := e.Verify("inv-1")
	if !r.Valid {
		t.Fatalf("numeric normalised match should validate: %+v", r.Issues)
	}
}

func TestExtractTraceVerifyCaseInsensitive(t *testing.T) {
	e := NewExtractTraceStore()
	source := "Diagnosis: PNEUMONIA confirmed"
	e.New("rec", source)
	e.Set("rec", "dx", "pneumonia", 11, 20, 0.9)
	r, _ := e.Verify("rec")
	if !r.Valid {
		t.Fatalf("case-insensitive match should validate: %+v", r.Issues)
	}
}

func TestExtractTraceSetOverwrites(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("x", "hello world hello again")
	e.Set("x", "greet", "hello", 0, 5, 0.5)
	e.Set("x", "greet", "hello again", 12, 23, 0.8)
	row, _ := e.Get("x", "greet")
	if row.Value != "hello again" {
		t.Fatalf("overwrite failed: %s", row.Value)
	}
}

func TestExtractTraceGetMissingField(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("x", "source")
	if _, ok := e.Get("x", "missing"); ok {
		t.Fatal("missing field should report not-ok")
	}
}

func TestExtractTraceListSorted(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("zeta", "x")
	e.New("alpha", "x")
	e.New("mid", "x")
	l := e.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestExtractTraceDropOne(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("a", "x")
	e.New("b", "x")
	if e.Drop("a") != 1 {
		t.Fatal("drop a should remove 1")
	}
}

func TestExtractTraceDropAll(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("a", "x")
	e.New("b", "x")
	if e.Drop("ALL") != 2 {
		t.Fatal("ALL drop should remove 2")
	}
}

func TestExtractTraceRejectsBadInput(t *testing.T) {
	e := NewExtractTraceStore()
	if err := e.New("", "x"); err == nil {
		t.Fatal("empty extract_id should fail")
	}
	if err := e.Set("", "f", "v", 0, 1, 0.5); err == nil {
		t.Fatal("empty extract_id should fail")
	}
	if err := e.Set("ghost", "f", "v", 0, 1, 0.5); err == nil {
		t.Fatal("unknown extract_id should fail")
	}
	e.New("x", "hello")
	if err := e.Set("x", "", "v", 0, 1, 0.5); err == nil {
		t.Fatal("empty field should fail")
	}
	if err := e.Set("x", "f", "v", 0, 1, 1.5); err == nil {
		t.Fatal("confidence > 1 should fail")
	}
}

func TestExtractTraceVerifyEmptyExtractValid(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("x", "source text")
	r, _ := e.Verify("x")
	if !r.Valid || r.NFields != 0 {
		t.Fatalf("empty extract should be valid: %+v", r)
	}
}

func TestExtractTraceStatsAdvance(t *testing.T) {
	e := NewExtractTraceStore()
	e.New("x", "hello")
	e.Set("x", "g", "hello", 0, 5, 0.5)
	e.Verify("x")
	st := e.Stats()
	if st.Extracts != 1 || st.TotalNew != 1 || st.TotalSets != 1 || st.TotalVerifies != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkExtractTraceSet(b *testing.B) {
	e := NewExtractTraceStore()
	e.New("x", "Invoice total: $42,000.00 USD for service rendered 2026-05-17.")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Set("x", "amount", "42000", 15, 25, 0.95)
	}
}

func BenchmarkExtractTraceVerify(b *testing.B) {
	e := NewExtractTraceStore()
	source := "Patient: John Doe. Date: 2026-05-17. Dx: pneumonia. Med: amoxicillin 500mg."
	e.New("x", source)
	e.Set("x", "patient", "John Doe", 9, 17, 0.99)
	e.Set("x", "date", "2026-05-17", 25, 35, 0.99)
	e.Set("x", "dx", "pneumonia", 41, 50, 0.95)
	e.Set("x", "med", "amoxicillin", 57, 68, 0.95)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Verify("x")
	}
}
