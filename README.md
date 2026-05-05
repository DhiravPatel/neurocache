<div align="center">

<br/>

```
 ███╗   ██╗███████╗██╗   ██╗██████╗  ██████╗  ██████╗ █████╗  ██████╗██╗  ██╗███████╗
 ████╗  ██║██╔════╝██║   ██║██╔══██╗██╔═══██╗██╔════╝██╔══██╗██╔════╝██║  ██║██╔════╝
 ██╔██╗ ██║█████╗  ██║   ██║██████╔╝██║   ██║██║     ███████║██║     ███████║█████╗
 ██║╚██╗██║██╔══╝  ██║   ██║██╔══██╗██║   ██║██║     ██╔══██║██║     ██╔══██║██╔══╝
 ██║ ╚████║███████╗╚██████╔╝██║  ██║╚██████╔╝╚██████╗██║  ██║╚██████╗██║  ██║███████╗
 ╚═╝  ╚═══╝╚══════╝ ╚═════╝ ╚═╝  ╚═╝ ╚═════╝  ╚═════╝╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝╚══════╝
```

**The memory layer for AI applications.**
Redis-compatible caching engine that understands meaning — with a built-in analytics dashboard.

<br/>

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![React](https://img.shields.io/badge/React-18-61DAFB?style=flat-square&logo=react)](https://react.dev)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?style=flat-square&logo=docker)](https://docker.com)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)

<br/>

[**Install**](#install) · [**Dashboard**](#dashboard) · [**Commands**](#commands) · [**SDKs**](#sdks) · [**Architecture**](#architecture) · [**Self-host**](#self-host--production)

<br/>

</div>

---

## What is NeuroCache?

NeuroCache is a **single-binary, Redis-compatible in-memory data store with AI-native commands** — built for LLM applications.

- **Drop-in Redis / Valkey / DiceDB** — speaks RESP on `:6379`, so `redis-cli`, `ioredis`, `go-redis`, `redis-py` all work. Implements the full Redis 8.6 / Valkey 8.0 / DiceDB 1.0 user-facing surface (**~545 commands across 12 data types**) plus 5 stack modules (RedisJSON, RedisBloom, RedisTimeSeries, RediSearch, vector sets).
- **Semantic cache** — `SEMANTIC_GET "how do I build an API"` retrieves a value stored under `"best backend language for APIs"` because it understands they mean the same thing.
- **LLM response cache** — stop paying for the same OpenAI/Anthropic call twice.
- **Per-user memory** — long-lived user context with semantic recall.
- **Production primitives** — `IDEMPOTENT`, `LOCK` (with monotonic fencing tokens), `RATELIMIT` (GCRA), `DEDUP`, `KEY.TRACK` time-travel, `AI.RECOMMEND` collaborative filtering, `HOTKEYS` real-time top-K.
- **Built-in dashboard** — real-time analytics at `http://localhost:8080`, served by the same binary.
- **One install, one process** — no external Vite server, no separate dashboard deploy.

```
Standard Redis:                            NeuroCache:

SET "best phone under 50k" "iPhone 13"     SEMANTIC_SET "best phone under 50k" "iPhone 13"
GET "best phone under 50k"  → "iPhone 13"  SEMANTIC_GET "good phones below 50000" → "iPhone 13"
GET "good phones below 50000" → (nil)      (same meaning, returns the cached value)
```

---

## Install

### One-line install (recommended)

```bash
curl -fsSL https://neurocache.dev/install.sh | sh
```

This script:
1. Checks Docker is installed and running
2. Pulls the `neurocache/engine:latest` image
3. Starts the container with ports `8080` (dashboard) and `6379` (RESP), and a persistent volume
4. Waits for the health check to pass
5. Prints the dashboard URL

Then open **[http://localhost:8080](http://localhost:8080)** — the dashboard is live.

### Docker (manual)

```bash
docker run -d \
  --name neurocache \
  -p 8080:8080 \
  -p 6379:6379 \
  -v neurocache-data:/data \
  neurocache/engine:latest
```

### Docker Compose

```bash
git clone https://github.com/dhiravpatel/neurocache.git
cd neurocache
docker compose up -d
```

### Connect

```bash
# Use any Redis client — it just works.
redis-cli -p 6379 ping              # → PONG
redis-cli -p 6379 SET greeting hi
redis-cli -p 6379 GET greeting

# Or use the bundled CLI
docker exec -it neurocache neurocache-cli
neurocache> SEMANTIC_SET "best language for APIs" "Go"
neurocache> SEMANTIC_GET "top backend language"
```

---

## Dashboard

Open [http://localhost:8080](http://localhost:8080) after install. The dashboard ships inside the engine binary — no separate service, no separate deploy.

**Dashboard pages:**
- **Dashboard** — live engine stats, uptime, keys, hit rates, heap.
- **Analytics** — commands-per-second chart (rolling 60s), cache hit rate, p50/p95 latency, hot keys, command breakdown, **estimated LLM cost savings**.
- **Key-Value** — browse, set, delete keys with TTL.
- **Semantic** — try `SEMANTIC_SET` / `SEMANTIC_GET` with a similarity slider.
- **LLM Cache** — seed + test the AI response cache.
- **Memory** — per-user memory store with semantic recall + synthesized context preview.
- **Playground** — redis-cli-style REPL inside the browser (runs both standard + AI-native commands).

The Analytics page queries these endpoints on the engine:

| Endpoint | What it returns |
|---|---|
| `GET /api/metrics/summary`    | totals, hit rates, estimated savings, command breakdown |
| `GET /api/metrics/timeline`   | rolling 60s time series — cmds/s, hits, misses, p50, p95 |
| `GET /api/metrics/hot-keys`   | top-K most-read keys |
| `GET /api/metrics/breakdown`  | per-command counts (SET, GET, SEMANTIC_GET, …) |

---

## Commands

**~545 commands** across **12 data types** — full Redis 8.6 / Valkey 8.0 / DiceDB 1.0 user-facing surface plus AI-native extensions and NeuroCache-only primitives. The complete reference with every subcommand, option, and example lives in [`FEATURES.md`](FEATURES.md) and on the [embedded docs site](http://localhost:8080/docs/commands).

### Redis-compatible (every group)

```bash
# Strings, hashes, lists, sets, zsets, streams, geo, bitmaps, HLL — all present
SET greeting "hello" EX 3600    # TTL flags, NX/XX, GETEX/GETDEL/GETSET, MSET/MSETNX, APPEND, STRLEN, INCR/INCRBY/INCRBYFLOAT, BITFIELD/BITFIELD_RO, BITOP, BITCOUNT, BITPOS, GETRANGE, SETRANGE, LCS, SORT/SORT_RO …
HSET user:1 name alice age 33   # HGET, HMGET, HGETALL, HKEYS, HVALS, HSCAN, HRANDFIELD, HEXPIRE/HTTL/HPERSIST per-field TTLs, HGETDEL, HGETEX, HSETEX …
LPUSH q a b c                   # LMOVE, LMPOP/BLMPOP, LPOS, LINSERT, LREM, LSET, LTRIM, BRPOPLPUSH, BLMOVE …
ZADD lb 100 alice 200 bob       # ZRANGEBYSCORE/LEX, ZUNIONSTORE/ZINTERSTORE/ZDIFFSTORE, ZRANGESTORE, ZMPOP/BZMPOP, ZRANDMEMBER …
XADD events * type login        # XGROUP, XREADGROUP, XACK, XPENDING, XCLAIM, XAUTOCLAIM, XACKDEL, XDELEX, XCFGSET …
GEOADD stores -73.98 40.74 nyc  # GEOSEARCH, GEOSEARCHSTORE, GEORADIUS/GEORADIUSBYMEMBER + _RO …
PFADD uniq a b c                # PFCOUNT, PFMERGE, PFDEBUG, PFSELFTEST

# Operations — replication, cluster, sentinel, modules, scripting, ACL, transactions, pub/sub
EVAL "return redis.call('GET', KEYS[1])" 1 mykey
SCRIPT LOAD/EXISTS/FLUSH/KILL/SHOW/DEBUG     # real Lua 5.1 (gopher-lua)
FUNCTION LOAD/LIST/DELETE/DUMP/RESTORE/KILL
ACL SETUSER alice on >pw ~cache:* +@read     # 22 categories, full rule grammar
CLUSTER INFO/NODES/SHARDS/SLOTS/SETSLOT/MIGRATE  # 16384-slot CRC16-XMODEM
SENTINEL MASTERS/MONITOR/FAILOVER/CKQUORUM   # full sentinel surface
MULTI / EXEC / DISCARD / WATCH               # optimistic concurrency
SUBSCRIBE / PSUBSCRIBE / PUBLISH             # pub/sub + sharded SSUBSCRIBE/SPUBLISH

# Modules (compile-time linked, activate via MODULE LOAD)
MODULE LOAD json | probabilistic | timeseries | search
JSON.SET / JSON.GET / JSON.MERGE             # RedisJSON
BF.ADD / CF.ADD / CMS.INCRBY / TOPK.ADD      # RedisBloom
TS.CREATE / TS.ADD / TS.RANGE                # RedisTimeSeries (Gorilla compression)
FT.CREATE / FT.SEARCH / FT.AGGREGATE         # RediSearch (BM25 + GEO + HNSW + hybrid)
VADD / VSIM / VEMB / VINFO                   # native vector set type (12th data type)

# Driver / ops fillers (Phase 7)
BRPOPLPUSH / MOVE / SWAPDB / EVICT
RESTORE-ASKING / CLUSTER DELSLOTSRANGE / SET-CONFIG-EPOCH
LATENCY HISTOGRAM / CLIENT CAPA|SETINFO|CACHING / COMMAND GETKEYSANDFLAGS
```

### AI-native

```bash
# Semantic cache — store by meaning, retrieve by meaning
SEMANTIC_SET "best backend language for APIs" "Go is ideal for high-performance APIs"
SEMANTIC_GET "what language should I use for building APIs"
# → "Go is ideal for high-performance APIs"    (paraphrase still matches)

# LLM response cache
CACHE_LLM "write a cold email for a SaaS product" "Subject: Quick question about..."
CACHE_LLM_GET "draft a cold outreach email for my software"
CACHE_LLM_STATS

# Per-user memory
MEMORY_ADD user:dhirav "Prefers Go + React + Tailwind"
MEMORY_ADD user:dhirav "Building NeuroCache"
MEMORY_QUERY user:dhirav "what is this user working on?"
MEMORY_LIST user:dhirav

# Embedding cache — embeddings are deterministic per (model, text);
# cache them at the engine and stop paying for the same vector twice.
EMB.CACHE_SET "the quick brown fox" "0.12,0.45,...,0.89" EX 86400
EMB.CACHE_GET "the quick brown fox"      # → cached vector
EMB.COST 0.0001                          # $/embedding-call
EMB.STATS                                # hit rate + saved_usd

# Conversation/session management — token-aware windowing baked in.
# Centralizes the truncation logic so an app can't ship a context-overflow.
CONV.APPEND chat:alice user "what's the weather?"
CONV.APPEND chat:alice assistant "Sunny, 72F today."
CONV.APPEND chat:alice user "and tomorrow?"
CONV.WINDOW chat:alice MAXTOKENS 4000    # recent turns within budget
CONV.SUMMARIZE chat:alice "User asked about weather Mon-Tue" KEEP 1000
CONV.LEN chat:alice                      # turns / tokens / has_summary

# Versioned prompt templates — auditability + safe rollback when v4 underperforms.
PROMPT.SET support-reply "Hi {name}, thanks for writing about {topic}."
PROMPT.SET support-reply "Hello {name}! Got your note about {topic}."   # auto-bumps to v2
PROMPT.RENDER support-reply VARS name "Alice" topic "billing"
PROMPT.GET support-reply VERSION 1       # historical (rollback target)
PROMPT.LIST                              # every template with latest version

# Hybrid retrieval — BM25 lexical + dense vector + RRF fusion in one
# command. The production retrieval stack: pure-vector misses model
# numbers and exact strings; pure-BM25 misses paraphrases; the fused
# rank handles both.
RETRIEVE.CREATE docs HNSW 1
RETRIEVE.ADD docs d1 "iPhone 13 review: best small phone of 2024" META entity Apple
RETRIEVE.ADD docs d2 "Samsung Galaxy S22 deep dive" META entity Samsung
RETRIEVE.QUERY docs "best small phone" K 3 ALPHA 0.5
# returns hits ranked by RRF(BM25, vector); each row carries both
# component ranks so you can debug "why did this match?".

# GraphRAG — hybrid retrieval AND knowledge-graph expansion in one
# call. Each top hit's `entity` metadata is walked through the graph
# up to N hops, and the visited triples ride back as `context`.
GRAPH.LINK Apple founded_by "Steve Jobs"
GRAPH.LINK Apple headquartered_in Cupertino
RAG.QUERY docs "best small phone" K 3 HOPS 2
# → { hits: [...d1, d2...], context: [(Apple, founded_by, Steve Jobs), ...] }

# Layered memory — episodic (events) / semantic (distilled facts) /
# procedural (rules), with importance hints, dedup-on-write,
# recency-weighted ranking, soft + hard decay, and bulk consolidation.
MEMORY.ADD user:dhirav "user prefers terse explanations" \
  LAYER semantic IMPORTANCE 0.9 DEDUP 0.85
MEMORY.QUERY user:dhirav "communication style" LAYER semantic K 5 RECENCY 0.3 TOUCH 1
MEMORY.STATS user:dhirav
MEMORY.DECAY user:dhirav LAYER episodic MAXAGE 2592000 DRYRUN 1
MEMORY.CONSOLIDATE user:dhirav THRESHOLD 0.85 MIN 3 DROP 1

# MCP catalog — every primitive above is also a registered MCP tool,
# so Claude Desktop / Cursor / any MCP client gets a working set out
# of the box. No registration code, no glue, just point the client.
MCP.TOOLS                                # 13 tools: kv_get/set, semantic_*,
                                          # memory_*, graph_*, retrieve_*,
                                          # rag_query, conv_*
MCP.CALL neurocache.rag_query '{"index":"docs","query":"best small phone","k":3,"hops":2}'
```

### NeuroCache-only primitives (no Redis equivalent)

```bash
# Run a command at-most-once per (key, ttl) window — replaces SETNX-then-execute
IDEMPOTENT order:42 60000 INCR order:counter

# Distributed lock with monotonic fencing tokens (Kleppmann-safe)
LOCK ACQUIRE deploy 30000     # → fencing token, e.g. 17
LOCK RELEASE deploy 17

# GCRA token-bucket rate limit — constant memory per key
RATELIMIT user:42 60000 100   # 100 requests / 60s; returns [allowed, remaining, retry-after-ms, reset-ms]

# Exactly-once on the cheap — bounded memory even for unbounded id streams
DEDUP webhooks evt-9f3b 86400000

# Cost-aware caching for LLM responses
CACHE.WEIGH gpt-key 0.05      # 5¢ to recompute → eviction prefers cheap entries
CACHE.HIT gpt-key
CACHE.STATS

# Per-key version history with binary-search time-travel
KEY.TRACK user:42:tier
KEY.HISTORY user:42:tier 10
KEY.AT user:42:tier 1735689600

# Collaborative-filtering recommendations
AI.LIKE alice book:42 1.0
AI.RECOMMEND alice 10
AI.SIMILAR alice 5

# Real-time top-K key tracker (HeavyKeeper) — replaces redis-cli --hotkeys SCAN dance
HOTKEYS 20
HOTKEYS STATS
HOTKEYS COUNT user:42
```

### HTTP API (for the dashboard and SDKs)

Every command is also available as a JSON endpoint, e.g.:

```bash
curl -X POST http://localhost:8080/api/kv \
  -H 'Content-Type: application/json' \
  -d '{"key":"greeting","value":"hello","ttl":3600}'

curl "http://localhost:8080/api/semantic?q=top+backend+language&threshold=0.75"

curl -X POST http://localhost:8080/api/exec \
  -H 'Content-Type: application/json' \
  -d '{"command":"MEMORY_QUERY","args":["user:dhirav","tech preferences"]}'
```

---

## SDKs

### TypeScript / JavaScript

```bash
pnpm add @neurocache/sdk
```

```ts
import { NeuroCache } from "@neurocache/sdk";

const cache = new NeuroCache({ baseUrl: "http://localhost:8080" });

// Standard
await cache.set("user:name", "Dhirav", 3600);
const { value } = await cache.get("user:name");

// Semantic
await cache.semanticSet("best language for APIs", "Go");
const hit = await cache.semanticGet("what language for backend services");

// LLM response cache — wrap your OpenAI/Anthropic calls
const { value: reply, hit: cacheHit } = await cache.cacheLLMAround(
  userPrompt,
  async () => (await openai.chat.completions.create({ /* ... */ })).choices[0].message.content!,
  { threshold: 0.88 },
);

// Per-user memory
await cache.memory.add("user:dhirav", "Prefers Go and React");
const { context } = await cache.memory.query("user:dhirav", "tech preferences?");
```

### Go (via go-redis, works out of the box on :6379)

```go
import "github.com/redis/go-redis/v9"

rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
rdb.Set(ctx, "greeting", "hello", 0)

// AI-native commands work via the generic Do() interface:
res, _ := rdb.Do(ctx, "SEMANTIC_SET", "best go framework", "Gin").Result()
val, _ := rdb.Do(ctx, "SEMANTIC_GET", "top go web framework").Text()
```

### Any Redis client

Because NeuroCache speaks RESP, every existing Redis client library works for the standard commands. The AI-native commands are available through each client's generic `command()` / `raw()` / `do()` API.

---

## Performance

Benchmarked head-to-head vs. Redis 7.x on Apple M4 (100k ops × 50 concurrent clients, both servers local). NeuroCache runs at **77–90% of Redis throughput** on every command, with `MSET` ahead of Redis after the hot-path optimizations:

| Command | Redis (rps) | NeuroCache (rps) | Ratio |
|---|---:|---:|---:|
| MSET (10 keys) | ~148k | ~173k | **117%** |
| SET | ~215k | ~196k | **91%** |
| GET | ~236k | ~200k | **85%** |
| HSET / ZADD | ~228k | ~198k | **87%** |
| LPOP / RPOP | ~232k | ~193k | **84%** |
| SADD / SPOP | ~238k | ~189k | **79%** |
| INCR / LPUSH / RPUSH | ~234k | ~186k | **80%** |

For comparison, the unoptimized baseline on the same hardware sat at **49–70%** for HSET / ZADD / SADD / SPOP. The wins came from:

- **Inlined FNV-1a hash** in `shardForKey` — eliminated the per-command `fnv.New32a()` hasher and `[]byte(key)` cast that ran on every dispatch
- **Cached shard index** — `shardIndex(sh)` is now O(1) instead of walking all 256 shards on every two-key op (RENAME, COPY, RPOPLPUSH, SMOVE, LMOVE)
- **Single-key fast paths** for `DEL` and `EXISTS` — skip the `bucketKeysByShard` map allocation
- **Zero-alloc ASCII upper** for command names — `asciiUpper` returns the same string when input is already upper, eliminating 4 redundant `strings.ToUpper(parts[0])` calls per command
- **ACL fast-path** — when the user has full perms (the default config), skip the entire `Allowed()` path including `CategoriesFor`, audit-log lock, and key-pattern matching
- **Writer streaming** — `writeArray([]any)` and `writeArray([][]any)` stream the header byte-by-byte instead of allocating `"*"+itoa(n)+"\r\n"`
- **Lock-free latency ring buffer** — the per-command `m.mu.Lock(); m.latencies = append(...)` pattern was costing ~5% of dispatch time at 200k cmds/sec. Now an 8192-slot ring + atomic position counter; aggregator reads a snapshot once per second
- **1ms latency-record threshold** — `LATENCY HISTORY` only stores genuinely slow events (the steady-state hot path is sub-millisecond), so the per-command mutex acquire is gated behind an atomic load
- **SLO `targetCount` fast-path** — when no SLOs are configured, `SLOTracker.Record` returns after a single atomic load, no mutex
- **`cmdTypes` Load-before-LoadOrStore** — fast-path the per-command-type counter once a command has been seen at least once
- **Container-aware `GOMAXPROCS`** — detects cgroup v2 (`cpu.max`) and v1 (`cpu.cfs_quota_us`) limits at boot and pins `GOMAXPROCS` to the effective quota, avoiding scheduler oversubscription on CPU-limited pods

### Why not faster?

To consistently exceed 100% of Redis in **pure Go** would require either:

- **`io_uring`** (Linux-only) replacing the current epoll-via-Go-runtime networking — batched I/O brings ~20-40% headroom, but ties us to Linux 5.6+ kernels
- **A C/Rust hot path** for the RESP parser + KV store — Dragonfly's 2-5× over Redis comes from C++ shared-nothing threading with hand-tuned wait-free queues; Go's channel/goroutine overhead (~50-100ns per op) eats most of that win

Both are architectural commitments measured in months, not hours. For nearly every workload, 80-90% of Redis on raw RPS is plenty — and the AI-native commands (semantic cache, layered memory, GraphRAG) are the actual differentiator; matching Redis on the standard surface is just table stakes.

For the deep architectural comparison — concurrency model, hot-key contention, p99 tail latency, large-value behavior, 1000-client connection scaling, persistence + replication overhead, and a list of every architectural risk we audited and either fixed or accepted — see **[docs/ARCHITECTURE_AUDIT.md](docs/ARCHITECTURE_AUDIT.md)**.

Reproduce on your hardware:

```bash
brew install redis           # for redis-benchmark
scripts/bench-vs-redis.sh    # builds NC, runs both side-by-side, prints the table with regression flags
```

In-process micro-benchmarks (catch any O(N) regressions in the store hot path before they ship):

```bash
cd apps/api && go test ./internal/store/ -run=NONE -bench=BenchmarkHot -benchmem
# LPUSH       95 ns/op
# RPUSH       95 ns/op
# LPOP        125 ns/op (constant, not O(N), even from a 100k-element list)
# GET          68 ns/op
# INCR         53 ns/op
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                 single neurocache binary                    │
│                                                             │
│   :8080 ──►  React dashboard  (embedded via go:embed)       │
│             + HTTP API        (/api/*)                      │
│                                                             │
│   :6379 ──►  RESP server      (redis-cli, ioredis, etc.)    │
│                                                             │
│   ┌────────────────────────────────────────────────────┐    │
│   │ KV store (sharded map) ─┐                          │    │
│   │                         ├─► AI eviction scorer     │    │
│   │ Vector index (HNSW-ish) ┘     (freq + recency      │    │
│   │                                 - size)            │    │
│   │ Memory store (per-user)                            │    │
│   │ Metrics (timeline + hot keys + savings)            │    │
│   └────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

The monorepo is managed with **Turborepo** and **pnpm workspaces**. For development, the React app runs on Vite (`:5173`) and proxies to the Go API on `:8080`. For production, the React build is embedded into the Go binary via `//go:embed`, and both the dashboard and API are served by a single process.

```
neurocache/
├── apps/
│   ├── api/                    # Go engine + embedded dashboard
│   │   ├── cmd/server/         # engine entrypoint
│   │   ├── cmd/cli/            # neurocache-cli
│   │   └── internal/
│   │       ├── store/          # in-memory KV with TTL
│   │       ├── vector/         # feature-hashed embeddings + cosine index
│   │       ├── vectorindex/    # HNSW + FLAT vector indexes (V* commands)
│   │       ├── retrieval/      # BM25 + vector + RRF fusion (RETRIEVE.*, RAG.QUERY)
│   │       ├── semcache/       # SEMANTIC_* + LLM response cache
│   │       ├── memory/         # layered memory (episodic/semantic/procedural)
│   │       │                   #   + decay + dedup + consolidation
│   │       ├── aiops/          # graph, MCP server + tool catalog, A/B,
│   │       │                   #   moderation, lineage, SLOs, scheduler...
│   │       ├── llmstack/       # embedding cache, conversations, prompts
│   │       ├── eviction/       # AI-scored / LRU / LFU policies
│   │       ├── metrics/        # rolling timeline + hot keys + savings
│   │       ├── http/           # HTTP + JSON handlers, /api/* routes
│   │       ├── resp/           # RESP2 TCP server for redis-cli
│   │       └── webui/          # go:embed of apps/web/dist
│   │
│   └── web/                    # React + Vite + Tailwind dashboard
│       └── src/pages/          # Dashboard, Analytics, KV, Semantic,
│                               # LLM, Memory, Playground
│
├── packages/
│   └── sdk-js/                 # @neurocache/sdk — TypeScript client
│
├── scripts/
│   └── install.sh              # one-line installer
│
├── Dockerfile                  # multi-stage: node builds UI → go embeds it
├── docker-compose.yml
├── render.yaml                 # one-service Render Blueprint
├── Makefile
├── turbo.json
└── pnpm-workspace.yaml
```

---

## Development

```bash
# Prereqs: Node 18+, pnpm 8+, Go 1.22+
pnpm install

# Run both the Go API (on :8080) and the React dev server (on :5173) with
# hot reload. The React app is told to call the API at :8080 via
# apps/web/.env (copy from apps/web/.env.example if needed).
pnpm dev
```

In dev, the dashboard at `:5173` uses Vite HMR and calls the Go API on `:8080`. In prod/Docker, everything is one process on `:8080`.

### Build the single binary locally (with embedded dashboard)

```bash
make build
./bin/neurocache        # → http://localhost:8080
```

### Run tests

```bash
make test
```

---

## Self-host / Production

### Render (recommended)

[`render.yaml`](render.yaml) defines a single Docker-based web service with a persistent disk for AOF data. Either:

- **Blueprint**: push this repo, click "New Blueprint", select `render.yaml`.
- **Manual**: create a Web Service, runtime = Docker, health check = `/api/health`, mount a 1 GB disk at `/data`.

Ports in Render default to one HTTP port (`8080`) — the dashboard + API both work. If you also need the RESP protocol publicly, add a TCP service or expose via a private network.

### Fly.io / Railway / any container host

The image is just `neurocache/engine:latest`. Expose `8080` for the dashboard + HTTP API, and optionally `6379` for RESP. Mount a volume at `/data` to persist the store across restarts.

### Configuration

All configuration is via environment variables — see [.env.example](.env.example) for the full list:

```env
NEUROCACHE_HTTP_PORT=8080
NEUROCACHE_RESP_PORT=6379
NEUROCACHE_MAX_MEMORY=512mb
NEUROCACHE_EVICTION_POLICY=ai-smart       # ai-smart | lru | lfu | noeviction
NEUROCACHE_SEMANTIC_THRESHOLD=0.75
NEUROCACHE_LOG_LEVEL=info
NEUROCACHE_LOG_FORMAT=text                # text | json
NEUROCACHE_CORS_ORIGINS=*
NEUROCACHE_DATA_DIR=/data
```

---

## How semantic matching works

NeuroCache ships with a **feature-hashed embedding** (word + character-trigram hashing → 384-dim L2-normalized vector) and a linear-scan cosine index. It is intentionally dependency-free and tiny — good enough for tens of thousands of keys on a single node and for getting a hit-rate signal on real traffic.

For production-grade semantic quality, the embedding function is a single interface — swap it for:
- **ONNX runtime** + a bundled MiniLM-L6 / BGE-small model (offline, ~100 MB)
- **OpenAI `text-embedding-3-small`** (cloud, requires API key)
- Any other model that returns a `[]float32`

The rest of the engine (vector index, semantic cache, memory store) is unchanged.

---

## Roadmap

### V1 — MVP *(current)*
- [x] RESP protocol server + HTTP API
- [x] Core KV: SET, GET, DEL, EXPIRE, TTL, INCR/DECR
- [x] Semantic cache (`SEMANTIC_SET` / `SEMANTIC_GET`)
- [x] LLM response cache (`CACHE_LLM`)
- [x] Per-user memory store
- [x] Embedded React dashboard with live analytics
- [x] Hot keys, command breakdown, p50/p95, estimated LLM savings
- [x] One-line Docker install
- [x] TypeScript SDK

### V2 — Stable
- [ ] Persistent storage — AOF + mmap snapshots
- [ ] Real ONNX embeddings bundled by default
- [ ] Hash and List data types
- [ ] HNSW vector index
- [ ] Multi-tenant namespace isolation
- [ ] OpenTelemetry tracing + Prometheus endpoint
- [ ] Go SDK (published module)

### V3 — Scale
- [ ] Replication — master → replica sync
- [ ] Cluster mode — hash slots, horizontal scaling
- [ ] Pub/Sub
- [ ] Auto cache learning (pattern detection)
- [ ] SIMD-accelerated vector ops

---

## License

MIT
