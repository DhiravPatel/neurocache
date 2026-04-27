# NeuroCache — Complete Feature Matrix

Single canonical reference for every feature shipped in NeuroCache, organised
by milestone. Each row lists the surface area, the commands or APIs it
exposes, and the file (or package) where the implementation lives.

Status legend: ✅ full · ⚠ pragmatic subset (documented) · ❌ deferred

---

## Day 0 — Engine foundations

| Feature | Status | Surface | Where |
|---|---|---|---|
| Multi-type keyspace | ✅ | string, list, hash, set, zset, stream | `apps/api/internal/store/` |
| Per-key TTL + lazy expirer | ✅ | `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT`, `PERSIST`, `TTL`, `PTTL` | `store/store.go` |
| Eviction policies | ✅ | `ai-smart`, `lru`, `lfu`, `noeviction` (config selectable) | `eviction/` |
| Vector index | ✅ | 384-dim feature-hashed embeddings, cosine similarity | `vector/`, `semcache/` |
| RESP server | ✅ | Listens on `:6379`, RESP2 protocol, inline-cmd compatible | `resp/` |
| HTTP API | ✅ | Every command also reachable over `/api/exec`, plus typed endpoints | `http/` |
| Embedded React dashboard | ✅ | KV browser, semantic tester, LLM cache UI, memory UI, playground, analytics | `apps/web/` |
| Metrics | ✅ | `/api/metrics/{summary,timeline,hot-keys,breakdown}` | `metrics/` |
| Pub/sub broker | ✅ | `SUBSCRIBE`/`UNSUBSCRIBE`/`PSUBSCRIBE`/`PUBLISH`/`PUBSUB` + keyspace notifications | `pubsub/` |
| Transactions | ✅ | `MULTI`/`EXEC`/`DISCARD`/`WATCH`/`UNWATCH` with optimistic per-key versioning | `transaction/` |

## Day 0 — AI-native (NeuroCache extensions)

| Feature | Status | Surface | Where |
|---|---|---|---|
| Semantic cache | ✅ | `SEMANTIC_SET`, `SEMANTIC_GET` (cosine threshold) | `semcache/` |
| LLM response cache | ✅ | `CACHE_LLM`, `CACHE_LLM_GET`, `CACHE_LLM_STATS` | `semcache/` |
| Per-user memory | ✅ | `MEMORY_ADD`, `MEMORY_QUERY`, `MEMORY_LIST` (top-k semantic recall + synthesis) | `memory/` |

---

## Part 1 — Single-node parity

