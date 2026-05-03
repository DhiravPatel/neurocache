package aiops

import (
	"sync"
	"time"
)

// Circuits is a distributed circuit-breaker registry. Redis ships none —
// every team rebuilds it in client libraries (Hystrix, resilience4j) or
// gnarly EVAL scripts. Centralising it at the cache layer means one
// shared verdict across every process pointing at the same NeuroCache.
//
// Each service tracks a sliding window of recent outcomes. The breaker
// trips OPEN when the failure ratio over the window exceeds Threshold
// (with at least MinSamples observations to avoid hair-trigger trips on
// the first failure). After Cooldown elapses it transitions to HALFOPEN,
// which lets up to HalfOpenMax probe calls through; if they all succeed
// it returns to CLOSED, otherwise it re-opens.
//
// CHECK is the gate every caller hits before issuing a downstream call.
// RECORD is what they call afterward with the outcome. The two are
// deliberately decoupled: a caller may CHECK, fast-fail because the
// breaker is OPEN, and skip RECORD entirely.
type Circuits struct {
	mu       sync.Mutex
	services map[string]*breaker
}

// CircuitState is one of the three canonical breaker states.
type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

// CircuitConfig is the per-service tunable. Defaults below match the
// safe-but-sensitive starting point Hystrix made famous: 50% failure
// over a 20-call window with 30s cooldown.
type CircuitConfig struct {
	Threshold   float64       // failure ratio in [0,1] that trips OPEN
	WindowSize  int           // number of recent outcomes considered
	MinSamples  int           // min observations before threshold can fire
	Cooldown    time.Duration // OPEN → HALFOPEN delay
	HalfOpenMax int           // probe calls allowed in HALFOPEN
}

// breaker is the per-service state. Outcomes are a ring of bytes (1=ok,
// 0=fail) so the failure ratio is an O(WindowSize) walk — fine for the
// 20–100-sample windows production usage settles on.
type breaker struct {
	cfg          CircuitConfig
	state        CircuitState
	outcomes     []byte // ring of last WindowSize results
	cursor       int
	filled       int
	openedAt     time.Time
	probeAllowed int   // remaining HALFOPEN probes
	probesInFly  int   // currently issued probes (CHECKed but not yet RECORDed)
	probesPassed int   // probes that came back OK in the current HALFOPEN
	totalRequests int64
	totalFailures int64
	totalRejected int64 // CHECK calls denied because breaker was OPEN
	tripCount    int64
	lastReason   string
	lastTrip     time.Time
}

func defaultCircuitConfig() CircuitConfig {
	return CircuitConfig{
		Threshold:   0.5,
		WindowSize:  20,
		MinSamples:  10,
		Cooldown:    30 * time.Second,
		HalfOpenMax: 3,
	}
}

// NewCircuits returns an empty registry.
func NewCircuits() *Circuits {
	return &Circuits{services: map[string]*breaker{}}
}

// getOrCreate returns the breaker for service, creating it with defaults
// if absent. Caller must hold c.mu.
func (c *Circuits) getOrCreate(service string) *breaker {
	b, ok := c.services[service]
	if ok {
		return b
	}
	cfg := defaultCircuitConfig()
	b = &breaker{
		cfg:      cfg,
		state:    CircuitClosed,
		outcomes: make([]byte, cfg.WindowSize),
	}
	c.services[service] = b
	return b
}

// Configure mutates the per-service tunables. Zero values mean "leave
// alone"; callers can incrementally tune one knob without specifying the
// others. Resizing the window resets the outcome buffer — the prior
// samples were observed under different semantics and would skew the
// ratio.
func (c *Circuits) Configure(service string, cfg CircuitConfig) CircuitConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.getOrCreate(service)
	if cfg.Threshold > 0 {
		if cfg.Threshold > 1 {
			cfg.Threshold = 1
		}
		b.cfg.Threshold = cfg.Threshold
	}
	if cfg.WindowSize > 0 && cfg.WindowSize != b.cfg.WindowSize {
		b.cfg.WindowSize = cfg.WindowSize
		b.outcomes = make([]byte, cfg.WindowSize)
		b.cursor = 0
		b.filled = 0
	}
	if cfg.MinSamples > 0 {
		b.cfg.MinSamples = cfg.MinSamples
	}
	if cfg.Cooldown > 0 {
		b.cfg.Cooldown = cfg.Cooldown
	}
	if cfg.HalfOpenMax > 0 {
		b.cfg.HalfOpenMax = cfg.HalfOpenMax
	}
	return b.cfg
}

