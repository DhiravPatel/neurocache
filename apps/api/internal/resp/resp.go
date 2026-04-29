// Package resp implements the RESP2 wire protocol plus the command
// dispatch every connected client runs against. Each TCP connection gets
// its own conn struct so transaction state (MULTI/EXEC) and pub/sub
// subscriptions are naturally scoped to the connection.
package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
	"github.com/dhiravpatel/neurocache/apps/api/internal/introspect"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
	"github.com/dhiravpatel/neurocache/apps/api/internal/replication"
	"github.com/dhiravpatel/neurocache/apps/api/internal/transaction"
)

type Server struct {
	addr string
	eng  *engine.Engine
	log  *slog.Logger

	mu   sync.Mutex
	lis  net.Listener
	quit chan struct{}
}

func NewServer(addr string, eng *engine.Engine, log *slog.Logger) *Server {
	return &Server{addr: addr, eng: eng, log: log, quit: make(chan struct{})}
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) ListenAndServe() error {
	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lis = l
	s.mu.Unlock()

	for {
		c, err := l.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil
			default:
				return err
			}
		}
		go s.handle(c)
	}
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lis != nil {
		close(s.quit)
		_ = s.lis.Close()
	}
}

// conn owns all per-connection mutable state.
type conn struct {
	nc   net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
	eng  *engine.Engine
	log  *slog.Logger
	tx   *transaction.State
	subs map[string]*pubsub.Subscription
	psub map[string]*pubsub.Subscription

	// auth holds the user this conn is currently acting as. Defaults to
	// the ACL "default" user; AUTH swaps it.
	user *acl.User

	// info is the registry record for CLIENT LIST/KILL/PAUSE.
	info *introspect.ClientInfo

	// Replication-handshake scratchpad. These are populated during the
	// initial REPLCONF frames so PSYNC has the info it needs. Once the
	// connection is adopted as a replica link the engine writes to it
	// exclusively — dispatch stops running.
	replListenPort  string
	replCapa        []string
	adoptedByMaster *replication.ReplicaLink

	// Cluster-mode per-conn flags. asking is single-shot — set by the
	// ASKING command, consumed by the very next dispatched command.
	// readonly toggles whether this conn accepts reads on imported slots
	// from a replica perspective (READONLY / READWRITE).
	asking   bool
	readonly bool

	// writeMu serializes writes across the client-reply goroutine and
	// background pub/sub fan-out, so frames never interleave.
	writeMu sync.Mutex

	// done closes when the connection is being torn down so background
	// pub/sub readers can exit cleanly.
	done chan struct{}
}

func (s *Server) handle(nc net.Conn) {
	c := &conn{
		nc:   nc,
		br:   bufio.NewReader(nc),
		bw:   bufio.NewWriter(nc),
		eng:  s.eng,
		log:  s.log,
		tx:   transaction.New(),
		subs: map[string]*pubsub.Subscription{},
		psub: map[string]*pubsub.Subscription{},
		user: s.eng.ACL.DefaultUser(),
		info: s.eng.Clients.Register(nc.RemoteAddr().String()),
		done: make(chan struct{}),
	}
	if c.user != nil {
		c.info.Username = c.user.Name
	}
	defer c.cleanup()

	for {
		// If this conn has been adopted as a replica link, the master
		// fan-out now owns the socket. Park here until the link closes.
		if c.adoptedByMaster != nil {
			<-c.adoptedByMaster.StopCh()
			return
		}
		parts, err := readArray(c.br)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Debug("resp client disconnect", "err", err)
			}
			return
		}
		if len(parts) == 0 {
			continue
		}
		// Honour CLIENT PAUSE — the client may sit through the pause
		// window before its command runs.
		if rem := s.eng.Clients.PauseRemaining(); rem > 0 {
			time.Sleep(rem)
		}
		start := time.Now()
		s.eng.CmdCount.Add(1)
		s.eng.Clients.Touch(c.info, strings.ToUpper(parts[0]))
		c.execute(parts)
		dur := time.Since(start)
		s.eng.Metrics.RecordCommand(strings.ToUpper(parts[0]), dur)
		s.eng.SlowLog.Maybe(dur, parts, c.info.Addr)
		s.eng.Latency.Record("command", dur)
		c.writeMu.Lock()
		// Honour CLIENT REPLY skip/off: silence the next reply or all replies.
		switch c.info.ReplyMode {
		case "skip":
			_ = c.bw.Flush()
			// drop the buffered bytes by truncating — implemented by
			// resetting the writer's underlying buffer.
			c.bw.Reset(c.nc)
			c.info.ReplyMode = "on"
		case "off":
			c.bw.Reset(c.nc)
		default:
			_ = c.bw.Flush()
		}
		c.writeMu.Unlock()
	}
}

func (c *conn) cleanup() {
	close(c.done)
	for _, sub := range c.subs {
		sub.Close()
	}
	for _, sub := range c.psub {
		sub.Close()
	}
	if c.info != nil {
		c.eng.Clients.Forget(c.info.ID)
	}
	_ = c.nc.Close()
}

