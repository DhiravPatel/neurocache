// Package persistence implements durability for NeuroCache.
//
//	AOF  — append-only command log; replayed on startup
//	RDB  — point-in-time JSON snapshots; periodic + on-shutdown
//
// Callers (the engine) inject a CommandRunner so persistence stays
// decoupled from dispatch logic.
package persistence

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FsyncPolicy controls how often the AOF is fsynced.
type FsyncPolicy int

const (
	FsyncEverySec FsyncPolicy = iota // balance of durability + throughput
	FsyncAlways                      // every command (slow but safest)
	FsyncNo                          // leave it to the OS
)

// AOF is the append-only command log. Writes land in an in-memory
// buffered writer and are flushed by a background goroutine per the
// chosen fsync policy.
type AOF struct {
	path   string
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	policy FsyncPolicy
	stopCh chan struct{}
	doneCh chan struct{}
}

// OpenAOF opens (or creates) the AOF file and starts its flush worker.
func OpenAOF(path string, policy FsyncPolicy) (*AOF, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	a := &AOF{
		path:   path,
		f:      f,
		w:      bufio.NewWriterSize(f, 64*1024),
		policy: policy,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go a.flushLoop()
	return a, nil
}

// Append writes a single command to the buffer in RESP-inspired format:
//
//	*N\r\n$len(arg)\r\narg\r\n ... (one frame per command)
//
// Only write-path commands should be appended; reads are pointless.
func (a *AOF) Append(cmd string, args []string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := fmt.Fprintf(a.w, "*%d\r\n", 1+len(args)); err != nil {
		return err
	}
	if err := writeBulk(a.w, cmd); err != nil {
		return err
	}
	for _, arg := range args {
		if err := writeBulk(a.w, arg); err != nil {
			return err
		}
	}
	if a.policy == FsyncAlways {
		if err := a.w.Flush(); err != nil {
			return err
		}
		return a.f.Sync()
	}
	return nil
}

// flushLoop fsyncs per policy. Running on its own goroutine keeps the
// hot path (Append) cheap.
func (a *AOF) flushLoop() {
	defer close(a.doneCh)
	if a.policy == FsyncNo {
		<-a.stopCh
		a.mu.Lock()
		_ = a.w.Flush()
		a.mu.Unlock()
		return
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			a.mu.Lock()
			_ = a.w.Flush()
			_ = a.f.Sync()
			a.mu.Unlock()
			return
		case <-t.C:
			a.mu.Lock()
			if err := a.w.Flush(); err == nil {
				_ = a.f.Sync()
			}
			a.mu.Unlock()
		}
	}
}

// Close flushes and closes the file.
func (a *AOF) Close() error {
	if a == nil {
		return nil
	}
	close(a.stopCh)
	<-a.doneCh
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.Close()
}

// Rewrite dumps the live keyspace back to the AOF using a passed-in
// snapshot builder (usually the engine asks the store for a snapshot
// and turns each key into SET/RPUSH/HSET/...). The temp file is renamed
// over the old one atomically on success.
func (a *AOF) Rewrite(build func(*bufio.Writer) error) error {
	if a == nil {
		return nil
	}
	tmp := a.path + ".rewrite"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 256*1024)
	if err := build(w); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.w.Flush(); err != nil {
		return err
	}
	if err := a.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		// best-effort reopen of the previous file if rename fails
		a.f, _ = os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
		a.w = bufio.NewWriterSize(a.f, 64*1024)
		return err
	}
	a.f, err = os.OpenFile(a.path, os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	a.w = bufio.NewWriterSize(a.f, 64*1024)
	return nil
}

// Replay walks a file at path and feeds each command through run. This
// is called once at engine startup before any clients connect.
func Replay(path string, run func(cmd string, args []string) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	count := 0
	for {
		cmd, args, err := readCommand(br)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("replay at entry %d: %w", count, err)
		}
		if err := run(cmd, args); err != nil {
			return fmt.Errorf("replay of %s failed: %w", cmd, err)
		}
		count++
	}
}

// ─── small RESP codec for AOF entries ──────────────────────────────────

func writeBulk(w *bufio.Writer, s string) error {
	if _, err := fmt.Fprintf(w, "$%d\r\n", len(s)); err != nil {
		return err
	}
	if _, err := w.WriteString(s); err != nil {
		return err
	}
	_, err := w.WriteString("\r\n")
	return err
}

func readCommand(br *bufio.Reader) (string, []string, error) {
	b, err := br.ReadByte()
	if err != nil {
		return "", nil, err
	}
	if b != '*' {
		return "", nil, errors.New("AOF: expected array header")
	}
	line, err := readLine(br)
	if err != nil {
		return "", nil, err
	}
	n, err := strconv.Atoi(line)
	if err != nil || n <= 0 {
		return "", nil, errors.New("AOF: invalid array length")
	}
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if tag, err := br.ReadByte(); err != nil {
			return "", nil, err
		} else if tag != '$' {
			return "", nil, errors.New("AOF: expected bulk header")
		}
		ll, err := readLine(br)
		if err != nil {
			return "", nil, err
		}
		size, err := strconv.Atoi(ll)
		if err != nil {
			return "", nil, err
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", nil, err
		}
		// consume trailing CRLF
		if _, err := readLine(br); err != nil {
			return "", nil, err
		}
		parts = append(parts, string(buf))
	}
	if len(parts) == 0 {
		return "", nil, errors.New("AOF: empty command")
	}
	return strings.ToUpper(parts[0]), parts[1:], nil
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