| # | Feature | Status | Surface | Where |
|---|---|---|---|---|
| M1 | AOF persistence | ✅ | Append-only log + boot replay; `NEUROCACHE_AOF_FSYNC=always|everysec|no` | `persistence/aof.go` |
| M1 | RDB snapshots | ✅ | Periodic gzip+JSON dumps; on-demand `SAVE`/`BGSAVE` (async) | `persistence/rdb.go`, `engine.go` |
| M1 | LASTSAVE | ✅ | Real timestamp seeded from `dump.rdb` mtime at boot | `engine.go` |
| M1 | BGREWRITEAOF | ✅ | Async rewrite from live keyspace, atomic rename | `engine.go` |
| M2 | Auth + ACL | ✅ | `AUTH`, `ACL LIST/WHOAMI/USERS/GETUSER/SETUSER/DELUSER/CAT/LOG/GENPASS/SAVE` | `acl/` |
| M2 | ACL rule grammar | ✅ | `on/off`, `nopass`, `>pw`/`<pw`/`#hex`, `+CMD`/`-CMD`, `+@cat`/`-@cat`, `~pat`, `&pat`, `reset` | `acl/acl.go` |
| M2 | Categories | ✅ | 22 categories (read, write, fast, slow, blocking, dangerous, ai, …) | `acl/categories.go` |
| M2 | Audit log | ✅ | Auth-fail / command-denied / key-denied / channel-denied dedupe + retain | `acl/acl.go` |
| M3 | BLPOP / BRPOP / BLMOVE | ✅ | Real wait/notify (no polling); float-second timeout, 0 = forever | `blocking/`, `resp/commands_block.go` |
| M3 | BZPOPMIN / BZPOPMAX | ✅ | Same blocking primitive over sorted sets | `resp/commands_block.go` |
| M3 | XREAD BLOCK | ✅ | Upgraded from 25ms-poll loop to condvar wake-up | `resp/commands.go` |
| M4 | XGROUP CREATE/SETID/DESTROY/CREATECONSUMER/DELCONSUMER | ✅ | Full consumer-group lifecycle | `store/stream_groups.go` |
| M4 | XREADGROUP | ✅ | New-entry `>` and PEL-replay; NOACK supported | `store/stream_groups.go` |
| M4 | XACK / XPENDING / XCLAIM / XAUTOCLAIM | ✅ | Pending-entries list with consumer ownership + idle tracking | `store/stream_groups.go` |
| M4 | XINFO STREAM/GROUPS/CONSUMERS | ✅ | Metadata (length, last-id, group cursors, per-consumer pending + idle) | `store/stream_groups.go` |
| M5 | EVAL / EVALSHA / SCRIPT | ✅ (real Lua 5.1) | Backed by gopher-lua; `redis.call`, `redis.pcall`, `redis.error_reply`, `redis.status_reply`, `redis.sha1hex` | `scripting/` |
| M5 | Scripting sandbox | ✅ | `os`/`io`/`package`/`debug` unloaded; `require`/`dofile`/`load*` nilled out | `scripting/lua_real.go` |
| M5 | Script timeout | ✅ | `NEUROCACHE_SCRIPT_TIMEOUT_MS` enforced via `context.WithDeadline` | `scripting/lua_real.go` |
| M6 | OBJECT | ✅ | `ENCODING`, `IDLETIME`, `FREQ`, `REFCOUNT` | `store/object.go`, `resp/commands_admin.go` |
| M6 | MEMORY | ✅ | `USAGE`, `STATS`, `DOCTOR`, `PURGE` (triggers GC) | `resp/commands_admin.go` |
| M6 | SLOWLOG | ✅ | Ring buffer fed from the command hot path; `GET`, `LEN`, `RESET`, `HELP` | `introspect/slowlog.go` |
| M6 | LATENCY | ✅ | `HISTORY`, `LATEST`, `RESET`, `DOCTOR`, `GRAPH`, `HELP` | `introspect/latency.go` |
| M6 | CLIENT | ✅ | `ID`, `GETNAME`, `SETNAME`, `LIST`, `KILL`, `PAUSE`, `UNPAUSE`, `REPLY`, `NO-EVICT`, `INFO` | `introspect/clients.go` |
| M6 | RESET | ✅ | Clears MULTI/WATCH, drops subs, reverts to default user | `resp/commands_admin.go` |
| M6 | COPY / DUMP / RESTORE | ✅ | gob+gzip payload, `REPLACE` honoured | `store/object.go` |

---

## Part 2 M1 — Replication

