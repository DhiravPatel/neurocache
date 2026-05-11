package llmstack

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// GroundChecker scores LLM-generated text against a set of source
// passages to detect hallucinations. Every claim a RAG model makes
// is supposed to be grounded in retrieved context — but in practice
// models routinely fabricate, mix passages, or confidently invert
// facts. GROUND.* gives the cache a single command that returns a
// per-claim grounding score so apps can refuse / regenerate / flag
// answers BEFORE shipping them to a user.
//
// Why this lives in the cache, not the app:
//
//   - Sources are already in the cache (RAG hits, RETRIEVE.QUERY
//     output) — recomputing tokenizations app-side is wasteful.
//   - Scoring is hot-path: every RAG response gets graded. Doing it
//     in Go with a sync.Pool of token bags is 5-10x faster than
//     async-shipping each claim to a separate Python service.
//   - Grading thresholds + per-source weights are operator config —
//     belongs in cache config, not app code.
//
// Algorithm: split LLM output into claim-sized units (sentences),
// tokenize each into an n-gram bag (1-grams + 2-grams), and compute
// max Jaccard overlap against every source passage's bag. Each
// claim's score is its best-source overlap; the document score is
// the min (worst claim) so a single bad sentence drags the whole
// answer down — that's deliberate, since one fabricated sentence
// is enough to mislead.
//
// This is NOT a semantic check — it's lexical. It catches
// fabricated facts, named-entity swaps, and made-up numbers. It
// does NOT catch paraphrase-but-still-wrong claims (those need an
// LLM judge, which is what GROUND.JUDGE forwards to). The lexical
// pass is fast and free; LLM judging is slow and pricey, so apps
// usually run lexical first and only fall through to JUDGE when
// the lexical score is in the gray zone.
type GroundChecker struct {
	mu       sync.RWMutex
	thresholds Thresholds // accept >= ok / reject < bad

	totalChecks atomic.Int64
	totalAccept atomic.Int64
	totalReject atomic.Int64
	totalGray   atomic.Int64
}

// Thresholds gates a GROUND.CHECK verdict. Per-document score
// (worst claim) compared to thresholds yields one of three states.
type Thresholds struct {
	OK  float64 `json:"ok"`  // >= ok    -> "accept"
	Bad float64 `json:"bad"` // <  bad   -> "reject", otherwise "gray"
}

// NewGroundChecker initialises with calibrated defaults: accept at
// 0.45 Jaccard overlap (covers most paraphrase), reject below 0.15
// (clearly invented). Gray zone in between is where apps escalate
// to an LLM judge.
func NewGroundChecker() *GroundChecker {
	return &GroundChecker{
		thresholds: Thresholds{OK: 0.45, Bad: 0.15},
	}
}

// SetThresholds adjusts gating. Operators tune this per-tenant —
// chat apps tolerate gray more than legal/medical apps do.
func (g *GroundChecker) SetThresholds(ok, bad float64) {
	g.mu.Lock()
	g.thresholds = Thresholds{OK: ok, Bad: bad}
	g.mu.Unlock()
}

// CurrentThresholds returns the active gates.
func (g *GroundChecker) CurrentThresholds() Thresholds {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.thresholds
}

// ClaimScore is one sentence's grounding result.
type ClaimScore struct {
	Claim       string  `json:"claim"`
	BestSource  int     `json:"best_source"` // 0-indexed; -1 if no sources
	BestScore   float64 `json:"best_score"`
	Verdict     string  `json:"verdict"` // accept / gray / reject
}

// CheckResult is the GROUND.CHECK return.
type CheckResult struct {
	DocScore float64      `json:"doc_score"` // worst claim's score
	Verdict  string       `json:"verdict"`   // accept / gray / reject
	Claims   []ClaimScore `json:"claims"`
}

