package introspect

import (
	"sync"
	"sync/atomic"

	"github.com/dhiravpatel/neurocache/apps/api/internal/probstruct"
)

// HotKeys is the runtime top-K key access tracker. It sits behind the
// engine's keyspace notifier hook: every write event lands here, and
// the tracker downsamples to keep the per-op overhead negligible.
//
// Why a tracker instead of OBJECT FREQ scans:
//
//   - OBJECT FREQ requires LFU eviction (allkeys-lfu / volatile-lfu);
//     callers running LRU or noeviction get nothing.
//   - The classical `redis-cli --hotkeys` approach SCANs every key,
//     which is O(n) and hostile to a busy server.
//   - HeavyKeeper holds the working set in a few KB and answers
//     HOTKEYS in O(K log K) without touching the keyspace.
//
// Sample rate: 1 in `sampleEvery` events is recorded. With the default
// 1, every event is recorded — toggle higher for hot workloads where
// the tracker shouldn't grow into the steady-state CPU budget.
//
// Concurrency: the underlying HeavyKeeper has its own mutex, and the
// sample-counter is atomic, so HotKeys is safe for the engine's
// per-event hook to call without extra locking.
type HotKeys struct {
	enabled atomic.Bool
	hk      atomic.Pointer[probstruct.HeavyKeeper]

	// sampleEvery is the 1-in-N sampling factor. 1 = record every
	// event, 100 = record 1% of events. Atomically swapped via
	// SetSampleRate so changes are visible immediately.
	sampleEvery atomic.Uint64

	// counter monotonically increments with every observed event.
	// We sample when counter % sampleEvery == 0.
	counter atomic.Uint64

	// thresholdMu protects the optional minimum-count gate used by
	// HOTKEYS THRESHOLD. Reads are cheap and on the hot path so we
	// snapshot via atomic.Uint64.
	threshold atomic.Uint64

	// k is the configured top-K width. Stored here so SetK can
	// rebuild the underlying HeavyKeeper without dropping the engine
	// pointer.
	configMu sync.Mutex
	k        int
	width    int
	depth    int
	decay    float64
}

// HotKeysOptions controls the initial HeavyKeeper geometry. Zero
// fields fall back to sensible defaults: K=128, width=512, depth=4,
// decay=0.9, sample=1.
type HotKeysOptions struct {
	K           int
	Width       int
	Depth       int
	Decay       float64
	SampleEvery uint64
	Threshold   uint64
}

// NewHotKeys allocates a fresh tracker. The tracker starts disabled
// — the engine flips it on after wiring the notifier hook so events
// before bootstrap don't blow up the counter.
func NewHotKeys(opts HotKeysOptions) *HotKeys {
	if opts.K <= 0 {
		opts.K = 128
	}
	if opts.Width <= 0 {
		opts.Width = 512
	}
	if opts.Depth <= 0 {
		opts.Depth = 4
	}
	if opts.Decay <= 0 || opts.Decay >= 1 {
		opts.Decay = 0.9
	}
	if opts.SampleEvery == 0 {
		opts.SampleEvery = 1
	}
	h := &HotKeys{
		k:     opts.K,
		width: opts.Width,
		depth: opts.Depth,
		decay: opts.Decay,
	}
	h.hk.Store(probstruct.New(opts.K, opts.Width, opts.Depth, opts.Decay))
	h.sampleEvery.Store(opts.SampleEvery)
	h.threshold.Store(opts.Threshold)
	h.enabled.Store(true)
	return h
}

// Record observes a single keyspace event. Cheap by design — when the
// tracker is disabled or the sample roll loses, the function returns
// immediately. Called from the engine's notifier on every keyspace
// mutation, so every branch matters.
func (h *HotKeys) Record(key string) {
	if !h.enabled.Load() || key == "" {
		return
	}
	rate := h.sampleEvery.Load()
	if rate == 0 {
		return
	}
	if h.counter.Add(1)%rate != 0 {
		return
	}
	if hk := h.hk.Load(); hk != nil {
		hk.Add(key)
	}
}

