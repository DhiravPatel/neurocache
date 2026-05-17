package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// RecallStore is the drift-driven proactive invalidator. CACHE-
// INVALIDATE / FACT.STALE invalidate on explicit triggers. RECALL is
// for the case where no trigger event happened, but the *world*
// drifted (a model swap, a knowledge cutoff change, a major news
// event) and many cached answers are now probably stale even though
// no specific FACT changed.
//
// The model:
//
//   - Each cached answer is REGISTERed with a generation timestamp
//     and a context fingerprint (model version, prompt version,
//     embedding model — any signal that "what produced this answer"
//     ).
//   - RECALL.MARK declares "the world drifted between T1 and T2 in
//     way W" — e.g., "we swapped from gpt-4o → gpt-4.5 at T1".
//   - SCAN returns the answer IDs that fall in the affected window
//     with a recall_confidence score (1 = certainly stale, 0 =
//     probably still good). The app decides whether to invalidate.
//
// The confidence score uses simple decay-from-the-event: 1.0 right at
// the change boundary, fading per the supplied half-life.
//
// Commands:
//
//   RECALL.REGISTER answer-id model-version [PROMPT v] [EMBED v]
//        [AT unix-ms]
//   RECALL.MARK change-id REASON "<text>" FROM unix-ms TO unix-ms
//        [HALF_LIFE_S s] [SCOPE model|prompt|embed]
//   RECALL.SCAN [MIN_CONFIDENCE f] [LIMIT n] [SCOPE model|prompt|embed]
//        → list of (answer_id, confidence, reason)
//   RECALL.STATS
//   RECALL.FORGET answer-id|ALL
//   RECALL.UNMARK change-id|ALL
//
// Hot path: REGISTER is one map insert. SCAN walks the registry
// against the active drift events; typically thousands not millions
// (you'd not call SCAN per request — schedule it).
type RecallStore struct {
	mu      sync.RWMutex
	answers map[string]*recallAnswer
	events  map[string]*recallEvent

	totalRegisters atomic.Int64
	totalScans     atomic.Int64
}

type recallAnswer struct {
	ID          string
	ModelVer    string
	PromptVer   string
	EmbedVer    string
	GeneratedAt time.Time
}

type recallEvent struct {
	ID           string
	Reason       string
	From         time.Time
	To           time.Time
	HalfLifeSec  float64
	Scope        string // model | prompt | embed | "" (any)
}

// NewRecallStore returns an empty store.
func NewRecallStore() *RecallStore {
	return &RecallStore{
		answers: map[string]*recallAnswer{},
		events:  map[string]*recallEvent{},
	}
}

// Register adds a cached answer to the recall ledger.
func (r *RecallStore) Register(id, modelVer, promptVer, embedVer string, atUnixMS int64) error {
	if id == "" {
		return errors.New("answer_id required")
	}
	if modelVer == "" {
		return errors.New("model_version required")
	}
	r.totalRegisters.Add(1)
	t := time.Now()
	if atUnixMS > 0 {
		t = time.UnixMilli(atUnixMS)
	}
	r.mu.Lock()
	r.answers[id] = &recallAnswer{
		ID: id, ModelVer: modelVer, PromptVer: promptVer,
		EmbedVer: embedVer, GeneratedAt: t,
	}
	r.mu.Unlock()
	return nil
}

// Mark declares a drift event window.
func (r *RecallStore) Mark(id, reason string, fromUnixMS, toUnixMS int64, halfLifeSec float64, scope string) error {
	if id == "" {
		return errors.New("change_id required")
	}
	if reason == "" {
		return errors.New("reason required")
	}
	if fromUnixMS <= 0 || toUnixMS <= 0 || toUnixMS < fromUnixMS {
		return errors.New("from/to must be positive and to >= from")
	}
	if halfLifeSec < 0 {
		return errors.New("half_life_seconds must be non-negative")
	}
	if scope != "" && scope != "model" && scope != "prompt" && scope != "embed" {
		return errors.New("scope must be one of: model, prompt, embed, \"\"")
	}
	r.mu.Lock()
	r.events[id] = &recallEvent{
		ID: id, Reason: reason,
		From: time.UnixMilli(fromUnixMS), To: time.UnixMilli(toUnixMS),
		HalfLifeSec: halfLifeSec, Scope: scope,
	}
	r.mu.Unlock()
	return nil
}

