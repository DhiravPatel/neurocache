package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// ─── Leaderboards over HTTP ──────────────────────────────────────────
//
// A leaderboard is a sorted set read highest-first. These handlers wrap the
// engine's ZSET (skip-list backed: O(log n) updates, O(log n) rank) with a
// game-friendly REST shape — set/increment a score, read the top N, and look
// up any member's rank and neighbours — so apps don't hand-roll ZADD/ZREVRANGE
// plumbing or compute ranks client-side.

type lbEntry struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
	Rank   int     `json:"rank"` // 1-based, highest score = rank 1
}

// rankOf returns the 1-based descending rank of member (0 if absent).
func (h *handlers) rankOf(name, member string) int {
	r, ok, err := h.eng.KV.ZRevRank(name, member)
	if err != nil || !ok {
		return 0
	}
	return r + 1
}

type lbSetReq struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

// lbSet → POST /api/leaderboard/{name} {member, score} → {member, score, rank}
func (h *handlers) lbSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("ZADD", time.Now())
	name := r.PathValue("name")
	var req lbSetReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Member == "" {
		writeErr(w, 400, "member required")
		return
	}
	if _, err := h.eng.KV.ZAdd(name, store.ZPair{Member: req.Member, Score: req.Score}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("ZADD", []string{name, strconv.FormatFloat(req.Score, 'g', -1, 64), req.Member})
	writeJSON(w, 200, lbEntry{Member: req.Member, Score: req.Score, Rank: h.rankOf(name, req.Member)})
}

type lbIncrReq struct {
	Member string  `json:"member"`
	By     float64 `json:"by"`
}

// lbIncr → POST /api/leaderboard/{name}/incr {member, by} → {member, score, rank}
func (h *handlers) lbIncr(w http.ResponseWriter, r *http.Request) {
	defer h.record("ZINCRBY", time.Now())
	name := r.PathValue("name")
	var req lbIncrReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if req.Member == "" {
		writeErr(w, 400, "member required")
		return
	}
	score, err := h.eng.KV.ZIncrBy(name, req.By, req.Member)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("ZINCRBY", []string{name, strconv.FormatFloat(req.By, 'g', -1, 64), req.Member})
	writeJSON(w, 200, lbEntry{Member: req.Member, Score: score, Rank: h.rankOf(name, req.Member)})
}

// lbTop → GET /api/leaderboard/{name}/top?n=10 → {count, entries:[…]}
func (h *handlers) lbTop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	n := queryInt(r, "n", 10)
	if n < 1 {
		n = 10
	}
	rows, err := h.eng.KV.ZRange(name, 0, n-1, true, true) // reverse = highest first
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	count, _ := h.eng.KV.ZCard(name)
	entries := make([]lbEntry, len(rows))
	for i, row := range rows {
		entries[i] = lbEntry{Member: row.Member, Score: row.Score, Rank: i + 1}
	}
	writeJSON(w, 200, map[string]any{"count": count, "entries": entries})
}

// lbRank → GET /api/leaderboard/{name}/rank/{member} → {member, score, rank} | {found:false}
func (h *handlers) lbRank(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	member := r.PathValue("member")
	rank, ok, err := h.eng.KV.ZRevRank(name, member)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !ok {
		writeJSON(w, 200, map[string]any{"found": false})
		return
	}
	score, _, _ := h.eng.KV.ZScore(name, member)
	writeJSON(w, 200, map[string]any{
		"found": true, "member": member, "score": score, "rank": rank + 1,
	})
}

// lbAround → GET /api/leaderboard/{name}/around/{member}?n=3
// Returns the member plus N neighbours on each side (the "your rank" view).
func (h *handlers) lbAround(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	member := r.PathValue("member")
	n := queryInt(r, "n", 3)
	if n < 0 {
		n = 3
	}
	rank, ok, err := h.eng.KV.ZRevRank(name, member)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !ok {
		writeJSON(w, 200, map[string]any{"found": false})
		return
	}
	start := rank - n
	if start < 0 {
		start = 0
	}
	rows, err := h.eng.KV.ZRange(name, start, rank+n, true, true)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	entries := make([]lbEntry, len(rows))
	for i, row := range rows {
		entries[i] = lbEntry{Member: row.Member, Score: row.Score, Rank: start + i + 1}
	}
	writeJSON(w, 200, map[string]any{"found": true, "entries": entries})
}

// lbRemove → DELETE /api/leaderboard/{name}/{member} → {removed}
func (h *handlers) lbRemove(w http.ResponseWriter, r *http.Request) {
	defer h.record("ZREM", time.Now())
	name := r.PathValue("name")
	member := r.PathValue("member")
	n, err := h.eng.KV.ZRem(name, member)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if n > 0 {
		h.eng.RecordWrite("ZREM", []string{name, member})
	}
	writeJSON(w, 200, map[string]bool{"removed": n > 0})
}

// queryInt reads an int query param with a default.
func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
