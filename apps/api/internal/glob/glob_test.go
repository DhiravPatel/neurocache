package glob

import (
	"math/rand"
	"testing"
)

// refMatch is the previous exponential recursive matcher (rune-based),
// kept ONLY as the oracle to prove Match is semantically identical on the
// ASCII glob subset.
func refMatch(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if refMatch(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		case '[':
			closeIdx := -1
			for i := 1; i < len(p); i++ {
				if p[i] == ']' {
					closeIdx = i
					break
				}
			}
			if closeIdx == -1 || len(s) == 0 {
				return false
			}
			ok := false
			for _, r := range p[1:closeIdx] {
				if r == s[0] {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
			p, s = p[closeIdx+1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

// TestMatchEquivalence fuzzes Match against the reference recursive matcher
// over an ASCII alphabet rich in glob metacharacters.
func TestMatchEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(0x9e3779b1))
	alpha := []rune("ab*?[]")
	salpha := []rune("abc")
	randStr := func(maxLen int, set []rune) []rune {
		n := rng.Intn(maxLen + 1)
		out := make([]rune, n)
		for i := range out {
			out[i] = set[rng.Intn(len(set))]
		}
		return out
	}
	for i := 0; i < 200000; i++ {
		p := randStr(7, alpha)
		s := randStr(7, salpha)
		if got, want := Match(string(p), string(s)), refMatch(p, s); got != want {
			t.Fatalf("mismatch: pattern=%q str=%q got=%v want=%v", string(p), string(s), got, want)
		}
	}
}

// TestMatchLinearNoBlowup proves the pathological pattern that made the old
// matcher exponential now returns immediately (the test would hang
// otherwise).
func TestMatchLinearNoBlowup(t *testing.T) {
	pattern := "*a*a*a*a*a*a*a*a*a*a*a*a*a*a*a*a*b"
	s := make([]byte, 2000)
	for i := range s {
		s[i] = 'a'
	}
	if Match(pattern, string(s)) {
		t.Fatal("expected no match (no trailing b)")
	}
}

func TestMatchBasics(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"*", "anything", true},
		{"", "", true},
		{"", "x", false},
		{"a*c", "abbbc", true},
		{"a*c", "abbbd", false},
		{"h?llo", "hello", true},
		{"h?llo", "hllo", false},
		{"h[ae]llo", "hallo", true},
		{"h[ae]llo", "hbllo", false},
		{"user:*", "user:42", true},
		{"user:*", "admin:42", false},
		{"[ab", "a", false}, // malformed class never matches
	}
	for _, c := range cases {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q,%q)=%v want %v", c.p, c.s, got, c.want)
		}
	}
}

// TestMatchZeroAlloc proves matching allocates nothing (the win that makes
// KEYS over a large keyspace fast).
func TestMatchZeroAlloc(t *testing.T) {
	if n := testing.AllocsPerRun(1000, func() { Match("user:*:session", "user:4242:session") }); n != 0 {
		t.Fatalf("Match should be zero-alloc, got %v allocs/op", n)
	}
}
