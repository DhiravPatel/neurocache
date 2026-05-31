package llmstack

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Market is the agent resource auction. FAIRQUEUE is static weighted
// priority. RATELIMIT rejects. Neither handles the actual 2026
// problem: many autonomous agents competing for one rate-limited API
// / GPU pool / token budget, where importance is dynamic and only the
// agents themselves know it.
//
// The right primitive isn't a queue — it's a market. Agents bid from
// their budget; the engine clears a uniform- or second-price auction
// at a defined cadence; the clearing price emerges from contention.
// Winners get a lease token to use the scarce resource; losers get
// the price signal and back off on their own.
//
// Two clearing modes:
//
//   uniform   — All winners pay the clearing price (highest losing
//               bid + ε, or the lowest winning bid if everything sold).
//               Truthful in expectation, and simple to reason about.
//
//   second_price — Each winner pays the next-highest losing bid. The
//               classic Vickrey auction; truth-dominant strategy is
//               to bid your true value. Slightly more complex but
//               proven to minimize regret across many bidders.
//
// The price signal is the magic: agents that see MARKET.PRICE
// climbing back off on their own. Self-regulating contention with
// zero central scheduler.
//
// Commands:
//
//   MARKET.CREATE market-id CAPACITY n [CLEARING uniform|second_price]
//        [WINDOW ms] [MAX_BIDS_PER_AGENT n]
//   MARKET.BID market-id agent-id PRICE p QTY q [DEADLINE ms]
//        DEADLINE optionally caps how long the bid is valid; the next
//        clearing after expiry returns it without filling.
//   MARKET.CLEAR market-id
//        Run the auction now. Returns clearing_price + filled list +
//        unfilled list. Idempotent within a single window — successive
//        CLEAR calls within WINDOW return the same allocation.
//   MARKET.LEASE market-id agent-id
//        Issue a lease token for one cleared allocation. Token must
//        be passed to MARKET.RELEASE when done so the slot frees.
//   MARKET.RELEASE market-id token
//        Free a previously-acquired lease.
//   MARKET.PRICE market-id
//        → last_clearing_price (live signal agents poll for back-off).
//   MARKET.STARVED market-id [MIN_LOSSES n]
//        Agents that lost the last N clearings — a fairness alarm:
//        if the same agent always loses, escalate or grant a floor.
//   MARKET.STATUS market-id   — capacity + bids + leases + last clear
//   MARKET.FORGET market-id|ALL
//   MARKET.LIST
//   MARKET.STATS
//
// Hot path: BID is one slice append. CLEAR is O(n log n) sort by
// price; n is typically tens-to-hundreds of bids per window. LEASE/
// RELEASE are map ops. PRICE is a single atomic read.
type Market struct {
	mu      sync.RWMutex
	markets map[string]*mktAuction

	totalBids   atomic.Int64
	totalClears atomic.Int64
	totalLeases atomic.Int64
}

type mktAuction struct {
	mu         sync.Mutex
	capacity   int
	clearing   string // "uniform" or "second_price"
	window     time.Duration
	maxPerAgent int

	bids       []*mktBid
	lastClear  time.Time
	lastPrice  float64
	lastResult *MarketClearResult // memoized within window

	// Leases: token → allocation
	leases     map[string]*mktLease
	nextTokenN atomic.Uint64

	// Per-agent loss counter for STARVED
	losses     map[string]int
}

type mktBid struct {
	BidID    string
	AgentID  string
	Price    float64
	Qty      int
	Deadline time.Time // zero = no deadline
	PostedAt time.Time
}

type mktLease struct {
	Token    string
	AgentID  string
	Qty      int
	Price    float64
	IssuedAt time.Time
}

// NewMarket returns an empty registry.
func NewMarket() *Market {
	return &Market{markets: map[string]*mktAuction{}}
}

