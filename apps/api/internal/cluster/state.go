package cluster

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SlotState tracks the migration status of one slot. STABLE means the
// owner serves it normally; MIGRATING means we're sending it to
// `target`; IMPORTING means we're receiving from `source`.
type SlotState int

const (
	SlotStable SlotState = iota
	SlotMigrating
	SlotImporting
)

// SlotInfo is per-slot metadata kept on every node.
type SlotInfo struct {
	OwnerID string    // node ID that authoritatively serves the slot
	State   SlotState
	PeerID  string    // target/source node during migration
}

// State is the per-process cluster registry. Read on every command
// (fast path = atomic.Load on the slot map), mutated by gossip and
// CLUSTER admin commands.
type State struct {
	enabled atomic.Bool

	mu     sync.RWMutex
	myself *Node
	nodes  map[string]*Node // by ID

	// slots is an immutable copy-on-write snapshot. Reads are lock-free
	// — readers grab the pointer, walk it, drop it. Writers build a new
	// array, swap it under mu.
	slots atomic.Pointer[[SlotCount]SlotInfo]

	currentEpoch atomic.Int64
}

// NewState returns a disabled cluster registry. Call Enable to flip
// cluster mode on after the engine has bootstrapped.
func NewState() *State {
	s := &State{nodes: map[string]*Node{}}
	empty := [SlotCount]SlotInfo{}
	s.slots.Store(&empty)
	return s
}

// Enable boots the cluster state with myself as the only known node.
// Idempotent — calling twice keeps myself unchanged.
func (s *State) Enable(myself *Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.myself != nil {
		return
	}
	s.myself = myself
	myself.SetFlag(FlagMyself)
	s.nodes[myself.ID] = myself
	s.enabled.Store(true)
}

// Enabled returns whether cluster mode has been turned on.
func (s *State) Enabled() bool { return s.enabled.Load() }

// Myself is the local node (nil if cluster mode is off).
func (s *State) Myself() *Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.myself
}

// Nodes returns every known node, sorted by ID for stable iteration.
func (s *State) Nodes() []*Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	sortByID(out)
	return out
}

// Node fetches by ID.
func (s *State) Node(id string) *Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[id]
}

// AddNode registers a freshly-discovered peer (gossip or CLUSTER MEET).
// Returns the canonical node — if we already knew about this ID, the
// existing pointer is returned and the caller's copy can be discarded.
func (s *State) AddNode(n *Node) *Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.nodes[n.ID]; ok {
		// Update transient fields if the gossip carried new info.
		if n.Host != "" {
			existing.Host = n.Host
		}
		if n.Port != "" {
			existing.Port = n.Port
		}
		if n.BusPort != "" {
			existing.BusPort = n.BusPort
		}
		return existing
	}
	s.nodes[n.ID] = n
	return n
}

// ForgetNode removes a peer entirely (CLUSTER FORGET). The local node
// can never forget itself.
func (s *State) ForgetNode(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.myself != nil && s.myself.ID == id {
		return false
	}
	if _, ok := s.nodes[id]; !ok {
		return false
	}
	delete(s.nodes, id)
	// Strip slot ownership if this node was the owner.
	cur := s.slots.Load()
	next := *cur
	for i := 0; i < SlotCount; i++ {
		if next[i].OwnerID == id {
			next[i] = SlotInfo{}
		}
	}
	s.slots.Store(&next)
	return true
}

