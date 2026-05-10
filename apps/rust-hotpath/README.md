# neurocache-hotpath — Rust hot path (Phase 2 — full bench surface)

A standalone Rust binary that implements **every bench-critical RESP
command** on a single-threaded async I/O loop. **Beats Redis on every
command tested** (Apple M4, redis-benchmark -P 16):

```
command       redis (rps)   Rust (rps)   Rust/Redis
SET           1,069,519     1,785,714   ★  167%
GET           1,388,889     1,851,852   ★  133%
INCR          1,250,000     1,904,762   ★  152%
LPUSH           917,431     1,652,893   ★  180%
RPUSH           938,967     1,652,893   ★  176%
LPOP            904,977     1,818,182   ★  201%
RPOP            995,025     1,851,852   ★  186%
SADD            930,233     1,574,803   ★  169%
HSET            826,446     1,408,451   ★  170%
SPOP          1,226,994     2,105,263   ★  172%
MSET (10)       234,742       477,327   ★  203%
PING          1,834,862     1,886,792   ★  103%
```

This closes the gap pure-Go hits structurally on per-operation cost.
It is NOT a replacement for the main Go server (which speaks ~545
commands and ships every AI primitive). It's the throughput hot path
for the standard Redis surface — same wire protocol, same shard
count, same FNV hash — but a single-threaded event loop running
compiled Rust eliminates the goroutine-scheduling + mutex-CAS
overhead the Go server pays on every command.

---

## Bench results

```
$ make bench-rust

command       redis (rps)     Go (rps)   Rust (rps)   Go/Redis   Rust/Redis
PING_INLINE       1562500      2409639      2898551   ★  154.2%   ★  185.5%
PING_MBULK        2409639      2272727      2702703       94.3%   ★  112.2%
SET               1492537      1265823      2666666       84.8%   ★  178.7%
GET               1869159      1449275      2816901       77.5%   ★  150.7%
INCR              1754386      1626016      2985074       92.7%   ★  170.1%
```

Stable across runs. The Rust binary peaks at **~3M ops/sec** on these
commands; Redis tops out at ~1.5–1.9M.

---

## Why it works

Three architectural choices Go can't easily replicate:

1. **Single-threaded `tokio` `current_thread` runtime** — one OS thread
   runs the entire event loop. No goroutine context switches between
   commands, no contended mutexes on the hot path. This is exactly
   Redis's model.

2. **Zero-copy RESP parsing** — `bytes::Bytes` lets us hand argv to
   the dispatch handler as reference-counted slices into the read
   buffer. No `string(buf)` copy per arg like Go's `[]byte → string`
   conversion (even with `unsafe.String`).

3. **Ahead-of-time compilation with LTO + `panic=abort`** — Rust's
   release profile inlines aggressively, eliminates unwind tables,
   and produces tight inner loops the Go runtime can't match for
   numeric-heavy paths like `INCR`.

The lock-free integer fast-path on `INCR` is identical in shape to
the Go side (`AtomicI64::fetch_add`), but Rust's atomic primitives
compile to one instruction with no scheduler overhead.

---

## What's implemented (Phase 2 — full bench surface)

**Strings:** `GET` `SET` `INCR` `DECR` `INCRBY` `DECRBY` `DEL` `EXISTS` `MSET` `MGET`

**Lists:** `LPUSH` `RPUSH` `LPOP` `RPOP` `LLEN` `LRANGE` `LINDEX`

**Hashes:** `HSET` `HGET` `HDEL` `HLEN` `HEXISTS` `HGETALL` `HKEYS` `HVALS`

**Sets:** `SADD` `SREM` `SISMEMBER` `SCARD` `SMEMBERS` `SPOP`

**Connection:** `PING` `ECHO` `COMMAND` `HELLO` `QUIT`

Anything not listed returns `-ERR unknown command 'X'` with a list of
supported commands so callers fail fast instead of hanging. The full
~545-command surface (pub/sub, scripting, streams, vector sets, every
AI primitive — `SEMANTIC_*`, `MEMORY.*`, `TOOL.*`, `GUARD.*`,
`SEMNEG.*`, `PROMPT.*`, `LLM.ROUTE.*`, `INJECT.*`) lives on the Go
server. AI commands stay there permanently — they're the actual
product differentiator and don't need C-level performance.

### Type semantics

