package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// PrefetchPredictor predicts the *next* request a user is likely to
// make and surfaces the top-N candidates so the orchestrator can
// pre-warm embeddings, pre-fetch RAG chunks, or speculatively start
// the next LLM call while the user is still reading the previous
// answer.
//
// Production cache-warming pipelines usually have two layers: a
// global popularity prefetcher (cold-start: "everyone asks for the
// pricing page") and a per-session predictor (warm: "this user is
// onboarding — next they'll ask about API keys"). The global layer
// exists in a CDN. The per-session layer is what everyone rebuilds
// — usually as a fragile bigram on URL paths.
//
// PREFETCH.PREDICT.* is that per-session layer, but operating on
// embedding similarity rather than exact-text bigrams:
//
//   OBSERVE   one request goes into the session's history.
//   PREDICT   "given my last K requests, the next is probably one
//             of these N candidates" — drawn from the transitions
//             you've already seen with similar prefixes.
//   HIT       feedback: the prediction was used (or wasn't),
//             updates an EMA of session-level predictor quality.
//
// Commands:
//
//   PREFETCH.PREDICT.OBSERVE session-id text
//   PREFETCH.PREDICT.PREDICT session-id [LIMIT n]
//        → top-N predicted next requests, sorted by score.
//   PREFETCH.PREDICT.HIT session-id text
//        Records that a prediction was confirmed useful.
//   PREFETCH.PREDICT.STATUS session-id
//   PREFETCH.PREDICT.SESSIONS
//   PREFETCH.PREDICT.HORIZON session-id n
//        Set the per-session lookback (default 8).
//   PREFETCH.PREDICT.RESET session-id|ALL
//   PREFETCH.PREDICT.STATS
//
// Hot path: OBSERVE is one embedFallback + append + soft cap.
// PREDICT is K cosines over the history followed by a partial
// sort — ~5 µs at the default horizon. Apps can drive PREDICT
// every time the user finishes typing and act on the top-1 result.
type PrefetchPredictor struct {
	mu       sync.RWMutex
	sessions map[string]*prefetchSession

	totalObserves atomic.Int64
	totalPredicts atomic.Int64
	totalHits     atomic.Int64
}

type prefetchSession struct {
	mu      sync.RWMutex
	history []prefetchEvent
	horizon int
	hitEMA  float64 // EMA of "prediction was used"
	totalPredictions int64
	totalHits        int64
}

type prefetchEvent struct {
	Text string
	Vec  []float64
	TS   int64
}

// NewPrefetchPredictor returns an empty predictor.
func NewPrefetchPredictor() *PrefetchPredictor {
	return &PrefetchPredictor{sessions: map[string]*prefetchSession{}}
}

// Observe records one request in a session's history.
func (p *PrefetchPredictor) Observe(sessionID, text string) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	if text == "" {
		return errors.New("request text required")
	}
	p.totalObserves.Add(1)
	s := p.sessionOrCreate(sessionID)
	vec := embedFallback(text)
	s.mu.Lock()
	s.history = append(s.history, prefetchEvent{Text: text, Vec: vec, TS: time.Now().UnixNano()})
	// Soft cap: keep last 200 events per session
	if len(s.history) > 200 {
		s.history = s.history[len(s.history)-200:]
	}
	s.mu.Unlock()
	return nil
}

