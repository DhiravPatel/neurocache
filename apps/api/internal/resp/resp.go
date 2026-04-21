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

	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
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
		done: make(chan struct{}),
	}
	defer c.cleanup()

	for {
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
		start := time.Now()
		s.eng.CmdCount.Add(1)
		c.execute(parts)
		s.eng.Metrics.RecordCommand(strings.ToUpper(parts[0]), time.Since(start))
		c.writeMu.Lock()
		_ = c.bw.Flush()
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

	// In subscribed mode almost nothing is allowed.
	if len(c.subs) > 0 || len(c.psub) > 0 {
		if !allowedDuringSubscribe[cmd] {
			c.writeMu.Lock()
			writeError(c.bw, "Can't execute '"+strings.ToLower(cmd)+"': only (P)SUBSCRIBE / (P)UNSUBSCRIBE / PING / QUIT are allowed in this context")
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
	defer c.writeMu.Unlock()
	c.dispatch(cmd, args)
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
