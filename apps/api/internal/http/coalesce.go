package http

import (
	"net/http"
	"time"
)

// ─── COALESCE.* (single-flight / thundering-herd protection) ──────────────
//
// Surfaces the engine's request coalescer over HTTP so the dashboard and
// SDK can drive and observe it. The coalescer collapses a burst of
// identical concurrent cache-miss calls into ONE upstream request:
// the first caller wins the lock and does the work; everyone else waits
// and is handed the same result. Its SaveRate (contended / locks) is the
// fraction of would-be upstream calls it eliminated — a direct
// throughput-and-cost win, which is why it earns a dashboard of its own.

type coalesceLockReq struct {
	Key       string `json:"key"`
	TimeoutMS int64  `json:"timeout_ms"`
}

type coalescePublishReq struct {
	Key    string `json:"key"`
	Token  string `json:"token"`
	Result string `json:"result"`
}

type coalesceKeyReq struct {
	Key string `json:"key"`
}

// coalesceStats — GET /api/coalesce/stats
func (h *handlers) coalesceStats(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.STATS", time.Now())
	writeJSON(w, 200, h.eng.Coalesce.Stats())
}

// coalesceKeys — GET /api/coalesce/keys (active herds, newest-first)
func (h *handlers) coalesceKeys(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.KEYS", time.Now())
	writeJSON(w, 200, map[string]any{"keys": h.eng.Coalesce.Keys()})
}

// coalesceLock — POST /api/coalesce/lock {key, timeout_ms}
// Returns {owner, token}. owner=true means the caller should do the work;
// owner=false means another caller already owns it — WAIT for the result.
func (h *handlers) coalesceLock(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.LOCK", time.Now())
	var req coalesceLockReq
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	writeJSON(w, 200, h.eng.Coalesce.Lock(req.Key, req.TimeoutMS))
}

// coalescePublish — POST /api/coalesce/publish {key, token, result}
// The owner publishes the answer; every waiter wakes with it. Returns
// {published} — false when the token doesn't match the current owner.
func (h *handlers) coalescePublish(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.PUBLISH", time.Now())
	var req coalescePublishReq
	if err := readJSON(r, &req); err != nil || req.Key == "" || req.Token == "" {
		writeErr(w, 400, "key + token required")
		return
	}
	writeJSON(w, 200, map[string]bool{"published": h.eng.Coalesce.Publish(req.Key, req.Token, req.Result)})
}

// coalesceWait — POST /api/coalesce/wait {key, timeout_ms}
// Blocks until the key is published or the timeout fires. Returns
// {got, result}. The HTTP handler caps the wait so a request can't hang a
// connection indefinitely; callers poll/retry on got=false.
func (h *handlers) coalesceWait(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.WAIT", time.Now())
	var req coalesceLockReq
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 25*time.Second {
		timeout = 25 * time.Second // bound the HTTP hold time
	}
	writeJSON(w, 200, h.eng.Coalesce.Wait(req.Key, timeout))
}

// coalesceStatus — GET /api/coalesce/status?key=
func (h *handlers) coalesceStatus(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.STATUS", time.Now())
	key := r.URL.Query().Get("key")
	if key == "" {
		writeErr(w, 400, "?key= required")
		return
	}
	st, ok := h.eng.Coalesce.Status(key)
	if !ok {
		writeJSON(w, 200, map[string]any{"exists": false})
		return
	}
	writeJSON(w, 200, map[string]any{"exists": true, "status": st})
}

// coalesceForget — POST /api/coalesce/forget {key}
func (h *handlers) coalesceForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("COALESCE.FORGET", time.Now())
	var req coalesceKeyReq
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	writeJSON(w, 200, map[string]bool{"forgotten": h.eng.Coalesce.Forget(req.Key)})
}
