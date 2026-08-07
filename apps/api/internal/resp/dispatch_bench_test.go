package resp

import (
	"bufio"
	"io"
	"log/slog"
	"testing"

	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
	"github.com/dhiravpatel/neurocache/apps/api/internal/transaction"
)

// benchConn builds a minimal real connection writing replies to io.Discard —
// enough to exercise the full executeUpper dispatch + RESP encoding path.
func benchConn(eng *engine.Engine) *conn {
	return &conn{
		eng:       eng,
		bw:        bufio.NewWriter(io.Discard),
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		tx:        transaction.New(),
		user:      eng.ACL.InitialUser(),
		info:      eng.Clients.Register("bench"),
		proto:     2,
		subs:      map[string]*pubsub.Subscription{},
		psub:      map[string]*pubsub.Subscription{},
		shardSubs: map[string]*pubsub.Subscription{},
		done:      make(chan struct{}),
	}
}

func benchEngine() *engine.Engine {
	return engine.New(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func BenchmarkDispatchSET(b *testing.B) {
	c := benchConn(benchEngine())
	parts := []string{"SET", "foo", "bar"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.executeUpper(parts, "SET")
	}
}

func BenchmarkDispatchGET(b *testing.B) {
	eng := benchEngine()
	eng.KV.Set("foo", "bar", 0)
	c := benchConn(eng)
	parts := []string{"GET", "foo"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.executeUpper(parts, "GET")
	}
}

func BenchmarkDispatchINCR(b *testing.B) {
	c := benchConn(benchEngine())
	parts := []string{"INCR", "counter"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.executeUpper(parts, "INCR")
	}
}

// BenchmarkDispatchLRANGE exercises a collection reply — one bulk header per
// element, the case the zero-alloc encoder helps most.
func BenchmarkDispatchLRANGE(b *testing.B) {
	eng := benchEngine()
	for i := 0; i < 100; i++ {
		eng.KV.RPush("list", "item")
	}
	c := benchConn(eng)
	parts := []string{"LRANGE", "list", "0", "-1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.executeUpper(parts, "LRANGE")
	}
}

func BenchmarkDispatchSADD(b *testing.B) {
	c := benchConn(benchEngine())
	parts := []string{"SADD", "s", "m"}
	b.ReportAllocs(); b.ResetTimer()
	for i := 0; i < b.N; i++ { c.executeUpper(parts, "SADD") }
}
func BenchmarkDispatchHSET(b *testing.B) {
	c := benchConn(benchEngine())
	parts := []string{"HSET", "h", "f", "v"}
	b.ReportAllocs(); b.ResetTimer()
	for i := 0; i < b.N; i++ { c.executeUpper(parts, "HSET") }
}

// BenchmarkDispatchZSCORE exercises the float reply path (writeFloat). The
// zero-alloc encoder formats the score straight into the writer's buffer
// instead of heap-allocating a string via strconv.FormatFloat.
func BenchmarkDispatchZSCORE(b *testing.B) {
	eng := benchEngine()
	_, _ = eng.KV.ZAdd("z", store.ZPair{Score: 3.14159, Member: "m"})
	c := benchConn(eng)
	parts := []string{"ZSCORE", "z", "m"}
	b.ReportAllocs(); b.ResetTimer()
	for i := 0; i < b.N; i++ { c.executeUpper(parts, "ZSCORE") }
}
