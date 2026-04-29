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

	// Raw command (like redis-cli EVAL)
	mux.HandleFunc("POST /api/exec", h.exec)

	// Admin
	mux.HandleFunc("POST /api/flushall", h.flushAll)

	return withCORS(cfg.CORSOrigins, withLogging(log, mux))
}
