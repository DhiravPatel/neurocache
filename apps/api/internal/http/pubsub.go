package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
)

// ─── Pub/Sub over HTTP ───────────────────────────────────────────────
//
// The RESP port already speaks the full SUBSCRIBE / PSUBSCRIBE / PUBLISH /
// PUBSUB surface. These handlers put a browser- and SDK-friendly REST +
// Server-Sent Events face on the SAME broker, so a message PUBLISHed from
// redis-cli reaches an HTTP/SSE subscriber and vice-versa. SUBSCRIBE needs a
// streaming transport, which plain request/response can't offer — hence SSE.

type publishReq struct {
	Channel string `json:"channel"`
	Message string `json:"message"`
}

// publish → POST /api/publish  {channel, message} → {receivers}
func (h *handlers) publish(w http.ResponseWriter, r *http.Request) {
	defer h.record("PUBLISH", time.Now())
	var req publishReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Channel == "" {
		writeErr(w, 400, "channel required")
		return
	}
	n := h.eng.PubSub.Publish(req.Channel, req.Message)
	writeJSON(w, 200, map[string]int{"receivers": n})
}

// pubsubChannels → GET /api/pubsub/channels?pattern=*
func (h *handlers) pubsubChannels(w http.ResponseWriter, r *http.Request) {
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		pattern = "*"
	}
	channels := h.eng.PubSub.Channels(pattern)
	writeJSON(w, 200, map[string]any{
		"channels":     channels,
		"num_subs":     h.eng.PubSub.NumSub(channels...),
		"num_patterns": h.eng.PubSub.NumPat(),
	})
}

// subscribe → GET /api/subscribe?channel=a&channel=b&pattern=news.*
//
// Streams matching messages as Server-Sent Events until the client
// disconnects. Each message is a default ("message") SSE event whose data is
// {"channel","pattern","payload"}; an initial "subscribed" event confirms the
// active channels/patterns, and a ": ping" comment is sent periodically to
// keep intermediaries from dropping an idle connection.
func (h *handlers) subscribe(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channels := q["channel"]
	patterns := q["pattern"]
	if len(channels) == 0 && len(patterns) == 0 {
		writeErr(w, 400, "at least one channel or pattern is required")
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // don't let nginx buffer the stream
	w.WriteHeader(http.StatusOK)

	var subs []*pubsub.Subscription
	if len(channels) > 0 {
		subs = append(subs, h.eng.PubSub.Subscribe(channels...))
	}
	if len(patterns) > 0 {
		subs = append(subs, h.eng.PubSub.PSubscribe(patterns...))
	}
	defer func() {
		for _, s := range subs {
			s.Close()
		}
	}()
	h.record("SUBSCRIBE", time.Now())

	ctx := r.Context()
	writeSSE(w, "subscribed", map[string]any{"channels": channels, "patterns": patterns})
	_ = rc.Flush()

	// Fan the (≤2) subscriptions into one channel. Both client disconnect
	// (ctx) and broker detach (Close → channel close) unblock the forwarders,
	// so they never leak.
	out := make(chan pubsub.Message, 128)
	for _, s := range subs {
		go func(s *pubsub.Subscription) {
			for {
				select {
				case <-ctx.Done():
					return
				case m, ok := <-s.Ch():
					if !ok {
						return
					}
					select {
					case out <- m:
					case <-ctx.Done():
						return
					}
				}
			}
		}(s)
	}

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
		case m := <-out:
			writeSSE(w, "", map[string]any{
				"channel": m.Channel,
				"pattern": m.Pattern,
				"payload": m.Payload,
			})
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// writeSSE writes one Server-Sent Event. An empty event name produces a
// default ("message") event, which browser EventSource delivers via onmessage.
func writeSSE(w http.ResponseWriter, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	if event != "" {
		_, _ = w.Write([]byte("event: " + event + "\n"))
	}
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n\n"))
}
