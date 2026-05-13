package llmstack

import (
	"testing"
)

func TestVerifyExactStrategy(t *testing.T) {
	v := NewVerifyManager()
	for i := 0; i < 3; i++ {
		v.AddSample("q1", "42", nil)
	}
	v.AddSample("q1", "43", nil)
	v.AddSample("q1", "44", nil)

	r, ok := v.Consensus("q1", "exact")
	if !ok {
		t.Fatal("consensus returned false")
	}
	if r.Chosen != "42" {
		t.Fatalf("chosen = %s, want 42", r.Chosen)
	}
	if r.Confidence < 0.59 || r.Confidence > 0.61 {
		t.Fatalf("confidence = %f, want ~0.6", r.Confidence)
	}
	if r.SampleN != 5 {
		t.Fatalf("sample_n = %d", r.SampleN)
	}
	if len(r.Buckets) != 3 {
		t.Fatalf("buckets = %d, want 3", len(r.Buckets))
	}
}

func TestVerifyExactTrimsWhitespace(t *testing.T) {
	v := NewVerifyManager()
	v.AddSample("q1", "42 ", nil)
	v.AddSample("q1", " 42", nil)
	v.AddSample("q1", "42", nil)
	r, _ := v.Consensus("q1", "exact")
	if r.Confidence != 1.0 {
		t.Fatalf("trimmed samples should match: %+v", r)
	}
}

func TestVerifyMedoidStrategy(t *testing.T) {
	// Medoid: pick the sample most similar to all others
	v := NewVerifyManager()
	v.AddSample("q1", "The capital of France is Paris", nil)
	v.AddSample("q1", "Paris is the capital of France", nil)
	v.AddSample("q1", "France's capital is Paris", nil)
	v.AddSample("q1", "Quantum entanglement powers fridges", nil) // outlier

	r, _ := v.Consensus("q1", "medoid")
	if r.Chosen == "Quantum entanglement powers fridges" {
		t.Fatalf("outlier should not be medoid, got: %s", r.Chosen)
	}
	if r.Confidence < 0.1 {
		t.Fatalf("medoid confidence too low: %f", r.Confidence)
	}
}

func TestVerifyClusterStrategy(t *testing.T) {
	v := NewVerifyManager()
	// Two clusters: Paris-related (3 samples) vs weather (1 sample)
	v.AddSample("q1", "The capital of France is Paris", nil)
	v.AddSample("q1", "Paris is the capital of France", nil)
	v.AddSample("q1", "France's capital city is Paris", nil)
	v.AddSample("q1", "Today's weather is sunny", nil)

	r, _ := v.Consensus("q1", "cluster")
	if r.Confidence < 0.7 || r.Confidence > 0.76 {
		t.Fatalf("cluster confidence = %f, want ~0.75 (3 of 4)", r.Confidence)
	}
}

func TestVerifySingleSample(t *testing.T) {
	v := NewVerifyManager()
	v.AddSample("q1", "lonely", nil)
	r, _ := v.Consensus("q1", "exact")
	if r.Confidence != 1.0 {
		t.Fatalf("single sample should have confidence=1: %f", r.Confidence)
	}
	r2, _ := v.Consensus("q1", "medoid")
	if r2.Chosen != "lonely" {
		t.Fatalf("medoid of single sample should be itself")
	}
}

func TestVerifyConsensusUnknownQuery(t *testing.T) {
	v := NewVerifyManager()
	if _, ok := v.Consensus("nope", "exact"); ok {
		t.Fatal("consensus on unknown query should return false")
	}
}

func TestVerifyEmptySampleRejected(t *testing.T) {
	v := NewVerifyManager()
	if err := v.AddSample("q1", "", nil); err == nil {
		t.Fatal("empty sample should be rejected")
	}
	if err := v.AddSample("", "ok", nil); err == nil {
		t.Fatal("empty query_id should be rejected")
	}
}

func TestVerifyUnknownStrategy(t *testing.T) {
	v := NewVerifyManager()
	v.AddSample("q1", "x", nil)
	r, _ := v.Consensus("q1", "magic")
	if r.Confidence != 0 {
		t.Fatalf("unknown strategy should yield confidence=0, got %f", r.Confidence)
	}
}

func TestVerifySamplesPreservesOrder(t *testing.T) {
	v := NewVerifyManager()
	v.AddSample("q1", "first", nil)
	v.AddSample("q1", "second", nil)
	v.AddSample("q1", "third", nil)
	got := v.Samples("q1")
	if len(got) != 3 || got[0] != "first" || got[2] != "third" {
		t.Fatalf("samples not in insertion order: %v", got)
	}
}

func TestVerifyForgetClearsSamples(t *testing.T) {
	v := NewVerifyManager()
	v.AddSample("q1", "x", nil)
	if !v.Forget("q1") {
		t.Fatal("forget should return true")
	}
	if v.Samples("q1") != nil {
		t.Fatal("samples should be empty after forget")
	}
}

func TestVerifyStatsAdvance(t *testing.T) {
	v := NewVerifyManager()
	v.AddSample("q1", "x", nil)
	v.AddSample("q2", "y", nil)
	v.Consensus("q1", "exact")
	s := v.Stats()
	if s.TotalSamples != 2 || s.Queries != 2 || s.TotalConsensus != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
