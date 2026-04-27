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
| Filter expressions `[?(@.qty>0)]` | ✅ | `==`, `!=`, `<`, `<=`, `>`, `>=`, `=~`, `&&`, `||`, `!`, dotted field paths, JSON literals — `jsonmod/predicate.go` |
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
| TopK (`TOPK.*`) | ✅ | HeavyKeeper algorithm; `TOPK.RESERVE/ADD/INCRBY/QUERY/COUNT/LIST/INFO` — `probmod/topk.go` |

### M4-C — TimeSeries (`timeseries` module)

| Feature | Status | Where |
|---|---|---|
| Per-key sorted-sample series with retention | ✅ | `modules/builtin/tsmod/series.go` |
| Duplicate policies BLOCK/FIRST/LAST/MIN/MAX/SUM | ✅ | same |
| Aggregators AVG/SUM/MIN/MAX/RANGE/COUNT/FIRST/LAST/STD.P/STD.S/VAR.P/VAR.S (Welford) | ✅ | `modules/builtin/tsmod/agg.go` |
| Downsampling rules (lazy bucket-close propagation) | ✅ | `modules/builtin/tsmod/series.go`, `tsmod.go` |
| `TS.CREATE/ALTER/ADD/MADD/INCRBY/DECRBY/GET/MGET/RANGE/REVRANGE/MRANGE/MREVRANGE/DEL/QUERYINDEX/INFO/CREATERULE/DELETERULE` | ✅ | `modules/builtin/tsmod/tsmod.go` |
| Label filters (`k=v`, `k!=v`, `k=`, `k!=`, `k=(v1,v2)`) | ✅ | same |
| Compressed chunks (Gorilla / delta-of-delta) | ✅ | XOR float compression + variable-length DoD timestamps; opt-in `GorillaChunk` — `tsmod/gorilla.go` |

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
| GEO field | ✅ | Haversine radius search, auto-detected lat/lon ordering, units `m`/`km`/`mi`/`ft`; query syntax `@field:[lat lon r unit]` — `searchmod/geo.go` |
| VECTOR field | ✅ | FLAT (exact, brute force) + HNSW (ANN, layered graph), metrics `COSINE`/`L2`/`IP`, KNN syntax `*=>[KNN k @field $vec]` with `PARAMS` binding — `searchmod/vector.go` |
| Fuzzy queries `%term%` | ✅ | Cutoff-aware Levenshtein; `%`/`%%`/`%%%` for distance 1/2/3 — `searchmod/fuzzy.go` |
| `FT.SUGADD/SUGGET/SUGDEL/SUGLEN` | ✅ | Trie-backed autocomplete with score table, `INCR`/`PAYLOAD`/`FUZZY`/`MAX`/`WITHSCORES`/`WITHPAYLOADS` — `searchmod/suggestions.go` |
| `FT.SYNUPDATE/SYNDUMP` | ✅ | Per-index synonym groups with query-time term expansion — `searchmod/suggestions.go` |
| `FT.SPELLCHECK` | ✅ | Levenshtein over indexed terms, scored by inverse edit-distance × document frequency — `searchmod/extras.go` |
| `FT.CURSOR READ/DEL` | ✅ | Per-process cursor registry with TTL refresh — `searchmod/extras.go` |
| `FT.PROFILE` | ✅ | Reports parse-time / exec-time / docs-scanned / hits-returned for `SEARCH` and `AGGREGATE` — `searchmod/extras.go` |
| `FT.AGGREGATE FILTER` stage | ✅ | Reuses APPLY arithmetic + adds `==`/`!=`/`<`/`<=`/`>`/`>=`/`&&`/`||` — `searchmod/aggregate.go` |
| Strict positional phrase matching | ✅ | Posting list now stores per-doc positions; phrase eval requires every term at `pos+offset` — `searchmod/index.go` + `query.go` |

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

## Final-batch additions (Redis-parity closeout)

Every item below was on the "Known gaps" list before this batch — all now ✅.

