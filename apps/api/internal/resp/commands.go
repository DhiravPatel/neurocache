package resp

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// dispatch routes a single command to the right handler. Kept as one big
// switch for clarity — a map-of-funcs reads nicely but makes argument
// validation repetitive. Order follows the Redis command groups.
//
// Hot-path fast-path: a tiny switch up-front handles the top-5 commands
// (GET/SET/INCR/DEL/EXISTS) BEFORE falling into the ~545-case main
// switch. Go's compiler turns large string switches into a binary
// search; the small switch is a single comparison + jump table, ~5-10
// ns shaved per call for the commands that account for ~80% of
// production traffic. Identical semantics to the matching cases below
// — kept in sync by the test suite.
func (c *conn) dispatch(cmd string, args []string) {
	switch cmd {
	case "GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'get'")
			return
		}
		v, ok, err := c.eng.KV.GetTyped(args[0])
		c.eng.Metrics.RecordKVHit(args[0], ok)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
		return
	case "SET":
		c.setCmd(args)
		return
	case "INCR":
		c.incrBy(args, 1)
		return
	case "DEL":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'del'")
			return
		}
		writeInt(c.bw, int64(c.eng.KV.Del(args...)))
		return
	case "EXISTS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'exists'")
			return
		}
		writeInt(c.bw, int64(c.eng.KV.Exists(args...)))
		return
	}
	switch cmd {

	// ─── connection / server ────────────────────────────────────────
	case "PING":
		if len(args) == 0 {
			writeSimple(c.bw, "PONG")
		} else {
			writeBulk(c.bw, args[0])
		}
	case "ECHO":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		writeBulk(c.bw, args[0])
	case "SELECT":
		// Single database — accept 0, reject others.
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		if args[0] != "0" {
			writeError(c.bw, "NeuroCache supports a single logical database (db 0 only)")
			return
		}
		writeSimple(c.bw, "OK")
	case "DBSIZE":
		writeInt(c.bw, int64(c.eng.KV.Size()))
	case "HELLO":
		c.helloCmd(args)
	case "QUIT":
		writeSimple(c.bw, "OK")
	case "AUTH":
		c.authCmd(args)
	case "ACL":
		c.aclCmd(args)
	case "CLIENT":
		c.clientCmd(args)
	case "INFO":
		writeBulk(c.bw, c.infoString())
	case "DEBUG":
		c.debugCmd(args)
	case "RESET":
		c.resetCmd()
	case "OBJECT":
		c.objectCmd(args)
	case "MEMORY":
		c.memoryCmd(args)
	case "SLOWLOG":
		c.slowlogCmd(args)
	case "LATENCY":
		c.latencyCmd(args)
	case "COPY":
		c.copyCmd(args)
	case "DUMP":
		c.dumpCmd(args)
	case "RESTORE":
		c.restoreCmd(args)
	case "EVAL":
		c.evalCmd(args, false)
	case "EVAL_RO":
		c.evalCmd(args, true)
	case "EVALSHA":
		c.evalshaCmd(args, false)
	case "EVALSHA_RO":
		c.evalshaCmd(args, true)
	case "SCRIPT":
		c.scriptCmd(args)
	case "BLPOP":
		c.blpopCmd(args, false)
	case "BRPOP":
		c.blpopCmd(args, true)
	case "LMOVE":
		c.lmoveCmd(args)
	case "BLMOVE":
		c.blmoveCmd(args)
	case "BZPOPMIN":
		c.bzpopCmd(args, false)
	case "BZPOPMAX":
		c.bzpopCmd(args, true)
	case "XGROUP":
		c.xgroupCmd(args)
	case "XREADGROUP":
		c.xreadgroupCmd(args)
	case "XACK":
		c.xackCmd(args)
	case "XPENDING":
		c.xpendingCmd(args)
	case "XCLAIM":
		c.xclaimCmd(args)
	case "XAUTOCLAIM":
		c.xautoclaimCmd(args)
	case "XINFO":
		c.xinfoCmd(args)

	// ─── replication ───────────────────────────────────────────────
	case "REPLICAOF", "SLAVEOF":
		c.replicaofCmd(args)
	case "ROLE":
		c.roleCmd()
	case "WAIT":
		c.waitCmd(args)
	case "FAILOVER":
		c.failoverCmd(args)
	case "PSYNC", "SYNC":
		c.psyncCmd(args)
	case "REPLCONF":
		c.replconfCmd(args)

	// ─── cluster ───────────────────────────────────────────────────
	case "CLUSTER":
		c.clusterCmd(args)
	case "ASKING":
		c.askingCmd()
	case "READONLY":
		c.readonlyCmd()
	case "READWRITE":
		c.readwriteCmd()
	case "MIGRATE":
		c.migrateCmd(args)

	// ─── modules ───────────────────────────────────────────────────
	case "MODULE":
		c.moduleCmd(args)

	// ─── operational extras ────────────────────────────────────────
	case "CONFIG":
		c.configCmd(args)
	case "MONITOR":
		c.monitorCmd()
	case "SSUBSCRIBE":
		c.ssubscribeCmd(args)
	case "SUNSUBSCRIBE":
		c.sunsubscribeCmd(args)
	case "SPUBLISH":
		c.spublishCmd(args)
	case "FUNCTION":
		c.functionCmd(args)
	case "FCALL":
		c.fcallCmd(args, false)
	case "FCALL_RO":
		c.fcallCmd(args, true)
	case "SENTINEL":
		c.sentinelCmd(args)

	// ─── new in this batch ─────────────────────────────────────────
	case "SMISMEMBER":
		c.smismemberCmd(args)
	case "SINTERCARD":
		c.sintercardCmd(args)
	case "GETDEL":
		c.getdelCmd(args)
	case "GETEX":
		c.getexCmd(args)
	case "LPOS":
		c.lposCmd(args)
	case "ZUNIONSTORE":
		c.zunionstoreCmd(args)
	case "ZINTERSTORE":
		c.zinterstoreCmd(args)
	case "ZDIFFSTORE":
		c.zdiffstoreCmd(args)
	case "ZUNION":
		c.zunionCmd(args)
	case "ZINTER":
		c.zinterCmd(args)
	case "ZDIFF":
		c.zdiffCmd(args)
	case "ZINTERCARD":
		c.zintercardCmd(args)
	case "ZRANGEBYLEX":
		c.zrangeByLexCmd(args, false)
	case "ZREVRANGEBYLEX":
		c.zrangeByLexCmd(args, true)
	case "ZLEXCOUNT":
		c.zlexcountCmd(args)
	case "ZRANGESTORE":
		c.zrangestoreCmd(args)
	case "ZMPOP":
		c.zmpopCmd(args)
	case "BZMPOP":
		c.bzmpopCmd(args)
	case "LMPOP":
		c.lmpopCmd(args)
	case "BLMPOP":
		c.blmpopCmd(args)
	case "HEXPIRE":
		c.hexpireCmd(args, false, false)
	case "HPEXPIRE":
		c.hexpireCmd(args, true, false)
	case "HEXPIREAT":
		c.hexpireCmd(args, false, true)
	case "HPEXPIREAT":
		c.hexpireCmd(args, true, true)
	case "HTTL":
		c.httlCmd(args, false)
	case "HPTTL":
		c.httlCmd(args, true)
	case "HPERSIST":
		c.hpersistCmd(args)
	case "HRANDFIELD":
		c.hrandfieldCmd(args)
	case "LCS":
		c.lcsCmd(args)
	case "BITFIELD":
		c.bitfieldCmd(args, false)
	case "BITFIELD_RO":
		c.bitfieldCmd(args, true)
	case "SORT":
		c.sortCmd(args, false)
	case "SORT_RO":
		c.sortCmd(args, true)
	case "WAITAOF":
		c.waitaofCmd(args)
	case "XSETID":
		c.xsetidCmd(args)

	// ─── phase 1: driver-critical fillers ─────────────────────────
	case "TOUCH":
		c.touchCmd(args)
	case "EXPIRETIME":
		c.expireTimeCmd(args)
	case "PEXPIRETIME":
		c.pexpireTimeCmd(args)
	case "ZMSCORE":
		c.zmscoreCmd(args)
	case "ZRANDMEMBER":
		c.zrandmemberCmd(args)
	case "ZREMRANGEBYRANK":
		c.zremrangebyrankCmd(args)
	case "ZREMRANGEBYSCORE":
		c.zremrangebyscoreCmd(args)
	case "ZREMRANGEBYLEX":
		c.zremrangebylexCmd(args)
	case "GEOSEARCHSTORE":
		c.geosearchstoreCmd(args)

	// ─── phase 2: hash field TTL extras ───────────────────────────
	case "HGETDEL":
		c.hgetdelCmd(args)
	case "HGETEX":
		c.hgetexCmd(args)
	case "HSETEX":
		c.hsetexCmd(args)
	case "HEXPIRETIME":
		c.hexpireTimeCmd(args, false)
	case "HPEXPIRETIME":
		c.hexpireTimeCmd(args, true)

	// ─── phase 2: deprecated geo family ───────────────────────────
	case "GEORADIUS":
		c.georadiusCmd(args, false)
	case "GEORADIUS_RO":
		c.georadiusCmd(args, true)
	case "GEORADIUSBYMEMBER":
		c.georadiusByMemberCmd(args, false)
	case "GEORADIUSBYMEMBER_RO":
		c.georadiusByMemberCmd(args, true)

	// ─── phase 3: hot-key tracker ─────────────────────────────────
	case "HOTKEYS":
		c.hotkeysCmd(args)

	// ─── phase 6: completionist polish ────────────────────────────
	case "LOLWUT":
		c.lolwutCmd(args)

	// ─── phase 5: vector set type ────────────────────────────────
	case "VADD":
		c.vaddCmd(args)
	case "VREM":
		c.vremCmd(args)
	case "VSIM":
		c.vsimCmd(args)
	case "VEMB":
		c.vembCmd(args)
	case "VSETATTR":
		c.vsetattrCmd(args)
	case "VGETATTR":
		c.vgetattrCmd(args)
	case "VDELATTR":
		c.vdelattrCmd(args)
	case "VLINKS":
		c.vlinksCmd(args)
	case "VINFO":
		c.vinfoCmd(args)
	case "VCARD":
		c.vcardCmd(args)
	case "VDIM":
		c.vdimCmd(args)
	case "VRANDMEMBER":
		c.vrandmemberCmd(args)
	case "VSCAN":
		c.vscanCmd(args)

	// ─── phase 4: niche 8.x-pattern additions ─────────────────────
	case "DELEX":
		c.delexCmd(args)
	case "DIGEST":
		c.digestCmd(args)
	case "MSETEX":
		c.msetexCmd(args)
	case "XACKDEL":
		c.xackdelCmd(args)
	case "XDELEX":
		c.xdelexCmd(args)
	case "XCFGSET":
		c.xcfgsetCmd(args)

	// ─── plumbing additions ────────────────────────────────────────
	case "COMMAND":
		c.commandCmd(args)
	case "SHUTDOWN":
		c.shutdownCmd(args)

	// ─── novel NeuroCache primitives ───────────────────────────────
	case "IDEMPOTENT":
		c.idempotentCmd(args)
	case "LOCK":
		c.lockCmd(args)
	case "RATELIMIT":
		c.ratelimitCmd(args)
	case "DEDUP":
		c.dedupCmd(args)
	case "CACHE.WEIGH", "CACHE.UNWEIGH", "CACHE.STATS", "CACHE.WEIGHTS", "CACHE.HIT":
		c.cacheCmd(splitDottedSubcommand(cmd, args))
	case "KEY.TRACK", "KEY.UNTRACK", "KEY.HISTORY", "KEY.AT":
		c.keyHistoryCmd(splitDottedSubcommand(cmd, args))
	case "AI.LIKE", "AI.RECOMMEND", "AI.SIMILAR", "AI.STATS", "AI.FORGET":
		c.aiCmd(splitDottedSubcommand(cmd, args))
	case "EMB.CACHE_SET", "EMB.CACHE_GET", "EMB.CACHE_DEL", "EMB.STATS", "EMB.PURGE", "EMB.COST":
		c.embCmd(strings.TrimPrefix(cmd, "EMB."), args)
	case "CONV.APPEND", "CONV.WINDOW", "CONV.SUMMARIZE", "CONV.RESET", "CONV.LEN", "CONV.LIST":
		c.convCmd(strings.TrimPrefix(cmd, "CONV."), args)
	case "PROMPT.SET", "PROMPT.GET", "PROMPT.RENDER", "PROMPT.LIST", "PROMPT.DELETE", "PROMPT.VERSIONS":
		c.promptCmd(strings.TrimPrefix(cmd, "PROMPT."), args)
	case "TOOL.SET", "TOOL.GET", "TOOL.FORGET", "TOOL.PURGE", "TOOL.STATS", "TOOL.LIST":
		c.toolCmd(strings.TrimPrefix(cmd, "TOOL."), args)
	case "GUARD.SETCAP", "GUARD.CHECK", "GUARD.RECORD", "GUARD.CHECKRECORD",
		"GUARD.SPENT", "GUARD.LIMIT", "GUARD.RESET", "GUARD.LIST", "GUARD.STATS":
		c.guardCmd(strings.TrimPrefix(cmd, "GUARD."), args)
	case "SEMNEG.MARK", "SEMNEG.CHECK", "SEMNEG.FORGET", "SEMNEG.CLEAR",
		"SEMNEG.STATS", "SEMNEG.LIST":
		c.semnegCmd(strings.TrimPrefix(cmd, "SEMNEG."), args)
	case "PROMPT.FINGERPRINT", "PROMPT.RECORD", "PROMPT.GROUPS",
		"PROMPT.SAMPLE", "PROMPT.STATS", "PROMPT.RESET_ANALYTICS":
		c.promptAnalyticsCmd(strings.TrimPrefix(cmd, "PROMPT."), args)
	case "LLM.ROUTE.SET", "LLM.ROUTE.NEXT", "LLM.ROUTE.MARKDOWN",
		"LLM.ROUTE.MARKUP", "LLM.ROUTE.HEALTHY", "LLM.ROUTE.LIST",
		"LLM.ROUTE.STATS", "LLM.ROUTE.FORGET":
		c.llmRouteCmd(strings.TrimPrefix(cmd, "LLM.ROUTE."), args)
	case "INJECT.SCAN", "INJECT.SCANALL", "INJECT.PATTERN.ADD",
		"INJECT.PATTERN.REMOVE", "INJECT.PATTERN.LIST",
		"INJECT.STATS", "INJECT.RESET":
		c.injectCmd(strings.TrimPrefix(cmd, "INJECT."), args)
	case "TOKEN.COUNT", "TOKEN.SPLIT",
		"TOKEN.BUDGET.SET", "TOKEN.BUDGET.FIT",
		"TOKEN.BUDGET.GET", "TOKEN.BUDGET.RESET",
		"TOKEN.BUDGET.DELETE", "TOKEN.BUDGET.LIST",
		"TOKEN.STATS":
		c.tokenCmd(strings.TrimPrefix(cmd, "TOKEN."), args)
	case "CHUNK.TEXT", "CHUNK.STATS":
		c.chunkCmd(strings.TrimPrefix(cmd, "CHUNK."), args)
	case "CONTEXT.ASSEMBLE":
		c.contextAssembleCmd(args)
	case "REDACT.SCRUB", "REDACT.RESTORE", "REDACT.FORGET",
		"REDACT.PATTERN.ADD", "REDACT.PATTERN.REMOVE",
		"REDACT.PATTERN.LIST", "REDACT.STATS":
		c.redactCmd(strings.TrimPrefix(cmd, "REDACT."), args)
	case "GROUND.CHECK", "GROUND.THRESHOLDS",
		"GROUND.SET_THRESHOLDS", "GROUND.STATS":
		c.groundCmd(strings.TrimPrefix(cmd, "GROUND."), args)
	case "CANARY.CREATE", "CANARY.PICK", "CANARY.RECORD",
		"CANARY.STATUS", "CANARY.SET_TRAFFIC",
		"CANARY.PROMOTE", "CANARY.ROLLBACK",
		"CANARY.LIST", "CANARY.FORGET", "CANARY.STATS":
		c.canaryCmd(strings.TrimPrefix(cmd, "CANARY."), args)
	case "RERANK.GET", "RERANK.SET", "RERANK.SCORE",
		"RERANK.FORGET", "RERANK.PURGE",
		"RERANK.SETCAP", "RERANK.SETCOST", "RERANK.STATS":
		c.rerankCmd(strings.TrimPrefix(cmd, "RERANK."), args)
	case "JUDGE.CASE.ADD", "JUDGE.CASE.REMOVE", "JUDGE.CASE.LIST",
		"JUDGE.SCORE", "JUDGE.HISTORY", "JUDGE.PASSRATE",
		"JUDGE.PROMPTS", "JUDGE.FORGET", "JUDGE.STATS":
		c.judgeCmd(strings.TrimPrefix(cmd, "JUDGE."), args)
	case "FEWSHOT.ADD", "FEWSHOT.QUERY", "FEWSHOT.GET", "FEWSHOT.DEL",
		"FEWSHOT.LIST", "FEWSHOT.BANKS", "FEWSHOT.FORGET", "FEWSHOT.STATS":
		c.fewshotCmd(strings.TrimPrefix(cmd, "FEWSHOT."), args)
	case "GUARDRAIL.DEFINE", "GUARDRAIL.RUN", "GUARDRAIL.LIST",
		"GUARDRAIL.FORGET", "GUARDRAIL.STATS":
		c.guardrailCmd(strings.TrimPrefix(cmd, "GUARDRAIL."), args)
	case "STRUCT.SCHEMA.SET", "STRUCT.SCHEMA.GET", "STRUCT.SCHEMA.LIST",
		"STRUCT.VALIDATE", "STRUCT.REPAIR_PROMPT", "STRUCT.FORGET", "STRUCT.STATS":
		c.structCmd(strings.TrimPrefix(cmd, "STRUCT."), args)
	case "COALESCE.LOCK", "COALESCE.PUBLISH", "COALESCE.WAIT",
		"COALESCE.STATUS", "COALESCE.FORGET", "COALESCE.KEYS", "COALESCE.STATS":
		c.coalesceCmd(strings.TrimPrefix(cmd, "COALESCE."), args)
	case "HEDGE.START", "HEDGE.PUBLISH", "HEDGE.WAIT", "HEDGE.STATUS",
		"HEDGE.FORGET", "HEDGE.STATS":
		c.hedgeCmd(strings.TrimPrefix(cmd, "HEDGE."), args)
	case "VERIFY.SAMPLE", "VERIFY.CONSENSUS", "VERIFY.SAMPLES",
		"VERIFY.FORGET", "VERIFY.STATS":
		c.verifyCmd(strings.TrimPrefix(cmd, "VERIFY."), args)
	case "REWRITE.SET", "REWRITE.GET", "REWRITE.SET_MULTI", "REWRITE.LIST",
		"REWRITE.FORGET", "REWRITE.PURGE", "REWRITE.SETCAP", "REWRITE.SETCOST", "REWRITE.STATS":
		c.rewriteCmd(strings.TrimPrefix(cmd, "REWRITE."), args)

	// ─── aiops families (AGENT/STREAM/COST/SHADOW/PERSONA/SAFE/
	// LINEAGE/SLO/AB/GRAPH/SCHEDULE/EVENT/POLICY/INFER/MCP) ────────
	case "AGENT.CALL", "AGENT.STORE", "AGENT.PROFILE", "AGENT.FORGET", "AGENT.STATS", "AGENT.PURGE":
		c.agentCmd(strings.TrimPrefix(cmd, "AGENT."), args)
	case "STREAM.SET", "STREAM.GET", "STREAM.REPLAY", "STREAM.FORGET", "STREAM.PURGE", "STREAM.STATS":
		c.streamCmd(strings.TrimPrefix(cmd, "STREAM."), args)
	case "COST.BUDGET", "COST.CHARGE", "COST.USAGE", "COST.RESET", "COST.LIST":
		c.costCmd(strings.TrimPrefix(cmd, "COST."), args)
	case "SHADOW.PUT", "SHADOW.GET", "SHADOW.FORGET", "SHADOW.STATS":
		c.shadowCmd(strings.TrimPrefix(cmd, "SHADOW."), args)
	case "PERSONA.SET", "PERSONA.GET", "PERSONA.LIST", "PERSONA.FORGET":
		c.personaCmd(strings.TrimPrefix(cmd, "PERSONA."), args)
	case "SAFE.SET", "SAFE.CHECK", "SAFE.INJECT", "SAFE.FORGET", "SAFE.PURGE", "SAFE.STATS":
		c.safeCmd(strings.TrimPrefix(cmd, "SAFE."), args)
	case "LINEAGE.RECORD", "LINEAGE.LIST", "LINEAGE.SOURCES", "LINEAGE.CONSUMERS", "LINEAGE.FORGET", "LINEAGE.STATS":
		c.lineageCmd(strings.TrimPrefix(cmd, "LINEAGE."), args)
	case "SLO.SET", "SLO.SNAPSHOT", "SLO.RESET":
		c.sloCmd(strings.TrimPrefix(cmd, "SLO."), args)
	case "AB.DEFINE", "AB.ASSIGN", "AB.EXPOSE", "AB.RECORD", "AB.STATS", "AB.LIST", "AB.RESET", "AB.DELETE":
		c.abCmd(strings.TrimPrefix(cmd, "AB."), args)
	case "GRAPH.LINK", "GRAPH.UNLINK", "GRAPH.NEIGHBORS", "GRAPH.IN", "GRAPH.PATH", "GRAPH.SUBJECTS", "GRAPH.STATS":
		c.graphCmd(strings.TrimPrefix(cmd, "GRAPH."), args)
	case "SCHEDULE.AT", "SCHEDULE.IN", "SCHEDULE.CANCEL", "SCHEDULE.LIST", "SCHEDULE.STATS":
		c.scheduleCmd(strings.TrimPrefix(cmd, "SCHEDULE."), args)
	case "EVENT.APPEND", "EVENT.PROJECT", "EVENT.READ", "EVENT.RANGE", "EVENT.LEN":
		c.eventCmd(strings.TrimPrefix(cmd, "EVENT."), args)
	case "POLICY.ALLOW", "POLICY.SET", "POLICY.PURGE", "POLICY.STATS":
		c.policyCmd(strings.TrimPrefix(cmd, "POLICY."), args)
	case "INFER.GENERATE", "INFER.FORGET", "INFER.PURGE", "INFER.STATS", "INFER.DEFAULT":
		c.inferCmd(strings.TrimPrefix(cmd, "INFER."), args)
	case "MCP.TOOLS", "MCP.RESOURCES", "MCP.CALL", "MCP.READ", "MCP.RPC":
		c.mcpCmd(strings.TrimPrefix(cmd, "MCP."), args)

	// ─── hybrid retrieval (BM25 + vector + RRF) and GraphRAG ───────
	case "RETRIEVE.CREATE", "RETRIEVE.DROP", "RETRIEVE.LIST", "RETRIEVE.STATS",
		"RETRIEVE.ADD", "RETRIEVE.DEL", "RETRIEVE.GET", "RETRIEVE.QUERY":
		c.retrieveCmd(strings.TrimPrefix(cmd, "RETRIEVE."), args)
	case "RAG.QUERY":
		c.ragQueryCmd(args)
	case "MEMORY.ADD", "MEMORY.QUERY", "MEMORY.LIST", "MEMORY.DEL",
		"MEMORY.CONSOLIDATE", "MEMORY.DECAY", "MEMORY.STATS":
		c.memoryFamilyCmd(strings.TrimPrefix(cmd, "MEMORY."), args)

	// ─── Phase 13 — resilience & coordination primitives (CIRCUIT /
	// SAGA / CRDT) ─────────────────────────────────────────────────
	case "CIRCUIT.CONFIG", "CIRCUIT.RECORD", "CIRCUIT.CHECK", "CIRCUIT.STATE",
		"CIRCUIT.TRIP", "CIRCUIT.RESET", "CIRCUIT.FORGET", "CIRCUIT.LIST", "CIRCUIT.STATS":
		c.circuitCmd(strings.TrimPrefix(cmd, "CIRCUIT."), args)
	case "SAGA.START", "SAGA.STEP", "SAGA.COMPLETE", "SAGA.FAIL", "SAGA.STATUS",
		"SAGA.LIST", "SAGA.FORGET", "SAGA.STATS":
		c.sagaCmd(strings.TrimPrefix(cmd, "SAGA."), args)
	case "CRDT.GINCR", "CRDT.GVALUE", "CRDT.PNINCR", "CRDT.PNVALUE",
		"CRDT.SADD", "CRDT.SREM", "CRDT.SMEMBERS", "CRDT.SISMEMBER",
		"CRDT.LWWSET", "CRDT.LWWGET", "CRDT.MERGE", "CRDT.STATE",
		"CRDT.TYPE", "CRDT.LIST", "CRDT.FORGET", "CRDT.STATS":
		c.crdtCmd(strings.TrimPrefix(cmd, "CRDT."), args)
	// ─── Phase 12 — uniqueness primitives (CHURN/WORKER/FLAG/AUDIT/
	// TRACE/DOC/OBSERVE) ───────────────────────────────────────────
	case "CHURN.TAG", "CHURN.UNTAG", "CHURN.INVALIDATE", "CHURN.KEYS", "CHURN.TAGS_OF", "CHURN.TAGS", "CHURN.STATS":
		c.churnCmd(strings.TrimPrefix(cmd, "CHURN."), args)
	case "WORKER.ENQUEUE", "WORKER.DEQUEUE", "WORKER.ACK", "WORKER.NACK", "WORKER.STATS", "WORKER.DLQ", "WORKER.REQUEUE", "WORKER.CONFIG", "WORKER.QUEUES":
		c.workerCmd(strings.TrimPrefix(cmd, "WORKER."), args)
	case "FLAG.SET", "FLAG.IS", "FLAG.ALLOW", "FLAG.DENY", "FLAG.GET", "FLAG.LIST", "FLAG.DELETE":
		c.flagCmd(strings.TrimPrefix(cmd, "FLAG."), args)
	case "AUDIT.LOG", "AUDIT.QUERY", "AUDIT.COUNT", "AUDIT.STATS", "AUDIT.RETENTION":
		c.auditCmd(strings.TrimPrefix(cmd, "AUDIT."), args)
	case "TRACE.START", "TRACE.END", "TRACE.ANNOTATE", "TRACE.GET", "TRACE.LIST", "TRACE.FORGET", "TRACE.STATS":
		c.traceCmd(strings.TrimPrefix(cmd, "TRACE."), args)
	case "DOC.INIT", "DOC.APPLY", "DOC.GET", "DOC.SINCE", "DOC.LIST", "DOC.FORGET":
		c.docCmd(strings.TrimPrefix(cmd, "DOC."), args)
	case "OBSERVE.REGISTER", "OBSERVE.INC", "OBSERVE.SET", "OBSERVE.RENDER":
		c.observeCmd(strings.TrimPrefix(cmd, "OBSERVE."), args)

	case "KV.SUBSCRIBE":
		c.kvSubscribeCmd(args)
	case "KV.UNSUBSCRIBE":
		c.kvUnsubscribeCmd(args)
	case "TIME":
		now := time.Now()
		writeValue(c.bw, []any{
			strconv.FormatInt(now.Unix(), 10),
			strconv.FormatInt(int64(now.Nanosecond()/1000), 10),
		})
	case "FLUSHDB", "FLUSHALL":
		c.eng.KV.FlushAll()
		writeSimple(c.bw, "OK")

	// ─── compat fillers (Redis / Valkey / DiceDB) ─────────────────
	case "BRPOPLPUSH":
		c.brpoplpushCmd(args)
	case "MOVE":
		c.moveCmd(args)
	case "SWAPDB":
		c.swapdbCmd(args)
	case "EVICT":
		c.evictCmd(args)
	case "PFDEBUG":
		c.pfdebugCmd(args)
	case "PFSELFTEST":
		c.pfselftestCmd()
	case "RESTORE-ASKING", "RESTORE_ASKING":
		c.restoreAskingCmd(args)

	// ─── keys / TTL ─────────────────────────────────────────────────
	case "DEL", "UNLINK":
		writeInt(c.bw, int64(c.eng.KV.Del(args...)))
	case "EXISTS":
		writeInt(c.bw, int64(c.eng.KV.Exists(args...)))
	case "TYPE":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		writeSimple(c.bw, c.eng.KV.Type(args[0]).String())
	case "EXPIRE", "PEXPIRE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		d := time.Duration(n) * time.Second
		if cmd == "PEXPIRE" {
			d = time.Duration(n) * time.Millisecond
		}
		if c.eng.KV.Expire(args[0], d) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "EXPIREAT", "PEXPIREAT":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		var t time.Time
		if cmd == "EXPIREAT" {
			t = time.Unix(n, 0)
		} else {
			t = time.UnixMilli(n)
		}
		if c.eng.KV.ExpireAt(args[0], t) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PERSIST":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		if c.eng.KV.Persist(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "TTL":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		d := c.eng.KV.TTL(args[0])
		if d < 0 {
			writeInt(c.bw, int64(d))
			return
		}
		writeInt(c.bw, int64(d.Seconds()))
	case "PTTL":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		d := c.eng.KV.TTL(args[0])
		if d < 0 {
			writeInt(c.bw, int64(d))
			return
		}
		writeInt(c.bw, d.Milliseconds())
	case "KEYS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		writeArray(c.bw, c.eng.KV.Keys(args[0]))
	case "RENAME":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		if !c.eng.KV.Rename(args[0], args[1]) {
			writeError(c.bw, "no such key")
			return
		}
		writeSimple(c.bw, "OK")
	case "RENAMENX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		if c.eng.KV.RenameNX(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SCAN":
		c.scanCmd(args)
	case "RANDOMKEY":
		keys := c.eng.KV.Keys("*")
		if len(keys) == 0 {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, keys[0])

	// ─── strings ───────────────────────────────────────────────────
	case "SET":
		c.setCmd(args)
	case "SETNX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		if c.eng.KV.SetNX(args[0], args[1], 0) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SETEX":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			writeError(c.bw, "invalid expire time in 'setex'")
			return
		}
		c.eng.KV.Set(args[0], args[2], time.Duration(n)*time.Second)
		writeSimple(c.bw, "OK")
	case "PSETEX":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			writeError(c.bw, "invalid expire time in 'psetex'")
			return
		}
		c.eng.KV.Set(args[0], args[2], time.Duration(n)*time.Millisecond)
		writeSimple(c.bw, "OK")
	case "GET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.GetTyped(args[0])
		c.eng.Metrics.RecordKVHit(args[0], ok)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "GETSET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		prev, had, err := c.eng.KV.GetSet(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !had {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, prev)
	case "MSET":
		if len(args) < 2 || len(args)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for MSET")
			return
		}
		if err := c.eng.KV.MSet(args...); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "MSETNX":
		if len(args) < 2 || len(args)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for MSETNX")
			return
		}
		ok, err := c.eng.KV.MSetNX(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "MGET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		vals, hits, _ := c.eng.KV.MGet(args...)
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			} else {
				out[i] = nil
			}
		}
		writeValue(c.bw, out)
	case "APPEND":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.Append(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "STRLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.StrLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "GETRANGE", "SUBSTR":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		s, err := c.eng.KV.GetRange(args[0], a, b)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeBulk(c.bw, s)
	case "SETRANGE":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		off, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "offset is not an integer")
			return
		}
		n, err := c.eng.KV.SetRange(args[0], off, args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "INCR":
		c.incrBy(args, 1)
	case "DECR":
		c.incrBy(args, -1)
	case "INCRBY":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		c.incrBy(args[:1], d)
	case "DECRBY":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer or out of range")
			return
		}
		c.incrBy(args[:1], -d)
	case "INCRBYFLOAT":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		d, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "value is not a valid float")
			return
		}
		v, err := c.eng.KV.IncrByFloat(args[0], d)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', -1, 64))

	// ─── lists ─────────────────────────────────────────────────────
	case "LPUSH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.LPush(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "RPUSH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.RPush(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LPUSHX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.LPushX(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "RPUSHX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.RPushX(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LPOP":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.LPop(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "RPOP":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.RPop(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "LLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.LLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LINDEX":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		i, _ := strconv.Atoi(args[1])
		v, ok, err := c.eng.KV.LIndex(args[0], i)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "LRANGE":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		out, err := c.eng.KV.LRange(args[0], a, b)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "LSET":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		i, _ := strconv.Atoi(args[1])
		if err := c.eng.KV.LSet(args[0], i, args[2]); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "LREM":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		count, _ := strconv.Atoi(args[1])
		n, err := c.eng.KV.LRem(args[0], count, args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "LTRIM":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		a, _ := strconv.Atoi(args[1])
		b, _ := strconv.Atoi(args[2])
		if err := c.eng.KV.LTrim(args[0], a, b); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")
	case "LINSERT":
		if !c.wantArgs(cmd, args, 4) {
			return
		}
		before := strings.EqualFold(args[1], "BEFORE")
		n, err := c.eng.KV.LInsert(args[0], before, args[2], args[3])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "RPOPLPUSH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		v, ok, err := c.eng.KV.RPopLPush(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)

	// ─── hashes ────────────────────────────────────────────────────
	case "HSET", "HMSET":
		if !c.wantArgs(cmd, args, 3) || (len(args)-1)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for "+cmd)
			return
		}
		n, err := c.eng.KV.HSet(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if cmd == "HMSET" {
			writeSimple(c.bw, "OK")
			return
		}
		writeInt(c.bw, int64(n))
	case "HSETNX":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		ok, err := c.eng.KV.HSetNX(args[0], args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "HGET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		v, ok, err := c.eng.KV.HGet(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "HMGET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		vals, hits, err := c.eng.KV.HMGet(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		out := make([]any, len(vals))
		for i := range vals {
			if hits[i] {
				out[i] = vals[i]
			} else {
				out[i] = nil
			}
		}
		writeValue(c.bw, out)
	case "HGETALL":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.HGetAll(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "HDEL":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.HDel(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "HEXISTS":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		ok, err := c.eng.KV.HExists(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "HLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.HLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "HKEYS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.HKeys(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "HVALS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.HVals(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "HINCRBY":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		d, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not an integer")
			return
		}
		v, err := c.eng.KV.HIncrBy(args[0], args[1], d)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, v)
	case "HINCRBYFLOAT":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		d, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "value is not a float")
			return
		}
		v, err := c.eng.KV.HIncrByFloat(args[0], args[1], d)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', -1, 64))
	case "HSTRLEN":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.HStrLen(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "HSCAN":
		c.hscanCmd(args)

	// ─── sets ──────────────────────────────────────────────────────
	case "SADD":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SAdd(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SREM":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SRem(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SISMEMBER":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		ok, err := c.eng.KV.SIsMember(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SMEMBERS":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		out, err := c.eng.KV.SMembers(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SCARD":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.SCard(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SPOP":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, ok, err := c.eng.KV.SPop(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "SRANDMEMBER":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		count := 1
		useArray := false
		if len(args) >= 2 {
			useArray = true
			n, err := strconv.Atoi(args[1])
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			count = n
		}
		out, err := c.eng.KV.SRandMember(args[0], count)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !useArray {
			if len(out) == 0 {
				writeNil(c.bw)
				return
			}
			writeBulk(c.bw, out[0])
			return
		}
		writeArray(c.bw, out)
	case "SMOVE":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		ok, err := c.eng.KV.SMove(args[0], args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if ok {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "SINTER":
		out, err := c.eng.KV.SInter(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SUNION":
		out, err := c.eng.KV.SUnion(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SDIFF":
		out, err := c.eng.KV.SDiff(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeArray(c.bw, out)
	case "SINTERSTORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SInterStore(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SUNIONSTORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SUnionStore(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SDIFFSTORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.SDiffStore(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "SSCAN":
		c.sscanCmd(args)

	// ─── sorted sets ───────────────────────────────────────────────
	case "ZADD":
		c.zaddCmd(args)
	case "ZSCORE":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		sc, ok, err := c.eng.KV.ZScore(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeFloat(c.bw, sc)
	case "ZREM":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.ZRem(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "ZCARD":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.ZCard(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "ZINCRBY":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		d, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "value is not a float")
			return
		}
		sc, err := c.eng.KV.ZIncrBy(args[0], d, args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeFloat(c.bw, sc)
	case "ZRANK":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		r, ok, err := c.eng.KV.ZRank(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeInt(c.bw, int64(r))
	case "ZREVRANK":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		r, ok, err := c.eng.KV.ZRevRank(args[0], args[1])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeNil(c.bw)
			return
		}
		writeInt(c.bw, int64(r))
	case "ZRANGE":
		c.zrangeCmd(args, false)
	case "ZREVRANGE":
		c.zrangeCmd(args, true)
	case "ZRANGEBYSCORE":
		c.zrangeByScoreCmd(args, false)
	case "ZREVRANGEBYSCORE":
		c.zrangeByScoreCmd(args, true)
	case "ZCOUNT":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := c.eng.KV.ZCount(args[0], args[1], args[2])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "ZPOPMIN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		m, sc, ok, err := c.eng.KV.ZPopMin(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeArray(c.bw, []string{})
			return
		}
		writeValue(c.bw, []any{m, strconv.FormatFloat(sc, 'f', -1, 64)})
	case "ZPOPMAX":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		m, sc, ok, err := c.eng.KV.ZPopMax(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		if !ok {
			writeArray(c.bw, []string{})
			return
		}
		writeValue(c.bw, []any{m, strconv.FormatFloat(sc, 'f', -1, 64)})
	case "ZSCAN":
		c.zscanCmd(args)

	// ─── pub/sub ───────────────────────────────────────────────────
	case "SUBSCRIBE":
		c.subscribeCmd(args, false)
	case "UNSUBSCRIBE":
		c.unsubscribeCmd(args, false)
	case "PSUBSCRIBE":
		c.subscribeCmd(args, true)
	case "PUNSUBSCRIBE":
		c.unsubscribeCmd(args, true)
	case "PUBLISH":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		writeInt(c.bw, int64(c.eng.PubSub.Publish(args[0], args[1])))
	case "PUBSUB":
		c.pubsubCmd(args)

	// ─── transactions ──────────────────────────────────────────────
	case "MULTI":
		if err := c.tx.Begin(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "EXEC":
		c.execCmd()
	case "DISCARD":
		if err := c.tx.Discard(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "WATCH":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		for _, k := range args {
			if err := c.tx.Watch(k, c.eng.KeyVersion(k)); err != nil {
				writeError(c.bw, err.Error())
				return
			}
		}
		writeSimple(c.bw, "OK")
	case "UNWATCH":
		c.tx.Unwatch()
		writeSimple(c.bw, "OK")

	// ─── bitmaps ───────────────────────────────────────────────────
	case "SETBIT":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		off, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "bit offset is not an integer")
			return
		}
		v, err := strconv.Atoi(args[2])
		if err != nil {
			writeError(c.bw, "bit is not an integer")
			return
		}
		prev, err := c.eng.KV.SetBit(args[0], off, v)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(prev))
	case "GETBIT":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		off, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "bit offset is not an integer")
			return
		}
		v, err := c.eng.KV.GetBit(args[0], off)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(v))
	case "BITCOUNT":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		hasRange := len(args) >= 3
		start, end := 0, -1
		if hasRange {
			start, _ = strconv.Atoi(args[1])
			end, _ = strconv.Atoi(args[2])
		}
		n, err := c.eng.KV.BitCount(args[0], start, end, hasRange)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "BITPOS":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		bit, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "bit must be 0 or 1")
			return
		}
		start, end := 0, -1
		hasEnd := false
		if len(args) >= 3 {
			start, _ = strconv.Atoi(args[2])
		}
		if len(args) >= 4 {
			end, _ = strconv.Atoi(args[3])
			hasEnd = true
		}
		n, err := c.eng.KV.BitPos(args[0], bit, start, end, hasEnd)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "BITOP":
		if !c.wantArgs(cmd, args, 3) {
			return
		}
		n, err := c.eng.KV.BitOp(args[0], args[1], args[2:])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))

	// ─── HyperLogLog ───────────────────────────────────────────────
	case "PFADD":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		var members []string
		if len(args) >= 2 {
			members = args[1:]
		}
		n, err := c.eng.KV.PFAdd(args[0], members...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "PFCOUNT":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.PFCount(args...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, n)
	case "PFMERGE":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		if err := c.eng.KV.PFMerge(args[0], args[1:]...); err != nil {
			c.writeStoreErr(err)
			return
		}
		writeSimple(c.bw, "OK")

	// ─── streams ───────────────────────────────────────────────────
	case "XADD":
		c.xaddCmd(args)
	case "XLEN":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		n, err := c.eng.KV.XLen(args[0])
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "XRANGE":
		c.xrangeCmd(args, false)
	case "XREVRANGE":
		c.xrangeCmd(args, true)
	case "XDEL":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		n, err := c.eng.KV.XDel(args[0], args[1:]...)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
	case "XTRIM":
		c.xtrimCmd(args)
	case "XREAD":
		c.xreadCmd(args)

	// ─── geo ───────────────────────────────────────────────────────
	case "GEOADD":
		c.geoaddCmd(args)
	case "GEOPOS":
		c.geoposCmd(args)
	case "GEODIST":
		c.geodistCmd(args)
	case "GEOSEARCH":
		c.geosearchCmd(args)
	case "GEOHASH":
		c.geohashCmd(args)

	// ─── persistence ───────────────────────────────────────────────
	case "SAVE":
		if err := c.eng.SaveRDB(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "BGSAVE":
		if err := c.eng.BGSaveRDB(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "Background saving started")
	case "BGREWRITEAOF":
		if err := c.eng.BGRewriteAOF(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "Background append only file rewriting started")
	case "LASTSAVE":
		writeInt(c.bw, c.eng.LastSave())

	// ─── AI-native ─────────────────────────────────────────────────
	case "SEMANTIC_SET":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		c.eng.Semantic.Set(args[0], args[1])
		writeSimple(c.bw, "OK")
	case "SEMANTIC_GET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, _, ok := c.eng.Semantic.Get(args[0], float32(c.eng.Cfg.SemThreshold))
		c.eng.Metrics.RecordSemantic(ok)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "CACHE_LLM":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		c.eng.LLM.Set(args[0], args[1])
		writeSimple(c.bw, "OK")
	case "CACHE_LLM_GET":
		if !c.wantArgs(cmd, args, 1) {
			return
		}
		v, _, ok := c.eng.LLM.Get(args[0], 0.88)
		c.eng.Metrics.RecordLLM(ok)
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, v)
	case "MEMORY_ADD":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		e := c.eng.Memory.Add(args[0], strings.Join(args[1:], " "), nil)
		writeBulk(c.bw, e.ID)
	case "MEMORY_QUERY":
		if !c.wantArgs(cmd, args, 2) {
			return
		}
		hits := c.eng.Memory.Query(args[0], strings.Join(args[1:], " "), 5, 0.3)
		writeBulk(c.bw, memory.Synthesize(hits))

	default:
		// Module-registered commands take the slow path: built-ins
		// always win on name collision, but anything new is claimed
		// here.
		if c.dispatchModule(cmd, args) {
			return
		}
		writeError(c.bw, "unknown command '"+cmd+"'")
	}
}

// ─── shared helpers ─────────────────────────────────────────────────────

func (c *conn) infoString() string {
	i := c.eng.Info()
	return fmt.Sprintf("neurocache_version:%s\r\nuptime_in_seconds:%d\r\nkeys:%d\r\nused_memory:%d\r\nconnected_clients:%d\r\n",
		i.Version, int(i.UptimeSeconds), i.KV.Keys, i.KV.Bytes, i.Runtime.Goroutines)
}

// setCmd handles SET with [EX seconds | PX ms | NX | XX].
func (c *conn) setCmd(args []string) {
	if !c.wantArgs("SET", args, 2) {
		return
	}
	key, value := args[0], args[1]
	var ttl time.Duration
	nx, xx := false, false
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "EX":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			ttl = time.Duration(n) * time.Second
			i++
		case "PX":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			ttl = time.Duration(n) * time.Millisecond
			i++
		case "NX":
			nx = true
		case "XX":
			xx = true
		default:
			writeError(c.bw, "syntax error")
			return
		}
	}
	if nx {
		if !c.eng.KV.SetNX(key, value, ttl) {
			writeNil(c.bw)
			return
		}
		writeSimple(c.bw, "OK")
		return
	}
	if xx {
		if c.eng.KV.Exists(key) == 0 {
			writeNil(c.bw)
			return
		}
	}
	c.eng.KV.Set(key, value, ttl)
	writeSimple(c.bw, "OK")
}

// incrBy is the shared body for INCR/DECR/INCRBY/DECRBY after the delta
// has been parsed.
func (c *conn) incrBy(args []string, delta int64) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	v, err := c.eng.KV.Incr(args[0], delta)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, v)
}

// ─── ZADD / ZRANGE helpers ─────────────────────────────────────────────

func (c *conn) zaddCmd(args []string) {
	if len(args) < 3 || len(args)%2 == 0 {
		writeError(c.bw, "wrong number of arguments for 'zadd'")
		return
	}
	pairs := make([]store.ZPair, 0, (len(args)-1)/2)
	for i := 1; i+1 < len(args); i += 2 {
		sc, err := strconv.ParseFloat(args[i], 64)
		if err != nil {
			writeError(c.bw, "value is not a valid float")
			return
		}
		pairs = append(pairs, store.ZPair{Score: sc, Member: args[i+1]})
	}
	n, err := c.eng.KV.ZAdd(args[0], pairs...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

func (c *conn) zrangeCmd(args []string, reverse bool) {
	if !c.wantArgs("ZRANGE", args, 3) {
		return
	}
	a, _ := strconv.Atoi(args[1])
	b, _ := strconv.Atoi(args[2])
	withScores := false
	for _, t := range args[3:] {
		if strings.EqualFold(t, "WITHSCORES") {
			withScores = true
		}
	}
	out, err := c.eng.KV.ZRange(args[0], a, b, withScores, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

func (c *conn) zrangeByScoreCmd(args []string, reverse bool) {
	if !c.wantArgs("ZRANGEBYSCORE", args, 3) {
		return
	}
	withScores := false
	offset, count := 0, -1
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "WITHSCORES":
			withScores = true
		case "LIMIT":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			offset, _ = strconv.Atoi(args[i+1])
			count, _ = strconv.Atoi(args[i+2])
			i += 2
		}
	}
	minArg, maxArg := args[1], args[2]
	if reverse {
		minArg, maxArg = args[2], args[1]
	}
	out, err := c.eng.KV.ZRangeByScore(args[0], minArg, maxArg, offset, count, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeZRange(c.bw, out, withScores)
}

func writeZRange(w *bufio.Writer, out []store.ZRangeResult, withScores bool) {
	if !withScores {
		members := make([]string, len(out))
		for i, r := range out {
			members[i] = r.Member
		}
		writeArray(w, members)
		return
	}
	flat := make([]string, 0, len(out)*2)
	for _, r := range out {
		flat = append(flat, r.Member, strconv.FormatFloat(r.Score, 'f', -1, 64))
	}
	writeArray(w, flat)
}

// ─── SCAN helpers ──────────────────────────────────────────────────────

func (c *conn) scanCmd(args []string) {
	cursor := "0"
	if len(args) >= 1 {
		cursor = args[0]
	}
	match, typeFilter, count := parseScanOpts(args[1:])
	next, keys := c.eng.KV.Scan(cursor, match, typeFilter, count)
	writeValue(c.bw, []any{next, keys})
}

func (c *conn) hscanCmd(args []string) {
	if !c.wantArgs("HSCAN", args, 2) {
		return
	}
	match, _, count := parseScanOpts(args[2:])
	next, out, err := c.eng.KV.HScan(args[0], args[1], match, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{next, out})
}

func (c *conn) sscanCmd(args []string) {
	if !c.wantArgs("SSCAN", args, 2) {
		return
	}
	match, _, count := parseScanOpts(args[2:])
	next, out, err := c.eng.KV.SScan(args[0], args[1], match, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{next, out})
}

func (c *conn) zscanCmd(args []string) {
	if !c.wantArgs("ZSCAN", args, 2) {
		return
	}
	match, _, count := parseScanOpts(args[2:])
	next, out, err := c.eng.KV.ZScan(args[0], args[1], match, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeValue(c.bw, []any{next, out})
}

func parseScanOpts(args []string) (match string, typeFilter string, count int) {
	count = 10
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "MATCH":
			if i+1 < len(args) {
				match = args[i+1]
				i++
			}
		case "COUNT":
			if i+1 < len(args) {
				count, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "TYPE":
			if i+1 < len(args) {
				typeFilter = args[i+1]
				i++
			}
		}
	}
	return
}

// ─── pub/sub helpers ───────────────────────────────────────────────────

// subscribeCmd registers one subscription per channel and starts a
// background goroutine that pushes inbound messages to the client. Redis
// sends back one reply per channel with the running subscription count.
func (c *conn) subscribeCmd(args []string, pattern bool) {
	if len(args) == 0 {
		writeError(c.bw, "wrong number of arguments")
		return
	}
	for _, ch := range args {
		if pattern {
			if _, already := c.psub[ch]; already {
				continue
			}
			sub := c.eng.PubSub.PSubscribe(ch)
			c.psub[ch] = sub
			go c.pumpSubscription(sub, true)
		} else {
			if _, already := c.subs[ch]; already {
				continue
			}
			sub := c.eng.PubSub.Subscribe(ch)
			c.subs[ch] = sub
			go c.pumpSubscription(sub, false)
		}
		kind := "subscribe"
		if pattern {
			kind = "psubscribe"
		}
		writeValue(c.bw, []any{kind, ch, int64(len(c.subs) + len(c.psub))})
	}
}

func (c *conn) unsubscribeCmd(args []string, pattern bool) {
	targets := args
	if len(targets) == 0 {
		if pattern {
			for ch := range c.psub {
				targets = append(targets, ch)
			}
		} else {
			for ch := range c.subs {
				targets = append(targets, ch)
			}
		}
	}
	if len(targets) == 0 {
		kind := "unsubscribe"
		if pattern {
			kind = "punsubscribe"
		}
		writeValue(c.bw, []any{kind, nil, int64(len(c.subs) + len(c.psub))})
		return
	}
	for _, ch := range targets {
		if pattern {
			if sub, ok := c.psub[ch]; ok {
				sub.Close()
				delete(c.psub, ch)
			}
		} else {
			if sub, ok := c.subs[ch]; ok {
				sub.Close()
				delete(c.subs, ch)
			}
		}
		kind := "unsubscribe"
		if pattern {
			kind = "punsubscribe"
		}
		writeValue(c.bw, []any{kind, ch, int64(len(c.subs) + len(c.psub))})
	}
}

// pumpSubscription forwards broker messages to the TCP client, locking
// the writer so a push never interleaves with a command reply.
func (c *conn) pumpSubscription(sub *pubsub.Subscription, pattern bool) {
	for {
		select {
		case <-c.done:
			return
		case m, ok := <-sub.Ch():
			if !ok {
				return
			}
			c.writeMu.Lock()
			if pattern {
				writeValue(c.bw, []any{"pmessage", m.Pattern, m.Channel, m.Payload})
			} else {
				writeValue(c.bw, []any{"message", m.Channel, m.Payload})
			}
			_ = c.bw.Flush()
			c.writeMu.Unlock()
		}
	}
}

func (c *conn) pubsubCmd(args []string) {
	if !c.wantArgs("PUBSUB", args, 1) {
		return
	}
	switch strings.ToUpper(args[0]) {
	case "SHARDCHANNELS", "SHARDNUMSUB":
		c.pubsubShardCmd(args)
		return
	case "CHANNELS":
		pat := "*"
		if len(args) >= 2 {
			pat = args[1]
		}
		writeArray(c.bw, c.eng.PubSub.Channels(pat))
	case "NUMSUB":
		counts := c.eng.PubSub.NumSub(args[1:]...)
		out := make([]any, 0, len(args[1:])*2)
		for _, ch := range args[1:] {
			out = append(out, ch, int64(counts[ch]))
		}
		writeValue(c.bw, out)
	case "NUMPAT":
		writeInt(c.bw, int64(c.eng.PubSub.NumPat()))
	default:
		writeError(c.bw, "unknown PUBSUB subcommand")
	}
}

// ─── EXEC ──────────────────────────────────────────────────────────────

// ─── stream helpers ────────────────────────────────────────────────────

func (c *conn) xaddCmd(args []string) {
	if !c.wantArgs("XADD", args, 4) {
		return
	}
	noMkStream := false
	maxLen := 0
	minID := ""
	i := 1
	for i < len(args) {
		switch strings.ToUpper(args[i]) {
		case "NOMKSTREAM":
			noMkStream = true
			i++
		case "MAXLEN":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			offset := i + 1
			if args[offset] == "~" || args[offset] == "=" {
				offset++
			}
			if offset >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			n, err := strconv.Atoi(args[offset])
			if err != nil {
				writeError(c.bw, "invalid MAXLEN")
				return
			}
			maxLen = n
			i = offset + 1
		case "MINID":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			offset := i + 1
			if args[offset] == "~" || args[offset] == "=" {
				offset++
			}
			if offset >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			minID = args[offset]
			i = offset + 1
		default:
			goto idArg
		}
	}
idArg:
	if i >= len(args) {
		writeError(c.bw, "syntax error")
		return
	}
	id := args[i]
	fields := args[i+1:]
	if len(fields) == 0 || len(fields)%2 != 0 {
		writeError(c.bw, "wrong number of arguments for 'xadd'")
		return
	}
	if noMkStream && c.eng.KV.Type(args[0]).String() == "none" {
		writeNil(c.bw)
		return
	}
	assigned, err := c.eng.KV.XAdd(args[0], id, fields, maxLen)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if minID != "" {
		_, _ = c.eng.KV.XTrimMinID(args[0], minID)
	}
	writeBulk(c.bw, assigned)
}

// xsetidCmd: XSETID key last-id [ENTRIESADDED n] [MAXDELETEDID id]
func (c *conn) xsetidCmd(args []string) {
	if !c.wantArgs("XSETID", args, 2) {
		return
	}
	if err := c.eng.KV.XSetID(args[0], args[1]); err != nil {
		c.writeStoreErr(err)
		return
	}
	writeSimple(c.bw, "OK")
}

func (c *conn) xrangeCmd(args []string, reverse bool) {
	if !c.wantArgs("XRANGE", args, 3) {
		return
	}
	count := 0
	for i := 3; i < len(args); i++ {
		if strings.EqualFold(args[i], "COUNT") && i+1 < len(args) {
			count, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	start, end := args[1], args[2]
	if reverse {
		start, end = args[1], args[2] // caller gives start>end for XREVRANGE; we handle in store
	}
	entries, err := c.eng.KV.XRange(args[0], start, end, count, reverse)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeStreamEntries(c.bw, entries)
}

func (c *conn) xtrimCmd(args []string) {
	if !c.wantArgs("XTRIM", args, 3) {
		return
	}
	if !strings.EqualFold(args[1], "MAXLEN") {
		writeError(c.bw, "XTRIM requires MAXLEN strategy")
		return
	}
	// accept optional "~" approximate marker
	idx := 2
	if args[idx] == "~" || args[idx] == "=" {
		idx++
	}
	if idx >= len(args) {
		writeError(c.bw, "syntax error")
		return
	}
	n, err := strconv.Atoi(args[idx])
	if err != nil {
		writeError(c.bw, "invalid MAXLEN")
		return
	}
	removed, err := c.eng.KV.XTrim(args[0], n)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(removed))
}

func (c *conn) xreadCmd(args []string) {
	// XREAD [COUNT n] [BLOCK ms] STREAMS key [key ...] id [id ...]
	if len(args) < 3 {
		writeError(c.bw, "wrong number of arguments for 'xread'")
		return
	}
	count := 0
	block := time.Duration(-1)
	i := 0
	for ; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "BLOCK":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			ms, _ := strconv.Atoi(args[i+1])
			block = time.Duration(ms) * time.Millisecond
			i++
		case "STREAMS":
			i++
			goto streams
		}
	}
streams:
	rest := args[i:]
	if len(rest) == 0 || len(rest)%2 != 0 {
		writeError(c.bw, "Unbalanced XREAD STREAMS keys and IDs")
		return
	}
	n := len(rest) / 2
	keys := rest[:n]
	ids := rest[n:]

	// Non-blocking pass.
	out, err := c.eng.KV.XRead(keys, ids, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if len(out) > 0 || block < 0 {
		writeXReadResult(c.bw, keys, out)
		return
	}
	// Block until any of the keys gets a new entry. The blocker fires
	// on every XADD; a wake just means "re-poll" — another consumer may
	// have raced us, in which case we re-block.
	deadline := time.Time{}
	if block > 0 {
		deadline = time.Now().Add(block)
	}
	for {
		w := c.eng.Blocker.RegisterFor(c.info.ID, keys...)
		out, err = c.eng.KV.XRead(keys, ids, count)
		if err != nil {
			w.Cancel()
			c.writeStoreErr(err)
			return
		}
		if len(out) > 0 {
			w.Cancel()
			writeXReadResult(c.bw, keys, out)
			return
		}
		var remaining time.Duration
		if !deadline.IsZero() {
			remaining = time.Until(deadline)
			if remaining <= 0 {
				w.Cancel()
				writeNilArray(c.bw)
				return
			}
		}
		_ = c.bw.Flush()
		_, woke := w.Wait(remaining)
		external := w.UnblockedExternal()
		errored := w.UnblockedByError()
		w.Cancel()
		if !woke {
			writeNilArray(c.bw)
			return
		}
		if external {
			if errored {
				writeTypedError(c.bw, "UNBLOCKED", "client unblocked via CLIENT UNBLOCK")
				return
			}
			writeNilArray(c.bw)
			return
		}
	}
}

func writeStreamEntries(w *bufio.Writer, entries []store.StreamEntry) {
	fmt.Fprintf(w, "*%d\r\n", len(entries))
	for _, e := range entries {
		// each entry is [id, [field, value, ...]]
		fmt.Fprintf(w, "*2\r\n")
		writeBulk(w, e.ID.String())
		writeArray(w, e.Fields)
	}
}

func writeXReadResult(w *bufio.Writer, keys []string, out map[string][]store.StreamEntry) {
	present := 0
	for _, k := range keys {
		if _, ok := out[k]; ok {
			present++
		}
	}
	fmt.Fprintf(w, "*%d\r\n", present)
	for _, k := range keys {
		es, ok := out[k]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "*2\r\n")
		writeBulk(w, k)
		writeStreamEntries(w, es)
	}
}

// ─── geo helpers ───────────────────────────────────────────────────────

func (c *conn) geoaddCmd(args []string) {
	if len(args) < 4 || (len(args)-1)%3 != 0 {
		writeError(c.bw, "wrong number of arguments for 'geoadd'")
		return
	}
	entries := make([]store.GeoAddEntry, 0, (len(args)-1)/3)
	for i := 1; i+2 < len(args); i += 3 {
		lon, err := strconv.ParseFloat(args[i], 64)
		if err != nil {
			writeError(c.bw, "invalid longitude")
			return
		}
		lat, err := strconv.ParseFloat(args[i+1], 64)
		if err != nil {
			writeError(c.bw, "invalid latitude")
			return
		}
		entries = append(entries, store.GeoAddEntry{Lon: lon, Lat: lat, Member: args[i+2]})
	}
	n, err := c.eng.KV.GeoAdd(args[0], entries...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

func (c *conn) geoposCmd(args []string) {
	if !c.wantArgs("GEOPOS", args, 2) {
		return
	}
	pts, err := c.eng.KV.GeoPos(args[0], args[1:]...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	fmt.Fprintf(c.bw, "*%d\r\n", len(pts))
	for _, p := range pts {
		if p == nil {
			writeNilArray(c.bw)
			continue
		}
		writeArray(c.bw, []string{
			strconv.FormatFloat(p.Lon, 'f', 10, 64),
			strconv.FormatFloat(p.Lat, 'f', 10, 64),
		})
	}
}

func (c *conn) geodistCmd(args []string) {
	if !c.wantArgs("GEODIST", args, 3) {
		return
	}
	unit := "m"
	if len(args) >= 4 {
		unit = strings.ToLower(args[3])
	}
	d, ok, err := c.eng.KV.GeoDist(args[0], args[1], args[2], unit)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeBulk(c.bw, strconv.FormatFloat(d, 'f', 4, 64))
}

func (c *conn) geosearchCmd(args []string) {
	// GEOSEARCH key FROMLONLAT lon lat BYRADIUS radius unit [COUNT n] [ASC|DESC]
	if !c.wantArgs("GEOSEARCH", args, 7) {
		return
	}
	var lon, lat, radius float64
	unit := "m"
	count := 0
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "FROMLONLAT":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			lon, _ = strconv.ParseFloat(args[i+1], 64)
			lat, _ = strconv.ParseFloat(args[i+2], 64)
			i += 2
		case "BYRADIUS":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			radius, _ = strconv.ParseFloat(args[i+1], 64)
			unit = strings.ToLower(args[i+2])
			i += 2
		case "COUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	out, err := c.eng.KV.GeoSearch(args[0], lat, lon, radius, unit, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	names := make([]string, len(out))
	for i, r := range out {
		names[i] = r.Member
	}
	writeArray(c.bw, names)
}

func (c *conn) geohashCmd(args []string) {
	if !c.wantArgs("GEOHASH", args, 2) {
		return
	}
	out, err := c.eng.KV.GeoHash(args[0], args[1:]...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeArray(c.bw, out)
}

// execCmd replays the queued commands after checking WATCHed keys.
// Each queued command is dispatched through the normal path so any side
// effects (pub/sub notifications, metrics) fire just like a direct call.
func (c *conn) execCmd() {
	c.tx.CheckDirty(c.eng.KeyVersion)
	queued, aborted := c.tx.Commit()
	if aborted {
		writeNilArray(c.bw)
		return
	}
	// Emit an array header, then dispatch each queued command with its
	// own nested reply. We flush the buffered writer between each so
	// multi-value replies (HGETALL etc.) stream correctly.
	fmt.Fprintf(c.bw, "*%d\r\n", len(queued))
	for _, q := range queued {
		c.dispatch(q.Cmd, q.Args)
	}
}