| Feature | Status | Surface | Where |
|---|---|---|---|
| Replication state | ✅ | 40-hex replid, monotonic offset, role + link state | `replication/state.go` |
| Backlog ring | ✅ | Configurable byte ring for partial-resync (`NEUROCACHE_REPL_BACKLOG_SIZE`) | `replication/backlog.go` |
| Master-side fan-out | ✅ | Single goroutine pulls from a pending buffer + writes to every replica | `replication/master.go` |
| Replica-side dial loop | ✅ | Dial → handshake → consume RDB → stream apply, with backoff | `replication/replica.go` |
| Handshake | ✅ | `PING`, `REPLCONF listening-port`, `REPLCONF capa eof psync2`, `PSYNC` | `replication/replica.go` |
| Full + partial resync | ✅ | `+FULLRESYNC` ships RDB as bulk frame; `+CONTINUE` replays from offset | `resp/commands_repl.go` |
| Heartbeats | ✅ | Replica sends `REPLCONF ACK <offset>` once per second | `replication/replica.go` |
| `REPLICAOF` / `SLAVEOF` (incl. `NO ONE`) | ✅ | Promote/demote per-conn | `resp/commands_repl.go` |
| `WAIT numreplicas timeout-ms` | ✅ | Counts ACKed offsets ≥ current master offset | `resp/commands_repl.go` |
| `FAILOVER [TO host port] [TIMEOUT ms] [FORCE]` | ✅ | Single-node promote / instructed-target follow | `resp/commands_repl.go` |
| `ROLE` | ✅ | Standard master/replica reply with replicas + offsets | `resp/commands_repl.go` |

---

## Part 2 M2 — Cluster mode

| Feature | Status | Surface | Where |
|---|---|---|---|
| 16384-slot keyslot | ✅ | Bit-for-bit Redis CRC16-XMODEM + `{tag}` extraction | `cluster/crc16.go` |
| Node + State | ✅ | 40-hex IDs, slot bitmap with range collapsing, copy-on-write slot table (lock-free reads) | `cluster/node.go`, `cluster/state.go` |
| Gossip bus | ✅ | TCP listener on RESP+10000, JSON line-framed (PING/PONG/MEET/FAIL/UPDATE/PUBLISH), failure detector (PFAIL→FAIL) | `cluster/gossip.go` |
| Slot routing | ✅ | OK / MOVED / ASK / CROSSSLOT / TRYAGAIN / CLUSTERDOWN gate in `execute` | `cluster/redirect.go`, `resp/resp.go` |
| `CLUSTER INFO` | ✅ | enabled/state/slots/nodes/size/epoch | `resp/commands_cluster.go` |
| `CLUSTER MYID/NODES/SLOTS/SHARDS` | ✅ | Canonical Redis reply formats | `resp/commands_cluster.go` |
| `CLUSTER KEYSLOT/COUNTKEYSINSLOT/GETKEYSINSLOT` | ✅ | Slot lookups | `resp/commands_cluster.go` |
| `CLUSTER MEET / FORGET / REPLICATE / FAILOVER / RESET / BUMPEPOCH` | ✅ | Node lifecycle | `resp/commands_cluster.go` |
| `CLUSTER ADDSLOTS / ADDSLOTSRANGE / DELSLOTS / SETSLOT` | ✅ | Slot ownership transitions (incl. MIGRATING/IMPORTING/STABLE/NODE) | `resp/commands_cluster.go` |
| `ASKING` | ✅ | Single-shot bypass for IMPORTING block | `resp/commands_cluster.go` |
| `READONLY` / `READWRITE` | ✅ | Per-conn flag for replica reads | `resp/commands_cluster.go` |
| `MIGRATE` | ✅ | Cross-node DUMP+RESTORE, `COPY`/`REPLACE`/`AUTH`/`AUTH2`/`KEYS` | `resp/commands_cluster.go` |

---

## Part 2 M3 — Modules

| Feature | Status | Surface | Where |
|---|---|---|---|
| Module ABI | ✅ | `Module`, `Cmd`, `KeyPosition`, `CustomType`, `TypeID`, `EngineHandle`, `RegisterCtx`, `Ctx`, `Writer` | `modules/api.go`, `modules/ctx.go` |
| Registry | ✅ | Available pool (compile-time linked) + per-engine load/unload, atomic init/rollback | `modules/registry.go` |
| Custom-type entries | ✅ | Module-typed keys participate in TTL, eviction, byte accounting, notifications | `store/module_type.go` |
| `MODULE LOAD/UNLOAD/LIST/LOADEX` | ✅ | RESP + HTTP surfaces | `resp/commands_module.go`, `http/modules.go` |
| Module commands → engine | ✅ | Same path as built-ins (ACL, cluster routing, AOF, replication propagation, slowlog) | `resp/commands_module.go` |
| Demo module `echo` | ✅ | `MOD.PING/SET/GET/DEL/STATS` exercising every leg of the ABI | `modules/builtin/echo/` |

