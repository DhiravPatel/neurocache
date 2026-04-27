package cluster

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// MsgType enumerates the gossip frame variants.
type MsgType string

const (
	MsgPing    MsgType = "PING"
	MsgPong    MsgType = "PONG"
	MsgMeet    MsgType = "MEET"
	MsgFail    MsgType = "FAIL"
	MsgUpdate  MsgType = "UPDATE"  // slot ownership change broadcast
	MsgPublish MsgType = "PUBLISH" // optional cluster pub/sub fan-out
)

// Frame is one gossip message. Encoded as one JSON object per line so
// a single TCP read scans cleanly to a frame boundary.
//
// We deliberately use JSON instead of Redis' binary cluster bus —
// JSON gives us schema evolution for free, the cluster bus is not a
// public protocol so wire compatibility doesn't matter, and the volume
// is low (a few KB/sec across the whole fleet).
type Frame struct {
	Type     MsgType   `json:"t"`
	From     string    `json:"from"`            // sender node ID
	Host     string    `json:"host,omitempty"`  // sender dataplane host
	Port     string    `json:"port,omitempty"`  // sender dataplane port
	BusPort  string    `json:"bus,omitempty"`   // sender bus port
	Epoch    int64     `json:"e"`               // sender's current epoch
	MyEpoch  int64     `json:"me"`              // sender's config epoch
	Slots    [][2]int  `json:"slots,omitempty"` // sender's owned slot ranges
	TargetID string    `json:"tid,omitempty"`   // for FAIL / UPDATE
	Channel  string    `json:"ch,omitempty"`    // PUBLISH only
	Payload  string    `json:"pl,omitempty"`    // PUBLISH only
	Gossip   []GNode   `json:"g,omitempty"`     // piggybacked peer summaries
	At       time.Time `json:"at"`
}

// GNode is the compact peer summary that PINGs sprinkle so new joiners
// learn about the rest of the cluster without a full sync.
type GNode struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	BusPort  string `json:"bus"`
	Flags    uint32 `json:"f"`
	LastSeen int64  `json:"ls"` // unix-nano
}

// Bus owns the cluster bus listener + dialer. One per process.
type Bus struct {
	state *State
	log   *slog.Logger
	addr  string

	mu       sync.Mutex
	lis      net.Listener
	dialedMu sync.Mutex
	dialed   map[string]*peerLink // peer node ID -> active outbound link

	// publishHandler is invoked when a remote node sends MsgPublish via
	// the bus — wired to the engine's pub/sub broker so cluster-wide
	// PUBLISH actually fans out.
	publishHandler func(channel, payload string)

	stop chan struct{}
}

type peerLink struct {
	conn net.Conn
	bw   *bufio.Writer
	mu   sync.Mutex
}

// NewBus builds an idle bus. Call Start to begin listening + gossiping.
func NewBus(state *State, log *slog.Logger, listenAddr string) *Bus {
	if log == nil {
		log = slog.Default()
	}
	return &Bus{
		state: state, log: log, addr: listenAddr,
		dialed: map[string]*peerLink{},
		stop:   make(chan struct{}),
	}
}

// SetPublishHandler installs the engine callback for cluster PUBLISH.
func (b *Bus) SetPublishHandler(fn func(channel, payload string)) {
	b.publishHandler = fn
}

// Start opens the listener and kicks off the gossip ticker.
func (b *Bus) Start() error {
	l, err := net.Listen("tcp", b.addr)
	if err != nil {
		return fmt.Errorf("cluster bus listen: %w", err)
	}
	b.mu.Lock()
	b.lis = l
	b.mu.Unlock()
	go b.acceptLoop()
	go b.gossipLoop()
	go b.failureLoop()
	b.log.Info("cluster bus listening", "addr", b.addr)
	return nil
}

// Stop closes the listener and signals goroutines to exit.
func (b *Bus) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
	if b.lis != nil {
		_ = b.lis.Close()
	}
	b.dialedMu.Lock()
	for _, p := range b.dialed {
		_ = p.conn.Close()
	}
	b.dialed = map[string]*peerLink{}
	b.dialedMu.Unlock()
}

