package llmstack

import (
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// WatermarkDetector scores text for "was this AI-generated?" using
// statistical fingerprints LLMs over-produce. Real production pain
// for trust & safety teams, content moderation, plagiarism systems,
// and AI-assistance-disclosure compliance. No off-the-shelf cache
// product covers this.
//
// The detector is INTENTIONALLY a fast pre-filter, not a perfect
// classifier — apps escalate borderline scores to a real classifier
// (HuggingFace AI-detect, GPTZero, etc.). The cache's job is to
// triage the firehose at <50 µs per call.
//
// Signals (weighted, summed to 0..1):
//
//   1. AI vocabulary frequency — words LLMs over-produce in
//      proportion to their training data ("delve", "tapestry",
//      "navigating", "moreover", "furthermore", "comprehensive",
//      "intricate", "facilitate", "leverage", "robust").
//
//   2. Em-dash density — LLMs use em-dashes far more than typical
//      human writing.
//
//   3. Bullet list structure — disproportionately frequent in LLM
//      output.
//
//   4. Paragraph length uniformity — LLMs produce paragraphs of
//      remarkably consistent length.
//
//   5. Adjective-noun ratio — LLMs over-modify nouns vs. typical
//      English.
//
// Commands:
//
//   WATERMARK.SCORE text
//        → {score 0..1, verdict, signals[]}
//   WATERMARK.PATTERN.ADD name regex weight
//        Register a custom detection regex (positive weight =
//        adds AI signal; negative weight = subtracts).
//   WATERMARK.PATTERN.REMOVE name
//   WATERMARK.PATTERN.LIST
//   WATERMARK.STATS
//
// Pure compute, atomic counters. Custom patterns compiled once at
// REGISTER and cached.
type WatermarkDetector struct {
	mu       sync.RWMutex
	custom   map[string]*watermarkPattern // name -> pattern

	totalScores atomic.Int64
	totalLikely atomic.Int64
}

type watermarkPattern struct {
	name   string
	source string
	re     *regexp.Regexp
	weight float64
}

// NewWatermarkDetector returns a detector with the default pattern
// library ready.
func NewWatermarkDetector() *WatermarkDetector {
	return &WatermarkDetector{custom: map[string]*watermarkPattern{}}
}

// AI-typical vocabulary. Lowercased keys. Weights reflect how
// over-represented the word is in LLM output vs. natural English.
var aiVocab = map[string]float64{
	"delve":           0.85,
	"tapestry":        0.85,
	"navigating":      0.65,
	"navigate":        0.45,
	"intricate":       0.70,
	"intricacies":     0.75,
	"moreover":        0.55,
	"furthermore":     0.55,
	"additionally":    0.40,
	"comprehensive":   0.50,
	"comprehensively": 0.60,
	"facilitate":      0.45,
	"facilitates":     0.45,
	"leverage":        0.45,
	"leveraging":      0.55,
	"robust":          0.40,
	"holistic":        0.55,
	"realm":           0.55,
	"realms":          0.55,
	"underscores":     0.55,
	"underpinning":    0.60,
	"plethora":        0.65,
	"myriad":          0.55,
	"paradigm":        0.50,
	"crucially":       0.50,
	"notably":         0.40,
	"essentially":     0.30,
	"showcasing":      0.50,
	"unparalleled":    0.50,
}

// ScoreSignal is one signal's contribution.
type ScoreSignal struct {
	Name        string  `json:"name"`
	Contribution float64 `json:"contribution"` // 0..1 raw per-signal score
	Weight      float64 `json:"weight"`       // weight in the final sum
}

// WatermarkScore is the SCORE return.
type WatermarkScore struct {
	Score   float64        `json:"score"`   // 0..1, 1 = very likely AI
	Verdict string         `json:"verdict"` // human / unclear / ai
	Signals []ScoreSignal  `json:"signals"`
	Words   int            `json:"words"`
}

// Score computes the AI-likelihood score and signal breakdown.
func (w *WatermarkDetector) Score(text string) WatermarkScore {
	w.totalScores.Add(1)
	tokens := tokenize(text)
	if len(tokens) < 5 {
		// Too short to score reliably — return neutral.
		return WatermarkScore{Score: 0.5, Verdict: "unclear", Words: len(tokens)}
	}

	signals := []ScoreSignal{}

	// 1. AI vocabulary frequency
	vocabHits := 0.0
	for _, t := range tokens {
		if w, ok := aiVocab[t]; ok {
			vocabHits += w
		}
	}
	vocabScore := vocabHits / float64(len(tokens)) * 100 // normalised
	if vocabScore > 1 {
		vocabScore = 1
	}
	signals = append(signals, ScoreSignal{
		Name: "ai_vocabulary", Contribution: vocabScore, Weight: 0.40,
	})

	// 2. Em-dash density
	emDashCount := strings.Count(text, "—") + strings.Count(text, "–")
	emDashDensity := float64(emDashCount) / float64(len(tokens)) * 50
	if emDashDensity > 1 {
		emDashDensity = 1
	}
	signals = append(signals, ScoreSignal{
		Name: "em_dash_density", Contribution: emDashDensity, Weight: 0.15,
	})

	// 3. Bullet list density
	bulletLines := 0
	totalLines := 0
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		totalLines++
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") ||
			strings.HasPrefix(l, "• ") || isNumberedBullet(l) {
			bulletLines++
		}
	}
	bulletDensity := 0.0
	if totalLines >= 3 {
		bulletDensity = float64(bulletLines) / float64(totalLines)
	}
	signals = append(signals, ScoreSignal{
		Name: "bullet_density", Contribution: bulletDensity, Weight: 0.15,
	})

	// 4. Paragraph length uniformity (low variance → LLM)
	paraScore := paragraphUniformityScore(text)
	signals = append(signals, ScoreSignal{
		Name: "paragraph_uniformity", Contribution: paraScore, Weight: 0.10,
	})

	// 5. Adjective-ish word ratio (heuristic: -ing / -ly / common adj endings)
	adjScore := adjectiveDensityScore(tokens)
	signals = append(signals, ScoreSignal{
		Name: "modifier_density", Contribution: adjScore, Weight: 0.10,
	})

	// 6. Custom patterns
	w.mu.RLock()
	customScore := 0.0
	customWeightSum := 0.0
	for _, p := range w.custom {
		hits := len(p.re.FindAllString(text, -1))
		if hits == 0 {
			continue
		}
		c := float64(hits) / float64(len(tokens)) * 100
		if c > 1 {
			c = 1
		}
		customScore += c * p.weight
		customWeightSum += p.weight
	}
	w.mu.RUnlock()
	if customWeightSum > 0 {
		signals = append(signals, ScoreSignal{
			Name: "custom_patterns", Contribution: customScore / customWeightSum,
			Weight: 0.10,
		})
	}

	// Weighted sum
	total := 0.0
	weightSum := 0.0
	for _, s := range signals {
		total += s.Contribution * s.Weight
		weightSum += s.Weight
	}
	score := 0.0
	if weightSum > 0 {
		score = total / weightSum
	}
	verdict := "unclear"
	if score >= 0.55 {
		verdict = "ai"
		w.totalLikely.Add(1)
	} else if score < 0.30 {
		verdict = "human"
	}
	return WatermarkScore{
		Score: score, Verdict: verdict, Signals: signals, Words: len(tokens),
	}
}

