package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Negotiations is the agent-to-agent bargaining protocol. Distinct
// from MARKET (auction with a clearing price) and DEBATE (multi-
// party deliberation): NEGOTIATE is a bilateral haggle — offer,
// counter, accept, walk-away — with each party's BATNA (Best
// Alternative To a Negotiated Agreement) acting as a reservation
// value below which they should walk.
//
// Why a primitive: multi-agent commerce needs this constantly and
// every system fakes it with prompt glue. Making it structured means:
//
//   - The agents can be different models / vendors / processes —
//     the protocol is the contract.
//   - Walk-away conditions are explicit (BATNA), not buried in
//     prompts.
//   - Settled deals produce a clean (buyer, seller, price, terms)
//     tuple that SETTLE.TXN can consume directly.
//
// Lifecycle:
//
//   OPEN → open ── OFFER ──► offered
//                ◄─ COUNTER ─
//                ── ACCEPT ──► accepted (terminal)
//                ── REJECT ──► rejected (terminal)
//                ── WALK ────► walked-away (terminal)
//
// At any state, either party can WALK, which is final.
//
// Commands:
//
//   NEGOTIATE.OPEN nego-id buyer seller asset [BATNA_BUYER f] [BATNA_SELLER f]
//        [DEADLINE ms] [META k v ...]
//        BATNAs are the reservation values: buyer won't pay above
//        their BATNA; seller won't accept below theirs.
//   NEGOTIATE.OFFER nego-id party price [TERMS "..."]
//   NEGOTIATE.COUNTER nego-id party price [TERMS "..."]
//        Counter is just an offer following a prior offer; we don't
//        distinguish protocol-wise (separate command name for clarity).
//   NEGOTIATE.ACCEPT nego-id party
//        Errors if the current offer would violate the accepter's
//        BATNA — a guard against silly autonomous mistakes.
//   NEGOTIATE.REJECT nego-id party [REASON r]
//   NEGOTIATE.WALK   nego-id party [REASON r]
//   NEGOTIATE.GET nego-id          → full history
//   NEGOTIATE.LIST [STATE s]
//   NEGOTIATE.FORGET nego-id|ALL
//   NEGOTIATE.STATS
//
// Hot path: every mutation is O(1); GET returns the full move
// history (typically dozens of moves).
type Negotiations struct {
	mu      sync.RWMutex
	negos   map[string]*negotiation

	totalOpens     atomic.Int64
	totalOffers    atomic.Int64
	totalAccepts   atomic.Int64
	totalRejects   atomic.Int64
	totalWalks     atomic.Int64
}

type negotiation struct {
	mu          sync.Mutex
	id          string
	buyer       string
	seller      string
	asset       string
	batnaBuyer  *float64 // nil = unset
	batnaSeller *float64
	state       string // open, offered, accepted, rejected, walked-away
	moves       []negoMove
	currentPrice float64
	currentTerms string
	currentParty string // who made the last offer
	createdAt   time.Time
	deadline    time.Time
	resolution  string // final terms summary
	meta        map[string]string
}

type negoMove struct {
	Party  string
	Kind   string // offer, counter, accept, reject, walk
	Price  float64
	Terms  string
	Reason string
	At     time.Time
}

// NewNegotiations returns an empty registry.
func NewNegotiations() *Negotiations {
	return &Negotiations{negos: map[string]*negotiation{}}
}

// NegoOpenOpts is the bag of optional Open parameters.
type NegoOpenOpts struct {
	BatnaBuyer  *float64
	BatnaSeller *float64
	Deadline    time.Duration
	Meta        map[string]string
}

// Open creates a new negotiation.
func (n *Negotiations) Open(id, buyer, seller, asset string, opts NegoOpenOpts) error {
	if id == "" {
		return errors.New("nego_id required")
	}
	if buyer == "" || seller == "" {
		return errors.New("buyer and seller required")
	}
	if buyer == seller {
		return errors.New("buyer == seller; nothing to negotiate")
	}
	if asset == "" {
		return errors.New("asset required")
	}
	if opts.Deadline < 0 {
		return errors.New("deadline must be non-negative")
	}
	n.totalOpens.Add(1)
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.negos[id]; ok {
		return errors.New("negotiation already exists: " + id)
	}
	cp := map[string]string{}
	for k, v := range opts.Meta {
		cp[k] = v
	}
	ng := &negotiation{
		id: id, buyer: buyer, seller: seller, asset: asset,
		batnaBuyer: copyFloat(opts.BatnaBuyer),
		batnaSeller: copyFloat(opts.BatnaSeller),
		state: "open", createdAt: time.Now(), meta: cp,
	}
	if opts.Deadline > 0 {
		ng.deadline = ng.createdAt.Add(opts.Deadline)
	}
	n.negos[id] = ng
	return nil
}

