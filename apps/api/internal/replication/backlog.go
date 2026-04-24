package replication

import "sync"

// Backlog is a fixed-size ring buffer of propagated write commands, in
// raw RESP wire format. Each byte written to the backlog advances a
// monotonically-increasing offset; partial-resync works by scanning
// backwards from the ring head looking for (replid, offset) that the
// reconnecting replica asserts.
//
// Thread-safety: one producer (the engine's write hook) and many
// consumers (replica fan-out goroutines). A mutex suffices — writes
// are small and infrequent compared to the KV hot path.
type Backlog struct {
	mu     sync.RWMutex
	buf    []byte
	size   int64 // capacity in bytes
	begin  int64 // absolute offset of buf[0] contents
	length int64 // live bytes currently in the ring
	head   int64 // write cursor within buf (0..size-1)
}

// NewBacklog returns a ring of the given capacity. 0 / negative use
// 1 MiB as a sane default — enough to absorb a brief network hiccup
// without forcing a full resync.
func NewBacklog(size int64) *Backlog {
	if size <= 0 {
		size = 1 << 20
	}
	return &Backlog{buf: make([]byte, size), size: size}
}

// Capacity reports the ring's size in bytes.
func (b *Backlog) Capacity() int64 { return b.size }

// FirstOffset returns the earliest offset still retained.
func (b *Backlog) FirstOffset() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.begin
}

// LastOffset returns the latest offset currently in the ring
// (exclusive upper bound).
func (b *Backlog) LastOffset() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.begin + b.length
}

// Len reports live bytes in the ring.
func (b *Backlog) Len() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.length
}

// Append writes p to the ring, advancing head (and evicting the oldest
// bytes when the ring wraps). Returns the absolute offset immediately
// after the write — callers pass that to State.AdvanceOffset.
func (b *Backlog) Append(p []byte) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := int64(len(p))
	// If the payload is bigger than the ring, keep only the tail — the
	// earlier bytes would be overwritten on the same append anyway.
	if n >= b.size {
		copy(b.buf, p[n-b.size:])
		b.head = 0
		// The surviving bytes are p[n-size:n], so their absolute start
		// is (old-last) + (n-size). old-last == begin+length.
		b.begin = b.begin + b.length + (n - b.size)
		b.length = b.size
		return b.begin + b.length
	}
	// Two-phase copy to handle wrap.
	tail := b.size - b.head
	if n <= tail {
		copy(b.buf[b.head:], p)
	} else {
		copy(b.buf[b.head:], p[:tail])
		copy(b.buf[:n-tail], p[tail:])
	}
	b.head = (b.head + n) % b.size
	newLen := b.length + n
	if newLen > b.size {
		b.begin += newLen - b.size
		newLen = b.size
	}
	b.length = newLen
	return b.begin + b.length
}

// Read copies bytes starting at offset into dst. Returns (n, true) on
// success, (0, false) when the offset is no longer in the ring (the
// caller must full-resync in that case).
func (b *Backlog) Read(offset int64, dst []byte) (int, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if offset < b.begin || offset > b.begin+b.length {
		return 0, false
	}
	avail := b.begin + b.length - offset
	if avail <= 0 {
		return 0, true
	}
	n := int64(len(dst))
	if n > avail {
		n = avail
	}
	// position of offset inside buf:
	start := (b.head - b.length + (offset - b.begin) + b.size) % b.size
	if start < 0 {
		start += b.size
	}
	// handle wrap
	tail := b.size - start
	if n <= tail {
		copy(dst, b.buf[start:start+n])
	} else {
		copy(dst, b.buf[start:start+tail])
		copy(dst[tail:], b.buf[:n-tail])
	}
	return int(n), true
}

// Contains reports whether the given offset is still replayable.
func (b *Backlog) Contains(offset int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return offset >= b.begin && offset <= b.begin+b.length
}

// Slice returns a newly-allocated copy of bytes [offset, offset+len).
// Convenience for small reads in tests and handshake replies.
func (b *Backlog) Slice(offset, length int64) ([]byte, bool) {
	out := make([]byte, length)
	n, ok := b.Read(offset, out)
	if !ok {
		return nil, false
	}
	return out[:n], true
}
