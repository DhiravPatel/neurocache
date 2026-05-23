package llmstack

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SemLocks is the SEMANTIC version of LOCK. LOCK dedupes by key —
// two workers can't both hold "deploy". LOCK.SEM dedupes by
// MEANING — prevents semantically equivalent work running
// concurrently:
//
//   Worker A: "summarize document 12"
//   Worker B: "give me a summary of doc 12"   ← should serialize
//
// COALESCE is the related primitive but different shape: COALESCE
// is "first caller does the work, the rest WAIT and share." LOCK.SEM
// is "first caller acquires, the rest GET REJECTED — go do something
// else, don't queue up." Apps use COALESCE for the cache-warm hot
// path, LOCK.SEM for the side-effecty-work hot path.
//
// Commands:
//
//   LOCK.SEM.ACQUIRE namespace text [THRESHOLD t] [TTL ms]
//        → [acquired, token, similar_text, similar_score]
//        On miss (no semantic collision): acquired=1 + a token.
//        On hit (collision): acquired=0 + the similar_text +
//        score so the caller can decide to retry / skip / queue.
//
//   LOCK.SEM.RELEASE namespace token
//        Caller passes the token from ACQUIRE. No-op if expired or
//        unknown.
//
//   LOCK.SEM.STATUS namespace [LIMIT n]
//        Currently held locks (text + age + TTL remaining).
//
//   LOCK.SEM.FORGET namespace text
//        Force-release by text (admin override).
//
//   LOCK.SEM.STATS
//
// Storage: per-namespace list of held locks + atomic state. TTL is
// enforced lazily on the next ACQUIRE / STATUS — expired locks
// drop. Default TTL 30s, default threshold 0.85.
type SemLocks struct {
	mu         sync.RWMutex
	namespaces map[string]*semLockNS

	totalAcquires  atomic.Int64
	totalAcquired  atomic.Int64 // successful
	totalRejected  atomic.Int64 // collided
	totalReleases  atomic.Int64
	totalExpiries  atomic.Int64
}

type semLockNS struct {
	mu    sync.RWMutex
	locks map[string]*semLockEntry // token → entry
	dim   int
}

type semLockEntry struct {
	text        string
	vec         []float64
	acquiredAt  int64 // unix-ns
	expiresAt   int64 // unix-ns
}

// NewSemLocks returns an empty lock manager.
func NewSemLocks() *SemLocks {
	return &SemLocks{namespaces: map[string]*semLockNS{}}
}

// SemAcquireResult is ACQUIRE's return.
type SemAcquireResult struct {
	Acquired      bool    `json:"acquired"`
	Token         string  `json:"token,omitempty"`
	SimilarText   string  `json:"similar_text,omitempty"`
	SimilarScore  float64 `json:"similar_score,omitempty"`
}

