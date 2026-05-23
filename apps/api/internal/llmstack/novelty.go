package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// NoveltyDetector is the per-request complement to DRIFT.* (which
// is aggregate distribution-shift monitoring). NOVELTY answers
// "is THIS specific input unlike anything we've seen?" — a gate
// every pipeline-grade RAG / agent system needs: novel input
// means cache lookups won't help, RAG coverage is uncertain, and
// the response should probably skip caching + escalate to human
// review.
//
// Commands:
//
//   NOVELTY.BASELINE detector-id text1 text2 ...
//        Seed the in-distribution baseline. Apps populate from
//        a representative sample of normal traffic.
//
//   NOVELTY.ADD detector-id text
//        Incrementally extend the baseline (the more recent
//        normal-traffic examples teach the gate that this is fine).
//
//   NOVELTY.SCORE detector-id text
//        → [score, verdict, nearest_score, nearest_text]
//        score = 1 - max(cosine(text, baseline)).
//        verdict: in_distribution (<0.30) / borderline (<0.55)
//                 / novel (>=0.55)
//
//   NOVELTY.SET_THRESHOLDS detector-id ok bad
//   NOVELTY.SIZE detector-id
//   NOVELTY.FORGET detector-id
//   NOVELTY.STATS
//
// Storage: per-detector array of (text, L2-normalised vec).
// SCORE is O(N) cosine — at typical baselines of 1k-10k examples
// it's 0.5-5 ms; apps cache the verdict per session to amortise.
type NoveltyDetector struct {
	mu        sync.RWMutex
	detectors map[string]*noveltyState

	totalScores      atomic.Int64
	totalNovel       atomic.Int64
	totalInDist      atomic.Int64
	totalBorderline  atomic.Int64
}

type noveltyState struct {
	id       string
	mu       sync.RWMutex
	baseline []noveltyExample
	dim      int
	thOK     float64
	thBad    float64
}

type noveltyExample struct {
	text string
	vec  []float64
}

// NewNoveltyDetector returns an empty registry.
func NewNoveltyDetector() *NoveltyDetector {
	return &NoveltyDetector{detectors: map[string]*noveltyState{}}
}

// Baseline (re)initialises a detector from a representative
// sample of normal traffic. Default thresholds: ok=0.30, bad=0.55.
func (n *NoveltyDetector) Baseline(detectorID string, texts []string) error {
	if detectorID == "" {
		return errors.New("detector_id required")
	}
	if len(texts) == 0 {
		return errors.New("at least one baseline text required")
	}
	st := &noveltyState{id: detectorID, thOK: 0.30, thBad: 0.55}
	for _, t := range texts {
		if t == "" {
			continue
		}
		vec := embedFallback(t)
		if st.dim == 0 {
			st.dim = len(vec)
		}
		st.baseline = append(st.baseline, noveltyExample{text: t, vec: vec})
	}
	if len(st.baseline) == 0 {
		return errors.New("no non-empty baseline texts")
	}
	n.mu.Lock()
	n.detectors[detectorID] = st
	n.mu.Unlock()
	return nil
}

// Add appends to the baseline. Apps call this to teach the
// detector that a previously-novel input has been seen enough
// times to count as normal.
func (n *NoveltyDetector) Add(detectorID, text string) error {
	if text == "" {
		return errors.New("text required")
	}
	n.mu.RLock()
	st, ok := n.detectors[detectorID]
	n.mu.RUnlock()
	if !ok {
		return errors.New("unknown detector_id: " + detectorID)
	}
	vec := embedFallback(text)
	if st.dim != 0 && len(vec) != st.dim {
		return errors.New("baseline embedding dim mismatch")
	}
	st.mu.Lock()
	st.baseline = append(st.baseline, noveltyExample{text: text, vec: vec})
	st.mu.Unlock()
	return nil
}

// SetThresholds adjusts the gate. bad must be > ok.
func (n *NoveltyDetector) SetThresholds(detectorID string, ok, bad float64) error {
	if bad <= ok {
		return errors.New("bad threshold must be > ok")
	}
	n.mu.RLock()
	st, found := n.detectors[detectorID]
	n.mu.RUnlock()
	if !found {
		return errors.New("unknown detector_id: " + detectorID)
	}
	st.mu.Lock()
	st.thOK = ok
	st.thBad = bad
	st.mu.Unlock()
	return nil
}