// PrefetchCandidate is one row of PREDICT output.
type PrefetchCandidate struct {
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

// Predict returns the top-N likely next requests for a session.
//
// Algorithm: given the last event in the history, score every past
// (event_i → event_{i+1}) transition by cosine(event_i, last_event)
// and surface the corresponding event_{i+1} as a candidate. Same
// successor that shows up across multiple matching prefixes scores
// higher (boost = sum of similarities).
func (p *PrefetchPredictor) Predict(sessionID string, limit int) ([]PrefetchCandidate, bool) {
	p.totalPredicts.Add(1)
	p.mu.RLock()
	s, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if !ok {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.history) < 2 {
		return nil, true // not enough history to predict
	}
	s.totalPredictions++
	horizon := s.horizon
	if horizon <= 0 {
		horizon = 8
	}
	last := s.history[len(s.history)-1]
	// score each past transition (event_i → event_{i+1}) where i+1 <
	// len(history)-1, weighted by cosine(event_i, last)
	scores := map[string]float64{}
	start := 0
	if len(s.history) > horizon {
		start = len(s.history) - horizon
	}
	for i := start; i < len(s.history)-1; i++ {
		sim := dotProduct(s.history[i].Vec, last.Vec)
		if sim <= 0 {
			continue
		}
		successor := s.history[i+1].Text
		// Avoid suggesting the request that just happened
		if successor == last.Text {
			continue
		}
		scores[successor] += sim
	}
	out := make([]PrefetchCandidate, 0, len(scores))
	for text, sc := range scores {
		out = append(out, PrefetchCandidate{Text: text, Score: sc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, true
}

// Hit records that a prediction was confirmed useful for the session.
// Used to track per-session predictor quality.
func (p *PrefetchPredictor) Hit(sessionID, text string) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	p.totalHits.Add(1)
	p.mu.RLock()
	s, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if !ok {
		return errors.New("unknown session_id: " + sessionID)
	}
	const alpha = 0.20
	s.mu.Lock()
	s.totalHits++
	s.hitEMA = s.hitEMA + alpha*(1.0-s.hitEMA)
	s.mu.Unlock()
	_ = text // text accepted so callers can log which prediction landed
	return nil
}

// Horizon updates the per-session lookback window. n <= 0 resets to default.
func (p *PrefetchPredictor) Horizon(sessionID string, n int) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	s := p.sessionOrCreate(sessionID)
	s.mu.Lock()
	s.horizon = n
	s.mu.Unlock()
	return nil
}

// PrefetchStatus is per-session snapshot.
type PrefetchStatus struct {
	SessionID        string  `json:"session_id"`
	HistorySize      int     `json:"history_size"`
	Horizon          int     `json:"horizon"`
	HitRate          float64 `json:"hit_rate_ema"`
	TotalPredictions int64   `json:"total_predictions"`
	TotalHits        int64   `json:"total_hits"`
}

// Status returns the per-session snapshot.
func (p *PrefetchPredictor) Status(sessionID string) (PrefetchStatus, bool) {
	p.mu.RLock()
	s, ok := p.sessions[sessionID]
	p.mu.RUnlock()
	if !ok {
		return PrefetchStatus{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	horizon := s.horizon
	if horizon <= 0 {
		horizon = 8
	}
	return PrefetchStatus{
		SessionID:        sessionID,
		HistorySize:      len(s.history),
		Horizon:          horizon,
		HitRate:          s.hitEMA,
		TotalPredictions: s.totalPredictions,
		TotalHits:        s.totalHits,
	}, true
}

// Sessions returns every session id, sorted.
func (p *PrefetchPredictor) Sessions() []string {
	p.mu.RLock()
	out := make([]string, 0, len(p.sessions))
	for k := range p.sessions {
		out = append(out, k)
	}
	p.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a session's history. sessionID="ALL" wipes all.
func (p *PrefetchPredictor) Reset(sessionID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sessionID == "ALL" {
		n := len(p.sessions)
		p.sessions = map[string]*prefetchSession{}
		return n
	}
	if _, ok := p.sessions[sessionID]; ok {
		delete(p.sessions, sessionID)
		return 1
	}
	return 0
}

// PrefetchStats is the global snapshot.
type PrefetchStats struct {
	Sessions       int   `json:"sessions"`
	TotalObserves  int64 `json:"total_observes"`
	TotalPredicts  int64 `json:"total_predicts"`
	TotalHits      int64 `json:"total_hits"`
}

func (p *PrefetchPredictor) Stats() PrefetchStats {
	p.mu.RLock()
	n := len(p.sessions)
	p.mu.RUnlock()
	return PrefetchStats{
		Sessions:      n,
		TotalObserves: p.totalObserves.Load(),
		TotalPredicts: p.totalPredicts.Load(),
		TotalHits:     p.totalHits.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (p *PrefetchPredictor) sessionOrCreate(id string) *prefetchSession {
	p.mu.RLock()
	s, ok := p.sessions[id]
	p.mu.RUnlock()
	if ok {
		return s
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sessions[id]; ok {
		return s
	}
	s = &prefetchSession{horizon: 8}
	p.sessions[id] = s
	return s
}
