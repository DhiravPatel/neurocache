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
