// Package resp implements a minimal RESP2 parser and encoder plus a TCP
// server. Handles enough Redis commands for redis-cli to connect and play
// with NeuroCache: PING, SET, GET, DEL, EXISTS, INCR, KEYS, and the AI
// commands (SEMANTIC_SET/GET, CACHE_LLM, MEMORY_ADD/QUERY).
package resp

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

	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
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

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	bw := bufio.NewWriter(c)
	for {
		parts, err := readArray(br)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Debug("resp client disconnect", "err", err)
			}
			return
		}
		if len(parts) == 0 {
			continue
		}
		s.eng.CmdCount.Add(1)
		s.execute(parts, bw)
		_ = bw.Flush()
	}
}

func (s *Server) execute(parts []string, w *bufio.Writer) {
	cmd := strings.ToUpper(parts[0])
	args := parts[1:]
	switch cmd {
	case "PING":
		writeSimple(w, "PONG")

	case "COMMAND", "HELLO":
		// Minimal compatibility — enough to stop redis-cli from exiting.
		writeArray(w, []string{})

	case "SET":
		if len(args) < 2 {
			writeError(w, "wrong number of arguments for 'set'")
			return
		}
		ttl := time.Duration(0)
		for i := 2; i+1 < len(args); i += 2 {
			if strings.EqualFold(args[i], "EX") {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					ttl = time.Duration(n) * time.Second
				}
			}
		}
		s.eng.KV.Set(args[0], args[1], ttl)
		writeSimple(w, "OK")

	case "GET":
		if len(args) < 1 {
			writeError(w, "GET requires key")
			return
		}
		v, ok := s.eng.KV.Get(args[0])
		if !ok {
			writeNil(w)
			return
		}
		writeBulk(w, v)

	case "DEL":
		writeInt(w, int64(s.eng.KV.Del(args...)))

	case "EXISTS":
		n := 0
		for _, k := range args {
			if s.eng.KV.Exists(k) {
				n++
			}
		}
		writeInt(w, int64(n))

	case "INCR":
		if len(args) < 1 {
			writeError(w, "INCR requires key")
			return
		}
		v, err := s.eng.KV.Incr(args[0], 1)
		if err != nil {
			writeError(w, err.Error())
			return
		}
		writeInt(w, v)

	case "DECR":
		if len(args) < 1 {
			writeError(w, "DECR requires key")
			return
		}
		v, err := s.eng.KV.Incr(args[0], -1)
		if err != nil {
			writeError(w, err.Error())
			return
		}
		writeInt(w, v)

	case "KEYS":
		writeArray(w, s.eng.KV.Keys())

	case "FLUSHDB", "FLUSHALL":
		s.eng.KV.FlushAll()
		writeSimple(w, "OK")

	case "SEMANTIC_SET":
		if len(args) < 2 {
			writeError(w, "SEMANTIC_SET key value")
			return
		}
		s.eng.Semantic.Set(args[0], args[1])
		writeSimple(w, "OK")

	case "SEMANTIC_GET":
		if len(args) < 1 {
			writeError(w, "SEMANTIC_GET query")
			return
		}
		v, _, ok := s.eng.Semantic.Get(args[0], float32(s.eng.Cfg.SemThreshold))
		if !ok {
			writeNil(w)
			return
		}
		writeBulk(w, v)

	case "CACHE_LLM":
		if len(args) < 2 {
			writeError(w, "CACHE_LLM prompt response")
			return
		}
		s.eng.LLM.Set(args[0], args[1])
		writeSimple(w, "OK")

	case "CACHE_LLM_GET":
		if len(args) < 1 {
			writeError(w, "CACHE_LLM_GET prompt")
			return
		}
		v, _, ok := s.eng.LLM.Get(args[0], 0.88)
		if !ok {
			writeNil(w)
			return
		}
		writeBulk(w, v)

	case "MEMORY_ADD":
		if len(args) < 2 {
			writeError(w, "MEMORY_ADD user text")
			return
		}
		e := s.eng.Memory.Add(args[0], strings.Join(args[1:], " "), nil)
		writeBulk(w, e.ID)

	case "MEMORY_QUERY":
		if len(args) < 2 {
			writeError(w, "MEMORY_QUERY user query")
			return
		}
		hits := s.eng.Memory.Query(args[0], strings.Join(args[1:], " "), 5, 0.3)
		writeBulk(w, memory.Synthesize(hits))

	case "INFO":
		writeBulk(w, fmt.Sprintf("neurocache_version:0.1.0\nuptime:%ds\nkeys:%d",
			int(time.Since(s.eng.StartedAt).Seconds()), s.eng.KV.Size()))

	case "QUIT":
		writeSimple(w, "OK")

	default:
		writeError(w, "unknown command '"+cmd+"'")
	}
}
