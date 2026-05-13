package llmstack

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// HedgeTracker coordinates hedged upstream calls across N providers.
// Real production pain: tail latency in LLM apps is dominated by
// occasional slow upstream calls (a single provider hiccup adds 5-10s
// to a 99th-percentile request). The standard fix is "send to N
// providers in parallel, first wins, cancel the rest" — but each team
// rebuilds it with subtly broken cancellation semantics + no
// per-provider stats.
//
// HEDGE.* gives the cache a single coordination point:
//
//   HEDGE.START call-id provider1 provider2 ...   -> token
//        Register a hedged call. Token authenticates PUBLISH.
//
//   HEDGE.PUBLISH call-id provider result token   -> is_winner
//        First publish wins; subsequent publishes from other
//        providers are recorded as "late arrivals" for telemetry
//        but the result is NOT swapped in.
//
//   HEDGE.WAIT call-id timeout-ms                 -> first result
//        Block until anyone publishes. Same channel-broadcast
//        wakeup as COALESCE — thousands of waiters, no polling.
//
//   HEDGE.STATUS call-id    -> per-provider state + latencies
//   HEDGE.FORGET call-id
//   HEDGE.STATS             -> per-provider win counts, p50/p99
//                              latency, total hedges, ms saved
//
// Latency-saved telemetry: every hedge records the gap between
// winner_at and the latest late_arrival. Summed, this is "how much
// time the hedging strategy actually saved you." Operators tune the
// N-providers list based on the per-provider win-rate and the saved
// total — when one provider always wins, drop the redundant peers.
//
// Implementation: per-call state with `winnerCh chan struct{}`
// closed exactly once on first PUBLISH. Atomic CAS on `winnerIdx`
// ensures only one publisher claims the winner slot even under
// concurrent publishes.
type HedgeTracker struct {
	mu    sync.RWMutex
	calls map[string]*hedgeCall

	providerStats sync.Map // provider -> *providerStat
	totalHedges   atomic.Int64
	totalSavedMS  atomic.Int64
}

type hedgeCall struct {
	token       string
	providers   []string                  // declared at START
	startNS     int64
	winnerIdx   atomic.Int32              // -1 until decided
	winnerAtNS  atomic.Int64
	result      string
	late        sync.Map                  // provider -> *lateArrival
	winnerCh    chan struct{}
	mu          sync.Mutex                // guards result write under winner CAS
}

type lateArrival struct {
	arrivedAtNS int64
	result      string
}

type providerStat struct {
	wins      atomic.Int64
	lateNS    atomic.Int64 // sum of late-arrival latencies
	totalNS   atomic.Int64 // sum of all latencies (wins + late)
	totalCalls atomic.Int64
}

// NewHedgeTracker returns an empty tracker.
func NewHedgeTracker() *HedgeTracker {
	return &HedgeTracker{calls: map[string]*hedgeCall{}}
}

// StartResult is the HEDGE.START return.
type StartResult struct {
	Token     string   `json:"token"`
	Providers []string `json:"providers"`
}

// Start registers a hedged call. Replacing an existing call_id is
// allowed (apps occasionally retry with the same id).
func (h *HedgeTracker) Start(callID string, providers []string) (StartResult, error) {
	if callID == "" {
		return StartResult{}, errEmptyCallID
	}
	if len(providers) < 1 {
		return StartResult{}, errNoProviders
	}
	tok := newHedgeToken()
	hc := &hedgeCall{
		token:     tok,
		providers: providers,
		startNS:   time.Now().UnixNano(),
		winnerCh:  make(chan struct{}),
	}
	hc.winnerIdx.Store(-1)

	h.mu.Lock()
	h.calls[callID] = hc
	h.mu.Unlock()
	h.totalHedges.Add(1)
	return StartResult{Token: tok, Providers: providers}, nil
}

// PublishResult is the HEDGE.PUBLISH return.
type PublishResult struct {
	IsWinner    bool   `json:"is_winner"`
	Winner      string `json:"winner"`           // who actually won
	LatencyMS   int64  `json:"latency_ms"`       // this provider's latency
	WinnerLatMS int64  `json:"winner_latency_ms"` // winner's latency
}

