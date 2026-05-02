import { Code } from "../../components/Code";
import { Link } from "react-router-dom";

export default function Architecture() {
  return (
    <>
      <h1>Architecture</h1>
      <p className="lead">
        One process. One port for the product (8080), one port for Redis
        clients (6379). Everything else — dashboard, vector index,
        per-user memory, eviction scorer, metrics, replication,
        cluster gossip — is bundled into the same Go binary via{" "}
        <code>//go:embed</code>.
      </p>

      <h2>Process diagram</h2>
      <Code>{`┌─────────────────────────────────────────────────────────────────┐
│                  single neurocache binary                       │
│                                                                 │
│   :8080 ──►  React dashboard  (embedded via go:embed)           │
│             + HTTP API        (/api/*)                          │
│                                                                 │
│   :6379 ──►  RESP2/3 server   (redis-cli, ioredis, go-redis…)   │
│   :16379 ──► Cluster gossip   (peer discovery / failure detect) │
│                                                                 │
│   ┌──────────────────────────────────────────────────────────┐  │
│   │ Engine — wires every subsystem                           │  │
│   │                                                          │  │
│   │ ┌──────────────────────────────────────────────────────┐ │  │
│   │ │ Sharded keyspace (256 shards × RWMutex)              │ │  │
│   │ │   - 12 data types (string/list/hash/set/zset/        │ │  │
│   │ │     stream + bitmap/HLL/geo + JSON/timeseries/       │ │  │
│   │ │     vector set)                                      │ │  │
│   │ │   - per-key TTL + lazy expirer                       │ │  │
│   │ │   - eviction scorer (ai-smart / lru / lfu)           │ │  │
│   │ └──────────────────────────────────────────────────────┘ │  │
│   │                                                          │  │
│   │ Vector index (FLAT + HNSW, cosine/L2/IP) ──┐             │  │
│   │ Semantic + LLM cache (cosine threshold) ───┤             │  │
│   │ Per-user memory (synthesized recall) ──────┤             │  │
│   │ AI stack — EMB / CONV / PROMPT ────────────┤             │  │
│   │ Pub/sub broker (channels + sharded)        │             │  │
│   │ Stack modules (JSON / Bloom / TimeSeries / Search)       │  │
│   │ Replication (master fan-out, replica replay)             │  │
│   │ Cluster (16384 slots, gossip bus, MOVED/ASK)             │  │
│   │ ACL (22 categories, key + channel patterns)              │  │
│   │ Persistence (AOF + RDB, async fsync)                     │  │
│   │ Metrics (rolling timeline + hot keys + savings)          │  │
│   └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘`}</Code>

      <h2>Concurrency model</h2>
      <p>
        NeuroCache is multi-goroutine, not single-threaded like Redis.
        The keyspace is partitioned into <strong>256 shards</strong>,
        each owning its own <code>sync.RWMutex</code> + data map. A
        key's owning shard is determined by FNV-1a(key) &amp; 255 —
        single-key operations take exactly one shard's lock; cross-key
        operations (RENAME, COPY, RPOPLPUSH, etc.) take the involved
        shards in canonical (lowest-index-first) order to avoid
        deadlock; range operations (KEYS, SCAN, FLUSHALL, eviction
        snapshot) walk every shard.
      </p>
      <p>
        This was a deliberate architectural choice during the
        production-readiness audit: a single global mutex would have
        bottlenecked write-heavy multi-key workloads at high
        concurrency. With sharding, 500-client mixed SET workloads run
        at <strong>73% of Redis throughput</strong> — the remaining gap
        is Go vs C, not lock contention. See{" "}
        <a href="https://github.com/dhiravpatel/neurocache/blob/main/docs/ARCHITECTURE_AUDIT.md">
          ARCHITECTURE_AUDIT.md
        </a>{" "}
        for the verified numbers.
      </p>

      <table>
        <thead>
          <tr>
            <th>Subsystem</th>
            <th>Concurrency model</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>Connection handling</td>
            <td>goroutine-per-connection — Go scheduler handles 10k+ concurrent clients natively</td>
          </tr>
          <tr>
            <td>Keyspace mutations</td>
            <td>256 sharded RWMutexes (FNV-1a → shard); independent keys never contend</td>
          </tr>
          <tr>
            <td>Per-conn writer</td>
            <td>Per-conn <code>sync.Mutex</code>; pipelined replies stay in order</td>
          </tr>
          <tr>
            <td>AOF append</td>
            <td>Dedicated mutex, separate from store; buffered, async fsync</td>
          </tr>
          <tr>
            <td>Replication fan-out</td>
            <td>Single goroutine drains a pending buffer; slow replicas don't block writes</td>
          </tr>
          <tr>
            <td>Pub/Sub publish</td>
            <td>RLock + non-blocking <code>trySend</code>; slow subscribers drop messages, never block</td>
          </tr>
          <tr>
            <td>Eviction loop</td>
            <td>Background goroutine; snapshots all shards under read locks</td>
          </tr>
          <tr>
            <td>Keyspace notifier</td>
            <td>Inline callback under shard lock; fast path is 1 atomic + 1 channel send if anyone's blocked</td>
          </tr>
          <tr>
            <td>GC tuning</td>
            <td>
              Boot-time defaults: <code>GOGC=200</code> (collect half as often), <code>GOMEMLIMIT = MaxMemoryMB × 1.25</code> (Go 1.19+ soft heap budget). Operator-overridable.
            </td>
          </tr>
        </tbody>
      </table>

      <h2>Internal packages</h2>
      <table>
        <thead>
          <tr>
            <th>Package</th>
            <th>Responsibility</th>
          </tr>
        </thead>
        <tbody>
          <tr><td><code>internal/store</code></td><td>Sharded multi-type keyspace (12 data types) + TTL + eviction scoring fields. 256 shards × <code>RWMutex</code>.</td></tr>
          <tr><td><code>internal/vector</code></td><td>Feature-hashed embedding (default) + cosine index. Pluggable embedder via interface.</td></tr>
          <tr><td><code>internal/vectorindex</code></td><td>Reusable HNSW + FLAT primitives shared by V* type and the search module.</td></tr>
          <tr><td><code>internal/semcache</code></td><td><code>SEMANTIC_*</code> + <code>CACHE_LLM*</code> on top of the vector index.</td></tr>
          <tr><td><code>internal/memory</code></td><td>Per-user memory with semantic recall + context synthesis.</td></tr>
          <tr><td><code>internal/llmstack</code></td><td>LLM-stack primitives — embedding cache, conversation/session management, versioned prompt templates.</td></tr>
          <tr><td><code>internal/primitives</code></td><td>NeuroCache-only ops: <code>IDEMPOTENT</code>, <code>LOCK</code> with fencing tokens, <code>RATELIMIT</code>, <code>DEDUP</code>, <code>KEY.TRACK</code>, <code>AI.RECOMMEND</code>.</td></tr>
          <tr><td><code>internal/eviction</code></td><td>ai-smart / LRU / LFU scorers + victim picker.</td></tr>
          <tr><td><code>internal/blocking</code></td><td>Per-key waiter hub for BLPOP / BRPOP / BLMOVE / BZPOPMIN/MAX / XREAD BLOCK. Real condvar-style wakeups, not polling.</td></tr>
          <tr><td><code>internal/transaction</code></td><td>MULTI / EXEC / WATCH with optimistic per-key versioning.</td></tr>
          <tr><td><code>internal/scripting</code></td><td>Real Lua 5.1 (gopher-lua) for EVAL/EVALSHA + FUNCTION libraries.</td></tr>
          <tr><td><code>internal/replication</code></td><td>Master fan-out, replica dial loop, full + partial resync, replica chains.</td></tr>
          <tr><td><code>internal/cluster</code></td><td>16384-slot keyslot, gossip bus, MOVED/ASK redirection, MIGRATE.</td></tr>
          <tr><td><code>internal/sentinel</code></td><td>Sentinel mode — SDOWN/ODOWN, leader election, replica promotion.</td></tr>
          <tr><td><code>internal/modules</code></td><td>Module ABI + registry. Built-ins compile-time linked, loaded by name via <code>MODULE LOAD</code>.</td></tr>
          <tr><td><code>internal/persistence</code></td><td>AOF appender (3 fsync policies) + RDB gzipped snapshots + atomic rewrite.</td></tr>
          <tr><td><code>internal/pubsub</code></td><td>Channel + pattern + sharded broker. Non-blocking publish (drops on slow subscriber).</td></tr>
          <tr><td><code>internal/acl</code></td><td>Users + 22 categories + key/channel patterns + audit log.</td></tr>
          <tr><td><code>internal/introspect</code></td><td>SLOWLOG / LATENCY / CLIENT / MONITOR / TRACKING / HOTKEYS — observability surfaces.</td></tr>
          <tr><td><code>internal/metrics</code></td><td>Rolling 60s timeline, hot keys, LLM savings.</td></tr>
          <tr><td><code>internal/http</code></td><td>REST/JSON routes. Serves <code>/api/*</code>.</td></tr>
          <tr><td><code>internal/resp</code></td><td>RESP2/3 parser + TCP server. 64 KiB bufio buffers, pipeline-aware flush, no-copy bulk-string ingress.</td></tr>
          <tr><td><code>internal/webui</code></td><td><code>//go:embed all:dist</code> of the built React app.</td></tr>
          <tr><td><code>internal/engine</code></td><td>Wiring: constructs every subsystem above + the eviction loop + persistence.</td></tr>
        </tbody>
      </table>

      <h2>Request paths</h2>
      <p><strong>HTTP</strong>:</p>
      <Code>{`Client → :8080 → SPA file server            (anything not /api/*)
                │
                └──► HTTP router → JSON handler → Engine`}</Code>

      <p><strong>RESP</strong>:</p>
      <Code>{`Client → :6379 → RESP2/3 parser → Per-shard dispatcher → Engine
                                  │
                                  ├─► Metrics.RecordCommand(name, dur)
                                  ├─► AOF.Append (write commands only)
                                  └─► Replication.Propagate (master mode)`}</Code>

      <h2>Vector index</h2>
      <p>
        Two algorithms, same package, same wire format:
      </p>
      <ul>
        <li>
          <strong>FLAT (brute force)</strong> — exact KNN by linear scan
          + cosine. Sub-millisecond on tens of thousands of entries.
          The right choice when recall &gt; throughput and the dataset
          fits comfortably in RAM.
        </li>
        <li>
          <strong>HNSW (Hierarchical Navigable Small World)</strong> —
          approximate nearest neighbour via a layered graph. Sub-linear
          query time. Tunable <code>M</code> (graph degree) +{" "}
          <code>EFCONSTRUCTION</code> + <code>EFRUNTIME</code> to trade
          recall vs. speed. The right choice for large datasets and
          high QPS.
        </li>
      </ul>
      <p>
        Both algorithms support <code>COSINE</code>, <code>L2</code>,
        and <code>IP</code> metrics. The vector set type (<code>V*</code>)
        and the search module (<code>FT.CREATE … VECTOR</code>) both
        ride on the shared <code>internal/vectorindex</code> package.
      </p>

      <h2>Embeddings</h2>
      <p>
        The default embedding is a feature-hashed vector built from word
        unigrams and character trigrams, projected into a 384-dimensional
        space and L2-normalized. It runs locally in microseconds, has
        zero external dependencies, and is good enough to produce a clear
        hit-rate signal on real prompts.
      </p>
      <p>
        For stronger semantic quality, swap the embedding function:
      </p>
      <ul>
        <li><strong>ONNX runtime</strong> — bundle a MiniLM-L6 or BGE-small model (offline, ~100 MB).</li>
        <li><strong>OpenAI <code>text-embedding-3-small</code></strong> — cloud, needs an API key.</li>
        <li>Anything that returns a <code>[]float32</code>.</li>
      </ul>
      <p>
        The rest of the engine (vector index, semcache, memory, EMB
        cache) is embedding-agnostic.
      </p>

      <h2>Eviction scorer</h2>
      <p>The AI-smart scorer computes, per entry:</p>
      <Code>{`score = freq × 0.40 + recency × 0.35 − size_kb × 0.25

freq     = running hit count (atomic)
recency  = 1 / (1 + age_seconds / 3600)   // decays ~1h half-life
size_kb  = bytes(entry) / 1024`}</Code>
      <p>
        Lowest-scoring entries are evicted first when the store exceeds{" "}
        <code>NEUROCACHE_MAX_MEMORY</code>. Eviction runs on a
        background goroutine — snapshots all shards under read locks,
        computes scores, and deletes victims via <code>DEL</code>.
        Cost-aware caching is opt-in via <code>CACHE.WEIGH</code>:
        annotate entries with their dollar / token cost and the scorer
        factors that into the survival ranking.
      </p>

      <h2>Persistence</h2>
      <p>
        Two complementary formats, controlled by{" "}
        <code>NEUROCACHE_AOF_ENABLED</code> and{" "}
        <code>NEUROCACHE_RDB_ENABLED</code>:
      </p>
      <ul>
        <li>
          <strong>AOF (append-only file)</strong> — every write command
          appended to <code>append.aof</code>. Three fsync policies:
          <code> always</code> (durable per-op, slow), <code>everysec</code>
          {" "}(default — within-1s durability), <code>no</code>{" "}
          (OS-buffered). Replays on boot before accepting connections.
        </li>
        <li>
          <strong>RDB (snapshot)</strong> — periodic gzipped dump of the
          live keyspace via <code>BGSAVE</code> or the configured
          interval. Faster restore on million-key keyspaces.
        </li>
      </ul>
      <p>
        When both are enabled, AOF is the source of truth on startup
        (RDB is ignored — replaying AOF on top of an RDB would
        double-apply non-idempotent writes).
      </p>

      <h2>Metrics collection</h2>
      <p>
        A background goroutine ticks every second and snapshots
        per-command counts, per-cache hits / misses, and latency
        histogram samples into a rolling 60-point ring buffer. Reads are
        lock-free on the hot path (atomic counters); the aggregator
        holds the series mutex briefly when it writes a new sample.
      </p>

      <h2>Performance characteristics</h2>
      <p>
        Verified head-to-head against Redis 7.x via{" "}
        <code>scripts/bench-vs-redis.sh</code>:
      </p>
      <ul>
        <li>~70-80% of Redis throughput on standard single-key commands</li>
        <li>~73% of Redis on 500-client mixed write workloads (post-sharding)</li>
        <li>~89% of Redis on 100 KiB GET (post 64 KiB bufio + no-copy ingress)</li>
        <li>p99 ~1-3× Redis under sustained load (Go GC tax — bounded by GOGC=200 default)</li>
        <li>1000 concurrent connections: 73% of Redis SET, 83% GET, p99 within noise</li>
      </ul>
      <p>
        Full numbers + the list of every architectural risk we audited
        and either fixed or accepted live in{" "}
        <Link to="/docs/configuration">Configuration</Link> and the
        repository's{" "}
        <a href="https://github.com/dhiravpatel/neurocache/blob/main/docs/ARCHITECTURE_AUDIT.md">
          docs/ARCHITECTURE_AUDIT.md
        </a>.
      </p>
    </>
  );
}
