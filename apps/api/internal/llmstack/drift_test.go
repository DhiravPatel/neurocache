package llmstack

import (
	"testing"
)

func TestDriftOnTopicLowerThanOffTopic(t *testing.T) {
	// The right way to test drift is a relative check: on-topic
	// observations score LOWER than off-topic ones. Absolute
	// thresholds depend on the bag sizes and aren't meaningful.
	baseline := []string{
		"customers reporting login issues with Safari browser",
		"users cannot log in via Safari browser",
		"login is broken on Safari page crashes",
		"Safari users see login errors when authenticating",
	}

	d1 := NewDriftDetector()
	d1.SetBaseline("on", baseline, 100)
	for i := 0; i < 30; i++ {
		d1.Observe("on", "Safari login is broken users cannot authenticate")
	}
	onTopic, _ := d1.Score("on")

	d2 := NewDriftDetector()
	d2.SetBaseline("off", baseline, 100)
	for i := 0; i < 30; i++ {
		d2.Observe("off", "delivery driver did not arrive at the warehouse on time")
	}
	offTopic, _ := d2.Score("off")

	if onTopic.Score >= offTopic.Score {
		t.Fatalf("on-topic drift (%.4f) should be < off-topic drift (%.4f)",
			onTopic.Score, offTopic.Score)
	}
}

func TestDriftDetectsDivergence(t *testing.T) {
	d := NewDriftDetector()
	d.SetBaseline("support", []string{
		"Safari login is broken",
		"users cannot authenticate on Safari",
	}, 100)

	// Flood with totally different topic
	for i := 0; i < 60; i++ {
		d.Observe("support", "delivery driver did not arrive at the warehouse")
	}
	r, _ := d.Score("support")
	if r.Verdict == "stable" {
		t.Fatalf("should detect drift: score=%.4f verdict=%s", r.Score, r.Verdict)
	}
}

func TestDriftRejectsEmptySamples(t *testing.T) {
	d := NewDriftDetector()
	if err := d.SetBaseline("x", nil, 100); err == nil {
		t.Fatal("empty samples should fail")
	}
	if err := d.SetBaseline("", []string{"x"}, 100); err == nil {
		t.Fatal("empty tracker_id should fail")
	}
}

func TestDriftObserveUnknownTracker(t *testing.T) {
	d := NewDriftDetector()
	if _, ok := d.Observe("nope", "x"); ok {
		t.Fatal("observe on unknown tracker should return false")
	}
}

func TestDriftReset(t *testing.T) {
	d := NewDriftDetector()
	d.SetBaseline("x", []string{"baseline text"}, 100)
	for i := 0; i < 50; i++ {
		d.Observe("x", "different topic here")
	}
	if !d.Reset("x") {
		t.Fatal("reset should return true")
	}
	r, _ := d.Score("x")
	if r.Samples != 0 {
		t.Fatalf("after reset samples = %d, want 0", r.Samples)
	}
}

func TestDriftForget(t *testing.T) {
	d := NewDriftDetector()
	d.SetBaseline("x", []string{"baseline"}, 100)
	if !d.Forget("x") {
		t.Fatal("forget should return true")
	}
	if _, ok := d.Score("x"); ok {
		t.Fatal("score on forgotten tracker should fail")
	}
}

func TestDriftTrackersList(t *testing.T) {
	d := NewDriftDetector()
	d.SetBaseline("a", []string{"x"}, 100)
	d.SetBaseline("b", []string{"y"}, 100)
	rows := d.Trackers()
	if len(rows) != 2 {
		t.Fatalf("trackers = %d", len(rows))
	}
}

func TestDriftStatsAdvance(t *testing.T) {
	d := NewDriftDetector()
	d.SetBaseline("x", []string{"a"}, 100)
	d.Observe("x", "b")
	d.Score("x")
	s := d.Stats()
	if s.TotalBaselines != 1 || s.TotalObserves != 1 || s.TotalScores != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestDriftWindowEviction(t *testing.T) {
	d := NewDriftDetector()
	d.SetBaseline("x", []string{"baseline text here"}, 10)
	for i := 0; i < 100; i++ {
		d.Observe("x", "some text")
	}
	r, _ := d.Score("x")
	if r.Samples > 10 {
		t.Fatalf("samples = %d, want <=10 (window)", r.Samples)
	}
}
