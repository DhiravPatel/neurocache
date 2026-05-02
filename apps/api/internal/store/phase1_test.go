package store

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

// ── ZMSCORE ───────────────────────────────────────────────────────

func TestZMScoreParallelHits(t *testing.T) {
	s := New()
	if _, err := s.ZAdd("z", ZPair{1, "a"}, ZPair{2, "b"}, ZPair{3, "c"}); err != nil {
		t.Fatal(err)
	}
	scores, hits, err := s.ZMScore("z", "a", "missing", "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits len=%d", len(hits))
	}
	if !hits[0] || hits[1] || !hits[2] {
		t.Fatalf("hit pattern wrong: %v", hits)
	}
	if scores[0] != 1 || scores[2] != 3 {
		t.Fatalf("scores wrong: %v", scores)
	}
}

func TestZMScoreMissingKey(t *testing.T) {
	s := New()
	scores, hits, err := s.ZMScore("nope", "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0] || hits[1] {
		t.Fatalf("missing-key result wrong: %v %v", scores, hits)
	}
}

// ── ZRANDMEMBER ───────────────────────────────────────────────────

func TestZRandMemberSingleAndCount(t *testing.T) {
	s := New()
	for i := 0; i < 5; i++ {
		s.ZAdd("z", ZPair{float64(i), "m" + strconv.Itoa(i)})
	}
	// single-member form
	members, _, ok, err := s.ZRandMember("z", 0, false)
	if err != nil || !ok || len(members) != 1 {
		t.Fatalf("single form: ok=%v len=%d err=%v", ok, len(members), err)
	}
	// count > 0 — unique members capped at length
	members, _, ok, _ = s.ZRandMember("z", 100, false)
	if !ok || len(members) != 5 {
		t.Fatalf("expected 5 unique members, got %d", len(members))
	}
	// count < 0 — sample with replacement, exactly |count|
	members, _, ok, _ = s.ZRandMember("z", -10, false)
	if !ok || len(members) != 10 {
		t.Fatalf("with-replacement count = %d", len(members))
	}
	// WITHSCORES
	members, scores, _, _ := s.ZRandMember("z", 3, true)
	if len(scores) != len(members) {
		t.Fatalf("withscores mismatch: %d vs %d", len(members), len(scores))
	}
}

// ── ZREMRANGEBY{RANK,SCORE,LEX} ───────────────────────────────────

func TestZRemRangeByRank(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.ZAdd("z", ZPair{float64(i), strconv.Itoa(i)})
	}
	n, err := s.ZRemRangeByRank("z", 0, 2) // remove the 3 lowest scores
	if err != nil || n != 3 {
		t.Fatalf("removed=%d err=%v", n, err)
	}
	card, _ := s.ZCard("z")
	if card != 7 {
		t.Fatalf("card after rank rem = %d", card)
	}
}

func TestZRemRangeByScore(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.ZAdd("z", ZPair{float64(i), strconv.Itoa(i)})
	}
	n, err := s.ZRemRangeByScore("z", "(2", "5") // exclusive 2, inclusive 5 → {3,4,5}
	if err != nil || n != 3 {
		t.Fatalf("removed=%d err=%v", n, err)
	}
}

func TestZRemRangeByLex(t *testing.T) {
	s := New()
	// every member shares score 0 — required for lex semantics
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		s.ZAdd("z", ZPair{0, m})
	}
	n, err := s.ZRemRangeByLex("z", "[b", "(d")
	if err != nil || n != 2 { // b and c
		t.Fatalf("removed=%d err=%v", n, err)
	}
	card, _ := s.ZCard("z")
	if card != 3 {
		t.Fatalf("card after lex rem = %d", card)
	}
}

// ── LMOVE ─────────────────────────────────────────────────────────

func TestLMoveAllDirections(t *testing.T) {
	s := New()
	s.RPush("src", "1", "2", "3", "4")
	// LEFT → RIGHT (head pop, tail push)
	v, ok, _ := s.LMove("src", "dst", false, true)
	if !ok || v != "1" {
		t.Fatalf("LEFT→RIGHT: v=%q ok=%v", v, ok)
	}
	// RIGHT → LEFT (tail pop, head push)
	v, ok, _ = s.LMove("src", "dst", true, false)
	if !ok || v != "4" {
		t.Fatalf("RIGHT→LEFT: v=%q ok=%v", v, ok)
	}
	got, _ := s.LRange("dst", 0, -1)
	if got[0] != "4" || got[len(got)-1] != "1" {
		t.Fatalf("dst layout wrong: %v", got)
	}
}

