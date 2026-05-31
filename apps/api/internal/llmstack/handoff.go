package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Handoffs is the structured subagent spawn/join primitive every
// agent framework reimplements. The pattern: parent agent SPAWNs a
// subagent with a context budget + a return contract (what it must
// produce); the subagent does its work and RETURNs a typed result;
// the parent JOINs (blocks for the result, with a deadline so a
// crashed/looping subagent doesn't hang the parent).
//
// What this gives you that ad-hoc job queues don't:
//
//   1. Typed contract — return_schema is declared at spawn and the
//      RETURN call verifies its keys are present. Parent code doesn't
//      have to defensively check the bag.
//
//   2. Budget accounting — token budget is declared up-front, decremented
//      by REPORT_USAGE calls. Exceeding the budget terminates the handoff
//      so a runaway subagent can't bankrupt the parent.
//
//   3. Deadline-aware join — JOIN polls with a timeout that's a hard
//      cap; a crashed subagent's slot times out cleanly.
//
// Commands:
//
//   HANDOFF.SPAWN parent task [BUDGET tokens] [DEADLINE ms]
//        [RETURN k1,k2,...] [META k v ...]
//        → handoff-id (server-assigned, unique)
//   HANDOFF.REPORT_USAGE id tokens
//        Decrements the budget; over-budget → status=cancelled.
//   HANDOFF.RETURN id key value [key value ...]
//        Validates keys against the return contract; missing key → error.
//   HANDOFF.JOIN id [TIMEOUT ms]
//        Blocks (poll loop, 5ms granularity) until the handoff is
//        complete, cancelled, or the timeout fires.
//   HANDOFF.STATUS id            — non-blocking status snapshot
//   HANDOFF.CANCEL id reason
//   HANDOFF.LIST [PARENT p]
//   HANDOFF.FORGET id|ALL
//   HANDOFF.STATS
//
// Hot path: every command is one map lookup; JOIN's poll loop sleeps
// 5ms between checks so a million-msg/s system doesn't burn cores.
type Handoffs struct {
	mu        sync.RWMutex
	handoffs  map[string]*handoff
	nextID    atomic.Int64

	totalSpawns atomic.Int64
	totalJoins  atomic.Int64
	totalReturns atomic.Int64
	totalCancels atomic.Int64
	totalTimeouts atomic.Int64
}

type handoff struct {
	mu              sync.Mutex
	id              string
	parent          string
	task            string
	status          string // pending, returned, cancelled
	createdAt       time.Time
	deadline        time.Time // zero means no deadline
	budgetTokens    int64
	usedTokens      int64
	requiredKeys    []string
	returnedFields  map[string]string
	cancelReason    string
	meta            map[string]string
}

// NewHandoffs returns an empty registry.
func NewHandoffs() *Handoffs {
	return &Handoffs{handoffs: map[string]*handoff{}}
}

// HandoffSpawnResult is SPAWN's return.
type HandoffSpawnResult struct {
	ID string `json:"id"`
}

// Spawn opens a new handoff. budget=0 means no token cap; deadline=0
// means no time cap; requiredKeys=nil means RETURN accepts anything.
func (h *Handoffs) Spawn(parent, task string, budget int64, deadline time.Duration, requiredKeys []string, meta map[string]string) (HandoffSpawnResult, error) {
	if parent == "" {
		return HandoffSpawnResult{}, errors.New("parent required")
	}
	if task == "" {
		return HandoffSpawnResult{}, errors.New("task required")
	}
	if budget < 0 {
		return HandoffSpawnResult{}, errors.New("budget must be non-negative")
	}
	if deadline < 0 {
		return HandoffSpawnResult{}, errors.New("deadline must be non-negative")
	}
	id := "ho-" + u32x(uint32(h.nextID.Add(1)))
	hd := &handoff{
		id:            id,
		parent:        parent,
		task:          task,
		status:        "pending",
		createdAt:     time.Now(),
		budgetTokens:  budget,
		requiredKeys:  append([]string{}, requiredKeys...),
		returnedFields: map[string]string{},
		meta:          copyMetaProv(meta),
	}
	if deadline > 0 {
		hd.deadline = time.Now().Add(deadline)
	}
	h.mu.Lock()
	h.handoffs[id] = hd
	h.mu.Unlock()
	h.totalSpawns.Add(1)
	return HandoffSpawnResult{ID: id}, nil
}

