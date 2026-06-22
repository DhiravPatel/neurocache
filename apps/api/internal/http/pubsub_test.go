package http

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
)

func testHandlers(t *testing.T) *handlers {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(config.Config{}, log)
	return &handlers{eng: eng, cfg: config.Config{}, log: log}
}

func TestPublishAndChannels(t *testing.T) {
	h := testHandlers(t)

	// No subscribers yet → 0 receivers.
	rec := httptest.NewRecorder()
	h.publish(rec, httptest.NewRequest("POST", "/api/publish",
		strings.NewReader(`{"channel":"news","message":"hi"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"receivers":0`) {
		t.Fatalf("publish with no subs: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// Missing channel → 400.
	rec = httptest.NewRecorder()
	h.publish(rec, httptest.NewRequest("POST", "/api/publish", strings.NewReader(`{"message":"x"}`)))
	if rec.Code != 400 {
		t.Fatalf("publish without channel: code=%d", rec.Code)
	}

	// A live subscriber should show up in PUBSUB CHANNELS.
	sub := h.eng.PubSub.Subscribe("news")
	defer sub.Close()
	rec = httptest.NewRecorder()
	h.pubsubChannels(rec, httptest.NewRequest("GET", "/api/pubsub/channels?pattern=*", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"news"`) {
		t.Fatalf("channels: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSubscribeSSEStream(t *testing.T) {
	h := testHandlers(t)
	srv := httptest.NewServer(http.HandlerFunc(h.subscribe))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/subscribe?channel=room1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe request: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Give the subscription a moment to register, then publish.
	deadline := time.Now().Add(2 * time.Second)
	for h.eng.PubSub.NumSub("room1")["room1"] == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.eng.PubSub.Publish("room1", "hello-sse"); got != 1 {
		t.Fatalf("publish reached %d receivers, want 1", got)
	}

	// Read the stream until we see the published payload.
	type result struct{ line string }
	done := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "hello-sse") {
				done <- result{sc.Text()}
				return
			}
		}
		done <- result{""}
	}()

	select {
	case r := <-done:
		if !strings.Contains(r.line, `"payload":"hello-sse"`) {
			t.Fatalf("did not receive published message over SSE, got: %q", r.line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE message")
	}
}