// Publish records a result from `provider`. The first PUBLISH per
// call wins and stamps the winner. Subsequent publishes are
// recorded as late arrivals for telemetry. Bad token rejects.
func (h *HedgeTracker) Publish(callID, provider, result, token string) (PublishResult, bool) {
	h.mu.RLock()
	hc, ok := h.calls[callID]
	h.mu.RUnlock()
	if !ok || hc.token != token {
		return PublishResult{}, false
	}

	provIdx := indexOfStr(hc.providers, provider)
	if provIdx < 0 {
		return PublishResult{}, false
	}

	now := time.Now().UnixNano()
	stat := h.statFor(provider)
	stat.totalCalls.Add(1)
	stat.totalNS.Add(now - hc.startNS)

	// First publish wins via CAS on winnerIdx (-1 → provIdx).
	if hc.winnerIdx.CompareAndSwap(-1, int32(provIdx)) {
		hc.mu.Lock()
		hc.result = result
		hc.mu.Unlock()
		hc.winnerAtNS.Store(now)
		close(hc.winnerCh)
		stat.wins.Add(1)
		return PublishResult{
			IsWinner:    true,
			Winner:      provider,
			LatencyMS:   (now - hc.startNS) / int64(time.Millisecond),
			WinnerLatMS: (now - hc.startNS) / int64(time.Millisecond),
		}, true
	}

	// Late arrival.
	hc.late.Store(provider, &lateArrival{arrivedAtNS: now, result: result})
	winnerAt := hc.winnerAtNS.Load()
	savedNS := now - winnerAt
	h.totalSavedMS.Add(savedNS / int64(time.Millisecond))
	stat.lateNS.Add(now - hc.startNS)

	winnerIdx := hc.winnerIdx.Load()
	winnerName := ""
	if winnerIdx >= 0 && int(winnerIdx) < len(hc.providers) {
		winnerName = hc.providers[winnerIdx]
	}
	return PublishResult{
		IsWinner:    false,
		Winner:      winnerName,
		LatencyMS:   (now - hc.startNS) / int64(time.Millisecond),
		WinnerLatMS: (winnerAt - hc.startNS) / int64(time.Millisecond),
	}, true
}

// HedgeWaitResult is the HEDGE.WAIT return.
type HedgeWaitResult struct {
	Got       bool   `json:"got"`
	Result    string `json:"result"`
	Winner    string `json:"winner"`
	LatencyMS int64  `json:"latency_ms"`
}

// Wait blocks until the first PUBLISH wins or `timeout` elapses.
// Returns immediately if the winner already exists.
func (h *HedgeTracker) Wait(callID string, timeout time.Duration) HedgeWaitResult {
	h.mu.RLock()
	hc, ok := h.calls[callID]
	h.mu.RUnlock()
	if !ok {
		return HedgeWaitResult{}
	}
	if hc.winnerIdx.Load() >= 0 {
		return hedgeResultOf(hc)
	}
	select {
	case <-hc.winnerCh:
		return hedgeResultOf(hc)
	case <-time.After(timeout):
		return HedgeWaitResult{Got: false}
	}
}

// HedgeStatusRow is one provider's per-call state.
type HedgeStatusRow struct {
	Provider     string `json:"provider"`
	State        string `json:"state"`     // pending|winner|late
	LatencyMS    int64  `json:"latency_ms,omitempty"`
}

// Status returns per-provider state for a call.
type HedgeStatus struct {
	CallID      string           `json:"call_id"`
	Winner      string           `json:"winner,omitempty"`
	WinnerLatMS int64            `json:"winner_latency_ms,omitempty"`
	StartedAt   int64            `json:"started_at_unix"`
	Providers   []HedgeStatusRow `json:"providers"`
}