// Offer / Counter post a price (we treat them as the same op).
func (n *Negotiations) Offer(id, party string, price float64, terms string) error {
	return n.offerInternal(id, party, "offer", price, terms)
}
func (n *Negotiations) Counter(id, party string, price float64, terms string) error {
	return n.offerInternal(id, party, "counter", price, terms)
}

func (n *Negotiations) offerInternal(id, party, kind string, price float64, terms string) error {
	if id == "" || party == "" {
		return errors.New("nego_id and party required")
	}
	if price < 0 {
		return errors.New("price must be non-negative")
	}
	n.totalOffers.Add(1)
	n.mu.RLock()
	ng, ok := n.negos[id]
	n.mu.RUnlock()
	if !ok {
		return errors.New("unknown negotiation: " + id)
	}
	ng.mu.Lock()
	defer ng.mu.Unlock()
	n.lazyExpire(ng)
	if !isOpenState(ng.state) {
		return errors.New("negotiation is " + ng.state)
	}
	if party != ng.buyer && party != ng.seller {
		return errors.New("party not in negotiation")
	}
	ng.moves = append(ng.moves, negoMove{
		Party: party, Kind: kind, Price: price, Terms: terms, At: time.Now(),
	})
	ng.state = "offered"
	ng.currentPrice = price
	ng.currentTerms = terms
	ng.currentParty = party
	return nil
}

// Accept closes the deal at the current offer. Guards against
// accepting against your own BATNA — a buyer can't accept a price
// above batna_buyer; a seller can't accept below batna_seller.
func (n *Negotiations) Accept(id, party string) error {
	if id == "" || party == "" {
		return errors.New("nego_id and party required")
	}
	n.totalAccepts.Add(1)
	n.mu.RLock()
	ng, ok := n.negos[id]
	n.mu.RUnlock()
	if !ok {
		return errors.New("unknown negotiation: " + id)
	}
	ng.mu.Lock()
	defer ng.mu.Unlock()
	n.lazyExpire(ng)
	if ng.state != "offered" {
		return errors.New("nothing to accept in state " + ng.state)
	}
	if party != ng.buyer && party != ng.seller {
		return errors.New("party not in negotiation")
	}
	// Can't accept your own offer
	if party == ng.currentParty {
		return errors.New("cannot accept your own offer; the counterparty must accept")
	}
	// BATNA guard
	if party == ng.buyer && ng.batnaBuyer != nil && ng.currentPrice > *ng.batnaBuyer {
		return errors.New("price exceeds buyer BATNA")
	}
	if party == ng.seller && ng.batnaSeller != nil && ng.currentPrice < *ng.batnaSeller {
		return errors.New("price below seller BATNA")
	}
	ng.moves = append(ng.moves, negoMove{
		Party: party, Kind: "accept", Price: ng.currentPrice,
		Terms: ng.currentTerms, At: time.Now(),
	})
	ng.state = "accepted"
	return nil
}

// Reject closes the negotiation without a deal but without "walk-away
// is final" semantics — a separate REJECT keeps the move history
// clear for postmortem.
func (n *Negotiations) Reject(id, party, reason string) error {
	return n.terminal(id, party, "reject", reason, "rejected")
}

// Walk is a unilateral, final exit.
func (n *Negotiations) Walk(id, party, reason string) error {
	return n.terminal(id, party, "walk", reason, "walked-away")
}

func (n *Negotiations) terminal(id, party, kind, reason, newState string) error {
	if id == "" || party == "" {
		return errors.New("nego_id and party required")
	}
	if kind == "reject" {
		n.totalRejects.Add(1)
	} else {
		n.totalWalks.Add(1)
	}
	n.mu.RLock()
	ng, ok := n.negos[id]
	n.mu.RUnlock()
	if !ok {
		return errors.New("unknown negotiation: " + id)
	}
	ng.mu.Lock()
	defer ng.mu.Unlock()
	n.lazyExpire(ng)
	if !isOpenState(ng.state) {
		return errors.New("negotiation is " + ng.state)
	}
	if party != ng.buyer && party != ng.seller {
		return errors.New("party not in negotiation")
	}
	ng.moves = append(ng.moves, negoMove{
		Party: party, Kind: kind, Reason: reason, At: time.Now(),
	})
	ng.state = newState
	return nil
}

