package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTP surface for the AI-stack primitives. Mirrors the RESP commands
// 1:1 but in JSON — same shape every dashboard / SDK consumes.

// ── EMB.* ───────────────────────────────────────────────────────────

type embSetReq struct {
	Text   string    `json:"text"`
	Vector []float32 `json:"vector"`
	TTLSec int       `json:"ttl_sec,omitempty"`
}

func (h *handlers) embSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("EMB.CACHE_SET", time.Now())
	var req embSetReq
	if err := readJSON(r, &req); err != nil || req.Text == "" || len(req.Vector) == 0 {
		writeErr(w, 400, "text + vector required")
		return
	}
	ttl := time.Duration(0)
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	h.eng.EmbCache.Set(req.Text, req.Vector, ttl)
	h.eng.RecordWrite("EMB.CACHE_SET", []string{req.Text, formatVector(req.Vector), "EX", strconv.Itoa(req.TTLSec)})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) embGet(w http.ResponseWriter, r *http.Request) {
	defer h.record("EMB.CACHE_GET", time.Now())
	text := r.URL.Query().Get("text")
	if text == "" {
		writeErr(w, 400, "?text= required")
		return
	}
	vec, ok := h.eng.EmbCache.Get(text)
	if !ok {
		writeJSON(w, 200, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"hit": true, "vector": vec})
}

func (h *handlers) embStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.EmbCache.Stats())
}

func (h *handlers) embPurge(w http.ResponseWriter, _ *http.Request) {
	defer h.record("EMB.PURGE", time.Now())
	n := h.eng.EmbCache.Purge()
	h.eng.RecordWrite("EMB.PURGE", nil)
	writeJSON(w, 200, map[string]int{"dropped": n})
}

// ── CONV.* ──────────────────────────────────────────────────────────

type convAppendReq struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (h *handlers) convAppend(w http.ResponseWriter, r *http.Request) {
	defer h.record("CONV.APPEND", time.Now())
	key := r.PathValue("key")
	var req convAppendReq
	if err := readJSON(r, &req); err != nil || req.Role == "" {
		writeErr(w, 400, "role required")
		return
	}
	n, err := h.eng.Conversations.Append(key, req.Role, req.Content)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("CONV.APPEND", []string{key, req.Role, req.Content})
	writeJSON(w, 200, map[string]int{"turns": n})
}

func (h *handlers) convWindow(w http.ResponseWriter, r *http.Request) {
	defer h.record("CONV.WINDOW", time.Now())
	key := r.PathValue("key")
	max := 0
	if v := r.URL.Query().Get("max_tokens"); v != "" {
		max, _ = strconv.Atoi(v)
	}
	turns := h.eng.Conversations.Window(key, max)
	writeJSON(w, 200, map[string]any{"turns": turns})
}

type convSummarizeReq struct {
	Summary string `json:"summary"`
	KeepTok int    `json:"keep_tokens,omitempty"`
}

func (h *handlers) convSummarize(w http.ResponseWriter, r *http.Request) {
	defer h.record("CONV.SUMMARIZE", time.Now())
	key := r.PathValue("key")
	var req convSummarizeReq
	if err := readJSON(r, &req); err != nil || req.Summary == "" {
		writeErr(w, 400, "summary required")
		return
	}
	dropped, total, err := h.eng.Conversations.Summarize(key, req.Summary, req.KeepTok)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	h.eng.RecordWrite("CONV.SUMMARIZE", []string{key, req.Summary, "KEEP", strconv.Itoa(req.KeepTok)})
	writeJSON(w, 200, map[string]int{"dropped_turns": dropped, "tokens_remaining": total})
}

func (h *handlers) convReset(w http.ResponseWriter, r *http.Request) {
	defer h.record("CONV.RESET", time.Now())
	key := r.PathValue("key")
	had := h.eng.Conversations.Reset(key)
	if had {
		h.eng.RecordWrite("CONV.RESET", []string{key})
	}
	writeJSON(w, 200, map[string]bool{"reset": had})
}

func (h *handlers) convList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"conversations": h.eng.Conversations.Keys(),
		"count":         h.eng.Conversations.Size(),
	})
}

// ── PROMPT.* ────────────────────────────────────────────────────────

type promptSetReq struct {
	Body    string `json:"body"`
	Version int    `json:"version,omitempty"`
}

func (h *handlers) promptSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("PROMPT.SET", time.Now())
	name := r.PathValue("name")
	var req promptSetReq
	if err := readJSON(r, &req); err != nil || req.Body == "" {
		writeErr(w, 400, "body required")
		return
	}
	v, err := h.eng.Prompts.Set(name, req.Version, req.Body)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("PROMPT.SET", []string{name, req.Body, "VERSION", strconv.Itoa(v)})
	writeJSON(w, 200, map[string]int{"version": v})
}

func (h *handlers) promptGet(w http.ResponseWriter, r *http.Request) {
	defer h.record("PROMPT.GET", time.Now())
	name := r.PathValue("name")
	ver := 0
	if v := r.URL.Query().Get("version"); v != "" {
		ver, _ = strconv.Atoi(v)
	}
	pv, ok := h.eng.Prompts.Get(name, ver)
	if !ok {
		writeErr(w, 404, "no such template")
		return
	}
	writeJSON(w, 200, pv)
}

type promptRenderReq struct {
	Version int               `json:"version,omitempty"`
	Vars    map[string]string `json:"vars"`
}

func (h *handlers) promptRender(w http.ResponseWriter, r *http.Request) {
	defer h.record("PROMPT.RENDER", time.Now())
	name := r.PathValue("name")
	var req promptRenderReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	out, err := h.eng.Prompts.Render(name, req.Version, req.Vars)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"rendered": out})
}

func (h *handlers) promptList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Prompts.List())
}

func (h *handlers) promptVersions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	writeJSON(w, 200, h.eng.Prompts.Versions(name))
}

func (h *handlers) promptDelete(w http.ResponseWriter, r *http.Request) {
	defer h.record("PROMPT.DELETE", time.Now())
	name := r.PathValue("name")
	ver := 0
	if v := r.URL.Query().Get("version"); v != "" {
		ver, _ = strconv.Atoi(v)
	}
	removed := h.eng.Prompts.Delete(name, ver)
	if removed > 0 {
		args := []string{name}
		if ver > 0 {
			args = append(args, "VERSION", strconv.Itoa(ver))
		}
		h.eng.RecordWrite("PROMPT.DELETE", args)
	}
	writeJSON(w, 200, map[string]int{"removed": removed})
}

// formatVector renders a []float32 as a comma-separated decimal list,
// matching what the RESP layer accepts. Used so HTTP-driven writes
// replay through AOF identically to RESP-driven ones.
func formatVector(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	return strings.Join(parts, ",")
}