---

## Part 2 M4 — Stack types

### M4-A — RedisJSON (`json` module)

| Feature | Status | Where |
|---|---|---|
| JSONPath subset (`$`, `$.field`, `$["field"]`, `$[0]`, `$[*]`, `$.*`, `$..field`) | ✅ | `modules/builtin/jsonmod/path.go` |
| Filter expressions `[?(@.qty>0)]` | ❌ | deferred |
| `JSON.SET key path value [NX|XX]` | ✅ | `modules/builtin/jsonmod/commands.go` |
| `JSON.GET` (multi-path, INDENT/NEWLINE/SPACE) | ✅ | same |
| `JSON.DEL` / `JSON.FORGET` / `JSON.TYPE` | ✅ | same |
| `JSON.NUMINCRBY` / `JSON.NUMMULTBY` (preserves int/float shape) | ✅ | same |
| `JSON.STRAPPEND` / `JSON.STRLEN` | ✅ | same |
| `JSON.ARRAPPEND` / `ARRINSERT` / `ARRLEN` / `ARRPOP` / `ARRTRIM` | ✅ | same |
| `JSON.OBJKEYS` / `JSON.OBJLEN` / `JSON.TOGGLE` / `JSON.CLEAR` / `JSON.RESP` | ✅ | same |
| `JSON.MGET` / `JSON.MSET` | ✅ | same |

### M4-B — Probabilistic (`probabilistic` module)

| Feature | Status | Where |
|---|---|---|
| Bloom filter (scaling, version-tagged binary marshal) | ✅ | `modules/builtin/probmod/bloom.go` |
| `BF.RESERVE/ADD/MADD/EXISTS/MEXISTS/INSERT/INFO/CARD` | ✅ | `modules/builtin/probmod/probmod.go` |
| Cuckoo filter (random-eviction, fingerprint deletion) | ✅ | `modules/builtin/probmod/cuckoo.go` |
| `CF.RESERVE/ADD/ADDNX/INSERT/INSERTNX/EXISTS/MEXISTS/DEL/COUNT/INFO` | ✅ | `modules/builtin/probmod/probmod.go` |
| Count-Min Sketch (init by dim or prob, weighted merge) | ✅ | `modules/builtin/probmod/cms.go` |
| `CMS.INITBYDIM/INITBYPROB/INCRBY/QUERY/MERGE/INFO` | ✅ | `modules/builtin/probmod/probmod.go` |
| TopK (`TOPK.*`) | ❌ | deferred |

### M4-C — TimeSeries (`timeseries` module)

| Feature | Status | Where |
|---|---|---|
| Per-key sorted-sample series with retention | ✅ | `modules/builtin/tsmod/series.go` |
| Duplicate policies BLOCK/FIRST/LAST/MIN/MAX/SUM | ✅ | same |
| Aggregators AVG/SUM/MIN/MAX/RANGE/COUNT/FIRST/LAST/STD.P/STD.S/VAR.P/VAR.S (Welford) | ✅ | `modules/builtin/tsmod/agg.go` |
| Downsampling rules (lazy bucket-close propagation) | ✅ | `modules/builtin/tsmod/series.go`, `tsmod.go` |
| `TS.CREATE/ALTER/ADD/MADD/INCRBY/DECRBY/GET/MGET/RANGE/REVRANGE/MRANGE/MREVRANGE/DEL/QUERYINDEX/INFO/CREATERULE/DELETERULE` | ✅ | `modules/builtin/tsmod/tsmod.go` |
| Label filters (`k=v`, `k!=v`, `k=`, `k!=`, `k=(v1,v2)`) | ✅ | same |
| Compressed chunks (Gorilla / delta-of-delta) | ⚠ | uncompressed today |

