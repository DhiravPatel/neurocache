package llmstack

import (
	"testing"
)

func TestConfidenceRecordAndCurve(t *testing.T) {
	c := NewConfidenceCalibrator()
	// Use 0.75 (mid-bin) to avoid float-boundary ambiguity between
	// 6 and 7 when dividing 0.7/0.1.
	for i := 0; i < 100; i++ {
		actual := 0.0
		if i < 70 {
			actual = 1.0
		}
		c.Record("gpt-4", 0.75, actual)
	}
	curve, ok := c.Curve("gpt-4", 10)
	if !ok {
		t.Fatal("curve returned false")
	}
	// All samples land in bin 7 (0.7-0.8)
	if curve[7].Count != 100 {
		t.Fatalf("bin[7].Count = %d, want 100", curve[7].Count)
	}
	if curve[7].PredictedAvg != 0.75 {
		t.Fatalf("predicted_avg = %f, want 0.75", curve[7].PredictedAvg)
	}
	if curve[7].ActualRate < 0.69 || curve[7].ActualRate > 0.71 {
		t.Fatalf("actual_rate = %f, want ~0.7", curve[7].ActualRate)
	}
}

func TestConfidenceECEMeasuresMisCalibration(t *testing.T) {
	c := NewConfidenceCalibrator()
	// Severely miscalibrated: model predicts 0.9, actual is only 0.3
	for i := 0; i < 100; i++ {
		actual := 0.0
		if i < 30 {
			actual = 1.0
		}
		c.Record("bad", 0.9, actual)
	}
	ece, _, _ := c.ECE("bad", 10)
	// Gap is 0.9 - 0.3 = 0.6
	if ece < 0.55 || ece > 0.65 {
		t.Fatalf("ECE = %f, want ~0.6", ece)
	}
}

func TestConfidenceECEPerfectCalibration(t *testing.T) {
	c := NewConfidenceCalibrator()
	// Record across multiple bins, each perfectly calibrated
	for i := 0; i < 50; i++ {
		c.Record("good", 0.3, 0.0) // 0.3 predicted, 0% actual
	}
	for i := 0; i < 50; i++ {
		c.Record("good", 0.3, 0.0) // continue 0% — actually want 30%
	}
	// Hmm, can't do partial credit easily. Let me redo:
	c2 := NewConfidenceCalibrator()
	for i := 0; i < 100; i++ {
		actual := 0.0
		if i < 30 {
			actual = 1.0
		}
		c2.Record("good", 0.3, actual)
	}
	ece, _, _ := c2.ECE("good", 10)
	if ece > 0.05 {
		t.Fatalf("perfectly-calibrated bin should have low ECE, got %f", ece)
	}
}

func TestConfidenceCalibrateRemapsRawConfidence(t *testing.T) {
	c := NewConfidenceCalibrator()
	// Train: model says 0.85, actual is 0.40
	for i := 0; i < 100; i++ {
		actual := 0.0
		if i < 40 {
			actual = 1.0
		}
		c.Record("m", 0.85, actual)
	}
	calibrated, ok := c.Calibrate("m", 0.85, 10)
	if !ok {
		t.Fatal("calibrate returned false")
	}
	if calibrated < 0.35 || calibrated > 0.45 {
		t.Fatalf("calibrate(0.85) = %f, want ~0.40", calibrated)
	}
}

func TestConfidenceCalibrateFallsBackOnSparseBins(t *testing.T) {
	c := NewConfidenceCalibrator()
	// Only 3 samples — below minSamples=10
	for i := 0; i < 3; i++ {
		c.Record("m", 0.7, 0.0)
	}
	calibrated, _ := c.Calibrate("m", 0.7, 10)
	// Should fall back to raw 0.7
	if calibrated != 0.7 {
		t.Fatalf("sparse bin fallback failed: got %f, want 0.7 raw", calibrated)
	}
}

func TestConfidenceRejectsOutOfRange(t *testing.T) {
	c := NewConfidenceCalibrator()
	if err := c.Record("m", -0.1, 0.5); err == nil {
		t.Fatal("predicted < 0 should fail")
	}
	if err := c.Record("m", 1.5, 0.5); err == nil {
		t.Fatal("predicted > 1 should fail")
	}
	if err := c.Record("m", 0.5, 2); err == nil {
		t.Fatal("actual > 1 should fail")
	}
}

func TestConfidenceReset(t *testing.T) {
	c := NewConfidenceCalibrator()
	c.Record("m", 0.5, 1.0)
	if !c.Reset("m") {
		t.Fatal("reset should return true")
	}
	curve, _ := c.Curve("m", 10)
	total := 0
	for _, b := range curve {
		total += b.Count
	}
	if total != 0 {
		t.Fatalf("after reset, sample count = %d", total)
	}
}

func TestConfidenceCurveUnknownModel(t *testing.T) {
	c := NewConfidenceCalibrator()
	if _, ok := c.Curve("nope", 10); ok {
		t.Fatal("unknown model should return false")
	}
}

func TestConfidenceModelsAndStats(t *testing.T) {
	c := NewConfidenceCalibrator()
	c.Record("gpt-4", 0.5, 1.0)
	c.Record("claude", 0.5, 0.0)
	rows := c.Models()
	if len(rows) != 2 {
		t.Fatalf("models = %d", len(rows))
	}
	s := c.Stats()
	if s.TotalRecords != 2 || s.Models != 2 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestConfidenceRollingBuffer(t *testing.T) {
	// Buffer holds 10k samples; after 10001 records, oldest should be evicted
	c := NewConfidenceCalibrator()
	for i := 0; i < 11_000; i++ {
		c.Record("m", 0.5, 1.0)
	}
	curve, _ := c.Curve("m", 10)
	total := 0
	for _, b := range curve {
		total += b.Count
	}
	if total > 10_000 {
		t.Fatalf("buffer should cap at 10k, got %d", total)
	}
	if total < 9_000 {
		t.Fatalf("buffer should be near-full, got %d", total)
	}
}