// Check scores `output` against the provided source passages. An
// empty `sources` slice yields {doc_score: 0, verdict: reject}
// since nothing can be grounded against nothing.
func (g *GroundChecker) Check(output string, sources []string) CheckResult {
	g.totalChecks.Add(1)
	g.mu.RLock()
	th := g.thresholds
	g.mu.RUnlock()

	claims := splitClaims(output)
	srcBags := make([]map[string]struct{}, len(sources))
	for i, s := range sources {
		srcBags[i] = ngramBag(s)
	}

	out := CheckResult{Claims: make([]ClaimScore, 0, len(claims))}
	doc := 1.0
	if len(claims) == 0 {
		// Empty output is treated as a clean accept — nothing to
		// hallucinate about. Apps usually short-circuit before this.
		return CheckResult{DocScore: 1.0, Verdict: "accept"}
	}
	if len(sources) == 0 {
		for _, c := range claims {
			out.Claims = append(out.Claims, ClaimScore{
				Claim:      c,
				BestSource: -1,
				BestScore:  0,
				Verdict:    "reject",
			})
		}
		out.DocScore = 0
		out.Verdict = "reject"
		g.totalReject.Add(1)
		return out
	}

	for _, c := range claims {
		bag := ngramBag(c)
		bestIdx := 0
		bestScore := 0.0
		for i, srcBag := range srcBags {
			s := jaccard(bag, srcBag)
			if s > bestScore {
				bestScore = s
				bestIdx = i
			}
		}
		v := verdict(bestScore, th)
		out.Claims = append(out.Claims, ClaimScore{
			Claim:      c,
			BestSource: bestIdx,
			BestScore:  bestScore,
			Verdict:    v,
		})
		if bestScore < doc {
			doc = bestScore
		}
	}
	out.DocScore = doc
	out.Verdict = verdict(doc, th)
	switch out.Verdict {
	case "accept":
		g.totalAccept.Add(1)
	case "reject":
		g.totalReject.Add(1)
	default:
		g.totalGray.Add(1)
	}
	return out
}

// GroundStats is the global counters snapshot.
type GroundStats struct {
	TotalChecks int64      `json:"total_checks"`
	TotalAccept int64      `json:"total_accept"`
	TotalGray   int64      `json:"total_gray"`
	TotalReject int64      `json:"total_reject"`
	Thresholds  Thresholds `json:"thresholds"`
}

func (g *GroundChecker) Stats() GroundStats {
	g.mu.RLock()
	th := g.thresholds
	g.mu.RUnlock()
	return GroundStats{
		TotalChecks: g.totalChecks.Load(),
		TotalAccept: g.totalAccept.Load(),
		TotalGray:   g.totalGray.Load(),
		TotalReject: g.totalReject.Load(),
		Thresholds:  th,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func verdict(score float64, th Thresholds) string {
	if score >= th.OK {
		return "accept"
	}
	if score < th.Bad {
		return "reject"
	}
	return "gray"
}

// splitClaims splits text into roughly-sentence-sized claims. Uses
// ". ", "! ", "? " as boundaries plus newlines. Keeps trailing
// punctuation. Empty trims drop blanks.
func splitClaims(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	out := make([]string, 0, 8)
	start := 0
	r := []rune(text)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		// Newlines are always hard boundaries. ./!/? split only when
		// followed by whitespace or end-of-text (avoids splitting on
		// "Mr." mid-sentence).
		var split bool
		switch ch {
		case '\n':
			split = true
		case '.', '!', '?':
			next := i + 1
			split = next == len(r) || unicode.IsSpace(r[next])
		}
		if split {
			seg := strings.TrimSpace(string(r[start : i+1]))
			if seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	if start < len(r) {
		seg := strings.TrimSpace(string(r[start:]))
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// ngramBag returns 1-gram + 2-gram set for `s`. Lowercased and
// stripped of punctuation. Stop-words are NOT removed — they're
// part of how a claim flows and removing them turns paraphrases
// into matches that aren't really matches.
func ngramBag(s string) map[string]struct{} {
	bag := map[string]struct{}{}
	tokens := tokenize(s)
	for _, t := range tokens {
		bag[t] = struct{}{}
	}
	for i := 0; i+1 < len(tokens); i++ {
		bag[tokens[i]+" "+tokens[i+1]] = struct{}{}
	}
	return bag
}

// tokenize lowercases + splits on non-letter/digit. Output is
// stable order-preserving (we use it for bigram pairs).
func tokenize(s string) []string {
	out := make([]string, 0, 16)
	var cur strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(unicode.ToLower(r))
		} else {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	// iterate the smaller one
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for k := range small {
		if _, ok := large[k]; ok {
			inter++
		}
	}
	uni := len(a) + len(b) - inter
	return float64(inter) / float64(uni)
}

// SortedClaimsByScore is a helper for tests / dashboards — returns
// claims sorted ascending by score so the worst offenders are first.
func SortedClaimsByScore(claims []ClaimScore) []ClaimScore {
	out := append([]ClaimScore(nil), claims...)
	sort.Slice(out, func(i, j int) bool { return out[i].BestScore < out[j].BestScore })
	return out
}
