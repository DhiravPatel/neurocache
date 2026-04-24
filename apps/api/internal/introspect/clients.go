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

	// Reply mode: "on" (default), "off" (silent), "skip" (skip next).
	ReplyMode  string
	NoEvict    bool
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

// Touch records that the client just executed cmd. Cheap; called from
// the dispatch hot path so we keep it lock-free per client.
func (r *ClientRegistry) Touch(c *ClientInfo, cmd string) {
	if c == nil {
		return
	}
	c.LastCmdAt = time.Now()
	c.LastCmd = cmd
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
		return
	}
	r.pauseUntil = time.Now().Add(d)
}

// PauseRemaining returns 0 (no pause) or the time-until-resume.
func (r *ClientRegistry) PauseRemaining() time.Duration {
	r.pauseMu.RLock()
	defer r.pauseMu.RUnlock()
	if r.pauseUntil.IsZero() {
		return 0
	}
	rem := time.Until(r.pauseUntil)
	if rem <= 0 {
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
