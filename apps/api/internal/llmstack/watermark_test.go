package llmstack

import (
	"strings"
	"testing"
)

func TestWatermarkScoreVeryShortText(t *testing.T) {
	w := NewWatermarkDetector()
	r := w.Score("hi")
	if r.Verdict != "unclear" {
		t.Fatalf("very short text should be unclear: %+v", r)
	}
}

func TestWatermarkScoreHumanText(t *testing.T) {
	w := NewWatermarkDetector()
	text := "ok so this is just a normal message from a real person typing fast. " +
		"sorry for the typos lol, the kids are screaming. hope everyone is good. " +
		"will follow up tomorrow morning when i'm back at my desk."
	r := w.Score(text)
	if r.Verdict == "ai" {
		t.Fatalf("plain human text scored as AI: %+v", r)
	}
}

func TestWatermarkScoreAITypicalText(t *testing.T) {
	w := NewWatermarkDetector()
	text := `Navigating the intricate tapestry of modern software development requires a
comprehensive understanding of multiple paradigms. Moreover, the holistic approach
to system design facilitates a robust framework — one that delves into the realm
of distributed computing while leveraging the unparalleled capabilities of cloud
infrastructure. Furthermore, the underpinning architecture showcases a plethora
of intricate optimizations. Crucially, this paradigm shift underscores the
importance of a myriad of considerations.`
	r := w.Score(text)
	if r.Verdict != "ai" {
		t.Fatalf("AI-typical text should be detected: score=%.4f verdict=%s",
			r.Score, r.Verdict)
	}
}

func TestWatermarkBulletDensity(t *testing.T) {
	w := NewWatermarkDetector()
	text := `Here is a comprehensive overview:
- First point about the matter
- Second point with additional detail
- Third point covering more ground
- Fourth point with another consideration
- Fifth point summarizing the topic`
	r := w.Score(text)
	// Find bullet_density signal
	var bullet *ScoreSignal
	for i := range r.Signals {
		if r.Signals[i].Name == "bullet_density" {
			bullet = &r.Signals[i]
		}
	}
	if bullet == nil || bullet.Contribution < 0.8 {
		t.Fatalf("bullet density should be high: %+v", bullet)
	}
}

func TestWatermarkEmDashDensity(t *testing.T) {
	w := NewWatermarkDetector()
	text := strings.Repeat("normal phrase — followed by em dash ", 10)
	r := w.Score(text)
	var emDash *ScoreSignal
	for i := range r.Signals {
		if r.Signals[i].Name == "em_dash_density" {
			emDash = &r.Signals[i]
		}
	}
	if emDash == nil || emDash.Contribution < 0.5 {
		t.Fatalf("em dash density should fire: %+v", emDash)
	}
}

func TestWatermarkCustomPatternRaisesScore(t *testing.T) {
	w := NewWatermarkDetector()
	textPlain := "I cannot answer that question. I must remind you of my limitations here. " +
		strings.Repeat("Here is what I can say about the topic at hand. ", 5)
	plain := w.Score(textPlain)

	if err := w.AddPattern("ai-signature", "(?i)as an ai", 1.0); err != nil {
		t.Fatal(err)
	}
	textWith := "As an AI, I cannot answer that. As an AI, I must remind you of my limitations. " +
		strings.Repeat("As an AI, here is what I can say about the topic at hand. ", 5)
	with := w.Score(textWith)

	if with.Score <= plain.Score {
		t.Fatalf("custom pattern should raise score above plain text: plain=%.4f with=%.4f",
			plain.Score, with.Score)
	}
}

func TestWatermarkBadPatternRejected(t *testing.T) {
	w := NewWatermarkDetector()
	if err := w.AddPattern("bad", "[unclosed", 1.0); err == nil {
		t.Fatal("bad regex should fail")
	}
}

func TestWatermarkPatternRemove(t *testing.T) {
	w := NewWatermarkDetector()
	w.AddPattern("p1", "test", 0.5)
	if !w.RemovePattern("p1") {
		t.Fatal("remove should return true")
	}
	if w.RemovePattern("p1") {
		t.Fatal("remove on missing should return false")
	}
}

func TestWatermarkStatsAdvance(t *testing.T) {
	w := NewWatermarkDetector()
	w.Score("plain human text here, hi how are you doing today thanks")
	w.Score("the intricate tapestry of comprehensive paradigms moreover delves into the realm")
	s := w.Stats()
	if s.TotalScores != 2 {
		t.Fatalf("scores = %d", s.TotalScores)
	}
}

func TestWatermarkPatternList(t *testing.T) {
	w := NewWatermarkDetector()
	w.AddPattern("a", "foo", 0.5)
	w.AddPattern("b", "bar", -0.3)
	rows := w.Patterns()
	if len(rows) != 2 {
		t.Fatalf("patterns = %d", len(rows))
	}
}
