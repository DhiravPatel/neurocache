package cluster

import (
	"errors"
	"fmt"
	"strings"
)

// Redirect is the data-plane verdict for one command. Three outcomes:
//
//	OK         — local node owns the slot; serve it.
//	MOVED      — another node owns it; client should reissue against `Addr`.
//	ASK        — slot is migrating; client should reissue with the
//	             ASKING flag against `Addr` (transient redirect).
//	CROSSSLOT  — multi-key command touches more than one slot.
//	TRYAGAIN   — slot is being moved and the key isn't here yet.
type Redirect int

const (
	RedirectOK Redirect = iota
	RedirectMoved
	RedirectAsk
	RedirectCrossSlot
	RedirectTryAgain
	RedirectClusterDown
)

// Verdict is the full result of a routing decision.
type Verdict struct {
	Redirect Redirect
	Slot     int
	Addr     string // "host:port" — populated for MOVED/ASK
	Reason   string
}

// Error formats the verdict as the canonical Redis cluster error
// message clients expect.
func (v Verdict) Error() string {
	switch v.Redirect {
	case RedirectMoved:
		return fmt.Sprintf("MOVED %d %s", v.Slot, v.Addr)
	case RedirectAsk:
		return fmt.Sprintf("ASK %d %s", v.Slot, v.Addr)
	case RedirectCrossSlot:
		return "CROSSSLOT Keys in request don't hash to the same slot"
	case RedirectTryAgain:
		return "TRYAGAIN Multiple keys request during rehashing of slot"
	case RedirectClusterDown:
		return "CLUSTERDOWN Hash slot not served"
	}
	return ""
}

// Route inspects the keys touched by one command and returns the
// verdict. asking == true means the caller already issued ASKING in
// the previous frame, so they may bypass an inbound IMPORTING block.
func (s *State) Route(keys []string, asking bool) Verdict {
	if !s.enabled.Load() {
		return Verdict{Redirect: RedirectOK}
	}
	if len(keys) == 0 {
		// Commands with no key args are always local (PING, INFO, etc.).
		return Verdict{Redirect: RedirectOK}
	}
	slot := KeySlot(keys[0])
	for _, k := range keys[1:] {
		if KeySlot(k) != slot {
			return Verdict{Redirect: RedirectCrossSlot}
		}
	}
	info := s.SlotInfo(slot)
	myself := s.Myself()
	if info.OwnerID == "" {
		return Verdict{Redirect: RedirectClusterDown, Slot: slot}
	}
	if myself == nil || info.OwnerID != myself.ID {
		// We don't own this slot; redirect to the owner.
		owner := s.SlotOwner(slot)
		if owner == nil {
			return Verdict{Redirect: RedirectClusterDown, Slot: slot}
		}
		return Verdict{Redirect: RedirectMoved, Slot: slot, Addr: owner.Addr()}
	}
	// We own it — but check migration state.
	switch info.State {
	case SlotMigrating:
		// We're sending the slot away. If the key isn't here, redirect
		// to the new owner with ASK.
		if info.PeerID != "" {
			peer := s.Node(info.PeerID)
			if peer != nil {
				return Verdict{Redirect: RedirectAsk, Slot: slot, Addr: peer.Addr()}
			}
		}
	case SlotImporting:
		// We're receiving the slot. The client must have sent ASKING in
		// the previous command, otherwise tell them to go to the source.
		if !asking {
			peer := s.Node(info.PeerID)
			if peer != nil {
				return Verdict{Redirect: RedirectMoved, Slot: slot, Addr: peer.Addr()}
			}
		}
	}
	return Verdict{Redirect: RedirectOK, Slot: slot}
}

// MultiKeyVerdict is a convenience for commands that explicitly take
// multiple keys: it ensures every key hashes to the same slot.
func (s *State) MultiKeyVerdict(keys []string) error {
	if !s.enabled.Load() || len(keys) <= 1 {
		return nil
	}
	first := KeySlot(keys[0])
	for _, k := range keys[1:] {
		if KeySlot(k) != first {
			return errors.New("CROSSSLOT Keys in request don't hash to the same slot")
		}
	}
	return nil
}

// IsOurs is the local-only check used by cluster admin commands.
func (s *State) IsOurs(slot int) bool {
	myself := s.Myself()
	if myself == nil {
		return false
	}
	info := s.SlotInfo(slot)
	return info.OwnerID == myself.ID
}

// FormatNodesLine renders one CLUSTER NODES row in the canonical
// Redis format. Keeping the formatting in the cluster package makes it
// easy to keep flag-strings and slot-range layout consistent.
func FormatNodesLine(n *Node, myselfID string) string {
	flags := n.Flags()
	if n.ID == myselfID {
		flags |= FlagMyself
	}
	var sb strings.Builder
	sb.WriteString(n.ID)
	sb.WriteByte(' ')
	if n.Host != "" {
		sb.WriteString(n.Host + ":" + n.Port + "@" + n.BusPort)
	} else {
		sb.WriteString(":0@0")
	}
	sb.WriteByte(' ')
	sb.WriteString(flags.String())
	sb.WriteByte(' ')
	if n.Role == RoleReplica && n.MasterID != "" {
		sb.WriteString(n.MasterID)
	} else {
		sb.WriteByte('-')
	}
	// ping-sent / pong-recv / config-epoch / link-state are simplified
	sb.WriteString(" 0 0 ")
	fmt.Fprintf(&sb, "%d connected", n.ConfigEpoch)
	for _, r := range n.SlotRanges() {
		if r[0] == r[1] {
			fmt.Fprintf(&sb, " %d", r[0])
		} else {
			fmt.Fprintf(&sb, " %d-%d", r[0], r[1])
		}
	}
	return sb.String()
}
