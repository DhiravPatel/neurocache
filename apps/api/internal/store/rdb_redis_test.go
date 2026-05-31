package store

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests round-trip the DUMP/RESTORE payload against a real redis-server
// to prove byte-level wire compatibility. They are skipped unless
// REDIS_ADDR is set (e.g. REDIS_ADDR=127.0.0.1:6399) and reachable.

type respClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialRESP(addr string) (*respClient, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return nil, err
	}
	return &respClient{conn: c, r: bufio.NewReader(c)}, nil
}

func (c *respClient) close() { c.conn.Close() }

func (c *respClient) do(args ...[]byte) (any, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n", len(a))
		b.Write(a)
		b.WriteString("\r\n")
	}
	if _, err := c.conn.Write(b.Bytes()); err != nil {
		return nil, err
	}
	return c.readReply()
}

func (c *respClient) cmd(args ...string) (any, error) {
	bs := make([][]byte, len(args))
	for i, a := range args {
		bs[i] = []byte(a)
	}
	return c.do(bs...)
}

func (c *respClient) readReply() (any, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, errors.New(line[1:])
	case ':':
		n, _ := strconv.ParseInt(line[1:], 10, 64)
		return n, nil
	case '$':
		l, _ := strconv.Atoi(line[1:])
		if l < 0 {
			return nil, nil
		}
		buf := make([]byte, l)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		_, _ = c.r.Discard(2)
		return buf, nil
	case '*':
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return nil, nil
		}
		arr := make([]any, n)
		for i := range arr {
			arr[i], err = c.readReply()
			if err != nil {
				return nil, err
			}
		}
		return arr, nil
	}
	return nil, fmt.Errorf("bad reply: %q", line)
}

func arrToStrings(v any) []string {
	a, _ := v.([]any)
	out := make([]string, 0, len(a))
	for _, e := range a {
		switch x := e.(type) {
		case []byte:
			out = append(out, string(x))
		case string:
			out = append(out, x)
		}
	}
	return out
}

func redisClient(t *testing.T) *respClient {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set; skipping real-redis round-trip")
	}
	c, err := dialRESP(addr)
	if err != nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	return c
}

// TestRDBRedisToNeuroCache: redis-server produces the DUMP; rdbDeserialize
// must read it back faithfully (covers listpack / intset / quicklist_2 / int-
// and plain-encoded strings — the compact encodings real Redis emits).
func TestRDBRedisToNeuroCache(t *testing.T) {
	rc := redisClient(t)
	defer rc.close()
	_, _ = rc.cmd("FLUSHALL")

	// seed every type, then DUMP + decode + assert.
	_, _ = rc.cmd("SET", "s", "hello world")
	_, _ = rc.cmd("SET", "sint", "12345")
	_, _ = rc.cmd("RPUSH", "l", "a", "b", "c")
	_, _ = rc.cmd("SADD", "iset", "1", "2", "3")
	_, _ = rc.cmd("SADD", "sset", "apple", "banana", "cherry")
	_, _ = rc.cmd("HSET", "h", "f1", "v1", "f2", "v2")
	_, _ = rc.cmd("ZADD", "z", "1.5", "alice", "2.5", "bob")

	dump := func(k string) []byte {
		v, err := rc.do([]byte("DUMP"), []byte(k))
		if err != nil {
			t.Fatalf("DUMP %s: %v", k, err)
		}
		return v.([]byte)
	}

	// string (plain + int-encoded)
	if e, err := rdbDeserialize(dump("s")); err != nil || e.Str != "hello world" {
		t.Fatalf("string decode: %q err=%v", e.Str, err)
	}
	if e, err := rdbDeserialize(dump("sint")); err != nil || e.Str != "12345" {
		t.Fatalf("int-string decode: %q err=%v", e.Str, err)
	}
	// list (quicklist_2 → listpack node)
	if e, err := rdbDeserialize(dump("l")); err != nil || strings.Join(e.List, ",") != "a,b,c" {
		t.Fatalf("list decode: %v err=%v", e.List, err)
	}
	// set (intset)
	if e, err := rdbDeserialize(dump("iset")); err != nil || sortedJoin(e.Set) != "1,2,3" {
		t.Fatalf("intset decode: %v err=%v", e.Set, err)
	}
	// set (listpack)
	if e, err := rdbDeserialize(dump("sset")); err != nil || sortedJoin(e.Set) != "apple,banana,cherry" {
		t.Fatalf("set listpack decode: %v err=%v", e.Set, err)
	}
	// hash (listpack)
	if e, err := rdbDeserialize(dump("h")); err != nil || e.Hash["f1"] != "v1" || e.Hash["f2"] != "v2" {
		t.Fatalf("hash decode: %v err=%v", e.Hash, err)
	}
	// zset (listpack, scores as strings)
	e, err := rdbDeserialize(dump("z"))
	if err != nil || len(e.ZSet) != 2 {
		t.Fatalf("zset decode: %v err=%v", e.ZSet, err)
	}
	zm := map[string]float64{}
	for _, m := range e.ZSet {
		zm[m.Member] = m.Score
	}
	if zm["alice"] != 1.5 || zm["bob"] != 2.5 {
		t.Fatalf("zset scores: %v", zm)
	}
}

