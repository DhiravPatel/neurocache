package engine

import (
	"sync/atomic"
	"time"
)

// FastClock is a process-wide cached monotonic clock. A background
// goroutine updates `nowNanos` every tickInterval; readers do a single
// atomic.Load instead of paying the ~30 ns vDSO cost of `time.Now()`.
//
// Why this matters: every dispatched RESP command calls `time.Now()`
// twice (once for `start`, once via `time.Since`). At 200k cmds/sec
// that's 12 ms/sec of pure clock overhead per CPU. Caching brings the
// per-read cost to ~1 ns and trades a small amount of precision (we
// resolve to ~tickInterval, currently 100 µs) for a few percent of
// throughput.
//
// Code that needs ns-precision time (slowlog timestamps, AOF entries)
// keeps using time.Now(). Code that just wants a cheap monotonic
// "now" for latency math reads FastClock.
type FastClock struct {
	// startWall is the wall-clock at process start. Combined with
	// `nowNanos` (a monotonic delta) this gives us a stable Time
	// reproduction without re-reading the wall clock per tick.
	startWall  time.Time
	startMono  int64
	nowNanos   atomic.Int64 // monotonic ns since process start
	stop       chan struct{}
}

// tickInterval is the resolution of the cached clock. 100 µs is well
// below any threshold we actually act on (slowlog default is 1 ms,
// our latency-monitor default is 1 ms) so the precision loss is
// invisible to operators.
const fastClockTick = 100 * time.Microsecond

// NewFastClock returns a started clock. Always paired with Stop().
// The first read returns startup time + tickInterval — there is no
// "uninitialized" value to guard against.
func NewFastClock() *FastClock {
	c := &FastClock{
		startWall: time.Now(),
		startMono: nowMono(),
		stop:      make(chan struct{}),
	}
	c.nowNanos.Store(0)
	go c.loop()
	return c
}

// Stop halts the background updater. After Stop, Now() and Since()
// return the last tick's value.
func (c *FastClock) Stop() {
	if c == nil {
		return
	}
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
}

func (c *FastClock) loop() {
	t := time.NewTicker(fastClockTick)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.nowNanos.Store(nowMono() - c.startMono)
		}
	}
}

// NowNanos returns the cached monotonic-ns since process start. Use
// this for latency arithmetic; it is fast and monotonic but only
// updates every fastClockTick.
func (c *FastClock) NowNanos() int64 {
	if c == nil {
		return 0
	}
	return c.nowNanos.Load()
}

// Since returns the duration since `t` (which must be a value from a
// prior NowNanos call). Equivalent to time.Since for cached-clock
// callers.
func (c *FastClock) Since(t int64) time.Duration {
	return time.Duration(c.NowNanos() - t)
}

// Now returns a wall-clock time approximated from the cached
// monotonic delta. Cheaper than time.Now() but only resolves to
// fastClockTick precision.
func (c *FastClock) Now() time.Time {
	if c == nil {
		return time.Now()
	}
	return c.startWall.Add(time.Duration(c.NowNanos()))
}
