package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SessionCluster is the real-time semantic-cohort analytics layer.
//
// PROMPT.GROUPS clusters by *lexical* fingerprint (good for cache-key
// normalisation; bad at "the user community asked the same question
// in 40 phrasings"). RAG.GAP clusters low-score queries. Neither is
// the product-analytics surface teams actually want: "what are the
// top 10 things users are asking about this week, ranked by volume,
// with member sessions so the PM can dig into examples?"
//
// SESSION.CLUSTER.* gives PMs that exact view, in embedding space,
// real-time:
//
//   OBSERVE   one request from one session goes into the right
//             cohort (or creates a new one if cosine < min_sim to
//             every existing cohort).
//   TOP       top-N cohorts by member-session count, with sample
//             request + last_seen + age in window.
//   MEMBERS   sessions in a specific cohort (for PM drill-down).
//   STATUS    which cohort the session currently sits in.
//
// Commands:
//
//   SESSION.CLUSTER.OBSERVE cluster-id session-id request-text
//        [MIN_SIM f]
//        Auto-cohorting. MIN_SIM defaults to 0.50 (hashed-BoW;
//        callers wiring a real sentence-transformer can raise it).
//   SESSION.CLUSTER.TOP cluster-id [LIMIT n] [WINDOW seconds]
//        → cohort rows sorted by member count desc.
//   SESSION.CLUSTER.MEMBERS cluster-id cohort-id
//        → sessions in that cohort.
//   SESSION.CLUSTER.STATUS cluster-id session-id
//        → current cohort id for the session.
//   SESSION.CLUSTER.LIST
//   SESSION.CLUSTER.RESET cluster-id|ALL
//   SESSION.CLUSTER.STATS
//
// Hot path: OBSERVE is one embedFallback + one dot product per
// existing cohort. With a few hundred cohorts per cluster_id and
// 128-dim vectors that's ~10-50 µs — fine for product-analytics
// rates, not for inner-loop request gating.
type SessionCluster struct {
	mu      sync.RWMutex
	clusters map[string]*sessionClusterEntry

	totalObserves atomic.Int64
}

type sessionClusterEntry struct {
	mu        sync.RWMutex
	cohorts   []*sessionCohort
	bySession map[string]string // session_id → cohort_id
	seq       int
}

type sessionCohort struct {
	ID          string
	Centroid    []float64
	N           int
	SampleQuery string
	Sessions    map[string]bool
	LastSeen    int64
	CreatedAt   int64
}

// NewSessionCluster returns an empty store.
func NewSessionCluster() *SessionCluster {
	return &SessionCluster{clusters: map[string]*sessionClusterEntry{}}
}

// Observe assigns one request to a cohort (or creates a new one).
func (s *SessionCluster) Observe(clusterID, sessionID, text string, minSim float64) error {
	if clusterID == "" || sessionID == "" {
		return errors.New("cluster_id and session_id required")
	}
	if text == "" {
		return errors.New("request text required")
	}
	if minSim <= 0 {
		minSim = 0.50
	}
	s.totalObserves.Add(1)
	e := s.entryOrCreate(clusterID)
	vec := embedFallback(text)
	now := time.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	// Find best matching cohort
	bestIdx := -1
	bestSim := -1.0
	for i, c := range e.cohorts {
		sim := dotProduct(c.Centroid, vec)
		if sim > bestSim {
			bestSim = sim
			bestIdx = i
		}
	}
	if bestIdx >= 0 && bestSim >= minSim {
		c := e.cohorts[bestIdx]
		// Update running-average centroid
		inv := 1.0 / float64(c.N+1)
		for d := range c.Centroid {
			c.Centroid[d] = (c.Centroid[d]*float64(c.N) + vec[d]) * inv
		}
		c.N++
		c.Sessions[sessionID] = true
		c.LastSeen = now
		e.bySession[sessionID] = c.ID
		return nil
	}
	// New cohort
	e.seq++
	id := "cohort-" + itoaBenchPub(e.seq)
	centroid := make([]float64, len(vec))
	copy(centroid, vec)
	c := &sessionCohort{
		ID: id, Centroid: centroid, N: 1,
		SampleQuery: text,
		Sessions:    map[string]bool{sessionID: true},
		LastSeen:    now,
		CreatedAt:   now,
	}
	e.cohorts = append(e.cohorts, c)
	e.bySession[sessionID] = id
	return nil
}

