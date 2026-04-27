package introspect

import (
	"strconv"
	"testing"
)

func TestHotKeysRecordsEvents(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 10, SampleEvery: 1})
	for i := 0; i < 100; i++ {
		hk.Record("user:1")
	}
	for i := 0; i < 30; i++ {
		hk.Record("user:" + strconv.Itoa(i))
	}
	top := hk.Top(0)
	if len(top) == 0 {
		t.Fatal("Top should be non-empty after Record")
	}
	if top[0].Key != "user:1" {
		t.Fatalf("expected user:1 at top, got %s with count %d", top[0].Key, top[0].Count)
	}
}

func TestHotKeysSampleRateThins(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 10, SampleEvery: 100})
	for i := 0; i < 50; i++ {
		hk.Record("hot")
	}
	stats := hk.Stats()
	if stats.Events != 50 {
		t.Fatalf("Events = %d, want 50", stats.Events)
	}
	// At sample 1/100 with 50 events, no observations should have
	// landed (events 1..50 — none divisible by 100).
	if stats.Observations != 0 {
		t.Fatalf("Observations = %d, want 0 (50 events at sample 100)", stats.Observations)
	}
	// Push past 100 events → exactly one observation should land.
	for i := 0; i < 51; i++ {
		hk.Record("hot")
	}
	stats = hk.Stats()
	if stats.Observations != 1 {
		t.Fatalf("after crossing 100, observations = %d, want 1", stats.Observations)
	}
}

func TestHotKeysDisableSkipsRecord(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 10, SampleEvery: 1})
	hk.SetEnabled(false)
	for i := 0; i < 50; i++ {
		hk.Record("k")
	}
	if hk.Stats().Events != 0 {
		t.Fatal("Disabled tracker should not count events")
	}
}

func TestHotKeysThresholdGate(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 10, SampleEvery: 1})
	for i := 0; i < 5; i++ {
		hk.Record("low")
	}
	for i := 0; i < 50; i++ {
		hk.Record("high")
	}
	hk.SetThreshold(20)
	top := hk.Top(0)
	if len(top) != 1 || top[0].Key != "high" {
		t.Fatalf("threshold should leave only 'high', got %v", top)
	}
}

func TestHotKeysReset(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 10, SampleEvery: 1})
	for i := 0; i < 20; i++ {
		hk.Record("x")
	}
	hk.Reset()
	if hk.Stats().Events != 0 || len(hk.Top(0)) != 0 {
		t.Fatal("Reset should clear heap and event counter")
	}
}

func TestHotKeysSetKResets(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 5, SampleEvery: 1})
	hk.Record("a")
	hk.SetK(50)
	if hk.Stats().K != 50 {
		t.Fatalf("after SetK, stats.K = %d, want 50", hk.Stats().K)
	}
	if len(hk.Top(0)) != 0 {
		t.Fatal("SetK should reset the heap")
	}
}

func TestHotKeysEmptyKeyIgnored(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 5, SampleEvery: 1})
	hk.Record("")
	if hk.Stats().Events != 0 {
		t.Fatal("empty key should be ignored (no Events bump)")
	}
}

func TestHotKeysSampleZeroDisables(t *testing.T) {
	hk := NewHotKeys(HotKeysOptions{K: 5, SampleEvery: 1})
	hk.SetSampleRate(0)
	if hk.Enabled() {
		t.Fatal("SetSampleRate(0) should disable the tracker")
	}
	for i := 0; i < 10; i++ {
		hk.Record("k")
	}
	if hk.Stats().Events != 0 {
		t.Fatal("disabled tracker should not record")
	}
}
