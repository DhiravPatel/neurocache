package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FairQueue is the weighted-fair-queueing primitive that completes
// the rate-limit / queueing story.
//
// RATELIMIT *rejects* requests when over budget — that protects the
// system but burns the caller (429s, retries, surfaced errors).
// FAIRQUEUE *parks* requests by tenant priority and drains them at
// the system's allowed rate, so a free-tier burst doesn't starve a
// paid tenant and nothing 429s that didn't have to. Apps wire it as
// "the request queue between the API gateway and the LLM caller."
//
// Algorithm: stride scheduling — a deterministic weighted-fair queue
// that gives each tenant scheduled "passes" proportional to weight.
//   - Each tenant has stride = 1.0 / weight (lower weight → bigger
//     stride → fewer passes per cycle).
//   - Each tenant has a "pass" counter (the next virtual time
//     they're due to be served).
//   - DEQUEUE picks the tenant with the smallest pass that has
//     pending requests; bumps that pass by stride.
//
// Compared to deficit round-robin, stride scheduling is
// O(log tenants) per DEQUEUE with a heap, deterministic, and easy
// to reason about for compliance reviews.
//
// Commands:
//
//   FAIRQUEUE.CONFIG queue-id [TENANT t WEIGHT n]+
//        Set tenant weights. Repeated TENANT t WEIGHT n triples
//        register or update one tenant each. Weight must be > 0.
//   FAIRQUEUE.ENQUEUE queue-id tenant request-id [PAYLOAD p]
//        → position in queue (overall) at insertion time.
//   FAIRQUEUE.DEQUEUE queue-id
//        → {tenant, request_id, payload, waited_ms} or nil if empty.
//   FAIRQUEUE.PEEK queue-id [LIMIT n]
//        → next-up entries without dequeuing.
//   FAIRQUEUE.LEN queue-id [TENANT t]
//        → overall depth, or per-tenant depth.
//   FAIRQUEUE.DROPTENANT queue-id tenant
//        Remove tenant + its parked requests.
//   FAIRQUEUE.RESET queue-id|ALL
//   FAIRQUEUE.LIST
//   FAIRQUEUE.STATS
//
// Hot path: ENQUEUE is O(1). DEQUEUE is O(tenants) without the heap
// optimisation — plenty fast at typical tenant counts (≤100).
type FairQueue struct {
	mu     sync.RWMutex
	queues map[string]*fqQueue

	totalEnqueues atomic.Int64
	totalDequeues atomic.Int64
}

type fqQueue struct {
	mu      sync.Mutex
	tenants map[string]*fqTenant
}

type fqTenant struct {
	weight  float64
	stride  float64
	pass    float64
	pending []fqEntry
}

type fqEntry struct {
	RequestID string
	Payload   string
	TS        int64
}

// NewFairQueue returns an empty store.
func NewFairQueue() *FairQueue {
	return &FairQueue{queues: map[string]*fqQueue{}}
}

// Configure registers / updates tenant weights. Weights are absolute
// (not a fraction); a tenant with weight 10 gets 10× the schedule
// slots of weight 1. Passing weight=0 removes a tenant configured
// previously.
func (f *FairQueue) Configure(queueID string, weights map[string]float64) error {
	if queueID == "" {
		return errors.New("queue_id required")
	}
	for t, w := range weights {
		if t == "" {
			return errors.New("tenant name required")
		}
		if w < 0 {
			return errors.New("weight must be non-negative")
		}
	}
	q := f.queueOrCreate(queueID)
	q.mu.Lock()
	defer q.mu.Unlock()
	for t, w := range weights {
		if w == 0 {
			delete(q.tenants, t)
			continue
		}
		stride := 1.0 / w
		if existing, ok := q.tenants[t]; ok {
			existing.weight = w
			existing.stride = stride
			continue
		}
		q.tenants[t] = &fqTenant{
			weight: w, stride: stride, pass: stride,
		}
	}
	return nil
}

// Enqueue parks a request under one tenant. Returns the overall
// queue depth at insertion (handy for "you're #34 in line" UX).
func (f *FairQueue) Enqueue(queueID, tenant, requestID, payload string) (int, error) {
	if queueID == "" {
		return 0, errors.New("queue_id required")
	}
	if tenant == "" {
		return 0, errors.New("tenant required")
	}
	if requestID == "" {
		return 0, errors.New("request_id required")
	}
	f.totalEnqueues.Add(1)
	q := f.queueOrCreate(queueID)
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tenants[tenant]
	if !ok {
		return 0, errors.New("tenant not configured (call FAIRQUEUE.CONFIG): " + tenant)
	}
	t.pending = append(t.pending, fqEntry{
		RequestID: requestID, Payload: payload, TS: time.Now().UnixNano(),
	})
	depth := 0
	for _, tt := range q.tenants {
		depth += len(tt.pending)
	}
	return depth, nil
}

// FQDequeueResult is DEQUEUE's return.
type FQDequeueResult struct {
	Tenant    string `json:"tenant"`
	RequestID string `json:"request_id"`
	Payload   string `json:"payload"`
	WaitedMS  int64  `json:"waited_ms"`
}

