package replication

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ReplicaLink is one master-side connection to a follower. Life cycle:
//
//	Attach → Handshake (REPLCONF, PSYNC) → FULLRESYNC (RDB dump) →
//	Streaming (writes fan out from backlog) → Closed.
//
// The write path holds a short lock around `Send` so a slow replica
// doesn't block the producer — if the send buffer is full we drop the
// connection and let the replica reconnect + resync.
type ReplicaLink struct {
	ID        string
	Conn      net.Conn
	br        *bufio.Reader
	bw        *bufio.Writer
	ListenPort string // port the replica's own RESP server listens on
	Capa       []string
	mu         sync.Mutex

	AckOffset   atomic.Int64 // highest offset the replica has ACKed
	LastAck     atomic.Int64 // unix-nano of the most recent ACK
	initialSync bool         // true while the RDB dump is streaming

	closed atomic.Bool
	stop   chan struct{}
}

// NewReplicaLink wraps an accepted socket. Call after the server has
// read the initial REPLCONF/PSYNC frames and is ready to stream.
func NewReplicaLink(conn net.Conn, br *bufio.Reader, bw *bufio.Writer) *ReplicaLink {
	r := &ReplicaLink{
		ID: conn.RemoteAddr().String(),
		Conn: conn, br: br, bw: bw, stop: make(chan struct{}),
	}
	r.LastAck.Store(time.Now().UnixNano())
	return r
}

// Close disconnects the replica. Idempotent.
func (r *ReplicaLink) Close() {
	if r.closed.Swap(true) {
		return
	}
	close(r.stop)
	_ = r.Conn.Close()
}

// Send writes a raw RESP frame to the replica. Failure closes the link
// so the master's fan-out loop can prune it.
func (r *ReplicaLink) Send(b []byte) error {
	if r.closed.Load() {
		return errors.New("replica link closed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.bw.Write(b); err != nil {
		r.Close()
		return err
	}
	return r.bw.Flush()
}

// Reader returns the buffered reader — the master uses it to observe
// REPLCONF ACK <offset> heartbeats during streaming.
func (r *ReplicaLink) Reader() *bufio.Reader { return r.br }

// Stop chan lets goroutines abort cleanly when the link dies.
func (r *ReplicaLink) StopCh() <-chan struct{} { return r.stop }

// Master owns the write-fan-out loop. Its job is simple: any bytes
// appended to the backlog must be mirrored to every connected replica.
// Slow replicas are disconnected, not blocked.
type Master struct {
	state   *State
	backlog *Backlog

	pendingMu sync.Mutex
	pending   []byte // buffered writes awaiting fan-out
	notify    chan struct{}

	stop chan struct{}
}

// NewMaster wires the master around a state + backlog.
func NewMaster(state *State, backlog *Backlog) *Master {
	return &Master{
		state: state, backlog: backlog,
		notify: make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
}

// Start kicks off the fan-out goroutine. Safe to call once.
func (m *Master) Start() { go m.fanoutLoop() }

// Stop closes the fan-out loop.
func (m *Master) Stop() {
	select {
	case <-m.stop:
		return
	default:
		close(m.stop)
	}
}

// Propagate is called by the engine on every write command. It appends
// the RESP-encoded command to the backlog + queues it for fan-out.
func (m *Master) Propagate(cmd string, args []string) {
	frame := Encode(cmd, args)
	after := m.backlog.Append(frame)
	m.state.offset.Store(after)

	m.pendingMu.Lock()
	m.pending = append(m.pending, frame...)
	m.pendingMu.Unlock()
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *Master) drain() []byte {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := m.pending
	m.pending = nil
	return out
}

func (m *Master) fanoutLoop() {
	for {
		select {
		case <-m.stop:
			return
		case <-m.notify:
		}
		frame := m.drain()
		if frame == nil {
			continue
		}
		for _, r := range m.state.Replicas() {
			if r.initialSync {
				continue
			}
			if err := r.Send(frame); err != nil {
				m.state.RemoveReplica(r)
			}
		}
	}
}

// Handshake reply helpers — the master uses these when a replica
// hits it with PSYNC / REPLCONF.

// FullResyncReply builds the "+FULLRESYNC <replid> <offset>\r\n" line
// the master sends before streaming an RDB dump.
func FullResyncReply(replID string, offset int64) []byte {
	return []byte("+FULLRESYNC " + replID + " " + strconv.FormatInt(offset, 10) + "\r\n")
}

// ContinueReply is used when the replica's (replid, offset) lives in
// the backlog — we resume streaming without re-sending the RDB.
func ContinueReply(replID string) []byte {
	return []byte("+CONTINUE " + replID + "\r\n")
}

// BulkHeader produces "$<n>\r\n" for wrapping the RDB dump.
func BulkHeader(n int) []byte {
	return []byte(fmt.Sprintf("$%d\r\n", n))
}
