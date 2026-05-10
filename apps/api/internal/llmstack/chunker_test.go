package llmstack

import (
	"strings"
	"testing"
)

func TestChunkChar(t *testing.T) {
	c := NewChunker(NewTokens())
	text := strings.Repeat("a", 1000)
	chunks := c.Chunk(text, ChunkOpts{Strategy: "char", Size: 100, Overlap: 0})
	if len(chunks) != 10 {
		t.Fatalf("want 10 chunks, got %d", len(chunks))
	}
	for _, ch := range chunks {
		if len(ch) != 100 {
			t.Fatalf("char chunk size = %d, want 100", len(ch))
		}
	}
}

func TestChunkCharWithOverlap(t *testing.T) {
	c := NewChunker(NewTokens())
	text := strings.Repeat("a", 100)
	chunks := c.Chunk(text, ChunkOpts{Strategy: "char", Size: 30, Overlap: 10})
	// step = 30 - 10 = 20. 100/20 = 5 windows starting at 0,20,40,60,80
	// → chunks at [0:30],[20:50],[40:70],[60:90],[80:100] (last is short)
	if len(chunks) != 5 {
		t.Fatalf("want 5 chunks with overlap, got %d", len(chunks))
	}
}

func TestChunkSentence(t *testing.T) {
	c := NewChunker(NewTokens())
	text := "The quick brown fox. Jumps over the lazy dog. Then it ran away. " +
		"Far into the woods. Never to be seen again. Until next time."
	chunks := c.Chunk(text, ChunkOpts{Strategy: "sentence", Size: 50, Overlap: 0})
	if len(chunks) < 2 {
		t.Fatalf("want multiple sentence chunks, got %d", len(chunks))
	}
	// Each chunk should be roughly the size or smaller (we group up to size)
	for _, ch := range chunks {
		if len(ch) > 100 {
			t.Fatalf("sentence chunk too large: %d", len(ch))
		}
	}
}

func TestChunkParagraph(t *testing.T) {
	c := NewChunker(NewTokens())
	text := "First paragraph here.\n\nSecond paragraph.\n\nThird and final paragraph with more text in it."
	chunks := c.Chunk(text, ChunkOpts{Strategy: "paragraph", Size: 60, Overlap: 0})
	if len(chunks) < 2 {
		t.Fatalf("want multiple paragraph chunks, got %d", len(chunks))
	}
}

func TestChunkToken(t *testing.T) {
	c := NewChunker(NewTokens())
	long := ""
	for i := 0; i < 100; i++ {
		long += "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
	}
	chunks := c.Chunk(long, ChunkOpts{Strategy: "token", Size: 50, Model: "gpt-4o"})
	if len(chunks) < 2 {
		t.Fatalf("want multiple token chunks, got %d", len(chunks))
	}
}

func TestChunkEmptyInput(t *testing.T) {
	c := NewChunker(NewTokens())
	chunks := c.Chunk("", ChunkOpts{Strategy: "char", Size: 100})
	if len(chunks) != 0 {
		t.Fatalf("empty input should yield no chunks, got %d", len(chunks))
	}
}

func TestAssembleContextFitsAll(t *testing.T) {
	c := NewChunker(NewTokens())
	res := c.AssembleContext("gpt-4o", 1000, []ContextSection{
		{ID: "sys", Text: "You are a helpful assistant.", Priority: 100},
		{ID: "user", Text: "What is the weather?", Priority: 50},
	})
	if len(res.Used) != 2 || len(res.Skipped) != 0 {
		t.Fatalf("expected all fit, got used=%v skipped=%v", res.Used, res.Skipped)
	}
}

func TestAssembleContextPriorityOrdering(t *testing.T) {
	c := NewChunker(NewTokens())
	// Tiny budget — only highest priority fits.
	long := strings.Repeat("Lorem ipsum dolor sit amet. ", 100)
	res := c.AssembleContext("gpt-4o", 30, []ContextSection{
		{ID: "low", Text: long, Priority: 10},
		{ID: "high", Text: "Critical: must include.", Priority: 100},
		{ID: "mid", Text: long, Priority: 50},
	})
	// "high" should be first in Used; "low"/"mid" in Skipped.
	if len(res.Used) == 0 || res.Used[0] != "high" {
		t.Fatalf("expected high priority first; got %v", res.Used)
	}
	for _, id := range res.Used {
		if id == "low" || id == "mid" {
			t.Fatalf("low/mid shouldn't fit in tight budget; used=%v", res.Used)
		}
	}
}

func TestAssembleContextCombinedFormat(t *testing.T) {
	c := NewChunker(NewTokens())
	res := c.AssembleContext("gpt-4o", 1000, []ContextSection{
		{ID: "a", Text: "hello", Priority: 2},
		{ID: "b", Text: "world", Priority: 1},
	})
	want := "hello\n\n---\n\nworld"
	if res.Combined != want {
		t.Fatalf("combined = %q, want %q", res.Combined, want)
	}
}
