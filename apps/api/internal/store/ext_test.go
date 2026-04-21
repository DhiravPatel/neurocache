package store

import (
	"strconv"
	"testing"
)

// ─── bitmaps ───────────────────────────────────────────────────────────

func TestBitmapBasics(t *testing.T) {
	s := New()
	prev, err := s.SetBit("b", 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	if prev != 0 {
		t.Errorf("first SetBit returned %d", prev)
	}
	v, _ := s.GetBit("b", 7)
	if v != 1 {
		t.Errorf("GetBit 7 = %d", v)
	}
	v, _ = s.GetBit("b", 6)
	if v != 0 {
		t.Errorf("GetBit 6 = %d", v)
	}
	// Set another bit; byte should now hold bits 0 and 7
	s.SetBit("b", 0, 1)
	n, _ := s.BitCount("b", 0, -1, true)
	if n != 2 {
		t.Errorf("BitCount = %d", n)
	}
}

func TestBitOp(t *testing.T) {
	s := New()
	s.Set("a", "\xff\x0f", 0)
	s.Set("b", "\x0f\xff", 0)
	if _, err := s.BitOp("AND", "dst", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Get("dst")
	if v != "\x0f\x0f" {
		t.Errorf("AND = %q", v)
	}
	s.BitOp("OR", "dst", []string{"a", "b"})
	v, _ = s.Get("dst")
	if v != "\xff\xff" {
		t.Errorf("OR = %q", v)
	}
}

// ─── HyperLogLog ───────────────────────────────────────────────────────

func TestHLLApproxCount(t *testing.T) {
	s := New()
	for i := 0; i < 10000; i++ {
		s.PFAdd("hll", "item-"+strconv.Itoa(i))
	}
	n, err := s.PFCount("hll")
	if err != nil {
		t.Fatal(err)
	}
	// expect ~10000, allow 5% error (HLL dense guarantees <1%
	// typically, but we're CI-flakiness-safe)
	if n < 9500 || n > 10500 {
		t.Errorf("PFCount = %d, want ~10000", n)
	}
}

func TestHLLMerge(t *testing.T) {
	s := New()
	for i := 0; i < 500; i++ {
		s.PFAdd("a", strconv.Itoa(i))
		s.PFAdd("b", strconv.Itoa(i+250)) // overlap 250, union=750
	}
	if err := s.PFMerge("c", "a", "b"); err != nil {
		t.Fatal(err)
	}
	n, _ := s.PFCount("c")
	if n < 700 || n > 800 {
		t.Errorf("merged cardinality = %d, want ~750", n)
	}
}

// ─── streams ───────────────────────────────────────────────────────────

func TestStreamAddAndRange(t *testing.T) {
	s := New()
	id1, err := s.XAdd("s", "*", []string{"k", "v1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.XAdd("s", "*", []string{"k", "v2"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Errorf("auto IDs collided: %s", id1)
	}
	if n, _ := s.XLen("s"); n != 2 {
		t.Errorf("XLen = %d", n)
	}
	entries, _ := s.XRange("s", "-", "+", 0, false)
	if len(entries) != 2 {
		t.Errorf("XRange len = %d", len(entries))
	}
	if entries[0].Fields[1] != "v1" {
		t.Errorf("first entry = %+v", entries[0])
	}
}

func TestStreamTrim(t *testing.T) {
	s := New()
	for i := 0; i < 5; i++ {
		s.XAdd("s", "*", []string{"k", strconv.Itoa(i)}, 0)
	}
	removed, err := s.XTrim("s", 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Errorf("XTrim removed %d", removed)
	}
	if n, _ := s.XLen("s"); n != 2 {
		t.Errorf("length after trim = %d", n)
	}
}

func TestStreamRead(t *testing.T) {
	s := New()
	id1, _ := s.XAdd("s", "*", []string{"k", "v1"}, 0)
	id2, _ := s.XAdd("s", "*", []string{"k", "v2"}, 0)
	_ = id2
	out, err := s.XRead([]string{"s"}, []string{id1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out["s"]) != 1 {
		t.Errorf("XRead after id1 = %d entries", len(out["s"]))
	}
	if out["s"][0].Fields[1] != "v2" {
		t.Errorf("expected v2, got %+v", out["s"][0])
	}
}

// ─── geo ───────────────────────────────────────────────────────────────

func TestGeoAddAndDist(t *testing.T) {
	s := New()
	_, err := s.GeoAdd("cities",
		GeoAddEntry{Lon: -73.9857, Lat: 40.7484, Member: "nyc"}, // Empire State
		GeoAddEntry{Lon: -0.1276, Lat: 51.5074, Member: "lon"},  // London
	)
	if err != nil {
		t.Fatal(err)
	}
	d, ok, err := s.GeoDist("cities", "nyc", "lon", "km")
	if err != nil || !ok {
		t.Fatal("GeoDist failed")
	}
	// NYC <-> London ≈ 5570 km, allow 5% slop for encoding rounding
	if d < 5250 || d > 5850 {
		t.Errorf("distance = %v km", d)
	}
}

func TestGeoSearch(t *testing.T) {
	s := New()
	s.GeoAdd("cities",
		GeoAddEntry{Lon: -73.9857, Lat: 40.7484, Member: "nyc"},
		GeoAddEntry{Lon: -0.1276, Lat: 51.5074, Member: "lon"},
		GeoAddEntry{Lon: -73.965, Lat: 40.782, Member: "cp"}, // Central Park
	)
	hits, err := s.GeoSearch("cities", 40.75, -73.98, 10, "km", 0)
	if err != nil {
		t.Fatal(err)
	}
	// both NYC members should land; London is too far
	if len(hits) != 2 {
		t.Errorf("got %d hits: %+v", len(hits), hits)
	}
}

// ─── snapshot round-trip ──────────────────────────────────────────────

func TestExportRestore(t *testing.T) {
	s := New()
	s.Set("k", "v", 0)
	s.RPush("l", "a", "b")
	s.HSet("h", "f", "v")
	s.SAdd("st", "x", "y")
	s.ZAdd("z", ZPair{1, "m"})
	s.XAdd("x", "*", []string{"k", "v"}, 0)

	dump := s.Export()
	s2 := New()
	s2.Restore(dump)

	if v, _ := s2.Get("k"); v != "v" {
		t.Errorf("restored string = %q", v)
	}
	if n, _ := s2.LLen("l"); n != 2 {
		t.Errorf("restored list len = %d", n)
	}
	if v, ok, _ := s2.HGet("h", "f"); !ok || v != "v" {
		t.Errorf("restored hash = %q", v)
	}
	if ok, _ := s2.SIsMember("st", "x"); !ok {
		t.Error("restored set missing member")
	}
	if n, _ := s2.ZCard("z"); n != 1 {
		t.Errorf("restored zset = %d", n)
	}
	if n, _ := s2.XLen("x"); n != 1 {
		t.Errorf("restored stream = %d", n)
	}
}
