package llmstack

import (
	"crypto/sha1"
	"encoding/hex"
	"sync"
	"sync/atomic"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
)

// GroundVerifier is the SEMANTIC groundedness scorer behind GROUND.VERIFY
// and GROUND.REQUIRE. It complements the lexical GroundChecker (GROUND.CHECK,
// Jaccard n-gram overlap): where the lexical pass catches fabricated facts /
// entity swaps / invented numbers, the semantic pass catches the
// paraphrase-but-supported case the lexical pass under-scores — and, more
// importantly, it produces a single doc-level support score that feeds
// RISK.BUDGET.DEBIT automatically (GROUND.REQUIRE), closing the loop that
// previously left the risk score for the client to compute by hand.
//
// Algorithm — cosine sentence-to-chunk alignment:
//
//   1. Split the answer into sentence-sized claims (shared splitClaims).
//   2. Embed each claim and each context chunk with the project embedder.
//   3. Each claim's support = its max cosine similarity to any chunk.
//   4. doc_score = the MIN claim support (worst sentence) — one unsupported
//      sentence drags the whole answer down, mirroring the lexical checker's
//      deliberate worst-case philosophy.
//
// A claim is "unsupported" when its support falls below the caller's
// min_support; the answer is "grounded" when every claim clears the bar.
// NLI cross-encoders are the natural upgrade path — the command surface
// doesn't change when a stronger scorer is dropped in.
//
// All embeddings are non-negative feature-hashed unit vectors, so cosine
// lands in [0,1]; we clamp defensively regardless.
type GroundVerifier struct {
	dim int

	totalVerify  atomic.Int64
	totalRequire atomic.Int64
	totalPass    atomic.Int64 // grounded results
	totalFail    atomic.Int64 // not-grounded results

	// scorer selects how a claim's support is computed: "cosine" (the
	// built-in feature-hash alignment) or "extern" (entailment scores an
	// external NLI model supplies via Ingest). In extern mode a claim with
	// no ingested score falls back to cosine, so the surface degrades
	// gracefully and a caller can mix the two. extern maps the answer's
	// content hash → sentence index → entailment score in [0,1].
	mu      sync.RWMutex
	scorer  string
	extern  map[string]map[int]float64
	ingests atomic.Int64
}

// NewGroundVerifier builds a verifier embedding at dim (defaults to 384,
// matching the rest of the semantic layer).
func NewGroundVerifier(dim int) *GroundVerifier {
	if dim <= 0 {
		dim = 384
	}
	return &GroundVerifier{dim: dim, scorer: ScorerCosine, extern: map[string]map[int]float64{}}
}

// Scorer modes.
const (
	ScorerCosine = "cosine"
	ScorerExtern = "extern"
)

// SetScorer switches the active scoring mode. Returns false for an unknown
// mode (the current mode is left unchanged).
func (g *GroundVerifier) SetScorer(mode string) bool {
	if mode != ScorerCosine && mode != ScorerExtern {
		return false
	}
	g.mu.Lock()
	g.scorer = mode
	g.mu.Unlock()
	return true
}

// CurrentScorer returns the active scoring mode.
func (g *GroundVerifier) CurrentScorer() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.scorer
}

