// Package engine wires every subsystem — typed KV store, semantic
// cache, LLM cache, memory store, pub/sub broker, eviction loop, key
// versioning for WATCH, and persistence (AOF + RDB).
package engine

import (
	"bufio"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/blocking"
	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/eviction"
	"github.com/dhiravpatel/neurocache/apps/api/internal/introspect"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/metrics"
	"github.com/dhiravpatel/neurocache/apps/api/internal/persistence"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
	"github.com/dhiravpatel/neurocache/apps/api/internal/scripting"
	"github.com/dhiravpatel/neurocache/apps/api/internal/semcache"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

type Engine struct {
	Cfg      config.Config
	Log      *slog.Logger
	KV       *store.Store
	Semantic *semcache.Store
	LLM      *semcache.Store
	Memory   *memory.Store
	Scorer   eviction.Scorer
	Metrics  *metrics.Metrics
	PubSub   *pubsub.Broker
	ACL      *acl.Manager
	SlowLog  *introspect.SlowLog
	Latency  *introspect.LatencyMonitor
	Clients  *introspect.ClientRegistry
	Scripts  *scripting.Cache
	Blocker  *blocking.Hub

	AOF *persistence.AOF
	RDB *persistence.RDB

	StartedAt time.Time
	CmdCount  atomic.Uint64
	stopCh    chan struct{}

	// lastSave is the unix timestamp of the most recent successful RDB
	// write (manual or scheduled). Seeded from the on-disk file's mtime
	// at boot so LASTSAVE survives restarts.
	lastSave atomic.Int64

	// bgSaveBusy / bgRewriteBusy throttle BGSAVE and BGREWRITEAOF to a
	// single concurrent operation. Redis also rejects re-entrant calls.
	bgSaveBusy    atomic.Bool
	bgRewriteBusy atomic.Bool

	vmu      sync.RWMutex
	versions map[string]uint64
}

func New(cfg config.Config, log *slog.Logger) *Engine {
	aclMgr := acl.NewManager(log)
	if path := acl.ResolvePath(cfg.ACLFile, cfg.DataDir); path != "" {
		if err := aclMgr.LoadFile(path); err != nil {
			log.Warn("acl load failed", "err", err, "path", path)
		}
	}
	if cfg.RequirePass != "" {
		// Legacy "requirepass" — set the default user's password so
		// callers can `AUTH <pass>` without a username.
		aclMgr.SetRequirePass(cfg.RequirePass)
	}

	e := &Engine{
		Cfg:       cfg,
		Log:       log,
		KV:        store.New(),
		Semantic:  semcache.New(cfg.EmbeddingDim, "semantic"),
		LLM:       semcache.New(cfg.EmbeddingDim, "llm"),
		Memory:    memory.New(cfg.EmbeddingDim),
		Scorer:    eviction.NewScorer(cfg.Eviction),
		Metrics:   metrics.New(),
		PubSub:    pubsub.New(64),
		ACL:       aclMgr,
		SlowLog:   introspect.NewSlowLog(cfg.SlowLogMaxLen, time.Duration(cfg.SlowLogThreshold)*time.Microsecond),
		Latency:   introspect.NewLatencyMonitor(cfg.LatencyMaxLen),
		Clients:   introspect.NewClientRegistry(),
		Scripts:   scripting.NewCache(),
		Blocker:   blocking.NewHub(),
		StartedAt: time.Now(),
		stopCh:    make(chan struct{}),
		versions:  map[string]uint64{},
	}
	e.KV.SetNotifier(func(event, key string) {
		e.BumpKey(key)
		if key == "" {
			return
		}
		e.PubSub.Publish("__keyspace__:"+key, event)
		e.PubSub.Publish("__keyevent__:"+event, key)
		// Wake any blocked clients (BLPOP/BRPOP/BLMOVE/BZPOPMIN/BZPOPMAX
		// /XREAD BLOCK). The blocker filters by event below — only writes
		// that produce something a consumer can pop need to fire.
		switch event {
		case "lpush", "rpush", "rpoplpush", "lpushx", "rpushx",
			"zadd", "zincrby",
			"xadd",
			"set", "setnx", "setex", "psetex", "incr", "decr",
			"incrby", "decrby", "incrbyfloat", "append", "setrange":
			e.Blocker.Notify(key)
		case "del", "expired", "flushdb":
			e.Blocker.NotifyAll(key)
		}
	})
	return e
}

// EnablePersistence opens AOF/RDB handles and restores any prior state.
//
// Load-order rule (matches Redis's default): if AOF is enabled, it is
// the sole source of truth — RDB files are ignored on startup, since
// replaying AOF on top of an RDB would double-apply non-idempotent
// commands like XADD. If only RDB is enabled, we load it. The reverse
// (AOF-only) replays every recorded write.
//
// The caller passes a run function that can execute a command against
// the engine without re-appending it to the AOF; only dispatch knows
// how to turn "SET k v EX 10" into the right store calls.
func (e *Engine) EnablePersistence(run func(cmd string, args []string) error) error {
	if !e.Cfg.AOFEnabled && !e.Cfg.RDBEnabled {
		return nil
	}
	dir := e.Cfg.DataDir

	switch {
	case e.Cfg.AOFEnabled:
		aofPath := filepath.Join(dir, "append.aof")
		if err := persistence.Replay(aofPath, run); err != nil {
			e.Log.Warn("aof replay failed", "err", err)
		} else {
			e.Log.Info("aof replayed", "path", aofPath)
		}
		aof, err := persistence.OpenAOF(aofPath, parseFsyncPolicy(e.Cfg.AOFFsync))
		if err != nil {
			return err
		}
		e.AOF = aof
	case e.Cfg.RDBEnabled:
		rdbPath := filepath.Join(dir, "dump.rdb")
		snap, err := persistence.LoadRDB(rdbPath)
		if err != nil {
			e.Log.Warn("rdb load failed", "err", err)
		} else if snap != nil {
			e.KV.Restore(convertFromRDB(snap.Keys))
			e.Log.Info("rdb loaded", "keys", len(snap.Keys), "at", snap.CreatedAt)
			e.lastSave.Store(snap.CreatedAt.Unix())
		}
	}

	// RDB snapshotting is always wired when enabled — it works fine
	// alongside AOF as a periodic full-state backup. The *load* path
	// is the only place where the two modes are mutually exclusive.
	if e.Cfg.RDBEnabled {
		rdbPath := filepath.Join(dir, "dump.rdb")
		interval := time.Duration(e.Cfg.RDBIntervalSec) * time.Second
		rdb, err := persistence.OpenRDB(rdbPath, interval, e.snapshotFn)
		if err != nil {
			return err
		}
		e.RDB = rdb
	}
	return nil
}

func (e *Engine) Start() {
	go e.evictLoop()
	if e.RDB != nil {
		e.RDB.Start()
	}
}

func (e *Engine) Stop() {
	close(e.stopCh)
	e.Metrics.Stop()
	if e.AOF != nil {
		_ = e.AOF.Close()
	}
	if e.RDB != nil {
		e.RDB.Stop()
	}
}

// BumpKey increments the per-key version counter. Called by the store
// notifier on every mutation.
func (e *Engine) BumpKey(key string) {
	if key == "" {
		return
	}
	e.vmu.Lock()
	e.versions[key]++
	e.vmu.Unlock()
}

// KeyVersion reads the current version for a key (0 if never seen).
func (e *Engine) KeyVersion(key string) uint64 {
	e.vmu.RLock()
	defer e.vmu.RUnlock()
	return e.versions[key]
}

// RecordWrite hands a write-path command to the AOF. A no-op if AOF is
// disabled. Called from dispatch after the command executes successfully.
func (e *Engine) RecordWrite(cmd string, args []string) {
	if e.AOF == nil {
		return
	}
	_ = e.AOF.Append(cmd, args)
}

// RewriteAOF dumps the current keyspace back to the AOF synchronously.
// Used by the CLI/replay paths. The RESP-level BGREWRITEAOF command
// calls BGRewriteAOF to avoid blocking the caller.
func (e *Engine) RewriteAOF() error {
	if e.AOF == nil {
		return nil
	}
	return e.AOF.Rewrite(func(w *bufio.Writer) error {
		return writeAOFSnapshot(w, e.KV.Export())
	})
}

// BGRewriteAOF kicks off an AOF rewrite on a background goroutine. It
// returns immediately. Only one rewrite runs at a time — a concurrent
// request returns ErrBgBusy so clients can retry.
func (e *Engine) BGRewriteAOF() error {
	if e.AOF == nil {
		return nil
	}
	if !e.bgRewriteBusy.CompareAndSwap(false, true) {
		return ErrBgBusy
	}
	go func() {
		defer e.bgRewriteBusy.Store(false)
		if err := e.RewriteAOF(); err != nil {
			e.Log.Warn("bgrewriteaof failed", "err", err)
		}
	}()
	return nil
}

// SaveRDB writes a snapshot synchronously and updates the LASTSAVE
// timestamp on success.
func (e *Engine) SaveRDB() error {
	if e.RDB == nil {
		return nil
	}
	if err := e.RDB.SaveNow(); err != nil {
		return err
	}
	e.lastSave.Store(time.Now().Unix())
	return nil
}

// BGSaveRDB runs an RDB snapshot on a background goroutine. Returns
// immediately. Concurrent requests return ErrBgBusy.
func (e *Engine) BGSaveRDB() error {
	if e.RDB == nil {
		return nil
	}
	if !e.bgSaveBusy.CompareAndSwap(false, true) {
		return ErrBgBusy
	}
	go func() {
		defer e.bgSaveBusy.Store(false)
		if err := e.SaveRDB(); err != nil {
			e.Log.Warn("bgsave failed", "err", err)
		}
	}()
	return nil
}

// LastSave returns the unix timestamp of the last successful RDB write,
// 0 if none has happened this process.
func (e *Engine) LastSave() int64 { return e.lastSave.Load() }

// IsBGSaveInProgress / IsBGRewriteInProgress expose the async flags to
// INFO and DEBUG handlers.
func (e *Engine) IsBGSaveInProgress() bool    { return e.bgSaveBusy.Load() }
func (e *Engine) IsBGRewriteInProgress() bool { return e.bgRewriteBusy.Load() }

// ErrBgBusy is returned when BGSAVE/BGREWRITEAOF is already running.
var ErrBgBusy = fmt.Errorf("background save already in progress")

// parseFsyncPolicy maps the config string onto the persistence policy.
func parseFsyncPolicy(s string) persistence.FsyncPolicy {
	switch s {
	case "always":
		return persistence.FsyncAlways
	case "no":
		return persistence.FsyncNo
	default:
		return persistence.FsyncEverySec
	}
}

// snapshotFn is the callback the RDB loop invokes. Convert our typed
// export into the persistence wire format.
func (e *Engine) snapshotFn() persistence.Snapshot {
	keys := e.KV.Export()
	out := make([]persistence.KeySnapshot, 0, len(keys))
	for _, k := range keys {
		ks := persistence.KeySnapshot{Key: k.Key, Type: k.Type, ExpireAt: k.ExpireAt, Str: k.Str, List: k.List, Hash: k.Hash, Set: k.Set}
		for _, zm := range k.ZSet {
			ks.ZSet = append(ks.ZSet, persistence.ZMember{Member: zm.Member, Score: zm.Score})
		}
		for _, se := range k.Stream {
			ks.Stream = append(ks.Stream, persistence.StreamSnapshotEntry{ID: se.ID, Fields: se.Fields})
		}
		out = append(out, ks)
	}
	return persistence.Snapshot{Version: 1, CreatedAt: time.Now(), Keys: out}
}

// convertFromRDB maps the wire format back into the store's type.
func convertFromRDB(in []persistence.KeySnapshot) []store.ExportEntry {
	out := make([]store.ExportEntry, 0, len(in))
	for _, k := range in {
		ent := store.ExportEntry{Key: k.Key, Type: k.Type, ExpireAt: k.ExpireAt, Str: k.Str, List: k.List, Hash: k.Hash, Set: k.Set}
		for _, zm := range k.ZSet {
			ent.ZSet = append(ent.ZSet, store.ExportZMember{Member: zm.Member, Score: zm.Score})
		}
		for _, se := range k.Stream {
			ent.Stream = append(ent.Stream, store.ExportStreamEntry{ID: se.ID, Fields: se.Fields})
		}
		out = append(out, ent)
	}
	return out
}

// writeAOFSnapshot serializes the live keyspace as RESP-format commands
// the engine can replay on startup. This powers BGREWRITEAOF.
func writeAOFSnapshot(w *bufio.Writer, entries []store.ExportEntry) error {
	for _, e := range entries {
		switch e.Type {
		case "string":
			if err := writeAOFCmd(w, "SET", e.Key, e.Str); err != nil {
				return err
			}
		case "list":
			args := append([]string{e.Key}, e.List...)
			if err := writeAOFCmd(w, "RPUSH", args...); err != nil {
				return err
			}
		case "hash":
			args := []string{e.Key}
			for f, v := range e.Hash {
				args = append(args, f, v)
			}
			if err := writeAOFCmd(w, "HSET", args...); err != nil {
				return err
			}
		case "set":
			args := append([]string{e.Key}, e.Set...)
			if err := writeAOFCmd(w, "SADD", args...); err != nil {
				return err
			}
		case "zset":
			args := []string{e.Key}
			for _, zm := range e.ZSet {
				args = append(args, strconv.FormatFloat(zm.Score, 'f', -1, 64), zm.Member)
			}
			if err := writeAOFCmd(w, "ZADD", args...); err != nil {
				return err
			}
		case "stream":
			for _, se := range e.Stream {
				args := append([]string{e.Key, se.ID}, se.Fields...)
				if err := writeAOFCmd(w, "XADD", args...); err != nil {
					return err
				}
			}
		}
		if e.ExpireAt > 0 {
			ms := strconv.FormatInt(e.ExpireAt, 10)
			if err := writeAOFCmd(w, "PEXPIREAT", e.Key, ms); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAOFCmd(w *bufio.Writer, cmd string, args ...string) error {
	if _, err := w.WriteString("*"); err != nil {
		return err
	}
	if _, err := w.WriteString(strconv.Itoa(1 + len(args))); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n$"); err != nil {
		return err
	}
	if _, err := w.WriteString(strconv.Itoa(len(cmd))); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n" + cmd + "\r\n"); err != nil {
		return err
	}
	for _, a := range args {
		if _, err := w.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// evictLoop sweeps when we cross the configured memory cap.
func (e *Engine) evictLoop() {
	if e.Scorer == nil {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	capBytes := int64(e.Cfg.MaxMemoryMB) * 1024 * 1024
	for {
		select {
		case <-e.stopCh:
			return
		case <-t.C:
			used := e.KV.BytesUsed()
			if used <= capBytes {
				continue
			}
			snap := e.KV.Snapshot()
			n := len(snap) / 10
			if n < 1 {
				n = 1
			}
			victims := eviction.PickVictims(snap, e.Scorer, n)
			if removed := e.KV.Evict(victims); removed > 0 {
				e.Log.Info("evicted", "count", removed, "used_bytes", used, "cap_bytes", capBytes)
			}
		}
	}
}

// Info is a snapshot of engine metrics for /api/info.
type Info struct {
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	Commands      uint64  `json:"commands"`
	KV            struct {
		Keys  int   `json:"keys"`
		Bytes int64 `json:"bytes"`
	} `json:"kv"`
	Semantic semcache.Stats `json:"semantic"`
	LLM      semcache.Stats `json:"llm"`
	Memory   struct {
		Entries int `json:"entries"`
		Users   int `json:"users"`
	} `json:"memory"`
	Eviction    string `json:"eviction"`
	Persistence struct {
		AOF                 bool  `json:"aof"`
		RDB                 bool  `json:"rdb"`
		LastSave            int64 `json:"last_save"`
		BGSaveInProgress    bool  `json:"bgsave_in_progress"`
		BGRewriteInProgress bool  `json:"bgrewrite_in_progress"`
	} `json:"persistence"`
	PubSub struct {
		Patterns int `json:"patterns"`
	} `json:"pubsub"`
	Runtime struct {
		Goroutines int    `json:"goroutines"`
		GoVersion  string `json:"go_version"`
		HeapMB     uint64 `json:"heap_mb"`
	} `json:"runtime"`
}

func (e *Engine) Info() Info {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	i := Info{
		Version:       "0.3.0",
		UptimeSeconds: time.Since(e.StartedAt).Seconds(),
		Commands:      e.CmdCount.Load(),
		Eviction:      e.Cfg.Eviction,
	}
	i.KV.Keys = e.KV.Size()
	i.KV.Bytes = e.KV.BytesUsed()
	i.Semantic = e.Semantic.Stats()
	i.LLM = e.LLM.Stats()
	i.Memory.Entries = e.Memory.Size()
	i.Memory.Users = e.Memory.Users()
	i.Persistence.AOF = e.AOF != nil
	i.Persistence.RDB = e.RDB != nil
	i.Persistence.LastSave = e.LastSave()
	i.Persistence.BGSaveInProgress = e.IsBGSaveInProgress()
	i.Persistence.BGRewriteInProgress = e.IsBGRewriteInProgress()
	i.PubSub.Patterns = e.PubSub.NumPat()
	i.Runtime.Goroutines = runtime.NumGoroutine()
	i.Runtime.GoVersion = runtime.Version()
	i.Runtime.HeapMB = m.HeapAlloc / (1024 * 1024)
	return i
}
