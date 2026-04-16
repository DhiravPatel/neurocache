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

	// Raw command (like redis-cli EVAL)
	mux.HandleFunc("POST /api/exec", h.exec)

	// Admin
	mux.HandleFunc("POST /api/flushall", h.flushAll)

	return withCORS(cfg.CORSOrigins, withLogging(log, mux))
}