// NoveltyScore is what SCORE returns.
type NoveltyScore struct {
	Score        float64 `json:"score"`
	Verdict      string  `json:"verdict"`
	NearestScore float64 `json:"nearest_score"`
	NearestText  string  `json:"nearest_text"`
}

// Score returns the per-query novelty verdict.
func (n *NoveltyDetector) Score(detectorID, text string) (NoveltyScore, bool) {
	n.totalScores.Add(1)
	n.mu.RLock()
	st, ok := n.detectors[detectorID]
	n.mu.RUnlock()
	if !ok {
		return NoveltyScore{}, false
	}
	vec := embedFallback(text)
	st.mu.RLock()
	if len(st.baseline) == 0 || (st.dim != 0 && len(vec) != st.dim) {
		st.mu.RUnlock()
		return NoveltyScore{Score: 1, Verdict: "novel"}, true
	}
	bestIdx := 0
	bestSim := 0.0
	for i, ex := range st.baseline {
		s := dotProduct(vec, ex.vec)
		if s > bestSim {
			bestSim = s
			bestIdx = i
		}
	}
	nearestText := st.baseline[bestIdx].text
	thOK := st.thOK
	thBad := st.thBad
	st.mu.RUnlock()

	score := 1 - bestSim
	verdict := noveltyVerdict(score, thOK, thBad)
	switch verdict {
	case "novel":
		n.totalNovel.Add(1)
	case "in_distribution":
		n.totalInDist.Add(1)
	default:
		n.totalBorderline.Add(1)
	}
	return NoveltyScore{
		Score: score, Verdict: verdict,
		NearestScore: bestSim, NearestText: nearestText,
	}, true
}

// Size returns the baseline cardinality.
func (n *NoveltyDetector) Size(detectorID string) (int, bool) {
	n.mu.RLock()
	st, ok := n.detectors[detectorID]
	n.mu.RUnlock()
	if !ok {
		return 0, false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.baseline), true
}

// Forget drops a detector entirely.
func (n *NoveltyDetector) Forget(detectorID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.detectors[detectorID]
	delete(n.detectors, detectorID)
	return ok
}

// Detectors returns every registered detector id with its baseline
// size + thresholds.
type NoveltyDetectorRow struct {
	DetectorID   string  `json:"detector_id"`
	BaselineSize int     `json:"baseline_size"`
	ThresholdOK  float64 `json:"threshold_ok"`
	ThresholdBad float64 `json:"threshold_bad"`
}

func (n *NoveltyDetector) Detectors() []NoveltyDetectorRow {
	n.mu.RLock()
	out := make([]NoveltyDetectorRow, 0, len(n.detectors))
	for id, st := range n.detectors {
		st.mu.RLock()
		out = append(out, NoveltyDetectorRow{
			DetectorID:   id,
			BaselineSize: len(st.baseline),
			ThresholdOK:  st.thOK,
			ThresholdBad: st.thBad,
		})
		st.mu.RUnlock()
	}
	n.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].DetectorID < out[j].DetectorID })
	return out
}

// NoveltyStats is the global snapshot.
type NoveltyStats struct {
	Detectors        int   `json:"detectors"`
	TotalScores      int64 `json:"total_scores"`
	TotalInDist      int64 `json:"total_in_distribution"`
	TotalBorderline  int64 `json:"total_borderline"`
	TotalNovel       int64 `json:"total_novel"`
}

func (n *NoveltyDetector) Stats() NoveltyStats {
	n.mu.RLock()
	num := len(n.detectors)
	n.mu.RUnlock()
	return NoveltyStats{
		Detectors:       num,
		TotalScores:     n.totalScores.Load(),
		TotalInDist:     n.totalInDist.Load(),
		TotalBorderline: n.totalBorderline.Load(),
		TotalNovel:      n.totalNovel.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func noveltyVerdict(score, ok, bad float64) string {
	switch {
	case score < ok:
		return "in_distribution"
	case score < bad:
		return "borderline"
	default:
		return "novel"
	}
}