| Feature | Status | Where |
|---|---|---|
| `SMISMEMBER` (multi-member SISMEMBER) | ✅ | `store/extras.go` |
| `SINTERCARD` (intersection cardinality with LIMIT) | ✅ | `store/extras.go` |
| `GETDEL` / `GETEX` (atomic read+delete / read+set-TTL) | ✅ | `store/extras.go` |
| `LPOS` (positional list search with RANK/COUNT/MAXLEN) | ✅ | `store/extras.go` |
| `ZUNIONSTORE` / `ZINTERSTORE` / `ZDIFFSTORE` + non-store `ZUNION`/`ZINTER`/`ZDIFF`/`ZINTERCARD` | ✅ | `store/zset_setops.go` — WEIGHTS, AGGREGATE SUM/MIN/MAX |
| `ZRANGEBYLEX` / `ZREVRANGEBYLEX` / `ZLEXCOUNT` / `ZRANGESTORE` (INDEX/BYSCORE/BYLEX) | ✅ | `store/zset_setops.go` |
| `ZMPOP` / `BZMPOP` (multi-key zset pop with COUNT) | ✅ | `store/zset_setops.go` + `resp/commands_extras.go` |
| `LMPOP` / `BLMPOP` (multi-key list pop with COUNT) | ✅ | same |
| Hash field TTLs: `HEXPIRE` / `HPEXPIRE` / `HEXPIREAT` / `HPEXPIREAT` / `HTTL` / `HPTTL` / `HPERSIST` (NX/XX/GT/LT conditions) | ✅ | `store/hash_ttl.go` — swept by ttlLoop |
| `HRANDFIELD` with COUNT + WITHVALUES | ✅ | `store/hash_ttl.go` |
| `LCS` (longest common subsequence — STRING / LEN / IDX modes, MINMATCHLEN, WITHMATCHLEN) | ✅ | `store/string_extras.go` |
| `BITFIELD` / `BITFIELD_RO` (GET/SET/INCRBY at any bit offset, signed/unsigned 1-64 bit fields, WRAP/SAT/FAIL overflow) | ✅ | `store/string_extras.go` |
| `SORT` / `SORT_RO` (BY pattern with `*`/`->field` indirection, LIMIT, GET, ASC/DESC, ALPHA, STORE) | ✅ | `store/string_extras.go` |
| `CLIENT TRACKING` / `TRACKINGINFO` / `NO-LOOP` (server-assisted client caching with default + BCAST modes, RESP3 push frames) | ✅ | `introspect/tracking.go` + `resp/commands_admin.go` |
| `WAITAOF` (durability barrier — wait for local AOF + N replica AOFs) | ✅ | `resp/commands_extras2.go` |
| `CLUSTER LINKS` (gossip link inspector) | ✅ | `resp/commands_extras2.go` + `resp/commands_cluster.go` |
| `XSETID` + `XADD NOMKSTREAM` + `XADD MINID` | ✅ | `store/stream.go` + `resp/commands.go` |
| Diskless replication | ✅ | already in-memory; `NEUROCACHE_REPL_DISKLESS` config flag for documentation |
| Replica-of-replica chains | ✅ | `NEUROCACHE_REPL_CHAINS=true` opts a replica into populating its backlog so downstream replicas can `PSYNC` |

## Plumbing closeout (Redis-shipped commands we previously stubbed)

| Feature | Status | Where |
|---|---|---|
| `COMMAND` / `COMMAND COUNT` / `COMMAND LIST` (with FILTERBY) / `COMMAND INFO` / `COMMAND DOCS` / `COMMAND GETKEYS` | ✅ | `resp/commands_command.go` |
| `SHUTDOWN [NOSAVE\|SAVE\|ABORT]` | ✅ | `resp/commands_plumbing.go` |
| `SCRIPT KILL` | ✅ | `resp/commands_plumbing.go` |
| `OBJECT HELP` | ✅ | `resp/commands_admin.go` |
| `ACL DRYRUN <user> <command> [args]` | ✅ | `resp/commands_plumbing.go` |
| `DEBUG SLEEP <seconds>` | ✅ | `resp/commands_plumbing.go` |
| `CLIENT KILL` with `ID/ADDR/LADDR/USER/TYPE/SKIPME` selectors | ✅ | `resp/commands_plumbing.go` |
| `CLIENT GETREDIR` | ✅ | `resp/commands_plumbing.go` |
| `XINFO STREAM key FULL` (per-group + per-consumer breakdown) | ✅ | `resp/commands_streams.go` |

## NeuroCache-only primitives (not in Redis)

These commands have no Redis equivalent. Each replaces a pattern most teams hand-roll in client code (or never get around to building correctly).

