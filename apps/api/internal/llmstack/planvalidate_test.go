package llmstack

import (
	"testing"
)

func TestPlanValidateEmptyIsValid(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	r, _ := v.Check("p1", false)
	if !r.Valid {
		t.Fatalf("empty plan not valid: %+v", r)
	}
}

func TestPlanValidateLinearDAG(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "s1", nil, nil, []string{"out"})
	v.AddStep("p1", "s2", nil,
		map[string]string{"x": "step:s1.out"}, []string{"y"})
	v.AddStep("p1", "s3", nil,
		map[string]string{"x": "step:s2.y"}, nil)
	r, _ := v.Check("p1", false)
	if !r.Valid {
		t.Fatalf("linear DAG flagged: %+v", r)
	}
}

func TestPlanValidateCycleCaught(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "a", []string{"b"}, nil, []string{"x"})
	v.AddStep("p1", "b", []string{"a"}, nil, []string{"y"})
	r, _ := v.Check("p1", false)
	if r.Valid {
		t.Fatalf("cycle not caught: %+v", r)
	}
	foundCycle := false
	for _, iss := range r.Issues {
		if iss.Code == "cycle" {
			foundCycle = true
		}
	}
	if !foundCycle {
		t.Fatalf("issues missing cycle code: %+v", r.Issues)
	}
}

func TestPlanValidateUnknownDep(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "s1", []string{"ghost"}, nil, nil)
	r, _ := v.Check("p1", false)
	if r.Valid {
		t.Fatal("unknown dep not caught")
	}
	foundUnknown := false
	for _, iss := range r.Issues {
		if iss.Code == "unknown-dep" && iss.StepID == "s1" {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatalf("unknown-dep missing: %+v", r.Issues)
	}
}

func TestPlanValidateUnknownOutputField(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "s1", nil, nil, []string{"out_a"})
	v.AddStep("p1", "s2", nil, map[string]string{
		"x": "step:s1.nonexistent",
	}, nil)
	r, _ := v.Check("p1", false)
	if r.Valid {
		t.Fatal("unknown output field not caught")
	}
	foundField := false
	for _, iss := range r.Issues {
		if iss.Code == "unknown-output" && iss.StepID == "s2" {
			foundField = true
		}
	}
	if !foundField {
		t.Fatalf("unknown-output missing: %+v", r.Issues)
	}
}

func TestPlanValidateLiteralInputsIgnored(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "s1", nil, map[string]string{
		"x": "literal",
		"y": "literal:hardcoded value",
	}, nil)
	r, _ := v.Check("p1", false)
	if !r.Valid {
		t.Fatalf("literal inputs flagged: %+v", r.Issues)
	}
}

func TestPlanValidateUnreachableWarning(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "orphan", nil, nil, []string{"out"})
	v.AddStep("p1", "real_end", nil, nil, nil)
	r, _ := v.Check("p1", false)
	// orphan produces "out" that nothing consumes, but it's not the
	// final step in insertion order
	found := false
	for _, iss := range r.Issues {
		if iss.Code == "unreachable" && iss.StepID == "orphan" && iss.Level == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unreachable warning missing: %+v", r.Issues)
	}
	if !r.Valid {
		t.Fatal("warning should not invalidate plan in non-strict mode")
	}
}

func TestPlanValidateUnreachableStrictInvalidates(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "orphan", nil, nil, []string{"out"})
	v.AddStep("p1", "real_end", nil, nil, nil)
	r, _ := v.Check("p1", true)
	if r.Valid {
		t.Fatal("strict mode should invalidate on unreachable")
	}
}

func TestPlanValidateFinalStepNotMarkedUnreachable(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "s1", nil, nil, []string{"a"})
	v.AddStep("p1", "final", nil, map[string]string{"x": "step:s1.a"}, []string{"result"})
	r, _ := v.Check("p1", false)
	for _, iss := range r.Issues {
		if iss.Code == "unreachable" && iss.StepID == "final" {
			t.Fatal("final step incorrectly flagged unreachable")
		}
	}
}

func TestPlanValidateImplicitDependencyViaInput(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	// No explicit DEPS, just an input referencing another step
	v.AddStep("p1", "a", nil, nil, []string{"out"})
	v.AddStep("p1", "b", nil, map[string]string{"x": "step:a.out"}, nil)
	r, _ := v.Check("p1", false)
	if !r.Valid {
		t.Fatalf("implicit deps via input should be fine: %+v", r)
	}
}

func TestPlanValidateImplicitCycle(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "a", nil, map[string]string{"x": "step:b.out"}, []string{"out"})
	v.AddStep("p1", "b", nil, map[string]string{"x": "step:a.out"}, []string{"out"})
	r, _ := v.Check("p1", false)
	if r.Valid {
		t.Fatal("implicit cycle through inputs should be caught")
	}
}

func TestPlanValidateStatusReturnsStructure(t *testing.T) {
	v := NewPlanValidator()
	v.New("p1")
	v.AddStep("p1", "s1", []string{"s0"}, map[string]string{"x": "literal"}, []string{"y"})
	rows, ok := v.Status("p1")
	if !ok || len(rows) != 1 {
		t.Fatalf("status = %+v", rows)
	}
	if rows[0].Inputs["x"] != "literal" {
		t.Fatalf("inputs lost: %+v", rows[0])
	}
}

func TestPlanValidateListSorted(t *testing.T) {
	v := NewPlanValidator()
	v.New("zeta")
	v.New("alpha")
	v.New("mid")
	l := v.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestPlanValidateDropOne(t *testing.T) {
	v := NewPlanValidator()
	v.New("a")
	v.New("b")
	if v.Drop("a") != 1 {
		t.Fatal("drop a should remove 1")
	}
}

func TestPlanValidateDropAll(t *testing.T) {
	v := NewPlanValidator()
	v.New("a")
	v.New("b")
	if v.Drop("ALL") != 2 {
		t.Fatal("ALL drop should remove 2")
	}
}

func TestPlanValidateRejectsBadInput(t *testing.T) {
	v := NewPlanValidator()
	if err := v.New(""); err == nil {
		t.Fatal("empty plan_id should fail")
	}
	if err := v.AddStep("", "s", nil, nil, nil); err == nil {
		t.Fatal("empty plan_id should fail")
	}
	if err := v.AddStep("ghost", "s", nil, nil, nil); err == nil {
		t.Fatal("unknown plan_id should fail")
	}
}

func TestPlanValidateStatsAdvance(t *testing.T) {
	v := NewPlanValidator()
	v.New("p")
	v.AddStep("p", "s1", nil, nil, nil)
	v.Check("p", false)
	st := v.Stats()
	if st.Plans != 1 || st.TotalChecks != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkPlanValidateCheck20Steps(b *testing.B) {
	v := NewPlanValidator()
	v.New("p")
	for i := 0; i < 20; i++ {
		id := "s" + itoaBench(i)
		var deps []string
		if i > 0 {
			deps = []string{"s" + itoaBench(i-1)}
		}
		v.AddStep("p", id, deps, nil, []string{"out"})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.Check("p", false)
	}
}