// NegoView is GET's return.
type NegoView struct {
	NegoID       string         `json:"nego_id"`
	Buyer        string         `json:"buyer"`
	Seller       string         `json:"seller"`
	Asset        string         `json:"asset"`
	State        string         `json:"state"`
	CurrentPrice float64        `json:"current_price"`
	CurrentParty string         `json:"current_party"`
	BatnaBuyer   *float64       `json:"batna_buyer,omitempty"`
	BatnaSeller  *float64       `json:"batna_seller,omitempty"`
	Moves        []NegoMoveRow  `json:"moves"`
	DeadlineUnix int64          `json:"deadline_unix"`
}

// NegoMoveRow is one row of moves in GET.
type NegoMoveRow struct {
	Party  string  `json:"party"`
	Kind   string  `json:"kind"`
	Price  float64 `json:"price"`
	Terms  string  `json:"terms,omitempty"`
	Reason string  `json:"reason,omitempty"`
	AtUnix int64   `json:"at_unix"`
}

// Get returns the full negotiation transcript.
func (n *Negotiations) Get(id string) (NegoView, bool) {
	n.mu.RLock()
	ng, ok := n.negos[id]
	n.mu.RUnlock()
	if !ok {
		return NegoView{}, false
	}
	ng.mu.Lock()
	defer ng.mu.Unlock()
	n.lazyExpire(ng)
	v := NegoView{
		NegoID: ng.id, Buyer: ng.buyer, Seller: ng.seller,
		Asset: ng.asset, State: ng.state,
		CurrentPrice: ng.currentPrice, CurrentParty: ng.currentParty,
		BatnaBuyer: copyFloat(ng.batnaBuyer),
		BatnaSeller: copyFloat(ng.batnaSeller),
	}
	if !ng.deadline.IsZero() {
		v.DeadlineUnix = ng.deadline.Unix()
	}
	for _, m := range ng.moves {
		v.Moves = append(v.Moves, NegoMoveRow{
			Party: m.Party, Kind: m.Kind, Price: m.Price,
			Terms: m.Terms, Reason: m.Reason, AtUnix: m.At.Unix(),
		})
	}
	return v, true
}

// NegoListRow is one row of LIST.
type NegoListRow struct {
	NegoID  string  `json:"nego_id"`
	State   string  `json:"state"`
	Price   float64 `json:"current_price"`
}

// List returns negotiations (optionally filtered).
func (n *Negotiations) List(state string) []NegoListRow {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]NegoListRow, 0, len(n.negos))
	for _, ng := range n.negos {
		ng.mu.Lock()
		n.lazyExpire(ng)
		if state != "" && ng.state != state {
			ng.mu.Unlock()
			continue
		}
		out = append(out, NegoListRow{
			NegoID: ng.id, State: ng.state, Price: ng.currentPrice,
		})
		ng.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NegoID < out[j].NegoID })
	return out
}

// Forget drops a negotiation (or all).
func (n *Negotiations) Forget(id string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id == "ALL" {
		k := len(n.negos)
		n.negos = map[string]*negotiation{}
		return k
	}
	if _, ok := n.negos[id]; ok {
		delete(n.negos, id)
		return 1
	}
	return 0
}

// NegoStats is the global snapshot.
type NegoStats struct {
	Negotiations int   `json:"negotiations"`
	TotalOpens   int64 `json:"total_opens"`
	TotalOffers  int64 `json:"total_offers"`
	TotalAccepts int64 `json:"total_accepts"`
	TotalRejects int64 `json:"total_rejects"`
	TotalWalks   int64 `json:"total_walks"`
}

func (n *Negotiations) Stats() NegoStats {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return NegoStats{
		Negotiations: len(n.negos),
		TotalOpens:   n.totalOpens.Load(),
		TotalOffers:  n.totalOffers.Load(),
		TotalAccepts: n.totalAccepts.Load(),
		TotalRejects: n.totalRejects.Load(),
		TotalWalks:   n.totalWalks.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func isOpenState(s string) bool {
	return s == "open" || s == "offered"
}

func (n *Negotiations) lazyExpire(ng *negotiation) {
	if !isOpenState(ng.state) {
		return
	}
	if ng.deadline.IsZero() {
		return
	}
	if time.Now().After(ng.deadline) {
		ng.state = "expired"
	}
}

func copyFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