// Dequeue pops the next request in weighted-fair order. Returns
// ok=false when every tenant is empty.
func (f *FairQueue) Dequeue(queueID string) (FQDequeueResult, bool) {
	f.totalDequeues.Add(1)
	f.mu.RLock()
	q, ok := f.queues[queueID]
	f.mu.RUnlock()
	if !ok {
		return FQDequeueResult{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// Find tenant with smallest pass that has pending requests
	var winner string
	var winnerPass = 0.0
	first := true
	for name, t := range q.tenants {
		if len(t.pending) == 0 {
			continue
		}
		if first || t.pass < winnerPass {
			winner = name
			winnerPass = t.pass
			first = false
		}
	}
	if winner == "" {
		return FQDequeueResult{}, false
	}
	t := q.tenants[winner]
	entry := t.pending[0]
	t.pending = t.pending[1:]
	t.pass += t.stride
	return FQDequeueResult{
		Tenant: winner, RequestID: entry.RequestID,
		Payload: entry.Payload,
		WaitedMS: (time.Now().UnixNano() - entry.TS) / int64(time.Millisecond),
	}, true
}

// FQPeekRow is one PEEK row.
type FQPeekRow struct {
	Tenant    string `json:"tenant"`
	RequestID string `json:"request_id"`
	WaitedMS  int64  `json:"waited_ms"`
}

// Peek returns the next n entries in dequeue order without removing
// them. limit=0 returns the entire queue.
func (f *FairQueue) Peek(queueID string, limit int) ([]FQPeekRow, bool) {
	f.mu.RLock()
	q, ok := f.queues[queueID]
	f.mu.RUnlock()
	if !ok {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// Snapshot pending lists + passes
	type cursor struct {
		tenant string
		idx    int
		pass   float64
		stride float64
	}
	cursors := make([]*cursor, 0, len(q.tenants))
	for name, t := range q.tenants {
		if len(t.pending) == 0 {
			continue
		}
		cursors = append(cursors, &cursor{
			tenant: name, idx: 0, pass: t.pass, stride: t.stride,
		})
	}
	out := make([]FQPeekRow, 0, 8)
	now := time.Now().UnixNano()
	for {
		if len(cursors) == 0 {
			break
		}
		// Find smallest pass
		bestI := 0
		for i, c := range cursors {
			if c.pass < cursors[bestI].pass {
				bestI = i
			}
		}
		c := cursors[bestI]
		t := q.tenants[c.tenant]
		entry := t.pending[c.idx]
		out = append(out, FQPeekRow{
			Tenant: c.tenant, RequestID: entry.RequestID,
			WaitedMS: (now - entry.TS) / int64(time.Millisecond),
		})
		if limit > 0 && len(out) >= limit {
			break
		}
		c.idx++
		c.pass += c.stride
		if c.idx >= len(t.pending) {
			cursors = append(cursors[:bestI], cursors[bestI+1:]...)
		}
	}
	return out, true
}

// Len returns the overall queue depth (tenant="") or per-tenant depth.
func (f *FairQueue) Len(queueID, tenant string) (int, bool) {
	f.mu.RLock()
	q, ok := f.queues[queueID]
	f.mu.RUnlock()
	if !ok {
		return 0, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if tenant != "" {
		t, ok := q.tenants[tenant]
		if !ok {
			return 0, false
		}
		return len(t.pending), true
	}
	n := 0
	for _, t := range q.tenants {
		n += len(t.pending)
	}
	return n, true
}

// DropTenant removes a tenant + their pending requests.
func (f *FairQueue) DropTenant(queueID, tenant string) (int, bool) {
	f.mu.RLock()
	q, ok := f.queues[queueID]
	f.mu.RUnlock()
	if !ok {
		return 0, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tenants[tenant]
	if !ok {
		return 0, false
	}
	dropped := len(t.pending)
	delete(q.tenants, tenant)
	return dropped, true
}

// List returns every queue id, sorted.
func (f *FairQueue) List() []string {
	f.mu.RLock()
	out := make([]string, 0, len(f.queues))
	for k := range f.queues {
		out = append(out, k)
	}
	f.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a queue. queueID="ALL" wipes all.
func (f *FairQueue) Reset(queueID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if queueID == "ALL" {
		n := len(f.queues)
		f.queues = map[string]*fqQueue{}
		return n
	}
	if _, ok := f.queues[queueID]; ok {
		delete(f.queues, queueID)
		return 1
	}
	return 0
}

// FairQueueStats is the global snapshot.
type FairQueueStats struct {
	Queues        int   `json:"queues"`
	TotalParked   int   `json:"total_parked"`
	TotalEnqueues int64 `json:"total_enqueues"`
	TotalDequeues int64 `json:"total_dequeues"`
}

func (f *FairQueue) Stats() FairQueueStats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	parked := 0
	for _, q := range f.queues {
		q.mu.Lock()
		for _, t := range q.tenants {
			parked += len(t.pending)
		}
		q.mu.Unlock()
	}
	return FairQueueStats{
		Queues:        len(f.queues),
		TotalParked:   parked,
		TotalEnqueues: f.totalEnqueues.Load(),
		TotalDequeues: f.totalDequeues.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (f *FairQueue) queueOrCreate(id string) *fqQueue {
	f.mu.RLock()
	q, ok := f.queues[id]
	f.mu.RUnlock()
	if ok {
		return q
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if q, ok := f.queues[id]; ok {
		return q
	}
	q = &fqQueue{tenants: map[string]*fqTenant{}}
	f.queues[id] = q
	return q
}
