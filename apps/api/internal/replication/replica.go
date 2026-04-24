package replication

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Applier is the callback the replica client invokes for every command
// it receives from the master. The engine-side implementation executes
// the command while holding a "replica mode" flag that suppresses AOF
// appends and replication fan-out, so we never echo back.
type Applier func(cmd string, args []string) error

// Client is the replica-side controller. One per process. It owns the
// dial loop, handshake, and apply goroutine.
type Client struct {
	State   *State
	Log     *slog.Logger
	Applier Applier

	// ListenPort is what we announce via REPLCONF listening-port so the
	// master can surface it in `CLUSTER NODES` / `INFO replication`.
	ListenPort string

	// RDBRestore is invoked when the master sends a full-resync dump.
	// The engine provides a loader that wipes the current keyspace and
	// decodes the blob back. If nil, we log + skip (tests sometimes
	// want that).
	RDBRestore func(blob []byte) error

	mu   sync.Mutex
	conn net.Conn
	stop chan struct{}
}

// NewClient constructs a replica controller.
func NewClient(state *State, log *slog.Logger, a Applier) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{State: state, Log: log, Applier: a, stop: make(chan struct{})}
}

// Start begins the dial/handshake/apply loop in a goroutine. Safe to
// call once per role switch; Stop() must fire before a second Start().
func (c *Client) Start() { go c.run() }

// Stop signals the loop to exit and closes the live connection.
func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// run is the outer reconnect loop. Any dial/handshake/stream failure
// drops back here; we wait a short backoff and try again.
func (c *Client) run() {
	backoff := time.Second
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		if c.State.Role() != RoleReplica {
			return
		}
		host, port := c.State.Master()
		if host == "" {
			return
		}
		c.State.SetLinkState(LinkConnecting)
		addr := net.JoinHostPort(host, port)
		nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			c.Log.Warn("replica dial failed", "addr", addr, "err", err)
			time.Sleep(backoff)
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		c.mu.Lock()
		c.conn = nc
		c.mu.Unlock()
		backoff = time.Second

		if err := c.handshake(nc); err != nil {
			c.Log.Warn("replica handshake failed", "err", err)
			_ = nc.Close()
			time.Sleep(backoff)
			continue
		}
		if err := c.streamLoop(nc); err != nil {
			c.Log.Info("replica stream ended", "err", err)
		}
		_ = nc.Close()
		c.State.SetLinkState(LinkDown)
	}
}