// allowedDuringSubscribe enumerates commands RESP2 permits while in
// pub/sub mode. Anything else errors out with the canonical message.
var allowedDuringSubscribe = map[string]bool{
	"SUBSCRIBE": true, "UNSUBSCRIBE": true,
	"PSUBSCRIBE": true, "PUNSUBSCRIBE": true,
	"PING": true, "QUIT": true,
}

// execute is the top-level command router. It gates MULTI queueing and
// subscribed-mode restrictions before handing off to the per-command
// handler.
func (c *conn) execute(parts []string) {
	cmd := strings.ToUpper(parts[0])
	args := parts[1:]

	// ACL gate. AUTH itself is always allowed (otherwise unauth'd
	// clients couldn't ever log in). HELLO + RESET + QUIT are also free.
	// Replication handshake commands also bypass the gate — replicas
	// connect before any AUTH can happen and the protocol is internal.
	switch cmd {
	case "AUTH", "HELLO", "QUIT", "RESET",
		"PSYNC", "REPLCONF", "SYNC":
		// fall through
	default:
		if c.user == nil && c.eng.Cfg.ProtectedMode {
			c.writeMu.Lock()
			writeTypedError(c.bw, "NOAUTH", "Authentication required.")
			c.writeMu.Unlock()
			return
		}
		if c.user != nil {
			keys := keysForCommand(cmd, args)
			channels := channelsForCommand(cmd, args)
			if err := c.eng.ACL.Allowed(c.user, cmd, keys, channels); err != nil {
				c.writeMu.Lock()
				writeTypedError(c.bw, "NOPERM", strings.TrimPrefix(err.Error(), "NOPERM "))
				c.writeMu.Unlock()
				return
			}
		}
	}

	// In subscribed mode almost nothing is allowed.
	if len(c.subs) > 0 || len(c.psub) > 0 {
		if !allowedDuringSubscribe[cmd] {
			c.writeMu.Lock()
			writeError(c.bw, "Can't execute '"+strings.ToLower(cmd)+"': only (P)SUBSCRIBE / (P)UNSUBSCRIBE / PING / QUIT are allowed in this context")
			c.writeMu.Unlock()
			return
		}
	}

	// Cluster slot routing. Skipped for cluster-admin commands and any
	// command that doesn't carry a key. The asking flag is single-shot.
	if c.eng.Cluster != nil && c.eng.Cluster.Enabled() && !clusterRoutingExempt[cmd] {
		keys := keysForCommand(cmd, args)
		v := c.eng.Cluster.Route(keys, c.asking)
		c.asking = false
		switch v.Redirect {
		case 1, 2: // RedirectMoved (1) or RedirectAsk (2)
			c.writeMu.Lock()
			writeError(c.bw, v.Error())
			c.writeMu.Unlock()
			return
		case 3, 4, 5: // CrossSlot, TryAgain, ClusterDown
			c.writeMu.Lock()
			writeError(c.bw, v.Error())
			c.writeMu.Unlock()
			return
		}
	}

	// Inside a MULTI block, queue everything except the transaction
	// control commands themselves.
	if c.tx.InProgress() {
		switch cmd {
		case "EXEC", "DISCARD", "MULTI", "WATCH", "UNWATCH":
			// fall through to the handler
		default:
			if err := c.tx.Queue(cmd, args); err != nil {
				c.writeMu.Lock()
				writeError(c.bw, err.Error())
				c.writeMu.Unlock()
				return
			}
			c.writeMu.Lock()
			writeSimple(c.bw, "QUEUED")
			c.writeMu.Unlock()
			return
		}
	}

	c.writeMu.Lock()
	c.dispatch(cmd, args)
	c.writeMu.Unlock()
	// Record after dispatch so failed commands don't pollute the AOF.
	// This check is a best-effort — a write that errored out at parse
	// time still gets appended, and replay will just log-and-skip it.
	if isWriteCommand(cmd) {
		c.eng.RecordWrite(cmd, args)
	}
}

// wantArgs enforces a minimum argument count, writing the canonical
// "wrong number of arguments" error and returning false on failure.
func (c *conn) wantArgs(cmd string, args []string, n int) bool {
	if len(args) < n {
		writeError(c.bw, fmt.Sprintf("wrong number of arguments for '%s'", strings.ToLower(cmd)))
		return false
	}
	return true
}

// writeStoreErr maps store errors (WRONGTYPE etc.) to proper RESP errors.
func (c *conn) writeStoreErr(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "WRONGTYPE") {
		writeTypedError(c.bw, "WRONGTYPE", strings.TrimPrefix(msg, "WRONGTYPE "))
		return
	}
	if strings.HasPrefix(msg, "ERR ") {
		writeTypedError(c.bw, "ERR", strings.TrimPrefix(msg, "ERR "))
		return
	}
	writeError(c.bw, msg)
}