// Check returns true if a downstream call is permitted right now. It
// also performs the time-driven OPEN→HALFOPEN transition and reserves
// a probe slot in HALFOPEN. Callers MUST follow a successful Check with
// a matching Record so probe accounting stays consistent.
func (c *Circuits) Check(service string) (allowed bool, state CircuitState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.getOrCreate(service)
	c.transition(b)
	switch b.state {
	case CircuitClosed:
		return true, b.state
	case CircuitHalfOpen:
		if b.probeAllowed <= 0 {
			b.totalRejected++
			return false, b.state
		}
		b.probeAllowed--
		b.probesInFly++
		return true, b.state
	default: // OPEN
		b.totalRejected++
		return false, b.state
	}
}

// Record reports the outcome of a downstream call. ok=true means
// success; ok=false means failure. Calling Record without a preceding
// Check is supported but won't reverse a previous probe reservation —
// it's only meaningful for callers that gate themselves externally.
func (c *Circuits) Record(service string, ok bool) CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.getOrCreate(service)
	c.transition(b)
	b.totalRequests++
	if !ok {
		b.totalFailures++
	}
	switch b.state {
	case CircuitHalfOpen:
		if b.probesInFly > 0 {
			b.probesInFly--
		}
		if ok {
			b.probesPassed++
			// Promote back to CLOSED when we've seen HalfOpenMax
			// successive probes succeed without a failure.
			if b.probesPassed >= b.cfg.HalfOpenMax {
				b.state = CircuitClosed
				b.cursor = 0
				b.filled = 0
				b.probeAllowed = 0
				b.probesPassed = 0
				b.probesInFly = 0
			}
		} else {
			// Any failure during HALFOPEN re-opens the breaker.
			b.openedAt = time.Now()
			b.state = CircuitOpen
			b.tripCount++
			b.lastReason = "halfopen_probe_failed"
			b.lastTrip = b.openedAt
			b.probeAllowed = 0
			b.probesPassed = 0
			b.probesInFly = 0
		}
	case CircuitClosed:
		// Push outcome onto the ring.
		val := byte(0)
		if ok {
			val = 1
		}
		b.outcomes[b.cursor] = val
		b.cursor = (b.cursor + 1) % b.cfg.WindowSize
		if b.filled < b.cfg.WindowSize {
			b.filled++
		}
		// Evaluate trip condition.
		if b.filled >= b.cfg.MinSamples {
			fails := 0
			for i := 0; i < b.filled; i++ {
				if b.outcomes[i] == 0 {
					fails++
				}
			}
			ratio := float64(fails) / float64(b.filled)
			if ratio >= b.cfg.Threshold {
				b.openedAt = time.Now()
				b.state = CircuitOpen
				b.tripCount++
				b.lastReason = "threshold_exceeded"
				b.lastTrip = b.openedAt
			}
		}
	}
	return b.state
}

// Trip forces the breaker OPEN. Useful for operator-initiated drains
// (e.g., taking a flaky downstream out of rotation pre-emptively).
func (c *Circuits) Trip(service, reason string) CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.getOrCreate(service)
	b.state = CircuitOpen
	b.openedAt = time.Now()
	b.tripCount++
	if reason == "" {
		reason = "manual_trip"
	}
	b.lastReason = reason
	b.lastTrip = b.openedAt
	b.probeAllowed = 0
	b.probesPassed = 0
	b.probesInFly = 0
	return b.state
}

// Reset clears the breaker back to CLOSED with empty history. Use
// after a downstream is known-good (e.g., post-deploy health check).
func (c *Circuits) Reset(service string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.services[service]
	if !ok {
		return false
	}
	b.state = CircuitClosed
	b.cursor = 0
	b.filled = 0
	b.probeAllowed = 0
	b.probesPassed = 0
	b.probesInFly = 0
	for i := range b.outcomes {
		b.outcomes[i] = 0
	}
	return true
}

// Forget drops a service entirely. Returns true if it existed.
func (c *Circuits) Forget(service string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.services[service]
	delete(c.services, service)
	return ok
}