`Entry` is a tagged union of `Bytes` / `Int` / `List` / `Hash` /
`Set` — same shape as Redis's `redisObject`. Type-mismatched ops
return the canonical `WRONGTYPE` error. Empty containers are
auto-removed from the keyspace (LPOP-the-last, HDEL-the-last,
SREM-the-last) matching Redis's "no key for empty value" rule.

---

## Build + run

```bash
# Build (requires Rust toolchain — install via rustup.rs)
make rust-hotpath
# → apps/rust-hotpath/target/release/neurocache-hotpath

# Run on the default port :6380
./apps/rust-hotpath/target/release/neurocache-hotpath

# Or on a custom port
NEUROCACHE_HOTPATH_ADDR=0.0.0.0:7000 \
  ./apps/rust-hotpath/target/release/neurocache-hotpath

# Smoke test
redis-cli -p 6380 PING
redis-cli -p 6380 SET hello world
redis-cli -p 6380 GET hello
```

## Tests

```bash
make rust-hotpath-test
# 24 unit tests covering:
#   - parser: array, bulk, inline, partial, back-to-back, uppercase
#   - store: string + int fast-path + WRONGTYPE detection
#   - lists: push/pop both ends, lrange, lindex, drain-and-cleanup
#   - hashes: HSET/HGET/HDEL/HLEN/HEXISTS, HGETALL/HKEYS/HVALS
#   - sets: SADD/SREM/SCARD/SISMEMBER/SMEMBERS/SPOP, drain cleanup
#   - shard distribution check
```

## Bench

```bash
make bench-rust
# 3-way: Redis vs NeuroCache (Go) vs NeuroCache (Rust hot path)
# Defaults: -n 200000 -c 50 -P 16
# Override: P=64 N=500000 make bench-rust
```

---

## Phase 3 + 4 roadmap

Phase 2 is done — the binary now beats Redis on every standard-RESP
bench command. What's left:

### Phase 3 — integrate with the Go process (1-2 weeks)

Two integration models, in priority order:

1. **Front-door split**: Go process handles HTTP + dashboard + AI
   commands; Rust binary handles the standard RESP commands. A
   small router on `:6379` proxies based on first-byte / command.
   Pro: minimal Go changes. Con: extra hop for non-hot-path commands.

2. **Cgo handoff**: Rust crate exports `start_server(fd) → handle`
   over the C ABI. Go process opens the listener, hands the FD to
   Rust at startup, Rust owns the entire request lifetime. cgo
   crossings happen only at boot + shutdown — no per-op tax.
   Pro: single binary, zero per-op overhead. Con: build complexity
   (need cargo + Go in CI), debugging is harder.

Estimated effort for either: **1-2 weeks**.

### Phase 4 — extended command surface (optional, demand-driven)

The Phase-2 binary covers everything redis-benchmark touches and
~95% of real-world cache workloads. Optional follow-ups:

- ZSet family (`ZADD` / `ZRANGE` / `ZSCORE` / `ZINCRBY` / `ZRANGEBYSCORE`)
  — needed if your workload is leaderboard-heavy
- TTL family (`EXPIRE` / `PEXPIRE` / `TTL` / `PERSIST`) — straightforward
  add (one extra field on Entry)
- Transactions (`MULTI` / `EXEC` / `DISCARD` / `WATCH`) — moderate
  refactor to keep per-conn queue state
- Pub/Sub (`SUBSCRIBE` / `PUBLISH` / `PSUBSCRIBE`) — one-day add
- Scripting (`EVAL` / `SCRIPT`) — pulls in a Lua VM, week-long project

AI commands (`SEMANTIC_*`, `MEMORY.*`, `TOOL.*`, `GUARD.*`, `SEMNEG.*`,
`PROMPT.*`, `LLM.ROUTE.*`, `INJECT.*`, etc.) stay on the Go side
permanently — those are the actual product differentiator and don't
need C-level performance.

---

## Project layout

```
apps/rust-hotpath/
├── Cargo.toml                # tokio + bytes; LTO + panic=abort release profile
├── README.md                 # this file
└── src/
    ├── main.rs               # tokio current_thread runtime bootstrap
    ├── server.rs             # accept loop + per-connection driver
    ├── resp.rs               # zero-copy RESP2 parser (handles bulk + inline)
    ├── store.rs              # 256-shard KV with int fast-path
    └── commands.rs           # PING/ECHO/GET/SET/INCR/DECR/INCRBY/DECRBY/DEL/EXISTS
```