func TestLMoveSelfRotate(t *testing.T) {
	s := New()
	s.RPush("k", "a", "b", "c")
	// rotate tail → head
	v, _, _ := s.LMove("k", "k", true, false)
	if v != "c" {
		t.Fatalf("rotate v=%q", v)
	}
	got, _ := s.LRange("k", 0, -1)
	if got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("rotated layout wrong: %v", got)
	}
}

func TestLMoveMissingSource(t *testing.T) {
	s := New()
	v, ok, err := s.LMove("nope", "dst", false, false)
	if err != nil || ok || v != "" {
		t.Fatalf("missing src: v=%q ok=%v err=%v", v, ok, err)
	}
}

// ── TOUCH / EXPIRETIME ────────────────────────────────────────────

func TestTouchUpdatesLastRead(t *testing.T) {
	s := New()
	s.Set("a", "v", 0)
	s.Set("b", "v", 0)
	// step LastRead back so we can detect the bump
	shA := s.shardForKey("a")
	shA.mu.Lock()
	shA.data["a"].LastRead = time.Now().Add(-time.Hour)
	shA.mu.Unlock()
	n := s.Touch("a", "b", "missing")
	if n != 2 {
		t.Fatalf("touch count = %d", n)
	}
	shA.mu.RLock()
	if time.Since(shA.data["a"].LastRead) > time.Second {
		t.Fatalf("a LastRead not refreshed")
	}
	shA.mu.RUnlock()
}

func TestExpireTimeAndPExpireTime(t *testing.T) {
	s := New()
	if got := s.ExpireTime("missing"); got != -2 {
		t.Fatalf("missing-key EXPIRETIME = %d, want -2", got)
	}
	s.Set("k", "v", 0)
	if got := s.ExpireTime("k"); got != -1 {
		t.Fatalf("no-ttl EXPIRETIME = %d, want -1", got)
	}
	s.Expire("k", 60*time.Second)
	got := s.ExpireTime("k")
	if got < time.Now().Unix() {
		t.Fatalf("EXPIRETIME = %d (now=%d)", got, time.Now().Unix())
	}
	gotMs := s.PExpireTime("k")
	if gotMs < time.Now().UnixMilli() {
		t.Fatalf("PEXPIRETIME = %d", gotMs)
	}
}

// ── GEOSEARCHSTORE ────────────────────────────────────────────────

func TestGeoSearchStorePreservesGeohash(t *testing.T) {
	s := New()
	s.GeoAdd("geo",
		GeoAddEntry{Lon: -122.4194, Lat: 37.7749, Member: "sf"},
		GeoAddEntry{Lon: -73.9857, Lat: 40.7484, Member: "ny"},
		GeoAddEntry{Lon: 2.3522, Lat: 48.8566, Member: "paris"},
	)
	// 1000km circle around SF — should pick up sf only
	n, err := s.GeoSearchStore("near-sf", "geo", 37.7749, -122.4194, 1000, "km", 0, false)
	if err != nil || n != 1 {
		t.Fatalf("store cardinality = %d err=%v", n, err)
	}
	// verify the score matches the original geohash for sf
	srcSc, _, _ := s.ZScore("geo", "sf")
	dstSc, _, _ := s.ZScore("near-sf", "sf")
	if srcSc != dstSc {
		t.Fatalf("default mode should preserve geohash: src=%v dst=%v", srcSc, dstSc)
	}
}

func TestGeoSearchStoreStoreDistMode(t *testing.T) {
	s := New()
	s.GeoAdd("geo",
		GeoAddEntry{Lon: -122.4194, Lat: 37.7749, Member: "sf"},
		GeoAddEntry{Lon: -122.4324, Lat: 37.7849, Member: "near"},
	)
	n, err := s.GeoSearchStore("d", "geo", 37.7749, -122.4194, 50, "km", 0, true)
	if err != nil || n < 2 {
		t.Fatalf("storedist count = %d err=%v", n, err)
	}
	// sf is at the center → distance ~0; near is small but positive
	sfDist, _, _ := s.ZScore("d", "sf")
	if sfDist > 1 { // <1km from itself
		t.Fatalf("sf self-distance = %v km, want ~0", sfDist)
	}
}

// silence sort import — used for ordering checks in future expansion
var _ = sort.Sort