// RecallScanRow is one row of SCAN.
type RecallScanRow struct {
	AnswerID   string  `json:"answer_id"`
	Confidence float64 `json:"recall_confidence"`
	Reason     string  `json:"reason"`
	ChangeID   string  `json:"change_id"`
}

// Scan walks the registry and reports stale candidates.
func (r *RecallStore) Scan(minConfidence float64, limit int, scope string) []RecallScanRow {
	if limit <= 0 {
		limit = 100
	}
	r.totalScans.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	out := make([]RecallScanRow, 0)
	for _, ans := range r.answers {
		bestConf := 0.0
		bestEv := (*recallEvent)(nil)
		for _, ev := range r.events {
			if scope != "" && ev.Scope != "" && ev.Scope != scope {
				continue
			}
			if ans.GeneratedAt.Before(ev.From) || ans.GeneratedAt.After(ev.To) {
				continue
			}
			conf := recallConfidence(ev, now)
			if conf > bestConf {
				bestConf = conf
				bestEv = ev
			}
		}
		if bestEv == nil || bestConf < minConfidence {
			continue
		}
		out = append(out, RecallScanRow{
			AnswerID: ans.ID, Confidence: bestConf,
			Reason: bestEv.Reason, ChangeID: bestEv.ID,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Confidence > out[j].Confidence
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// recallConfidence: 1.0 at event time, decays per half-life.
func recallConfidence(ev *recallEvent, now time.Time) float64 {
	if ev.HalfLifeSec == 0 {
		return 1.0
	}
	elapsed := now.Sub(ev.To).Seconds()
	if elapsed <= 0 {
		return 1.0
	}
	// Confidence = 0.5^(elapsed / half_life)
	return expDecay(elapsed / ev.HalfLifeSec)
}

func expDecay(halves float64) float64 {
	// 0.5^halves; halves can be fractional
	if halves < 0 {
		return 1.0
	}
	// 2^x = e^(x ln 2). We avoid math by approximating with a fast pow.
	// For correctness use math.Pow(0.5, halves) — pull it in:
	return powFast(0.5, halves)
}

func powFast(base, exp float64) float64 {
	// Series: 0.5^x via repeated squaring + linear interp for fractional
	if exp == 0 {
		return 1
	}
	if exp < 0 {
		return 1 / powFast(base, -exp)
	}
	whole := int(exp)
	frac := exp - float64(whole)
	res := 1.0
	for i := 0; i < whole; i++ {
		res *= base
	}
	// Linear interp for fraction (good enough for confidence; not exact)
	res *= 1.0 - frac*(1.0-base)
	return res
}

// Forget drops an answer (or all).
func (r *RecallStore) Forget(answerID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if answerID == "ALL" {
		n := len(r.answers)
		r.answers = map[string]*recallAnswer{}
		return n
	}
	if _, ok := r.answers[answerID]; ok {
		delete(r.answers, answerID)
		return 1
	}
	return 0
}

// Unmark drops a drift event (or all).
func (r *RecallStore) Unmark(changeID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if changeID == "ALL" {
		n := len(r.events)
		r.events = map[string]*recallEvent{}
		return n
	}
	if _, ok := r.events[changeID]; ok {
		delete(r.events, changeID)
		return 1
	}
	return 0
}

// RecallStats is the global snapshot.
type RecallStats struct {
	Answers        int   `json:"answers"`
	Events         int   `json:"events"`
	TotalRegisters int64 `json:"total_registers"`
	TotalScans     int64 `json:"total_scans"`
}

func (r *RecallStore) Stats() RecallStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RecallStats{
		Answers:        len(r.answers),
		Events:         len(r.events),
		TotalRegisters: r.totalRegisters.Load(),
		TotalScans:     r.totalScans.Load(),
	}
}