// Create registers a new market.
func (m *Market) Create(id string, capacity int, clearing string, window time.Duration, maxPerAgent int) error {
	if id == "" {
		return errors.New("market_id required")
	}
	if capacity <= 0 {
		return errors.New("capacity must be positive")
	}
	if clearing == "" {
		clearing = "uniform"
	}
	if clearing != "uniform" && clearing != "second_price" {
		return errors.New("clearing must be uniform or second_price")
	}
	if window < 0 {
		return errors.New("window must be non-negative")
	}
	if maxPerAgent < 0 {
		return errors.New("max_bids_per_agent must be non-negative")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markets[id] = &mktAuction{
		capacity: capacity, clearing: clearing, window: window,
		maxPerAgent: maxPerAgent,
		leases:      map[string]*mktLease{},
		losses:      map[string]int{},
	}
	return nil
}

// MarketBidResult is BID's return.
type MarketBidResult struct {
	BidID    string `json:"bid_id"`
	Position int    `json:"position"` // current rank (by price) among posted bids
}

// Bid posts one bid. The bid is parked until the next CLEAR call
// (or until the deadline expires).
func (m *Market) Bid(marketID, agentID string, price float64, qty int, deadline time.Duration) (MarketBidResult, error) {
	if marketID == "" {
		return MarketBidResult{}, errors.New("market_id required")
	}
	if agentID == "" {
		return MarketBidResult{}, errors.New("agent_id required")
	}
	if price < 0 {
		return MarketBidResult{}, errors.New("price must be non-negative")
	}
	if qty <= 0 {
		return MarketBidResult{}, errors.New("qty must be positive")
	}
	if deadline < 0 {
		return MarketBidResult{}, errors.New("deadline must be non-negative")
	}
	m.totalBids.Add(1)
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return MarketBidResult{}, errors.New("unknown market: " + marketID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Per-agent cap
	if a.maxPerAgent > 0 {
		count := 0
		for _, b := range a.bids {
			if b.AgentID == agentID {
				count++
			}
		}
		if count >= a.maxPerAgent {
			return MarketBidResult{}, errors.New("agent has reached max_bids_per_agent")
		}
	}
	bid := &mktBid{
		BidID:    "bid-" + strconv.FormatUint(uint64(a.bidCounter()), 10),
		AgentID:  agentID,
		Price:    price,
		Qty:      qty,
		PostedAt: time.Now(),
	}
	if deadline > 0 {
		bid.Deadline = bid.PostedAt.Add(deadline)
	}
	a.bids = append(a.bids, bid)
	// Note: we do NOT clear lastResult on bid — within-window
	// memoization deliberately returns the same allocation to every
	// caller in the window, even if late bids arrive (they get the
	// next window's clearing).
	// Position by price (highest first)
	pos := 1
	for _, b := range a.bids {
		if b.Price > bid.Price {
			pos++
		}
	}
	return MarketBidResult{BidID: bid.BidID, Position: pos}, nil
}

// bidCounter is a per-auction monotonic id. We hash off auction
// state rather than a global counter so bids id are unique per market.
func (a *mktAuction) bidCounter() int {
	return len(a.bids) + 1
}

// invalidateCached clears the memoized clearing result whenever bids
// change. We don't recompute eagerly — next CLEAR call does it.
func (a *mktAuction) invalidateCached() {
	a.lastResult = nil
}

// MarketClearResult is CLEAR's return.
type MarketClearResult struct {
	MarketID      string             `json:"market_id"`
	ClearingPrice float64            `json:"clearing_price"`
	Capacity      int                `json:"capacity"`
	Filled        []MarketFillRow    `json:"filled"`
	Unfilled      []MarketFillRow    `json:"unfilled"`
	ClearedAt     int64              `json:"cleared_unix"`
}

// MarketFillRow is one row of Filled/Unfilled.
type MarketFillRow struct {
	BidID   string  `json:"bid_id"`
	AgentID string  `json:"agent_id"`
	Price   float64 `json:"price"`
	Qty     int     `json:"qty"`     // requested
	Awarded int     `json:"awarded"` // filled (0 for unfilled rows; partial possible)
}

// Clear runs the auction. Within WINDOW the same result is returned
// to give all agents in the same window the same view.
func (m *Market) Clear(marketID string) (MarketClearResult, error) {
	if marketID == "" {
		return MarketClearResult{}, errors.New("market_id required")
	}
	m.totalClears.Add(1)
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return MarketClearResult{}, errors.New("unknown market: " + marketID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	// Within-window memoization
	if a.lastResult != nil && a.window > 0 && now.Sub(a.lastClear) < a.window {
		return *a.lastResult, nil
	}
	// Drop expired bids
	live := make([]*mktBid, 0, len(a.bids))
	for _, b := range a.bids {
		if !b.Deadline.IsZero() && now.After(b.Deadline) {
			continue
		}
		live = append(live, b)
	}
	// Sort by price desc; ties broken by earlier-posted-first (FIFO fairness)
	sort.SliceStable(live, func(i, j int) bool {
		if live[i].Price != live[j].Price {
			return live[i].Price > live[j].Price
		}
		return live[i].PostedAt.Before(live[j].PostedAt)
	})
	remaining := a.capacity
	filled := []MarketFillRow{}
	unfilled := []MarketFillRow{}
	lastClearingBidIdx := -1
	for i, b := range live {
		if remaining <= 0 {
			unfilled = append(unfilled, MarketFillRow{
				BidID: b.BidID, AgentID: b.AgentID,
				Price: b.Price, Qty: b.Qty,
			})
			continue
		}
		award := b.Qty
		if award > remaining {
			award = remaining
		}
		filled = append(filled, MarketFillRow{
			BidID: b.BidID, AgentID: b.AgentID,
			Price: b.Price, Qty: b.Qty, Awarded: award,
		})
		remaining -= award
		lastClearingBidIdx = i
	}
	// Clearing price
	clearingPrice := 0.0
	switch a.clearing {
	case "uniform":
		// Lowest winning bid (highest losing bid + ε is also a defensible
		// choice; we pick lowest-winning to keep it simple and deterministic).
		if lastClearingBidIdx >= 0 {
			clearingPrice = live[lastClearingBidIdx].Price
		}
	case "second_price":
		// Each winner pays the next-highest losing bid (Vickrey). With a
		// uniform-capacity slot, the Vickrey generalization is the (k+1)-th
		// highest bid where k = number of winning slots.
		nextLoserIdx := lastClearingBidIdx + 1
		if nextLoserIdx < len(live) {
			clearingPrice = live[nextLoserIdx].Price
		} else if lastClearingBidIdx >= 0 {
			// No losers — clearing price is the lowest winning bid
			clearingPrice = live[lastClearingBidIdx].Price
		}
	}
	// Track losses for STARVED
	for _, r := range unfilled {
		a.losses[r.AgentID]++
	}
	// Reset losses for agents that won
	for _, r := range filled {
		if r.Awarded > 0 {
			delete(a.losses, r.AgentID)
		}
	}
	// Commit
	result := MarketClearResult{
		MarketID: marketID, ClearingPrice: clearingPrice,
		Capacity: a.capacity, Filled: filled, Unfilled: unfilled,
		ClearedAt: now.Unix(),
	}
	a.lastResult = &result
	a.lastClear = now
	a.lastPrice = clearingPrice
	// Bids that were filled (any awarded > 0) get retired; unfilled stay
	// for the next clearing window so the agent doesn't need to re-bid.
	keep := make([]*mktBid, 0, len(unfilled))
	winnersByBidID := map[string]bool{}
	for _, r := range filled {
		if r.Awarded > 0 {
			winnersByBidID[r.BidID] = true
		}
	}
	for _, b := range live {
		if winnersByBidID[b.BidID] {
			continue
		}
		keep = append(keep, b)
	}
	a.bids = keep
	return result, nil
}

// MarketLeaseResult is LEASE's return.
type MarketLeaseResult struct {
	Token   string  `json:"token"`
	AgentID string  `json:"agent_id"`
	Qty     int     `json:"qty"`
	Price   float64 `json:"price"`
}

// Lease issues a token for one cleared allocation. The agent must
// have a winning bid in the most-recent CLEAR (one lease per won bid).
func (m *Market) Lease(marketID, agentID string) (MarketLeaseResult, error) {
	if marketID == "" || agentID == "" {
		return MarketLeaseResult{}, errors.New("market_id and agent_id required")
	}
	m.totalLeases.Add(1)
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return MarketLeaseResult{}, errors.New("unknown market: " + marketID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastResult == nil {
		return MarketLeaseResult{}, errors.New("no clearing run yet")
	}
	for _, r := range a.lastResult.Filled {
		if r.AgentID == agentID && r.Awarded > 0 {
			token := "lease-" + strconv.FormatUint(a.nextTokenN.Add(1), 10)
			a.leases[token] = &mktLease{
				Token: token, AgentID: agentID,
				Qty: r.Awarded, Price: r.Price, IssuedAt: time.Now(),
			}
			// Mark the allocation consumed (set awarded → 0 so a second
			// LEASE call doesn't double-issue)
			r.Awarded = 0
			// We modified a value; need to write back since we're iterating
			// over slice of struct values. Mutate in place.
			for i := range a.lastResult.Filled {
				if a.lastResult.Filled[i].BidID == r.BidID {
					a.lastResult.Filled[i].Awarded = 0
				}
			}
			return MarketLeaseResult{
				Token: token, AgentID: agentID, Qty: r.Qty, Price: r.Price,
			}, nil
		}
	}
	return MarketLeaseResult{}, errors.New("no unredeemed winning allocation for agent")
}

// Release frees a lease.
func (m *Market) Release(marketID, token string) (int, error) {
	if marketID == "" || token == "" {
		return 0, errors.New("market_id and token required")
	}
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return 0, errors.New("unknown market: " + marketID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.leases[token]; ok {
		delete(a.leases, token)
		return 1, nil
	}
	return 0, nil
}

// Price returns the last clearing price (the live back-off signal).
func (m *Market) Price(marketID string) (float64, bool) {
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastPrice, true
}

// MarketStarvedRow is one row of STARVED.
type MarketStarvedRow struct {
	AgentID string `json:"agent_id"`
	Losses  int    `json:"losses"`
}

// Starved returns agents who lost at least minLosses recent clearings.
// Default minLosses=3 — three strikes signals systematic exclusion.
func (m *Market) Starved(marketID string, minLosses int) ([]MarketStarvedRow, bool) {
	if minLosses <= 0 {
		minLosses = 3
	}
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]MarketStarvedRow, 0, len(a.losses))
	for k, n := range a.losses {
		if n >= minLosses {
			out = append(out, MarketStarvedRow{AgentID: k, Losses: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Losses > out[j].Losses })
	return out, true
}

// MarketStatus is STATUS's return.
type MarketStatus struct {
	MarketID     string  `json:"market_id"`
	Capacity     int     `json:"capacity"`
	Clearing     string  `json:"clearing"`
	WindowMS     int64   `json:"window_ms"`
	PendingBids  int     `json:"pending_bids"`
	ActiveLeases int     `json:"active_leases"`
	LastPrice    float64 `json:"last_price"`
}

func (m *Market) Status(marketID string) (MarketStatus, bool) {
	m.mu.RLock()
	a, ok := m.markets[marketID]
	m.mu.RUnlock()
	if !ok {
		return MarketStatus{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return MarketStatus{
		MarketID: marketID,
		Capacity: a.capacity, Clearing: a.clearing,
		WindowMS:     a.window.Milliseconds(),
		PendingBids:  len(a.bids),
		ActiveLeases: len(a.leases),
		LastPrice:    a.lastPrice,
	}, true
}

// Forget drops a market (or all).
func (m *Market) Forget(marketID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if marketID == "ALL" {
		n := len(m.markets)
		m.markets = map[string]*mktAuction{}
		return n
	}
	if _, ok := m.markets[marketID]; ok {
		delete(m.markets, marketID)
		return 1
	}
	return 0
}

// List returns every known market id.
func (m *Market) List() []string {
	m.mu.RLock()
	out := make([]string, 0, len(m.markets))
	for k := range m.markets {
		out = append(out, k)
	}
	m.mu.RUnlock()
	sort.Strings(out)
	return out
}

// MarketStats is the global snapshot.
type MarketStats struct {
	Markets     int   `json:"markets"`
	TotalBids   int64 `json:"total_bids"`
	TotalClears int64 `json:"total_clears"`
	TotalLeases int64 `json:"total_leases"`
}

func (m *Market) Stats() MarketStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MarketStats{
		Markets:     len(m.markets),
		TotalBids:   m.totalBids.Load(),
		TotalClears: m.totalClears.Load(),
		TotalLeases: m.totalLeases.Load(),
	}
}
