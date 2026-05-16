package llmstack

import (
	"math"
	"testing"
)

func TestJurySubmitAndUnanimousVerdict(t *testing.T) {
	j := NewJury()
	j.Submit("q1", "candA", "answer A text")
	j.Submit("q1", "candB", "answer B text")
	for _, judge := range []string{"gpt", "claude", "gemini"} {
		j.Vote("q1", judge, "candA", 1.0)
	}
	v, err := j.Verdict("q1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Winner != "candA" {
		t.Fatalf("winner = %s", v.Winner)
	}
	if v.WinnerText != "answer A text" {
		t.Fatalf("winner text = %s", v.WinnerText)
	}
	if math.Abs(v.Agreement-1.0) > 1e-9 {
		t.Fatalf("agreement should be 1.0, got %f", v.Agreement)
	}
	if v.JudgesN != 3 {
		t.Fatalf("judges = %d", v.JudgesN)
	}
}

func TestJurySplitVote(t *testing.T) {
	j := NewJury()
	j.Submit("q1", "candA", "A")
	j.Submit("q1", "candB", "B")
	j.Vote("q1", "gpt", "candA", 1.0)
	j.Vote("q1", "claude", "candA", 1.0)
	j.Vote("q1", "gemini", "candB", 1.0)
	v, _ := j.Verdict("q1")
	if v.Winner != "candA" {
		t.Fatalf("winner = %s", v.Winner)
	}
	if math.Abs(v.Agreement-2.0/3.0) > 1e-9 {
		t.Fatalf("agreement = %f, want 0.667", v.Agreement)
	}
}

func TestJuryConfidenceWeighting(t *testing.T) {
	j := NewJury()
	j.Submit("q", "A", "x")
	j.Submit("q", "B", "y")
	// Two low-confidence votes for A; one high-confidence for B
	j.Vote("q", "j1", "A", 0.30)
	j.Vote("q", "j2", "A", 0.30)
	j.Vote("q", "j3", "B", 0.99)
	v, _ := j.Verdict("q")
	if v.Winner != "B" {
		t.Fatalf("high-confidence single vote should win against two low-confidence: %s (score=%f)", v.Winner, v.WinnerScore)
	}
}

func TestJuryNoVotesEmptyWinner(t *testing.T) {
	j := NewJury()
	j.Submit("q", "A", "x")
	v, _ := j.Verdict("q")
	if v.Winner != "" {
		t.Fatalf("with no votes, winner should be empty: %s", v.Winner)
	}
	if v.CandidatesN != 1 {
		t.Fatalf("candidates n = %d", v.CandidatesN)
	}
}

func TestJuryTieBreakerAlphabetic(t *testing.T) {
	j := NewJury()
	j.Submit("q", "zeta", "z")
	j.Submit("q", "alpha", "a")
	j.Vote("q", "j1", "zeta", 1.0)
	j.Vote("q", "j2", "alpha", 1.0)
	v, _ := j.Verdict("q")
	if v.Winner != "alpha" {
		t.Fatalf("tie should resolve alphabetic to alpha, got %s", v.Winner)
	}
	if !v.TieBroken {
		t.Fatal("tie flag should be set when zeta lost on tiebreak")
	}
}

func TestJuryVoteRejectsBadInputs(t *testing.T) {
	j := NewJury()
	if err := j.Submit("", "c", "x"); err == nil {
		t.Fatal("empty question id should fail")
	}
	if err := j.Submit("q", "", "x"); err == nil {
		t.Fatal("empty candidate id should fail")
	}
	j.Submit("q", "c", "x")
	if err := j.Vote("q", "", "c", 1.0); err == nil {
		t.Fatal("empty judge id should fail")
	}
	if err := j.Vote("q", "j", "", 1.0); err == nil {
		t.Fatal("empty candidate id should fail")
	}
	if err := j.Vote("q", "j", "c", 1.5); err == nil {
		t.Fatal("confidence > 1 should fail")
	}
	if err := j.Vote("q", "j", "ghost", 1.0); err == nil {
		t.Fatal("unknown candidate should fail")
	}
	if err := j.Vote("missing-q", "j", "c", 1.0); err == nil {
		t.Fatal("unknown question should fail")
	}
}

func TestJuryJudgeOverridesPriorVote(t *testing.T) {
	j := NewJury()
	j.Submit("q", "A", "x")
	j.Submit("q", "B", "y")
	j.Vote("q", "gpt", "A", 1.0)
	j.Vote("q", "gpt", "B", 1.0) // changed mind
	v, _ := j.Verdict("q")
	if v.Winner != "B" {
		t.Fatalf("revote should land on B: %s", v.Winner)
	}
}

func TestJuryStatusListsAllCandidates(t *testing.T) {
	j := NewJury()
	j.Submit("q", "A", "x")
	j.Submit("q", "B", "y")
	j.Vote("q", "j", "A", 1.0)
	st, ok := j.Status("q")
	if !ok || len(st.Rows) != 2 {
		t.Fatalf("status rows = %d", len(st.Rows))
	}
	if st.Rows[0].CandidateID != "A" {
		t.Fatalf("top row = %s", st.Rows[0].CandidateID)
	}
}

func TestJuryListSorted(t *testing.T) {
	j := NewJury()
	j.Submit("zeta", "x", "a")
	j.Submit("alpha", "x", "a")
	j.Submit("mid", "x", "a")
	l := j.List()
	if l[0] != "alpha" || l[2] != "zeta" {
		t.Fatalf("list = %v", l)
	}
}

func TestJuryResetOne(t *testing.T) {
	j := NewJury()
	j.Submit("a", "x", "a")
	j.Submit("b", "x", "a")
	if j.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestJuryResetAll(t *testing.T) {
	j := NewJury()
	j.Submit("a", "x", "a")
	j.Submit("b", "x", "a")
	if j.Reset("ALL") != 2 {
		t.Fatal("reset ALL should drop 2")
	}
}

func TestJuryStatsAdvance(t *testing.T) {
	j := NewJury()
	j.Submit("q", "A", "x")
	j.Vote("q", "judge", "A", 1.0)
	j.Verdict("q")
	s := j.Stats()
	if s.Questions != 1 || s.TotalSubmits != 1 || s.TotalVotes != 1 || s.TotalVerdicts != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func BenchmarkJuryVote(b *testing.B) {
	j := NewJury()
	j.Submit("q", "A", "x")
	j.Submit("q", "B", "y")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j.Vote("q", "judge-many", "A", 1.0)
	}
}

func BenchmarkJuryVerdict(b *testing.B) {
	j := NewJury()
	for c := 0; c < 4; c++ {
		j.Submit("q", "cand-"+itoaBench(c), "answer")
	}
	for v := 0; v < 8; v++ {
		j.Vote("q", "judge-"+itoaBench(v), "cand-0", 0.9)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j.Verdict("q")
	}
}
