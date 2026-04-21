package store

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

// ─── strings ───────────────────────────────────────────────────────────

func TestStringBasics(t *testing.T) {
	s := New()
	s.Set("k", "hello", 0)
	if v, ok := s.Get("k"); !ok || v != "hello" {
		t.Fatalf("Get k: got (%q,%v)", v, ok)
	}
	if n, _ := s.StrLen("k"); n != 5 {
		t.Errorf("StrLen = %d, want 5", n)
	}
	if _, err := s.Append("k", ", world"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("k"); v != "hello, world" {
		t.Errorf("Append result = %q", v)
	}
	sub, _ := s.GetRange("k", 7, 11)
	if sub != "world" {
		t.Errorf("GetRange = %q, want world", sub)
	}
}

func TestStringSetOptions(t *testing.T) {
	s := New()
	if !s.SetNX("k", "first", 0) {
		t.Fatal("SetNX on empty key should succeed")
	}
	if s.SetNX("k", "second", 0) {
		t.Fatal("SetNX on existing key should fail")
	}
	prev, had, _ := s.GetSet("k", "third")
	if !had || prev != "first" {
		t.Errorf("GetSet: prev=%q had=%v", prev, had)
	}
	v, _ := s.Get("k")
	if v != "third" {
		t.Errorf("after GetSet = %q", v)
	}
}

func TestMSetMGet(t *testing.T) {
	s := New()
	if err := s.MSet("a", "1", "b", "2", "c", "3"); err != nil {
		t.Fatal(err)
	}
	vals, hits, _ := s.MGet("a", "x", "c")
	if !hits[0] || vals[0] != "1" {
		t.Errorf("a miss: %+v", vals)
	}
	if hits[1] {
		t.Errorf("x should miss")
	}
	if !hits[2] || vals[2] != "3" {
		t.Errorf("c miss: %+v", vals)
	}
}

func TestIncrFloat(t *testing.T) {
	s := New()
	s.Set("k", "10", 0)
	if v, _ := s.Incr("k", 5); v != 15 {
		t.Errorf("Incr = %d", v)
	}
	f, err := s.IncrByFloat("k", 2.5)
	if err != nil {
		t.Fatal(err)
	}
	if f != 17.5 {
		t.Errorf("IncrByFloat = %v", f)
	}
}

// ─── TTL / type ────────────────────────────────────────────────────────

func TestTTLAndPersist(t *testing.T) {
	s := New()
	s.Set("k", "v", 5*time.Second)
	if d := s.TTL("k"); d <= 0 {
		t.Errorf("TTL = %v", d)
	}
	if !s.Persist("k") {
		t.Error("Persist should succeed on TTL-bearing key")
	}
	if d := s.TTL("k"); d != -1 {
		t.Errorf("after Persist TTL = %v, want -1", d)
	}
	if s.TTL("missing") != -2 {
		t.Error("missing key TTL should be -2")
	}
}

func TestTypeCommand(t *testing.T) {
	s := New()
	s.Set("str", "v", 0)
	s.LPush("lst", "a")
	s.SAdd("st", "a")
	s.HSet("h", "f", "v")
	s.ZAdd("z", ZPair{Score: 1, Member: "m"})
	cases := map[string]string{"str": "string", "lst": "list", "st": "set", "h": "hash", "z": "zset", "missing": "none"}
	for k, want := range cases {
		if got := s.Type(k).String(); got != want {
			t.Errorf("Type(%s) = %s, want %s", k, got, want)
		}
	}
}

func TestWrongType(t *testing.T) {
	s := New()
	s.Set("k", "v", 0)
	if _, err := s.LPush("k", "x"); err != ErrWrongType {
		t.Errorf("LPush on string: err = %v", err)
	}
	if _, err := s.SAdd("k", "x"); err != ErrWrongType {
		t.Errorf("SAdd on string: err = %v", err)
	}
}

// ─── lists ─────────────────────────────────────────────────────────────

func TestListPushPop(t *testing.T) {
	s := New()
	s.RPush("l", "a", "b", "c")
	s.LPush("l", "z")
	if n, _ := s.LLen("l"); n != 4 {
		t.Errorf("LLen = %d", n)
	}
	out, _ := s.LRange("l", 0, -1)
	want := []string{"z", "a", "b", "c"}
	if !equal(out, want) {
		t.Errorf("LRange = %v, want %v", out, want)
	}
	v, ok, _ := s.LPop("l")
	if !ok || v != "z" {
		t.Errorf("LPop = %q ok=%v", v, ok)
	}
	v, ok, _ = s.RPop("l")
	if !ok || v != "c" {
		t.Errorf("RPop = %q ok=%v", v, ok)
	}
}

func TestListLIndexLSet(t *testing.T) {
	s := New()
	s.RPush("l", "a", "b", "c")
	v, ok, _ := s.LIndex("l", 1)
	if !ok || v != "b" {
		t.Errorf("LIndex 1 = %q", v)
	}
	v, ok, _ = s.LIndex("l", -1)
	if !ok || v != "c" {
		t.Errorf("LIndex -1 = %q", v)
	}
	if err := s.LSet("l", 1, "B"); err != nil {
		t.Fatal(err)
	}
	v, _, _ = s.LIndex("l", 1)
	if v != "B" {
		t.Errorf("after LSet = %q", v)
	}
}

func TestListLRemLTrim(t *testing.T) {
	s := New()
	s.RPush("l", "a", "b", "a", "c", "a")
	n, _ := s.LRem("l", 2, "a")
	if n != 2 {
		t.Errorf("LRem = %d", n)
	}
	out, _ := s.LRange("l", 0, -1)
	if !equal(out, []string{"b", "c", "a"}) {
		t.Errorf("after LRem = %v", out)
	}
	_ = s.LTrim("l", 0, 1)
	out, _ = s.LRange("l", 0, -1)
	if !equal(out, []string{"b", "c"}) {
		t.Errorf("after LTrim = %v", out)
	}
}

// ─── hashes ────────────────────────────────────────────────────────────

func TestHashBasics(t *testing.T) {
	s := New()
	added, _ := s.HSet("h", "a", "1", "b", "2")
	if added != 2 {
		t.Errorf("HSet added = %d, want 2", added)
	}
	added, _ = s.HSet("h", "a", "1b") // overwrite, not new
	if added != 0 {
		t.Errorf("overwrite counted as new")
	}
	v, ok, _ := s.HGet("h", "a")
	if !ok || v != "1b" {
		t.Errorf("HGet = %q", v)
	}
	n, _ := s.HLen("h")
	if n != 2 {
		t.Errorf("HLen = %d", n)
	}
	flat, _ := s.HGetAll("h")
	if len(flat) != 4 {
		t.Errorf("HGetAll len = %d", len(flat))
	}
}

func TestHashIncrByDel(t *testing.T) {
	s := New()
	s.HSet("h", "n", "10")
	v, _ := s.HIncrBy("h", "n", 5)
	if v != 15 {
		t.Errorf("HIncrBy = %d", v)
	}
	removed, _ := s.HDel("h", "n", "missing")
	if removed != 1 {
		t.Errorf("HDel removed = %d", removed)
	}
	if s.Type("h") != TypeNone {
		t.Error("empty hash should be deleted")
	}
}

// ─── sets ──────────────────────────────────────────────────────────────

func TestSetBasics(t *testing.T) {
	s := New()
	s.SAdd("s", "a", "b", "c")
	s.SAdd("s", "b") // dup, no new
	n, _ := s.SCard("s")
	if n != 3 {
		t.Errorf("SCard = %d", n)
	}
	ok, _ := s.SIsMember("s", "b")
	if !ok {
		t.Error("SIsMember b should be true")
	}
	members, _ := s.SMembers("s")
	if len(members) != 3 {
		t.Errorf("SMembers = %v", members)
	}
}

func TestSetOps(t *testing.T) {
	s := New()
	s.SAdd("a", "1", "2", "3")
	s.SAdd("b", "2", "3", "4")
	inter, _ := s.SInter("a", "b")
	sort.Strings(inter)
	if !equal(inter, []string{"2", "3"}) {
		t.Errorf("SInter = %v", inter)
	}
	union, _ := s.SUnion("a", "b")
	sort.Strings(union)
	if !equal(union, []string{"1", "2", "3", "4"}) {
		t.Errorf("SUnion = %v", union)
	}
	diff, _ := s.SDiff("a", "b")
	sort.Strings(diff)
	if !equal(diff, []string{"1"}) {
		t.Errorf("SDiff = %v", diff)
	}
}

// ─── sorted sets ───────────────────────────────────────────────────────

func TestZSetBasics(t *testing.T) {
	s := New()
	added, _ := s.ZAdd("z", ZPair{1, "a"}, ZPair{2, "b"}, ZPair{3, "c"})
	if added != 3 {
		t.Errorf("ZAdd added = %d", added)
	}
	sc, ok, _ := s.ZScore("z", "b")
	if !ok || sc != 2 {
		t.Errorf("ZScore b = %v", sc)
	}
	n, _ := s.ZCard("z")
	if n != 3 {
		t.Errorf("ZCard = %d", n)
	}
	r, ok, _ := s.ZRank("z", "b")
	if !ok || r != 1 {
		t.Errorf("ZRank b = %d", r)
	}
}

func TestZSetRange(t *testing.T) {
	s := New()
	for i := 0; i < 5; i++ {
		s.ZAdd("z", ZPair{Score: float64(i), Member: strconv.Itoa(i)})
	}
	out, _ := s.ZRange("z", 0, -1, false, false)
	if len(out) != 5 {
		t.Errorf("ZRange len = %d", len(out))
	}
	for i, r := range out {
		if r.Member != strconv.Itoa(i) {
			t.Errorf("out[%d] = %q", i, r.Member)
		}
	}
	// reverse
	out, _ = s.ZRange("z", 0, 2, false, true)
	want := []string{"4", "3", "2"}
	for i, r := range out {
		if r.Member != want[i] {
			t.Errorf("reverse[%d] = %q", i, r.Member)
		}
	}
}

func TestZSetRangeByScore(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.ZAdd("z", ZPair{Score: float64(i), Member: strconv.Itoa(i)})
	}
	out, _ := s.ZRangeByScore("z", "3", "7", 0, -1, false)
	if len(out) != 5 {
		t.Errorf("inclusive [3,7] = %d", len(out))
	}
	out, _ = s.ZRangeByScore("z", "(3", "(7", 0, -1, false)
	if len(out) != 3 {
		t.Errorf("exclusive (3,7) = %d", len(out))
	}
	out, _ = s.ZRangeByScore("z", "-inf", "+inf", 0, -1, false)
	if len(out) != 10 {
		t.Errorf("-inf..+inf = %d", len(out))
	}
}