// Acquire tries to grab a semantic lock on `text` in namespace.
// Returns acquired=true + token on miss; acquired=false + the
// colliding lock's text on hit.
func (s *SemLocks) Acquire(namespace, text string, threshold float64, ttl time.Duration) (SemAcquireResult, error) {
	if namespace == "" {
		return SemAcquireResult{}, errors.New("namespace required")
	}
	if text == "" {
		return SemAcquireResult{}, errors.New("text required")
	}
	s.totalAcquires.Add(1)
	if threshold <= 0 {
		threshold = 0.85
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	vec := embedFallback(text)

	s.mu.Lock()
	ns, ok := s.namespaces[namespace]
	if !ok {
		ns = &semLockNS{locks: map[string]*semLockEntry{}, dim: len(vec)}
		s.namespaces[namespace] = ns
	}
	s.mu.Unlock()
	if ns.dim != 0 && len(vec) != ns.dim {
		return SemAcquireResult{}, errors.New("embedding dim mismatch")
	}

	now := time.Now().UnixNano()
	ns.mu.Lock()
	defer ns.mu.Unlock()

	// Lazy expiry sweep
	for tok, e := range ns.locks {
		if e.expiresAt <= now {
			delete(ns.locks, tok)
			s.totalExpiries.Add(1)
		}
	}

	// Check semantic collision over remaining held locks
	bestScore := 0.0
	bestText := ""
	for _, e := range ns.locks {
		score := dotProduct(vec, e.vec)
		if score > bestScore {
			bestScore = score
			bestText = e.text
		}
	}
	if bestScore >= threshold {
		s.totalRejected.Add(1)
		return SemAcquireResult{
			Acquired:     false,
			SimilarText:  bestText,
			SimilarScore: bestScore,
		}, nil
	}

	// No collision — acquire
	token := newSemLockToken()
	ns.locks[token] = &semLockEntry{
		text:       text,
		vec:        vec,
		acquiredAt: now,
		expiresAt:  now + ttl.Nanoseconds(),
	}
	s.totalAcquired.Add(1)
	return SemAcquireResult{Acquired: true, Token: token}, nil
}

// Release drops a held lock by its token. Idempotent on unknown
// tokens.
func (s *SemLocks) Release(namespace, token string) bool {
	s.mu.RLock()
	ns, ok := s.namespaces[namespace]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	_, was := ns.locks[token]
	delete(ns.locks, token)
	if was {
		s.totalReleases.Add(1)
	}
	return was
}

// SemLockStatusRow is one row of STATUS.
type SemLockStatusRow struct {
	Token     string `json:"token"`
	Text      string `json:"text"`
	AgeMS     int64  `json:"age_ms"`
	RemainMS  int64  `json:"remain_ms"`
}

// Status returns the currently-held locks in a namespace.
func (s *SemLocks) Status(namespace string, limit int) []SemLockStatusRow {
	s.mu.RLock()
	ns, ok := s.namespaces[namespace]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	now := time.Now().UnixNano()
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	out := make([]SemLockStatusRow, 0, len(ns.locks))
	for tok, e := range ns.locks {
		if e.expiresAt <= now {
			continue
		}
		out = append(out, SemLockStatusRow{
			Token:    tok,
			Text:     e.text,
			AgeMS:    (now - e.acquiredAt) / int64(time.Millisecond),
			RemainMS: (e.expiresAt - now) / int64(time.Millisecond),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgeMS < out[j].AgeMS })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ForgetByText admin-override: drops every lock in the namespace
// whose text matches exactly. Returns the count.
func (s *SemLocks) ForgetByText(namespace, text string) int {
	s.mu.RLock()
	ns, ok := s.namespaces[namespace]
	s.mu.RUnlock()
	if !ok {
		return 0
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	n := 0
	for tok, e := range ns.locks {
		if e.text == text {
			delete(ns.locks, tok)
			n++
		}
	}
	return n
}

// ForgetNamespace wipes a whole namespace.
func (s *SemLocks) ForgetNamespace(namespace string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.namespaces[namespace]
	if !ok {
		return 0
	}
	ns.mu.Lock()
	n := len(ns.locks)
	ns.mu.Unlock()
	delete(s.namespaces, namespace)
	return n
}

// SemLockStats is the global snapshot.
type SemLockStats struct {
	Namespaces     int   `json:"namespaces"`
	HeldNow        int   `json:"held_now"`
	TotalAcquires  int64 `json:"total_acquires"`
	TotalAcquired  int64 `json:"total_acquired"`
	TotalRejected  int64 `json:"total_rejected"`
	TotalReleases  int64 `json:"total_releases"`
	TotalExpiries  int64 `json:"total_expiries"`
}

func (s *SemLocks) Stats() SemLockStats {
	s.mu.RLock()
	n := len(s.namespaces)
	now := time.Now().UnixNano()
	held := 0
	for _, ns := range s.namespaces {
		ns.mu.RLock()
		for _, e := range ns.locks {
			if e.expiresAt > now {
				held++
			}
		}
		ns.mu.RUnlock()
	}
	s.mu.RUnlock()
	return SemLockStats{
		Namespaces:    n,
		HeldNow:       held,
		TotalAcquires: s.totalAcquires.Load(),
		TotalAcquired: s.totalAcquired.Load(),
		TotalRejected: s.totalRejected.Load(),
		TotalReleases: s.totalReleases.Load(),
		TotalExpiries: s.totalExpiries.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func newSemLockToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
