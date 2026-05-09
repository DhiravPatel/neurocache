# neurocache-hotpath — Rust phase-1 hot path

A standalone Rust binary that implements the bench-critical RESP
commands on a single-threaded async I/O loop. **Beats Redis by 50-86%
on `PING` / `GET` / `SET` / `INCR`** (Apple M4, redis-benchmark -P 16).

This is Phase 1 of the throughput project that closes the gap pure-Go
hits structurally on per-operation cost. It is NOT a replacement for
the main Go server (which speaks ~545 commands and ships every AI
primitive). It is a focused proof-of-architecture: same wire protocol,
same shard count, same FNV hash — but a single-threaded event loop
running compiled Rust eliminates the goroutine-scheduling + mutex-CAS
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

## What's implemented (Phase 1)

| Command  | Notes |
|----------|-------|
| `PING`   | Optional bulk arg echoes back |
| `ECHO`   | Single-arg passthrough |
| `GET`    | Returns nil bulk on miss |
| `SET`    | Bare `SET key value` only — EX/NX/XX flags ignored (not in bench) |
| `INCR`   | Two-tier: lock-free `AtomicI64::fetch_add` after first promotion |
| `DECR`   | Same path with `delta = -1` |
| `INCRBY` | Parses the delta arg |
| `DECRBY` | Parses the delta arg |
| `DEL`    | Variadic — counts removed |
| `EXISTS` | Variadic — counts present |
| `COMMAND`| Empty array reply (redis-benchmark sends this at startup) |
| `HELLO`  | Minimal RESP2 reply |
| `QUIT`   | Cleanly closes the connection |

Anything else returns `-ERR unknown command 'X'` so callers fail
fast instead of hanging.

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
# 18 unit tests (parser, store, command formatters)
```

## Bench

```bash
make bench-rust
# 3-way: Redis vs NeuroCache (Go) vs NeuroCache (Rust hot path)
# Defaults: -n 200000 -c 50 -P 16
# Override: P=64 N=500000 make bench-rust
```

---

## Phase 2 + 3 roadmap

This binary proves the architecture works. To make it the production
hot path:

### Phase 2 — expand the command surface
Port the rest of the bench-critical commands from Go:
- List family (`LPUSH`, `RPUSH`, `LPOP`, `RPOP`, `LRANGE`, `LLEN`, …)
- Hash family (`HSET`, `HGET`, `HDEL`, `HGETALL`, …)
- Set family (`SADD`, `SREM`, `SISMEMBER`, `SMEMBERS`, …)
- ZSet family (`ZADD`, `ZRANGE`, `ZSCORE`, …)
- TTL / expire (`EXPIRE`, `TTL`, `PERSIST`)
- `MSET` / `MGET`
- Transactions (`MULTI` / `EXEC` / `DISCARD` / `WATCH`)

Estimated effort: **2-3 weeks**.

### Phase 3 — integrate with the Go process
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

Estimated effort for either: **1-2 weeks** after Phase 2 lands.

### Phase 4 — feature parity
Port the remaining standard commands (Pub/Sub, scripting, cluster
mode) so the Rust hot path can run as a drop-in. AI commands
(`SEMANTIC_*`, `MEMORY.*`, `TOOL.*`, `GUARD.*`, `LLM.ROUTE.*`,
`INJECT.*`, etc.) stay on the Go side — those are the actual product
differentiator and don't need C-level performance.

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