// AddPattern registers a custom regex. weight may be negative (e.g.
// "casual typos like 'gonna' subtract AI signal").
func (w *WatermarkDetector) AddPattern(name, source string, weight float64) error {
	re, err := regexp.Compile(source)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.custom[name] = &watermarkPattern{name: name, source: source, re: re, weight: weight}
	w.mu.Unlock()
	return nil
}

// RemovePattern drops a custom pattern. Returns true if it existed.
func (w *WatermarkDetector) RemovePattern(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.custom[name]
	delete(w.custom, name)
	return ok
}

// PatternRow is one row of WATERMARK.PATTERN.LIST.
type WMPatternRow struct {
	Name   string  `json:"name"`
	Source string  `json:"source"`
	Weight float64 `json:"weight"`
}

// Patterns returns every custom pattern.
func (w *WatermarkDetector) Patterns() []WMPatternRow {
	w.mu.RLock()
	out := make([]WMPatternRow, 0, len(w.custom))
	for _, p := range w.custom {
		out = append(out, WMPatternRow{Name: p.name, Source: p.source, Weight: p.weight})
	}
	w.mu.RUnlock()
	return out
}

// WatermarkStats is the global counters snapshot.
type WatermarkStats struct {
	CustomPatterns int   `json:"custom_patterns"`
	TotalScores    int64 `json:"total_scores"`
	TotalLikelyAI  int64 `json:"total_likely_ai"`
}

func (w *WatermarkDetector) Stats() WatermarkStats {
	w.mu.RLock()
	n := len(w.custom)
	w.mu.RUnlock()
	return WatermarkStats{
		CustomPatterns: n,
		TotalScores:    w.totalScores.Load(),
		TotalLikelyAI:  w.totalLikely.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func isNumberedBullet(s string) bool {
	// "1." / "12." / "1)" at start
	if len(s) < 2 {
		return false
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) {
		return false
	}
	c := s[i]
	if (c == '.' || c == ')') && (i+1 < len(s) && s[i+1] == ' ') {
		return true
	}
	return false
}

func paragraphUniformityScore(text string) float64 {
	paragraphs := strings.Split(text, "\n\n")
	if len(paragraphs) < 3 {
		return 0
	}
	lengths := make([]int, 0, len(paragraphs))
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p != "" {
			lengths = append(lengths, len(p))
		}
	}
	if len(lengths) < 3 {
		return 0
	}
	// Compute mean
	sum := 0
	for _, l := range lengths {
		sum += l
	}
	mean := float64(sum) / float64(len(lengths))
	if mean == 0 {
		return 0
	}
	// Coefficient of variation
	varSum := 0.0
	for _, l := range lengths {
		d := float64(l) - mean
		varSum += d * d
	}
	cv := (varSum / float64(len(lengths))) / (mean * mean)
	// Lower CV = more uniform = higher AI score
	// CV < 0.05 → high uniformity → score 1.0
	// CV > 0.5  → low uniformity  → score 0.0
	score := 1.0 - cv*2.0
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score
}

func adjectiveDensityScore(tokens []string) float64 {
	if len(tokens) == 0 {
		return 0
	}
	adj := 0
	for _, t := range tokens {
		if isLikelyAdjective(t) {
			adj++
		}
	}
	density := float64(adj) / float64(len(tokens))
	// Human writing typically 6-10% adjectives; LLM output often 14%+
	// Map 0.06→0.0 and 0.16→1.0
	score := (density - 0.06) / 0.10
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score
}

func isLikelyAdjective(t string) bool {
	if len(t) < 5 {
		return false
	}
	for _, r := range t {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	// Heuristic suffixes
	suffixes := []string{"ive", "ous", "ful", "less", "able", "ible", "ical", "istic", "ant", "ent"}
	for _, s := range suffixes {
		if strings.HasSuffix(t, s) {
			return true
		}
	}
	return false
}
