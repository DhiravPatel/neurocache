import { Code } from "../../components/Code";

export default function Configuration() {
  return (
    <>
      <h1>Configuration</h1>
      <p className="lead">
        All configuration is via environment variables. No config files
        required. Every setting is safe to change at container start.
      </p>

      <h2>Environment variables</h2>
      <table>
        <thead><tr><th>Variable</th><th>Default</th><th>Description</th></tr></thead>
        <tbody>
          <tr>
            <td><code>NEUROCACHE_HTTP_PORT</code></td>
            <td><code>8080</code></td>
            <td>HTTP API + embedded dashboard.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_RESP_PORT</code></td>
            <td><code>6379</code></td>
            <td>RESP TCP server for <code>redis-cli</code> and clients.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_HOST</code></td>
            <td><code>0.0.0.0</code></td>
            <td>Interface to bind on.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_MAX_MEMORY</code></td>
            <td><code>512mb</code></td>
            <td>Soft cap. When exceeded, eviction kicks in.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_EVICTION_POLICY</code></td>
            <td><code>ai-smart</code></td>
            <td>
              One of <code>ai-smart</code> (scoring by freq, recency, size),
              <code> lru</code>, <code>lfu</code>, or <code>noeviction</code>.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_EMBEDDING_DIM</code></td>
            <td><code>384</code></td>
            <td>Dimensions of the embedding vectors.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_SEMANTIC_THRESHOLD</code></td>
            <td><code>0.75</code></td>
            <td>
              Cosine similarity threshold for <code>SEMANTIC_GET</code>.
              Per-request override available via HTTP.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_DATA_DIR</code></td>
            <td><code>/data</code></td>
            <td>Persistence directory (AOF + snapshots, when enabled).</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_AOF_ENABLED</code></td>
            <td><code>false</code></td>
            <td>
              Append-only file persistence. When enabled, every write is
              appended to <code>append.aof</code> and replayed on startup.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_AOF_FSYNC</code></td>
            <td><code>everysec</code></td>
            <td>
              AOF fsync cadence: <code>always</code> (slowest, safest),{" "}
              <code>everysec</code> (balanced), or <code>no</code> (OS-managed).
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_RDB_ENABLED</code></td>
            <td><code>false</code></td>
            <td>
              Periodic gzipped-JSON snapshot written to{" "}
              <code>dump.rdb</code>. Safe to run alongside AOF.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_RDB_INTERVAL_SEC</code></td>
            <td><code>300</code></td>
            <td>Seconds between scheduled RDB snapshots. 0 = manual only.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_REQUIREPASS</code></td>
            <td><em>(unset)</em></td>
            <td>
              Legacy password gate — sets the password on the{" "}
              <code>default</code> user. For richer ACLs, use{" "}
              <code>NEUROCACHE_ACL_FILE</code>.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_ACL_FILE</code></td>
            <td><em>(unset)</em></td>
            <td>
              Path to a Redis-format <code>users.acl</code> file. Overrides
              the default of <code>&lt;DATA_DIR&gt;/users.acl</code>.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_PROTECTED_MODE</code></td>
            <td><code>false</code></td>
            <td>Reject commands from unauthenticated clients.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_SLOWLOG_THRESHOLD_US</code></td>
            <td><code>10000</code></td>
            <td>
              Commands slower than this (microseconds) enter the{" "}
              <code>SLOWLOG</code>.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_SLOWLOG_MAX_LEN</code></td>
            <td><code>128</code></td>
            <td>Ring-buffer capacity for the slow log.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_LATENCY_MAX_LEN</code></td>
            <td><code>160</code></td>
            <td>Samples kept per event name for <code>LATENCY HISTORY</code>.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_SCRIPT_TIMEOUT_MS</code></td>
            <td><code>5000</code></td>
            <td>
              Wall-clock ceiling for <code>EVAL</code> / <code>EVALSHA</code>{" "}
              scripts.
            </td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_LOG_LEVEL</code></td>
            <td><code>info</code></td>
            <td><code>debug | info | warn | error</code>.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_LOG_FORMAT</code></td>
            <td><code>text</code></td>
            <td><code>text</code> for humans, <code>json</code> for log aggregators.</td>
          </tr>
          <tr>
            <td><code>NEUROCACHE_CORS_ORIGINS</code></td>
            <td><code>*</code></td>
            <td>Comma-separated list of allowed origins, or <code>*</code>.</td>
          </tr>
        </tbody>
      </table>

      <h2>Example: production config</h2>
      <Code lang="bash">{`docker run -d --name neurocache \\
  -p 8080:8080 -p 6379:6379 \\
  -v neurocache-data:/data \\
  -e NEUROCACHE_MAX_MEMORY=2gb \\
  -e NEUROCACHE_EVICTION_POLICY=ai-smart \\
  -e NEUROCACHE_SEMANTIC_THRESHOLD=0.80 \\
  -e NEUROCACHE_LOG_LEVEL=info \\
  -e NEUROCACHE_LOG_FORMAT=json \\
  -e NEUROCACHE_CORS_ORIGINS=https://app.example.com,https://admin.example.com \\
  neurocache/engine:latest`}</Code>

      <h2>Eviction policies</h2>
      <p>
        NeuroCache ships with four eviction policies. The score determines
        which keys are removed first when memory is tight.
      </p>
      <table>
        <thead><tr><th>Policy</th><th>Formula</th></tr></thead>
        <tbody>
          <tr>
            <td><code>ai-smart</code> (default)</td>
            <td>
              <code>score = freq × 0.40 + recency × 0.35 − size_kb × 0.25</code>.
              Lowest scoring key evicted first.
            </td>
          </tr>
          <tr><td><code>lru</code></td><td>Evict least-recently-read key.</td></tr>
          <tr><td><code>lfu</code></td><td>Evict least-frequently-read key.</td></tr>
          <tr><td><code>noeviction</code></td><td>Never evict. Writes error when over cap.</td></tr>
        </tbody>
      </table>

      <h2>CORS</h2>
      <p>
        The dashboard is served from the same origin as the API, so CORS
        only matters when an external browser app calls the API from a
        different domain (e.g. you host NeuroCache separately from your
        web app).
      </p>
      <Code lang="bash">{`# Dev: allow everything
NEUROCACHE_CORS_ORIGINS=*

# Prod: restrict
NEUROCACHE_CORS_ORIGINS=https://app.example.com,https://admin.example.com`}</Code>
    </>
  );
}