// Meet bootstraps a connection to a previously-unknown node. Used by
// CLUSTER MEET on the receiving side: we open a link, send MEET, the
// peer registers us and sends back a PONG with its node info.
func (b *Bus) Meet(host, busPort string) error {
	addr := net.JoinHostPort(host, busPort)
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	link := &peerLink{conn: conn, bw: bufio.NewWriter(conn)}
	// We don't know the peer's ID yet — store under the addr until the
	// PONG resolves it.
	b.dialedMu.Lock()
	b.dialed["addr:"+addr] = link
	b.dialedMu.Unlock()

	go b.readLoop(conn)
	return b.send(link, b.buildFrame(MsgMeet, ""))
}

// Broadcast sends a frame to every peer we have an open link to.
// Used for FAIL announcements + PUBLISH fan-out.
func (b *Bus) Broadcast(f Frame) {
	b.dialedMu.Lock()
	defer b.dialedMu.Unlock()
	for _, link := range b.dialed {
		_ = b.send(link, f)
	}
}

// PublishToCluster gossips a PUBLISH so subscribers on other nodes
// receive the message. Engine layer calls this from the PUBLISH
// command handler when cluster mode is enabled.
func (b *Bus) PublishToCluster(channel, payload string) {
	f := b.buildFrame(MsgPublish, "")
	f.Channel = channel
	f.Payload = payload
	b.Broadcast(f)
}

// AnnounceFail tells everyone a node is down. Receivers add the FAIL
// flag — and stop routing data-plane traffic to it.
func (b *Bus) AnnounceFail(targetID string) {
	f := b.buildFrame(MsgFail, "")
	f.TargetID = targetID
	b.Broadcast(f)
}

// acceptLoop hands off each incoming connection to readLoop.
func (b *Bus) acceptLoop() {
	for {
		c, err := b.lis.Accept()
		if err != nil {
			select {
			case <-b.stop:
				return
			default:
			}
			b.log.Warn("cluster bus accept", "err", err)
			return
		}
		go b.readLoop(c)
	}
}

// readLoop processes one connection's frames sequentially.
func (b *Bus) readLoop(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				b.log.Debug("cluster bus read", "err", err)
			}
			return
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			b.log.Warn("cluster bus malformed frame", "err", err)
			continue
		}
		b.handle(conn, f)
	}
}

// handle dispatches one frame.
func (b *Bus) handle(conn net.Conn, f Frame) {
	if f.From == "" {
		return
	}
	// Update sender bookkeeping.
	peer := b.state.Node(f.From)
	if peer == nil {
		peer = NewNode(f.From, f.Host, f.Port, f.BusPort, RoleMaster)
		peer = b.state.AddNode(peer)
	}
	peer.Touch(time.Now())
	for _, r := range f.Slots {
		for s := r[0]; s <= r[1]; s++ {
			peer.AddSlot(s)
		}
	}
	for _, g := range f.Gossip {
		if g.ID == "" || g.ID == b.localID() {
			continue
		}
		if b.state.Node(g.ID) == nil {
			b.state.AddNode(NewNode(g.ID, g.Host, g.Port, g.BusPort, RoleMaster))
		}
	}

	switch f.Type {
	case MsgPing, MsgMeet:
		// Cache the inbound peer's link so we can reply.
		b.cacheLink(peer.ID, conn)
		// Always reply with a PONG so the sender's failure timer resets.
		out := b.buildFrame(MsgPong, "")
		_ = b.sendByPeer(peer.ID, out)
	case MsgPong:
		b.cacheLink(peer.ID, conn)
		// nothing else: receiving the PONG already touched the node.
	case MsgFail:
		if f.TargetID != "" {
			if t := b.state.Node(f.TargetID); t != nil {
				t.SetFlag(FlagFail)
			}
		}
	case MsgUpdate:
		// Authoritative slot ownership; trust monotonic epochs.
		if f.MyEpoch >= peer.ConfigEpoch {
			peer.ConfigEpoch = f.MyEpoch
			for _, r := range f.Slots {
				for s := r[0]; s <= r[1]; s++ {
					_, _ = b.state.AssignSlot(s, peer.ID)
				}
			}
		}
	case MsgPublish:
		if b.publishHandler != nil {
			b.publishHandler(f.Channel, f.Payload)
		}
	}
}

