// Package metrics collects runtime analytics: a rolling 60-second time
// series of commands/hits/misses, per-command-type counters, and a
// top-K tracker for the hottest keys. Designed to be cheap on the hot
// path (atomics + a background aggregator), and simple enough that the
// web dashboard can render it without any external TSDB.
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	seriesLen      = 60       // 60 buckets = last ~60 seconds at 1Hz
	bucketDuration = time.Second
	topKSize       = 10
	hotKeysCap     = 2048 // soft cap before eviction
)

// Sample is one point in the time series.
type Sample struct {
	T           time.Time `json:"t"`
	Commands    uint64    `json:"commands"`
	SemHits     uint64    `json:"sem_hits"`
	SemMisses   uint64    `json:"sem_misses"`
	LLMHits     uint64    `json:"llm_hits"`
	LLMMisses   uint64    `json:"llm_misses"`
	KVHits      uint64    `json:"kv_hits"`
	KVMisses    uint64    `json:"kv_misses"`
	LatencyP50  float64   `json:"p50_ms"`
	LatencyP95  float64   `json:"p95_ms"`
}

type counters struct {
	commands  atomic.Uint64
	semHits   atomic.Uint64
	semMisses atomic.Uint64
	llmHits   atomic.Uint64
	llmMisses atomic.Uint64
	kvHits    atomic.Uint64
	kvMisses  atomic.Uint64
}

type Metrics struct {
	c counters

	// per-command-type totals (never reset) — map[string]*atomic.Uint64
	cmdTypes sync.Map

	// rolling time series — 60 ring-buffer slots
	mu       sync.RWMutex
	series   []Sample
	lastTick Sample
	latencies []time.Duration // accumulated since last tick

	// hot keys — simple map with periodic eviction of coldest
	hotMu sync.Mutex
	hot   map[string]*hotKey

	// cumulative estimated LLM savings, stored as micro-USD ($1 = 1_000_000).
	// Keeps atomic ops cheap; converted to float at read time in Summary().
	savingsMicroUSD      atomic.Int64
	tokensPerHit         atomic.Int64
	priceMicroUSDPerMTok atomic.Int64 // micro-USD per million tokens (e.g. $10/M = 10_000_000)

	quit chan struct{}
}

type hotKey struct {
	Count    atomic.Uint64
	LastSeen time.Time
}

func New() *Metrics {
	m := &Metrics{
		series: make([]Sample, 0, seriesLen),
		hot:    make(map[string]*hotKey, hotKeysCap),
		quit:   make(chan struct{}),
	}
	// Assumption for LLM savings: 1000 tokens per cached response,
	// $10.00 per million tokens (adjust via env later).
	m.tokensPerHit.Store(1000)
	m.priceMicroUSDPerMTok.Store(10_000_000) // $10 per million tokens
	go m.aggregator()
	return m
}

func (m *Metrics) Stop() { close(m.quit) }

// ─── hot-path recording ───

func (m *Metrics) RecordCommand(name string, latency time.Duration) {
	m.c.commands.Add(1)
	val, _ := m.cmdTypes.LoadOrStore(name, new(atomic.Uint64))
	val.(*atomic.Uint64).Add(1)
	m.mu.Lock()
	m.latencies = append(m.latencies, latency)
	m.mu.Unlock()
}

func (m *Metrics) RecordKVHit(key string, hit bool) {
	if hit {
		m.c.kvHits.Add(1)
		m.bumpHotKey(key)
	} else {
		m.c.kvMisses.Add(1)
	}
}

func (m *Metrics) RecordSemantic(hit bool) {
	if hit {
		m.c.semHits.Add(1)
	} else {
		m.c.semMisses.Add(1)
	}
}

func (m *Metrics) RecordLLM(hit bool) {
	if hit {
		m.c.llmHits.Add(1)
		tokens := m.tokensPerHit.Load()
		priceMicroUSDPerM := m.priceMicroUSDPerMTok.Load()
		// savings (micro-USD) = tokens × (price_μUSD/M) / 1_000_000 tokens/M
		m.savingsMicroUSD.Add(tokens * priceMicroUSDPerM / 1_000_000)
	} else {
		m.c.llmMisses.Add(1)
	}
}

func (m *Metrics) bumpHotKey(key string) {
	m.hotMu.Lock()
	defer m.hotMu.Unlock()
	if h, ok := m.hot[key]; ok {
		h.Count.Add(1)
		h.LastSeen = time.Now()
		return
	}
	if len(m.hot) >= hotKeysCap {
		m.evictColdest()
	}
	m.hot[key] = &hotKey{LastSeen: time.Now()}
	m.hot[key].Count.Store(1)
}

// evictColdest removes the single coldest hot-key entry (approximate).
// Caller holds hotMu.
func (m *Metrics) evictColdest() {
	var evictKey string
	var evictTs time.Time
	for k, v := range m.hot {
		if evictKey == "" || v.LastSeen.Before(evictTs) {
			evictKey = k
			evictTs = v.LastSeen
		}
	}
	if evictKey != "" {
		delete(m.hot, evictKey)
	}
}

// ─── aggregator ───

