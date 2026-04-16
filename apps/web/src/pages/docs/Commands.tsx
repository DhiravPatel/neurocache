import { Code } from "../../components/Code";

export default function Commands() {
  return (
    <>
      <h1>Commands Reference</h1>
      <p className="lead">
        Every command is available over both RESP (port 6379) and HTTP
        (JSON on port 8080). The RESP syntax below works with
        <code> redis-cli </code> and any existing Redis client.
      </p>

      <h2>Core KV (Redis-compatible)</h2>
      <table>
        <thead><tr><th>Command</th><th>Description</th></tr></thead>
        <tbody>
          <tr><td><code>SET key value [EX seconds]</code></td><td>Set key to value with optional TTL.</td></tr>
          <tr><td><code>GET key</code></td><td>Fetch value (nil if missing or expired).</td></tr>
          <tr><td><code>DEL key [key ...]</code></td><td>Delete one or more keys. Returns count removed.</td></tr>
          <tr><td><code>EXISTS key [key ...]</code></td><td>Count of keys that exist.</td></tr>
          <tr><td><code>EXPIRE key seconds</code></td><td>Set / update the TTL on an existing key.</td></tr>
          <tr><td><code>TTL key</code></td><td>Remaining seconds (-1 = no expiry, -2 = missing).</td></tr>
          <tr><td><code>INCR key</code> / <code>DECR key</code></td><td>Atomic counter.</td></tr>
          <tr><td><code>INCRBY key delta</code></td><td>Atomic counter by arbitrary int.</td></tr>
          <tr><td><code>KEYS</code></td><td>List all keys (blocking scan).</td></tr>
          <tr><td><code>FLUSHDB</code> / <code>FLUSHALL</code></td><td>Clear everything.</td></tr>
          <tr><td><code>PING</code></td><td>Returns <code>PONG</code>.</td></tr>
          <tr><td><code>INFO</code></td><td>Server metadata.</td></tr>
        </tbody>
      </table>

      <h2>Semantic cache</h2>
      <table>
        <thead><tr><th>Command</th><th>Description</th></tr></thead>
        <tbody>
          <tr><td><code>SEMANTIC_SET key value</code></td><td>Store value keyed by the meaning of <code>key</code>.</td></tr>
          <tr><td><code>SEMANTIC_GET query</code></td><td>Return the stored value whose key is most similar to <code>query</code>, above the configured threshold.</td></tr>
        </tbody>
      </table>
      <p>
        The similarity threshold defaults to <code>NEUROCACHE_SEMANTIC_THRESHOLD</code> (0.75)
        and can be overridden per-request on the HTTP API.
      </p>

      <h2>LLM response cache</h2>
      <table>
        <thead><tr><th>Command</th><th>Description</th></tr></thead>
        <tbody>
          <tr><td><code>CACHE_LLM prompt response</code></td><td>Cache an LLM response keyed by the prompt.</td></tr>
          <tr><td><code>CACHE_LLM_GET prompt</code></td><td>Return a cached response for a semantically similar prompt (default threshold 0.88).</td></tr>
          <tr><td><code>CACHE_LLM_STATS</code></td><td>Return hit rate, miss count, and cache size.</td></tr>
        </tbody>
      </table>

      <h2>Per-user memory</h2>
      <table>
        <thead><tr><th>Command</th><th>Description</th></tr></thead>
        <tbody>
          <tr><td><code>MEMORY_ADD user text</code></td><td>Append a memory for this user. Embedding is computed automatically.</td></tr>
          <tr><td><code>MEMORY_QUERY user query</code></td><td>Return a synthesized context string from top-k memories matching <code>query</code>.</td></tr>
          <tr><td><code>MEMORY_LIST user</code></td><td>Return all memories for this user.</td></tr>
        </tbody>
      </table>

      <h2>HTTP API equivalents</h2>
      <p>Every command is also a JSON endpoint. A few examples:</p>
      <Code lang="bash">{`# SET
curl -X POST http://localhost:8080/api/kv \\
  -H 'Content-Type: application/json' \\
  -d '{"key":"greeting","value":"hello","ttl":3600}'

# SEMANTIC_GET with custom threshold
curl "http://localhost:8080/api/semantic?q=top+backend+language&threshold=0.8"

# MEMORY_QUERY
curl "http://localhost:8080/api/memory/dhirav?q=tech+stack&k=5"

# Run any command via /api/exec (for tooling / dashboard Playground)
curl -X POST http://localhost:8080/api/exec \\
  -H 'Content-Type: application/json' \\
  -d '{"command":"MEMORY_ADD","args":["user:abc","Likes Tailwind"]}'`}</Code>

      <h2>Metrics endpoints (dashboard uses these)</h2>
      <ul>
        <li><code>GET /api/metrics/summary</code> — totals, hit rates, estimated savings, command breakdown</li>
        <li><code>GET /api/metrics/timeline</code> — rolling 60s samples (cmds/s, hits, misses, p50, p95)</li>
        <li><code>GET /api/metrics/hot-keys?k=10</code> — top-K most-read keys</li>
        <li><code>GET /api/metrics/breakdown</code> — count of each command type</li>
      </ul>
    </>
  );
}
