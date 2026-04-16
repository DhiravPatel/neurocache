import { Code } from "../../components/Code";

export default function Architecture() {
  return (
    <>
      <h1>Architecture</h1>
      <p className="lead">
        One process. One port for the product (8080), one port for Redis
        clients (6379). Everything else — dashboard, vector index,
        per-user memory, eviction scorer, metrics — is bundled into the
        same Go binary via <code>//go:embed</code>.
      </p>

      <h2>Process diagram</h2>
      <Code>{`┌─────────────────────────────────────────────────────────────┐
│                 single neurocache binary                    │
│                                                             │
│   :8080 ──►  React dashboard  (embedded via go:embed)       │
│             + HTTP API        (/api/*)                      │
│                                                             │
│   :6379 ──►  RESP server      (redis-cli, ioredis, etc.)    │
│                                                             │
│   ┌────────────────────────────────────────────────────┐    │
│   │ KV store (sharded map + TTL) ──┐                   │    │
│   │                                ├─► AI eviction     │    │
│   │ Vector index (in-memory ANN) ──┘   (freq + recency │    │
│   │                                    - size scoring) │    │
│   │ Memory store (per-user entries)                    │    │
│   │ Metrics (timeline + hot keys + savings)            │    │
│   └────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘`}</Code>

      <h2>Packages</h2>
      <table>
        <thead><tr><th>Package</th><th>Responsibility</th></tr></thead>
        <tbody>
          <tr><td><code>internal/store</code></td><td>In-memory KV with TTL and hit counters.</td></tr>
          <tr><td><code>internal/vector</code></td><td>Feature-hashed embedding + cosine index.</td></tr>
          <tr><td><code>internal/semcache</code></td><td>SEMANTIC_* and LLM response cache on top of the vector index.</td></tr>
          <tr><td><code>internal/memory</code></td><td>Per-user memory with semantic recall + context synthesis.</td></tr>
          <tr><td><code>internal/eviction</code></td><td>AI-smart / LRU / LFU scorers, victim picker.</td></tr>
          <tr><td><code>internal/metrics</code></td><td>Rolling 60s timeline, hot keys, LLM savings.</td></tr>
          <tr><td><code>internal/http</code></td><td>REST/JSON routes. Serves <code>/api/*</code> under the web root.</td></tr>
          <tr><td><code>internal/resp</code></td><td>Minimal RESP2 parser + TCP server.</td></tr>
          <tr><td><code>internal/webui</code></td><td><code>//go:embed all:dist</code> of the built React app.</td></tr>
          <tr><td><code>internal/engine</code></td><td>Wiring: constructs the store, vector index, metrics, and the eviction loop.</td></tr>
        </tbody>
      </table>

      <h2>Request paths</h2>
      <p><strong>HTTP</strong>:</p>
      <Code>{`Client → :8080 → SPA file server            (anything not /api/*)
                │
                └──► HTTP router → JSON handler → Engine`}</Code>

      <p><strong>RESP</strong>:</p>
      <Code>{`Client → :6379 → RESP parser → Command dispatcher → Engine
                                 │
                                 └─► Metrics.RecordCommand(name, dur)`}</Code>

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
        The rest of the engine (vector index, semcache, memory) is
        embedding-agnostic.
      </p>

      <h2>Vector index</h2>
      <p>
        The current implementation is an in-memory linear-scan cosine
        index with fine-grained locking. It handles tens of thousands of
        entries at sub-millisecond latency on a single node. HNSW is
        on the roadmap for V2 when entry counts grow.
      </p>

      <h2>Eviction scorer</h2>
      <p>The AI-smart scorer computes, per entry:</p>
      <Code>{`score = freq × 0.40 + recency × 0.35 − size_kb × 0.25

freq     = running hit count (atomic)
recency  = 1 / (1 + age_seconds / 3600)   // decays ~1h half-life
size_kb  = bytes(entry) / 1024`}</Code>
      <p>
        Lowest-scoring entries are evicted first when the store exceeds
        <code> NEUROCACHE_MAX_MEMORY</code>.
      </p>

      <h2>Metrics collection</h2>
      <p>
        A background goroutine ticks every second and snapshots per-command
        counts, per-cache hits / misses, and latency histogram samples
        into a rolling 60-point ring buffer. Reads are lock-free on the
        hot path (atomic counters); the aggregator holds the series mutex
        briefly when it writes a new sample.
      </p>
    </>
  );
}
