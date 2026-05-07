package aiops

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Observe is a Prometheus-compatible metrics surface. The engine
// already exposes `/api/metrics/*` JSON endpoints; OBSERVE exposes a
// scrapeable `/metrics` endpoint in the standard text-exposition
// format so any Prometheus / VictoriaMetrics / Mimir scraper picks
// it up without a separate exporter sidecar.
//
// We register baseline gauges/counters automatically (process,
// cache hit rate, command counts, eviction rate, replication offset)
// and let user code register custom metrics via Counter()/Gauge().
type Observe struct {
	mu        sync.RWMutex
	counters  map[string]*atomic.Int64
	gauges    map[string]*atomic.Int64        // we store gauges as int64 nanoseconds-precision floats × 1000
	labels    map[string]map[string]string    // metric → label set
	helpText  map[string]string
	startedAt time.Time
}

// NewObserve returns an empty exporter. The engine constructs one and
// registers its baseline metrics during boot.
func NewObserve() *Observe {
	o := &Observe{
		counters:  map[string]*atomic.Int64{},
		gauges:    map[string]*atomic.Int64{},
		labels:    map[string]map[string]string{},
		helpText:  map[string]string{},
		startedAt: time.Now(),
	}
	o.RegisterCounter("neurocache_observe_render_count", "Number of times /metrics has been scraped.", nil)
	return o
}

// RegisterCounter declares a counter ahead of time so it appears in
// the export even when never incremented. Optional labels are part of
// the metric name (Prometheus convention is to use real labels in the
// line, but we keep the simple name+labelmap form here).
func (o *Observe) RegisterCounter(name, help string, labels map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.counters[name]; !ok {
		o.counters[name] = &atomic.Int64{}
	}
	o.helpText[name] = help
	if labels != nil {
		o.labels[name] = labels
	}
}

// RegisterGauge is the gauge equivalent of RegisterCounter.
func (o *Observe) RegisterGauge(name, help string, labels map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.gauges[name]; !ok {
		o.gauges[name] = &atomic.Int64{}
	}
	o.helpText[name] = help
	if labels != nil {
		o.labels[name] = labels
	}
}

// Inc bumps a counter (creating it if missing).
func (o *Observe) Inc(name string, delta int64) {
	o.mu.RLock()
	c, ok := o.counters[name]
	o.mu.RUnlock()
	if !ok {
		o.mu.Lock()
		if c, ok = o.counters[name]; !ok {
			c = &atomic.Int64{}
			o.counters[name] = c
		}
		o.mu.Unlock()
	}
	c.Add(delta)
}

// SetGauge writes a gauge value (creating it if missing). Values are
// multiplied by 1000 internally so we can carry millisecond-precision
// floats in an int64 atomic without losing precision on common
// metrics (latencies, hit rates).
func (o *Observe) SetGauge(name string, v float64) {
	o.mu.RLock()
	g, ok := o.gauges[name]
	o.mu.RUnlock()
	if !ok {
		o.mu.Lock()
		if g, ok = o.gauges[name]; !ok {
			g = &atomic.Int64{}
			o.gauges[name] = g
		}
		o.mu.Unlock()
	}
	g.Store(int64(v * 1000))
}

// Render produces the Prometheus text-exposition format. Caller writes
// the output as `Content-Type: text/plain; version=0.0.4`.
func (o *Observe) Render() string {
	// Refresh "live" baselines that change continuously — process
	// uptime, GC stats, goroutine count, memstats. These are computed
	// at scrape time so the exporter doesn't have to maintain a
	// background sampler.
	o.refreshRuntimeMetrics()

	o.mu.RLock()
	defer o.mu.RUnlock()
	var sb strings.Builder
	o.Inc("neurocache_observe_render_count", 1)

	render := func(name, kind string, value float64) {
		if help, ok := o.helpText[name]; ok && help != "" {
			fmt.Fprintf(&sb, "# HELP %s %s\n", name, help)
		}
		fmt.Fprintf(&sb, "# TYPE %s %s\n", name, kind)
		labels := o.labels[name]
		if len(labels) == 0 {
			fmt.Fprintf(&sb, "%s %g\n", name, value)
			return
		}
		fmt.Fprintf(&sb, "%s{", name)
		first := true
		for k, v := range labels {
			if !first {
				sb.WriteByte(',')
			}
			first = false
			fmt.Fprintf(&sb, "%s=%q", k, v)
		}
		fmt.Fprintf(&sb, "} %g\n", value)
	}

	// Counters
	names := make([]string, 0, len(o.counters))
	for k := range o.counters {
		names = append(names, k)
	}
	sortStrings(names)
	for _, k := range names {
		render(k, "counter", float64(o.counters[k].Load()))
	}

	// Gauges (divide by 1000 to undo the storage scaling)
	names = names[:0]
	for k := range o.gauges {
		names = append(names, k)
	}
	sortStrings(names)
	for _, k := range names {
		render(k, "gauge", float64(o.gauges[k].Load())/1000.0)
	}
	return sb.String()
}

// refreshRuntimeMetrics computes Go-runtime gauges at scrape time.
// Cheap — runtime.ReadMemStats is the only non-trivial bit.
func (o *Observe) refreshRuntimeMetrics() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	o.SetGauge("neurocache_uptime_seconds", time.Since(o.startedAt).Seconds())
	o.SetGauge("neurocache_goroutines", float64(runtime.NumGoroutine()))
	o.SetGauge("neurocache_memstats_heap_alloc_bytes", float64(ms.HeapAlloc))
	o.SetGauge("neurocache_memstats_heap_sys_bytes", float64(ms.HeapSys))
	o.SetGauge("neurocache_memstats_heap_objects", float64(ms.HeapObjects))
	o.SetGauge("neurocache_memstats_next_gc_bytes", float64(ms.NextGC))
	o.SetGauge("neurocache_memstats_num_gc", float64(ms.NumGC))
	o.SetGauge("neurocache_memstats_pause_ns_total", float64(ms.PauseTotalNs))
}

func sortStrings(in []string) {
	// 25-line insertion sort is plenty — typical metric counts are
	// in the tens, not millions.
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j-1] > in[j]; j-- {
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
}
