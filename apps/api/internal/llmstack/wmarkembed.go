package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// WatermarkEmbedder is the statistical text watermark embedder. The
// existing WATERMARK.* family is a *detector* (pattern matching for
// existing watermarks). This is the *embedder* — given a generated
// text, inject a statistically-recoverable provenance watermark so
// months later you can prove your system produced the text.
//
// Why this matters:
//
//   1. Leak attribution. A user pastes "your" output into a
//      competitor; the watermark proves where it came from.
//   2. Training-data contamination defense. Future models scraping
//      the web will inevitably ingest your output; the watermark
//      lets you measure (and exclude) your own contributions when
//      training your next model.
//   3. Compliance: EU AI Act draft language requires AI-generated
//      content to be marked.
//
// The approach is the Kirchenbauer-style "green list" scheme,
// implemented post-hoc on existing text:
//
//   - A keyed PRF partitions the vocabulary into "green" (preferred)
//     and "red" (avoided) tokens per position.
//   - To embed, we rewrite the text replacing some red-list words
//     with green-list synonyms (using a tiny built-in synonym dict).
//     The fraction of replacements is the "strength" knob.
//   - To detect, we count the green-token rate and z-score it against
//     the expected 50% baseline. A z-score > threshold (default 4)
//     means "this text came from a watermarker keyed by K." The
//     z-score is recoverable from the text alone + the secret key.
//
// We ship a small synonym dictionary (~150 common words) for
// post-hoc rewriting. Production users with a real LLM-side
// integration would push the watermark into sampling instead — but
// the post-hoc API still works as a content-attribution tool and
// catches the "I forgot to enable watermarking at sampling time"
// case.
//
// Commands:
//
//   WMARK.EMBED text [KEY secret] [STRENGTH 0..1]
//        → marked-text, replacements (count), green_rate
//   WMARK.DETECT text [KEY secret]
//        → green_rate, baseline, z_score, watermarked (bool),
//        confidence
//   WMARK.KEY register-id PUBLISH key
//        Save a known key for retrospective detection.
//   WMARK.KEYS                       — list registered keys
//   WMARK.DROPKEY register-id|ALL
//   WMARK.STATS
//
// The implementation is deterministic given the same key + input
// (important for reproducibility).
type WatermarkEmbedder struct {
	mu   sync.RWMutex
	keys map[string]string // register-id → key

	totalEmbeds  atomic.Int64
	totalDetects atomic.Int64
	totalMarks   atomic.Int64
}

// NewWatermarkEmbedder returns an empty registry.
func NewWatermarkEmbedder() *WatermarkEmbedder {
	return &WatermarkEmbedder{keys: map[string]string{}}
}

// Small synonym dictionary used for post-hoc rewriting. Each entry's
// first word is the "canonical" replacement; the rest may be replaced
// with the canonical given the right green/red partition. Picked for
// high-frequency interchangeable words where substitution is unlikely
// to change meaning.
var wmarkSynonyms = map[string][]string{
	"big":      {"big", "large", "huge"},
	"small":    {"small", "tiny", "little"},
	"good":     {"good", "great", "fine"},
	"bad":      {"bad", "poor", "awful"},
	"fast":     {"fast", "quick", "rapid"},
	"slow":     {"slow", "sluggish"},
	"happy":    {"happy", "glad", "joyful"},
	"sad":      {"sad", "unhappy"},
	"easy":     {"easy", "simple", "trivial"},
	"hard":     {"hard", "difficult", "tough"},
	"begin":    {"begin", "start", "commence"},
	"end":      {"end", "finish", "conclude"},
	"make":     {"make", "build", "create"},
	"do":       {"do", "perform", "execute"},
	"use":      {"use", "apply", "employ"},
	"get":      {"get", "obtain", "fetch"},
	"give":     {"give", "provide", "offer"},
	"show":     {"show", "display", "present"},
	"see":      {"see", "view", "observe"},
	"think":    {"think", "consider", "believe"},
	"know":     {"know", "understand", "grasp"},
	"want":     {"want", "wish", "desire"},
	"need":     {"need", "require"},
	"often":    {"often", "frequently"},
	"rarely":   {"rarely", "seldom"},
	"actually": {"actually", "indeed", "really"},
}

// reverseSynLookup: every word → its canonical synonym group key
var wmarkRevLookup map[string]string

