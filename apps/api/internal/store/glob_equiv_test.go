package store

import (
	"math/rand"
	"testing"
)

// oldMatchRunes is the previous exponential recursive matcher, kept here
// ONLY as the reference oracle to prove the new linear matchRunes is
// semantically identical. Do not use it outside the test.
func oldMatchRunes(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if oldMatchRunes(p[1:], s[i:]) {
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

// TestGlobMatchEquivalence fuzzes the new linear matcher against the old
// recursive one over a small alphabet rich in glob metacharacters, so any
// behavioural drift (including `[set]` and `?` edge cases) is caught.
func TestGlobMatchEquivalence(t *testing.T) {
	// Deterministic seed (no time-based randomness) so failures reproduce.
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
		if got, want := matchRunes(p, s), oldMatchRunes(p, s); got != want {
			t.Fatalf("mismatch: pattern=%q str=%q new=%v old=%v", string(p), string(s), got, want)
		}
	}
}

// TestGlobMatchLinearNoBlowup proves the pathological pattern that made the
// old matcher exponential now returns quickly. If matchRunes were still
// exponential this test would hang; the test harness timeout catches that.
func TestGlobMatchLinearNoBlowup(t *testing.T) {
	pattern := []rune("*a*a*a*a*a*a*a*a*a*a*a*a*a*a*a*a*b")
	s := make([]rune, 1000)
	for i := range s {
		s[i] = 'a'
	}
	if matchRunes(pattern, s) {
		t.Fatal("expected no match (string has no trailing b)")
	}
}

// TestGlobMatchBasics pins concrete cases.
func TestGlobMatchBasics(t *testing.T) {
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
		{"foo*", "foobar", true},
		{"*bar", "foobar", true},
		{"user:*", "user:42", true},
		{"user:*", "admin:42", false},
	}
	for _, c := range cases {
		if got := globMatch(c.p, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.p, c.s, got, c.want)
		}
	}
}
