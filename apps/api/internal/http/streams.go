package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// ─── Streams over HTTP ───────────────────────────────────────────────
//
// Redis streams are an append-only log with monotonic IDs. The RESP port has
// the full XADD/XRANGE/XREAD/XGROUP surface; these handlers add a JSON REST
// face plus a Server-Sent Events tail so browsers and the SDK can append
// events and follow a stream live without speaking RESP. (Consumer groups
// remain available over RESP for at-least-once fan-out.)

func fieldsToMap(f []string) map[string]string {
	m := make(map[string]string, len(f)/2)
	for i := 0; i+1 < len(f); i += 2 {
		m[f[i]] = f[i+1]
	}
	return m
}

func entryJSON(e store.StreamEntry) map[string]any {
	return map[string]any{"id": e.ID.String(), "fields": fieldsToMap(e.Fields)}
}

type streamAddReq struct {
	Fields map[string]string `json:"fields"`
	ID     string            `json:"id,omitempty"`
	MaxLen int               `json:"maxlen,omitempty"`
}

// streamAdd → POST /api/streams/{key} {fields, id?, maxlen?} → {id}
func (h *handlers) streamAdd(w http.ResponseWriter, r *http.Request) {
	defer h.record("XADD", time.Now())
	key := r.PathValue("key")
	var req streamAddReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.Fields) == 0 {
		writeErr(w, 400, "fields required")
		return
	}
	flat := make([]string, 0, len(req.Fields)*2)
	args := []string{key}
	for k, v := range req.Fields {
		flat = append(flat, k, v)
	}
	id := req.ID
	if id == "" {
		id = "*"
	}
	newID, err := h.eng.KV.XAdd(key, id, flat, req.MaxLen)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	args = append(args, newID)
	args = append(args, flat...)
	h.eng.RecordWrite("XADD", args)
	writeJSON(w, 200, map[string]string{"id": newID})
}

// streamRange → GET /api/streams/{key}?start=-&end=+&count=50&reverse=
func (h *handlers) streamRange(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	q := r.URL.Query()
	// Trim because an un-encoded "+" in a query string decodes to a space;
	// a blank start/end falls back to the full-range sentinels.
	start := strings.TrimSpace(q.Get("start"))
	end := strings.TrimSpace(q.Get("end"))
	if start == "" {
		start = "-"
	}
	if end == "" {
		end = "+"
	}
	reverse := q.Get("reverse") == "1" || q.Get("reverse") == "true"
	entries, err := h.eng.KV.XRange(key, start, end, queryInt(r, "count", 100), reverse)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	out := make([]map[string]any, len(entries))
	for i, e := range entries {
		out[i] = entryJSON(e)
	}
	length, _ := h.eng.KV.XLen(key)
	writeJSON(w, 200, map[string]any{"length": length, "entries": out})
}

// streamLen → GET /api/streams/{key}/len → {length}
func (h *handlers) streamLen(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	n, err := h.eng.KV.XLen(key)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"length": n})
}

// streamTail → GET /api/streams/{key}/tail?last=$
//
// Follows a stream as Server-Sent Events. `last` is the ID to read after:
// "$" (default) streams only entries added from now on; "0" replays the whole
// stream then follows. Implemented by polling the non-blocking XREAD.
func (h *handlers) streamTail(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	last := r.URL.Query().Get("last")
	if last == "" || last == "$" {
		if id, ok, _ := h.eng.KV.XLast(key); ok {
			last = id
		} else {
			last = "0"
		}
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	h.record("XREAD", time.Now())

	writeSSE(w, "subscribed", map[string]any{"stream": key, "from": last})
	_ = rc.Flush()

	ctx := r.Context()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			_ = rc.Flush()
		case <-poll.C:
			res, err := h.eng.KV.XRead([]string{key}, []string{last}, 100)
			if err != nil {
				continue
			}
			for _, e := range res[key] {
				writeSSE(w, "", entryJSON(e))
				last = e.ID.String()
			}
			if len(res[key]) > 0 {
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	}
}
