package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentBlackboard is the shared workspace primitive for multi-agent
// systems. Every agent framework (Autogen, CrewAI, Swarm, LangGraph)
// reimplements the same pattern: an in-memory bag of findings that
// agents POST to and READ from, with semantic search over the posts
// so an agent can ask "what do we know about X?" without knowing what
// the other agents called it.
//
// The blackboard solves three things one Redis HASH cannot:
//
//   1. Semantic READ — "what do we know about pricing?" returns a
//      post that said "EU VAT is 21% for SaaS" even though it never
//      used the word pricing. Tags help but can't be the only path.
//
//   2. CLAIM — atomic "I'll handle task X" so two agents don't dupe
//      the same work. CLAIM has a TTL so a crashed claimant doesn't
//      poison the task forever.
//
//   3. Per-run isolation — every run has its own board so a parallel
//      research run doesn't leak findings into yours.
//
// Commands:
//
//   AGENT.BB.POST run-id agent-id text [TAGS t1,t2,...]
//        → post-id (server-assigned, monotonic per run)
//   AGENT.BB.READ run-id query [K n] [MIN_SIM f]
//        → posts ranked by cosine to the query
//   AGENT.BB.LIST run-id [LIMIT n] [TAG t]
//        → reverse-chronological listing, optional tag filter
//   AGENT.BB.CLAIM run-id task-id agent-id [TTL ms]
//        → claimed=1 if you got it, claimed=0 + owner if not
//   AGENT.BB.RELEASE run-id task-id agent-id
//        → released=1 if you held it; 0 otherwise
//   AGENT.BB.CLAIMS run-id   — every active claim on this run
//   AGENT.BB.DROP run-id|ALL — wipe a run's board
//   AGENT.BB.LIST_RUNS       — every run with posts/claims
//   AGENT.BB.STATS           — global counters
//
// Hot path: POST is one embedFallback + append. READ is a linear scan
// over the run's posts (typically dozens, not millions — a board is
// per-run, not per-tenant). CLAIM is one map lookup + insert under
// the per-run mutex.
type AgentBlackboard struct {
	mu   sync.RWMutex
	runs map[string]*bbRun

	totalPosts    atomic.Int64
	totalReads    atomic.Int64
	totalClaims   atomic.Int64
	claimConflicts atomic.Int64
}

type bbRun struct {
	mu       sync.RWMutex
	posts    []*bbPost
	nextID   int64
	claims   map[string]*bbClaim // task-id → claim
}

type bbPost struct {
	ID       int64
	AgentID  string
	Text     string
	Vec      []float64
	Tags     []string
	PostedAt time.Time
}

type bbClaim struct {
	TaskID    string
	AgentID   string
	ClaimedAt time.Time
	ExpiresAt time.Time // zero means no expiry
}

// NewAgentBlackboard returns an empty blackboard registry.
func NewAgentBlackboard() *AgentBlackboard {
	return &AgentBlackboard{runs: map[string]*bbRun{}}
}

// BBPostResult is POST's return.
type BBPostResult struct {
	PostID int64 `json:"post_id"`
}

