package http

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
)

type handlers struct {
	eng *engine.Engine
	cfg config.Config
	log *slog.Logger
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dst)
}

// ─── health / info ───

func (h *handlers) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"uptime": time.Since(h.eng.StartedAt).Seconds(),
	})
}

func (h *handlers) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Info())
}

// ─── KV ───

type kvSetReq struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTL   int    `json:"ttl,omitempty"` // seconds
}

func (h *handlers) kvSet(w http.ResponseWriter, r *http.Request) {
	var req kvSetReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	h.eng.CmdCount.Add(1)
	h.eng.KV.Set(req.Key, req.Value, time.Duration(req.TTL)*time.Second)
	writeJSON(w, 200, map[string]any{"ok": true, "key": req.Key})
}

func (h *handlers) kvGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	h.eng.CmdCount.Add(1)
	v, ok := h.eng.KV.Get(key)
	if !ok {
		writeJSON(w, 404, map[string]any{"key": key, "value": nil, "hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"key": key, "value": v, "hit": true})
}

func (h *handlers) kvDel(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	h.eng.CmdCount.Add(1)
	n := h.eng.KV.Del(key)
	writeJSON(w, 200, map[string]any{"deleted": n})
}

func (h *handlers) kvList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	out := make([]map[string]any, 0, limit)
	for _, k := range h.eng.KV.Keys() {
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		v, ok := h.eng.KV.Get(k)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"key": k, "value": v})
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, 200, map[string]any{"keys": out, "total": h.eng.KV.Size()})
}

type incrReq struct {
	By int64 `json:"by"`
}

func (h *handlers) kvIncr(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	req := incrReq{By: 1}
	_ = readJSON(r, &req)
	if req.By == 0 {
		req.By = 1
	}
	h.eng.CmdCount.Add(1)
	v, err := h.eng.KV.Incr(key, req.By)
	if err != nil {
		writeErr(w, 400, "value is not an integer")
		return
	}
	writeJSON(w, 200, map[string]any{"key": key, "value": v})
}

type expireReq struct {
	TTL int `json:"ttl"`
}

func (h *handlers) kvExpire(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req expireReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	h.eng.CmdCount.Add(1)
	ok := h.eng.KV.Expire(key, time.Duration(req.TTL)*time.Second)
	writeJSON(w, 200, map[string]any{"ok": ok})
}

// ─── Semantic ───

type semSetReq struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (h *handlers) semanticSet(w http.ResponseWriter, r *http.Request) {
	var req semSetReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	h.eng.CmdCount.Add(1)
	id := h.eng.Semantic.Set(req.Key, req.Value)
	writeJSON(w, 200, map[string]any{"ok": true, "id": id})
}

func (h *handlers) semanticGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, 400, "q required")
		return
	}
	threshold := h.cfg.SemThreshold
	if t, err := strconv.ParseFloat(r.URL.Query().Get("threshold"), 64); err == nil {
		threshold = t
	}
	h.eng.CmdCount.Add(1)
	v, score, ok := h.eng.Semantic.Get(q, float32(threshold))
	writeJSON(w, 200, map[string]any{
		"query": q,
		"hit":   ok,
		"value": nullable(v, ok),
		"score": score,
	})
}

// ─── LLM cache ───

type llmSetReq struct {
	Prompt   string `json:"prompt"`
	Response string `json:"response"`
}

func (h *handlers) llmSet(w http.ResponseWriter, r *http.Request) {
	var req llmSetReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Prompt == "" {
		writeErr(w, 400, "prompt required")
		return
	}
	h.eng.CmdCount.Add(1)
	h.eng.LLM.Set(req.Prompt, req.Response)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (h *handlers) llmGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("prompt")
	if q == "" {
		writeErr(w, 400, "prompt required")
		return
	}
	threshold := 0.88
	if t, err := strconv.ParseFloat(r.URL.Query().Get("threshold"), 64); err == nil {
		threshold = t
	}
	h.eng.CmdCount.Add(1)
	v, score, ok := h.eng.LLM.Get(q, float32(threshold))
	writeJSON(w, 200, map[string]any{
		"prompt":   q,
		"hit":      ok,
		"response": nullable(v, ok),
		"score":    score,
	})
}

func (h *handlers) llmStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.LLM.Stats())
}

// ─── Memory ───

type memAddReq struct {
	Text string            `json:"text"`
	Meta map[string]string `json:"meta,omitempty"`
}

func (h *handlers) memoryAdd(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	var req memAddReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Text == "" {
		writeErr(w, 400, "text required")
		return
	}
	h.eng.CmdCount.Add(1)
	e := h.eng.Memory.Add(user, req.Text, req.Meta)
	writeJSON(w, 200, e)
}

func (h *handlers) memoryQueryOrList(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	q := r.URL.Query().Get("q")
	h.eng.CmdCount.Add(1)
	if q == "" {
		writeJSON(w, 200, map[string]any{"user": user, "entries": h.eng.Memory.List(user)})
		return
	}
	k := 5
	if v, err := strconv.Atoi(r.URL.Query().Get("k")); err == nil && v > 0 {
		k = v
	}
	threshold := 0.3
	if t, err := strconv.ParseFloat(r.URL.Query().Get("threshold"), 64); err == nil {
		threshold = t
	}
	hits := h.eng.Memory.Query(user, q, k, float32(threshold))
	writeJSON(w, 200, map[string]any{
		"user":    user,
		"query":   q,
		"hits":    hits,
		"context": memory.Synthesize(hits),
	})
}

func (h *handlers) memoryDel(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	id := r.PathValue("id")
	h.eng.CmdCount.Add(1)
	ok := h.eng.Memory.Delete(user, id)
	writeJSON(w, 200, map[string]any{"deleted": ok})
}

// ─── Admin / exec ───

func (h *handlers) flushAll(w http.ResponseWriter, _ *http.Request) {
	h.eng.KV.FlushAll()
	writeJSON(w, 200, map[string]any{"ok": true})
}

type execReq struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// exec is a convenience endpoint so the web playground can send
// Redis-style commands like {"command":"SET","args":["k","v"]}.
func (h *handlers) exec(w http.ResponseWriter, r *http.Request) {
	var req execReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	h.eng.CmdCount.Add(1)
	result, err := h.dispatch(strings.ToUpper(req.Command), req.Args)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "result": result})
}

func nullable[T any](v T, ok bool) any {
	if !ok {
		return nil
	}
	return v
}
