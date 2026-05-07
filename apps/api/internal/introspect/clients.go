package introspect

import (
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ClientRegistry tracks every connected RESP client so CLIENT
// LIST/KILL/PAUSE/REPLY can act on them. Concurrent-safe.
type ClientRegistry struct {
	nextID atomic.Uint64
	mu     sync.Mutex
	by     map[uint64]*ClientInfo

	// pauseUntil is the wall-clock until which all clients should
	// reject new commands (CLIENT PAUSE). Zero = no pause.
	pauseUntil time.Time
	pauseMu    sync.RWMutex

	// pauseActive is an atomic mirror of "pauseUntil != zero". The
	// hot path (every dispatched command) reads this without
	// touching pauseMu — pauses are rare enough that the steady
	// state shouldn't pay the RWMutex cost.
	pauseActive atomic.Bool
}

// ClientInfo is one connected session's metadata.
type ClientInfo struct {
	ID         uint64
	Addr       string
	Name       string
	Username   string
	ConnectedAt time.Time
	LastCmdAt  time.Time
	LastCmd    string

	// touchSeq is incremented on every dispatched command. We only
	// re-read the wall clock every (1<<touchSampleShift) calls,
	// trading idle-time precision (off by up to ~32 commands) for a
	// significant cost reduction in the hot path. The CLIENT LIST
	// output rounds idle to whole seconds anyway.
	touchSeq atomic.Uint64

	// Reply mode: "on" (default), "off" (silent), "skip" (skip next).
	ReplyMode  string
	NoEvict    bool
	// NoTouch suppresses LastRead / hit-counter updates for this
	// connection's reads. Operators set it before scrubbing the
	// keyspace so the audit doesn't churn LRU/LFU statistics.
	// Mirrors Redis 7.2's CLIENT NO-TOUCH.
	NoTouch    bool

	// LibName / LibVer are populated by CLIENT SETINFO (Valkey 7.2).
	// Drivers send their identity here so operators can attribute load
	// to specific applications via CLIENT LIST / INFO.
	LibName string
	LibVer  string
}

// NewClientRegistry returns an empty registry.
func NewClientRegistry() *ClientRegistry { return &ClientRegistry{by: map[uint64]*ClientInfo{}} }

// Register adds a client and returns its assigned ID.
func (r *ClientRegistry) Register(addr string) *ClientInfo {
	r.nextID.Add(1)
	c := &ClientInfo{
		ID: r.nextID.Load(), Addr: addr,
		ConnectedAt: time.Now(), LastCmdAt: time.Now(),
		ReplyMode: "on",
	}
	r.mu.Lock()
	r.by[c.ID] = c
	r.mu.Unlock()
	return c
}

// Forget removes a client (called on disconnect).
func (r *ClientRegistry) Forget(id uint64) {
	r.mu.Lock()
	delete(r.by, id)
	r.mu.Unlock()
}

// touchSampleShift controls how often TouchSampled re-reads the wall
// clock. 5 → 1 in 32. CLIENT LIST's `idle=` output rounds to whole
// seconds, so even at 200k cmds/sec the LastCmdAt is at most 160 µs
// stale — irrelevant to operators.
const touchSampleShift = 5

// Touch records that the client just executed cmd. Cheap; called from
// the dispatch hot path so we keep it lock-free per client.
func (r *ClientRegistry) Touch(c *ClientInfo, cmd string) {
	if c == nil {
		return
	}
	c.LastCmdAt = time.Now()
	c.LastCmd = cmd
}

// TouchSampled is the hot-path variant: it always updates LastCmd
// (cheap — string assignment) but only re-reads the wall clock every
// (1<<touchSampleShift) calls. Eliminates one of the two time.Now()
// calls per dispatched command on the steady-state path.
func (r *ClientRegistry) TouchSampled(c *ClientInfo, cmd string) {
	if c == nil {
		return
	}
	c.LastCmd = cmd
	if c.touchSeq.Add(1)&((1<<touchSampleShift)-1) == 0 {
		c.LastCmdAt = time.Now()
	}
}

// List returns a snapshot of all clients (sorted by ID for stable output).
func (r *ClientRegistry) List() []ClientInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]uint64, 0, len(r.by))
	for id := range r.by {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]ClientInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, *r.by[id])
	}
	return out
}

// Kill removes a client by ID. Returns whether it existed; the caller
// is responsible for closing the underlying conn.
func (r *ClientRegistry) Kill(id uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.by[id]
	if ok {
		delete(r.by, id)
	}
	return ok
}

// Get returns the client info pointer (callers must treat it as read-only).
func (r *ClientRegistry) Get(id uint64) *ClientInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.by[id]
}

// Pause blocks new commands for the given duration on every client.
// Mirrors CLIENT PAUSE semantics. Setting d == 0 clears the pause.
func (r *ClientRegistry) Pause(d time.Duration) {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	if d <= 0 {
		r.pauseUntil = time.Time{}
		r.pauseActive.Store(false)
		return
	}
	r.pauseUntil = time.Now().Add(d)
	r.pauseActive.Store(true)
}

// PauseRemaining returns 0 (no pause) or the time-until-resume.
//
// Hot path: the atomic.Bool fast-path returns 0 without touching
// pauseMu when no pause is configured (the steady state). When a
// pause IS active the slow path reads the wall-clock time and may
// also clear the atomic mirror once the deadline has passed, so
// later callers also skip the lock.
func (r *ClientRegistry) PauseRemaining() time.Duration {
	if !r.pauseActive.Load() {
		return 0
	}
	r.pauseMu.RLock()
	defer r.pauseMu.RUnlock()
	if r.pauseUntil.IsZero() {
		return 0
	}
	rem := time.Until(r.pauseUntil)
	if rem <= 0 {
		// Deadline passed — clear the mirror so future callers
		// short-circuit. Safe under RLock because Store is atomic.
		r.pauseActive.Store(false)
		return 0
	}
	return rem
}

// FormatLine renders one CLIENT LIST line in the canonical Redis shape.
// (Only fields useful to operators; we omit obj-tracking, multi-state,
// and other internals not modelled here.)
func (c *ClientInfo) FormatLine() string {
	var sb stringBuf
	sb.add("id=" + strconv.FormatUint(c.ID, 10))
	sb.add("addr=" + c.Addr)
	if c.Name != "" {
		sb.add("name=" + c.Name)
	}
	sb.add("user=" + c.Username)
	sb.add("age=" + strconv.FormatInt(int64(time.Since(c.ConnectedAt).Seconds()), 10))
	sb.add("idle=" + strconv.FormatInt(int64(time.Since(c.LastCmdAt).Seconds()), 10))
	sb.add("cmd=" + c.LastCmd)
	sb.add("reply=" + c.ReplyMode)
	if c.NoEvict {
		sb.add("no-evict=1")
	}
	if c.NoTouch {
		sb.add("no-touch=1")
	}
	if c.LibName != "" {
		sb.add("lib-name=" + c.LibName)
	}
	if c.LibVer != "" {
		sb.add("lib-ver=" + c.LibVer)
	}
	return sb.String()
}

type stringBuf struct{ s string }

func (b *stringBuf) add(part string) {
	if b.s != "" {
		b.s += " "
	}
	b.s += part
}
func (b *stringBuf) String() string { return b.s }
