// Package resp implements the RESP2 wire protocol plus the command
// dispatch every connected client runs against. Each TCP connection gets
// its own conn struct so transaction state (MULTI/EXEC) and pub/sub
// subscriptions are naturally scoped to the connection.
package resp

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
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
	tls  *tls.Config // nil = plain TCP

	mu   sync.Mutex
	lis  net.Listener
	quit chan struct{}
}

func NewServer(addr string, eng *engine.Engine, log *slog.Logger) *Server {
	s := &Server{addr: addr, eng: eng, log: log, quit: make(chan struct{})}
	if cfg, err := buildTLSConfig(eng.Cfg); err != nil {
		log.Error("tls config build failed", "err", err)
	} else if cfg != nil {
		s.tls = cfg
		log.Info("tls enabled for resp listener", "client_auth", eng.Cfg.TLSClientAuth)
	}
	return s
}

// buildTLSConfig assembles a *tls.Config from the engine's config
// when a cert+key pair is provided. Returns (nil, nil) when TLS isn't
// configured — callers fall back to plain TCP. Setting NEUROCACHE_TLS_CA
// + a non-"none" client-auth mode opts into mTLS.
func buildTLSConfig(c config.Config) (*tls.Config, error) {
	if c.TLSCertFile == "" || c.TLSKeyFile == "" {
		return nil, nil
	}
	pair, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}
	out := &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	}
	switch c.TLSClientAuth {
	case "request":
		out.ClientAuth = tls.RequestClientCert
	case "require":
		out.ClientAuth = tls.RequireAnyClientCert
	case "verify":
		out.ClientAuth = tls.RequireAndVerifyClientCert
	default:
		out.ClientAuth = tls.NoClientCert
	}
	if c.TLSCAFile != "" {
		pool := x509.NewCertPool()
		ca, err := os.ReadFile(c.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca file: %w", err)
		}
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errors.New("ca file held no PEM certificates")
		}
		out.ClientCAs = pool
	}
	return out, nil
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) ListenAndServe() error {
	var l net.Listener
	var err error
	if s.tls != nil {
		l, err = tls.Listen("tcp", s.addr, s.tls)
	} else {
		l, err = net.Listen("tcp", s.addr)
	}
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

	// proto is the negotiated wire protocol: 2 (default) or 3. HELLO 3
	// promotes to RESP3, which adds Map/Set/Bool/Double/BigNumber/Push
	// reply types. proto stays at 2 for the connection's life otherwise.
	proto int

	// monitorID is non-zero when this conn is in MONITOR mode. Set by
	// the MONITOR command, consumed by cleanup() on disconnect.
	monitorID uint64

	// shardSubs holds active SSUBSCRIBE handles keyed by channel name.
	shardSubs map[string]*pubsub.Subscription

	// invalidateCh receives CLIENT TRACKING invalidation push frames.
	// nil until the client opts into tracking; closed on disconnect.
	invalidateCh chan []string

	// cachingNext is set by CLIENT CACHING YES|NO and consumed (cleared)
	// by the very next command — matches Redis's single-shot semantics.
	cachingNext bool

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
		// 64 KiB buffers. Default bufio sizes (4 KiB) caused a syscall
		// per ~4 KiB of payload for large GETs/SETs — measured at 30%
		// of Redis throughput on 100 KiB values. With 64 KiB we get
		// one syscall for typical replies and ~2 for 100 KiB ones.
		br: bufio.NewReaderSize(nc, 64*1024),
		bw: bufio.NewWriterSize(nc, 64*1024),
		eng:  s.eng,
		log:  s.log,
		tx:   transaction.New(),
		subs:      map[string]*pubsub.Subscription{},
		psub:      map[string]*pubsub.Subscription{},
		shardSubs: map[string]*pubsub.Subscription{},
		user:  s.eng.ACL.DefaultUser(),
		info:  s.eng.Clients.Register(nc.RemoteAddr().String()),
		proto: 2,
		done:  make(chan struct{}),
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
		// Hoist the upper-cased command name once — previously we
		// upper-cased it 4 times per command (Touch, RecordCommand,
		// SLOTracker.Record, Monitor.Broadcast) plus once more inside
		// execute(). asciiUpper is zero-alloc when the input is already
		// upper, which redis-benchmark and most production drivers
		// emit, so the typical case hits no allocation at all.
		cmdU := asciiUpper(parts[0])
		s.eng.Clients.Touch(c.info, cmdU)
		c.executeUpper(parts, cmdU)
		dur := time.Since(start)
		s.eng.Metrics.RecordCommand(cmdU, dur)
		s.eng.SlowLog.Maybe(dur, parts, c.info.Addr)
		s.eng.Latency.Record("command", dur)
		// Phase 11: feed the SLO tracker with the latency sample.
		// Cheap when no targets are configured for this command —
		// SLOTracker.Record returns immediately on a missing entry.
		if s.eng.SLOTracker != nil {
			s.eng.SLOTracker.Record(cmdU, dur)
		}
		// MONITOR fan-out: cheap when no subscribers are attached.
		s.eng.Monitor.Broadcast(c.info.Addr, 0, cmdU, parts[1:])
		c.writeMu.Lock()
		// Honour CLIENT REPLY skip/off: silence the next reply or all replies.
		switch c.info.ReplyMode {
		case "skip":
			_ = c.bw.Flush()
			c.bw.Reset(c.nc)
			c.info.ReplyMode = "on"
		case "off":
			c.bw.Reset(c.nc)
		default:
			// Pipelining optimization: only flush the network writer
			// when the read side has nothing buffered. Pipelined
			// clients (redis-benchmark -P, ioredis multi() batches,
			// most production drivers) feed N commands at once; the
			// previous behaviour flushed after each one and triggered
			// per-op syscall + TCP overhead. Now we accumulate replies
			// while the input pipeline is draining and emit one big
			// write at the end — same shape Redis uses internally.
			//
			// The bufio.Writer auto-flushes when its buffer fills, so
			// we never accumulate more than ~4 KiB unflushed even
			// under pathological pipelines.
			if c.br.Buffered() == 0 {
				_ = c.bw.Flush()
			}
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
	for _, sub := range c.shardSubs {
		sub.Close()
	}
	if c.monitorID != 0 {
		c.eng.Monitor.Unsubscribe(c.monitorID)
	}
	if c.invalidateCh != nil {
		engine.UnregisterInvalidationChannel(c.info.ID)
		c.eng.Tracking.Disable(c.info.ID)
		close(c.invalidateCh)
		c.invalidateCh = nil
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
//
// Kept for callers that don't already have an upper-cased command name
// (script eval, transaction replay, replication apply). The hot dispatch
// path goes through executeUpper which skips the redundant ToUpper.
func (c *conn) execute(parts []string) {
	c.executeUpper(parts, asciiUpper(parts[0]))
}

// executeUpper is the hot-path entry — the caller has already
// asciiUpper'd the command name. Avoids re-allocating it.
func (c *conn) executeUpper(parts []string, cmd string) {
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
			// Fast-path: the typical local-dev / simple-prod user is the
			// default user with full perms. Skip the Allowed() call
			// entirely — that path does CategoriesFor + map lookups +
			// audit-log mu.RLock per command, which we measured at ~5%
			// of dispatch time under redis-benchmark workloads.
			if !c.user.AllowsEverything() {
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
	// CLIENT NO-TOUCH (Redis 7.2): if this conn opted in, snapshot
	// the LastRead / Hits of every key the command touches so we can
	// restore them after dispatch. Only meaningful for read-class
	// commands; writes are exempt because they legitimately mutate
	// the entry's state.
	var touchSnap []touchSnapshot
	if c.info != nil && c.info.NoTouch && !isWriteCommand(cmd) {
		touchSnap = c.snapshotTouchedKeys(args)
	}
	c.dispatch(cmd, args)
	if touchSnap != nil {
		c.restoreTouchedKeys(touchSnap)
	}
	c.writeMu.Unlock()
	// Record after dispatch so failed commands don't pollute the AOF.
	// This check is a best-effort — a write that errored out at parse
	// time still gets appended, and replay will just log-and-skip it.
	if isWriteCommand(cmd) {
		c.eng.RecordWrite(cmd, args)
	}
}

// touchSnapshot captures the per-entry state CLIENT NO-TOUCH must
// preserve across a read.
type touchSnapshot struct {
	key      string
	hits     uint64
	lastRead time.Time
}

// snapshotTouchedKeys grabs LastRead / Hits for every key the
// command might touch. We use keys_for_command — the same key
// extractor the ACL gate uses — so the snapshot covers exactly
// what the read will inspect.
func (c *conn) snapshotTouchedKeys(args []string) []touchSnapshot {
	keys := keysForCommand(strings.ToUpper(args[0]), args[1:])
	if len(keys) == 0 {
		// Some no-key reads (DBSIZE, KEYS, etc.) — nothing to preserve.
		return nil
	}
	out := make([]touchSnapshot, 0, len(keys))
	for _, k := range keys {
		hits, last, ok := c.eng.KV.PeekTouchState(k)
		if !ok {
			continue
		}
		out = append(out, touchSnapshot{key: k, hits: hits, lastRead: last})
	}
	return out
}

// restoreTouchedKeys puts the LastRead / Hits we snapshotted back —
// undoing whatever the dispatch did. Keys that disappeared during
// the call (deleted by a concurrent writer) are skipped silently.
func (c *conn) restoreTouchedKeys(snap []touchSnapshot) {
	for _, s := range snap {
		c.eng.KV.RestoreTouchState(s.key, s.hits, s.lastRead)
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