| Command | What it does | Why it's first-class |
|---|---|---|
| `IDEMPOTENT key ttl-ms <command> [args ...]` | Run `<command>` at most once per `(key, ttl)` window; subsequent calls return the cached result without re-executing | Replaces hand-rolled SETNX-then-execute patterns; safe under concurrent retries — coordinated leader/follower wait |
| `LOCK ACQUIRE/RELEASE/EXTEND/CHECK` | Distributed lock with **monotonic fencing tokens** | Every write returns a strictly-increasing token; downstream services can reject stale operations after a network partition (the bug Kleppmann's "How to do distributed locking" essay called out) |
| `RATELIMIT key window-ms max [COST n]` | GCRA token-bucket rate limit; returns `[allowed, remaining, retry-after-ms, reset-ms]` | Smooth bursts + exact recovery rate; constant memory per key. The rate-limiter every team eventually rebuilds in Lua |
| `DEDUP bucket id window-ms` | Returns 1 the first time `(bucket, id)` is seen within `window-ms`, 0 thereafter | Backed by a rotating two-bloom scheme — bounded memory even for unbounded id streams. The exactly-once-on-the-cheap primitive |
| `CACHE.WEIGH key cost` / `CACHE.UNWEIGH` / `CACHE.HIT` / `CACHE.STATS` / `CACHE.WEIGHTS` | Annotate cache entries with cost (USD, tokens, ms); the eviction scorer uses `cost × (1 + hits)` so high-value entries survive longer | Cost-aware eviction tuned for LLM/AI caches where one cache miss might cost $$ in re-computation |
| `KEY.TRACK key` / `KEY.UNTRACK` / `KEY.HISTORY key [count]` / `KEY.AT key unix-seconds` | Per-key version history with binary-search time-travel | Audit trails ("what was this user's tier when they hit our API?"), debugging ("show the value right before the incident"), and undo workflows |
| `AI.LIKE user item [weight]` / `AI.RECOMMEND user [k]` / `AI.SIMILAR user [k]` / `AI.STATS` / `AI.FORGET user` | Collaborative-filtering recommendations: cosine-similarity over user interaction profiles, top-K items unseen by the requester | The recommendation substrate every social/commerce app rebuilds. Pairs with the existing `SEMANTIC_*` and `MEMORY_*` family for hybrid (content + collaborative) recall |

## Phase 1 — Driver-critical fillers (Redis 8.6 closeout)

Commands every official driver calls by default. Each is a small, additive handler — no new types, no new subsystems.

| Feature | Status | Where |
|---|---|---|
| `ZMSCORE key member [member ...]` — parallel `ZSCORE` (Redis 6.2) | ✅ | `store/zset_extras.go`, `resp/commands_misc.go` |
| `ZRANDMEMBER key [count [WITHSCORES]]` — single / unique / with-replacement / scored | ✅ | `store/zset_extras.go`, `resp/commands_misc.go` |
| `ZREMRANGEBYRANK / ZREMRANGEBYSCORE / ZREMRANGEBYLEX` | ✅ | `store/zset_extras.go`, `resp/commands_misc.go` |
| `LMOVE source destination LEFT\|RIGHT LEFT\|RIGHT` — atomic across all 4 directions, supports `src == dst` rotation | ✅ | `store/list_extras.go`, `resp/commands_misc.go` |
| `TOUCH key [key ...]` — refresh `LastRead` without reading values (LFU/LRU helper) | ✅ | `store/list_extras.go`, `resp/commands_misc.go` |
| `EXPIRETIME / PEXPIRETIME` — absolute Unix expiry as seconds / ms | ✅ | `store/list_extras.go`, `resp/commands_misc.go` |
| `OBJECT FREQ` — already shipped; reads from per-entry hit counter | ✅ | `resp/commands_admin.go` |
| `EVAL_RO / EVALSHA_RO` — read-only EVAL; bridge rejects writes, kill flag now actually toggles | ✅ | `resp/commands_script.go` |
| `FUNCTION KILL` — wakes the script-kill flag the FCALL bridge polls between `redis.call` invocations | ✅ | `resp/commands_function.go` |
| `CLIENT UNBLOCK <id> [TIMEOUT\|ERROR]` — unblock blocked client; `ERROR` form emits canonical `-UNBLOCKED` reply | ✅ | `blocking/waiters.go` (per-client index, reason flag), `resp/commands_misc.go`, every blocking handler now uses `RegisterFor` |
| `GEOSEARCHSTORE dest src ...search-args [STOREDIST]` — write search results into a destination zset; default keeps geohash scores, `STOREDIST` writes haversine distances | ✅ | `store/geo.go`, `resp/commands_misc.go` |
| `JSON.MERGE key path value` — RFC 7396 JSON Merge Patch (object-recurse, scalar-replace, null-deletes) | ✅ | `modules/builtin/jsonmod/extras.go` |
| `JSON.ARRINDEX key path value [start [stop]]` — deep-equality search (works for nested objects/arrays + numeric int/float comparison) | ✅ | `modules/builtin/jsonmod/extras.go` |

## Phase 2 — Production-relevant supporting commands

Heavier than Phase 1, still no new types — mostly subcommands inside existing modules. These are the operational fillers that tooling, drivers, and operators reach for next.

| Feature | Status | Where |
|---|---|---|
| `HGETDEL key FIELDS n field [...]` — atomic read+delete on hash fields; key dies when last field goes (Redis 8.0) | ✅ | `store/hash_extras.go`, `resp/commands_phase2.go` |
| `HGETEX key [EX\|PX\|EXAT\|PXAT v\|PERSIST] FIELDS n field [...]` — atomic read + per-field TTL adjust | ✅ | `store/hash_extras.go`, `resp/commands_phase2.go` |
| `HSETEX key seconds [FNX\|FXX] FIELDS n field value [...]` — atomic set + per-field TTL with FNX/FXX conditional gate (whole call rejected if any field fails) | ✅ | `store/hash_extras.go`, `resp/commands_phase2.go` |
| `HEXPIRETIME / HPEXPIRETIME key FIELDS n field [...]` — absolute Unix expiry per field (s / ms) | ✅ | `store/hash_extras.go`, `resp/commands_phase2.go` |
| `FT.ALIASADD / FT.ALIASUPDATE / FT.ALIASDEL` — alternate names that resolve to a canonical index; honoured by every FT.* read path; FT.DROPINDEX sweeps dangling aliases | ✅ | `modules/builtin/searchmod/admin.go`, `admin_commands.go` |
| `FT.DICTADD / FT.DICTDEL / FT.DICTDUMP` — custom term dictionaries used by `FT.SPELLCHECK ... TERMS INCLUDE/EXCLUDE` | ✅ | `modules/builtin/searchmod/admin.go`, `admin_commands.go` |
| `FT.TAGVALS index field` — distinct values present on a TAG field, sorted | ✅ | `modules/builtin/searchmod/admin.go`, `admin_commands.go` |
| `FT.CONFIG GET\|SET\|RESETSTAT\|HELP` — runtime tunables; ships with `MAXEXPANSIONS / MAXSEARCHRESULTS / MAXAGGREGATERESULTS / DEFAULT_DIALECT / TIMEOUT / MIN_PHONETIC_TERM_LEN / FORK_GC_RUN_INTERVAL` defaults; unknown keys round-trip | ✅ | `modules/builtin/searchmod/admin.go`, `admin_commands.go` |
| `CLUSTER REPLICAS / CLUSTER SLAVES <node-id>` — every replica pointing at the named master, formatted as CLUSTER NODES rows | ✅ | `resp/commands_cluster_admin.go` |
| `CLUSTER MYSHARDID` — shard identifier (master's own ID, or master-id for a replica) | ✅ | `resp/commands_cluster_admin.go` |
| `CLUSTER FLUSHSLOTS` — release every slot this node owns (re-shard prep) | ✅ | `resp/commands_cluster_admin.go` |
| `CLUSTER SAVECONFIG` — bump epoch so the gossip subsystem snapshots on the next tick | ✅ | `resp/commands_cluster_admin.go` |
| `CLUSTER SLOT-STATS [SLOTSRANGE start end] [ORDERBY field [ASC\|DESC] [LIMIT n]]` — per-slot key-count stats with optional range + ordering | ✅ | `resp/commands_cluster_admin.go` |
| `GEORADIUS key lon lat r unit [WITHCOORD\|WITHDIST\|WITHHASH] [COUNT n [ANY]] [ASC\|DESC] [STORE\|STOREDIST dest]` — deprecated form retained for legacy drivers; STORE/STOREDIST routes through the same helper as `GEOSEARCHSTORE` | ✅ | `resp/commands_geo_legacy.go` |
| `GEORADIUSBYMEMBER` — same shape but the centre is a member's coordinates; auto-excludes the centre from results | ✅ | `resp/commands_geo_legacy.go` |
| `GEORADIUS_RO / GEORADIUSBYMEMBER_RO` — read-only variants; STORE/STOREDIST options return ERR | ✅ | `resp/commands_geo_legacy.go` |

## Phase 3 — HOTKEYS (runtime top-K key access tracker)

NeuroCache-native observability. Replaces the awkward `redis-cli --hotkeys` SCAN-and-OBJECT-FREQ dance with a real-time HeavyKeeper-backed tracker fed by the engine notifier.

| Feature | Status | Where |
|---|---|---|
| `HOTKEYS [count]` — top-K hot keys by estimated frequency, descending | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS RESET` — clear counters, preserve config | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS STATS` — config + observation counts (pre/post sampling) + memory cost | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS COUNT <key>` — estimated frequency for one key (0 if absent from heap) | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS THRESHOLD [min]` — read or set the minimum count to surface a key (0 = all) | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS RESIZE <k>` — rebuild HeavyKeeper with new K (resets) | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS SAMPLE [every]` — read or set 1-in-N sampling rate (1 = every event) | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS ENABLE \| DISABLE` — toggle the tracker without losing the snapshot | ✅ | `resp/commands_hotkeys.go` |
| `HOTKEYS HELP` | ✅ | `resp/commands_hotkeys.go` |

**Implementation notes**
- Shared `internal/probstruct/heavykeeper.go` owns the algorithm — both this tracker and the existing `TOPK.*` module use it.
- `internal/introspect/hotkeys.go` is the sampling wrapper: atomic counter + 1-in-N gate, threshold filter, K-resize, enable/disable. Concurrent-safe.
- Wired into `engine.New` via the existing keyspace notifier — the per-event branch is one atomic load + one atomic add when the sample roll loses, so it stays cheap on the hot path.
- Configurable via `NEUROCACHE_HOTKEYS_K` (default 128) and `NEUROCACHE_HOTKEYS_SAMPLE` (default 1 = sample everything).
- HTTP surface: `GET /api/hotkeys?k=N` returns `{keys: [{key, count}, ...], stats: {...}}`.
- Dashboard: new "Hot Keys (writes)" panel on the Analytics page sits alongside the existing GET-hits panel — they answer different questions (read popularity vs write churn).
- Cluster-exempt (no key argument); single-node by design — each node tracks its own slot subset.

## Phase 4 — Niche 8.x-pattern additions

Small, high-value commands that close common operational pain points. Each is a NeuroCache-flavored extension inspired by patterns Redis 8.x is moving toward — useful in their own right rather than literal Redis 8.6 commands.

| Feature | Status | Where |
|---|---|---|
| `DELEX key value` — compare-and-delete on a string key. Returns 1 (matched + deleted), 0 (mismatch / wrong type), -1 (missing). Makes safe "delete only if I still own this lease" patterns trivial without a Lua script | ✅ | `store/string_phase4.go`, `resp/commands_phase4.go` |
| `DIGEST key [key ...]` — 40-char hex SHA1 of each key's content; insertion-order independent for collections. Drop-in for ETags, replication consistency probes, "did this change?" cache validation | ✅ | `store/string_phase4.go`, `resp/commands_phase4.go` |
| `MSETEX seconds key value [key value ...]` — atomic multi-set with a shared TTL. Either every pair lands with the expiry or none do | ✅ | `store/string_phase4.go`, `resp/commands_phase4.go` |
| `XACKDEL key group id [id ...]` — atomic ACK + DEL. Prevents the race where a second consumer grabs the entry between a separate XACK and XDEL pair | ✅ | `store/stream_phase4.go`, `resp/commands_phase4.go` |
| `XDELEX key [REF\|KEEPREF\|ACKED] id [id ...]` — reference-aware XDEL. KEEPREF (default) is classic XDEL; REF refuses to delete entries still pending in any group; ACKED removes only entries no group still references | ✅ | `store/stream_phase4.go`, `resp/commands_phase4.go` |
| `XCFGSET key group [MAXDELIVERIES n] [MINIDLE ms]` — per-group runtime config (poison-message cap, XAUTOCLAIM idle floor). Returns the post-change values so callers can confirm the apply | ✅ | `store/stream_phase4.go`, `resp/commands_phase4.go` |
| `FT.HYBRID index "<text>" KNN k @field $vec [WEIGHTS sw dw] [NORMALIZE rrf\|minmax\|none] [LIMIT off n] [PARAMS n k v ...] [WITHSCORES] [RETURN ...]` — single-call hybrid retrieval. Runs the sparse (BM25) and dense (vector KNN) legs server-side and blends them with Reciprocal Rank Fusion (default), min-max normalization, or raw weighted sum | ✅ | `modules/builtin/searchmod/hybrid.go` |
| `CLUSTER MIGRATION` — list every slot currently in MIGRATING or IMPORTING state with the peer node ID + address. The operator's window into "what re-shard is running right now?" without parsing CLUSTER NODES suffixes | ✅ | `resp/commands_cluster_admin.go` |

**EVAL bridge**: `DELEX`, `DIGEST`, `MSETEX`, `XACKDEL`, `XDELEX` are all callable from Lua via `redis.call`.

## Phase 5 — Vector set type (V*) — first-class data type

The big one. New first-class data type backed by a shared `internal/vectorindex/` package (HNSW + FLAT with COSINE / L2 / IP metrics). Sits alongside string / list / hash / set / zset / stream as a peer in the keyspace, not a module type.

| Feature | Status | Where |
|---|---|---|
| `VADD key id vec [DIM n] [METRIC L2\|IP\|COSINE] [TYPE FLAT\|HNSW] [M m] [EFCONSTRUCTION n] [EFRUNTIME n] [SETATTR json]` — insert/replace; trailing options configure the new index, ignored on existing keys; vec accepts FP32 binary or comma-separated decimals | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VREM key id [id ...]` — remove members (PEL-equivalent: drops attributes too) | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VSIM key vec [COUNT n] [WITHSCORES] [WITHATTRS]` — KNN; smaller distance = more similar across all metrics | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VEMB key id` — fetch the stored vector as FP32 binary | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VSETATTR / VGETATTR / VDELATTR key id [json]` — opaque per-member JSON attribute storage | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VLINKS key id` — HNSW neighbour lists per layer (empty on FLAT or when id is missing) | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VINFO key` — algo / dim / metric / M / EFC / EFR / card / bytes-approx | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VCARD key` / `VDIM key` — member count / configured dimension | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VRANDMEMBER key [count]` — single / unique / with-replacement (matches SRANDMEMBER) | ✅ | `store/vector.go`, `resp/commands_vector.go` |
| `VSCAN key cursor [MATCH pat] [COUNT n]` — cursor iteration over member ids; sort-stabilised so see-every-key holds across calls | ✅ | `store/vector.go`, `resp/commands_vector.go` |

**Shared algorithm** [`internal/vectorindex/`](apps/api/internal/vectorindex/) — clean reusable package, deliberately distinct from the searchmod's tightly-coupled vector code so the two evolve independently.

**Engine integration**
- `TypeVector ValueType = 101` (out of the iota block, mirroring `TypeModule`); new `Entry.Vector *VectorSet` field
- Participates in TTL expiry, eviction byte accounting, keyspace notifications (`vadd` / `vrem` events fire), `DEL` / `EXISTS` / `TYPE`
- `removeIfEmpty` keeps vector sets alive at zero members — index config is precious; clients tear it down via `DEL`
- Cluster routing automatic (single-key commands)
- Replication propagation via the writeset (`VADD` / `VREM` / `VSETATTR` / `VDELATTR`)

**Persistence**
- `Export()` / `Restore()` round-trip the `ExportVectorOpts` (algo / dim / metric / M / EFC / EFR) plus every `(id, vec, attr)` triple
- `DUMP` / `RESTORE` (per-key blob) and `COPY` paths in `object.go` carry the same payload
- AOF replay: VADD / VREM / VSETATTR / VDELATTR are in the writeset, replayed on startup as ordinary commands — no new opcode needed

**HTTP + Dashboard**
- `GET /api/vector/sets` returns every vector-set key with its config + memory cost
- New "Vector Sets" page on the dashboard with a sortable inventory table and a built-in KNN probe panel (paste a CSV vector, run VSIM, see the top-K with distances)

**Coverage bump**: 11 → **12 data types**.

## Total command count

**~493 commands** across 12 data types + 5 modules + AI-native extensions + the NeuroCache-only primitives.

## Known gaps

Effectively everything Redis ships is now covered. Cosmetic gaps remain:

- `OBJECT ENCODING` precise variants (we report uniform "raw"/"linkedlist"/"hashtable"/"skiplist"/"stream" labels; Redis distinguishes ziplist vs listpack vs hashtable based on internal encoding heuristics)
- `LOLWUT` (joke command)
- Sharded pub/sub keyspace notifications (regular keyspace notifications work; the sharded-channel variant isn't auto-routed today)
- Some niche `DEBUG` subcommands (`DEBUG OBJECT`, `DEBUG SLEEP`, `DEBUG JMAP` — admin tools, not part of typical app usage)