func (m *Metrics) aggregator() {
	t := time.NewTicker(bucketDuration)
	defer t.Stop()
	for {
		select {
		case <-m.quit:
			return
		case now := <-t.C:
			m.tick(now)
		}
	}
}

func (m *Metrics) tick(now time.Time) {
	cur := Sample{
		T:         now,
		Commands:  m.c.commands.Load(),
		SemHits:   m.c.semHits.Load(),
		SemMisses: m.c.semMisses.Load(),
		LLMHits:   m.c.llmHits.Load(),
		LLMMisses: m.c.llmMisses.Load(),
		KVHits:    m.c.kvHits.Load(),
		KVMisses:  m.c.kvMisses.Load(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// compute deltas
	delta := Sample{
		T:         now,
		Commands:  cur.Commands - m.lastTick.Commands,
		SemHits:   cur.SemHits - m.lastTick.SemHits,
		SemMisses: cur.SemMisses - m.lastTick.SemMisses,
		LLMHits:   cur.LLMHits - m.lastTick.LLMHits,
		LLMMisses: cur.LLMMisses - m.lastTick.LLMMisses,
		KVHits:    cur.KVHits - m.lastTick.KVHits,
		KVMisses:  cur.KVMisses - m.lastTick.KVMisses,
	}
	delta.LatencyP50, delta.LatencyP95 = percentiles(m.latencies)
	m.latencies = m.latencies[:0]

	m.lastTick = cur
	if len(m.series) >= seriesLen {
		m.series = m.series[1:]
	}
	m.series = append(m.series, delta)
}

func percentiles(xs []time.Duration) (p50, p95 float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	sorted := make([]time.Duration, len(xs))
	copy(sorted, xs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx50 := int(float64(len(sorted)) * 0.50)
	idx95 := int(float64(len(sorted)) * 0.95)
	if idx50 >= len(sorted) {
		idx50 = len(sorted) - 1
	}
	if idx95 >= len(sorted) {
		idx95 = len(sorted) - 1
	}
	return float64(sorted[idx50].Microseconds()) / 1000.0,
		float64(sorted[idx95].Microseconds()) / 1000.0
}

// ─── read-side ───

func (m *Metrics) Timeline() []Sample {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Sample, len(m.series))
	copy(out, m.series)
	return out
}

type HotKey struct {
	Key   string `json:"key"`
	Hits  uint64 `json:"hits"`
	LastS int64  `json:"last_seen_unix"`
}

func (m *Metrics) HotKeys(k int) []HotKey {
	if k <= 0 {
		k = topKSize
	}
	m.hotMu.Lock()
	list := make([]HotKey, 0, len(m.hot))
	for key, v := range m.hot {
		list = append(list, HotKey{Key: key, Hits: v.Count.Load(), LastS: v.LastSeen.Unix()})
	}
	m.hotMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Hits > list[j].Hits })
	if len(list) > k {
		list = list[:k]
	}
	return list
}

type CommandCount struct {
	Command string `json:"command"`
	Count   uint64 `json:"count"`
}

func (m *Metrics) CommandBreakdown() []CommandCount {
	var out []CommandCount
	m.cmdTypes.Range(func(k, v any) bool {
		out = append(out, CommandCount{Command: k.(string), Count: v.(*atomic.Uint64).Load()})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

type Summary struct {
	Commands      uint64         `json:"commands"`
	SemHits       uint64         `json:"sem_hits"`
	SemMisses     uint64         `json:"sem_misses"`
	SemHitRate    float64        `json:"sem_hit_rate"`
	LLMHits       uint64         `json:"llm_hits"`
	LLMMisses     uint64         `json:"llm_misses"`
	LLMHitRate    float64        `json:"llm_hit_rate"`
	KVHits        uint64         `json:"kv_hits"`
	KVMisses      uint64         `json:"kv_misses"`
	EstSavingsUSD float64        `json:"estimated_savings_usd"`
	TokensPerHit  int64          `json:"tokens_per_hit"`
	UsdPerMillion float64        `json:"usd_per_million_tokens"`
	Breakdown     []CommandCount `json:"command_breakdown"`
}

func (m *Metrics) Summary() Summary {
	sh := m.c.semHits.Load()
	sm := m.c.semMisses.Load()
	lh := m.c.llmHits.Load()
	lm := m.c.llmMisses.Load()
	s := Summary{
		Commands:     m.c.commands.Load(),
		SemHits:      sh,
		SemMisses:    sm,
		LLMHits:      lh,
		LLMMisses:    lm,
		KVHits:       m.c.kvHits.Load(),
		KVMisses:     m.c.kvMisses.Load(),
		TokensPerHit: m.tokensPerHit.Load(),
	}
	s.UsdPerMillion = float64(m.priceMicroUSDPerMTok.Load()) / 1_000_000.0
	s.EstSavingsUSD = float64(m.savingsMicroUSD.Load()) / 1_000_000.0
	if sh+sm > 0 {
		s.SemHitRate = float64(sh) / float64(sh+sm)
	}
	if lh+lm > 0 {
		s.LLMHitRate = float64(lh) / float64(lh+lm)
	}
	s.Breakdown = m.CommandBreakdown()
	return s
}
