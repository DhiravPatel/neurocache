// Package engine wires all subsystems together: typed KV store, semantic
// cache, LLM cache, memory store, pub/sub broker, eviction loop, and a
// small per-key version counter used by the WATCH/EXEC machinery.
package engine

import (
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/eviction"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/metrics"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
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

	StartedAt time.Time
	CmdCount  atomic.Uint64
	stopCh    chan struct{}

	// versions tracks a monotonic counter per key used by WATCH to detect
	// races between a client calling WATCH and running EXEC. Bumped on
	// any mutation via BumpKey / the keyspace notifier.
	vmu      sync.RWMutex
	versions map[string]uint64
}

func New(cfg config.Config, log *slog.Logger) *Engine {
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
		StartedAt: time.Now(),
		stopCh:    make(chan struct{}),
		versions:  map[string]uint64{},
	}
	// Wire keyspace notifications: every mutation fans out to pub/sub
	// (so clients can SUBSCRIBE __keyspace__:key) and bumps the key
	// version so WATCH can spot concurrent writes.
	e.KV.SetNotifier(func(event, key string) {
		e.BumpKey(key)
		if key == "" {
			return
		}
		e.PubSub.Publish("__keyspace__:"+key, event)
		e.PubSub.Publish("__keyevent__:"+event, key)
	})
	return e
}

func (e *Engine) Start() { go e.evictLoop() }

func (e *Engine) Stop() {
	close(e.stopCh)
	e.Metrics.Stop()
}

// BumpKey increments the per-key version counter.
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

// evictLoop sweeps when we cross the memory cap. A periodic scan is fine
// for this scaffold; a production build would make eviction event-driven
// on SET to keep peak memory tighter.
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
	Eviction string `json:"eviction"`
	PubSub   struct {
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
		Version:       "0.2.0",
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
	i.PubSub.Patterns = e.PubSub.NumPat()
	i.Runtime.Goroutines = runtime.NumGoroutine()
	i.Runtime.GoVersion = runtime.Version()
	i.Runtime.HeapMB = m.HeapAlloc / (1024 * 1024)
	return i
}
