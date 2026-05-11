package llmstack

import (
	"errors"
	"testing"
)

func TestRouteHappyPath(t *testing.T) {
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic", "mistral"})
	p, err := r.Next("chat")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if p != "openai" {
		t.Fatalf("first pick=%q want=openai", p)
	}
}

func TestRouteFailoverOnDown(t *testing.T) {
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic", "mistral"})
	if err := r.MarkDown("openai"); err != nil {
		t.Fatalf("MarkDown: %v", err)
	}
	p, _ := r.Next("chat")
	if p != "anthropic" {
		t.Fatalf("after openai down: %q want=anthropic", p)
	}
	r.MarkDown("anthropic")
	p, _ = r.Next("chat")
	if p != "mistral" {
		t.Fatalf("after both down: %q want=mistral", p)
	}
}

func TestRouteAllDownReturnsError(t *testing.T) {
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic"})
	r.MarkDown("openai")
	r.MarkDown("anthropic")
	_, err := r.Next("chat")
	if !errors.Is(err, ErrNoHealthyProvider) {
		t.Fatalf("expected NOHEALTHY; got %v", err)
	}
}

func TestRouteRecoveryViaMarkUp(t *testing.T) {
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic"})
	r.MarkDown("openai")
	if p, _ := r.Next("chat"); p != "anthropic" {
		t.Fatalf("expected anthropic; got %s", p)
	}
	r.MarkUp("openai")
	if p, _ := r.Next("chat"); p != "openai" {
		t.Fatalf("expected openai back; got %s", p)
	}
}

func TestRouteSharedProviderFlipsAcrossRoutes(t *testing.T) {
	// Marking openai down on one route's view should propagate to
	// every route that lists openai.
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic"})
	r.SetRoute("embed", []string{"openai", "cohere"})
	r.MarkDown("openai")
	if p, _ := r.Next("chat"); p == "openai" {
		t.Fatal("chat route still picked openai after MarkDown")
	}
	if p, _ := r.Next("embed"); p == "openai" {
		t.Fatal("embed route still picked openai after MarkDown")
	}
}

func TestRouteUnknownErrors(t *testing.T) {
	r := NewLLMRouter()
	if _, err := r.Next("ghost"); !errors.Is(err, ErrUnknownRoute) {
		t.Fatalf("expected UNKNOWNROUTE; got %v", err)
	}
	if err := r.MarkDown("ghost"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("expected UNKNOWNPROVIDER; got %v", err)
	}
}

func TestRouteRotationCounter(t *testing.T) {
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic"})
	r.Next("chat") // happy: no rotation
	r.MarkDown("openai")
	r.Next("chat") // rotation: skip openai → anthropic
	r.Next("chat") // rotation again
	rows := r.List()
	for _, row := range rows {
		if row.Name == "chat" && row.Rotations < 2 {
			t.Fatalf("expected ≥2 rotations; got %d", row.Rotations)
		}
	}
}

func BenchmarkRouteNext(b *testing.B) {
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic", "mistral"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Next("chat")
	}
}

func BenchmarkRouteNextWithFailover(b *testing.B) {
	// openai is down; every Next pays the cost of one skip.
	r := NewLLMRouter()
	r.SetRoute("chat", []string{"openai", "anthropic"})
	r.MarkDown("openai")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Next("chat")
	}
}