// SessionClusterTopRow is one row of TOP.
type SessionClusterTopRow struct {
	CohortID     string  `json:"cohort_id"`
	SampleQuery  string  `json:"sample_query"`
	Members      int     `json:"member_sessions"`
	Observations int     `json:"observations"`
	LastSeen     int64   `json:"last_seen"`
	AgeSeconds   int64   `json:"age_seconds"`
}

// Top returns cohorts sorted by member-session count desc, optionally
// filtered to those active within window.
func (s *SessionCluster) Top(clusterID string, limit int, window time.Duration) ([]SessionClusterTopRow, bool) {
	s.mu.RLock()
	e, ok := s.clusters[clusterID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	cutoff := int64(0)
	if window > 0 {
		cutoff = time.Now().UnixNano() - window.Nanoseconds()
	}
	now := time.Now().UnixNano()
	out := make([]SessionClusterTopRow, 0, len(e.cohorts))
	for _, c := range e.cohorts {
		if cutoff > 0 && c.LastSeen < cutoff {
			continue
		}
		out = append(out, SessionClusterTopRow{
			CohortID:     c.ID,
			SampleQuery:  c.SampleQuery,
			Members:      len(c.Sessions),
			Observations: c.N,
			LastSeen:     c.LastSeen / int64(time.Second),
			AgeSeconds:   (now - c.CreatedAt) / int64(time.Second),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Members != out[j].Members {
			return out[i].Members > out[j].Members
		}
		return out[i].Observations > out[j].Observations
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, true
}

// Members returns sessions in a specific cohort, sorted.
func (s *SessionCluster) Members(clusterID, cohortID string) ([]string, bool) {
	s.mu.RLock()
	e, ok := s.clusters[clusterID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, c := range e.cohorts {
		if c.ID == cohortID {
			out := make([]string, 0, len(c.Sessions))
			for sid := range c.Sessions {
				out = append(out, sid)
			}
			sort.Strings(out)
			return out, true
		}
	}
	return nil, false
}

// SessionClusterStatus is per-session snapshot.
type SessionClusterStatus struct {
	SessionID string `json:"session_id"`
	CohortID  string `json:"cohort_id"`
}

// Status returns the cohort a session currently belongs to.
func (s *SessionCluster) Status(clusterID, sessionID string) (SessionClusterStatus, bool) {
	s.mu.RLock()
	e, ok := s.clusters[clusterID]
	s.mu.RUnlock()
	if !ok {
		return SessionClusterStatus{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	cid, ok := e.bySession[sessionID]
	if !ok {
		return SessionClusterStatus{}, false
	}
	return SessionClusterStatus{SessionID: sessionID, CohortID: cid}, true
}

// List returns every cluster id, sorted.
func (s *SessionCluster) List() []string {
	s.mu.RLock()
	out := make([]string, 0, len(s.clusters))
	for k := range s.clusters {
		out = append(out, k)
	}
	s.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a cluster. clusterID="ALL" wipes everything.
func (s *SessionCluster) Reset(clusterID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if clusterID == "ALL" {
		n := len(s.clusters)
		s.clusters = map[string]*sessionClusterEntry{}
		return n
	}
	if _, ok := s.clusters[clusterID]; ok {
		delete(s.clusters, clusterID)
		return 1
	}
	return 0
}

// SessionClusterStats is the global snapshot.
type SessionClusterStats struct {
	Clusters      int   `json:"clusters"`
	TotalCohorts  int   `json:"total_cohorts"`
	TotalObserves int64 `json:"total_observes"`
}

func (s *SessionCluster) Stats() SessionClusterStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cohorts := 0
	for _, e := range s.clusters {
		e.mu.RLock()
		cohorts += len(e.cohorts)
		e.mu.RUnlock()
	}
	return SessionClusterStats{
		Clusters:      len(s.clusters),
		TotalCohorts:  cohorts,
		TotalObserves: s.totalObserves.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (s *SessionCluster) entryOrCreate(id string) *sessionClusterEntry {
	s.mu.RLock()
	e, ok := s.clusters[id]
	s.mu.RUnlock()
	if ok {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.clusters[id]; ok {
		return e
	}
	e = &sessionClusterEntry{bySession: map[string]string{}}
	s.clusters[id] = e
	return e
}
