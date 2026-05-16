package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// StreamWatcher detects degeneration mid-generation. LLMs go off the
// rails in three recognisable ways:
//
//   - cycle:       same token repeats many times in a row ("the the
//                  the the the …").
//   - n-gram loop: a short n-gram repeats in the recent window ("X
//                  Y Z X Y Z X Y Z …"). The model is rambling.
//   - diversity collapse: unique-token ratio drops below a floor as
//                  the generation grows ("the model said the same
//                  fifteen words for the last 200 tokens").
//
// STREAM.PARSE extracts fields from a finished stream. STREAM.WATCH.*
// runs *during* generation so the orchestrator can early-stop a
// runaway and save the output tokens. Apps wire it as: every token
// the upstream LLM emits, fire TOKEN; on verdict=stop, cancel the
// upstream stream.
//
// Commands:
//
//   STREAM.WATCH.OPEN session-id [MAX_LEN n] [CYCLE_THRESHOLD n]
//        [NGRAM n] [NGRAM_REPEAT_THRESHOLD n] [DIVERSITY_FLOOR f]
//        [MIN_TOKENS n]
//        Defaults: MAX_LEN=2000, CYCLE_THRESHOLD=8,
//        NGRAM=3, NGRAM_REPEAT_THRESHOLD=4, DIVERSITY_FLOOR=0.10,
//        MIN_TOKENS=40 (signals don't fire below MIN_TOKENS — early
//        repetition is normal).
//   STREAM.WATCH.TOKEN session-id token
//        → {verdict, reason, length, repeat_count, unique_ratio}
//        verdict: ok | warning | stop
//   STREAM.WATCH.STATUS session-id
//   STREAM.WATCH.CLOSE  session-id [REASON r]
//   STREAM.WATCH.SESSIONS
//   STREAM.WATCH.RESET  session-id|ALL
//   STREAM.WATCH.STATS
//
// Hot path: TOKEN is O(1) amortised — a ring of recent tokens, a
// running unique-token map, and an n-gram counter that updates as
// the window slides. Sub-microsecond per token at the default config.
type StreamWatcher struct {
	mu       sync.RWMutex
	sessions map[string]*streamWatchSession

	totalTokens atomic.Int64
	totalStops  atomic.Int64
	totalWarns  atomic.Int64
}

type streamWatchSession struct {
	mu             sync.Mutex
	cfg            streamWatchConfig
	tokens         []string         // ring of all tokens (capped by MaxLen)
	lastToken      string
	cycleCount     int               // consecutive identical token count
	lastVerdict    string
	lastReason     string
	uniqueTokens   map[string]int    // token → count over the full session
	ngrams         map[string]int    // recent n-gram → repeat count
	closedAt       int64
	closedReason   string
	startedAt      int64
	stoppedByWatch bool
}

type streamWatchConfig struct {
	MaxLen                int
	CycleThreshold        int
	NGram                 int
	NGramRepeatThreshold  int
	DiversityFloor        float64
	MinTokens             int
}

// NewStreamWatcher returns an empty watcher.
func NewStreamWatcher() *StreamWatcher {
	return &StreamWatcher{sessions: map[string]*streamWatchSession{}}
}