func TestZSetIncrAndPop(t *testing.T) {
	s := New()
	s.ZAdd("z", ZPair{1, "a"})
	v, _ := s.ZIncrBy("z", 2.5, "a")
	if v != 3.5 {
		t.Errorf("ZIncrBy = %v", v)
	}
	m, _, ok, _ := s.ZPopMax("z")
	if !ok || m != "a" {
		t.Errorf("ZPopMax = %q ok=%v", m, ok)
	}
	_, _, ok, _ = s.ZPopMin("z")
	if ok {
		t.Error("empty zset should not pop")
	}
}

// ─── scan ──────────────────────────────────────────────────────────────

func TestScan(t *testing.T) {
	s := New()
	for i := 0; i < 25; i++ {
		s.Set("k"+strconv.Itoa(i), "v", 0)
	}
	var all []string
	cursor := "0"
	for {
		next, keys := s.Scan(cursor, "", "", 10)
		all = append(all, keys...)
		cursor = next
		if cursor == "0" {
			break
		}
	}
	if len(all) != 25 {
		t.Errorf("scan returned %d, want 25", len(all))
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"user:*", "user:42", true},
		{"user:*", "admin:42", false},
		{"u?er", "user", true},
		{"u?er", "usser", false},
		{"[abc]ar", "bar", true},
		{"[abc]ar", "dar", false},
		{"*", "anything", true},
	}
	for _, c := range cases {
		got := globMatch(c.p, c.s)
		if got != c.want {
			t.Errorf("glob(%q,%q) = %v want %v", c.p, c.s, got, c.want)
		}
	}
}

// ─── keyspace notify ───────────────────────────────────────────────────

func TestNotifier(t *testing.T) {
	s := New()
	var events []string
	s.SetNotifier(func(event, key string) {
		events = append(events, event+":"+key)
	})
	s.Set("k", "v", 0)
	s.Del("k")
	s.LPush("l", "a")
	if len(events) < 3 {
		t.Errorf("events = %v", events)
	}
}

// ─── helpers ───────────────────────────────────────────────────────────

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
