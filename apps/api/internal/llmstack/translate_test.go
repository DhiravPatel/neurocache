package llmstack

import (
	"math"
	"testing"
	"time"
)

func TestTranslateSetAndGet(t *testing.T) {
	c := NewTranslateCache()
	if err := c.Set("en", "es", "Hello", "Hola", 0); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("en", "es", "Hello")
	if !ok || got != "Hola" {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
}

func TestTranslateMissOnUnknownPair(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "Hello", "Hola", 0)
	if _, ok := c.Get("en", "fr", "Hello"); ok {
		t.Fatal("different target should miss")
	}
	if _, ok := c.Get("de", "es", "Hello"); ok {
		t.Fatal("different source should miss")
	}
}

func TestTranslateTTL(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "Hi", "Hola", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("en", "es", "Hi"); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestTranslateMGet(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "Hello", "Hola", 0)
	c.Set("en", "es", "World", "Mundo", 0)
	rows := c.MGet("en", "es", []string{"Hello", "missing", "World"})
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if !rows[0].Hit || rows[0].Translation != "Hola" {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[1].Hit {
		t.Fatalf("row 1 should miss: %+v", rows[1])
	}
	if !rows[2].Hit || rows[2].Translation != "Mundo" {
		t.Fatalf("row 2 = %+v", rows[2])
	}
}

func TestTranslateForget(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "Hi", "Hola", 0)
	if !c.Forget("en", "es", "Hi") {
		t.Fatal("forget should return true")
	}
	if c.Forget("en", "es", "Hi") {
		t.Fatal("forget on miss should return false")
	}
}

func TestTranslatePurgeAll(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "a", "x", 0)
	c.Set("en", "fr", "b", "y", 0)
	if n := c.Purge("", ""); n != 2 {
		t.Fatalf("purge all = %d, want 2", n)
	}
}

func TestTranslatePurgeByPair(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "a", "x", 0)
	c.Set("en", "fr", "a", "x", 0)
	c.Set("de", "es", "a", "x", 0)
	if n := c.Purge("en", ""); n != 2 {
		t.Fatalf("purge source en = %d, want 2", n)
	}
	if _, ok := c.Get("de", "es", "a"); !ok {
		t.Fatal("de-es entry should survive en purge")
	}
}

func TestTranslateSavedUSD(t *testing.T) {
	c := NewTranslateCache()
	c.SetCostUSD(0.001)
	c.Set("en", "es", "Hi", "Hola", 0)
	for i := 0; i < 100; i++ {
		c.Get("en", "es", "Hi")
	}
	s := c.Stats()
	want := 0.1
	if math.Abs(s.SavedUSD-want) > 1e-6 {
		t.Fatalf("saved_usd = %f, want %f", s.SavedUSD, want)
	}
}

func TestTranslatePerPairStats(t *testing.T) {
	c := NewTranslateCache()
	c.Set("en", "es", "a", "x", 0)
	c.Set("en", "fr", "b", "y", 0)
	c.Get("en", "es", "a") // hit
	c.Get("en", "es", "z") // miss
	c.Get("en", "fr", "b") // hit
	s := c.Stats()
	pairs := map[string]TranslatePairStatsRow{}
	for _, p := range s.Pairs {
		pairs[p.Pair] = p
	}
	if pairs["en|es"].Hits != 1 || pairs["en|es"].Misses != 1 {
		t.Fatalf("en|es = %+v", pairs["en|es"])
	}
	if pairs["en|fr"].Hits != 1 {
		t.Fatalf("en|fr = %+v", pairs["en|fr"])
	}
}

func TestTranslateRejectsEmpty(t *testing.T) {
	c := NewTranslateCache()
	if err := c.Set("", "es", "x", "y", 0); err == nil {
		t.Fatal("empty source should fail")
	}
	if err := c.Set("en", "", "x", "y", 0); err == nil {
		t.Fatal("empty target should fail")
	}
	if err := c.Set("en", "es", "", "y", 0); err == nil {
		t.Fatal("empty text should fail")
	}
}
