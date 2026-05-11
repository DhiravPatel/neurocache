package llmstack

import (
	"strings"
	"testing"
)

func TestGroundCheckExactMatch(t *testing.T) {
	g := NewGroundChecker()
	src := "The Eiffel Tower is in Paris and stands 330 meters tall."
	out := "The Eiffel Tower is in Paris."
	r := g.Check(out, []string{src})
	if r.Verdict != "accept" {
		t.Fatalf("verdict = %s, want accept (doc_score=%.2f)", r.Verdict, r.DocScore)
	}
	if len(r.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(r.Claims))
	}
	if r.Claims[0].BestSource != 0 {
		t.Fatalf("best_source = %d, want 0", r.Claims[0].BestSource)
	}
}

func TestGroundCheckHallucinationFullyFabricated(t *testing.T) {
	g := NewGroundChecker()
	src := "Snowboards arrived in retail stores during the late 1980s."
	out := "Quantum entanglement powers our refrigerators."
	r := g.Check(out, []string{src})
	if r.Verdict != "reject" {
		t.Fatalf("verdict = %s, want reject (doc_score=%.4f)", r.Verdict, r.DocScore)
	}
}

func TestGroundCheckPartialOverlapIsGray(t *testing.T) {
	// Partial topic overlap (subject mentioned, predicate fabricated)
	// should land in the gray zone — apps escalate to an LLM judge.
	g := NewGroundChecker()
	src := "The Eiffel Tower is in Paris and stands 330 meters tall."
	out := "The Eiffel Tower was built by aliens in 1492."
	r := g.Check(out, []string{src})
	if r.Verdict != "gray" {
		t.Fatalf("verdict = %s, want gray (doc_score=%.4f)", r.Verdict, r.DocScore)
	}
}

func TestGroundCheckNoSources(t *testing.T) {
	g := NewGroundChecker()
	r := g.Check("This is something.", nil)
	if r.Verdict != "reject" {
		t.Fatalf("verdict = %s, want reject when no sources", r.Verdict)
	}
}

func TestGroundCheckEmptyOutput(t *testing.T) {
	g := NewGroundChecker()
	r := g.Check("", []string{"Some source."})
	if r.Verdict != "accept" {
		t.Fatalf("verdict = %s, want accept on empty output", r.Verdict)
	}
}

func TestGroundCheckWorstClaimDragsDown(t *testing.T) {
	g := NewGroundChecker()
	src := "The cat sat on the mat. The mat was red."
	out := "The cat sat on the mat. The cat invented the wheel in 1873."
	r := g.Check(out, []string{src})
	if r.Verdict == "accept" {
		t.Fatal("worst-claim policy should prevent accept when one claim is fabricated")
	}
	if len(r.Claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(r.Claims))
	}
	// Find the bad claim
	worst := SortedClaimsByScore(r.Claims)[0]
	if !strings.Contains(worst.Claim, "wheel") {
		t.Fatalf("worst claim should be about the wheel, got %q", worst.Claim)
	}
}

func TestGroundCheckPicksBestSource(t *testing.T) {
	g := NewGroundChecker()
	srcs := []string{
		"Snowboards are sold in winter.",
		"The Pacific Ocean is the largest body of water on Earth.",
	}
	r := g.Check("The Pacific Ocean is the largest body of water on Earth.",
		srcs)
	if len(r.Claims) != 1 {
		t.Fatalf("claims = %d", len(r.Claims))
	}
	if r.Claims[0].BestSource != 1 {
		t.Fatalf("best_source = %d, want 1", r.Claims[0].BestSource)
	}
}

func TestGroundCheckThresholdsConfig(t *testing.T) {
	g := NewGroundChecker()
	g.SetThresholds(0.9, 0.8) // very strict
	src := "The Eiffel Tower is in Paris."
	out := "The Eiffel Tower is in Paris."
	r := g.Check(out, []string{src})
	if r.Verdict != "accept" {
		t.Fatalf("identical text should still accept under strict thresholds, got %s (score=%.4f)",
			r.Verdict, r.DocScore)
	}
	th := g.CurrentThresholds()
	if th.OK != 0.9 || th.Bad != 0.8 {
		t.Fatalf("thresholds not applied: %+v", th)
	}
}

func TestGroundCheckStatsAdvance(t *testing.T) {
	g := NewGroundChecker()
	g.Check("The dog barked.", []string{"The dog barked loudly at noon."}) // accept
	g.Check("The cat invented physics.", []string{"The dog barked."})       // reject
	s := g.Stats()
	if s.TotalChecks != 2 {
		t.Fatalf("checks = %d", s.TotalChecks)
	}
	if s.TotalAccept < 1 || s.TotalReject < 1 {
		t.Fatalf("expected ≥1 accept and ≥1 reject, got %+v", s)
	}
}

func TestGroundSplitClaims(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"One sentence.", 1},
		{"First. Second. Third!", 3},
		{"A\nB\nC", 3},
		{"Question? Answer.", 2},
		{"   ", 0},
	}
	for _, c := range cases {
		got := splitClaims(c.in)
		if len(got) != c.want {
			t.Errorf("splitClaims(%q) = %d, want %d (got %v)", c.in, len(got), c.want, got)
		}
	}
}