// Post records one finding on the run's board.
func (b *AgentBlackboard) Post(runID, agentID, text string, tags []string) (BBPostResult, error) {
	if runID == "" {
		return BBPostResult{}, errors.New("run_id required")
	}
	if agentID == "" {
		return BBPostResult{}, errors.New("agent_id required")
	}
	if text == "" {
		return BBPostResult{}, errors.New("text required")
	}
	b.totalPosts.Add(1)
	r := b.runOrCreate(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	p := &bbPost{
		ID:       r.nextID,
		AgentID:  agentID,
		Text:     text,
		Vec:      embedFallback(text),
		Tags:     dedupTagsBB(tags),
		PostedAt: time.Now(),
	}
	r.posts = append(r.posts, p)
	return BBPostResult{PostID: p.ID}, nil
}

// BBReadRow is one row of READ's result.
type BBReadRow struct {
	PostID   int64    `json:"post_id"`
	AgentID  string   `json:"agent_id"`
	Text     string   `json:"text"`
	Tags     []string `json:"tags"`
	Score    float64  `json:"score"`
	AgeMS    int64    `json:"age_ms"`
}

// Read returns the K posts most semantically similar to the query.
func (b *AgentBlackboard) Read(runID, query string, k int, minSim float64) ([]BBReadRow, bool) {
	if runID == "" || query == "" {
		return nil, false
	}
	if k <= 0 {
		k = 5
	}
	if minSim < 0 {
		minSim = 0
	}
	b.totalReads.Add(1)
	b.mu.RLock()
	r, ok := b.runs[runID]
	b.mu.RUnlock()
	if !ok {
		return nil, false
	}
	qvec := embedFallback(query)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.posts) == 0 {
		return []BBReadRow{}, true
	}
	now := time.Now()
	cand := make([]BBReadRow, 0, len(r.posts))
	for _, p := range r.posts {
		s := dotProduct(p.Vec, qvec)
		if s < minSim {
			continue
		}
		cand = append(cand, BBReadRow{
			PostID:  p.ID,
			AgentID: p.AgentID,
			Text:    p.Text,
			Tags:    p.Tags,
			Score:   s,
			AgeMS:   now.Sub(p.PostedAt).Milliseconds(),
		})
	}
	sort.Slice(cand, func(i, j int) bool {
		return cand[i].Score > cand[j].Score
	})
	if len(cand) > k {
		cand = cand[:k]
	}
	return cand, true
}

// BBListRow is one row of LIST's reverse-chronological view.
type BBListRow struct {
	PostID  int64    `json:"post_id"`
	AgentID string   `json:"agent_id"`
	Text    string   `json:"text"`
	Tags    []string `json:"tags"`
	AgeMS   int64    `json:"age_ms"`
}

// List returns the most recent posts, optionally filtered by tag.
func (b *AgentBlackboard) List(runID string, limit int, tag string) ([]BBListRow, bool) {
	if runID == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 20
	}
	b.mu.RLock()
	r, ok := b.runs[runID]
	b.mu.RUnlock()
	if !ok {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]BBListRow, 0, limit)
	now := time.Now()
	// Reverse-chronological walk
	for i := len(r.posts) - 1; i >= 0 && len(out) < limit; i-- {
		p := r.posts[i]
		if tag != "" && !containsString(p.Tags, tag) {
			continue
		}
		out = append(out, BBListRow{
			PostID:  p.ID,
			AgentID: p.AgentID,
			Text:    p.Text,
			Tags:    p.Tags,
			AgeMS:   now.Sub(p.PostedAt).Milliseconds(),
		})
	}
	return out, true
}

// BBClaimResult is CLAIM's return.
type BBClaimResult struct {
	Claimed bool   `json:"claimed"`
	Owner   string `json:"owner"` // populated when Claimed=false
	TTLMS   int64  `json:"ttl_ms"`
}

// Claim atomically reserves a task. Returns Claimed=true on first
// caller, Claimed=false + Owner on subsequent callers (unless the
// holding claim's TTL has expired). TTL=0 means no expiry.
func (b *AgentBlackboard) Claim(runID, taskID, agentID string, ttl time.Duration) (BBClaimResult, error) {
	if runID == "" {
		return BBClaimResult{}, errors.New("run_id required")
	}
	if taskID == "" {
		return BBClaimResult{}, errors.New("task_id required")
	}
	if agentID == "" {
		return BBClaimResult{}, errors.New("agent_id required")
	}
	if ttl < 0 {
		return BBClaimResult{}, errors.New("ttl must be non-negative")
	}
	b.totalClaims.Add(1)
	r := b.runOrCreate(runID)
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.claims[taskID]; ok {
		// Expired claims are auto-released
		if !existing.ExpiresAt.IsZero() && now.After(existing.ExpiresAt) {
			delete(r.claims, taskID)
		} else {
			b.claimConflicts.Add(1)
			ttlMS := int64(0)
			if !existing.ExpiresAt.IsZero() {
				ttlMS = existing.ExpiresAt.Sub(now).Milliseconds()
				if ttlMS < 0 {
					ttlMS = 0
				}
			}
			return BBClaimResult{Claimed: false, Owner: existing.AgentID, TTLMS: ttlMS}, nil
		}
	}
	cl := &bbClaim{TaskID: taskID, AgentID: agentID, ClaimedAt: now}
	if ttl > 0 {
		cl.ExpiresAt = now.Add(ttl)
	}
	r.claims[taskID] = cl
	out := BBClaimResult{Claimed: true, Owner: agentID}
	if ttl > 0 {
		out.TTLMS = ttl.Milliseconds()
	}
	return out, nil
}

