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
        <strong>~290 commands</strong> across <strong>11 data types</strong> +
        AI-native extensions + Stack modules. Organized below by group. Jump
        to:{" "}
        <a href="#connection">Connection</a> · <a href="#keys">Keys/TTL</a> ·{" "}
        <a href="#strings">Strings</a> · <a href="#lists">Lists</a> ·{" "}
        <a href="#hashes">Hashes</a> · <a href="#sets">Sets</a> ·{" "}
        <a href="#zsets">Sorted Sets</a> · <a href="#streams">Streams</a> ·{" "}
        <a href="#geo">Geo</a> · <a href="#bitmaps">Bitmaps</a> ·{" "}
        <a href="#hll">HyperLogLog</a> · <a href="#pubsub">Pub/Sub</a> ·{" "}
        <a href="#tx">Transactions</a> · <a href="#blocking">Blocking</a> ·{" "}
        <a href="#acl">Auth / ACL</a> · <a href="#scripting">Scripting</a> ·{" "}
        <a href="#introspect">Introspection</a> ·{" "}
        <a href="#replication">Replication</a> ·{" "}
        <a href="#cluster">Cluster</a> · <a href="#modules">Modules</a> ·{" "}
        <a href="#json">JSON</a> · <a href="#prob">Bloom / Cuckoo / CMS</a> ·{" "}
        <a href="#timeseries">TimeSeries</a> · <a href="#search">Search</a> ·{" "}
        <a href="#persistence">Persistence</a> · <a href="#ai">AI-native</a> ·{" "}
        <a href="#http">HTTP API</a>
      </div>

      {/* ── connection / server ───────────────────────────────────── */}
      <h2 id="connection">Connection &amp; Server</h2>
      <CmdTable
        rows={[
          { cmd: "PING [message]", desc: "Heartbeat. Returns PONG or the echoed message." },
          { cmd: "ECHO message", desc: "Return the message back." },
          { cmd: "SELECT 0", desc: "NeuroCache exposes a single logical database (db 0 only)." },
          { cmd: "DBSIZE", desc: "Total live key count." },
          { cmd: "INFO", desc: "Server metadata (version, uptime, memory, keys, persistence state)." },
          { cmd: "TIME", desc: "Server wall-clock as [seconds, microseconds]." },
          { cmd: "HELLO [protover [AUTH user pass] [SETNAME name]]", desc: "Server handshake; optional inline AUTH and client name." },
          { cmd: "AUTH [username] password", desc: "Authenticate as an ACL user. One-arg form targets the default user (legacy requirepass)." },
          { cmd: "RESET", desc: "Discard MULTI/WATCH state, unsubscribe from all channels, revert to the default user." },
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
          { cmd: "EXPIRETIME key", desc: "Absolute expiry as Unix seconds. -1 no TTL, -2 missing." },
          { cmd: "PEXPIRETIME key", desc: "Absolute expiry in milliseconds." },
          { cmd: "TOUCH key [key ...]", desc: "Refresh last-read time on each existing key without reading the value. Returns count touched." },
          { cmd: "KEYS pattern", desc: "Glob-matched key list. Supports *, ?, [abc]." },
          { cmd: "RENAME src dst", desc: "Atomic rename. Overwrites dst." },
          { cmd: "RENAMENX src dst", desc: "Rename only if dst doesn't exist." },
          { cmd: "SCAN cursor [MATCH pat] [COUNT n] [TYPE t]", desc: "Cursor-based keyspace scan." },
          { cmd: "RANDOMKEY", desc: "Return an arbitrary live key." },
          { cmd: "OBJECT ENCODING|IDLETIME|FREQ|REFCOUNT key", desc: "Per-key introspection: storage encoding, idle seconds, hit count." },
          { cmd: "COPY src dst [REPLACE]", desc: "Deep-copy a key. Fails if dst exists without REPLACE." },
          { cmd: "DUMP key", desc: "Serialize a key as an opaque gob+gzip blob usable with RESTORE." },
          { cmd: "RESTORE key ttl-ms blob [REPLACE]", desc: "Recreate a key from a DUMP blob. TTL 0 = no expiry." },
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
          { cmd: "LMOVE src dst LEFT|RIGHT LEFT|RIGHT", desc: "Atomic pop-then-push across all four directions; src == dst is a single-element rotate." },
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
          { cmd: "HEXPIRE / HPEXPIRE key sec FIELDS n field [...]", desc: "Per-field TTL (seconds / ms). Supports NX/XX/GT/LT conditional flags." },
          { cmd: "HEXPIREAT / HPEXPIREAT key ts FIELDS n field [...]", desc: "Absolute per-field expiry." },
          { cmd: "HTTL / HPTTL key FIELDS n field [...]", desc: "Remaining per-field TTL. -1 no TTL, -2 missing." },
          { cmd: "HEXPIRETIME / HPEXPIRETIME key FIELDS n field [...]", desc: "Absolute Unix expiry per field (s / ms)." },
          { cmd: "HPERSIST key FIELDS n field [...]", desc: "Clear per-field TTL." },
          { cmd: "HRANDFIELD key [count [WITHVALUES]]", desc: "Random field(s); negative count allows repeats." },
          { cmd: "HGETDEL key FIELDS n field [...]", desc: "Atomic read+delete on hash fields. Hash key disappears when last field is removed." },
          { cmd: "HGETEX key [EX|PX|EXAT|PXAT v|PERSIST] FIELDS n field [...]", desc: "Atomic read with per-field TTL adjust." },
          { cmd: "HSETEX key seconds [FNX|FXX] FIELDS n field value [...]", desc: "Atomic set + per-field TTL. FNX/FXX is all-or-nothing across the call." },
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
          { cmd: "ZMSCORE key member [member ...]", desc: "Parallel ZSCORE; one reply per member, nil for absent." },
          { cmd: "ZRANDMEMBER key [count [WITHSCORES]]", desc: "Random members. count > 0 unique, < 0 with replacement." },
          { cmd: "ZREMRANGEBYRANK key start stop", desc: "Remove members in [start, stop] rank range." },
          { cmd: "ZREMRANGEBYSCORE key min max", desc: "Remove members with score in range. (exclusive bounds and ±inf supported." },
          { cmd: "ZREMRANGEBYLEX key min max", desc: "Remove members in lex range; tokens accept -/+, [v, (v." },
        ]}
      />

      {/* ── streams ──────────────────────────────────────────────── */}
      <h2 id="streams">Streams</h2>
      <p>
        Append-only log with auto-generated IDs (<code>ms-seq</code>). Supports
        server-side trimming, blocking reads, and full consumer-group semantics
        with a pending-entries list (PEL) per group.
      </p>
      <CmdTable
        rows={[
          { cmd: "XADD key [MAXLEN [~|=] N] * field value [field value ...]", desc: "Append an entry; * auto-generates the ID." },
          { cmd: "XLEN key", desc: "Number of entries in the stream." },
          { cmd: "XRANGE key start end [COUNT n]", desc: "Entries with IDs in [start, end]. Use -/+ for min/max." },
          { cmd: "XREVRANGE key end start [COUNT n]", desc: "Reverse iteration." },
          { cmd: "XDEL key id [id ...]", desc: "Remove specific entries by ID." },
          { cmd: "XTRIM key MAXLEN [~|=] N", desc: "Cap the stream at N entries; returns removed count." },
          { cmd: "XREAD [COUNT n] [BLOCK ms] STREAMS key [...] id [...]", desc: "Read entries newer than the given IDs; BLOCK uses real wait/notify, not polling." },
        ]}
      />
      <h3>Consumer groups</h3>
      <p>
        Multiple consumers in the same group share an ever-advancing cursor,
        so each new entry is delivered to exactly one of them. Un-ACKed
        deliveries stay in the group&apos;s PEL and can be reclaimed by any
        consumer with <code>XCLAIM</code> / <code>XAUTOCLAIM</code>.
      </p>
      <CmdTable
        rows={[
          { cmd: "XGROUP CREATE key group id [MKSTREAM]", desc: "Create a group starting at id (0 or $). MKSTREAM auto-creates the stream." },
          { cmd: "XGROUP SETID key group id", desc: "Reset a group's last-delivered-id." },
          { cmd: "XGROUP DESTROY key group", desc: "Remove a group and every consumer under it." },
          { cmd: "XGROUP CREATECONSUMER key group consumer", desc: "Ensure a consumer exists. Returns 1 if newly created." },
          { cmd: "XGROUP DELCONSUMER key group consumer", desc: "Remove a consumer. Returns how many pending entries it owned." },
          { cmd: "XREADGROUP GROUP g c [COUNT n] [BLOCK ms] [NOACK] STREAMS key ... id ...", desc: "Read new entries (> ) or replay this consumer's PEL (any other id). NOACK skips PEL bookkeeping." },
          { cmd: "XACK key group id [id ...]", desc: "Acknowledge delivery; drops entries from the PEL." },
          { cmd: "XPENDING key group [[IDLE ms] start end count [consumer]]", desc: "Summary form (no extra args) or long form with range + optional consumer filter." },
          { cmd: "XCLAIM key group consumer min-idle-ms id [id ...] [IDLE ms] [TIME t] [RETRYCOUNT n] [FORCE] [JUSTID]", desc: "Re-assign pending entries to a new consumer." },
          { cmd: "XAUTOCLAIM key group consumer min-idle-ms start [COUNT n] [JUSTID]", desc: "Scan the PEL and bulk-claim idle entries. Returns cursor, claimed entries, and deleted IDs." },
          { cmd: "XINFO STREAM|GROUPS|CONSUMERS key [group]", desc: "Metadata: stream length + last-id, group cursors, per-consumer pending counts + idle time." },
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
          { cmd: "GEOSEARCHSTORE dest src ...search-args [STOREDIST]", desc: "Same shape as GEOSEARCH, but writes results into dest. Default keeps source geohashes; STOREDIST writes haversine distances." },
          { cmd: "GEOHASH key member [member ...]", desc: "Standard 11-char base32 geohash per member." },
          { cmd: "GEORADIUS key lon lat r unit [WITHCOORD|WITHDIST|WITHHASH] [COUNT n [ANY]] [ASC|DESC] [STORE|STOREDIST dest]", desc: "Deprecated; kept for legacy drivers. STORE/STOREDIST routes through GEOSEARCHSTORE." },
          { cmd: "GEORADIUSBYMEMBER key member r unit [...]", desc: "Same as GEORADIUS but the centre is a member's coordinates; centre is excluded from results." },
          { cmd: "GEORADIUS_RO / GEORADIUSBYMEMBER_RO ...", desc: "Read-only variants — STORE / STOREDIST options return ERR." },
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

      {/* ── blocking ─────────────────────────────────────────────── */}
      <h2 id="blocking">Blocking commands</h2>
      <p>
        Backed by a per-key waiter hub — producers fire notifications on{" "}
        <code>LPUSH</code> / <code>RPUSH</code> / <code>ZADD</code> /{" "}
        <code>XADD</code>, and blocked consumers wake immediately without
        polling. <code>timeout</code> is a float in seconds; <code>0</code>{" "}
        means wait forever.
      </p>
      <CmdTable
        rows={[
          { cmd: "BLPOP key [key ...] timeout", desc: "Pop from the head of the first non-empty list; block until one has data." },
          { cmd: "BRPOP key [key ...] timeout", desc: "Same, popping from the tail." },
          { cmd: "BLMOVE src dst LEFT|RIGHT LEFT|RIGHT timeout", desc: "Atomic pop-from-src + push-to-dst with a blocking wait." },
          { cmd: "BZPOPMIN key [key ...] timeout", desc: "Block-pop the lowest-scoring member of the first non-empty sorted set." },
          { cmd: "BZPOPMAX key [key ...] timeout", desc: "Block-pop the highest-scoring member." },
          { cmd: "XREAD ... BLOCK ms ...", desc: "See Streams. Uses the same waiter hub; replaces the older 25ms poll loop." },
          { cmd: "XREADGROUP ... BLOCK ms ...", desc: "Consumer-group read with blocking semantics." },
        ]}
      />

      {/* ── auth / ACL ───────────────────────────────────────────── */}
      <h2 id="acl">Auth &amp; ACL</h2>
      <p>
        Users, commands, categories, key patterns, and channel patterns are all
        first-class. The default user is <code>default</code> with{" "}
        <code>nopass</code> + wildcard permissions unless you set{" "}
        <code>NEUROCACHE_REQUIREPASS</code> or load a{" "}
        <code>users.acl</code> file. Set{" "}
        <code>NEUROCACHE_PROTECTED_MODE=true</code> to reject commands from
        unauthenticated clients.
      </p>
      <CmdTable
        rows={[
          { cmd: "ACL WHOAMI", desc: "Name of the user on the current connection." },
          { cmd: "ACL LIST / ACL USERS", desc: "Every user — LIST returns canonical rules, USERS returns just the names." },
          { cmd: "ACL GETUSER name", desc: "Flags, password hashes, key patterns, channel patterns, commands." },
          { cmd: "ACL SETUSER name [rule ...]", desc: "Create/update a user (see rule grammar below). Persists to users.acl." },
          { cmd: "ACL DELUSER name [name ...]", desc: "Delete users. The default user is protected." },
          { cmd: "ACL CAT [category]", desc: "List all categories, or all commands in one." },
          { cmd: "ACL LOG [count | RESET]", desc: "Recent rejections (auth-fail, command-denied, key-denied, channel-denied)." },
          { cmd: "ACL GENPASS [bits]", desc: "Mint a random hex password. Uses crypto/rand." },
          { cmd: "ACL SAVE", desc: "Flush the in-memory registry to users.acl." },
        ]}
      />
      <h3>SETUSER rule grammar</h3>
      <p>Compatible with Redis. Rules are applied in order.</p>
      <ul>
        <li><code>on</code> / <code>off</code> — enable / disable the user.</li>
        <li><code>nopass</code> / <code>resetpass</code> — accept any password / clear passwords.</li>
        <li><code>&gt;pw</code> / <code>&lt;pw</code> — add / remove a plaintext password (hashed on write).</li>
        <li><code>#hex</code> / <code>!hex</code> — add / remove an already-hashed password.</li>
        <li><code>+CMD</code> / <code>-CMD</code> — grant / revoke a single command.</li>
        <li><code>+@cat</code> / <code>-@cat</code> — grant / revoke a category.</li>
        <li><code>allcommands</code> (<code>+@all</code>) / <code>nocommands</code>.</li>
        <li><code>~pat</code> / <code>allkeys</code> / <code>resetkeys</code> — key-pattern access.</li>
        <li><code>&amp;pat</code> / <code>allchannels</code> / <code>resetchannels</code> — pub/sub channel access.</li>
        <li><code>reset</code> — wipe everything.</li>
      </ul>
      <Code lang="bash">{`# Create a read-only user scoped to the "cache:*" prefix
ACL SETUSER alice on >s3cret ~cache:* +@read
# Promote them to full access, including writes
ACL SETUSER alice +@write +@list +@set
# Revoke dangerous operations explicitly
ACL SETUSER alice -FLUSHALL -DEBUG
# Confirm
ACL GETUSER alice
AUTH alice s3cret`}</Code>

      {/* ── scripting ────────────────────────────────────────────── */}
      <h2 id="scripting">Scripting</h2>
      <p>
        Scripts run under an embedded Lua-subset interpreter with a
        configurable wall-clock deadline (<code>NEUROCACHE_SCRIPT_TIMEOUT_MS</code>).
        <code> redis.call</code> re-enters the dispatcher and re-checks ACL
        permissions, so a script can never widen its caller&apos;s grants.
      </p>
      <CmdTable
        rows={[
          { cmd: "EVAL script numkeys [key ...] [arg ...]", desc: "Run a script. KEYS and ARGV are pre-populated Lua tables (1-indexed)." },
          { cmd: "EVAL_RO script numkeys [key ...] [arg ...]", desc: "Read-only EVAL: redis.call refuses every keyspace-mutating command. Safe on read-only replicas." },
          { cmd: "EVALSHA sha1 numkeys [key ...] [arg ...]", desc: "Same but looks the script up by hash. Returns NOSCRIPT when absent." },
          { cmd: "EVALSHA_RO sha1 numkeys [key ...] [arg ...]", desc: "Read-only EVALSHA." },
          { cmd: "SCRIPT LOAD script", desc: "Precompile a script and return its sha1." },
          { cmd: "SCRIPT EXISTS sha1 [sha1 ...]", desc: "1/0 vector for whether each hash is cached." },
          { cmd: "SCRIPT FLUSH", desc: "Drop every cached script." },
          { cmd: "SCRIPT KILL / FUNCTION KILL", desc: "Wake the kill flag the EVAL/FCALL bridge polls between redis.call invocations." },
        ]}
      />
      <p>
        <strong>Supported subset:</strong> local/assignment, numbers, strings,
        booleans, nil, tables (array + hash), <code>if / elseif / else</code>,{" "}
        <code>while</code>, numeric <code>for</code>, <code>for-in</code> over
        tables, <code>return</code>, <code>break</code>, arithmetic +
        comparison + <code>..</code> concat, <code>not</code>/<code>and</code>/<code>or</code>,
        and the <code>redis.*</code> / <code>KEYS</code> / <code>ARGV</code> globals.
      </p>
      <Code lang="lua">{`-- atomic rate-limit: allow N hits per window
local n = tonumber(redis.call("INCR", KEYS[1]))
if n == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
if n > tonumber(ARGV[2]) then
  return redis.error_reply("rate_limited")
end
return n`}</Code>

      {/* ── introspection ────────────────────────────────────────── */}
      <h2 id="introspect">Introspection &amp; Operations</h2>
      <CmdTable
        rows={[
          { cmd: "CLIENT ID", desc: "Numeric ID of this connection." },
          { cmd: "CLIENT GETNAME / SETNAME name", desc: "Read or set the friendly name shown in CLIENT LIST." },
          { cmd: "CLIENT INFO", desc: "One-line summary of the current connection." },
          { cmd: "CLIENT LIST", desc: "Newline-separated summary of every connected client." },
          { cmd: "CLIENT KILL ID id", desc: "Evict a client by ID. Returns 1 / 0." },
          { cmd: "CLIENT UNBLOCK id [TIMEOUT|ERROR]", desc: "Wake a blocked client. TIMEOUT (default) replies nil; ERROR replies -UNBLOCKED. Returns 1/0 (1 = was blocked)." },
          { cmd: "CLIENT PAUSE ms / CLIENT UNPAUSE", desc: "Pause new command execution on every client for ms milliseconds." },
          { cmd: "CLIENT REPLY ON|OFF|SKIP", desc: "Silence replies for this connection (ON reverts; SKIP drops the next reply only)." },
          { cmd: "CLIENT NO-EVICT ON|OFF", desc: "Mark this connection as no-evict (advisory flag)." },
          { cmd: "HOTKEYS [count]", desc: "Top-K hot keys by estimated frequency. NeuroCache-native — replaces redis-cli --hotkeys + LFU dance with a real-time HeavyKeeper tracker fed by the engine notifier." },
          { cmd: "HOTKEYS RESET / STATS / COUNT key / THRESHOLD [min] / RESIZE k / SAMPLE [every] / ENABLE | DISABLE / HELP", desc: "Tracker management. STATS exposes config + observation counts + memory cost. SAMPLE 1 records every event; bump to thin under load." },
          { cmd: "SLOWLOG GET [count]", desc: "Most-recent slow executions (id, timestamp, micros, command, client)." },
          { cmd: "SLOWLOG LEN / SLOWLOG RESET", desc: "Entry count / wipe the ring buffer." },
          { cmd: "LATENCY LATEST", desc: "One row per event name with the most recent sample." },
          { cmd: "LATENCY HISTORY event", desc: "Every sample for an event name." },
          { cmd: "LATENCY RESET [event ...]", desc: "Clear one or every event bucket." },
          { cmd: "LATENCY DOCTOR / LATENCY GRAPH", desc: "Human-readable summary / ASCII graph." },
          { cmd: "MEMORY USAGE key", desc: "Approximate bytes held for a key." },
          { cmd: "MEMORY STATS", desc: "Heap + dataset byte counters, goroutine count." },
          { cmd: "MEMORY DOCTOR / MEMORY PURGE", desc: "Diagnostic text / runtime.GC trigger." },
        ]}
      />

      {/* ── replication ──────────────────────────────────────────── */}
      <h2 id="replication">Replication</h2>
      <p>
        Async master → replica streaming. The master appends every write
        to a fixed-size byte-offset backlog (default 1 MiB) and fans the
        bytes out to every connected replica. Replicas dial the master,
        run a <code>PING / REPLCONF / PSYNC</code> handshake, consume an
        RDB snapshot on first connect (or resume from the backlog if
        their replid+offset are still in range), and ACK their applied
        offset every second so <code>WAIT</code> can tell when N
        replicas have caught up.
      </p>
      <CmdTable
        rows={[
          { cmd: "REPLICAOF host port", desc: "Switch this node into replica mode following host:port. Drops any prior follower link." },
          { cmd: "REPLICAOF NO ONE / SLAVEOF NO ONE", desc: "Promote this node back to master. Mints a fresh replid; the previous one is preserved for partial-resync of former siblings." },
          { cmd: "SLAVEOF host port", desc: "Legacy alias of REPLICAOF." },
          { cmd: "ROLE", desc: "Reports master|slave + offset. On a master also lists every connected replica with its acknowledged offset." },
          { cmd: "WAIT numreplicas timeout-ms", desc: "Block until numreplicas have ACKed the master's current offset, or the deadline fires. Returns the count actually reached." },
          { cmd: "FAILOVER [TO host port] [TIMEOUT ms] [ABORT|FORCE]", desc: "Promote a chosen replica to master (TO form), self-promote (no args, on a replica), or cancel an in-flight failover (ABORT)." },
          { cmd: "PSYNC replid offset / SYNC", desc: "Internal handshake. Replies +FULLRESYNC <replid> <offset> and streams an RDB dump, or +CONTINUE to resume from the backlog." },
          { cmd: "REPLCONF listening-port|capa|ack|getack ...", desc: "Internal handshake / heartbeat. Replicas announce their listen port and capabilities; ACKs feed WAIT." },
        ]}
      />
      <p>
        <strong>Auto-follow at boot:</strong> set{" "}
        <code>NEUROCACHE_REPLICAOF=host:port</code> and the engine will
        dial the master before accepting client traffic. Backlog size
        and dial timeout are tunable — see{" "}
        <a href="/docs/configuration">Configuration</a>.
      </p>
      <Code lang="bash">{`# On the replica
redis-cli -p 6380 REPLICAOF localhost 6379

# On the master, after a few writes
redis-cli -p 6379 ROLE
# 1) "master"
# 2) (integer) 1428                     ← current byte offset
# 3) 1) 1) "127.0.0.1"
#       2) "6380"                       ← replica's listen port
#       3) (integer) 1428               ← replica's ACKed offset

# Block until at least 1 replica has caught up to the current offset
redis-cli -p 6379 WAIT 1 5000           # → (integer) 1`}</Code>

      {/* ── persistence ──────────────────────────────────────────── */}
      <h2 id="persistence">Persistence</h2>
      <p>
        Enable AOF with <code>NEUROCACHE_AOF_ENABLED=true</code>, RDB with{" "}
        <code>NEUROCACHE_RDB_ENABLED=true</code>. Fsync cadence for the AOF is
        controlled by <code>NEUROCACHE_AOF_FSYNC</code> (<code>always</code>,{" "}
        <code>everysec</code>, <code>no</code>). When both are enabled, AOF is
        the sole source of truth on startup; RDB still runs as a periodic
        backup and a fast cold-boot restore.
      </p>
      <CmdTable
        rows={[
          { cmd: "SAVE", desc: "Write an RDB snapshot synchronously (blocks the caller)." },
          { cmd: "BGSAVE", desc: "Same, but on a background goroutine. Returns 'Background saving started' or an error if one is already in flight." },
          { cmd: "BGREWRITEAOF", desc: "Rebuild append.aof from the live keyspace, atomically renamed. Runs in the background." },
          { cmd: "LASTSAVE", desc: "Unix timestamp of the last successful RDB write (seeded from dump.rdb mtime at boot)." },
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

      {/* ── cluster ──────────────────────────────────────────────── */}
      <h2 id="cluster">Cluster</h2>
      <p>
        16384-slot hash space, gossip-driven membership, MOVED/ASK
        redirection, MIGRATE for live rebalancing. Slot calculation is
        bit-for-bit Redis-compatible (CRC16 + <code>{`{tag}`}</code>{" "}
        extraction), so any cluster-aware client driver routes against
        a NeuroCache cluster unchanged. Enable with{" "}
        <code>NEUROCACHE_CLUSTER_ENABLED=true</code>; the gossip bus
        defaults to the RESP port + 10000.
      </p>
      <CmdTable
        rows={[
          { cmd: "CLUSTER MYID", desc: "Local node's 40-hex ID." },
          { cmd: "CLUSTER INFO", desc: "Health summary (state, slots assigned, known nodes, current epoch)." },
          { cmd: "CLUSTER NODES", desc: "Newline-separated peer table in the canonical Redis format (id host:port@bus flags master-id ping pong epoch state slot-ranges)." },
          { cmd: "CLUSTER SLOTS", desc: "Slot ranges → owner. Drivers cache this for routing." },
          { cmd: "CLUSTER SHARDS", desc: "Per-shard view (master + replicas + slot ranges)." },
          { cmd: "CLUSTER KEYSLOT key", desc: "Compute the slot a key hashes into. Honours hashtags." },
          { cmd: "CLUSTER COUNTKEYSINSLOT slot", desc: "Number of local keys in the slot." },
          { cmd: "CLUSTER GETKEYSINSLOT slot count", desc: "Up to count local keys in the slot — used by MIGRATE batchers." },
          { cmd: "CLUSTER MEET host bus-port", desc: "Open a gossip connection to a new peer." },
          { cmd: "CLUSTER FORGET node-id", desc: "Drop a peer from the local view (not allowed for self)." },
          { cmd: "CLUSTER ADDSLOTS slot [slot ...]", desc: "Claim ownership of one or more slots." },
          { cmd: "CLUSTER ADDSLOTSRANGE start end [start end ...]", desc: "Claim contiguous slot ranges." },
          { cmd: "CLUSTER DELSLOTS slot [slot ...]", desc: "Release ownership; slot becomes unassigned." },
          { cmd: "CLUSTER SETSLOT slot MIGRATING|IMPORTING|STABLE|NODE [target]", desc: "Drive a slot migration or hand-off." },
          { cmd: "CLUSTER REPLICATE node-id", desc: "Make this node a replica of node-id." },
          { cmd: "CLUSTER FAILOVER", desc: "Promote this replica to master." },
          { cmd: "CLUSTER RESET [HARD|SOFT]", desc: "Wipe peers + slots; HARD also mints a fresh node ID." },
          { cmd: "CLUSTER BUMPEPOCH", desc: "Increment the current epoch (last-write-wins coordination)." },
          { cmd: "CLUSTER COUNT-FAILURE-REPORTS node-id", desc: "Always 0 in this build (no quorum-based failure detection yet)." },
          { cmd: "CLUSTER REPLICAS / CLUSTER SLAVES node-id", desc: "List replicas of the named master in CLUSTER NODES line format." },
          { cmd: "CLUSTER MYSHARDID", desc: "Shard ID — the master's own ID, or its master-id when called on a replica." },
          { cmd: "CLUSTER FLUSHSLOTS", desc: "Release every slot this node owns. Use before re-sharding." },
          { cmd: "CLUSTER SAVECONFIG", desc: "Bump epoch so gossip persists the latest cluster state on the next tick." },
          { cmd: "CLUSTER SLOT-STATS [SLOTSRANGE start end] [ORDERBY key-count [ASC|DESC] [LIMIT n]]", desc: "Per-slot key-count stats with optional range + ordering." },
          { cmd: "CLUSTER LINKS", desc: "Open gossip connections (peer ID, direction, age, ping/pong stats)." },
          { cmd: "ASKING", desc: "Single-shot — bypass an IMPORTING block on the very next command." },
          { cmd: "READONLY / READWRITE", desc: "Per-conn flag controlling reads on imported slots from a replica perspective." },
          { cmd: "MIGRATE host port key|\"\" db timeout-ms [COPY] [REPLACE] [AUTH pw] [AUTH2 user pw] [KEYS key ...]", desc: "Cross-node key transfer via DUMP+RESTORE; deletes the source unless COPY." },
        ]}
      />

      {/* ── modules ──────────────────────────────────────────────── */}
      <h2 id="modules">Modules</h2>
      <p>
        Modules are Go packages compile-time linked into the binary and
        activated by name via <code>MODULE LOAD</code>. They register
        commands and custom data types through a stable ABI that
        re-uses every engine path: ACL gating, cluster slot routing,
        AOF + replication propagation, slowlog and latency capture all
        apply automatically. Pre-load with{" "}
        <code>NEUROCACHE_MODULES_LOAD=json,probabilistic,timeseries,search</code>.
      </p>
      <CmdTable
        rows={[
          { cmd: "MODULE LOAD name [args ...]", desc: "Activate a compile-time-linked module by name. Init runs once; commands and types become live." },
          { cmd: "MODULE LOADEX name", desc: "Alias of LOAD. Reserved for future option parsing." },
          { cmd: "MODULE UNLOAD name", desc: "Stop a module and remove its commands + types from the dispatcher." },
          { cmd: "MODULE LIST", desc: "Loaded modules with version, description, commands and types they registered." },
        ]}
      />
      <p>
        <strong>Built-in modules shipped:</strong>{" "}
        <code>echo</code> (reference / smoke test),{" "}
        <code>json</code> (RedisJSON-compatible),{" "}
        <code>probabilistic</code> (BF / CF / CMS),{" "}
        <code>timeseries</code> (RedisTimeSeries-compatible), and{" "}
        <code>search</code> (RediSearch subset). Each is described in
        its own section below.
      </p>

      {/* ── JSON ─────────────────────────────────────────────────── */}
      <h2 id="json">JSON (module: <code>json</code>)</h2>
      <p>
        Document storage with a JSONPath subset matching Redis JSON v2.
        Supports <code>$</code>, <code>$.field</code>,{" "}
        <code>$["field"]</code>, <code>$.field.sub</code>,{" "}
        <code>$[0]</code> (negatives ok), <code>$[*]</code>,{" "}
        <code>$.*</code>, <code>$..field</code> (recursive descent).
        Filter expressions like <code>[?(@.qty &gt; 0)]</code> are
        supported — use them inside any path segment to narrow the
        result set server-side.
      </p>
      <CmdTable
        rows={[
          { cmd: "JSON.SET key path value [NX|XX]", desc: "Set the JSON value at path. NX inserts only if absent; XX only if present." },
          { cmd: "JSON.GET key [INDENT i] [NEWLINE n] [SPACE s] [path ...]", desc: "Fetch one or more paths. Multi-path returns an object keyed by path." },
          { cmd: "JSON.DEL key [path] / JSON.FORGET key [path]", desc: "Delete a path or the whole document." },
          { cmd: "JSON.TYPE key [path]", desc: "Return null|boolean|integer|number|string|object|array." },
          { cmd: "JSON.NUMINCRBY key path delta", desc: "Atomic numeric increment; preserves int/float shape." },
          { cmd: "JSON.NUMMULTBY key path delta", desc: "Atomic numeric multiplication." },
          { cmd: "JSON.STRAPPEND key [path] value", desc: "Append to a string value; returns the new length." },
          { cmd: "JSON.STRLEN key [path]", desc: "Byte length of the string at path." },
          { cmd: "JSON.ARRAPPEND key path value [value ...]", desc: "Push values onto the array at path." },
          { cmd: "JSON.ARRINSERT key path index value [value ...]", desc: "Insert at a 0-based index (negatives ok)." },
          { cmd: "JSON.ARRLEN key [path]", desc: "Array length per path." },
          { cmd: "JSON.ARRPOP key [path [index]]", desc: "Pop and return one element (default: tail)." },
          { cmd: "JSON.ARRTRIM key path start stop", desc: "Truncate the array to [start, stop]." },
          { cmd: "JSON.OBJKEYS key [path]", desc: "Member names at the matched object(s)." },
          { cmd: "JSON.OBJLEN key [path]", desc: "Member count per matched object." },
          { cmd: "JSON.TOGGLE key path", desc: "Flip a boolean at path." },
          { cmd: "JSON.CLEAR key [path]", desc: "Reset containers to empty / numerics to 0 / strings to \"\"." },
          { cmd: "JSON.RESP key [path]", desc: "Return the value as a flattened RESP-shaped array." },
          { cmd: "JSON.MGET key [key ...] path", desc: "Same path on multiple keys." },
          { cmd: "JSON.MSET key path value [key path value ...]", desc: "Atomic multi-document set." },
          { cmd: "JSON.MERGE key path value", desc: "RFC 7396 JSON Merge Patch — object members merge recursively, null deletes, scalars/arrays replace wholesale." },
          { cmd: "JSON.ARRINDEX key path value [start [stop]]", desc: "First-match index of value in the array(s) at path; deep equality on objects/arrays, numeric int/float matching." },
        ]}
      />

      {/* ── Probabilistic ───────────────────────────────────────── */}
      <h2 id="prob">Bloom / Cuckoo / CMS (module: <code>probabilistic</code>)</h2>
      <p>
        Three space-efficient probabilistic structures sharing FNV-1a
        hashing with double-hashing for k positions. All three persist
        through AOF replay and DUMP / RESTORE via version-tagged binary
        marshalers.
      </p>
      <h3>Bloom filter</h3>
      <CmdTable
        rows={[
          { cmd: "BF.RESERVE key error_rate capacity [EXPANSION exp] [NONSCALING]", desc: "Allocate a filter sized for capacity items at the given error rate." },
          { cmd: "BF.ADD key item", desc: "Insert one item. Returns 1 if probably new, 0 if probably already there." },
          { cmd: "BF.MADD key item [item ...]", desc: "Bulk insert — one boolean per item." },
          { cmd: "BF.EXISTS key item / BF.MEXISTS key item [item ...]", desc: "Membership test (1 = probably present, 0 = definitely absent)." },
          { cmd: "BF.INSERT key [CAPACITY cap] [ERROR err] [EXPANSION exp] [NOCREATE] [NONSCALING] ITEMS item [item ...]", desc: "All-in-one create + insert with full option surface." },
          { cmd: "BF.INFO key", desc: "Layer count, capacity, size, expansion rate, items inserted." },
          { cmd: "BF.CARD key", desc: "Total items inserted (exact counter, not estimator)." },
        ]}
      />
      <h3>Cuckoo filter</h3>
      <CmdTable
        rows={[
          { cmd: "CF.RESERVE key capacity [BUCKETSIZE n] [MAXITERATIONS n] [EXPANSION n]", desc: "Allocate a cuckoo filter sized for capacity items." },
          { cmd: "CF.ADD key item / CF.ADDNX key item", desc: "Insert; ADDNX rejects duplicates." },
          { cmd: "CF.INSERT key [CAPACITY cap] [NOCREATE] ITEMS item [item ...] / CF.INSERTNX ...", desc: "Bulk insert with auto-create." },
          { cmd: "CF.EXISTS key item / CF.MEXISTS key item [item ...]", desc: "Membership test." },
          { cmd: "CF.DEL key item", desc: "Remove one matching fingerprint (cuckoo can over-delete on collisions — same as Redis)." },
          { cmd: "CF.COUNT key item", desc: "Approximate occurrence count." },
          { cmd: "CF.INFO key", desc: "Buckets, bucket size, items, expansion, max iterations." },
        ]}
      />
      <h3>Count-Min Sketch</h3>
      <CmdTable
        rows={[
          { cmd: "CMS.INITBYDIM key width depth", desc: "Allocate a sketch with explicit dimensions." },
          { cmd: "CMS.INITBYPROB key error_rate probability", desc: "Allocate sized for the desired error guarantee." },
          { cmd: "CMS.INCRBY key item delta [item delta ...]", desc: "Add to one or more item counters; returns the post-increment minimum row estimate." },
          { cmd: "CMS.QUERY key item [item ...]", desc: "Estimated counts (over-counts; never under-counts)." },
          { cmd: "CMS.MERGE dest numkeys src [src ...] [WEIGHTS w [w ...]]", desc: "Fold sources into dest with optional weights." },
          { cmd: "CMS.INFO key", desc: "Width, depth, total events." },
        ]}
      />

      {/* ── TimeSeries ──────────────────────────────────────────── */}
      <h2 id="timeseries">TimeSeries (module: <code>timeseries</code>)</h2>
      <p>
        Sorted (timestamp, value) samples per key with retention,
        labels, six duplicate-handling policies, and downsampling rules
        that lazily flush at bucket close. Twelve aggregators including
        Welford-based variance / std deviation.
      </p>
      <CmdTable
        rows={[
          { cmd: "TS.CREATE key [RETENTION ms] [CHUNK_SIZE n] [DUPLICATE_POLICY p] [LABELS k v ...]", desc: "Allocate a series. Policies: BLOCK | FIRST | LAST | MIN | MAX | SUM." },
          { cmd: "TS.ALTER key [RETENTION ms] [CHUNK_SIZE n] [DUPLICATE_POLICY p] [LABELS k v ...]", desc: "Update mutable settings on an existing series." },
          { cmd: "TS.ADD key timestamp value [opts ...]", desc: "Append a sample. timestamp = '*' uses server clock. Auto-creates the series." },
          { cmd: "TS.MADD key ts value [key ts value ...]", desc: "Bulk insert across many series." },
          { cmd: "TS.INCRBY key delta [TIMESTAMP ts]", desc: "Add to the running value." },
          { cmd: "TS.DECRBY key delta [TIMESTAMP ts]", desc: "Subtract from the running value." },
          { cmd: "TS.GET key", desc: "Latest sample as [timestamp, value]." },
          { cmd: "TS.MGET FILTER label-filter [...]", desc: "Latest sample per series matching label filters." },
          { cmd: "TS.RANGE key from to [COUNT n] [AGGREGATION agg bucket-ms] [ALIGN ts]", desc: "Range scan; aggregators: AVG/SUM/MIN/MAX/RANGE/COUNT/FIRST/LAST/STD.P/STD.S/VAR.P/VAR.S." },
          { cmd: "TS.REVRANGE key from to [...]", desc: "Reverse-order range." },
          { cmd: "TS.MRANGE from to [LATEST] [COUNT n] [AGGREGATION agg bucket] [ALIGN ts] [WITHLABELS|SELECTED_LABELS l ...] FILTER label-filter [...]", desc: "Multi-series range with label filtering." },
          { cmd: "TS.MREVRANGE from to [...]", desc: "Reverse-order multi-series range." },
          { cmd: "TS.DEL key from to", desc: "Drop samples in [from, to]." },
          { cmd: "TS.QUERYINDEX FILTER label-filter [...]", desc: "Series keys matching the filter (label=val, label!=val, label=, label!=, label=(v1,v2,...))." },
          { cmd: "TS.INFO key", desc: "Sample count, memory, retention, labels, downsampling rules." },
          { cmd: "TS.CREATERULE source dest AGGREGATION agg bucket-ms [alignTimestamp]", desc: "Compaction rule: aggregate source into dest at bucket close." },
          { cmd: "TS.DELETERULE source dest", desc: "Drop a compaction rule." },
        ]}
      />

      {/* ── Search ──────────────────────────────────────────────── */}
      <h2 id="search">Search (module: <code>search</code>)</h2>
      <p>
        RediSearch-compatible subset: TEXT / NUMERIC / TAG / GEO /
        VECTOR (FLAT + HNSW) fields, recursive-descent query parser
        (boolean ops, field qualifiers, numeric ranges, tag sets,
        phrases with positional matching, prefix, fuzzy), full BM25
        scoring with per-field weights, and a streaming aggregation
        pipeline. Suggestions, synonyms, spellcheck, server-side
        cursors, and profile are all live.
      </p>
      <CmdTable
        rows={[
          { cmd: "FT.CREATE index [ON HASH] [PREFIX n p1 ...] SCHEMA name TYPE [WEIGHT n] [SORTABLE] [NOINDEX] [NOSTEM] [SEPARATOR sep] ...", desc: "Define an index. Field types: TEXT | NUMERIC | TAG | GEO | VECTOR." },
          { cmd: "FT.DROPINDEX index [DD]", desc: "Drop the index. Sweeps any aliases pointing at it." },
          { cmd: "FT.ALTER index SCHEMA ADD field type [flags ...]", desc: "Add fields to an existing index." },
          { cmd: "FT.ADD index docID score [REPLACE] FIELDS field value [...]", desc: "Index a document." },
          { cmd: "FT.DEL index docID", desc: "Remove a document from the index." },
          { cmd: "FT.GET index docID", desc: "Fetch a stored document." },
          { cmd: "FT.SEARCH index query [NOCONTENT] [WITHSCORES] [LIMIT off n] [SORTBY field [ASC|DESC]] [RETURN n field ...] [PARAMS n k v ...] [DIALECT n]", desc: "Run a query. Supports `term`, `term*`, `\"phrase\"`, `%term%` (fuzzy), `@field:term`, `@field:[lo hi]`, `@field:{tag1|tag2}`, `*=>[KNN k @field $vec]`, `A B` (AND), `A | B` (OR), `-A` (NOT), parentheses." },
          { cmd: "FT.AGGREGATE index query [LOAD ...] [pipeline-stages ...]", desc: "Stages: GROUPBY n key... REDUCE fn nargs args... [AS alias]; SORTBY n field [ASC|DESC] ...; LIMIT off n; FILTER expr; APPLY expr AS alias." },
          { cmd: "FT.EXPLAIN index query", desc: "Pretty-print the parsed query tree." },
          { cmd: "FT.INFO index", desc: "Schema, field flags, document count." },
          { cmd: "FT._LIST", desc: "Every defined index name." },
          { cmd: "FT.SUGADD / FT.SUGGET / FT.SUGDEL / FT.SUGLEN", desc: "Auto-complete suggestion dictionary." },
          { cmd: "FT.SYNUPDATE / FT.SYNDUMP", desc: "Synonym groups." },
          { cmd: "FT.SPELLCHECK index query [DISTANCE n] [TERMS INCLUDE|EXCLUDE dict ...]", desc: "Levenshtein-based spellcheck honouring custom dictionaries." },
          { cmd: "FT.CURSOR READ|DEL index cursor [COUNT n]", desc: "Resume a paginated FT.AGGREGATE WITHCURSOR session." },
          { cmd: "FT.PROFILE index SEARCH|AGGREGATE QUERY ...", desc: "Wrap a SEARCH/AGGREGATE invocation with timing data." },
          { cmd: "FT.ALIASADD / FT.ALIASUPDATE / FT.ALIASDEL alias index", desc: "Alternate names that resolve to a canonical index. Honoured by every FT.* read path; FT.DROPINDEX cleans up dangling aliases." },
          { cmd: "FT.DICTADD / FT.DICTDEL / FT.DICTDUMP dict term [...]", desc: "Custom term dictionaries used by FT.SPELLCHECK ... TERMS INCLUDE/EXCLUDE." },
          { cmd: "FT.TAGVALS index field", desc: "Distinct values present on a TAG field, sorted." },
          { cmd: "FT.CONFIG GET pattern | SET key value | RESETSTAT", desc: "Runtime tunables. Ships with MAXEXPANSIONS / MAXSEARCHRESULTS / DEFAULT_DIALECT / TIMEOUT defaults; unknown keys round-trip." },
        ]}
      />
      <p>
        <strong>Reducers supported by FT.AGGREGATE:</strong>{" "}
        <code>COUNT</code>, <code>SUM</code>, <code>MIN</code>,{" "}
        <code>MAX</code>, <code>AVG</code>, <code>COUNT_DISTINCT</code>,{" "}
        <code>FIRST_VALUE</code>, <code>TOLIST</code>.{" "}
        <strong>APPLY expressions:</strong> field references{" "}
        <code>@field</code>, numeric literals, <code>+ - * /</code>,
        parentheses.
      </p>

      <h2>Known gaps</h2>
      <p>
        The remaining cosmetic gaps versus stock Redis 8.6:
      </p>
      <ul>
        <li>
          <strong>OBJECT ENCODING precise variants</strong> — we report
          uniform encoding labels (<code>raw</code> /{" "}
          <code>linkedlist</code> / <code>hashtable</code> /{" "}
          <code>skiplist</code> / <code>stream</code>); Redis
          distinguishes ziplist vs listpack vs hashtable based on
          internal encoding heuristics.
        </li>
        <li>
          <strong>Vector set type (V*)</strong> — first-class vector
          set is in the next phase.
        </li>
      </ul>
    </>
  );
}
