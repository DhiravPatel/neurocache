package llmstack

import "testing"

// TestGroundVerifyExactMatchGrounded — an answer that restates the context
// verbatim is maximally supported (cosine 1.0 against its own embedding).
func TestGroundVerifyExactMatchGrounded(t *testing.T) {
	g := NewGroundVerifier(384)
	ctx := []string{"The capital of France is Paris."}
	res := g.Verify("The capital of France is Paris.", ctx, 0.9)
	if !res.Grounded {
		t.Fatalf("verbatim answer not grounded; doc_score=%v", res.DocScore)
	}
	if res.DocScore < 0.99 {
		t.Fatalf("verbatim doc_score=%v, want ~1.0", res.DocScore)
	}
	if len(res.Unsupported) != 0 {
		t.Fatalf("verbatim answer has unsupported claims: %v", res.Unsupported)
	}
}

// TestGroundVerifyUnrelatedNotGrounded — an answer unrelated to the context
// scores well below a verbatim match and is flagged unsupported.
func TestGroundVerifyUnrelatedNotGrounded(t *testing.T) {
	g := NewGroundVerifier(384)
	ctx := []string{"The capital of France is Paris."}
	related := g.Verify("The capital of France is Paris.", ctx, 0.9)
	unrelated := g.Verify("Quantum entanglement correlates distant particles instantly.", ctx, 0.9)

	if unrelated.Grounded {
		t.Fatalf("unrelated answer marked grounded; doc_score=%v", unrelated.DocScore)
	}
	if len(unrelated.Unsupported) == 0 {
		t.Fatal("unrelated answer produced no unsupported claims")
	}
	if !(unrelated.DocScore < related.DocScore) {
		t.Fatalf("unrelated doc_score %v should be below related %v", unrelated.DocScore, related.DocScore)
	}
}

// TestGroundRequireNoContext — nothing can be grounded against an empty
// context, so REQUIRE fails and the fail counter advances.
func TestGroundRequireNoContext(t *testing.T) {
	g := NewGroundVerifier(384)
	res := g.Require("Some confident claim about nothing.", nil, 0.5)
	if res.Grounded {
		t.Fatal("claim grounded against empty context")
	}
	s := g.Stats()
	if s.TotalRequire != 1 || s.TotalFail != 1 || s.TotalPass != 0 {
		t.Fatalf("stats = %+v, want require=1 fail=1 pass=0", s)
	}
}

// TestGroundRequireFeedsRiskScore — the closed loop: a poorly-grounded answer
// yields a low doc_score, which debits a near-full RISK budget hard enough to
// enforce. This is the GROUND.REQUIRE → RISK.BUDGET.DEBIT wiring in miniature.
func TestGroundRequireFeedsRiskScore(t *testing.T) {
	g := NewGroundVerifier(384)
	r := NewRiskBudgets()
	r.Set("sess", 0.5, 1.0) // tight budget: one low-grounding answer exhausts it

	res := g.Require("Totally unsupported assertion.", []string{"Unrelated context about gardening."}, 0.9)
	if res.DocScore > 0.5 {
		t.Fatalf("unrelated answer scored %v, expected a low support score", res.DocScore)
	}
	// Low support → debit (1-doc)*weight ≥ 0.5 → balance ≤ 0 → enforce.
	dr, err := r.Debit("sess", res.DocScore, "ground.require")
	if err != nil {
		t.Fatal(err)
	}
	if !dr.Enforce {
		t.Fatalf("expected enforce after low-grounding debit; balance=%v doc=%v", dr.Balance, res.DocScore)
	}
}

// TestGroundVerifyEmptyAnswer — an empty answer is vacuously grounded.
func TestGroundVerifyEmptyAnswer(t *testing.T) {
	g := NewGroundVerifier(384)
	res := g.Verify("", []string{"anything"}, 0.5)
	if !res.Grounded || res.DocScore != 1.0 {
		t.Fatalf("empty answer = %+v, want grounded doc_score 1.0", res)
	}
}