// HandoffUsageResult is REPORT_USAGE's return.
type HandoffUsageResult struct {
	UsedTokens    int64  `json:"used_tokens"`
	BudgetTokens  int64  `json:"budget_tokens"`
	Remaining     int64  `json:"remaining"`
	OverBudget    bool   `json:"over_budget"`
}

// ReportUsage debits tokens. Crossing the budget cancels the handoff
// (status="cancelled", reason="budget exhausted").
func (h *Handoffs) ReportUsage(id string, tokens int64) (HandoffUsageResult, error) {
	if id == "" {
		return HandoffUsageResult{}, errors.New("id required")
	}
	if tokens < 0 {
		return HandoffUsageResult{}, errors.New("tokens must be non-negative")
	}
	h.mu.RLock()
	hd, ok := h.handoffs[id]
	h.mu.RUnlock()
	if !ok {
		return HandoffUsageResult{}, errors.New("unknown handoff: " + id)
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	hd.usedTokens += tokens
	out := HandoffUsageResult{
		UsedTokens: hd.usedTokens, BudgetTokens: hd.budgetTokens,
	}
	if hd.budgetTokens > 0 {
		out.Remaining = hd.budgetTokens - hd.usedTokens
		if out.Remaining < 0 {
			out.OverBudget = true
			if hd.status == "pending" {
				hd.status = "cancelled"
				hd.cancelReason = "budget exhausted"
				h.totalCancels.Add(1)
			}
		}
	}
	return out, nil
}

// Return submits the subagent's result. Missing required keys → error.
// Returning twice is rejected — handoffs are write-once.
func (h *Handoffs) Return(id string, fields map[string]string) error {
	if id == "" {
		return errors.New("id required")
	}
	if len(fields) == 0 {
		return errors.New("at least one field required")
	}
	h.mu.RLock()
	hd, ok := h.handoffs[id]
	h.mu.RUnlock()
	if !ok {
		return errors.New("unknown handoff: " + id)
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	if hd.status != "pending" {
		return errors.New("handoff already " + hd.status)
	}
	for _, k := range hd.requiredKeys {
		if _, ok := fields[k]; !ok {
			return errors.New("missing required key: " + k)
		}
	}
	for k, v := range fields {
		hd.returnedFields[k] = v
	}
	hd.status = "returned"
	h.totalReturns.Add(1)
	return nil
}

// HandoffStatus is STATUS's return.
type HandoffStatus struct {
	ID            string            `json:"id"`
	Parent        string            `json:"parent"`
	Task          string            `json:"task"`
	Status        string            `json:"status"`
	AgeMS         int64             `json:"age_ms"`
	BudgetTokens  int64             `json:"budget_tokens"`
	UsedTokens    int64             `json:"used_tokens"`
	DeadlineMS    int64             `json:"deadline_ms"` // ms remaining (0 if unset, negative if past)
	Returned      map[string]string `json:"returned,omitempty"`
	CancelReason  string            `json:"cancel_reason,omitempty"`
}

// Status returns the non-blocking snapshot. The deadline check is
// done here so STATUS/JOIN flip to "cancelled" on expiry without a
// background goroutine.
func (h *Handoffs) Status(id string) (HandoffStatus, bool) {
	h.mu.RLock()
	hd, ok := h.handoffs[id]
	h.mu.RUnlock()
	if !ok {
		return HandoffStatus{}, false
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	now := time.Now()
	// Lazy deadline enforcement
	if hd.status == "pending" && !hd.deadline.IsZero() && now.After(hd.deadline) {
		hd.status = "cancelled"
		hd.cancelReason = "deadline exceeded"
		h.totalTimeouts.Add(1)
	}
	out := HandoffStatus{
		ID: hd.id, Parent: hd.parent, Task: hd.task, Status: hd.status,
		AgeMS: now.Sub(hd.createdAt).Milliseconds(),
		BudgetTokens: hd.budgetTokens, UsedTokens: hd.usedTokens,
		CancelReason: hd.cancelReason,
	}
	if !hd.deadline.IsZero() {
		out.DeadlineMS = hd.deadline.Sub(now).Milliseconds()
	}
	if hd.status == "returned" {
		out.Returned = copyMetaProv(hd.returnedFields)
	}
	return out, true
}

// Join blocks until the handoff is complete, cancelled, or the
// timeout fires. timeout=0 → wait forever (still bounded by the
// handoff's own deadline if any).
func (h *Handoffs) Join(id string, timeout time.Duration) (HandoffStatus, bool) {
	h.totalJoins.Add(1)
	start := time.Now()
	for {
		st, ok := h.Status(id)
		if !ok {
			return HandoffStatus{}, false
		}
		if st.Status != "pending" {
			return st, true
		}
		if timeout > 0 && time.Since(start) >= timeout {
			return st, true // last seen state; status is still "pending"
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Cancel terminates a pending handoff with a reason. Already-cancelled
// or already-returned handoffs are unaffected.
func (h *Handoffs) Cancel(id, reason string) (int, error) {
	if id == "" {
		return 0, errors.New("id required")
	}
	h.mu.RLock()
	hd, ok := h.handoffs[id]
	h.mu.RUnlock()
	if !ok {
		return 0, errors.New("unknown handoff: " + id)
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	if hd.status != "pending" {
		return 0, nil
	}
	hd.status = "cancelled"
	if reason == "" {
		reason = "user cancelled"
	}
	hd.cancelReason = reason
	h.totalCancels.Add(1)
	return 1, nil
}

// HandoffListRow is one row of LIST.
type HandoffListRow struct {
	ID        string `json:"id"`
	Parent    string `json:"parent"`
	Task      string `json:"task"`
	Status    string `json:"status"`
	AgeMS     int64  `json:"age_ms"`
}

// List returns every active handoff (filtered by parent if given).
func (h *Handoffs) List(parent string) []HandoffListRow {
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := time.Now()
	out := make([]HandoffListRow, 0, len(h.handoffs))
	for _, hd := range h.handoffs {
		hd.mu.Lock()
		if parent != "" && hd.parent != parent {
			hd.mu.Unlock()
			continue
		}
		out = append(out, HandoffListRow{
			ID: hd.id, Parent: hd.parent, Task: hd.task,
			Status: hd.status, AgeMS: now.Sub(hd.createdAt).Milliseconds(),
		})
		hd.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Forget drops a handoff (or all). id="ALL" wipes everything.
func (h *Handoffs) Forget(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == "ALL" {
		n := len(h.handoffs)
		h.handoffs = map[string]*handoff{}
		return n
	}
	if _, ok := h.handoffs[id]; ok {
		delete(h.handoffs, id)
		return 1
	}
	return 0
}

// HandoffStats is the global snapshot.
type HandoffStats struct {
	Active        int   `json:"active"`
	TotalSpawns   int64 `json:"total_spawns"`
	TotalJoins    int64 `json:"total_joins"`
	TotalReturns  int64 `json:"total_returns"`
	TotalCancels  int64 `json:"total_cancels"`
	TotalTimeouts int64 `json:"total_timeouts"`
}

func (h *Handoffs) Stats() HandoffStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return HandoffStats{
		Active:        len(h.handoffs),
		TotalSpawns:   h.totalSpawns.Load(),
		TotalJoins:    h.totalJoins.Load(),
		TotalReturns:  h.totalReturns.Load(),
		TotalCancels:  h.totalCancels.Load(),
		TotalTimeouts: h.totalTimeouts.Load(),
	}
}
