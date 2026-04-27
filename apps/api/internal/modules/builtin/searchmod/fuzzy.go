package searchmod

// levenshtein returns the edit distance between a and b, clamped at
// `cutoff` so it can early-exit. Allocates one row of `len(b)+1` ints —
// fine for the typical 4–20 character vocabulary terms searches face.
//
// Returns `cutoff` when the actual distance is ≥ cutoff so callers can
// reject without paying for the full DP.
func levenshtein(a, b string, cutoff int) int {
	if cutoff <= 0 {
		if a == b {
			return 0
		}
		return 1
	}
	la, lb := len(a), len(b)
	if abs(la-lb) >= cutoff {
		return cutoff
	}
	if la == 0 {
		if lb < cutoff {
			return lb
		}
		return cutoff
	}
	if lb == 0 {
		if la < cutoff {
			return la
		}
		return cutoff
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
			if curr[j] < rowMin {
				rowMin = curr[j]
			}
		}
		if rowMin >= cutoff {
			return cutoff
		}
		prev, curr = curr, prev
	}
	if prev[lb] < cutoff {
		return prev[lb]
	}
	return cutoff
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func minInt3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
