import { Code } from "../../components/Code";

type Row = { cmd: string; desc: string };

function CmdTable({ rows }: { rows: Row[] }) {
  return (
    <table>
      <thead>
        <tr>
          <th>Command</th>
          <th>Description</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.cmd}>
            <td>
              <code>{r.cmd}</code>
            </td>
            <td>{r.desc}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function Commands() {
  return (
    <>
      <h1>Commands Reference</h1>
      <p className="lead">
        Every command is available over both RESP (port <code>6379</code>) and
        HTTP/JSON (port <code>8080</code>). The RESP syntax below works with
        <code> redis-cli</code>, <code>ioredis</code>, <code>go-redis</code>,
        <code> redis-py</code>, and any other Redis client.
      </p>

      <div className="my-6 rounded-lg border border-border bg-white/5 px-4 py-3 text-sm">
        <strong>~146 commands</strong> across <strong>7 data types</strong> +
        AI-native extensions. Organized below by group. Jump to:{" "}
        <a href="#connection">Connection</a> · <a href="#keys">Keys/TTL</a> ·{" "}
        <a href="#strings">Strings</a> · <a href="#lists">Lists</a> ·{" "}
        <a href="#hashes">Hashes</a> · <a href="#sets">Sets</a> ·{" "}
        <a href="#zsets">Sorted Sets</a> · <a href="#streams">Streams</a> ·{" "}
        <a href="#geo">Geo</a> · <a href="#bitmaps">Bitmaps</a> ·{" "}
        <a href="#hll">HyperLogLog</a> · <a href="#pubsub">Pub/Sub</a> ·{" "}
        <a href="#tx">Transactions</a> · <a href="#persistence">Persistence</a>{" "}
        · <a href="#ai">AI-native</a> · <a href="#http">HTTP API</a>
      </div>

      {/* ── connection / server ───────────────────────────────────── */}
      <h2 id="connection">Connection &amp; Server</h2>
      <CmdTable
        rows={[
          { cmd: "PING [message]", desc: "Heartbeat. Returns PONG or the echoed message." },
          { cmd: "ECHO message", desc: "Return the message back." },
          { cmd: "SELECT 0", desc: "NeuroCache exposes a single logical database (db 0 only)." },
          { cmd: "DBSIZE", desc: "Total live key count." },
          { cmd: "INFO", desc: "Server metadata (version, uptime, memory, keys)." },
          { cmd: "TIME", desc: "Server wall-clock as [seconds, microseconds]." },
          { cmd: "COMMAND / HELLO", desc: "Minimal handshake replies (no-op compatibility stubs)." },
          { cmd: "AUTH password", desc: "Stub — accepts any password. No ACL yet." },
          { cmd: "CLIENT *", desc: "Stubbed. Returns OK so drivers that send CLIENT SETNAME don't choke." },
          { cmd: "FLUSHDB / FLUSHALL", desc: "Delete every key in the keyspace." },
          { cmd: "QUIT", desc: "Close the connection." },
        ]}
      />

      {/* ── keys / TTL ────────────────────────────────────────────── */}
      <h2 id="keys">Keys &amp; TTL</h2>
      <CmdTable
        rows={[
          { cmd: "DEL key [key ...]", desc: "Delete keys. Returns count removed." },
          { cmd: "UNLINK key [key ...]", desc: "Alias of DEL (non-blocking unlink in Redis)." },
          { cmd: "EXISTS key [key ...]", desc: "Count of keys that exist. Duplicates count." },
          { cmd: "TYPE key", desc: "Returns string | list | hash | set | zset | stream | none." },
          { cmd: "EXPIRE key seconds", desc: "Set TTL in seconds." },
          { cmd: "PEXPIRE key ms", desc: "Set TTL in milliseconds." },
          { cmd: "EXPIREAT key unix-seconds", desc: "Absolute expiry timestamp (seconds)." },
          { cmd: "PEXPIREAT key unix-ms", desc: "Absolute expiry timestamp (milliseconds)." },
          { cmd: "PERSIST key", desc: "Clear the TTL; key stops expiring." },
          { cmd: "TTL key", desc: "Remaining seconds. -1 = no expiry, -2 = missing." },
          { cmd: "PTTL key", desc: "Remaining milliseconds." },
          { cmd: "KEYS pattern", desc: "Glob-matched key list. Supports *, ?, [abc]." },
          { cmd: "RENAME src dst", desc: "Atomic rename. Overwrites dst." },
          { cmd: "RENAMENX src dst", desc: "Rename only if dst doesn't exist." },
          { cmd: "SCAN cursor [MATCH pat] [COUNT n] [TYPE t]", desc: "Cursor-based keyspace scan." },
          { cmd: "RANDOMKEY", desc: "Return an arbitrary live key." },
        ]}
      />

      {/* ── strings ──────────────────────────────────────────────── */}
      <h2 id="strings">Strings</h2>
      <CmdTable
        rows={[
          { cmd: "SET key value [EX sec | PX ms] [NX | XX]", desc: "Set value with optional TTL and NX/XX conditional flags." },
          { cmd: "SETNX key value", desc: "Set only if key doesn't exist." },
          { cmd: "SETEX key seconds value", desc: "Set with TTL in seconds." },
          { cmd: "PSETEX key ms value", desc: "Set with TTL in milliseconds." },
          { cmd: "GET key", desc: "Return the value (nil if missing)." },
          { cmd: "GETSET key value", desc: "Atomic swap: store new value, return previous." },
          { cmd: "MSET k v [k v ...]", desc: "Set many key/value pairs atomically." },
          { cmd: "MSETNX k v [k v ...]", desc: "Set many only if NONE of the keys exist." },
          { cmd: "MGET key [key ...]", desc: "Return values in order; missing keys come back as nil." },
          { cmd: "APPEND key value", desc: "Append to string, return new length." },
          { cmd: "STRLEN key", desc: "Byte length of the string." },
          { cmd: "GETRANGE key start end / SUBSTR ...", desc: "Substring by byte range (supports negatives)." },
          { cmd: "SETRANGE key offset value", desc: "Overwrite bytes starting at offset (zero-pads as needed)." },
          { cmd: "INCR key / DECR key", desc: "Atomic +/-1 integer counter." },
          { cmd: "INCRBY key delta / DECRBY key delta", desc: "Atomic counter by any int." },
          { cmd: "INCRBYFLOAT key delta", desc: "Atomic floating-point counter." },
        ]}
      />

      {/* ── lists ────────────────────────────────────────────────── */}
      <h2 id="lists">Lists</h2>
      <CmdTable
        rows={[
          { cmd: "LPUSH key value [value ...]", desc: "Prepend one or more values. Returns new length." },
          { cmd: "RPUSH key value [value ...]", desc: "Append one or more values." },
          { cmd: "LPUSHX key value / RPUSHX key value", desc: "Push only if the key already exists." },
          { cmd: "LPOP key / RPOP key", desc: "Pop from head / tail." },
          { cmd: "LLEN key", desc: "Length of the list." },
          { cmd: "LINDEX key index", desc: "Element at index (supports negative indices)." },
          { cmd: "LRANGE key start stop", desc: "Elements in [start, stop]. -1 = last." },
          { cmd: "LSET key index value", desc: "Overwrite element at index." },
          { cmd: "LREM key count value", desc: "Remove |count| occurrences. count<0 walks from tail, 0 = all." },
          { cmd: "LTRIM key start stop", desc: "Truncate list to the [start, stop] slice." },
          { cmd: "LINSERT key BEFORE|AFTER pivot value", desc: "Insert relative to pivot." },
          { cmd: "RPOPLPUSH src dst", desc: "Pop from src tail, push to dst head, atomically." },
        ]}
      />

      {/* ── hashes ───────────────────────────────────────────────── */}
      <h2 id="hashes">Hashes</h2>
      <CmdTable
        rows={[
          { cmd: "HSET key field value [field value ...]", desc: "Set one or more fields. Returns count of NEW fields." },
          { cmd: "HMSET key f v [f v ...]", desc: "Legacy multi-set form; replies OK." },
          { cmd: "HSETNX key field value", desc: "Set field only if it doesn't exist." },
          { cmd: "HGET key field", desc: "Return one field's value." },
          { cmd: "HMGET key field [field ...]", desc: "Return multiple fields." },
          { cmd: "HGETALL key", desc: "Return all field/value pairs." },
          { cmd: "HDEL key field [field ...]", desc: "Delete fields. Returns count removed." },
          { cmd: "HEXISTS key field", desc: "1 if field exists, 0 otherwise." },
          { cmd: "HLEN key", desc: "Number of fields." },
          { cmd: "HKEYS key / HVALS key", desc: "Return all field names / all values." },
          { cmd: "HINCRBY key field delta", desc: "Atomic integer field increment." },
          { cmd: "HINCRBYFLOAT key field delta", desc: "Atomic float field increment." },
          { cmd: "HSTRLEN key field", desc: "Byte length of a field's value." },
          { cmd: "HSCAN key cursor [MATCH pat] [COUNT n]", desc: "Cursor-based field iteration." },
        ]}
      />

      {/* ── sets ─────────────────────────────────────────────────── */}
      <h2 id="sets">Sets</h2>
      <CmdTable
        rows={[
          { cmd: "SADD key member [member ...]", desc: "Add members. Returns count of NEW members." },
          { cmd: "SREM key member [member ...]", desc: "Remove members. Returns count actually removed." },
          { cmd: "SISMEMBER key member", desc: "1 if member is in the set." },
          { cmd: "SMEMBERS key", desc: "All members." },
          { cmd: "SCARD key", desc: "Set cardinality." },
          { cmd: "SPOP key", desc: "Remove and return a random member." },
          { cmd: "SRANDMEMBER key [count]", desc: "Random member(s) without removing. Negative count allows repeats." },
          { cmd: "SMOVE src dst member", desc: "Atomically move member from src to dst." },
          { cmd: "SINTER key [key ...]", desc: "Intersection of sets." },
          { cmd: "SUNION key [key ...]", desc: "Union of sets." },
          { cmd: "SDIFF key [key ...]", desc: "Members in the first set absent from the rest." },
          { cmd: "SINTERSTORE / SUNIONSTORE / SDIFFSTORE dst key [key ...]", desc: "Store the result into dst. Returns size." },
          { cmd: "SSCAN key cursor [MATCH pat] [COUNT n]", desc: "Cursor-based member iteration." },
        ]}
      />

      {/* ── sorted sets ──────────────────────────────────────────── */}
      <h2 id="zsets">Sorted Sets</h2>
      <p>
        Backed by a proper skiplist — O(log n) insert/delete/rank, O(log n + k)
        range scans. Ordering is (score asc, member asc), matching Redis.
      </p>
      <CmdTable
        rows={[
          { cmd: "ZADD key score member [score member ...]", desc: "Insert or update members." },
          { cmd: "ZSCORE key member", desc: "Score of member (nil if absent)." },
          { cmd: "ZREM key member [member ...]", desc: "Remove members." },
          { cmd: "ZCARD key", desc: "Number of members." },
          { cmd: "ZINCRBY key delta member", desc: "Increment member's score." },
          { cmd: "ZRANK key member / ZREVRANK key member", desc: "0-based rank, ascending / descending." },
          { cmd: "ZRANGE key start stop [WITHSCORES]", desc: "Members by index." },
          { cmd: "ZREVRANGE key start stop [WITHSCORES]", desc: "Reverse-order slice." },
          { cmd: "ZRANGEBYSCORE key min max [WITHSCORES] [LIMIT off count]", desc: "Score-bounded range. Supports -inf, +inf, and (exclusive bounds." },
          { cmd: "ZREVRANGEBYSCORE key max min [...]", desc: "Reverse score range." },
          { cmd: "ZCOUNT key min max", desc: "Count members with score in range." },
          { cmd: "ZPOPMIN key / ZPOPMAX key", desc: "Remove and return lowest/highest-scoring member." },
          { cmd: "ZSCAN key cursor [MATCH pat] [COUNT n]", desc: "Cursor-based iteration with scores." },
        ]}
      />

      {/* ── streams ──────────────────────────────────────────────── */}
      <h2 id="streams">Streams</h2>
      <p>
        Append-only log with auto-generated IDs (<code>ms-seq</code>). Supports
        server-side trimming and non-blocking reads; <code>XREAD BLOCK</code>{" "}
        is available over RESP.
      </p>
      <CmdTable
        rows={[
          { cmd: "XADD key [MAXLEN [~|=] N] * field value [field value ...]", desc: "Append an entry; * auto-generates the ID." },
          { cmd: "XLEN key", desc: "Number of entries in the stream." },
          { cmd: "XRANGE key start end [COUNT n]", desc: "Entries with IDs in [start, end]. Use -/+ for min/max." },
          { cmd: "XREVRANGE key end start [COUNT n]", desc: "Reverse iteration." },
          { cmd: "XDEL key id [id ...]", desc: "Remove specific entries by ID." },
          { cmd: "XTRIM key MAXLEN [~|=] N", desc: "Cap the stream at N entries; returns removed count." },
          { cmd: "XREAD [COUNT n] [BLOCK ms] STREAMS key [...] id [...]", desc: "Read entries newer than the given IDs; optionally block." },
        ]}
      />

      {/* ── geo ──────────────────────────────────────────────────── */}
      <h2 id="geo">Geo</h2>
      <p>
        Geo is layered on top of sorted sets: the 52-bit interleaved geohash
        becomes the score, giving ~0.6 m precision and ZRANGE-style ordering.
      </p>
      <CmdTable
        rows={[
          { cmd: "GEOADD key lon lat member [lon lat member ...]", desc: "Add geo points." },
          { cmd: "GEOPOS key member [member ...]", desc: "Return [lon, lat] for each member (nil for missing)." },
          { cmd: "GEODIST key a b [m|km|mi|ft]", desc: "Distance between two members." },
          { cmd: "GEOSEARCH key FROMLONLAT lon lat BYRADIUS r unit [COUNT n]", desc: "Members within a radius of a point." },
          { cmd: "GEOHASH key member [member ...]", desc: "Standard 11-char base32 geohash per member." },
        ]}
      />

      {/* ── bitmaps ──────────────────────────────────────────────── */}
      <h2 id="bitmaps">Bitmaps</h2>
      <CmdTable
        rows={[
          { cmd: "SETBIT key offset 0|1", desc: "Set the bit at offset; returns previous bit value." },
          { cmd: "GETBIT key offset", desc: "Read a single bit (0/1)." },
          { cmd: "BITCOUNT key [start end]", desc: "Number of set bits in range." },
          { cmd: "BITPOS key bit [start [end]]", desc: "Byte-level position of first 0/1 bit (or -1)." },
          { cmd: "BITOP AND|OR|XOR|NOT dst key [key ...]", desc: "Bitwise op across source strings, write to dst." },
        ]}
      />

      {/* ── HyperLogLog ──────────────────────────────────────────── */}
      <h2 id="hll">HyperLogLog</h2>
      <p>
        14-bit precision (16384 registers, ~12 KiB/key), ~0.81% standard error.
        Uses FNV-1a hashing with splitmix64 avalanche for reliable bit
        distribution.
      </p>
      <CmdTable
        rows={[
          { cmd: "PFADD key [element ...]", desc: "Add elements. Returns 1 if internal state changed." },
          { cmd: "PFCOUNT key [key ...]", desc: "Cardinality estimate. Multiple keys = union cardinality." },
          { cmd: "PFMERGE dst src [src ...]", desc: "Merge source HLLs into dst." },
        ]}
      />

      {/* ── pub/sub ──────────────────────────────────────────────── */}
      <h2 id="pubsub">Pub/Sub</h2>
      <p>
        Connections enter subscribed mode after SUBSCRIBE/PSUBSCRIBE and accept
        only (P)SUBSCRIBE / (P)UNSUBSCRIBE / PING / QUIT until they unsubscribe.
        Slow subscribers drop messages instead of blocking publishers.
      </p>
      <CmdTable
        rows={[
          { cmd: "SUBSCRIBE channel [channel ...]", desc: "Subscribe to one or more channels." },
          { cmd: "UNSUBSCRIBE [channel ...]", desc: "Leave channels (or all if no arg)." },
          { cmd: "PSUBSCRIBE pattern [pattern ...]", desc: "Glob-pattern subscription." },
          { cmd: "PUNSUBSCRIBE [pattern ...]", desc: "Leave patterns." },
          { cmd: "PUBLISH channel message", desc: "Fan message out. Returns receiver count." },
          { cmd: "PUBSUB CHANNELS [pattern]", desc: "Active channels matching pattern." },
          { cmd: "PUBSUB NUMSUB [channel ...]", desc: "Subscriber counts per channel." },
          { cmd: "PUBSUB NUMPAT", desc: "Number of active pattern subscriptions." },
        ]}
      />
      <p>
        <strong>Keyspace notifications:</strong> every write fires{" "}
        <code>__keyspace__:&lt;key&gt;</code> and{" "}
        <code>__keyevent__:&lt;event&gt;</code> messages automatically. Subscribe
        to those channels to watch mutations in real time.
      </p>

      {/* ── transactions ─────────────────────────────────────────── */}
      <h2 id="tx">Transactions</h2>
      <p>
        Optimistic concurrency via per-key version counters. Commands issued
        between MULTI and EXEC are QUEUED; EXEC runs them atomically unless a
        WATCHed key was touched by another connection.
      </p>
      <CmdTable
        rows={[
          { cmd: "MULTI", desc: "Begin a transaction. Nested MULTI errors." },
          { cmd: "EXEC", desc: "Run queued commands, or return (nil) if a WATCHed key changed." },
          { cmd: "DISCARD", desc: "Abandon the queued transaction." },
          { cmd: "WATCH key [key ...]", desc: "Mark keys for optimistic locking (must precede MULTI)." },
          { cmd: "UNWATCH", desc: "Clear all WATCHed keys." },
        ]}
      />

      {/* ── persistence ──────────────────────────────────────────── */}
      <h2 id="persistence">Persistence</h2>
      <p>
        Enable AOF with <code>NEUROCACHE_AOF_ENABLED=true</code>, RDB with{" "}
        <code>NEUROCACHE_RDB_ENABLED=true</code>. If AOF is enabled it is the
        sole source of truth on startup; RDB is still written periodically as
        a fast backup.
      </p>
      <CmdTable
        rows={[
          { cmd: "SAVE / BGSAVE", desc: "Write an RDB snapshot (gzipped JSON) to NEUROCACHE_DATA_DIR/dump.rdb." },
          { cmd: "BGREWRITEAOF", desc: "Rebuild append.aof from the live keyspace (atomic rename)." },
          { cmd: "LASTSAVE", desc: "Unix timestamp of the most recent snapshot." },
        ]}
      />

      {/* ── AI-native ────────────────────────────────────────────── */}
      <h2 id="ai">AI-native</h2>
      <p>
        NeuroCache extensions not present in Redis. Each semantic command uses
        384-dim feature-hashed embeddings with cosine similarity.
      </p>
      <CmdTable
        rows={[
          { cmd: "SEMANTIC_SET key value", desc: "Store value keyed by the meaning of key." },
          { cmd: "SEMANTIC_GET query", desc: "Return the value whose key is most similar, above the configured threshold." },
          { cmd: "CACHE_LLM prompt response", desc: "Cache an LLM response keyed by the prompt." },
          { cmd: "CACHE_LLM_GET prompt", desc: "Return a cached response for a semantically similar prompt (default threshold 0.88)." },
          { cmd: "CACHE_LLM_STATS", desc: "Hit rate, miss count, cache size, estimated USD savings." },
          { cmd: "MEMORY_ADD user text", desc: "Append a long-lived memory for a user. Embedding is computed automatically." },
          { cmd: "MEMORY_QUERY user query", desc: "Return a synthesized context string from top-k semantic matches." },
          { cmd: "MEMORY_LIST user", desc: "Return every stored memory for the user (HTTP only)." },
        ]}
      />

      {/* ── HTTP ─────────────────────────────────────────────────── */}
      <h2 id="http">HTTP API</h2>
      <p>Every command is also available as JSON. A few examples:</p>
      <Code lang="bash">{`# Basic KV
curl -X POST http://localhost:8080/api/kv \\
  -H 'Content-Type: application/json' \\
  -d '{"key":"greeting","value":"hello","ttl":3600}'

# Semantic cache with custom threshold
curl "http://localhost:8080/api/semantic?q=top+backend+language&threshold=0.8"

# Per-user memory
curl "http://localhost:8080/api/memory/dhirav?q=tech+stack&k=5"

# Run any command (any data type) via /api/exec
curl -X POST http://localhost:8080/api/exec \\
  -H 'Content-Type: application/json' \\
  -d '{"command":"ZADD","args":["leaderboard","100","alice","85","bob"]}'

curl -X POST http://localhost:8080/api/exec \\
  -H 'Content-Type: application/json' \\
  -d '{"command":"XADD","args":["events","*","type","login","user","alice"]}'

curl -X POST http://localhost:8080/api/exec \\
  -H 'Content-Type: application/json' \\
  -d '{"command":"GEOADD","args":["stores","-73.9857","40.7484","nyc"]}'`}</Code>

      <h2>Metrics endpoints</h2>
      <p>
        The dashboard reads these directly — handy for building your own
        observability panels.
      </p>
      <ul>
        <li>
          <code>GET /api/metrics/summary</code> — totals, hit rates, estimated
          savings, command breakdown
        </li>
        <li>
          <code>GET /api/metrics/timeline</code> — rolling 60s samples (cmds/s,
          hits, misses, p50, p95)
        </li>
        <li>
          <code>GET /api/metrics/hot-keys?k=10</code> — top-K most-read keys
        </li>
        <li>
          <code>GET /api/metrics/breakdown</code> — count of each command type
        </li>
      </ul>

      <h2>Not yet implemented</h2>
      <p>
        Some Redis features are intentionally out of scope for now. If you
        need any of these, open an issue:
      </p>
      <ul>
        <li>
          <strong>Stream consumer groups:</strong> XGROUP, XREADGROUP, XACK,
          XCLAIM, XAUTOCLAIM, XPENDING, XINFO
        </li>
        <li>
          <strong>Blocking commands:</strong> BLPOP, BRPOP, BLMOVE, BLMPOP,
          BZPOPMIN/MAX
        </li>
        <li>
          <strong>Advanced sorted-set ops:</strong> ZUNIONSTORE, ZINTERSTORE,
          ZDIFFSTORE, ZRANGEBYLEX, ZRANGESTORE
        </li>
        <li>
          <strong>Scripting:</strong> EVAL, EVALSHA, FUNCTION, FCALL (requires
          an embedded Lua VM)
        </li>
        <li>
          <strong>Cluster / Replication:</strong> CLUSTER *, REPLICAOF, ROLE,
          FAILOVER
        </li>
        <li>
          <strong>ACL:</strong> ACL SETUSER, USERS, GETUSER, CAT, WHOAMI
        </li>
      </ul>
    </>
  );
}
