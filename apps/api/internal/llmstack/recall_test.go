package llmstack

import (
	"testing"
	"time"
)

func TestRecallRegisterScan(t *testing.T) {
	r := NewRecallStore()
	now := time.Now()
	// Register answer generated at T-1h
	r.Register("ans-1", "gpt-4o", "v3", "ada-002", now.Add(-time.Hour).UnixMilli())
	// Drift event covering [T-2h, T-30min] — answer falls inside
	r.Mark("swap", "model swap", now.Add(-2*time.Hour).UnixMilli(), now.Add(-30*time.Minute).UnixMilli(), 0, "")
	rows := r.Scan(0, 10, "")
	if len(rows) != 1 || rows[0].AnswerID != "ans-1" {
		t.Fatalf("scan = %+v", rows)
	}
}

func TestRecallOutOfWindowExcluded(t *testing.T) {
	r := NewRecallStore()
	now := time.Now()
	r.Register("ans-1", "m", "", "", now.UnixMilli())
	// Event entirely before the answer's generation
	r.Mark("old", "old change",
		now.Add(-2*time.Hour).UnixMilli(),
		now.Add(-time.Hour).UnixMilli(), 0, "")
	rows := r.Scan(0, 10, "")
	if len(rows) != 0 {
		t.Fatalf("should be empty: %+v", rows)
	}
}

func TestRecallConfidenceDecays(t *testing.T) {
	r := NewRecallStore()
	now := time.Now()
	r.Register("ans-1", "m", "", "", now.Add(-time.Hour).UnixMilli())
	// Event ended 10 half-lives ago → confidence near 0
	r.Mark("c", "x",
		now.Add(-2*time.Hour).UnixMilli(),
		now.Add(-time.Hour).UnixMilli(),
		1.0, "") // 1-second half-life; way past
	rows := r.Scan(0.5, 10, "")
	if len(rows) != 0 {
		t.Fatalf("decayed event should be filtered: %+v", rows)
	}
}

func TestRecallScopeFilter(t *testing.T) {
	r := NewRecallStore()
	now := time.Now()
	r.Register("a", "m", "v1", "", now.Add(-time.Hour).UnixMilli())
	r.Mark("prompt-swap", "x",
		now.Add(-2*time.Hour).UnixMilli(),
		now.Add(-30*time.Minute).UnixMilli(), 0, "prompt")
	// Scan with scope=model should miss the prompt event
	if rows := r.Scan(0, 10, "model"); len(rows) != 0 {
		t.Fatalf("scope filter failed: %+v", rows)
	}
}

func TestRecallForgetUnmark(t *testing.T) {
	r := NewRecallStore()
	r.Register("a", "m", "", "", 0)
	r.Mark("c", "x", 1, 2, 0, "")
	if r.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if r.Unmark("c") != 1 {
		t.Fatal("unmark c")
	}
}

func TestRecallRejectsBadInput(t *testing.T) {
	r := NewRecallStore()
	if err := r.Register("", "m", "", "", 0); err == nil {
		t.Fatal("empty id")
	}
	if err := r.Register("a", "", "", "", 0); err == nil {
		t.Fatal("empty model")
	}
	if err := r.Mark("", "x", 1, 2, 0, ""); err == nil {
		t.Fatal("empty change id")
	}
	if err := r.Mark("c", "", 1, 2, 0, ""); err == nil {
		t.Fatal("empty reason")
	}
	if err := r.Mark("c", "x", 2, 1, 0, ""); err == nil {
		t.Fatal("to < from")
	}
	if err := r.Mark("c", "x", 1, 2, 0, "bogus"); err == nil {
		t.Fatal("bad scope")
	}
}

func TestRecallStats(t *testing.T) {
	r := NewRecallStore()
	r.Register("a", "m", "", "", 0)
	r.Scan(0, 0, "")
	s := r.Stats()
	if s.TotalRegisters != 1 || s.TotalScans != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