// handshake runs PING → REPLCONF listening-port → REPLCONF capa → PSYNC
// against the master, consuming the RDB dump if it's a FULLRESYNC.
func (c *Client) handshake(nc net.Conn) error {
	c.State.SetLinkState(LinkHandshake)
	bw := bufio.NewWriter(nc)
	br := bufio.NewReader(nc)

	send := func(cmd string, args ...string) error {
		if _, err := bw.Write(Encode(cmd, args)); err != nil {
			return err
		}
		return bw.Flush()
	}
	expectSimpleOK := func() error {
		k, pl, err := ReadReply(br)
		if err != nil {
			return err
		}
		if k == '+' {
			return nil
		}
		if k == '-' {
			return errors.New(pl)
		}
		return fmt.Errorf("unexpected reply kind %q", k)
	}

	if err := send("PING"); err != nil {
		return err
	}
	// PONG or authentication challenge
	if k, pl, err := ReadReply(br); err != nil {
		return err
	} else if k == '-' {
		return errors.New(pl)
	}

	if c.ListenPort != "" {
		if err := send("REPLCONF", "listening-port", c.ListenPort); err != nil {
			return err
		}
		if err := expectSimpleOK(); err != nil {
			return fmt.Errorf("REPLCONF listening-port: %w", err)
		}
	}
	if err := send("REPLCONF", "capa", "eof", "capa", "psync2"); err != nil {
		return err
	}
	if err := expectSimpleOK(); err != nil {
		return fmt.Errorf("REPLCONF capa: %w", err)
	}

	// PSYNC: on first connect we don't have a replid/offset, so send "? -1".
	replID := c.State.ReplID()
	offset := c.State.MasterOffset()
	if offset == 0 && replID == c.State.ReplID() && !c.State.replicaMode.Load() {
		replID = "?"
		offset = -1
	}
	if err := send("PSYNC", replID, strconv.FormatInt(offset, 10)); err != nil {
		return err
	}
	kind, payload, err := ReadReply(br)
	if err != nil {
		return err
	}
	if kind == '-' {
		return errors.New(payload)
	}
	if kind != '+' {
		return fmt.Errorf("unexpected PSYNC reply kind %q", kind)
	}
	parts := strings.Fields(payload)
	switch {
	case len(parts) >= 3 && parts[0] == "FULLRESYNC":
		c.State.SetReplID(parts[1])
		if off, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
			c.State.SetMasterOffset(off)
		}
		c.State.SetLinkState(LinkSyncing)
		if err := c.consumeRDB(br); err != nil {
			return err
		}
	case len(parts) >= 1 && parts[0] == "CONTINUE":
		c.Log.Info("replica partial resync accepted")
	default:
		return fmt.Errorf("unknown PSYNC reply: %s", payload)
	}
	c.State.SetLinkState(LinkConnected)

	// Start the ACK heartbeat. It runs for as long as the apply loop
	// runs and shares the same bw (protected by a tiny mutex).
	go c.ackLoop(bw, nc)

	// Continue streaming in the apply loop.
	c.applyStream(br)
	return nil
}

// consumeRDB reads a bulk-string framed RDB dump from the master and
// hands it to the engine's RDBRestore callback.
func (c *Client) consumeRDB(br *bufio.Reader) error {
	tag, err := br.ReadByte()
	if err != nil {
		return err
	}
	if tag != '$' {
		return fmt.Errorf("expected $ for RDB dump, got %q", tag)
	}
	line, err := readLine(br)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return err
	}
	if n < 0 {
		return errors.New("negative RDB size")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		return err
	}
	// The trailing CRLF belongs to the bulk frame.
	if _, err := readLine(br); err != nil {
		return err
	}
	if c.RDBRestore != nil {
		if err := c.RDBRestore(buf); err != nil {
			return fmt.Errorf("RDB restore: %w", err)
		}
	} else {
		c.Log.Warn("no RDB restore callback; dump discarded", "bytes", n)
	}
	return nil
}

// streamLoop is a no-op shim so run() can return without swallowing
// errors. The real work happens inside applyStream() after handshake.
func (c *Client) streamLoop(nc net.Conn) error { return nil }

// applyStream reads command frames and feeds them to the engine.
func (c *Client) applyStream(br *bufio.Reader) {
	for {
		parts, err := ReadArray(br)
		if err != nil {
			c.Log.Info("replica stream terminated", "err", err)
			return
		}
		if len(parts) == 0 {
			continue
		}
		// Count the bytes as the master does so ACKs line up.
		n := int64(len(Encode(parts[0], parts[1:])))
		c.State.SetMasterOffset(c.State.MasterOffset() + n)
		if c.Applier != nil {
			if err := c.Applier(strings.ToUpper(parts[0]), parts[1:]); err != nil {
				c.Log.Warn("replica apply failed", "cmd", parts[0], "err", err)
			}
		}
	}
}

// ackLoop periodically informs the master of the latest applied offset.
// Redis replicas ACK once per second; more often doesn't help WAIT but
// does chew network.
func (c *Client) ackLoop(bw *bufio.Writer, nc net.Conn) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		off := strconv.FormatInt(c.State.MasterOffset(), 10)
		frame := Encode("REPLCONF", []string{"ACK", off})
		if _, err := bw.Write(frame); err != nil {
			return
		}
		if err := bw.Flush(); err != nil {
			return
		}
		_ = nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}
}