// TestRDBNeuroCacheToRedis: rdbSerialize produces the DUMP; real redis-server
// must RESTORE it and read the value back correctly (proves our DUMP output is
// Redis-loadable).
func TestRDBNeuroCacheToRedis(t *testing.T) {
	rc := redisClient(t)
	defer rc.close()
	_, _ = rc.cmd("FLUSHALL")

	restore := func(k string, exp ExportEntry) {
		blob, ok := rdbSerialize(exp)
		if !ok {
			t.Fatalf("rdbSerialize(%s) not ok", exp.Type)
		}
		if _, err := rc.do([]byte("RESTORE"), []byte(k), []byte("0"), blob, []byte("REPLACE")); err != nil {
			t.Fatalf("redis RESTORE %s (%s): %v", k, exp.Type, err)
		}
	}

	// string
	restore("s", ExportEntry{Type: "string", Str: "round-trip me"})
	if v, _ := rc.cmd("GET", "s"); string(v.([]byte)) != "round-trip me" {
		t.Fatalf("string readback: %v", v)
	}
	// list
	restore("l", ExportEntry{Type: "list", List: []string{"x", "y", "z"}})
	if v, _ := rc.cmd("LRANGE", "l", "0", "-1"); strings.Join(arrToStrings(v), ",") != "x,y,z" {
		t.Fatalf("list readback: %v", v)
	}
	// set
	restore("set", ExportEntry{Type: "set", Set: []string{"a", "b", "c"}})
	if v, _ := rc.cmd("SMEMBERS", "set"); sortedJoin(arrToStrings(v)) != "a,b,c" {
		t.Fatalf("set readback: %v", v)
	}
	// hash
	restore("h", ExportEntry{Type: "hash", Hash: map[string]string{"k1": "v1", "k2": "v2"}})
	if v, _ := rc.cmd("HGET", "h", "k2"); string(v.([]byte)) != "v2" {
		t.Fatalf("hash readback: %v", v)
	}
	// zset (binary doubles)
	restore("z", ExportEntry{Type: "zset", ZSet: []ExportZMember{{Member: "alice", Score: 1.5}, {Member: "bob", Score: 2.5}}})
	if v, _ := rc.cmd("ZSCORE", "z", "bob"); string(v.([]byte)) != "2.5" {
		t.Fatalf("zset readback: %v", v)
	}
}

func sortedJoin(ss []string) string {
	c := append([]string(nil), ss...)
	sort.Strings(c)
	return strings.Join(c, ",")
}
