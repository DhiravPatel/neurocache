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
Redis-compatible caching engine that understands meaning, not just keys.

<br/>

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![React](https://img.shields.io/badge/React-18-61DAFB?style=flat-square&logo=react)](https://react.dev)
[![Turborepo](https://img.shields.io/badge/Turborepo-monorepo-EF4444?style=flat-square&logo=turborepo)](https://turbo.build/repo)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?style=flat-square&logo=docker)](https://docker.com)
[![Status](https://img.shields.io/badge/Status-V1%20Active-brightgreen?style=flat-square)]()

<br/>

[**Quick Start**](#quick-start) · [**Monorepo**](#monorepo-layout) · [**Commands**](#commands) · [**Architecture**](#architecture) · [**Deployment**](#deployment) · [**Roadmap**](#roadmap)

<br/>

</div>

---

## What is NeuroCache?

NeuroCache is an **AI-aware, Redis-compatible in-memory data store** built for LLM-powered applications.

Standard Redis stores and retrieves data by exact key match. NeuroCache goes further — it understands the *semantic meaning* of your queries, remembers user context across sessions, and caches AI responses intelligently so you stop paying for the same LLM call twice.

```
Standard Redis:                            NeuroCache:

SET "best phone under 50k" "iPhone 13"     SEMANTIC_SET "best phone under 50k" "iPhone 13"
GET "best phone under 50k"  → "iPhone 13"  SEMANTIC_GET "good phones below 50000" → "iPhone 13"
GET "good phones below 50000" → (nil)      (understands they mean the same thing)
```

---

## Why NeuroCache?

| Problem | How NeuroCache solves it |
|---|---|
| LLMs forget everything between sessions | Per-user memory store with semantic recall |
| Same AI prompt costs money every time | LLM response cache with similarity matching |
| Redis can't understand meaning | Embedded vector index alongside KV store |
| Smart eviction doesn't exist | AI-scored eviction based on value, not just recency |
| Vector DBs have no cache semantics | Hybrid KV + vector engine in one system |

---

## Tech Stack

- **Monorepo tooling:** [Turborepo](https://turbo.build/repo) + pnpm workspaces
- **Backend engine:** Go 1.22+
- **Frontend / dashboard:** React 18 + Vite + TypeScript
- **Shared packages:** TypeScript (types, SDK, config)
- **Backend hosting:** [Render](https://render.com)
- **Frontend hosting:** [Vercel](https://vercel.com)
- **Container runtime:** Docker

---

## Monorepo Layout

```
neurocache/
├── apps/
│   ├── api/                    # Go backend — the NeuroCache engine
│   │   ├── cmd/
│   │   │   ├── server/         # engine entrypoint
│   │   │   └── cli/            # neurocache-cli entrypoint
│   │   ├── internal/           # tcp, resp, cmd, store, vector, memory,
│   │   │                       # eviction, persist, embed, metrics
│   │   ├── pkg/                # config, logger
│   │   ├── go.mod
│   │   └── Dockerfile
│   │
│   └── web/                    # React frontend — dashboard + landing site
│       ├── src/
│       │   ├── pages/
│       │   ├── components/
│       │   └── lib/
│       ├── index.html
│       ├── vite.config.ts
│       └── package.json
│
├── packages/
│   ├── sdk-js/                 # @neurocache/client — TypeScript SDK
│   ├── types/                  # shared TS types between web and sdk
│   └── config/                 # shared ESLint / TS / Tailwind config
│
├── scripts/
│   ├── install.sh              # one-line install script
│   └── bench.sh                # benchmark runner
│
├── docker-compose.yml
├── render.yaml                 # Render Blueprint (backend)
├── vercel.json                 # Vercel config (frontend)
├── turbo.json                  # Turborepo pipeline
├── pnpm-workspace.yaml
├── package.json
└── README.md
```

---

## Prerequisites

- [Node.js](https://nodejs.org) >= 18
- [pnpm](https://pnpm.io) >= 8
- [Go](https://go.dev) >= 1.22
- [Docker](https://docs.docker.com/get-docker/) (optional, for running the engine in a container)

---

## Quick Start

### 1. Clone and install

```bash
git clone https://github.com/dhiravpatel/neurocache.git
cd neurocache
pnpm install
```

### 2. Run everything in dev mode

```bash
pnpm dev
```

Turborepo starts both workspaces in parallel:

- Go API on `http://localhost:6379` (RESP) and `http://localhost:8080` (HTTP admin)
- React web app on `http://localhost:5173`

### 3. Run apps individually

```bash
# Frontend only
pnpm --filter web dev

# Backend only
pnpm --filter api dev
# or directly:
cd apps/api && go run ./cmd/server
```

### 4. Run the engine via Docker (optional)

```bash
docker compose up -d

docker exec -it neurocache neurocache-cli ping
# → PONG
```

---

## Build

```bash
# Build everything (Turborepo caches outputs)
pnpm build

# Build a single workspace
pnpm --filter web build
pnpm --filter api build
```

---

## Commands

### Standard Redis Commands

```bash
# Key-value
SET user:name "Dhirav"
GET user:name                   # → "Dhirav"
DEL user:name                   # → (integer) 1
EXISTS user:name                # → (integer) 0

# Expiry
SET session:abc "token_xyz"
EXPIRE session:abc 3600         # expires in 1 hour
TTL session:abc                 # → 3598
PERSIST session:abc             # remove expiry

# Counters
SET api:calls 0
INCR api:calls                  # → 1
INCRBY api:calls 10             # → 11
DECR api:calls                  # → 10

# Hashes (V2)
HSET user:123 name "Dhirav" city "Morbi" role "developer"
HGET user:123 name              # → "Dhirav"
HGETALL user:123                # → all fields

# Lists (V2)
LPUSH tasks "build readme" "write tests" "deploy"
RPOP tasks                      # → "build readme"
LRANGE tasks 0 -1               # → all items
```

### AI-Native Commands

```bash
# Semantic cache — stores with meaning, retrieves by meaning
SEMANTIC_SET "best backend language for APIs" "Go is ideal for high-performance APIs"
SEMANTIC_GET "what language should I use for backend"
# → "Go is ideal for high-performance APIs"

# LLM response cache — avoid paying for the same AI call twice
CACHE_LLM "Write a cold email for a SaaS product" "Subject: Quick question..."
CACHE_LLM_GET "Draft a cold outreach email for my software product"
CACHE_LLM_STATS                # → hit rate, estimated savings, total cached

# Memory store — per-user persistent context
MEMORY_ADD user:dhirav "Prefers Go for backend, React for frontend"
MEMORY_QUERY user:dhirav "What tech stack does this user prefer?"
# → "Based on stored context: Go backend, React frontend"

MEMORY_LIST user:dhirav         # → all memories for this user
MEMORY_DEL user:dhirav <id>     # → delete a specific memory

# Similarity search
SIMILAR_KEYS "machine learning model" LIMIT 5
```

### Engine Commands

```bash
PING                            # → PONG
INFO                            # → server stats, memory, uptime
INFO ai                         # → vector index stats, embedding model, cache hit rate

MEMORY USAGE key                # → bytes used by this key
EVICTION SCORE key              # → current eviction score (0.0 – 1.0)
EVICTION STATS                  # → policy stats, last eviction time

SAVE                            # → force AOF flush to disk
BGSAVE                          # → async background save

FLUSHDB                         # clears current database
FLUSHALL                        # clears all data
```

---

## Architecture

```
                         ┌─────────────────────────────────────────────────┐
                         │              NeuroCache Engine (Go)              │
                         │                                                  │
   Redis client  ──────► │   ┌─────────────┐    ┌────────────────────────┐ │
   or SDK               │   │  TCP Server │    │   Command Router        │ │
                         │   │  RESP proto │───►│   (standard + AI cmds)  │ │
                         │   └─────────────┘    └──────────┬──────────────┘ │
                         │                                  │                │
                         │              ┌───────────────────┼──────────────┐ │
                         │   ┌──────────▼──────┐  ┌────────▼───────┐      │ │
                         │   │   KV Store      │  │  Vector Index  │      │ │
                         │   │  (sharded map)  │  │  (HNSW / ANN)  │      │ │
                         │   └──────────┬──────┘  └────────┬───────┘      │ │
                         │              │                   │              │ │
                         │   ┌──────────▼───────────────────▼───────────┐ │ │
                         │   │              Memory Store                 │ │ │
                         │   │     (per-user context + embeddings)       │ │ │
                         │   └────────────────────┬──────────────────────┘ │ │
                         │                        │                         │ │
                         │   ┌────────────────────▼──────────────────────┐ │ │
                         │   │           AI Eviction Engine               │ │ │
                         │   │   score = freq×w1 + similarity×w2 - size×w3│ │ │
                         │   └────────────────────┬──────────────────────┘ │ │
                         │                        │                         │ │
                         │   ┌────────────────────▼──────────────────────┐ │ │
                         │   │         Persistence Layer (AOF)            │ │ │
                         │   └───────────────────────────────────────────┘ │ │
                         └─────────────────────────────────────────────────┘
                                             ▲
                                             │ HTTP / WebSocket
                                             │
                         ┌───────────────────┴─────────────────────────────┐
                         │         React Web Dashboard (Vercel)             │
                         │  stats · live commands · memory inspector · docs │
                         └──────────────────────────────────────────────────┘
```

### Module breakdown (`apps/api`)

| Module | Responsibility |
|---|---|
| `tcp/` | Raw TCP server, connection handling, backpressure |
| `resp/` | RESP protocol parser and encoder (Redis-compatible) |
| `cmd/` | Command registry, router, and dispatcher |
| `store/` | In-memory sharded KV store with atomic ops |
| `vector/` | HNSW index, cosine similarity, embedding pipeline |
| `memory/` | Per-user memory store with relevance scoring |
| `eviction/` | AI-scored eviction engine, TTL daemon |
| `persist/` | AOF writer, snapshot, crash recovery |
| `embed/` | Local ONNX embedding model runner |
| `metrics/` | Prometheus exporter, pprof endpoint |

---

## SDK Usage

### JavaScript / TypeScript

```bash
pnpm add @neurocache/client
```

```typescript
import { NeuroCache } from "@neurocache/client";

const cache = new NeuroCache({ host: "localhost", port: 6379 });

// Standard Redis operations
await cache.set("user:name", "Dhirav");
const name = await cache.get("user:name");

// Semantic cache
await cache.semanticSet(
  "best backend language for microservices",
  "Go with gRPC is the standard choice",
);
const answer = await cache.semanticGet("what language to use for building APIs");

// LLM response cache — wrap your AI calls
const result = await cache.cacheLLM(
  userPrompt,
  async () => openai.chat.completions.create({ /* ... */ }),
  { threshold: 0.88 },
);

// Per-user memory
await cache.memory.add("user:dhirav", "Prefers Go and React");
const context = await cache.memory.query("user:dhirav", "tech preferences?");
```

### Go

```go
import "github.com/neurocache/neurocache-go"

client, _ := neurocache.NewClient(&neurocache.Options{Addr: "localhost:6379"})
defer client.Close()

client.Set(ctx, "key", "value", 0)
val, _ := client.Get(ctx, "key").Result()

client.SemanticSet(ctx, "best golang framework", "Gin is fast and minimal")
result, _ := client.SemanticGet(ctx, "top go web framework")
```

---

## Configuration

Copy `.env.example` to `.env` and adjust:

```env
# Server
NEUROCACHE_PORT=6379
NEUROCACHE_HOST=0.0.0.0
NEUROCACHE_MAX_CONNECTIONS=10000

# Memory
NEUROCACHE_MAX_MEMORY=512mb
NEUROCACHE_EVICTION_POLICY=ai-smart    # ai-smart | lru | lfu | noeviction

# AI / Vector
NEUROCACHE_EMBEDDING_MODEL=minilm-l6-v2
NEUROCACHE_SEMANTIC_THRESHOLD=0.85     # similarity score for cache hit (0.0–1.0)
NEUROCACHE_VECTOR_DIM=384

# Persistence
NEUROCACHE_AOF_ENABLED=true
NEUROCACHE_AOF_FSYNC=everysec          # always | everysec | no

# Logging & metrics
NEUROCACHE_LOG_LEVEL=info
NEUROCACHE_LOG_FORMAT=json
NEUROCACHE_METRICS_PORT=9090
```

Frontend env vars live in `apps/web/.env`:

```env
VITE_API_URL=http://localhost:8080
VITE_API_WS=ws://localhost:8080/ws
```

---

## Deployment

NeuroCache splits deployment between two providers that each do one thing well:

| Target | What runs there | Why |
|---|---|---|
| **Render** | Go backend (`apps/api`) | Long-running container, persistent disk, native Go support |
| **Vercel** | React frontend (`apps/web`) | Global edge CDN, instant previews per PR |

### Backend → Render

Deploy the Go engine in `apps/api` as a Render **Web Service**.

- **Root Directory:** `apps/api`
- **Runtime:** Docker (recommended) or native Go
- **Build Command:** `go build -o bin/server ./cmd/server`
- **Start Command:** `./bin/server`
- **Instance Type:** Standard or better (in-memory store)
- **Disk:** Attach a persistent disk mounted at `/data` for AOF persistence
- **Env vars:** set `PORT`, `NEUROCACHE_MAX_MEMORY`, `NEUROCACHE_EVICTION_POLICY`, etc. in the Render dashboard

Or define everything as code with `render.yaml` at the repo root (Render Blueprint):

```yaml
services:
  - type: web
    name: neurocache-api
    runtime: docker
    rootDir: apps/api
    dockerfilePath: ./Dockerfile
    plan: standard
    disk:
      name: neurocache-data
      mountPath: /data
      sizeGB: 10
    envVars:
      - key: NEUROCACHE_MAX_MEMORY
        value: 1gb
      - key: NEUROCACHE_EVICTION_POLICY
        value: ai-smart
```

### Frontend → Vercel

Deploy the React app in `apps/web` to Vercel.

- **Root Directory:** `apps/web`
- **Framework Preset:** Vite
- **Install Command:** `pnpm install` (run from repo root)
- **Build Command:** `pnpm turbo run build --filter=web...`
- **Output Directory:** `dist`
- **Env vars:**
  - `VITE_API_URL` → your Render backend URL (e.g. `https://neurocache-api.onrender.com`)
  - `VITE_API_WS` → WebSocket endpoint for live dashboard

Vercel automatically picks up the pnpm workspace and only rebuilds `web` (plus its dependencies) on push, thanks to Turborepo's filter graph.

---

## Scripts

| Command               | Description                                 |
| --------------------- | ------------------------------------------- |
| `pnpm dev`            | Run all apps in dev mode (Turborepo)        |
| `pnpm build`          | Build all apps                              |
| `pnpm lint`           | Lint all packages                           |
| `pnpm test`           | Run tests across the monorepo               |
| `pnpm --filter web …` | Run a script in the React app only          |
| `pnpm --filter api …` | Run a script in the Go backend only         |
| `docker compose up`   | Run the engine locally via Docker           |

---

## Roadmap

### V1 — MVP *(current)*
- [x] TCP server with RESP protocol
- [x] Core KV: SET, GET, DEL, EXPIRE, TTL, INCR/DECR
- [x] Semantic cache (`SEMANTIC_SET` / `SEMANTIC_GET`)
- [x] LLM response cache (`CACHE_LLM`)
- [x] Per-user memory store (`MEMORY_ADD` / `MEMORY_QUERY`)
- [x] Docker-based local install
- [x] Turborepo monorepo with Go + React
- [x] Basic CLI tool
- [ ] React dashboard on Vercel
- [ ] Go backend on Render

### V2 — Stable *(next)*
- [ ] Persistent storage — AOF + mmap
- [ ] AI-scored eviction engine
- [ ] Hash and List data types
- [ ] Improved HNSW vector index
- [ ] Multi-user / multi-app namespace isolation
- [ ] JavaScript + Go SDKs (published)
- [ ] Prometheus metrics endpoint
- [ ] Structured logging with OpenTelemetry

### V3 — Scale *(future)*
- [ ] Replication — master → replica sync
- [ ] Cluster mode — hash slots, horizontal scaling
- [ ] Pub/Sub system
- [ ] Auto cache learning (pattern detection)
- [ ] SIMD-accelerated vector ops
- [ ] Optional cloud sync (team collaboration)

---

## How it works

### Semantic caching

When you call `SEMANTIC_SET`, NeuroCache:
1. Generates a vector embedding of your key text (via local ONNX model)
2. Stores the KV entry as normal
3. Inserts the embedding into the HNSW vector index

When you call `SEMANTIC_GET`:
1. Generates an embedding for your query
2. Searches the HNSW index for the nearest neighbor
3. If similarity score ≥ threshold (default 0.85), returns the cached value
4. Otherwise, returns a cache miss

```
Query text → ONNX embed model → [0.12, -0.34, 0.87, …] (384 dims)
                                              ↓
                                    HNSW index search
                                              ↓
                               cosine_similarity(q, stored) ≥ 0.85?
                                    yes → return cached value
                                    no  → cache miss
```

### AI eviction scoring

Each entry is scored continuously:

```
eviction_score = (access_frequency   × 0.40)
               + (semantic_centrality × 0.35)
               - (memory_bytes_used   × 0.25)

# Lower score = evict first
```

### Memory store

The memory store is a per-namespace collection of text entries, each with its embedding. On `MEMORY_QUERY`, it runs a top-k semantic search over that user's memories and returns a synthesized context string — ready to inject into your LLM prompt.

---

## Benchmarks (target)

| Operation | Target latency | Notes |
|---|---|---|
| `GET` (hit) | < 0.1ms | pure in-memory |
| `SET` | < 0.2ms | with AOF everysec |
| `SEMANTIC_GET` (cache hit) | < 5ms | HNSW search + embed lookup |
| `SEMANTIC_GET` (cache miss) | < 50ms | includes embedding generation |
| `MEMORY_QUERY` | < 10ms | top-k semantic recall |

---
