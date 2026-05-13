package llmstack

import (
	"math"
	"testing"
	"time"
)

func TestRewriteSetAndGet(t *testing.T) {
	r := NewRewriteCache()
	if err := r.Set("hyDE", "what is bitcoin?", "Bitcoin is a decentralized digital currency...", 0); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("hyDE", "what is bitcoin?")
	if !ok {
		t.Fatal("get returned false on hit")
	}
	if got != "Bitcoin is a decentralized digital currency..." {
		t.Fatalf("got = %q", got)
	}
}

func TestRewriteMiss(t *testing.T) {
	r := NewRewriteCache()
	if _, ok := r.Get("hyDE", "missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestRewriteTTLHonored(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q", "v", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := r.Get("hyDE", "q"); ok {
		t.Fatal("expected expired entry to miss")
	}
}

func TestRewriteSetMultiAndList(t *testing.T) {
	r := NewRewriteCache()
	variants := []string{
		"What is the meaning of life?",
		"What's the purpose of existence?",
		"Why are we here?",
	}
	if err := r.SetMulti("multi-query", "meaning of life", variants, 0); err != nil {
		t.Fatal(err)
	}
	got, ok := r.List("multi-query", "meaning of life")
	if !ok {
		t.Fatal("list returned false")
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// GET returns the FIRST variant
	first, _ := r.Get("multi-query", "meaning of life")
	if first != variants[0] {
		t.Fatalf("get first = %q", first)
	}
}

func TestRewriteTechniqueScoped(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q", "hyDE-result", 0)
	r.Set("step-back", "q", "stepback-result", 0)
	a, _ := r.Get("hyDE", "q")
	b, _ := r.Get("step-back", "q")
	if a != "hyDE-result" || b != "stepback-result" {
		t.Fatalf("technique not scoping cache: a=%q b=%q", a, b)
	}
}

func TestRewriteRejectsEmpty(t *testing.T) {
	r := NewRewriteCache()
	if err := r.Set("", "q", "x", 0); err == nil {
		t.Fatal("expected error on empty technique")
	}
	if err := r.Set("t", "", "x", 0); err == nil {
		t.Fatal("expected error on empty query")
	}
	if err := r.SetMulti("t", "q", nil, 0); err == nil {
		t.Fatal("expected error on empty variants")
	}
}

func TestRewriteForget(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q", "v", 0)
	if !r.Forget("hyDE", "q") {
		t.Fatal("forget should return true on hit")
	}
	if r.Forget("hyDE", "q") {
		t.Fatal("forget should return false on miss")
	}
}

func TestRewritePurgeAll(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q1", "v", 0)
	r.Set("step-back", "q2", "v", 0)
	n := r.Purge("")
	if n != 2 {
		t.Fatalf("purge all = %d, want 2", n)
	}
}

func TestRewritePurgeByTechnique(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q1", "v", 0)
	r.Set("hyDE", "q2", "v", 0)
	r.Set("step-back", "q3", "v", 0)
	n := r.Purge("hyDE")
	if n != 2 {
		t.Fatalf("purge hyDE = %d, want 2", n)
	}
	if _, ok := r.Get("step-back", "q3"); !ok {
		t.Fatal("step-back entry should have survived hyDE purge")
	}
}

func TestRewriteCapEviction(t *testing.T) {
	r := NewRewriteCache()
	r.SetCap(20)
	for i := 0; i < 25; i++ {
		r.Set("hyDE", string(rune('a'+i)), "v", 0)
	}
	s := r.Stats()
	if s.Entries > 20 {
		t.Fatalf("entries = %d, want <=20", s.Entries)
	}
	if s.TotalEvicts == 0 {
		t.Fatal("expected eviction counter to advance")
	}
}

func TestRewriteStatsHitRate(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q", "v", 0)
	for i := 0; i < 3; i++ {
		r.Get("hyDE", "q") // hit
	}
	for i := 0; i < 7; i++ {
		r.Get("hyDE", "missing") // miss
	}
	s := r.Stats()
	if s.HitRate < 0.29 || s.HitRate > 0.31 {
		t.Fatalf("hit_rate = %f, want ~0.30", s.HitRate)
	}
}

func TestRewriteSavedUSD(t *testing.T) {
	r := NewRewriteCache()
	r.SetCostUSD(0.0005) // half-cent per rewrite call
	r.Set("hyDE", "q", "v", 0)
	for i := 0; i < 200; i++ {
		r.Get("hyDE", "q")
	}
	s := r.Stats()
	want := 0.1 // 200 * 0.0005
	if math.Abs(s.SavedUSD-want) > 1e-6 {
		t.Fatalf("saved_usd = %f, want %f", s.SavedUSD, want)
	}
}

func TestRewritePerTechniqueStats(t *testing.T) {
	r := NewRewriteCache()
	r.Set("hyDE", "q", "v", 0)
	r.Set("step-back", "q", "v", 0)
	r.Get("hyDE", "q")        // hit
	r.Get("hyDE", "miss")     // miss
	r.Get("step-back", "miss") // miss

	s := r.Stats()
	techs := map[string]RewriteTechStatsRow{}
	for _, t := range s.Techniques {
		techs[t.Technique] = t
	}
	if techs["hyDE"].Hits != 1 || techs["hyDE"].Misses != 1 {
		t.Fatalf("hyDE stats = %+v", techs["hyDE"])
	}
	if techs["step-back"].Misses != 1 {
		t.Fatalf("step-back stats = %+v", techs["step-back"])
	}
}