// Ingest records an external entailment score for one sentence of an answer.
// The answer is keyed by a stable hash of its text, and idx is the 0-based
// sentence index (matching the splitClaims order GROUND.VERIFY uses). score
// is clamped to [0,1].
func (g *GroundVerifier) Ingest(answer string, idx int, score float64) {
	if idx < 0 {
		return
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	h := answerHash(answer)
	g.mu.Lock()
	m := g.extern[h]
	if m == nil {
		m = map[int]float64{}
		g.extern[h] = m
	}
	m[idx] = score
	g.mu.Unlock()
	g.ingests.Add(1)
}

// externScores returns (the per-sentence map for answer, true) when extern
// mode is active and at least one score has been ingested for it.
func (g *GroundVerifier) externScores(answer string) (map[int]float64, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.scorer != ScorerExtern {
		return nil, false
	}
	m, ok := g.extern[answerHash(answer)]
	return m, ok && len(m) > 0
}

func answerHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

const defaultMinSupport = 0.5

// SentenceSupport is one claim's grounding result.
type SentenceSupport struct {
	Sentence  string  `json:"sentence"`
	Support   float64 `json:"support"`    // max cosine to any chunk, [0,1]
	BestChunk int     `json:"best_chunk"` // 0-indexed; -1 if no context
	Supported bool    `json:"supported"`  // support >= min_support
}

// VerifyResult is the GROUND.VERIFY / GROUND.REQUIRE return.
type VerifyResult struct {
	DocScore    float64           `json:"doc_score"`  // worst claim support
	MeanScore   float64           `json:"mean_score"` // average claim support
	MinSupport  float64           `json:"min_support"`
	Grounded    bool              `json:"grounded"` // every claim supported
	Sentences   []SentenceSupport `json:"sentences"`
	Unsupported []string          `json:"unsupported"`
}

// Verify scores answer against context and returns per-claim support. Pure
// read of the scorer (only counters move). minSupport <= 0 falls back to
// the default 0.5.
func (g *GroundVerifier) Verify(answer string, context []string, minSupport float64) VerifyResult {
	g.totalVerify.Add(1)
	return g.score(answer, context, minSupport)
}

// Require runs the same scoring as Verify but is the gate variant: callers
// treat the Grounded flag as an admit/regenerate decision, and the engine
// feeds DocScore into RISK.BUDGET.DEBIT. Tracks pass/fail counters.
func (g *GroundVerifier) Require(answer string, context []string, minSupport float64) VerifyResult {
	g.totalRequire.Add(1)
	res := g.score(answer, context, minSupport)
	if res.Grounded {
		g.totalPass.Add(1)
	} else {
		g.totalFail.Add(1)
	}
	return res
}

func (g *GroundVerifier) score(answer string, context []string, minSupport float64) VerifyResult {
	if minSupport <= 0 {
		minSupport = defaultMinSupport
	}
	sentences := splitClaims(answer)
	if len(sentences) == 0 {
		// Nothing to ground — vacuously grounded. Apps usually short
		// circuit before this, but be explicit rather than divide by zero.
		return VerifyResult{DocScore: 1, MeanScore: 1, MinSupport: minSupport, Grounded: true}
	}

	chunkVecs := make([][]float32, len(context))
	for i, c := range context {
		chunkVecs[i] = vector.Embed(c, g.dim)
	}

	// In extern mode, an external NLI model's per-sentence entailment scores
	// override the cosine pass for any sentence it covers.
	extern, hasExtern := g.externScores(answer)

	res := VerifyResult{MinSupport: minSupport, Sentences: make([]SentenceSupport, 0, len(sentences))}
	doc := 1.0
	sum := 0.0
	for idx, s := range sentences {
		best := -1
		bestScore := 0.0
		if hasExtern {
			if es, ok := extern[idx]; ok {
				best = -1 // external score is not tied to a single chunk
				bestScore = es
			} else {
				best, bestScore = bestChunk(vector.Embed(s, g.dim), chunkVecs)
			}
		} else {
			best, bestScore = bestChunk(vector.Embed(s, g.dim), chunkVecs)
		}
		if bestScore < 0 {
			bestScore = 0
		}
		if bestScore > 1 {
			bestScore = 1
		}
		supported := bestScore >= minSupport
		res.Sentences = append(res.Sentences, SentenceSupport{
			Sentence: s, Support: bestScore, BestChunk: best, Supported: supported,
		})
		if !supported {
			res.Unsupported = append(res.Unsupported, s)
		}
		if bestScore < doc {
			doc = bestScore
		}
		sum += bestScore
	}
	res.DocScore = doc
	res.MeanScore = sum / float64(len(sentences))
	res.Grounded = len(res.Unsupported) == 0
	return res
}

// bestChunk returns the index + cosine score of the chunk most similar to the
// sentence vector (best=-1 when there are no chunks).
func bestChunk(sv []float32, chunkVecs [][]float32) (int, float64) {
	best := -1
	bestScore := 0.0
	for i, cv := range chunkVecs {
		sc := float64(vector.Cosine(sv, cv))
		if sc > bestScore {
			bestScore = sc
			best = i
		}
	}
	return best, bestScore
}

// GroundVerifyStats is the GROUND.VSTATS snapshot.
type GroundVerifyStats struct {
	Dim          int    `json:"dim"`
	Scorer       string `json:"scorer"`
	TotalVerify  int64  `json:"total_verify"`
	TotalRequire int64  `json:"total_require"`
	TotalPass    int64  `json:"total_pass"`
	TotalFail    int64  `json:"total_fail"`
	ExternScores int64  `json:"extern_scores"`
}

func (g *GroundVerifier) Stats() GroundVerifyStats {
	return GroundVerifyStats{
		Dim:          g.dim,
		Scorer:       g.CurrentScorer(),
		TotalVerify:  g.totalVerify.Load(),
		TotalRequire: g.totalRequire.Load(),
		TotalPass:    g.totalPass.Load(),
		TotalFail:    g.totalFail.Load(),
		ExternScores: g.ingests.Load(),
	}
}