// Open starts watching a session. Calling Open on an existing session
// resets its state — useful when the upstream retries a generation.
func (w *StreamWatcher) Open(sessionID string, cfg streamWatchConfig) error {
	if sessionID == "" {
		return errors.New("session_id required")
	}
	cfg = defaultStreamWatchConfig(cfg)
	if cfg.NGram < 2 {
		return errors.New("NGRAM must be >= 2")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions[sessionID] = &streamWatchSession{
		cfg:          cfg,
		uniqueTokens: map[string]int{},
		ngrams:       map[string]int{},
		startedAt:    time.Now().UnixNano(),
	}
	return nil
}

// StreamWatchResult is TOKEN's return.
type StreamWatchResult struct {
	Verdict      string  `json:"verdict"`       // ok | warning | stop
	Reason       string  `json:"reason,omitempty"`
	Length       int     `json:"length"`
	RepeatCount  int     `json:"repeat_count"`  // for cycle/ngram, the offending count
	UniqueRatio  float64 `json:"unique_ratio"`
}

// Token feeds one token and returns the running verdict. Once a
// session goes to "stop", further TOKEN calls keep returning stop
// without further analysis (idempotent shutdown).
func (w *StreamWatcher) Token(sessionID, token string) (StreamWatchResult, error) {
	if sessionID == "" {
		return StreamWatchResult{}, errors.New("session_id required")
	}
	if token == "" {
		return StreamWatchResult{}, errors.New("token required")
	}
	w.totalTokens.Add(1)
	w.mu.RLock()
	s, ok := w.sessions[sessionID]
	w.mu.RUnlock()
	if !ok {
		return StreamWatchResult{}, errors.New("unknown session_id (call STREAM.WATCH.OPEN first): " + sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Once stopped, every subsequent token returns stop without analysis.
	if s.stoppedByWatch {
		return StreamWatchResult{
			Verdict: "stop", Reason: s.lastReason,
			Length:      len(s.tokens),
			UniqueRatio: uniqueRatio(s.uniqueTokens, len(s.tokens)),
		}, nil
	}

	// Append + roll window
	s.tokens = append(s.tokens, token)
	if s.cfg.MaxLen > 0 && len(s.tokens) > s.cfg.MaxLen {
		drop := s.tokens[0]
		s.tokens = s.tokens[1:]
		if c := s.uniqueTokens[drop]; c <= 1 {
			delete(s.uniqueTokens, drop)
		} else {
			s.uniqueTokens[drop] = c - 1
		}
	}
	s.uniqueTokens[token]++

	// Cycle: same token in a row
	if token == s.lastToken {
		s.cycleCount++
	} else {
		s.cycleCount = 1
	}
	s.lastToken = token

	// N-gram update — count the just-completed n-gram
	if len(s.tokens) >= s.cfg.NGram {
		gram := joinTail(s.tokens, s.cfg.NGram)
		s.ngrams[gram]++
	}

	out := StreamWatchResult{
		Verdict:     "ok",
		Length:      len(s.tokens),
		UniqueRatio: uniqueRatio(s.uniqueTokens, len(s.tokens)),
	}

	// Don't fire signals before MIN_TOKENS — early repetition is fine.
	if len(s.tokens) < s.cfg.MinTokens {
		s.lastVerdict = out.Verdict
		return out, nil
	}

	// Cycle stop
	if s.cycleCount >= s.cfg.CycleThreshold {
		out.Verdict = "stop"
		out.Reason = "cycle: token repeated " + itoaBenchPub(s.cycleCount) + " times"
		out.RepeatCount = s.cycleCount
		s.stoppedByWatch = true
		s.lastVerdict, s.lastReason = out.Verdict, out.Reason
		w.totalStops.Add(1)
		return out, nil
	}

	// N-gram stop
	if len(s.tokens) >= s.cfg.NGram {
		gram := joinTail(s.tokens, s.cfg.NGram)
		count := s.ngrams[gram]
		if count >= s.cfg.NGramRepeatThreshold {
			out.Verdict = "stop"
			out.Reason = "n-gram loop: '" + gram + "' repeated " + itoaBenchPub(count) + " times"
			out.RepeatCount = count
			s.stoppedByWatch = true
			s.lastVerdict, s.lastReason = out.Verdict, out.Reason
			w.totalStops.Add(1)
			return out, nil
		}
	}

	// Diversity floor
	if out.UniqueRatio < s.cfg.DiversityFloor {
		out.Verdict = "warning"
		out.Reason = "diversity collapse: unique_ratio below floor"
	}

	// Early warning for high cycle count
	if s.cycleCount >= s.cfg.CycleThreshold/2 && out.Verdict == "ok" {
		out.Verdict = "warning"
		out.Reason = "cycle building: token repeated " + itoaBenchPub(s.cycleCount) + " times"
		out.RepeatCount = s.cycleCount
	}

	if out.Verdict == "warning" {
		w.totalWarns.Add(1)
	}
	s.lastVerdict = out.Verdict
	s.lastReason = out.Reason
	return out, nil
}

// StreamWatchStatus is per-session snapshot.
type StreamWatchStatus struct {
	SessionID    string  `json:"session_id"`
	Length       int     `json:"length"`
	UniqueTokens int     `json:"unique_tokens"`
	UniqueRatio  float64 `json:"unique_ratio"`
	CycleCount   int     `json:"cycle_count"`
	LastVerdict  string  `json:"last_verdict"`
	LastReason   string  `json:"last_reason,omitempty"`
	StartedAt    int64   `json:"started_at"`
	ClosedAt     int64   `json:"closed_at,omitempty"`
	ClosedReason string  `json:"closed_reason,omitempty"`
	Stopped      bool    `json:"stopped_by_watch"`
}

// Status returns the per-session snapshot.
func (w *StreamWatcher) Status(sessionID string) (StreamWatchStatus, bool) {
	w.mu.RLock()
	s, ok := w.sessions[sessionID]
	w.mu.RUnlock()
	if !ok {
		return StreamWatchStatus{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return StreamWatchStatus{
		SessionID:    sessionID,
		Length:       len(s.tokens),
		UniqueTokens: len(s.uniqueTokens),
		UniqueRatio:  uniqueRatio(s.uniqueTokens, len(s.tokens)),
		CycleCount:   s.cycleCount,
		LastVerdict:  s.lastVerdict,
		LastReason:   s.lastReason,
		StartedAt:    s.startedAt / int64(time.Second),
		ClosedAt:     s.closedAt / int64(time.Second),
		ClosedReason: s.closedReason,
		Stopped:      s.stoppedByWatch,
	}, true
}

// Close marks a session done (caller is exiting the watch). The
// session is retained for STATUS lookup until RESET.
func (w *StreamWatcher) Close(sessionID, reason string) bool {
	w.mu.RLock()
	s, ok := w.sessions[sessionID]
	w.mu.RUnlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	s.closedAt = time.Now().UnixNano()
	s.closedReason = reason
	s.mu.Unlock()
	return true
}

// Sessions returns every session id known, sorted.
func (w *StreamWatcher) Sessions() []string {
	w.mu.RLock()
	out := make([]string, 0, len(w.sessions))
	for k := range w.sessions {
		out = append(out, k)
	}
	w.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a session. sessionID="ALL" wipes all.
func (w *StreamWatcher) Reset(sessionID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sessionID == "ALL" {
		n := len(w.sessions)
		w.sessions = map[string]*streamWatchSession{}
		return n
	}
	if _, ok := w.sessions[sessionID]; ok {
		delete(w.sessions, sessionID)
		return 1
	}
	return 0
}

// StreamWatchStats is the global snapshot.
type StreamWatchStats struct {
	Sessions    int   `json:"sessions"`
	TotalTokens int64 `json:"total_tokens"`
	TotalStops  int64 `json:"total_stops"`
	TotalWarns  int64 `json:"total_warns"`
}

func (w *StreamWatcher) Stats() StreamWatchStats {
	w.mu.RLock()
	n := len(w.sessions)
	w.mu.RUnlock()
	return StreamWatchStats{
		Sessions:    n,
		TotalTokens: w.totalTokens.Load(),
		TotalStops:  w.totalStops.Load(),
		TotalWarns:  w.totalWarns.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

// StreamWatchConfigPublic mirrors streamWatchConfig for the resp
// handler to populate from RESP args without exporting internals.
type StreamWatchConfigPublic struct {
	MaxLen                int
	CycleThreshold        int
	NGram                 int
	NGramRepeatThreshold  int
	DiversityFloor        float64
	MinTokens             int
}

// OpenPublic is the variant exposed to the resp layer; converts the
// public config struct into the internal one.
func (w *StreamWatcher) OpenPublic(sessionID string, c StreamWatchConfigPublic) error {
	return w.Open(sessionID, streamWatchConfig{
		MaxLen: c.MaxLen, CycleThreshold: c.CycleThreshold,
		NGram: c.NGram, NGramRepeatThreshold: c.NGramRepeatThreshold,
		DiversityFloor: c.DiversityFloor, MinTokens: c.MinTokens,
	})
}

func defaultStreamWatchConfig(c streamWatchConfig) streamWatchConfig {
	if c.MaxLen <= 0 {
		c.MaxLen = 2000
	}
	if c.CycleThreshold <= 0 {
		c.CycleThreshold = 8
	}
	if c.NGram <= 0 {
		c.NGram = 3
	}
	if c.NGramRepeatThreshold <= 0 {
		c.NGramRepeatThreshold = 4
	}
	if c.DiversityFloor <= 0 {
		c.DiversityFloor = 0.10
	}
	if c.MinTokens <= 0 {
		c.MinTokens = 40
	}
	return c
}

func uniqueRatio(unique map[string]int, length int) float64 {
	if length == 0 {
		return 0
	}
	return float64(len(unique)) / float64(length)
}

// joinTail concatenates the last n tokens with ASCII space.
func joinTail(tokens []string, n int) string {
	if n > len(tokens) {
		n = len(tokens)
	}
	tail := tokens[len(tokens)-n:]
	total := 0
	for _, t := range tail {
		total += len(t) + 1
	}
	buf := make([]byte, 0, total)
	for i, t := range tail {
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, t...)
	}
	return string(buf)
}