// Release drops a claim, only if the caller owns it. Returns 1 on
// success, 0 if not held or held by someone else.
func (b *AgentBlackboard) Release(runID, taskID, agentID string) (int, error) {
	if runID == "" || taskID == "" || agentID == "" {
		return 0, errors.New("run_id, task_id, agent_id required")
	}
	b.mu.RLock()
	r, ok := b.runs[runID]
	b.mu.RUnlock()
	if !ok {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.claims[taskID]
	if !ok {
		return 0, nil
	}
	if c.AgentID != agentID {
		return 0, nil
	}
	delete(r.claims, taskID)
	return 1, nil
}

// BBClaimRow is one row of CLAIMS.
type BBClaimRow struct {
	TaskID    string `json:"task_id"`
	AgentID   string `json:"agent_id"`
	ClaimedAt int64  `json:"claimed_unix"`
	TTLMS     int64  `json:"ttl_ms"`
	Expired   bool   `json:"expired"`
}

// Claims lists every active claim on a run (expired claims are surfaced
// with Expired=true so the caller can see why a task isn't being
// re-claimed).
func (b *AgentBlackboard) Claims(runID string) ([]BBClaimRow, bool) {
	if runID == "" {
		return nil, false
	}
	b.mu.RLock()
	r, ok := b.runs[runID]
	b.mu.RUnlock()
	if !ok {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	out := make([]BBClaimRow, 0, len(r.claims))
	for _, c := range r.claims {
		row := BBClaimRow{
			TaskID:    c.TaskID,
			AgentID:   c.AgentID,
			ClaimedAt: c.ClaimedAt.Unix(),
		}
		if !c.ExpiresAt.IsZero() {
			row.TTLMS = c.ExpiresAt.Sub(now).Milliseconds()
			if row.TTLMS <= 0 {
				row.Expired = true
				row.TTLMS = 0
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TaskID < out[j].TaskID
	})
	return out, true
}

// Drop wipes a run's board. runID="ALL" clears every run.
func (b *AgentBlackboard) Drop(runID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if runID == "ALL" {
		n := len(b.runs)
		b.runs = map[string]*bbRun{}
		return n
	}
	if _, ok := b.runs[runID]; ok {
		delete(b.runs, runID)
		return 1
	}
	return 0
}

// ListRuns returns every active run id, sorted.
func (b *AgentBlackboard) ListRuns() []string {
	b.mu.RLock()
	out := make([]string, 0, len(b.runs))
	for k := range b.runs {
		out = append(out, k)
	}
	b.mu.RUnlock()
	sort.Strings(out)
	return out
}

// BBStats is the global snapshot.
type BBStats struct {
	Runs           int   `json:"runs"`
	TotalPosts     int64 `json:"total_posts"`
	TotalReads     int64 `json:"total_reads"`
	TotalClaims    int64 `json:"total_claims"`
	ClaimConflicts int64 `json:"claim_conflicts"`
	ActivePosts    int   `json:"active_posts"`
	ActiveClaims   int   `json:"active_claims"`
}

// Stats returns the global counters + a current size snapshot.
func (b *AgentBlackboard) Stats() BBStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	posts, claims := 0, 0
	for _, r := range b.runs {
		r.mu.RLock()
		posts += len(r.posts)
		claims += len(r.claims)
		r.mu.RUnlock()
	}
	return BBStats{
		Runs:           len(b.runs),
		TotalPosts:     b.totalPosts.Load(),
		TotalReads:     b.totalReads.Load(),
		TotalClaims:    b.totalClaims.Load(),
		ClaimConflicts: b.claimConflicts.Load(),
		ActivePosts:    posts,
		ActiveClaims:   claims,
	}
}

// ─── internals ──────────────────────────────────────────────────

func (b *AgentBlackboard) runOrCreate(id string) *bbRun {
	b.mu.RLock()
	r, ok := b.runs[id]
	b.mu.RUnlock()
	if ok {
		return r
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.runs[id]; ok {
		return r
	}
	r = &bbRun{claims: map[string]*bbClaim{}}
	b.runs[id] = r
	return r
}

func dedupTagsBB(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

