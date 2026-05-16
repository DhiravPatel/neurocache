package resp

// writeCommands enumerates every command that mutates the keyspace.
// After a successful dispatch we hand these to the engine's AOF appender
// so durability tracks the live state. Pure reads are excluded.
var writeCommands = map[string]bool{
	// strings / keys
	"SET": true, "SETNX": true, "SETEX": true, "PSETEX": true,
	"GETSET": true, "APPEND": true, "SETRANGE": true,
	"MSET": true, "MSETNX": true,
	"INCR": true, "DECR": true, "INCRBY": true, "DECRBY": true, "INCRBYFLOAT": true,
	"DEL": true, "UNLINK": true,
	"EXPIRE": true, "PEXPIRE": true, "EXPIREAT": true, "PEXPIREAT": true, "PERSIST": true,
	"RENAME": true, "RENAMENX": true,
	"FLUSHDB": true, "FLUSHALL": true,

	// lists
	"LPUSH": true, "RPUSH": true, "LPUSHX": true, "RPUSHX": true,
	"LPOP": true, "RPOP": true, "LSET": true, "LREM": true, "LTRIM": true,
	"LINSERT": true, "RPOPLPUSH": true,

	// hashes
	"HSET": true, "HMSET": true, "HSETNX": true,
	"HDEL": true, "HINCRBY": true, "HINCRBYFLOAT": true,

	// sets
	"SADD": true, "SREM": true, "SPOP": true, "SMOVE": true,
	"SINTERSTORE": true, "SUNIONSTORE": true, "SDIFFSTORE": true,

	// sorted sets
	"ZADD": true, "ZREM": true, "ZINCRBY": true,
	"ZPOPMIN": true, "ZPOPMAX": true,

	// bitmaps
	"SETBIT": true, "BITOP": true,

	// HyperLogLog
	"PFADD": true, "PFMERGE": true,

	// streams
	"XADD": true, "XDEL": true, "XTRIM": true,
	"XGROUP": true, "XACK": true, "XCLAIM": true, "XAUTOCLAIM": true,
	"XREADGROUP": true,

	// COPY / RESTORE both produce keys
	"COPY": true, "RESTORE": true,

	// geo
	"GEOADD": true,

	// NeuroCache AI-native
	"SEMANTIC_SET": true, "CACHE_LLM": true, "MEMORY_ADD": true,

	// new in the gap-closing batch
	"GETDEL": true, "GETEX": true,
	"ZUNIONSTORE": true, "ZINTERSTORE": true, "ZDIFFSTORE": true,
	"ZRANGESTORE": true,
	"ZMPOP": true, "BZMPOP": true, "LMPOP": true, "BLMPOP": true,
	"HEXPIRE": true, "HPEXPIRE": true, "HEXPIREAT": true, "HPEXPIREAT": true,
	"HPERSIST": true,
	"BITFIELD": true,
	"XSETID": true,

	// phase 1: driver-critical fillers
	"LMOVE": true, "BLMOVE": true,
	"ZREMRANGEBYRANK": true, "ZREMRANGEBYSCORE": true, "ZREMRANGEBYLEX": true,
	"GEOSEARCHSTORE": true,
	// JSON.MERGE is module-write — module dispatch already records it
	// via the writer hook; we do not list it here.

	// phase 2: hash field TTL extras + deprecated geo family
	"HGETDEL": true, "HGETEX": true, "HSETEX": true,
	"GEORADIUS": true, "GEORADIUSBYMEMBER": true,
	// _RO variants are reads; only the writing forms appear here.

	// phase 4: niche 8.x-pattern additions. DIGEST is a pure read,
	// XCFGSET only mutates group config (recoverable from XINFO),
	// CLUSTER MIGRATION is admin observability — none of those land
	// in AOF. The mutators below do.
	"DELEX": true, "MSETEX": true,
	"XACKDEL": true, "XDELEX": true,

	// phase 5: vector set type — every state-changing V* command.
	"VADD": true, "VREM": true,
	"VSETATTR": true, "VDELATTR": true,

	// AI-stack: embedding cache, conversation management, prompt
	// templates. Each mutates in-memory state that needs to survive
	// restart, so they all flow through AOF + replication.
	"EMB.CACHE_SET": true, "EMB.CACHE_DEL": true, "EMB.PURGE": true, "EMB.COST": true,
	"CONV.APPEND": true, "CONV.SUMMARIZE": true, "CONV.RESET": true,
	"PROMPT.SET": true, "PROMPT.DELETE": true,

	// Tool memoization + cost guardrails — durable state, must
	// survive restart so the cap doesn't reset to "no limit" after
	// the engine recovers from a crash.
	"TOOL.SET": true, "TOOL.FORGET": true, "TOOL.PURGE": true,
	"GUARD.SETCAP": true, "GUARD.RECORD": true, "GUARD.CHECKRECORD": true, "GUARD.RESET": true,

	// Negative semantic cache + prompt analytics — durable counters,
	// must survive restart so dashboards keep the running totals
	// instead of resetting after a crash.
	"SEMNEG.MARK": true, "SEMNEG.FORGET": true, "SEMNEG.CLEAR": true,
	"PROMPT.RECORD": true, "PROMPT.RESET_ANALYTICS": true,

	// LLM provider failover ladder + injection scanner config —
	// route definitions and custom patterns must survive restart;
	// in-flight Mark Up/Down should NOT (let circuit breakers
	// re-probe upstreams on startup), so they're absent here.
	"LLM.ROUTE.SET": true, "LLM.ROUTE.FORGET": true,
	"INJECT.PATTERN.ADD": true, "INJECT.PATTERN.REMOVE": true, "INJECT.RESET": true,

	// Token budgets — config + counters survive restart so an
	// established daily/session budget isn't blown away on engine
	// recovery. TOKEN.COUNT / TOKEN.SPLIT / CHUNK.TEXT /
	// CONTEXT.ASSEMBLE are pure functions — never in the writeset.
	"TOKEN.BUDGET.SET": true, "TOKEN.BUDGET.FIT": true,
	"TOKEN.BUDGET.RESET": true, "TOKEN.BUDGET.DELETE": true,

	// Redaction patterns survive restart so operator-added regex
	// (custom employee-ID format, internal-host pattern) isn't lost
	// after a crash. SCRUB itself updates the per-pattern hit counter
	// and registers a restore-token, so it's a write too. RESTORE +
	// FORGET both mutate the restore-table.
	"REDACT.SCRUB": true, "REDACT.RESTORE": true, "REDACT.FORGET": true,
	"REDACT.PATTERN.ADD": true, "REDACT.PATTERN.REMOVE": true,

	// Grounding thresholds + counters are durable so dashboards
	// don't reset after a crash and operator-tuned gates persist.
	// GROUND.CHECK is a write because it updates the global accept/
	// gray/reject tallies. SET_THRESHOLDS is obviously a write.
	"GROUND.CHECK": true, "GROUND.SET_THRESHOLDS": true,

	// Canary deployments — every state-changing op. PICK is NOT a
	// write (it's a deterministic seed-based read). RECORD updates
	// per-arm tallies and may flip auto_rollback. PROMOTE swaps the
	// baseline and clears tallies. SET_TRAFFIC adjusts the live %.
	"CANARY.CREATE": true, "CANARY.RECORD": true,
	"CANARY.SET_TRAFFIC": true, "CANARY.PROMOTE": true,
	"CANARY.ROLLBACK": true, "CANARY.FORGET": true,

	// Rerank cache — every state-changing op. GET/SCORE are reads.
	// SETCAP/SETCOST are durable config so saved_usd numbers don't
	// reset to zero after restart.
	"RERANK.SET": true, "RERANK.FORGET": true, "RERANK.PURGE": true,
	"RERANK.SETCAP": true, "RERANK.SETCOST": true,

	// Judge suite — case definitions + run history must survive
	// restart so CI dashboards keep their pass-rate trends.
	// JUDGE.SCORE is a write (records the run). LIST/HISTORY/
	// PASSRATE/STATS/PROMPTS/CASE.LIST are pure reads.
	"JUDGE.CASE.ADD": true, "JUDGE.CASE.REMOVE": true,
	"JUDGE.SCORE": true, "JUDGE.FORGET": true,

	// Few-shot bank — examples are durable. QUERY/GET/LIST/BANKS
	// are reads.
	"FEWSHOT.ADD": true, "FEWSHOT.DEL": true, "FEWSHOT.FORGET": true,

	// Guardrail pipeline definitions are durable; RUN updates global
	// counters but doesn't change schema state — leave it out of the
	// writeset since recovering counters from runs would replay every
	// scan. (Operators who care about cumulative pass/fail use the
	// dashboard.)
	"GUARDRAIL.DEFINE": true, "GUARDRAIL.FORGET": true,

	// Struct schemas are durable. VALIDATE / REPAIR_PROMPT are reads
	// (they update counters but don't change schema state).
	"STRUCT.SCHEMA.SET": true, "STRUCT.FORGET": true,

	// Coalesce primitives are entirely in-flight runtime state. LOCK
	// / PUBLISH / WAIT only matter for currently-active herds; on
	// restart, every in-flight call gets re-issued by the app, which
	// is the correct semantic. So none of COALESCE.* is in the
	// writeset.

	// Hedge primitives are also in-flight runtime state — same logic
	// as COALESCE. Per-provider STATS counters do reset on restart;
	// dashboards expect that.

	// Verify samples are short-lived (one query worth) and not worth
	// AOF-replaying; they're rebuilt on every Self-Consistency loop.
	// FORGET / SAMPLE both omitted.

	// Rewrite cache is durable — operator-tuned rewrites should
	// survive restart so hit-rate doesn't crater after a deploy.
	// SET / SET_MULTI / FORGET / PURGE / SETCAP / SETCOST are writes.
	"REWRITE.SET": true, "REWRITE.SET_MULTI": true,
	"REWRITE.FORGET": true, "REWRITE.PURGE": true,
	"REWRITE.SETCAP": true, "REWRITE.SETCOST": true,

	// CITE / SHRINK are pure compute — no durable state. Counters
	// reset on restart, which dashboards expect.

	// AGENTLOOP state is purely in-flight (a single agent run). On
	// restart, agents either crashed (state is meaningless) or
	// continue from scratch. None of AGENTLOOP.* is in the writeset.

	// Semantic dedup bucket contents are durable — operators want
	// the same dedup decisions across restart (otherwise the same
	// paraphrase floods through immediately after recovery).
	// SEEN/ADD insert; FORGET drops a bucket. PEEK / RECENT /
	// BUCKETS / STATS are reads.
	"DEDUP.SEM.SEEN": true, "DEDUP.SEM.ADD": true, "DEDUP.SEM.FORGET": true,

	// Prefix router state is in-flight runtime data — KV-caches
	// don't survive worker restarts so reproducing the registration
	// state across a cache restart would be misleading. Workers
	// re-REGISTER on their next request anyway. None of PREFIX.*
	// is in the writeset.

	// Toolbox entries are durable. SEARCH / GET / LIST / STATS are
	// reads. REGISTER / FORGET are writes.
	"TOOLBOX.REGISTER": true, "TOOLBOX.FORGET": true,

	// Translation cache is durable — operator-paid translations
	// should survive restart so hit rate doesn't crater on deploy.
	"TRANSLATE.SET": true, "TRANSLATE.FORGET": true, "TRANSLATE.PURGE": true,
	"TRANSLATE.SETCAP": true, "TRANSLATE.SETCOST": true,

	// Embedding matrix is durable — apps store curated embeddings
	// that took real compute time to generate. SET/DEL/FORGET are
	// writes; TOPK/DOT/COSINE/LEN/LIST are reads.
	"EMBED.MAT.SET": true, "EMBED.MAT.DEL": true, "EMBED.MAT.FORGET": true,

	// OpCache is durable. Deterministic outputs are valuable to
	// preserve across restart.
	"OPCACHE.SET": true, "OPCACHE.FORGET": true, "OPCACHE.PURGE": true,
	"OPCACHE.SETCAP": true, "OPCACHE.SETCOST": true,

	// Autocomplete phrase lists are durable — operator-curated
	// dictionaries should survive restart.
	"AUTOCOMPLETE.ADD": true, "AUTOCOMPLETE.DEL": true, "AUTOCOMPLETE.FORGET": true,

	// CHAINSTATE definitions + runs are durable. The whole point is
	// crash-safe orchestration; runs that survive AOF replay can be
	// resumed from the exact step the original worker died on.
	"CHAINSTATE.DEFINE": true, "CHAINSTATE.START": true,
	"CHAINSTATE.DONE": true, "CHAINSTATE.FAIL": true,
	"CHAINSTATE.FORGET": true, "CHAINSTATE.FORGET_CHAIN": true,

	// MOE expert definitions are durable; RECORD updates atomic
	// counters that don't need replay (live health is naturally
	// rebuilt from new traffic post-restart). REGISTER + FORGET in
	// the writeset.
	"MOE.EXPERT.REGISTER": true, "MOE.FORGET": true,

	// CONFIDENCE samples are rolling — losing them on restart is
	// fine (calibration rebuilds from new traffic). None of
	// CONFIDENCE.* is in the writeset.

	// DRIFT baselines are durable (operator-curated samples);
	// observations are rolling-window state that rebuilds naturally.
	"DRIFT.BASELINE": true, "DRIFT.FORGET": true,

	// WATERMARK custom patterns are durable; SCORE is pure compute.
	"WATERMARK.PATTERN.ADD": true, "WATERMARK.PATTERN.REMOVE": true,

	// Matryoshka + quantized embedding matrices are durable —
	// operators stored curated vectors that took real compute to
	// generate. TOPK/LEN/COSINE are reads.
	"MATRYOSHKA.SET": true, "MATRYOSHKA.DEL": true, "MATRYOSHKA.FORGET": true,
	"VEC.QUANT.SET": true, "VEC.QUANT.DEL": true, "VEC.QUANT.FORGET": true,

	// EMBED.POOL.* is entirely pure compute — never in the writeset.

	// STREAM.PARSE state is per-active-stream, in-flight runtime
	// only — never in the writeset.

	// LIMITER.LLM config is durable (operator-set caps); RESERVE/
	// RECORD are sliding-window bucket updates that rebuild from
	// traffic post-restart, so don't replay them.
	"LIMITER.LLM.CONFIG": true, "LIMITER.LLM.RESET": true,

	// CACHE.LAYERS is durable — apps pay real LLM cost to populate
	// it, can't crater hit-rate on restart.
	"CACHE.LAYERS.SET": true, "CACHE.LAYERS.FORGET": true,
	"CACHE.LAYERS.PURGE": true, "CACHE.LAYERS.SET_THRESHOLD": true,

	// CONTRACT schemas are durable — operator-curated tool defs.
	"CONTRACT.REGISTER": true, "CONTRACT.UNREGISTER": true,

	// TIMELINE events are durable — context-injection scenarios
	// expect history to survive deploys/restarts.
	"TIMELINE.APPEND": true, "TIMELINE.FORGET": true,

	// HASH.LSH bucket definitions + rows are durable; SIGN/
	// NEIGHBORS/LEN/STATS are reads.
	"HASH.LSH.CREATE": true, "HASH.LSH.SET": true,
	"HASH.LSH.DEL": true, "HASH.LSH.FORGET": true,

	// NLI verdicts are durable (apps paid an LLM round-trip to
	// learn each one).
	"NLI.SET": true, "NLI.FORGET": true, "NLI.PURGE": true,

	// CASCADE config + learned routings are durable — losing them
	// would crater the cost-savings on restart.
	"CASCADE.CONFIG": true, "CASCADE.RECORD": true,
	"CASCADE.FORGET": true, "CASCADE.PURGE": true,

	// MASK templates are durable; BUILD is pure compute.
	"MASK.REGISTER": true, "MASK.UNREGISTER": true,

	// FACT registry + stamps are durable — the entire point is
	// surviving restart so cached entries can be invalidated when
	// the underlying fact changes.
	"FACT.SET": true, "FACT.BUMP": true, "FACT.STAMP": true,
	"FACT.UNSTAMP": true, "FACT.FORGET": true,

	// CACHE.INVALIDATE tracked entries are durable — apps stamp
	// real cache keys; losing the stamps would orphan the
	// invalidation story.
	"CACHE.INVALIDATE.TRACK": true, "CACHE.INVALIDATE.UNTRACK": true,
	"CACHE.INVALIDATE.PURGE": true,

	// BANDIT posteriors are durable — losing the learned alpha/beta
	// across restart would force the bandit back to uniform-prior
	// exploration, wasting the accumulated traffic data.
	"BANDIT.CREATE": true, "BANDIT.RECORD": true,
	"BANDIT.RESET": true, "BANDIT.FORGET": true,

	// POLICY.SEM seed banks are durable — operator-curated examples
	// are the whole maintenance burden.
	"POLICY.SEM.DEFINE": true, "POLICY.SEM.ADD": true,
	"POLICY.SEM.REMOVE": true, "POLICY.SEM.FORGET": true,

	// NOVELTY baselines are durable; SCORE is pure compute.
	"NOVELTY.BASELINE": true, "NOVELTY.ADD": true,
	"NOVELTY.SET_THRESHOLDS": true, "NOVELTY.FORGET": true,

	// LOCK.SEM state is entirely in-flight runtime — held locks
	// shouldn't survive restart (workers would all reawake from
	// crashed state holding stale locks). None in writeset.

	// Phase 11 — every command that mutates aiops manager state.
	// Reads (AGENT.CALL on a hit, COST.USAGE, SAFE.CHECK on a hit,
	// AB.ASSIGN, GRAPH.NEIGHBORS, EVENT.READ, etc.) are not in the
	// writeset because they don't change durable state. Inferred
	// follow-on writes (e.g. AGENT.CALL caching a fresh upstream
	// result) flow through their own AGENT.STORE invocation, which
	// is in the writeset.
	"AGENT.STORE": true, "AGENT.PROFILE": true, "AGENT.FORGET": true, "AGENT.PURGE": true,
	"STREAM.SET": true, "STREAM.FORGET": true, "STREAM.PURGE": true,
	"COST.BUDGET": true, "COST.CHARGE": true, "COST.RESET": true,
	"SHADOW.PUT": true, "SHADOW.FORGET": true,
	"PERSONA.SET": true, "PERSONA.FORGET": true,
	"SAFE.SET": true, "SAFE.FORGET": true, "SAFE.PURGE": true,
	"LINEAGE.RECORD": true, "LINEAGE.FORGET": true,
	"SLO.SET": true, "SLO.RESET": true,
	"AB.DEFINE": true, "AB.EXPOSE": true, "AB.RECORD": true, "AB.RESET": true, "AB.DELETE": true,
	"GRAPH.LINK": true, "GRAPH.UNLINK": true,
	"SCHEDULE.AT": true, "SCHEDULE.IN": true, "SCHEDULE.CANCEL": true,
	"EVENT.APPEND": true, "EVENT.PROJECT": true,
	"POLICY.SET": true, "POLICY.PURGE": true,
	"INFER.FORGET": true, "INFER.PURGE": true, "INFER.DEFAULT": true,

	// Hybrid retrieval (BM25 + vector + RRF). Index lifecycle and
	// document mutations need to survive restart; RETRIEVE.QUERY and
	// RAG.QUERY are pure reads. RETRIEVE.STATS is observability.
	"RETRIEVE.CREATE": true, "RETRIEVE.DROP": true,
	"RETRIEVE.ADD": true, "RETRIEVE.DEL": true,

	// Memory layer family — CONSOLIDATE writes new layer rows; DECAY
	// removes expired entries; ADD/DEL are obvious writes.
	"MEMORY.ADD": true, "MEMORY.DEL": true,
	"MEMORY.CONSOLIDATE": true, "MEMORY.DECAY": true,

	// Phase 13 — resilience & coordination primitives. CIRCUIT.CHECK
	// is a write because it can transition the breaker into HALFOPEN
	// and reserve a probe slot. SAGA.FAIL is a write because it
	// transitions the saga state machine even though it returns the
	// compensations — the caller still has to dispatch them. Pure
	// reads (CIRCUIT.STATE, SAGA.STATUS, CRDT.GVALUE/PNVALUE/etc.)
	// are not in the writeset.
	"CIRCUIT.CONFIG": true, "CIRCUIT.RECORD": true, "CIRCUIT.CHECK": true,
	"CIRCUIT.TRIP": true, "CIRCUIT.RESET": true, "CIRCUIT.FORGET": true,
	"SAGA.START": true, "SAGA.STEP": true, "SAGA.COMPLETE": true,
	"SAGA.FAIL": true, "SAGA.FORGET": true,
	"CRDT.GINCR": true, "CRDT.PNINCR": true,
	"CRDT.SADD": true, "CRDT.SREM": true,
	"CRDT.LWWSET": true, "CRDT.MERGE": true, "CRDT.FORGET": true,
	// Phase 12 — uniqueness primitives. Every command that mutates
	// in-memory state. WORKER.DEQUEUE is included because it moves a
	// job from the heap into the reserved set; replaying it on restart
	// reconstructs the in-flight pool. Pure reads (CHURN.KEYS,
	// FLAG.IS, AUDIT.QUERY, TRACE.GET, DOC.GET, OBSERVE.RENDER, etc.)
	// are not in the writeset.
	"CHURN.TAG": true, "CHURN.UNTAG": true, "CHURN.INVALIDATE": true,
	"WORKER.ENQUEUE": true, "WORKER.DEQUEUE": true,
	"WORKER.ACK": true, "WORKER.NACK": true,
	"WORKER.CONFIG": true, "WORKER.REQUEUE": true,
	"FLAG.SET": true, "FLAG.ALLOW": true, "FLAG.DENY": true, "FLAG.DELETE": true,
	"AUDIT.LOG": true, "AUDIT.RETENTION": true,
	"TRACE.START": true, "TRACE.END": true, "TRACE.ANNOTATE": true, "TRACE.FORGET": true,
	"DOC.INIT": true, "DOC.APPLY": true, "DOC.FORGET": true,
	"OBSERVE.REGISTER": true, "OBSERVE.INC": true, "OBSERVE.SET": true,
}

// isWriteCommand returns true if the command mutates the keyspace.
// Called from the dispatch path after a successful reply so that AOF
// captures writes without bogging down reads.
func isWriteCommand(cmd string) bool { return writeCommands[cmd] }