// Enabled returns whether the tracker is recording events.
func (h *HotKeys) Enabled() bool { return h.enabled.Load() }

// SetEnabled toggles the tracker. Disabling does NOT reset the
// existing top-K — operators can still read the last snapshot.
func (h *HotKeys) SetEnabled(on bool) { h.enabled.Store(on) }

// SetSampleRate adjusts the 1-in-N downsampling factor. Pass 1 to
// record every event, larger values to thin the stream. A value of
// zero disables the tracker (functionally equivalent to SetEnabled
// false but preserves the on/off state for callers that want it
// snapshot-able via Stats).
func (h *HotKeys) SetSampleRate(every uint64) {
	if every == 0 {
		h.enabled.Store(false)
		h.sampleEvery.Store(1)
		return
	}
	h.sampleEvery.Store(every)
	h.enabled.Store(true)
}

// SampleRate returns the current 1-in-N downsampling factor.
func (h *HotKeys) SampleRate() uint64 { return h.sampleEvery.Load() }

// SetThreshold sets the minimum count a key must reach before it is
// included in HOTKEYS / Top output. 0 disables the gate.
func (h *HotKeys) SetThreshold(t uint64) { h.threshold.Store(t) }

// Threshold returns the current minimum-count gate.
func (h *HotKeys) Threshold() uint64 { return h.threshold.Load() }

// SetK rebuilds the HeavyKeeper with a new K. Counters reset — the
// caller is essentially asking for a fresh window with a different
// width, so dropping the existing snapshot is the only honest answer.
func (h *HotKeys) SetK(k int) {
	if k <= 0 {
		return
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()
	h.k = k
	h.hk.Store(probstruct.New(k, h.width, h.depth, h.decay))
	h.counter.Store(0)
}

// Reset clears every counter while preserving the configuration.
func (h *HotKeys) Reset() {
	if hk := h.hk.Load(); hk != nil {
		hk.Reset()
	}
	h.counter.Store(0)
}

// Top returns the top-N hot keys, descending by count, filtered by the
// current threshold. n ≤ 0 returns the full heap.
func (h *HotKeys) Top(n int) []HotKey {
	hk := h.hk.Load()
	if hk == nil {
		return nil
	}
	rows := hk.Top(0)
	thresh := h.threshold.Load()
	out := make([]HotKey, 0, len(rows))
	for _, r := range rows {
		if r.Count < thresh {
			continue
		}
		out = append(out, HotKey{Key: r.Item, Count: r.Count})
	}
	if n > 0 && n < len(out) {
		out = out[:n]
	}
	return out
}

// Stats reports the tracker's configuration plus live observation
// counts. Surfaced via HOTKEYS STATS.
type HotKeysStats struct {
	Enabled      bool
	K            int
	Width        int
	Depth        int
	Decay        float64
	SampleEvery  uint64
	Threshold    uint64
	Tracked      int    // current heap occupancy
	Observations uint64 // cumulative recorded events (post-sampling)
	Events       uint64 // cumulative Record() invocations (pre-sampling)
	BytesApprox  int64
}

// Stats returns a snapshot of the tracker state.
func (h *HotKeys) Stats() HotKeysStats {
	hk := h.hk.Load()
	s := HotKeysStats{
		Enabled:     h.enabled.Load(),
		SampleEvery: h.sampleEvery.Load(),
		Threshold:   h.threshold.Load(),
		Events:      h.counter.Load(),
	}
	if hk != nil {
		hkStats := hk.Stats()
		s.K = hkStats.K
		s.Width = hkStats.Width
		s.Depth = hkStats.Depth
		s.Decay = hkStats.Decay
		s.Tracked = hkStats.Tracked
		s.Observations = hkStats.Observations
		s.BytesApprox = hkStats.BytesApprox
	}
	return s
}

// HotKey pairs a key with its estimated frequency.
type HotKey struct {
	Key   string
	Count uint64
}