### M4-D — RediSearch subset (`search` module)

| Feature | Status | Where |
|---|---|---|
| TEXT / NUMERIC / TAG fields with WEIGHT / SORTABLE / NOINDEX / NOSTEM / SEPARATOR | ✅ | `modules/builtin/searchmod/schema.go` |
| Inverted index (sorted postings, linear AND/OR merges) | ✅ | `modules/builtin/searchmod/index.go` |
| Tag set + sorted-array numeric range index | ✅ | same |
| BM25 scoring (per-field weights, IDF, length-norm) | ✅ | same |
| Tokenizer + stopwords + suffix stemmer | ✅ | `modules/builtin/searchmod/tokenize.go` |
| Query parser (boolean ops, field qualifiers, ranges, tag sets, phrases, prefix) | ✅ | `modules/builtin/searchmod/parser.go` |
| `FT.AGGREGATE` pipeline (GROUPBY, REDUCE×8, SORTBY, LIMIT, APPLY with embedded expr) | ✅ | `modules/builtin/searchmod/aggregate.go` |
| `FT.CREATE/DROPINDEX/ALTER/ADD/DEL/GET/SEARCH/AGGREGATE/EXPLAIN/INFO/_LIST` | ✅ | `modules/builtin/searchmod/searchmod.go` |
| GEO + VECTOR fields | ❌ | deferred |
| Fuzzy queries `%term%` | ❌ | deferred |
| `FT.SUGADD/SUGGET/SYNUPDATE/SYNDUMP/SPELLCHECK/CURSOR/PROFILE` | ❌ | deferred |
| `FT.AGGREGATE FILTER` stage | ❌ | deferred |
| Strict positional phrase matching | ❌ | conjunctive only |

---

## Part 2 final — Operational + protocol gaps

