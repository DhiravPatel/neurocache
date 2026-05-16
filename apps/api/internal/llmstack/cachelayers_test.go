package llmstack

import (
	"testing"
	"time"
)

func TestCacheLayersExactHit(t *testing.T) {
	c := NewCacheLayers()
	c.Set("exact", "what is bitcoin", "Bitcoin is...", LayerSetOpts{})
	r := c.Lookup("what is bitcoin", LookupOpts{})
	if r.HitLayer != "exact" || r.Value != "Bitcoin is..." {
		t.Fatalf("lookup = %+v", r)
	}
}

func TestCacheLayersSemanticHit(t *testing.T) {
	c := NewCacheLayers()
	c.SetThreshold(0.5)
	c.Set("semantic", "how do I reset my password",
		"Click forgot password on the login page.", LayerSetOpts{})
	r := c.Lookup("I forgot my password help", LookupOpts{})
	if r.HitLayer != "semantic" {
		t.Fatalf("expected semantic hit, got %+v", r)
	}
}

func TestCacheLayersNegativeHit(t *testing.T) {
	c := NewCacheLayers()
	c.Set("negative", "weather on mars right now",
		"upstream returned 'I don't have real-time data'", LayerSetOpts{})
	r := c.Lookup("weather on mars right now", LookupOpts{})
	if r.HitLayer != "negative" {
		t.Fatalf("expected negative hit, got %+v", r)
	}
}

func TestCacheLayersOrderingPreference(t *testing.T) {
	// Exact should win over semantic, semantic over negative
	c := NewCacheLayers()
	c.SetThreshold(0.5)
	c.Set("exact", "k1", "exact-value", LayerSetOpts{})
	c.Set("semantic", "k1", "semantic-value", LayerSetOpts{})
	c.Set("negative", "k1", "negative-value", LayerSetOpts{})
	r := c.Lookup("k1", LookupOpts{})
	if r.HitLayer != "exact" {
		t.Fatalf("exact should win: %+v", r)
	}
}

func TestCacheLayersMiss(t *testing.T) {
	c := NewCacheLayers()
	r := c.Lookup("anything", LookupOpts{})
	if r.HitLayer != "miss" {
		t.Fatalf("expected miss, got %+v", r)
	}
}

func TestCacheLayersTTLExpiry(t *testing.T) {
	c := NewCacheLayers()
	c.Set("exact", "k", "v", LayerSetOpts{TTL: 1 * time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	r := c.Lookup("k", LookupOpts{})
	if r.HitLayer != "miss" {
		t.Fatalf("expired entry should miss, got %+v", r)
	}
}

func TestCacheLayersForgetSpecificLayer(t *testing.T) {
	c := NewCacheLayers()
	c.Set("exact", "k", "v1", LayerSetOpts{})
	c.Set("negative", "k", "v3", LayerSetOpts{})
	dropped := c.Forget("exact", "k")
	if dropped != 1 {
		t.Fatalf("forget = %d, want 1", dropped)
	}
	r := c.Lookup("k", LookupOpts{})
	if r.HitLayer != "negative" {
		t.Fatalf("after forget exact, should fall through to negative: %+v", r)
	}
}

func TestCacheLayersForgetAllLayers(t *testing.T) {
	c := NewCacheLayers()
	c.Set("exact", "k", "v1", LayerSetOpts{})
	c.Set("negative", "k", "v2", LayerSetOpts{})
	dropped := c.Forget("", "k")
	if dropped != 2 {
		t.Fatalf("forget all = %d, want 2", dropped)
	}
}

func TestCacheLayersPurgeByLayer(t *testing.T) {
	c := NewCacheLayers()
	c.Set("exact", "k1", "v", LayerSetOpts{})
	c.Set("exact", "k2", "v", LayerSetOpts{})
	c.Set("negative", "k3", "v", LayerSetOpts{})
	if n := c.Purge("exact"); n != 2 {
		t.Fatalf("purge exact = %d, want 2", n)
	}
	r := c.Lookup("k3", LookupOpts{})
	if r.HitLayer != "negative" {
		t.Fatal("negative entry should survive exact purge")
	}
}

func TestCacheLayersStatsHitRate(t *testing.T) {
	c := NewCacheLayers()
	c.Set("exact", "k1", "v", LayerSetOpts{})
	c.Lookup("k1", LookupOpts{}) // hit
	c.Lookup("k2", LookupOpts{}) // miss
	c.Lookup("k3", LookupOpts{}) // miss
	s := c.Stats()
	if s.TotalLookups != 3 || s.ExactHits != 1 || s.Misses != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if s.HitRate < 0.32 || s.HitRate > 0.34 {
		t.Fatalf("hit_rate = %f, want ~0.333", s.HitRate)
	}
}

func TestCacheLayersRejectsUnknownLayer(t *testing.T) {
	c := NewCacheLayers()
	if err := c.Set("magic", "k", "v", LayerSetOpts{}); err == nil {
		t.Fatal("unknown layer should fail")
	}
}

func TestCacheLayersUpdateSemanticEntry(t *testing.T) {
	c := NewCacheLayers()
	c.SetThreshold(0.5)
	c.Set("semantic", "k", "v1", LayerSetOpts{})
	c.Set("semantic", "k", "v2", LayerSetOpts{}) // replace
	r := c.Lookup("k", LookupOpts{})
	if r.Value != "v2" {
		t.Fatalf("expected v2 after replace, got %q", r.Value)
	}
	if s := c.Stats(); s.SemanticSize != 1 {
		t.Fatalf("semantic_size = %d, want 1", s.SemanticSize)
	}
}