func init() {
	wmarkRevLookup = make(map[string]string, 256)
	for k, vs := range wmarkSynonyms {
		for _, v := range vs {
			wmarkRevLookup[v] = k
		}
	}
}

// WmarkEmbedResult is EMBED's return.
type WmarkEmbedResult struct {
	Text         string  `json:"text"`
	Replacements int     `json:"replacements"`
	GreenRate    float64 `json:"green_rate"`
}

// Embed rewrites the text to bias it toward green-list synonyms per
// the supplied key. Strength controls how aggressive the replacement
// is (0=no change; 1=replace every replaceable word).
func (w *WatermarkEmbedder) Embed(text, key string, strength float64) (WmarkEmbedResult, error) {
	if text == "" {
		return WmarkEmbedResult{}, errors.New("text required")
	}
	if key == "" {
		return WmarkEmbedResult{}, errors.New("key required")
	}
	if strength < 0 || strength > 1 {
		return WmarkEmbedResult{}, errors.New("strength must be in [0,1]")
	}
	w.totalEmbeds.Add(1)
	tokens := tokenizeForWmark(text)
	keyHash := fnv1a32(key)
	replacements := 0
	greenSeen := 0
	totalSeen := 0
	for i, tok := range tokens {
		lower := strings.ToLower(tok.word)
		group, isSyn := wmarkRevLookup[lower]
		if !isSyn {
			continue
		}
		totalSeen++
		// Compute green/red partition for this position via PRF
		preferred := selectPreferred(wmarkSynonyms[group], keyHash, uint32(i))
		isGreen := lower == preferred
		if isGreen {
			greenSeen++
			continue
		}
		// Possibly replace if strength permits
		decideHash := fnv1a32(key + "|decide|" + lower)
		threshold := uint32(strength * float64(^uint32(0)))
		if decideHash < threshold {
			// Replace, preserve casing
			tokens[i].word = matchCase(tok.word, preferred)
			replacements++
			greenSeen++
		}
	}
	rate := 0.0
	if totalSeen > 0 {
		rate = float64(greenSeen) / float64(totalSeen)
	}
	w.totalMarks.Add(int64(replacements))
	return WmarkEmbedResult{
		Text: detokenize(tokens), Replacements: replacements, GreenRate: rate,
	}, nil
}

// WmarkDetectResult is DETECT's return.
type WmarkDetectResult struct {
	GreenRate    float64 `json:"green_rate"`
	Baseline     float64 `json:"baseline"`
	ZScore       float64 `json:"z_score"`
	N            int     `json:"n"`
	Watermarked  bool    `json:"watermarked"`
	Confidence   string  `json:"confidence"`
}

// Detect counts green-vs-red rate and z-scores against the 0.5
// baseline. Returns watermarked=true if z-score > 4 (≈ p < 1e-5 of
// being random — quite confident).
func (w *WatermarkEmbedder) Detect(text, key string) (WmarkDetectResult, error) {
	if text == "" {
		return WmarkDetectResult{}, errors.New("text required")
	}
	if key == "" {
		return WmarkDetectResult{}, errors.New("key required")
	}
	w.totalDetects.Add(1)
	tokens := tokenizeForWmark(text)
	keyHash := fnv1a32(key)
	green, total := 0, 0
	for i, tok := range tokens {
		lower := strings.ToLower(tok.word)
		group, ok := wmarkRevLookup[lower]
		if !ok {
			continue
		}
		total++
		preferred := selectPreferred(wmarkSynonyms[group], keyHash, uint32(i))
		if lower == preferred {
			green++
		}
	}
	out := WmarkDetectResult{Baseline: 0.5, N: total}
	if total == 0 {
		out.Confidence = "INSUFFICIENT"
		return out, nil
	}
	rate := float64(green) / float64(total)
	out.GreenRate = rate
	// z = (rate - 0.5) / sqrt(0.25/n)
	var stderr float64
	stderr = 0.5 / sqrtFast(float64(total))
	if stderr == 0 {
		out.Confidence = "INSUFFICIENT"
		return out, nil
	}
	out.ZScore = (rate - 0.5) / stderr
	out.Watermarked = out.ZScore > 4
	switch {
	case total < 20:
		out.Confidence = "LOW"
	case total < 100:
		out.Confidence = "MEDIUM"
	default:
		out.Confidence = "HIGH"
	}
	return out, nil
}

