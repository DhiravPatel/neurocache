package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// NodeRole distinguishes masters (slot owners) from replicas.
type NodeRole int

const (
	RoleMaster NodeRole = iota
	RoleReplica
)

func (r NodeRole) String() string {
	if r == RoleReplica {
		return "slave"
	}
	return "master"
}

// NodeFlag is a bit field tracking transient state that other nodes
// learn about via gossip — failure detection, leadership claims, etc.
type NodeFlag uint32

const (
	FlagNone     NodeFlag = 0
	FlagMyself   NodeFlag = 1 << 0
	FlagMaster   NodeFlag = 1 << 1
	FlagReplica  NodeFlag = 1 << 2
	FlagPFail    NodeFlag = 1 << 3 // possibly failing (this node's view)
	FlagFail     NodeFlag = 1 << 4 // declared failed by quorum
	FlagHandshake NodeFlag = 1 << 5
	FlagNoAddr   NodeFlag = 1 << 6
)

// FlagsString renders the canonical CLUSTER NODES flag list.
func (f NodeFlag) String() string {
	var parts []string
	if f&FlagMyself != 0 {
		parts = append(parts, "myself")
	}
	if f&FlagMaster != 0 {
		parts = append(parts, "master")
	}
	if f&FlagReplica != 0 {
		parts = append(parts, "slave")
	}
	if f&FlagPFail != 0 {
		parts = append(parts, "fail?")
	}
	if f&FlagFail != 0 {
		parts = append(parts, "fail")
	}
	if f&FlagHandshake != 0 {
		parts = append(parts, "handshake")
	}
	if f&FlagNoAddr != 0 {
		parts = append(parts, "noaddr")
	}
	if len(parts) == 0 {
		return "noflags"
	}
	return strings.Join(parts, ",")
}

// Node is one peer in the cluster. Each node has a stable 40-hex ID
// (carried over restarts via the cluster-state file or env), a public
// host:port pair, a "cluster bus" port for gossip, plus a slot bitmap
// describing which slots it currently owns.
type Node struct {
	ID         string
	Host       string // dataplane host (the RESP listener)
	Port       string // dataplane port
	BusPort    string // cluster bus port (gossip)
	Role       NodeRole
	MasterID   string // empty for masters; the owner ID for replicas
	ConfigEpoch int64

	flagsMu sync.RWMutex
	flags   NodeFlag

	slotsMu sync.RWMutex
	slots   [SlotCount / 8]byte // bitmap

	lastPing atomic.Int64 // unix-nano
	lastPong atomic.Int64
}

// NewNode allocates a node with the given metadata. id == "" mints one.
func NewNode(id, host, port, busPort string, role NodeRole) *Node {
	if id == "" {
		id = randomNodeID()
	}
	n := &Node{ID: id, Host: host, Port: port, BusPort: busPort, Role: role}
	if role == RoleMaster {
		n.SetFlag(FlagMaster)
	} else {
		n.SetFlag(FlagReplica)
	}
	return n
}

// Addr returns the dataplane host:port (used by MOVED replies).
func (n *Node) Addr() string { return n.Host + ":" + n.Port }

// BusAddr returns host:bus-port for gossip dialing.
func (n *Node) BusAddr() string { return n.Host + ":" + n.BusPort }

// HasSlot tests slot ownership. Lock-light: bitmap reads under RLock.
func (n *Node) HasSlot(slot int) bool {
	if slot < 0 || slot >= SlotCount {
		return false
	}
	n.slotsMu.RLock()
	defer n.slotsMu.RUnlock()
	return n.slots[slot/8]&(1<<(uint(slot)%8)) != 0
}

// AddSlot / DelSlot update the bitmap. Returns whether the bit changed.
func (n *Node) AddSlot(slot int) bool {
	if slot < 0 || slot >= SlotCount {
		return false
	}
	n.slotsMu.Lock()
	defer n.slotsMu.Unlock()
	mask := byte(1) << (uint(slot) % 8)
	if n.slots[slot/8]&mask != 0 {
		return false
	}
	n.slots[slot/8] |= mask
	return true
}

func (n *Node) DelSlot(slot int) bool {
	if slot < 0 || slot >= SlotCount {
		return false
	}
	n.slotsMu.Lock()
	defer n.slotsMu.Unlock()
	mask := byte(1) << (uint(slot) % 8)
	if n.slots[slot/8]&mask == 0 {
		return false
	}
	n.slots[slot/8] &^= mask
	return true
}

// SlotRanges collapses the bitmap into [start,end] runs — the format
// CLUSTER NODES emits.
func (n *Node) SlotRanges() [][2]int {
	n.slotsMu.RLock()
	defer n.slotsMu.RUnlock()
	var out [][2]int
	start := -1
	for i := 0; i < SlotCount; i++ {
		bit := n.slots[i/8]&(1<<(uint(i)%8)) != 0
		if bit && start < 0 {
			start = i
		}
		if !bit && start >= 0 {
			out = append(out, [2]int{start, i - 1})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, SlotCount - 1})
	}
	return out
}

// SlotCount counts bits set in the bitmap.
func (n *Node) NumSlots() int {
	n.slotsMu.RLock()
	defer n.slotsMu.RUnlock()
	c := 0
	for _, b := range n.slots {
		for b != 0 {
			c++
			b &= b - 1
		}
	}
	return c
}

// SetFlag / ClearFlag / HasFlag are lock-protected since gossip and
// the data path both touch them.
func (n *Node) SetFlag(f NodeFlag) {
	n.flagsMu.Lock()
	n.flags |= f
	n.flagsMu.Unlock()
}

func (n *Node) ClearFlag(f NodeFlag) {
	n.flagsMu.Lock()
	n.flags &^= f
	n.flagsMu.Unlock()
}

func (n *Node) HasFlag(f NodeFlag) bool {
	n.flagsMu.RLock()
	defer n.flagsMu.RUnlock()
	return n.flags&f != 0
}

func (n *Node) Flags() NodeFlag {
	n.flagsMu.RLock()
	defer n.flagsMu.RUnlock()
	return n.flags
}

// Touch records a successful gossip exchange.
func (n *Node) Touch(now time.Time) { n.lastPong.Store(now.UnixNano()) }

// IdleSince reports how long since the last pong, in seconds.
func (n *Node) IdleSince(now time.Time) time.Duration {
	last := n.lastPong.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

func randomNodeID() string {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		copy(buf, []byte("neurocache-fallback0"))
	}
	return hex.EncodeToString(buf)
}