| Feature | Status | Surface / Notes | Where |
|---|---|---|---|
| TLS / mTLS | ✅ | `NEUROCACHE_TLS_CERT/KEY/CA/CLIENT_AUTH`; 4 client-auth modes (none/request/require/verify) | `resp/resp.go` |
| RESP3 protocol | ✅ | `HELLO 3` promotes per-conn; Map / Set / Bool / Double / BigNumber / Verbatim / Push / Null | `resp/resp3.go` |
| `CONFIG GET/SET/REWRITE/RESETSTAT` | ✅ | 14 runtime-mutable knobs with glob-matched GET, multi-pair SET, side-effect propagation | `config/runtime.go`, `resp/commands_config.go` |
| `MONITOR` | ✅ | Bounded-buffer broker fed from dispatch hot path; canonical Redis line format | `introspect/monitor.go`, `resp/commands_monitor.go` |
| Sharded pub/sub | ✅ | `SSUBSCRIBE`/`SUNSUBSCRIBE`/`SPUBLISH` with cluster slot routing + cross-node fan-out via cluster bus; `PUBSUB SHARDCHANNELS/SHARDNUMSUB` | `resp/commands_spub.go` |
| `FUNCTION LOAD/DELETE/LIST/STATS/FLUSH/DUMP/RESTORE` | ✅ | `#!lua name=…` + `redis.register_function('name', function(keys, args)…end)` | `scripting/functions.go`, `resp/commands_function.go` |
| `FCALL` / `FCALL_RO` | ✅ | Reuses gopher-lua runtime + ACL gate | `resp/commands_function.go` |
| Sentinel mode | ✅ surface, ⚠ pragmatic election | Every `SENTINEL` subcommand; SDOWN→ODOWN escalation via gossip-vote quorum; deterministic-lowest-ID leader (not full Raft terms) | `sentinel/sentinel.go`, `resp/commands_sentinel.go` |
| Auto-failover via cluster gossip | ✅ pragmatic | Opt-in (`NEUROCACHE_CLUSTER_AUTO_FAILOVER`); lowest-ID alive replica claims slots + bumps epoch on FAIL | `engine/engine.go` |
| Real Lua 5.1 | ✅ | Backed by [gopher-lua](https://github.com/yuin/gopher-lua) — full string/math/table libs, metatables, coroutines, closures | `scripting/lua_real.go` |

---

## Persistence & operations

| Feature | Status | Where |
|---|---|---|
| AOF append + replay + fsync policy | ✅ | `persistence/aof.go` |
| RDB gzipped JSON snapshot + load | ✅ | `persistence/rdb.go` |
| Async `BGSAVE` / `BGREWRITEAOF` with single-flight guard | ✅ | `engine/engine.go` |
| Real `LASTSAVE` (seeded from on-disk mtime) | ✅ | `engine/engine.go` |
| Cluster-wide PUBLISH fan-out via gossip bus | ✅ | `cluster/gossip.go` |
| Auto-load modules at boot | ✅ | `NEUROCACHE_MODULES_LOAD=json,probabilistic,timeseries,search` |

---

## Frontend — embedded dashboard

| Surface | Status | Where |
|---|---|---|
| Marketing landing | ✅ | `apps/web/src/pages/Landing.tsx` |
| Dashboard home (live engine stats) | ✅ | `pages/Dashboard.tsx` |
| Analytics (rolling chart, hit rate, p50/p95, cost savings) | ✅ | `pages/Analytics.tsx` |
| KV browser | ✅ | `pages/KV.tsx` |
| Semantic / LLM cache testers | ✅ | `pages/Semantic.tsx`, `pages/LLMCache.tsx` |
| Memory UI | ✅ | `pages/Memory.tsx` |
| Modules manager (Loaded + Available + Load/Unload) | ✅ | `pages/Modules.tsx` |
| Playground (RESP REPL via `/api/exec`) | ✅ | `pages/Playground.tsx` |
| Docs site — Installation, QuickStart, Commands (~290 entries), Architecture, SemanticCache, LLMCache, Memory, Configuration, SDKs, Deployment | ✅ | `pages/docs/` |

---

## Configuration knobs (env vars)

| Variable | Default | Purpose |
|---|---|---|
| `NEUROCACHE_HTTP_PORT` | `8080` | HTTP API + dashboard |
| `NEUROCACHE_RESP_PORT` | `6379` | RESP TCP |
| `NEUROCACHE_HOST` | `0.0.0.0` | Bind address |
| `NEUROCACHE_MAX_MEMORY` | `512mb` | Soft cap; eviction kicks in past this |
| `NEUROCACHE_EVICTION_POLICY` | `ai-smart` | `ai-smart` / `lru` / `lfu` / `noeviction` |
| `NEUROCACHE_EMBEDDING_DIM` | `384` | Embedding vector dimensions |
| `NEUROCACHE_SEMANTIC_THRESHOLD` | `0.75` | Cosine similarity threshold for `SEMANTIC_GET` |
| `NEUROCACHE_DATA_DIR` | `./data` | AOF/RDB/ACL files |
| `NEUROCACHE_AOF_ENABLED` | `false` | Append-only persistence |
| `NEUROCACHE_AOF_FSYNC` | `everysec` | `always` / `everysec` / `no` |
| `NEUROCACHE_RDB_ENABLED` | `false` | Periodic snapshots |
| `NEUROCACHE_RDB_INTERVAL_SEC` | `300` | Seconds between snapshots |
| `NEUROCACHE_REQUIREPASS` | _(unset)_ | Legacy password gate (sets default user pw) |
| `NEUROCACHE_ACL_FILE` | _(unset)_ | Path to `users.acl` (else `<DATA_DIR>/users.acl`) |
| `NEUROCACHE_PROTECTED_MODE` | `false` | Reject unauth'd commands |
| `NEUROCACHE_SLOWLOG_THRESHOLD_US` | `10000` | Slowlog inclusion threshold (μs) |
| `NEUROCACHE_SLOWLOG_MAX_LEN` | `128` | Slowlog ring capacity |
| `NEUROCACHE_LATENCY_MAX_LEN` | `160` | LATENCY HISTORY samples per event |
| `NEUROCACHE_SCRIPT_TIMEOUT_MS` | `5000` | Wall-clock ceiling for EVAL/FCALL |
| `NEUROCACHE_REPLICAOF` | _(unset)_ | `host:port` to follow at boot |
| `NEUROCACHE_REPL_BACKLOG_SIZE` | `1048576` | Bytes retained for partial-resync |
| `NEUROCACHE_REPL_TIMEOUT_SEC` | `60` | Replica → master link timeout |
| `NEUROCACHE_CLUSTER_ENABLED` | `false` | Slot/gossip stack |
| `NEUROCACHE_CLUSTER_BUS_PORT` | `RESP+10000` | Gossip listener |
| `NEUROCACHE_CLUSTER_ANNOUNCE_HOST` | _(uses HOST)_ | Host advertised in MOVED/ASK |
| `NEUROCACHE_CLUSTER_ANNOUNCE_PORT` | _(uses RESP)_ | Port advertised in MOVED/ASK |
| `NEUROCACHE_CLUSTER_NODE_ID` | _(generated)_ | Stable 40-hex ID |
| `NEUROCACHE_CLUSTER_REQUIRE_FULL_COVERAGE` | `true` | Refuse writes when not all 16384 slots are owned |
| `NEUROCACHE_CLUSTER_AUTO_FAILOVER` | `false` | Quorum-based replica self-promotion on master FAIL |
| `NEUROCACHE_MODULES_LOAD` | _(unset)_ | Comma-separated modules to activate at boot |
| `NEUROCACHE_TLS_CERT` | _(unset)_ | PEM server cert |
| `NEUROCACHE_TLS_KEY` | _(unset)_ | PEM private key |
| `NEUROCACHE_TLS_CA` | _(unset)_ | PEM CA bundle for client verification |
| `NEUROCACHE_TLS_CLIENT_AUTH` | `none` | `none` / `request` / `require` / `verify` |
| `NEUROCACHE_SENTINEL_ENABLED` | `false` | Run as a sentinel monitoring named masters |
| `NEUROCACHE_SENTINEL_MONITOR` | _(unset)_ | `name=host:port:quorum,name=host:port:quorum,...` |
| `NEUROCACHE_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `NEUROCACHE_LOG_FORMAT` | `text` | `text` / `json` |
| `NEUROCACHE_CORS_ORIGINS` | `*` | Comma-separated CORS allow-list |

---

## Total command count

**~310 commands** across 11 data types + 5 modules + AI-native extensions.

## Known gaps (each a bounded follow-up, not architectural)

- Advanced sorted-set ops: `ZUNIONSTORE`, `ZINTERSTORE`, `ZDIFFSTORE`, `ZRANGEBYLEX`, `ZRANGESTORE`, `ZMPOP`/`BZMPOP`
- Search: GEO + VECTOR fields, fuzzy queries, suggestions, synonyms, spellcheck, cursors, profile, FILTER aggregate stage, strict positional phrase matching
- Probabilistic: TopK (`TOPK.*`)
- TimeSeries: Gorilla-style compressed chunks
- Hash field-level TTLs (`HEXPIRE` / `HTTL`, Redis 7.4)
- `LMPOP`/`BLMPOP`, `LPOS`, `GETDEL`, `GETEX`, `LCS`, `BITFIELD`, `SORT`/`SORT_RO`
- Sharded pub/sub keyspace notifications
- Diskless replication wire optimisation, replica-of-replica chains
