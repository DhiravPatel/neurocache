package http

import (
	"log/slog"
	"net/http"

	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
)

func NewRouter(eng *engine.Engine, cfg config.Config, log *slog.Logger) http.Handler {
	h := &handlers{eng: eng, cfg: cfg, log: log}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/info", h.info)

	// Metrics / analytics
	mux.HandleFunc("GET /api/metrics/summary", h.metricsSummary)
	mux.HandleFunc("GET /api/metrics/timeline", h.metricsTimeline)
	mux.HandleFunc("GET /api/metrics/hot-keys", h.metricsHotKeys)
	mux.HandleFunc("GET /api/metrics/breakdown", h.metricsBreakdown)
	mux.HandleFunc("GET /api/hotkeys", h.hotKeysTracker)
	mux.HandleFunc("GET /api/vector/sets", h.vectorSets)

	// KV
	mux.HandleFunc("POST /api/kv", h.kvSet)
	mux.HandleFunc("GET /api/kv", h.kvList)
	mux.HandleFunc("GET /api/kv/{key}", h.kvGet)
	mux.HandleFunc("DELETE /api/kv/{key}", h.kvDel)
	mux.HandleFunc("POST /api/kv/{key}/incr", h.kvIncr)
	mux.HandleFunc("POST /api/kv/{key}/expire", h.kvExpire)

	// Semantic cache
	mux.HandleFunc("POST /api/semantic", h.semanticSet)
	mux.HandleFunc("GET /api/semantic", h.semanticGet)

	// LLM cache
	mux.HandleFunc("POST /api/llm", h.llmSet)
	mux.HandleFunc("GET /api/llm", h.llmGet)
	mux.HandleFunc("GET /api/llm/stats", h.llmStats)

	// Memory
	mux.HandleFunc("POST /api/memory/{user}", h.memoryAdd)
	mux.HandleFunc("GET /api/memory/{user}", h.memoryQueryOrList)
	mux.HandleFunc("DELETE /api/memory/{user}/{id}", h.memoryDel)

	// AI-stack: embedding cache
	mux.HandleFunc("POST /api/emb-cache", h.embSet)
	mux.HandleFunc("GET /api/emb-cache", h.embGet)
	mux.HandleFunc("GET /api/emb-cache/stats", h.embStats)
	mux.HandleFunc("POST /api/emb-cache/purge", h.embPurge)

	// AI-stack: conversation/session management
	mux.HandleFunc("POST /api/conv/{key}", h.convAppend)
	mux.HandleFunc("GET /api/conv/{key}", h.convWindow)
	mux.HandleFunc("POST /api/conv/{key}/summarize", h.convSummarize)
	mux.HandleFunc("DELETE /api/conv/{key}", h.convReset)
	mux.HandleFunc("GET /api/conv", h.convList)

	// AI-stack: versioned prompt templates
	mux.HandleFunc("POST /api/prompts/{name}", h.promptSet)
	mux.HandleFunc("GET /api/prompts/{name}", h.promptGet)
	mux.HandleFunc("POST /api/prompts/{name}/render", h.promptRender)
	mux.HandleFunc("GET /api/prompts/{name}/versions", h.promptVersions)
	mux.HandleFunc("DELETE /api/prompts/{name}", h.promptDelete)
	mux.HandleFunc("GET /api/prompts", h.promptList)

	// AI-ops: agent tool cache
	mux.HandleFunc("POST /api/agent", h.agentStore)
	mux.HandleFunc("GET /api/agent", h.agentCall)
	mux.HandleFunc("POST /api/agent/profile", h.agentProfile)
	mux.HandleFunc("DELETE /api/agent", h.agentForget)
	mux.HandleFunc("GET /api/agent/stats", h.agentStats)
	mux.HandleFunc("POST /api/agent/purge", h.agentPurge)

	// AI-ops: stream cache
	mux.HandleFunc("POST /api/stream", h.streamSet)
	mux.HandleFunc("GET /api/stream", h.streamGet)
	mux.HandleFunc("GET /api/stream/replay", h.streamReplay)
	mux.HandleFunc("DELETE /api/stream", h.streamForget)
	mux.HandleFunc("POST /api/stream/purge", h.streamPurge)
	mux.HandleFunc("GET /api/stream/stats", h.streamStats)

	// AI-ops: cost budgets
	mux.HandleFunc("POST /api/cost/{tenant}/budget", h.costBudget)
	mux.HandleFunc("POST /api/cost/{tenant}/charge", h.costCharge)
	mux.HandleFunc("GET /api/cost/{tenant}", h.costUsage)
	mux.HandleFunc("POST /api/cost/{tenant}/reset", h.costReset)
	mux.HandleFunc("GET /api/cost", h.costList)

	// AI-ops: shadow cache
	mux.HandleFunc("POST /api/shadow/{key}", h.shadowPut)
	mux.HandleFunc("GET /api/shadow/{key}", h.shadowGet)
	mux.HandleFunc("DELETE /api/shadow/{key}", h.shadowForget)
	mux.HandleFunc("GET /api/shadow/stats", h.shadowStats)

	// AI-ops: personas
	mux.HandleFunc("POST /api/persona/{user}", h.personaSet)
	mux.HandleFunc("GET /api/persona/{user}", h.personaGet)
	mux.HandleFunc("GET /api/persona/{user}/list", h.personaList)
	mux.HandleFunc("DELETE /api/persona/{user}", h.personaForget)

	// AI-ops: moderation / safety
	mux.HandleFunc("POST /api/safe", h.safeSet)
	mux.HandleFunc("GET /api/safe", h.safeCheck)
	mux.HandleFunc("GET /api/safe/inject", h.safeInject)
	mux.HandleFunc("DELETE /api/safe", h.safeForget)
	mux.HandleFunc("POST /api/safe/purge", h.safePurge)
	mux.HandleFunc("GET /api/safe/stats", h.safeStats)

	// AI-ops: lineage / provenance
	mux.HandleFunc("POST /api/lineage", h.lineageRecord)
	mux.HandleFunc("GET /api/lineage/stats", h.lineageStats)
	mux.HandleFunc("GET /api/lineage/sources/{source_id}/consumers", h.lineageConsumers)
	mux.HandleFunc("GET /api/lineage/{output_id}", h.lineageList)
	mux.HandleFunc("GET /api/lineage/{output_id}/sources", h.lineageSources)
	mux.HandleFunc("DELETE /api/lineage/{output_id}", h.lineageForget)

	// AI-ops: SLO tracking
	mux.HandleFunc("POST /api/slo/{cmd}", h.sloSet)
	mux.HandleFunc("GET /api/slo", h.sloSnapshot)
	mux.HandleFunc("POST /api/slo/reset", h.sloReset)

	// AI-ops: A/B experiments
	mux.HandleFunc("POST /api/ab", h.abDefine)
	mux.HandleFunc("GET /api/ab", h.abList)
	mux.HandleFunc("GET /api/ab/{name}/assign", h.abAssign)
	mux.HandleFunc("POST /api/ab/{name}/expose", h.abExpose)
	mux.HandleFunc("POST /api/ab/{name}/record", h.abRecord)
	mux.HandleFunc("GET /api/ab/{name}", h.abStats)
	mux.HandleFunc("POST /api/ab/{name}/reset", h.abReset)
	mux.HandleFunc("DELETE /api/ab/{name}", h.abDelete)

	// AI-ops: knowledge graph
	mux.HandleFunc("POST /api/graph/link", h.graphLink)
	mux.HandleFunc("POST /api/graph/unlink", h.graphUnlink)
	mux.HandleFunc("GET /api/graph/neighbors", h.graphNeighbors)
	mux.HandleFunc("GET /api/graph/in", h.graphIn)
	mux.HandleFunc("GET /api/graph/path", h.graphPath)
	mux.HandleFunc("GET /api/graph/subjects", h.graphSubjects)
	mux.HandleFunc("GET /api/graph/stats", h.graphStats)

	// AI-ops: scheduler
	mux.HandleFunc("POST /api/schedule/at", h.scheduleAt)
	mux.HandleFunc("POST /api/schedule/in", h.scheduleIn)
	mux.HandleFunc("DELETE /api/schedule/{id}", h.scheduleCancel)
	mux.HandleFunc("GET /api/schedule", h.scheduleList)
	mux.HandleFunc("GET /api/schedule/stats", h.scheduleStats)

	// AI-ops: event log
	mux.HandleFunc("POST /api/event/{stream}", h.eventAppend)
	mux.HandleFunc("POST /api/event/{stream}/project", h.eventProject)
	mux.HandleFunc("GET /api/event/{stream}/projection/{name}", h.eventRead)
	mux.HandleFunc("GET /api/event/{stream}/range", h.eventRange)
	mux.HandleFunc("GET /api/event/{stream}/len", h.eventLen)

	// AI-ops: policy verdict cache
	mux.HandleFunc("POST /api/policy/allow", h.policyAllow)
	mux.HandleFunc("POST /api/policy/set", h.policySet)
	mux.HandleFunc("POST /api/policy/purge", h.policyPurge)
	mux.HandleFunc("GET /api/policy/stats", h.policyStats)

	// AI-ops: inference proxy
	mux.HandleFunc("POST /api/infer", h.inferGenerate)
	mux.HandleFunc("DELETE /api/infer", h.inferForget)
	mux.HandleFunc("POST /api/infer/purge", h.inferPurge)
	mux.HandleFunc("GET /api/infer/stats", h.inferStats)
	mux.HandleFunc("POST /api/infer/default", h.inferDefault)

	// AI-ops: MCP server
	mux.HandleFunc("GET /api/mcp/tools", h.mcpTools)
	mux.HandleFunc("GET /api/mcp/resources", h.mcpResources)
	mux.HandleFunc("POST /api/mcp/call", h.mcpCall)
	mux.HandleFunc("GET /api/mcp/read", h.mcpRead)
	mux.HandleFunc("POST /api/mcp/rpc", h.mcpRPC)

	// Phase 12: tagged cache invalidation (CHURN.*)
	mux.HandleFunc("POST /api/churn/invalidate", h.churnInvalidate)
	mux.HandleFunc("GET /api/churn/keys", h.churnKeys)
	mux.HandleFunc("GET /api/churn/tags", h.churnTags)
	mux.HandleFunc("GET /api/churn/stats", h.churnStats)
	mux.HandleFunc("POST /api/churn/{key}", h.churnTag)
	mux.HandleFunc("DELETE /api/churn/{key}", h.churnUntag)
	mux.HandleFunc("GET /api/churn/{key}", h.churnTagsOf)

	// Phase 12: production job queues (WORKER.*)
	mux.HandleFunc("GET /api/worker", h.workerQueues)
	mux.HandleFunc("POST /api/worker/{queue}", h.workerEnqueue)
	mux.HandleFunc("GET /api/worker/{queue}/next", h.workerDequeue)
	mux.HandleFunc("POST /api/worker/{queue}/ack/{id}", h.workerAck)
	mux.HandleFunc("POST /api/worker/{queue}/nack/{id}", h.workerNack)
	mux.HandleFunc("GET /api/worker/{queue}/stats", h.workerStats)
	mux.HandleFunc("GET /api/worker/{queue}/dlq", h.workerDLQ)
	mux.HandleFunc("POST /api/worker/{queue}/requeue/{id}", h.workerRequeue)
	mux.HandleFunc("POST /api/worker/{queue}/config", h.workerConfig)

	// Phase 12: feature flags (FLAG.*)
	mux.HandleFunc("GET /api/flag", h.flagList)
	mux.HandleFunc("POST /api/flag/{name}", h.flagSet)
	mux.HandleFunc("GET /api/flag/{name}/is", h.flagIs)
	mux.HandleFunc("POST /api/flag/{name}/allow", h.flagAllow)
	mux.HandleFunc("POST /api/flag/{name}/deny", h.flagDeny)
	mux.HandleFunc("GET /api/flag/{name}", h.flagGet)
	mux.HandleFunc("DELETE /api/flag/{name}", h.flagDelete)

	// Phase 12: structured audit log (AUDIT.*)
	mux.HandleFunc("POST /api/audit", h.auditLog)
	mux.HandleFunc("GET /api/audit", h.auditQuery)
	mux.HandleFunc("GET /api/audit/stats", h.auditStats)
	mux.HandleFunc("POST /api/audit/retention", h.auditRetention)

	// Phase 12: in-memory distributed tracing (TRACE.*)
	mux.HandleFunc("GET /api/trace", h.traceList)
	mux.HandleFunc("GET /api/trace/stats", h.traceStats)
	mux.HandleFunc("POST /api/trace/{trace_id}/{span_id}", h.traceStart)
	mux.HandleFunc("POST /api/trace/{trace_id}/{span_id}/end", h.traceEnd)
	mux.HandleFunc("POST /api/trace/{trace_id}/{span_id}/annotate", h.traceAnnotate)
	mux.HandleFunc("GET /api/trace/{trace_id}", h.traceGet)
	mux.HandleFunc("DELETE /api/trace/{trace_id}", h.traceForget)

	// Phase 12: JSON-Patch document sync (DOC.*)
	mux.HandleFunc("GET /api/doc", h.docList)
	mux.HandleFunc("POST /api/doc/{key}", h.docInit)
	mux.HandleFunc("PATCH /api/doc/{key}", h.docApply)
	mux.HandleFunc("GET /api/doc/{key}", h.docGet)
	mux.HandleFunc("GET /api/doc/{key}/since", h.docSince)
	mux.HandleFunc("DELETE /api/doc/{key}", h.docForget)

	// Phase 12: Prometheus exporter (OBSERVE.*)
	mux.HandleFunc("GET /metrics", h.observeRender)
	mux.HandleFunc("POST /api/observe/register", h.observeRegister)
	mux.HandleFunc("POST /api/observe/inc", h.observeInc)
	mux.HandleFunc("POST /api/observe/gauge", h.observeSet)

	// Raw command (like redis-cli EVAL)
	mux.HandleFunc("POST /api/exec", h.exec)

	// Admin
	mux.HandleFunc("POST /api/flushall", h.flushAll)

	return withCORS(cfg.CORSOrigins, withLogging(log, mux))
}