// Key registers a known key for retrospective detection.
func (w *WatermarkEmbedder) Key(registerID, key string) error {
	if registerID == "" || key == "" {
		return errors.New("register_id and key required")
	}
	w.mu.Lock()
	w.keys[registerID] = key
	w.mu.Unlock()
	return nil
}

// Keys lists registered key handles (does NOT return the key bytes).
func (w *WatermarkEmbedder) Keys() []string {
	w.mu.RLock()
	out := make([]string, 0, len(w.keys))
	for k := range w.keys {
		out = append(out, k)
	}
	w.mu.RUnlock()
	sort.Strings(out)
	return out
}

// DropKey drops a registered key.
func (w *WatermarkEmbedder) DropKey(registerID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if registerID == "ALL" {
		n := len(w.keys)
		w.keys = map[string]string{}
		return n
	}
	if _, ok := w.keys[registerID]; ok {
		delete(w.keys, registerID)
		return 1
	}
	return 0
}

// WmarkStats is the global snapshot.
type WmarkStats struct {
	RegisteredKeys int   `json:"registered_keys"`
	TotalEmbeds    int64 `json:"total_embeds"`
	TotalDetects   int64 `json:"total_detects"`
	TotalMarks     int64 `json:"total_marks"`
}

func (w *WatermarkEmbedder) Stats() WmarkStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return WmarkStats{
		RegisteredKeys: len(w.keys),
		TotalEmbeds:    w.totalEmbeds.Load(),
		TotalDetects:   w.totalDetects.Load(),
		TotalMarks:     w.totalMarks.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

type wmarkTok struct {
	word    string
	suffix  string // whitespace + punctuation immediately following
}

// tokenizeForWmark splits text into words plus the trailing punctuation
// + whitespace so detokenize can rebuild the exact string with
// substitutions in-place.
func tokenizeForWmark(s string) []wmarkTok {
	tokens := make([]wmarkTok, 0, 32)
	i := 0
	for i < len(s) {
		// Skip leading whitespace/punctuation
		for i < len(s) && !isWordByte(s[i]) {
			// Emit a no-word token to preserve leading punctuation
			j := i
			for j < len(s) && !isWordByte(s[j]) {
				j++
			}
			if len(tokens) > 0 {
				tokens[len(tokens)-1].suffix += s[i:j]
			} else {
				// Leading punctuation kept as a synthetic empty token
				tokens = append(tokens, wmarkTok{word: "", suffix: s[i:j]})
			}
			i = j
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && isWordByte(s[i]) {
			i++
		}
		tokens = append(tokens, wmarkTok{word: s[start:i]})
	}
	return tokens
}

func detokenize(tokens []wmarkTok) string {
	var sb strings.Builder
	for _, t := range tokens {
		sb.WriteString(t.word)
		sb.WriteString(t.suffix)
	}
	return sb.String()
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '\''
}

// selectPreferred picks the "green" (preferred) variant from a
// synonym group via a keyed PRF over position. Deterministic.
func selectPreferred(group []string, keyHash, position uint32) string {
	if len(group) == 0 {
		return ""
	}
	h := keyHash ^ position
	h = h*16777619 ^ 2166136261
	return strings.ToLower(group[h%uint32(len(group))])
}

// matchCase makes the replacement carry the casing of the original
// word (Capitalize / UPPER / lower / mixedCase preserved as best we can).
func matchCase(original, replacement string) string {
	if original == "" {
		return replacement
	}
	if isAllUpper(original) {
		return strings.ToUpper(replacement)
	}
	if original[0] >= 'A' && original[0] <= 'Z' {
		// Capitalize first letter
		if len(replacement) == 0 {
			return replacement
		}
		return strings.ToUpper(replacement[:1]) + strings.ToLower(replacement[1:])
	}
	return strings.ToLower(replacement)
}

func isAllUpper(s string) bool {
	hasLetter := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			return false
		}
		if c >= 'A' && c <= 'Z' {
			hasLetter = true
		}
	}
	return hasLetter
}

// sqrtFast is a Newton-method sqrt — avoids dragging math just for one use.
func sqrtFast(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 8; i++ {
		z = (z + x/z) / 2
	}
	return z
}

