// Package replication implements Redis-style async replication:
//
//   - every write on the master appends to a ring-buffer "backlog"
//     indexed by a monotonically-increasing byte offset;
//   - connected replicas run a PSYNC handshake to either full-resync
//     (master sends an RDB snapshot, then streams from the tail) or
//     partial-resync (master replays from the replica's last-acked
//     offset if it's still in the backlog);
//   - replicas periodically REPLCONF ACK their offset so the master
//     can serve WAIT numreplicas and promote during FAILOVER.
//
// The package is intentionally transport-agnostic: it speaks raw RESP
// frames over io.Writer/io.Reader, so the existing resp.Server can
// accept replica connections on the same :6379 port — no extra ports
// or ABI decisions required.
package replication

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
)

// Role enumerates the current replication role.
type Role int

const (
	RoleMaster  Role = iota // default: we accept writes + propagate
	RoleReplica             // we dial a master + apply its stream
)

func (r Role) String() string {
	if r == RoleReplica {
		return "slave"
	}
	return "master"
}

// State is the shared replication state owned by the engine. Read by
// every command handler, mutated by REPLICAOF / PSYNC / REPLCONF and
// by the replica client goroutine.
type State struct {
	mu sync.RWMutex

	role        Role
	replID      string       // our current replication ID (40 hex chars)
	replIDPrev  string       // previous replid — allows partial resync after role flip
	offset      atomic.Int64 // bytes propagated since boot (master) or applied (replica)

	// master-side: connected replicas
	replicas map[*ReplicaLink]struct{}

	// replica-side: master we follow
	masterHost string
	masterPort string
	linkState  LinkState
	masterOff  atomic.Int64 // last offset successfully applied

	// replica-mode gate: when true the engine must NOT re-append
	// incoming writes to its own AOF or backlog, otherwise we loop.
	replicaMode atomic.Bool
}

// LinkState is what ROLE reports for the master link on a replica.
type LinkState int

const (
	LinkDown LinkState = iota
	LinkConnecting
	LinkHandshake
	LinkSyncing
	LinkConnected
)

func (l LinkState) String() string {
	switch l {
	case LinkConnecting:
		return "connecting"
	case LinkHandshake:
		return "handshake"
	case LinkSyncing:
		return "sync"
	case LinkConnected:
		return "connected"
	default:
		return "down"
	}
}

// NewState returns a master-mode state with a fresh random replid.
func NewState() *State {
	return &State{
		role:     RoleMaster,
		replID:   randomReplID(),
		replicas: map[*ReplicaLink]struct{}{},
	}
}

// Role returns the current role.
func (s *State) Role() Role {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.role
}

// IsReplica is a fast atomic check callers use on the write hot path.
func (s *State) IsReplica() bool { return s.replicaMode.Load() }

// ReplID / Offset — snapshot of the master replication coordinates.
func (s *State) ReplID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.replID
}

func (s *State) PrevReplID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.replIDPrev
}

// Offset returns the master's current byte offset — the sum of every
// command propagated so far. Replicas should AC this value back.
func (s *State) Offset() int64 { return s.offset.Load() }

// AdvanceOffset bumps the master offset by n bytes. Called by the
// backlog write-through helper.
func (s *State) AdvanceOffset(n int64) { s.offset.Add(n) }

// MasterOffset returns the last offset a replica has applied.
func (s *State) MasterOffset() int64 { return s.masterOff.Load() }

// SetMasterOffset is used by replica-side code after each applied frame.
func (s *State) SetMasterOffset(v int64) { s.masterOff.Store(v) }

// SetRoleMaster flips this node into master mode. Called by REPLICAOF
// NO ONE and FAILOVER (on the promoted replica). It mints a new replid
// so former siblings know they're out of sync.
func (s *State) SetRoleMaster() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.role = RoleMaster
	s.replIDPrev = s.replID
	s.replID = randomReplID()
	s.masterHost = ""
	s.masterPort = ""
	s.linkState = LinkDown
	s.replicaMode.Store(false)
}

// SetRoleReplica flips into replica mode, following host:port. The
// caller is responsible for actually dialing the master (the replica
// client does that, this only updates shared state).
func (s *State) SetRoleReplica(host, port string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.role = RoleReplica
	s.masterHost = host
	s.masterPort = port
	s.linkState = LinkConnecting
	s.replicaMode.Store(true)
}

// Master returns the host:port the replica follows (or "","" on a master).
func (s *State) Master() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.masterHost, s.masterPort
}

// LinkState returns the current master-link state (replica-side).
func (s *State) LinkState() LinkState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.linkState
}

// SetLinkState mutates the link state atomically.
func (s *State) SetLinkState(ls LinkState) {
	s.mu.Lock()
	s.linkState = ls
	s.mu.Unlock()
}

// BumpReplID rotates the replication id (and slides the previous
// id into the secondary slot) so any reconnecting replica's PSYNC
// offset becomes stale and forces a full resync. Called by
// `DEBUG CHANGE-REPL-ID` — the same primitive operators reach for
// after a botched FAILOVER when the in-flight master state is
// suspect.
func (s *State) BumpReplID() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replIDPrev = s.replID
	s.replID = randomReplID()
}

// SetReplID overrides the replid (replica-side: sync'd with the master
// after FULLRESYNC, or on SetRoleMaster during promotion).
func (s *State) SetReplID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replIDPrev = s.replID
	s.replID = id
}

// AddReplica / RemoveReplica manage the master-side registry.
func (s *State) AddReplica(r *ReplicaLink) {
	s.mu.Lock()
	s.replicas[r] = struct{}{}
	s.mu.Unlock()
}

func (s *State) RemoveReplica(r *ReplicaLink) {
	s.mu.Lock()
	delete(s.replicas, r)
	s.mu.Unlock()
}

// Replicas returns a snapshot of the current replica list (safe to
// iterate without holding the lock).
func (s *State) Replicas() []*ReplicaLink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ReplicaLink, 0, len(s.replicas))
	for r := range s.replicas {
		out = append(out, r)
	}
	return out
}

// ConnectedReplicasAtOffset counts replicas whose acknowledged offset
// is >= target. WAIT uses this to decide when to release a blocked
// client.
func (s *State) ConnectedReplicasAtOffset(target int64) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for r := range s.replicas {
		if r.AckOffset.Load() >= target {
			n++
		}
	}
	return n
}

// randomReplID mints a 40-char lowercase-hex string — same shape Redis
// uses for its `replid`.
func randomReplID() string {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failures are kernel-level; we keep a deterministic
		// fallback so startup never blocks on entropy starvation.
		copy(buf, []byte("neurocache-fallback0"))
	}
	return hex.EncodeToString(buf)
}
