package llmstack

import "math"

// float64 ↔ uint64 bit-cast helpers. We store float64 cost figures in
// atomic.Uint64 so reads on the hot path stay lock-free.
func float64Bits(f float64) uint64 { return math.Float64bits(f) }
func bitsFloat64(b uint64) float64 { return math.Float64frombits(b) }

// approxTokens estimates token count using the OpenAI cookbook fallback
// of ~4 chars per token. Real production should plug in a BPE tokenizer
// when integrating with a specific model; this gives a reasonable
// budget signal without dragging tiktoken into the binary today.
func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	n := (len(s) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}