// AssignSlot sets the owner of one slot. Used by ADDSLOTS and SETSLOT
// NODE. Returns the previous owner ID.
func (s *State) AssignSlot(slot int, ownerID string) (string, error) {
	if slot < 0 || slot >= SlotCount {
		return "", errors.New("slot out of range")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.nodes[ownerID]
	if !ok {
		return "", fmt.Errorf("unknown node %s", ownerID)
	}
	cur := s.slots.Load()
	next := *cur
	prev := next[slot].OwnerID
	next[slot] = SlotInfo{OwnerID: ownerID, State: SlotStable}
	s.slots.Store(&next)
	if prev != ownerID {
		if old, ok := s.nodes[prev]; ok {
			old.DelSlot(slot)
		}
		owner.AddSlot(slot)
	}
	return prev, nil
}

// UnassignSlot drops ownership of a slot (DELSLOTS).
func (s *State) UnassignSlot(slot int) error {
	if slot < 0 || slot >= SlotCount {
		return errors.New("slot out of range")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.slots.Load()
	next := *cur
	prev := next[slot].OwnerID
	next[slot] = SlotInfo{}
	s.slots.Store(&next)
	if prev != "" {
		if old, ok := s.nodes[prev]; ok {
			old.DelSlot(slot)
		}
	}
	return nil
}

// SetSlotState transitions a slot into MIGRATING or IMPORTING. The
// counterparty node ID is needed so the data plane can answer with
// ASK redirections during the move.
func (s *State) SetSlotState(slot int, state SlotState, peerID string) error {
	if slot < 0 || slot >= SlotCount {
		return errors.New("slot out of range")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.slots.Load()
	next := *cur
	next[slot].State = state
	next[slot].PeerID = peerID
	s.slots.Store(&next)
	return nil
}

// SlotInfo returns the current view of one slot. Lock-free fast path.
func (s *State) SlotInfo(slot int) SlotInfo {
	if slot < 0 || slot >= SlotCount {
		return SlotInfo{}
	}
	return s.slots.Load()[slot]
}

// SlotOwner returns the node currently authoritative for the slot.
// Returns nil when nobody owns it (not all 16384 covered yet).
func (s *State) SlotOwner(slot int) *Node {
	info := s.SlotInfo(slot)
	if info.OwnerID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[info.OwnerID]
}

// CountKeysInSlot needs help from the data layer; we expose a hook
// the engine wires up.
type KeyCounter func(slot int) int

var keyCounter atomic.Pointer[KeyCounter]

// SetKeyCounter installs the engine's per-slot counter.
func (s *State) SetKeyCounter(fn KeyCounter) { keyCounter.Store(&fn) }

func (s *State) CountKeysInSlot(slot int) int {
	if fn := keyCounter.Load(); fn != nil {
		return (*fn)(slot)
	}
	return 0
}

// CurrentEpoch is monotonic, incremented on every authoritative slot
// reassignment. Used by the gossip layer for last-write-wins.
func (s *State) CurrentEpoch() int64 { return s.currentEpoch.Load() }

// BumpEpoch atomically increments and returns the new value.
func (s *State) BumpEpoch() int64 { return s.currentEpoch.Add(1) }

// Stats is what CLUSTER INFO renders. Cheap to compute.
type Stats struct {
	Enabled        bool
	State          string // "ok" | "fail"
	SlotsAssigned  int
	SlotsOK        int
	SlotsPFail     int
	SlotsFail      int
	KnownNodes     int
	Size           int // master count
	CurrentEpoch   int64
	MyEpoch        int64
}

// Stats snapshots for CLUSTER INFO.
func (s *State) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{
		Enabled: s.enabled.Load(), KnownNodes: len(s.nodes),
		CurrentEpoch: s.currentEpoch.Load(),
	}
	if s.myself != nil {
		st.MyEpoch = s.myself.ConfigEpoch
	}
	cur := s.slots.Load()
	for i := 0; i < SlotCount; i++ {
		if cur[i].OwnerID != "" {
			st.SlotsAssigned++
			st.SlotsOK++
		}
	}
	for _, n := range s.nodes {
		if n.Role == RoleMaster {
			st.Size++
		}
	}
	if st.SlotsAssigned == SlotCount {
		st.State = "ok"
	} else {
		st.State = "fail"
	}
	return st
}

// CountReachableMasters is what determines whether the cluster is
// healthy enough to accept writes (called from health probes).
func (s *State) CountReachableMasters(now time.Time, timeout time.Duration) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, node := range s.nodes {
		if node.Role != RoleMaster {
			continue
		}
		if node.HasFlag(FlagFail) {
			continue
		}
		if node == s.myself {
			n++
			continue
		}
		if node.IdleSince(now) <= timeout {
			n++
		}
	}
	return n
}

// Reset wipes the cluster state (CLUSTER RESET HARD). The local node
// stays but loses its ID + slot ownership.
func (s *State) Reset(hard bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = map[string]*Node{}
	if s.myself != nil {
		if hard {
			s.myself.ID = randomNodeID()
		}
		// keep myself in the registry
		s.nodes[s.myself.ID] = s.myself
		// drop all slot bits
		for i := 0; i < SlotCount; i++ {
			s.myself.DelSlot(i)
		}
	}
	empty := [SlotCount]SlotInfo{}
	s.slots.Store(&empty)
	s.currentEpoch.Store(0)
}

// sortByID is a tiny ID sort so CLUSTER NODES output is stable.
func sortByID(ns []*Node) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && strings.Compare(ns[j-1].ID, ns[j].ID) > 0; j-- {
			ns[j-1], ns[j] = ns[j], ns[j-1]
		}
	}
}
