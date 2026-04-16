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

- **Drop-in Redis** — speaks RESP on `:6379`, so `redis-cli`, `ioredis`, `go-redis`, `redis-py` all work.
- **Semantic cache** — `SEMANTIC_GET "how do I build an API"` retrieves a value stored under `"best backend language for APIs"` because it understands they mean the same thing.
- **LLM response cache** — stop paying for the same OpenAI/Anthropic call twice.
- **Per-user memory** — long-lived user context with semantic recall.
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

### Redis-compatible

```bash
SET greeting "hello"            # → OK
GET greeting                    # → "hello"
DEL greeting                    # → (integer) 1
EXISTS greeting                 # → (integer) 0
EXPIRE session:abc 3600         # TTL
TTL session:abc
INCR counter                    # atomic
KEYS *
FLUSHDB / FLUSHALL
PING                            # → PONG
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
│   │       ├── semcache/       # SEMANTIC_* + LLM response cache
│   │       ├── memory/         # per-user memory with semantic recall
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
