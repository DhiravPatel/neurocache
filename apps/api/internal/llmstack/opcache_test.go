package llmstack

import (
	"math"
	"testing"
	"time"
)

func TestOpCacheSetAndGet(t *testing.T) {
	o := NewOpCache()
	key := OpKey{OpID: "code_gen", Input: "fib(n) in Python"}
	if err := o.Set(key, "def fib(n):\n  ...", 0); err != nil {
		t.Fatal(err)
	}
	got, ok := o.Get(key)
	if !ok {
		t.Fatal("get returned false")
	}
	if got != "def fib(n):\n  ..." {
		t.Fatalf("got = %q", got)
	}
}

func TestOpCacheKeyComponentsDistinguish(t *testing.T) {
	// Same op_id+input but different model → different cache key.
	o := NewOpCache()
	o.Set(OpKey{OpID: "x", Input: "y", Model: "gpt-4"}, "A", 0)
	o.Set(OpKey{OpID: "x", Input: "y", Model: "claude"}, "B", 0)
	a, _ := o.Get(OpKey{OpID: "x", Input: "y", Model: "gpt-4"})
	b, _ := o.Get(OpKey{OpID: "x", Input: "y", Model: "claude"})
	if a != "A" || b != "B" {
		t.Fatalf("model not distinguishing: a=%q b=%q", a, b)
	}
}

func TestOpCacheParamsDistinguish(t *testing.T) {
	o := NewOpCache()
	o.Set(OpKey{OpID: "x", Input: "y", Params: `{"temp":0}`}, "deterministic", 0)
	o.Set(OpKey{OpID: "x", Input: "y", Params: `{"temp":0.7}`}, "creative", 0)
	a, _ := o.Get(OpKey{OpID: "x", Input: "y", Params: `{"temp":0}`})
	b, _ := o.Get(OpKey{OpID: "x", Input: "y", Params: `{"temp":0.7}`})
	if a != "deterministic" || b != "creative" {
		t.Fatalf("params not distinguishing: a=%q b=%q", a, b)
	}
}

func TestOpCacheMiss(t *testing.T) {
	o := NewOpCache()
	if _, ok := o.Get(OpKey{OpID: "x", Input: "y"}); ok {
		t.Fatal("expected miss")
	}
}

func TestOpCacheTTL(t *testing.T) {
	o := NewOpCache()
	key := OpKey{OpID: "x", Input: "y"}
	o.Set(key, "v", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := o.Get(key); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestOpCacheForget(t *testing.T) {
	o := NewOpCache()
	key := OpKey{OpID: "x", Input: "y"}
	o.Set(key, "v", 0)
	if !o.Forget(key) {
		t.Fatal("forget should return true")
	}
	if o.Forget(key) {
		t.Fatal("forget on missing should return false")
	}
}

func TestOpCachePurgeByOp(t *testing.T) {
	o := NewOpCache()
	o.Set(OpKey{OpID: "code", Input: "a"}, "x", 0)
	o.Set(OpKey{OpID: "code", Input: "b"}, "x", 0)
	o.Set(OpKey{OpID: "sql", Input: "a"}, "x", 0)
	if n := o.Purge("code"); n != 2 {
		t.Fatalf("purge code = %d, want 2", n)
	}
	if _, ok := o.Get(OpKey{OpID: "sql", Input: "a"}); !ok {
		t.Fatal("sql entry should survive code purge")
	}
}

func TestOpCachePurgeAll(t *testing.T) {
	o := NewOpCache()
	o.Set(OpKey{OpID: "a", Input: "x"}, "v", 0)
	o.Set(OpKey{OpID: "b", Input: "x"}, "v", 0)
	if n := o.Purge(""); n != 2 {
		t.Fatalf("purge all = %d, want 2", n)
	}
}

func TestOpCacheSavedUSD(t *testing.T) {
	o := NewOpCache()
	o.SetCostUSD(0.01)
	key := OpKey{OpID: "x", Input: "y"}
	o.Set(key, "v", 0)
	for i := 0; i < 50; i++ {
		o.Get(key)
	}
	s := o.Stats()
	want := 0.5
	if math.Abs(s.SavedUSD-want) > 1e-6 {
		t.Fatalf("saved_usd = %f, want %f", s.SavedUSD, want)
	}
}

func TestOpCachePerOpStats(t *testing.T) {
	o := NewOpCache()
	o.Set(OpKey{OpID: "code", Input: "a"}, "x", 0)
	o.Set(OpKey{OpID: "sql", Input: "a"}, "x", 0)
	o.Get(OpKey{OpID: "code", Input: "a"})    // hit
	o.Get(OpKey{OpID: "code", Input: "miss"}) // miss
	o.Get(OpKey{OpID: "sql", Input: "a"})     // hit
	s := o.Stats()
	ops := map[string]OpStatsRow{}
	for _, op := range s.Ops {
		ops[op.OpID] = op
	}
	if ops["code"].Hits != 1 || ops["code"].Misses != 1 {
		t.Fatalf("code = %+v", ops["code"])
	}
	if ops["sql"].Hits != 1 {
		t.Fatalf("sql = %+v", ops["sql"])
	}
}

func TestOpCacheRejectsEmpty(t *testing.T) {
	o := NewOpCache()
	if err := o.Set(OpKey{OpID: "", Input: "x"}, "v", 0); err == nil {
		t.Fatal("empty op_id should fail")
	}
	if err := o.Set(OpKey{OpID: "x", Input: ""}, "v", 0); err == nil {
		t.Fatal("empty input should fail")
	}
}

func TestOpCacheCapEviction(t *testing.T) {
	o := NewOpCache()
	o.SetCap(20)
	for i := 0; i < 25; i++ {
		o.Set(OpKey{OpID: "x", Input: string(rune('a' + i))}, "v", 0)
	}
	s := o.Stats()
	if s.Entries > 20 {
		t.Fatalf("entries = %d, want <=20", s.Entries)
	}
	if s.TotalEvicts == 0 {
		t.Fatal("expected evictions")
	}
}
