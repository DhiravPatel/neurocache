package llmstack

import (
	"math"
	"testing"
)

func TestSpecDecCacheRoundtrip(t *testing.T) {
	s := NewSpecDecCache()
	if err := s.Cache("h-1", []string{"the", "cat", "sat"}); err != nil {
		t.Fatal(err)
	}
	tokens, ok := s.Get("h-1")
	if !ok || len(tokens) != 3 || tokens[1] != "cat" {
		t.Fatalf("got %v ok=%v", tokens, ok)
	}
}

func TestSpecDecCacheMissReturnsNil(t *testing.T) {
	s := NewSpecDecCache()
	t2, ok := s.Get("ghost")
	if ok || t2 != nil {
		t.Fatalf("miss should return nil/false: %v %v", t2, ok)
	}
}

func TestSpecDecRecordAndRate(t *testing.T) {
	s := NewSpecDecCache()
	// 8/10 accepted three times → EMA should settle near 0.8
	for i := 0; i < 3; i++ {
		s.Record("gpt-4o", "chat", 8, 10)
	}
	r, ok := s.Rate("gpt-4o", "chat")
	if !ok {
		t.Fatal("rate should be set")
	}
	if math.Abs(r.Rate-0.8) > 1e-9 {
		t.Fatalf("rate = %f, want ~0.8", r.Rate)
	}
	if r.Samples != 3 {
		t.Fatalf("samples = %d", r.Samples)
	}
}

func TestSpecDecRateAcrossClassesTokenWeighted(t *testing.T) {
	s := NewSpecDecCache()
	// "chat" gets 1000 tokens at 80% acceptance
	for i := 0; i < 100; i++ {
		s.Record("gpt-4o", "chat", 8, 10)
	}
	// "code" gets 1000 tokens at 20% acceptance
	for i := 0; i < 100; i++ {
		s.Record("gpt-4o", "code", 2, 10)
	}
	r, _ := s.Rate("gpt-4o", "")
	// Token-weighted average across classes: (800+200)/(1000+1000)=0.5
	if math.Abs(r.Rate-0.5) > 1e-9 {
		t.Fatalf("aggregate rate = %f, want 0.5", r.Rate)
	}
	if r.TokensSeen != 2000 {
		t.Fatalf("tokens seen = %d", r.TokensSeen)
	}
}

func TestSpecDecDecideUseDuringWarmup(t *testing.T) {
	s := NewSpecDecCache()
	s.Record("m", "x", 1, 10) // way below threshold but only 1 sample
	d := s.Decide("m", "x")
	if !d.Use {
		t.Fatalf("warmup should default to use=true: %+v", d)
	}
	if d.Reason == "" {
		t.Fatal("reason should be set")
	}
}

func TestSpecDecDecideRefuseLowRate(t *testing.T) {
	s := NewSpecDecCache()
	// 30+ samples with 10% acceptance
	for i := 0; i < 40; i++ {
		s.Record("m", "x", 1, 10)
	}
	d := s.Decide("m", "x")
	if d.Use {
		t.Fatalf("low acceptance should refuse: %+v", d)
	}
}

func TestSpecDecDecideUseHighRate(t *testing.T) {
	s := NewSpecDecCache()
	for i := 0; i < 40; i++ {
		s.Record("m", "x", 7, 10)
	}
	d := s.Decide("m", "x")
	if !d.Use {
		t.Fatalf("high acceptance should use: %+v", d)
	}
}

func TestSpecDecDecideUnknownDefaultsToUse(t *testing.T) {
	s := NewSpecDecCache()
	d := s.Decide("unknown-model", "class")
	if !d.Use {
		t.Fatal("unknown should default to use=true")
	}
}

func TestSpecDecStatusSortedByRate(t *testing.T) {
	s := NewSpecDecCache()
	s.Record("m", "low", 1, 10)
	s.Record("m", "high", 9, 10)
	s.Record("m", "mid", 5, 10)
	rows, ok := s.Status("m")
	if !ok || len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].PrefixClass != "high" || rows[2].PrefixClass != "low" {
		t.Fatalf("not sorted by rate desc: %+v", rows)
	}
}

func TestSpecDecResetOne(t *testing.T) {
	s := NewSpecDecCache()
	s.Record("m1", "x", 5, 10)
	s.Record("m2", "x", 5, 10)
	if s.Reset("m1") != 1 {
		t.Fatal("reset m1 should drop 1")
	}
	if _, ok := s.Rate("m1", ""); ok {
		t.Fatal("m1 still present after reset")
	}
	if _, ok := s.Rate("m2", ""); !ok {
		t.Fatal("m2 dropped by mistake")
	}
}

func TestSpecDecResetAll(t *testing.T) {
	s := NewSpecDecCache()
	s.Record("m1", "x", 5, 10)
	s.Record("m2", "x", 5, 10)
	if s.Reset("ALL") != 2 {
		t.Fatal("reset ALL should drop 2")
	}
}

func TestSpecDecCacheCapEvicts(t *testing.T) {
	s := NewSpecDecCache()
	s.SetCap(3)
	for i := 0; i < 10; i++ {
		s.Cache("h-"+itoaBench(i), []string{"t"})
	}
	st := s.Stats()
	if st.Drafts > 3 {
		t.Fatalf("cap not enforced: drafts=%d", st.Drafts)
	}
}

func TestSpecDecRejectsBadInput(t *testing.T) {
	s := NewSpecDecCache()
	if err := s.Cache("", []string{"x"}); err == nil {
		t.Fatal("empty hash should fail")
	}
	if err := s.Cache("h", nil); err == nil {
		t.Fatal("empty tokens should fail")
	}
	if err := s.Record("", "x", 1, 2); err == nil {
		t.Fatal("empty model should fail")
	}
	if err := s.Record("m", "c", 5, 0); err == nil {
		t.Fatal("total=0 should fail")
	}
	if err := s.Record("m", "c", 11, 10); err == nil {
		t.Fatal("accepted > total should fail")
	}
}

func TestSpecDecStatsAdvance(t *testing.T) {
	s := NewSpecDecCache()
	s.Cache("h", []string{"x"})
	s.Get("h")
	s.Get("nope")
	s.Record("m", "x", 5, 10)
	s.Decide("m", "x")
	st := s.Stats()
	if st.Drafts != 1 || st.ModelsTracked != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalCacheHits != 1 || st.TotalCacheMisses != 1 {
		t.Fatalf("hit/miss = %+v", st)
	}
	if st.TotalRecords != 1 || st.TotalDecisions != 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkSpecDecGet(b *testing.B) {
	s := NewSpecDecCache()
	s.Cache("h", []string{"t1", "t2", "t3", "t4", "t5"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Get("h")
	}
}

func BenchmarkSpecDecRecord(b *testing.B) {
	s := NewSpecDecCache()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Record("m", "class-a", 7, 10)
	}
}

func BenchmarkSpecDecDecide(b *testing.B) {
	s := NewSpecDecCache()
	for i := 0; i < 100; i++ {
		s.Record("m", "class-a", 7, 10)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Decide("m", "class-a")
	}
}
