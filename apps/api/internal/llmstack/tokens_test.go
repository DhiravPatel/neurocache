package llmstack

import "testing"

func TestTokensCountReasonable(t *testing.T) {
	tk := NewTokens()
	// "Hello, world!" is 13 chars — should estimate ~3-5 tokens.
	n := tk.Count("gpt-4o", "Hello, world!")
	if n < 2 || n > 6 {
		t.Fatalf("unexpected count for short text: %d", n)
	}
	// Large input should scale roughly linearly.
	long := ""
	for i := 0; i < 100; i++ {
		long += "The quick brown fox jumps over the lazy dog. "
	}
	n = tk.Count("gpt-4o", long)
	if n < 800 || n > 1400 {
		t.Fatalf("scaling check failed: %d tokens for 100 fox sentences", n)
	}
}

func TestTokensModelMultiplierApplied(t *testing.T) {
	tk := NewTokens()
	text := "The quick brown fox jumps over the lazy dog. " // ~10 tokens
	// Claude has higher mult, should produce more tokens than gpt-4o.
	gpt := tk.Count("gpt-4o", text)
	claude := tk.Count("claude-3-opus", text)
	if claude < gpt {
		t.Fatalf("claude(%d) should be ≥ gpt(%d)", claude, gpt)
	}
}

func TestTokensSplitFitsBudget(t *testing.T) {
	tk := NewTokens()
	long := ""
	for i := 0; i < 200; i++ {
		long += "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
	}
	chunks := tk.Split("gpt-4o", long, 100)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		got := tk.Count("gpt-4o", c)
		if got > 130 { // small slack for boundary alignment
			t.Fatalf("chunk %d: %d tokens, exceeds 100 budget", i, got)
		}
	}
}

func TestTokensSplitShortReturnsSingle(t *testing.T) {
	tk := NewTokens()
	chunks := tk.Split("gpt-4o", "short text", 1000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for short input, got %d", len(chunks))
	}
}

func TestTokensBudgetFitAndRecord(t *testing.T) {
	tk := NewTokens()
	tk.SetBudget("user:42", "gpt-4o", 100)

	r, ok := tk.FitAndRecord("user:42", "Hello, world!")
	if !ok || !r.Fits {
		t.Fatalf("first small request should fit: %+v ok=%v", r, ok)
	}
	if r.Remaining > 100 || r.Remaining < 0 {
		t.Fatalf("remaining out of range: %d", r.Remaining)
	}

	// Try to overflow with a huge text
	huge := ""
	for i := 0; i < 1000; i++ {
		huge += "lorem ipsum "
	}
	r, ok = tk.FitAndRecord("user:42", huge)
	if !ok || r.Fits {
		t.Fatalf("huge text should not fit: %+v ok=%v", r, ok)
	}
}

func TestTokensBudgetReset(t *testing.T) {
	tk := NewTokens()
	tk.SetBudget("u", "gpt-4o", 100)
	tk.FitAndRecord("u", "abc")
	tk.ResetBudget("u")
	st, _ := tk.Budget("u")
	if st.UsedTokens != 0 {
		t.Fatalf("after reset, used=%d", st.UsedTokens)
	}
}

func TestTokensBudgetMissing(t *testing.T) {
	tk := NewTokens()
	if _, ok := tk.FitAndRecord("ghost", "x"); ok {
		t.Fatal("expected false for unknown budget")
	}
	if _, ok := tk.Budget("ghost"); ok {
		t.Fatal("expected false for unknown budget")
	}
}

func BenchmarkTokensCount(b *testing.B) {
	tk := NewTokens()
	text := "The quick brown fox jumps over the lazy dog. Lorem ipsum dolor sit amet."
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tk.Count("gpt-4o", text)
	}
}
