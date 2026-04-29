package introspect

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// MonitorBroker fan-outs every executed command to subscribed clients.
// MONITOR is by-design dangerous in production (every command on every
// connection becomes serialised through the broker), so we cap the
// per-subscriber buffer and drop on overflow rather than blocking the
// hot path.
type MonitorBroker struct {
	mu   sync.RWMutex
	subs map[uint64]chan string
	next uint64
}

// NewMonitorBroker builds an empty broker.
func NewMonitorBroker() *MonitorBroker { return &MonitorBroker{subs: map[uint64]chan string{}} }

// Subscribe registers a new MONITOR client. Returns the channel +
// a handle the caller passes to Unsubscribe at disconnect time.
func (b *MonitorBroker) Subscribe() (uint64, <-chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := b.next
	ch := make(chan string, 256)
	b.subs[id] = ch
	return id, ch
}

// Unsubscribe drops a MONITOR client.
func (b *MonitorBroker) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

// Subscribers reports the live MONITOR client count.
func (b *MonitorBroker) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Broadcast formats one command in the canonical Redis MONITOR shape
// and ships it to every subscriber. Cheap when nobody's watching.
func (b *MonitorBroker) Broadcast(client string, db int, cmd string, args []string) {
	b.mu.RLock()
	if len(b.subs) == 0 {
		b.mu.RUnlock()
		return
	}
	b.mu.RUnlock()

	line := formatMonitorLine(client, db, cmd, args)
	b.mu.RLock()
	for _, ch := range b.subs {
		select {
		case ch <- line:
		default:
			// drop on overflow — MONITOR is a debug tool, not a contract
		}
	}
	b.mu.RUnlock()
}

// formatMonitorLine returns the same string redis-cli MONITOR prints:
//
//	+1700000000.123456 [0 127.0.0.1:55001] "SET" "k" "v"\r\n
func formatMonitorLine(client string, db int, cmd string, args []string) string {
	now := time.Now()
	var b strings.Builder
	b.WriteByte('+')
	b.WriteString(strconv.FormatInt(now.Unix(), 10))
	b.WriteByte('.')
	micros := now.Nanosecond() / 1000
	b.WriteString(zeroPad(micros, 6))
	b.WriteString(" [")
	b.WriteString(strconv.Itoa(db))
	b.WriteByte(' ')
	b.WriteString(client)
	b.WriteString("] \"")
	b.WriteString(cmd)
	b.WriteByte('"')
	for _, a := range args {
		b.WriteString(" \"")
		b.WriteString(escapeMonitorArg(a))
		b.WriteByte('"')
	}
	b.WriteString("\r\n")
	return b.String()
}

// escapeMonitorArg keeps the printed line single-line + parseable.
func escapeMonitorArg(s string) string {
	b := strings.Builder{}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				b.WriteString(`\x`)
				b.WriteByte(hexDigit(c >> 4))
				b.WriteByte(hexDigit(c & 0xf))
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func zeroPad(v, width int) string {
	s := strconv.Itoa(v)
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}