func (h *HedgeTracker) Status(callID string) (HedgeStatus, bool) {
	h.mu.RLock()
	hc, ok := h.calls[callID]
	h.mu.RUnlock()
	if !ok {
		return HedgeStatus{}, false
	}
	out := HedgeStatus{
		CallID:    callID,
		StartedAt: hc.startNS / int64(time.Second),
	}
	winnerIdx := hc.winnerIdx.Load()
	if winnerIdx >= 0 && int(winnerIdx) < len(hc.providers) {
		out.Winner = hc.providers[winnerIdx]
		out.WinnerLatMS = (hc.winnerAtNS.Load() - hc.startNS) / int64(time.Millisecond)
	}
	for i, p := range hc.providers {
		row := HedgeStatusRow{Provider: p, State: "pending"}
		if int32(i) == winnerIdx {
			row.State = "winner"
			row.LatencyMS = out.WinnerLatMS
		} else if v, ok := hc.late.Load(p); ok {
			la := v.(*lateArrival)
			row.State = "late"
			row.LatencyMS = (la.arrivedAtNS - hc.startNS) / int64(time.Millisecond)
		}
		out.Providers = append(out.Providers, row)
	}
	return out, true
}

// Forget drops a call. Wakes any pending waiters with got=false.
func (h *HedgeTracker) Forget(callID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	hc, ok := h.calls[callID]
	if !ok {
		return false
	}
	if hc.winnerIdx.Load() < 0 {
		close(hc.winnerCh)
	}
	delete(h.calls, callID)
	return true
}

// ProviderStatsRow is one row of HEDGE.STATS providers list.
type ProviderStatsRow struct {
	Provider     string  `json:"provider"`
	Wins         int64   `json:"wins"`
	TotalCalls   int64   `json:"total_calls"`
	WinRate      float64 `json:"win_rate"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
}

// HedgeStatsSnapshot is HEDGE.STATS return.
type HedgeStatsSnapshot struct {
	Providers     []ProviderStatsRow `json:"providers"`
	TotalHedges   int64              `json:"total_hedges"`
	TotalSavedMS  int64              `json:"total_saved_ms"`
	ActiveCalls   int                `json:"active_calls"`
}

func (h *HedgeTracker) Stats() HedgeStatsSnapshot {
	h.mu.RLock()
	n := len(h.calls)
	h.mu.RUnlock()
	out := HedgeStatsSnapshot{
		TotalHedges:  h.totalHedges.Load(),
		TotalSavedMS: h.totalSavedMS.Load(),
		ActiveCalls:  n,
	}
	h.providerStats.Range(func(k, v any) bool {
		name := k.(string)
		s := v.(*providerStat)
		total := s.totalCalls.Load()
		wins := s.wins.Load()
		rate := 0.0
		if total > 0 {
			rate = float64(wins) / float64(total)
		}
		avgMS := int64(0)
		if total > 0 {
			avgMS = (s.totalNS.Load() / total) / int64(time.Millisecond)
		}
		out.Providers = append(out.Providers, ProviderStatsRow{
			Provider:     name,
			Wins:         wins,
			TotalCalls:   total,
			WinRate:      rate,
			AvgLatencyMS: avgMS,
		})
		return true
	})
	sort.Slice(out.Providers, func(i, j int) bool {
		return out.Providers[i].Wins > out.Providers[j].Wins
	})
	return out
}

// ─── helpers ───────────────────────────────────────────────────

func hedgeResultOf(hc *hedgeCall) HedgeWaitResult {
	winnerIdx := hc.winnerIdx.Load()
	if winnerIdx < 0 {
		return HedgeWaitResult{Got: false}
	}
	winnerName := ""
	if int(winnerIdx) < len(hc.providers) {
		winnerName = hc.providers[winnerIdx]
	}
	hc.mu.Lock()
	result := hc.result
	hc.mu.Unlock()
	return HedgeWaitResult{
		Got:       true,
		Result:    result,
		Winner:    winnerName,
		LatencyMS: (hc.winnerAtNS.Load() - hc.startNS) / int64(time.Millisecond),
	}
}

func (h *HedgeTracker) statFor(provider string) *providerStat {
	if v, ok := h.providerStats.Load(provider); ok {
		return v.(*providerStat)
	}
	fresh := &providerStat{}
	actual, _ := h.providerStats.LoadOrStore(provider, fresh)
	return actual.(*providerStat)
}

func indexOfStr(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

func newHedgeToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ─── sentinel errors ───────────────────────────────────────────

var (
	errEmptyCallID = &hedgeErr{"call_id required"}
	errNoProviders = &hedgeErr{"at least one provider required"}
)

type hedgeErr struct{ msg string }

func (e *hedgeErr) Error() string { return e.msg }
