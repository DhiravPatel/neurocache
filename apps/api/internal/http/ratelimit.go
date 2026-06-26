package http

import (
	"net/http"
	"time"
)

// ─── Rate limiting over HTTP ─────────────────────────────────────────
//
// The engine's GCRA limiter (smooth bursts, exact recovery rate, O(1) memory
// per key) was previously reachable only over RESP. These handlers expose it
// as a typed REST gate so HTTP middleware, edge functions, and the SDK can
// throttle by any key (user id, IP, tenant, route) with retry hints.

type rateLimitReq struct {
	Key      string `json:"key"`
	WindowMs int64  `json:"window_ms"`
	Max      int64  `json:"max"`
	Cost     int64  `json:"cost,omitempty"`
	Peek     bool   `json:"peek,omitempty"` // evaluate without consuming a slot
}

// rateLimitCheck → POST /api/ratelimit
//   {key, window_ms, max, cost?, peek?}
//   → {allowed, remaining, retry_after_ms, reset_ms}
func (h *handlers) rateLimitCheck(w http.ResponseWriter, r *http.Request) {
	defer h.record("RATELIMIT", time.Now())
	var req rateLimitReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	if req.WindowMs <= 0 || req.Max <= 0 {
		writeErr(w, 400, "window_ms and max must be positive")
		return
	}
	cost := req.Cost
	if cost <= 0 {
		cost = 1
	}
	window := time.Duration(req.WindowMs) * time.Millisecond
	eval := h.eng.RateLimit.Allow
	if req.Peek {
		eval = h.eng.RateLimit.Peek
	}
	allowed, remaining, retry, reset := eval(req.Key, window, req.Max, cost)
	status := 200
	if !allowed {
		status = 429 // Too Many Requests — usable directly as the response code
	}
	writeJSON(w, status, map[string]any{
		"allowed":        allowed,
		"remaining":      remaining,
		"retry_after_ms": retry,
		"reset_ms":       reset,
	})
}

type rateLimitResetReq struct {
	Key string `json:"key"`
}

// rateLimitReset → POST /api/ratelimit/reset {key}
func (h *handlers) rateLimitReset(w http.ResponseWriter, r *http.Request) {
	defer h.record("RATELIMIT.RESET", time.Now())
	var req rateLimitResetReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Key == "" {
		writeErr(w, 400, "key required")
		return
	}
	h.eng.RateLimit.Reset(req.Key)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
