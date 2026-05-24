package llmstack

import (
	"testing"
)

func TestStreamWatchOpenCreatesSession(t *testing.T) {
	w := NewStreamWatcher()
	if err := w.OpenPublic("s1", StreamWatchConfigPublic{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Status("s1"); !ok {
		t.Fatal("status missing after open")
	}
}

func TestStreamWatchTokenOKBeforeSignals(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{MinTokens: 5})
	r, _ := w.Token("s1", "first")
	if r.Verdict != "ok" {
		t.Fatalf("verdict = %s", r.Verdict)
	}
}

func TestStreamWatchCycleStopsGeneration(t *testing.T) {
	w := NewStreamWatcher()
	// MinTokens 5, CycleThreshold 4 — once we feed 5 tokens then
	// 4 in-a-row of the same, it must stop.
	w.OpenPublic("s1", StreamWatchConfigPublic{MinTokens: 5, CycleThreshold: 4})
	// Warmup
	for _, tok := range []string{"a", "b", "c", "d", "e"} {
		w.Token("s1", tok)
	}
	// Now 4 in a row
	var last StreamWatchResult
	for i := 0; i < 4; i++ {
		last, _ = w.Token("s1", "the")
	}
	if last.Verdict != "stop" {
		t.Fatalf("cycle should stop: %+v", last)
	}
}

func TestStreamWatchSubsequentTokensStayStopped(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{MinTokens: 1, CycleThreshold: 3})
	w.Token("s1", "x")
	w.Token("s1", "the")
	w.Token("s1", "the")
	w.Token("s1", "the") // stop fires here
	r, _ := w.Token("s1", "anything")
	if r.Verdict != "stop" {
		t.Fatalf("after stop, subsequent token = %s", r.Verdict)
	}
}

func TestStreamWatchNGramLoopStops(t *testing.T) {
	w := NewStreamWatcher()
	// 3-gram repeats 4 times → stop
	w.OpenPublic("s1", StreamWatchConfigPublic{
		MinTokens: 5, NGram: 3, NGramRepeatThreshold: 4,
	})
	// Warmup
	for _, tok := range []string{"warm", "up", "phase", "now", "starting"} {
		w.Token("s1", tok)
	}
	// Feed X Y Z X Y Z X Y Z X Y Z (4× the 3-gram "X Y Z")
	var last StreamWatchResult
	for i := 0; i < 4; i++ {
		w.Token("s1", "X")
		w.Token("s1", "Y")
		last, _ = w.Token("s1", "Z")
	}
	if last.Verdict != "stop" {
		t.Fatalf("ngram loop should stop: %+v", last)
	}
}

func TestStreamWatchWarningForCycleBuilding(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{MinTokens: 3, CycleThreshold: 8})
	for _, tok := range []string{"a", "b", "c"} {
		w.Token("s1", tok)
	}
	// 4 in a row (half of cycle threshold)
	var last StreamWatchResult
	for i := 0; i < 4; i++ {
		last, _ = w.Token("s1", "the")
	}
	if last.Verdict != "warning" {
		t.Fatalf("half-cycle should warn: %+v", last)
	}
}

func TestStreamWatchNoSignalsBeforeMinTokens(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{MinTokens: 50, CycleThreshold: 3})
	// Even 10 of the same token is fine before MinTokens=50
	for i := 0; i < 10; i++ {
		r, _ := w.Token("s1", "x")
		if r.Verdict != "ok" {
			t.Fatalf("pre-min-tokens signal: %+v at %d", r, i)
		}
	}
}

func TestStreamWatchStatusReportsUniqueRatio(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{})
	for _, tok := range []string{"a", "b", "c", "a", "b"} {
		w.Token("s1", tok)
	}
	st, _ := w.Status("s1")
	if st.Length != 5 || st.UniqueTokens != 3 {
		t.Fatalf("status = %+v", st)
	}
	if st.UniqueRatio < 0.5 || st.UniqueRatio > 0.7 {
		t.Fatalf("ratio = %f", st.UniqueRatio)
	}
}

func TestStreamWatchCloseAndKeep(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{})
	w.Token("s1", "x")
	if !w.Close("s1", "upstream finished") {
		t.Fatal("close should report success")
	}
	st, ok := w.Status("s1")
	if !ok {
		t.Fatal("status should remain after close")
	}
	if st.ClosedReason != "upstream finished" {
		t.Fatalf("reason = %s", st.ClosedReason)
	}
}

func TestStreamWatchOpenResetsExisting(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{})
	w.Token("s1", "x")
	w.OpenPublic("s1", StreamWatchConfigPublic{})
	st, _ := w.Status("s1")
	if st.Length != 0 {
		t.Fatalf("open should reset: length=%d", st.Length)
	}
}

func TestStreamWatchSessionsSorted(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("zeta", StreamWatchConfigPublic{})
	w.OpenPublic("alpha", StreamWatchConfigPublic{})
	w.OpenPublic("mid", StreamWatchConfigPublic{})
	s := w.Sessions()
	if s[0] != "alpha" || s[2] != "zeta" {
		t.Fatalf("sessions = %v", s)
	}
}

func TestStreamWatchResetOne(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("a", StreamWatchConfigPublic{})
	w.OpenPublic("b", StreamWatchConfigPublic{})
	if w.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
}

func TestStreamWatchResetAll(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("a", StreamWatchConfigPublic{})
	w.OpenPublic("b", StreamWatchConfigPublic{})
	if w.Reset("ALL") != 2 {
		t.Fatal("ALL reset should drop 2")
	}
}

func TestStreamWatchRejectsBadInput(t *testing.T) {
	w := NewStreamWatcher()
	if err := w.OpenPublic("", StreamWatchConfigPublic{}); err == nil {
		t.Fatal("empty session_id should fail")
	}
	if err := w.OpenPublic("s", StreamWatchConfigPublic{NGram: 1}); err == nil {
		t.Fatal("NGram < 2 should fail")
	}
	if _, err := w.Token("ghost", "x"); err == nil {
		t.Fatal("token on unknown session should fail")
	}
	w.OpenPublic("a", StreamWatchConfigPublic{})
	if _, err := w.Token("a", ""); err == nil {
		t.Fatal("empty token should fail")
	}
}

func TestStreamWatchMaxLenRingRolls(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s1", StreamWatchConfigPublic{MaxLen: 5})
	for i := 0; i < 10; i++ {
		w.Token("s1", "tok-"+itoaBench(i))
	}
	st, _ := w.Status("s1")
	if st.Length != 5 {
		t.Fatalf("length should cap at 5, got %d", st.Length)
	}
}

func TestStreamWatchStatsAdvance(t *testing.T) {
	w := NewStreamWatcher()
	w.OpenPublic("s", StreamWatchConfigPublic{MinTokens: 1, CycleThreshold: 3})
	w.Token("s", "x")
	w.Token("s", "the")
	w.Token("s", "the")
	w.Token("s", "the") // stop
	st := w.Stats()
	if st.Sessions != 1 || st.TotalTokens != 4 || st.TotalStops != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkStreamWatchToken(b *testing.B) {
	w := NewStreamWatcher()
	w.OpenPublic("s", StreamWatchConfigPublic{MaxLen: 1000})
	tokens := []string{"the", "quick", "brown", "fox", "jumped", "over", "lazy", "dog"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Token("s", tokens[i%len(tokens)])
	}
}
