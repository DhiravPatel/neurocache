package http

import (
	"net/http"
	"time"
)

// ─── Distributed locks over HTTP ─────────────────────────────────────
//
// The RESP port already speaks LOCK ACQUIRE/RELEASE/EXTEND/CHECK with
// monotonic fencing tokens. These handlers put a typed REST face on the same
// LockManager so web apps and the SDK can take leases, fence stale writers,
// and observe held locks without dropping to the RESP protocol.

type lockAcquireReq struct {
	Owner string `json:"owner"`
	TTLMs int64  `json:"ttl_ms"`
}

// acquireLock → POST /api/locks/{name}/acquire {owner, ttl_ms}
//   → {acquired, token}  (token is the fencing token; 0 when not acquired)
func (h *handlers) acquireLock(w http.ResponseWriter, r *http.Request) {
	defer h.record("LOCK.ACQUIRE", time.Now())
	name := r.PathValue("name")
	var req lockAcquireReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Owner == "" {
		writeErr(w, 400, "owner required")
		return
	}
	if req.TTLMs <= 0 {
		writeErr(w, 400, "ttl_ms must be positive")
		return
	}
	token, ok := h.eng.Locks.Acquire(name, req.Owner, time.Duration(req.TTLMs)*time.Millisecond)
	writeJSON(w, 200, map[string]any{"acquired": ok, "token": token})
}

type lockOwnerReq struct {
	Owner string `json:"owner"`
}

// releaseLock → POST /api/locks/{name}/release {owner} → {released}
func (h *handlers) releaseLock(w http.ResponseWriter, r *http.Request) {
	defer h.record("LOCK.RELEASE", time.Now())
	name := r.PathValue("name")
	var req lockOwnerReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Owner == "" {
		writeErr(w, 400, "owner required")
		return
	}
	writeJSON(w, 200, map[string]bool{"released": h.eng.Locks.Release(name, req.Owner)})
}

// extendLock → POST /api/locks/{name}/extend {owner, ttl_ms} → {extended}
func (h *handlers) extendLock(w http.ResponseWriter, r *http.Request) {
	defer h.record("LOCK.EXTEND", time.Now())
	name := r.PathValue("name")
	var req lockAcquireReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Owner == "" {
		writeErr(w, 400, "owner required")
		return
	}
	if req.TTLMs <= 0 {
		writeErr(w, 400, "ttl_ms must be positive")
		return
	}
	ok := h.eng.Locks.Extend(name, req.Owner, time.Duration(req.TTLMs)*time.Millisecond)
	writeJSON(w, 200, map[string]bool{"extended": ok})
}

// checkLock → GET /api/locks/{name} → {held, owner, token, remaining_ms}
func (h *handlers) checkLock(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	info, ok := h.eng.Locks.Check(name)
	if !ok {
		writeJSON(w, 200, map[string]any{"held": false})
		return
	}
	writeJSON(w, 200, map[string]any{
		"held":         true,
		"owner":        info.Owner,
		"token":        info.Token,
		"remaining_ms": info.RemMs,
	})
}

// listLocks → GET /api/locks → {locks: [...]}
func (h *handlers) listLocks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"locks": h.eng.Locks.List()})
}
