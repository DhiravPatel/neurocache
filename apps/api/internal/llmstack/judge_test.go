package llmstack

import (
	"testing"
)

func TestJudgeAddCaseAndScoreExact(t *testing.T) {
	j := NewJudgeSuite()
	if err := j.AddCase("p1", "c1", "ping?", "pong", CaseOpts{}); err != nil {
		t.Fatal(err)
	}
	r, ok := j.Score("p1", "c1", "pong", ScoreOpts{})
	if !ok {
		t.Fatal("score returned false")
	}
	if !r.Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestJudgeContainsGrader(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "input", "shipping address", CaseOpts{Grader: "contains"})
	r, _ := j.Score("p1", "c1", "Sure! Please send your shipping address to support.", ScoreOpts{})
	if !r.Pass {
		t.Fatalf("contains should match: %+v", r)
	}
	r2, _ := j.Score("p1", "c1", "Tell me your email instead.", ScoreOpts{})
	if r2.Pass {
		t.Fatalf("contains should not match: %+v", r2)
	}
}

func TestJudgeRegexGrader(t *testing.T) {
	j := NewJudgeSuite()
	if err := j.AddCase("p1", "c1", "what year?", `^Year:\s*\d{4}$`, CaseOpts{Grader: "regex"}); err != nil {
		t.Fatal(err)
	}
	r, _ := j.Score("p1", "c1", "Year: 2024", ScoreOpts{})
	if !r.Pass {
		t.Fatalf("regex should match: %+v", r)
	}
	r2, _ := j.Score("p1", "c1", "It was 2024.", ScoreOpts{})
	if r2.Pass {
		t.Fatal("regex should not match free-form text")
	}
}

func TestJudgeNumericWithinGrader(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "what's 1/3?", "0.33", CaseOpts{Grader: "numeric_within", Tolerance: 0.01})
	r, _ := j.Score("p1", "c1", "0.333", ScoreOpts{})
	if !r.Pass {
		t.Fatalf("0.333 within 0.01 of 0.33: %+v", r)
	}
	r2, _ := j.Score("p1", "c1", "0.5", ScoreOpts{})
	if r2.Pass {
		t.Fatalf("0.5 not within 0.01 of 0.33: %+v", r2)
	}
	r3, _ := j.Score("p1", "c1", "not a number", ScoreOpts{})
	if r3.Pass {
		t.Fatal("non-numeric should fail")
	}
}

func TestJudgeLLMGraderAcceptsCallerVerdict(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "summarize", "concise summary", CaseOpts{Grader: "llm"})
	r, _ := j.Score("p1", "c1", "<long summary>", ScoreOpts{LLMPass: true, LLMScore: 0.85})
	if !r.Pass || r.Score != 0.85 {
		t.Fatalf("llm grader should defer to caller: %+v", r)
	}
}

func TestJudgeRejectsBadGrader(t *testing.T) {
	j := NewJudgeSuite()
	if err := j.AddCase("p1", "c1", "", "", CaseOpts{Grader: "magic"}); err == nil {
		t.Fatal("expected error for unknown grader")
	}
}

func TestJudgeRejectsBadRegex(t *testing.T) {
	j := NewJudgeSuite()
	if err := j.AddCase("p1", "c1", "", "[unclosed", CaseOpts{Grader: "regex"}); err == nil {
		t.Fatal("expected error for bad regex")
	}
}

func TestJudgeHistoryAndPassrate(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "ping", "pong", CaseOpts{})
	for i := 0; i < 7; i++ {
		j.Score("p1", "c1", "pong", ScoreOpts{}) // pass
	}
	for i := 0; i < 3; i++ {
		j.Score("p1", "c1", "no", ScoreOpts{}) // fail
	}
	pr, ok := j.PassRate("p1", 0)
	if !ok {
		t.Fatal("passrate returned false")
	}
	if pr.Pass != 7 || pr.Fail != 3 {
		t.Fatalf("pass/fail = %d/%d", pr.Pass, pr.Fail)
	}
	if pr.PassRate < 0.69 || pr.PassRate > 0.71 {
		t.Fatalf("pass_rate = %f", pr.PassRate)
	}
	hist := j.History("p1", 5)
	if len(hist) != 5 {
		t.Fatalf("history limit not honored: got %d", len(hist))
	}
}

func TestJudgePassrateWindowed(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "ping", "pong", CaseOpts{})
	// First 5: all fail
	for i := 0; i < 5; i++ {
		j.Score("p1", "c1", "no", ScoreOpts{})
	}
	// Last 5: all pass
	for i := 0; i < 5; i++ {
		j.Score("p1", "c1", "pong", ScoreOpts{})
	}
	pr, _ := j.PassRate("p1", 5)
	if pr.PassRate != 1.0 {
		t.Fatalf("windowed passrate = %f, want 1.0 (all pass in last 5)", pr.PassRate)
	}
}

func TestJudgeRemoveCase(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "i", "e", CaseOpts{})
	if !j.RemoveCase("p1", "c1") {
		t.Fatal("remove should return true on existing case")
	}
	if j.RemoveCase("p1", "c1") {
		t.Fatal("remove should return false on missing case")
	}
}

func TestJudgeForgetPrompt(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "i", "e", CaseOpts{})
	if !j.Forget("p1") {
		t.Fatal("forget should return true on existing prompt")
	}
	if _, ok := j.PassRate("p1", 0); ok {
		t.Fatal("passrate should fail after forget")
	}
}

func TestJudgePromptIDsSorted(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("zebra", "c", "i", "e", CaseOpts{})
	j.AddCase("alpha", "c", "i", "e", CaseOpts{})
	j.AddCase("mango", "c", "i", "e", CaseOpts{})
	got := j.PromptIDs()
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("PromptIDs not sorted: %v", got)
		}
	}
}

func TestJudgeStatsAdvance(t *testing.T) {
	j := NewJudgeSuite()
	j.AddCase("p1", "c1", "i", "e", CaseOpts{})
	j.Score("p1", "c1", "e", ScoreOpts{})  // pass
	j.Score("p1", "c1", "no", ScoreOpts{}) // fail
	s := j.Stats()
	if s.TotalRuns != 2 || s.TotalPass != 1 || s.TotalFail != 1 {
		t.Fatalf("stats=%+v", s)
	}
}
