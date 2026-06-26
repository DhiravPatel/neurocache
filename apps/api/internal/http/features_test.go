package http

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRateLimitGate(t *testing.T) {
	h := testHandlers(t)
	body := `{"key":"user:42","window_ms":10000,"max":2}`
	hit := func() (int, bool) {
		rec := httptest.NewRecorder()
		h.rateLimitCheck(rec, httptest.NewRequest("POST", "/api/ratelimit", strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out["allowed"] == true
	}
	if c, ok := hit(); !ok || c != 200 {
		t.Fatalf("1st call: code=%d allowed=%v", c, ok)
	}
	if c, ok := hit(); !ok || c != 200 {
		t.Fatalf("2nd call: code=%d allowed=%v", c, ok)
	}
	// Burst of 2 exhausted → 3rd denied with HTTP 429.
	if c, ok := hit(); ok || c != 429 {
		t.Fatalf("3rd call should be denied 429: code=%d allowed=%v", c, ok)
	}
}

func TestLeaderboard(t *testing.T) {
	h := testHandlers(t)
	set := func(member string, score float64) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/leaderboard/game",
			strings.NewReader(`{"member":"`+member+`","score":`+ftoa(score)+`}`))
		req.SetPathValue("name", "game")
		h.lbSet(rec, req)
		if rec.Code != 200 {
			t.Fatalf("set %s: code=%d", member, rec.Code)
		}
	}
	set("alice", 100)
	set("bob", 200)
	set("carol", 150)

	// Top should be highest-first: bob, carol, alice.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/leaderboard/game/top?n=10", nil)
	req.SetPathValue("name", "game")
	h.lbTop(rec, req)
	var top struct {
		Count   int `json:"count"`
		Entries []struct {
			Member string  `json:"member"`
			Score  float64 `json:"score"`
			Rank   int     `json:"rank"`
		} `json:"entries"`
	}
	json.Unmarshal(rec.Body.Bytes(), &top)
	if top.Count != 3 || len(top.Entries) != 3 {
		t.Fatalf("top count/len wrong: %+v", top)
	}
	if top.Entries[0].Member != "bob" || top.Entries[0].Rank != 1 ||
		top.Entries[1].Member != "carol" || top.Entries[2].Member != "alice" {
		t.Fatalf("top order wrong: %+v", top.Entries)
	}

	// Rank of bob is 1.
	rec = httptest.NewRecorder()
	rreq := httptest.NewRequest("GET", "/api/leaderboard/game/rank/bob", nil)
	rreq.SetPathValue("name", "game")
	rreq.SetPathValue("member", "bob")
	h.lbRank(rec, rreq)
	if !strings.Contains(rec.Body.String(), `"rank":1`) {
		t.Fatalf("bob rank: %s", rec.Body.String())
	}
}

func TestStreamAddRange(t *testing.T) {
	h := testHandlers(t)
	rec := httptest.NewRecorder()
	areq := httptest.NewRequest("POST", "/api/streams/events",
		strings.NewReader(`{"fields":{"type":"signup","user":"42"}}`))
	areq.SetPathValue("key", "events")
	h.streamAdd(rec, areq)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("xadd: code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	rreq := httptest.NewRequest("GET", "/api/streams/events", nil)
	rreq.SetPathValue("key", "events")
	h.streamRange(rec, rreq)
	if !strings.Contains(rec.Body.String(), `"length":1`) ||
		!strings.Contains(rec.Body.String(), `"signup"`) {
		t.Fatalf("xrange: %s", rec.Body.String())
	}
}

func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