// CircuitSnapshot is the outward-facing view returned by State / List.
type CircuitSnapshot struct {
	Service       string        `json:"service"`
	State         CircuitState  `json:"state"`
	Threshold     float64       `json:"threshold"`
	WindowSize    int           `json:"window_size"`
	MinSamples    int           `json:"min_samples"`
	Cooldown      time.Duration `json:"cooldown"`
	HalfOpenMax   int           `json:"half_open_max"`
	FailureRate   float64       `json:"failure_rate"`
	Filled        int           `json:"filled"`
	OpenedAt      time.Time     `json:"opened_at,omitempty"`
	CooldownLeft  time.Duration `json:"cooldown_left,omitempty"`
	ProbeAllowed  int           `json:"probe_allowed"`
	TotalRequests int64         `json:"total_requests"`
	TotalFailures int64         `json:"total_failures"`
	TotalRejected int64         `json:"total_rejected"`
	TripCount     int64         `json:"trip_count"`
	LastReason    string        `json:"last_reason,omitempty"`
	LastTrip      time.Time     `json:"last_trip,omitempty"`
}

// State returns a snapshot of one service. Returns ok=false if the
// service has never been observed.
func (c *Circuits) State(service string) (CircuitSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.services[service]
	if !ok {
		return CircuitSnapshot{}, false
	}
	c.transition(b)
	return c.snapshot(service, b), true
}

// List returns every known service in lexicographic order is left to
// the caller — we walk the map.
func (c *Circuits) List() []CircuitSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CircuitSnapshot, 0, len(c.services))
	for name, b := range c.services {
		c.transition(b)
		out = append(out, c.snapshot(name, b))
	}
	return out
}

// CircuitStats is an aggregate over the whole registry.
type CircuitStats struct {
	Services      int   `json:"services"`
	Open          int   `json:"open"`
	HalfOpen      int   `json:"half_open"`
	Closed        int   `json:"closed"`
	TotalRequests int64 `json:"total_requests"`
	TotalFailures int64 `json:"total_failures"`
	TotalRejected int64 `json:"total_rejected"`
}

// Stats rolls up every service.
func (c *Circuits) Stats() CircuitStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := CircuitStats{Services: len(c.services)}
	for _, b := range c.services {
		c.transition(b)
		switch b.state {
		case CircuitOpen:
			st.Open++
		case CircuitHalfOpen:
			st.HalfOpen++
		default:
			st.Closed++
		}
		st.TotalRequests += b.totalRequests
		st.TotalFailures += b.totalFailures
		st.TotalRejected += b.totalRejected
	}
	return st
}

// transition runs the time-driven OPEN→HALFOPEN promotion. Caller
// must hold c.mu. Pulls the current time once so two adjacent calls
// (Check + Record) see the same wall clock.
func (c *Circuits) transition(b *breaker) {
	if b.state != CircuitOpen {
		return
	}
	if time.Since(b.openedAt) < b.cfg.Cooldown {
		return
	}
	b.state = CircuitHalfOpen
	b.probeAllowed = b.cfg.HalfOpenMax
	b.probesPassed = 0
	b.probesInFly = 0
}

// snapshot builds a CircuitSnapshot. Caller holds c.mu.
func (c *Circuits) snapshot(name string, b *breaker) CircuitSnapshot {
	fails := 0
	for i := 0; i < b.filled; i++ {
		if b.outcomes[i] == 0 {
			fails++
		}
	}
	rate := 0.0
	if b.filled > 0 {
		rate = float64(fails) / float64(b.filled)
	}
	cooldownLeft := time.Duration(0)
	if b.state == CircuitOpen {
		left := b.cfg.Cooldown - time.Since(b.openedAt)
		if left > 0 {
			cooldownLeft = left
		}
	}
	return CircuitSnapshot{
		Service:       name,
		State:         b.state,
		Threshold:     b.cfg.Threshold,
		WindowSize:    b.cfg.WindowSize,
		MinSamples:    b.cfg.MinSamples,
		Cooldown:      b.cfg.Cooldown,
		HalfOpenMax:   b.cfg.HalfOpenMax,
		FailureRate:   rate,
		Filled:        b.filled,
		OpenedAt:      b.openedAt,
		CooldownLeft:  cooldownLeft,
		ProbeAllowed:  b.probeAllowed,
		TotalRequests: b.totalRequests,
		TotalFailures: b.totalFailures,
		TotalRejected: b.totalRejected,
		TripCount:     b.tripCount,
		LastReason:    b.lastReason,
		LastTrip:      b.lastTrip,
	}
}
