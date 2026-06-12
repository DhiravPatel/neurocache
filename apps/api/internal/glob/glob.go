// Package glob is the single canonical glob matcher for NeuroCache —
// used by KEYS/SCAN, pub/sub pattern channels, and ACL key/channel
// patterns. It supports the Redis keyspace-pattern subset: '*' (any run),
// '?' (any single byte), '[set]' (any one listed byte), and literal
// bytes.
//
// Two properties matter:
//
//   - Linear time. It uses single-pass star backtracking — O(|p|·|s|)
//     worst case — NOT recursion-per-star, which is exponential. A pattern
//     like "*a*a*a*a*b" against a long string used to let a client pin a
//     CPU for seconds (a ReDoS-class DoS) through KEYS, PSUBSCRIBE, or an
//     ACL pattern.
//   - Zero allocation. It indexes the pattern and subject as bytes in
//     place (no []rune conversion), so matching the whole keyspace in KEYS
//     allocates nothing — the previous []rune-per-key form was a real GC
//     drag. Byte matching also matches Redis's binary-safe stringmatchlen
//     exactly (`?` is one byte, not one rune).
package glob

// Match reports whether s matches the glob pattern.
func Match(pattern, s string) bool {
	if pattern == "*" {
		return true // hot path: KEYS *, PSUBSCRIBE *, ACL ~*
	}
	pi, si := 0, 0
	star, starS := -1, 0
	for si < len(s) {
		if pi < len(pattern) && pattern[pi] == '*' {
			star, starS = pi, si
			pi++
			continue
		}
		if matched, tl, ok := matchToken(pattern, pi, s[si]); ok && matched {
			pi += tl
			si++
			continue
		}
		// Current token didn't match (or pattern exhausted / malformed):
		// backtrack to the last '*' and let it swallow one more byte.
		if star >= 0 {
			starS++
			si = starS
			pi = star + 1
			continue
		}
		return false
	}
	// Subject consumed; the rest of the pattern must be all '*'.
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// matchToken reports whether the single-byte token at p[pi] matches byte
// c, plus the token's length. ok=false means there is no usable token at
// pi (pattern exhausted, a bare '*' the caller handles, or a malformed
// '[' with no ']' — which can never match).
func matchToken(p string, pi int, c byte) (matched bool, tokenLen int, ok bool) {
	if pi >= len(p) {
		return false, 0, false
	}
	switch p[pi] {
	case '*':
		return false, 0, false
	case '?':
		return true, 1, true
	case '[':
		closeIdx := -1
		for i := pi + 1; i < len(p); i++ {
			if p[i] == ']' {
				closeIdx = i
				break
			}
		}
		if closeIdx == -1 {
			return false, 0, false
		}
		for i := pi + 1; i < closeIdx; i++ {
			if p[i] == c {
				return true, closeIdx - pi + 1, true
			}
		}
		return false, closeIdx - pi + 1, true
	default:
		return p[pi] == c, 1, true
	}
}