// gossipLoop fires PINGs at a random subset of peers periodically so
// liveness propagates and joiners learn the membership.
func (b *Bus) gossipLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
		}
		f := b.buildFrame(MsgPing, "")
		b.Broadcast(f)
		// Also try to dial any peer we know about but don't have a link to.
		for _, n := range b.state.Nodes() {
			if n.HasFlag(FlagMyself) {
				continue
			}
			b.dialedMu.Lock()
			_, have := b.dialed[n.ID]
			b.dialedMu.Unlock()
			if !have && n.BusPort != "" {
				go b.openLink(n)
			}
		}
	}
}

// failureLoop checks for nodes that haven't responded recently and
// marks them PFAIL/FAIL.
func (b *Bus) failureLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	const pfailTimeout = 5 * time.Second
	const failTimeout = 15 * time.Second
	for {
		select {
		case <-b.stop:
			return
		case now := <-t.C:
			for _, n := range b.state.Nodes() {
				if n.HasFlag(FlagMyself) {
					continue
				}
				idle := n.IdleSince(now)
				if idle == 0 {
					continue
				}
				if idle > failTimeout && !n.HasFlag(FlagFail) {
					n.SetFlag(FlagFail)
					b.AnnounceFail(n.ID)
					b.log.Warn("cluster declared FAIL", "node", n.ID, "idle", idle)
				} else if idle > pfailTimeout {
					n.SetFlag(FlagPFail)
				}
			}
		}
	}
}

// openLink dials a peer and stores the outbound connection.
func (b *Bus) openLink(n *Node) {
	conn, err := net.DialTimeout("tcp", n.BusAddr(), 3*time.Second)
	if err != nil {
		b.log.Debug("bus dial failed", "node", n.ID, "err", err)
		return
	}
	b.cacheLink(n.ID, conn)
	go b.readLoop(conn)
	_ = b.sendByPeer(n.ID, b.buildFrame(MsgPing, ""))
}

func (b *Bus) cacheLink(peerID string, conn net.Conn) {
	b.dialedMu.Lock()
	defer b.dialedMu.Unlock()
	if existing, ok := b.dialed[peerID]; ok {
		if existing.conn == conn {
			return
		}
		_ = existing.conn.Close()
	}
	b.dialed[peerID] = &peerLink{conn: conn, bw: bufio.NewWriter(conn)}
}

func (b *Bus) sendByPeer(peerID string, f Frame) error {
	b.dialedMu.Lock()
	link := b.dialed[peerID]
	b.dialedMu.Unlock()
	if link == nil {
		return errors.New("no link to peer")
	}
	return b.send(link, f)
}

func (b *Bus) send(link *peerLink, f Frame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	link.mu.Lock()
	defer link.mu.Unlock()
	if _, err := link.bw.Write(body); err != nil {
		_ = link.conn.Close()
		return err
	}
	return link.bw.Flush()
}

// buildFrame fills in the sender-side fields every frame carries.
func (b *Bus) buildFrame(t MsgType, target string) Frame {
	myself := b.state.Myself()
	f := Frame{Type: t, At: time.Now()}
	if myself != nil {
		f.From = myself.ID
		f.Host = myself.Host
		f.Port = myself.Port
		f.BusPort = myself.BusPort
		f.Slots = myself.SlotRanges()
		f.MyEpoch = myself.ConfigEpoch
	}
	f.Epoch = b.state.CurrentEpoch()
	f.TargetID = target
	// Piggyback up to 3 random peers so newcomers learn the topology.
	for _, n := range b.state.Nodes() {
		if n.HasFlag(FlagMyself) {
			continue
		}
		f.Gossip = append(f.Gossip, GNode{
			ID: n.ID, Host: n.Host, Port: n.Port, BusPort: n.BusPort,
			Flags: uint32(n.Flags()), LastSeen: n.IdleSince(time.Now()).Nanoseconds(),
		})
		if len(f.Gossip) >= 3 {
			break
		}
	}
	return f
}

func (b *Bus) localID() string {
	if m := b.state.Myself(); m != nil {
		return m.ID
	}
	return ""
}
