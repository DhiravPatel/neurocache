package llmstack

import (
	"strconv"
	"testing"
)

func TestEntropyHealthy(t *testing.T) {
	e := NewEntropyMonitor()
	for i := 0; i < 50; i++ {
		e.Observe("p", "unique-"+strconv.Itoa(i))
	}
	r, _ := e.Report("p", 5)
	if r.Verdict != "HEALTHY" {
		t.Fatalf("diverse outputs should be HEALTHY: %+v", r)
	}
}

func TestEntropyCollapsedSingleMode(t *testing.T) {
	e := NewEntropyMonitor()
	for i := 0; i < 50; i++ {
		e.Observe("p", "same")
	}
	r, _ := e.Report("p", 5)
	if r.Verdict != "COLLAPSED" {
		t.Fatalf("single mode should be COLLAPSED: %+v", r)
	}
	if r.UniqueFraction > 0.05 {
		t.Fatalf("unique fraction = %f", r.UniqueFraction)
	}
}

func TestEntropyDegraded(t *testing.T) {
	e := NewEntropyMonitor()
	// 100 obs, 10 distinct → unique_fraction = 0.10 (DEGRADED, between 0.05 and 0.20)
	for i := 0; i < 100; i++ {
		e.Observe("p", "out-"+strconv.Itoa(i%10))
	}
	r, _ := e.Report("p", 5)
	if r.Verdict != "DEGRADED" {
		t.Fatalf("partial dedup should be DEGRADED: %+v", r)
	}
}

func TestEntropyInsufficient(t *testing.T) {
	e := NewEntropyMonitor()
	e.Observe("p", "x")
	r, _ := e.Report("p", 5)
	if r.Verdict != "INSUFFICIENT" {
		t.Fatalf("small n verdict = %s", r.Verdict)
	}
}

func TestEntropyTopModes(t *testing.T) {
	e := NewEntropyMonitor()
	for i := 0; i < 60; i++ {
		e.Observe("p", "A")
	}
	for i := 0; i < 20; i++ {
		e.Observe("p", "B")
	}
	for i := 0; i < 10; i++ {
		e.Observe("p", "C")
	}
	r, _ := e.Report("p", 3)
	if len(r.TopModes) != 3 || r.TopModes[0].Output != "A" {
		t.Fatalf("top modes: %+v", r.TopModes)
	}
}

func TestEntropyRollingWindow(t *testing.T) {
	e := NewEntropyMonitor()
	for i := 0; i < entropyWindowMax+1000; i++ {
		e.Observe("p", "o-"+strconv.Itoa(i))
	}
	r, _ := e.Report("p", 5)
	if r.N > entropyWindowMax {
		t.Fatalf("window not enforced: %d", r.N)
	}
}

func TestEntropyResetAndList(t *testing.T) {
	e := NewEntropyMonitor()
	e.Observe("a", "x")
	e.Observe("b", "x")
	if len(e.List()) != 2 {
		t.Fatal("list")
	}
	if e.Reset("a") != 1 {
		t.Fatal("reset a")
	}
	if e.Reset("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestEntropyRejectsBadInput(t *testing.T) {
	e := NewEntropyMonitor()
	if err := e.Observe("", "x"); err == nil {
		t.Fatal("empty pop")
	}
	if err := e.Observe("p", ""); err == nil {
		t.Fatal("empty output")
	}
}

func TestEntropyStats(t *testing.T) {
	e := NewEntropyMonitor()
	e.Observe("p", "x")
	e.Report("p", 5)
	s := e.Stats()
	if s.TotalObserves != 1 || s.TotalReports != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
