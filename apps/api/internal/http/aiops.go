package http

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// HTTP surface for the Phase 11 AI-ops primitives. Mirrors the RESP
// commands 1:1 in JSON. State lives in `internal/aiops/`; writes flow
// through `h.eng.RecordWrite` so AOF + replication propagate them like
// any other write-path command.

// ── AGENT.* ─────────────────────────────────────────────────────────

type agentStoreReq struct {
	Tool     string `json:"tool"`
	ArgsHash string `json:"args_hash"`
	Result   string `json:"result"`
}

func (h *handlers) agentStore(w http.ResponseWriter, r *http.Request) {
	defer h.record("AGENT.STORE", time.Now())
	var req agentStoreReq
	if err := readJSON(r, &req); err != nil || req.Tool == "" || req.ArgsHash == "" {
		writeErr(w, 400, "tool + args_hash + result required")
		return
	}
	h.eng.AgentCache.Set(req.Tool, req.ArgsHash, req.Result)
	h.eng.RecordWrite("AGENT.STORE", []string{req.Tool, req.ArgsHash, req.Result})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) agentCall(w http.ResponseWriter, r *http.Request) {
	defer h.record("AGENT.CALL", time.Now())
	tool := r.URL.Query().Get("tool")
	hash := r.URL.Query().Get("args_hash")
	if tool == "" || hash == "" {
		writeErr(w, 400, "?tool= and ?args_hash= required")
		return
	}
	v, ok := h.eng.AgentCache.Get(tool, hash)
	if !ok {
		writeJSON(w, 200, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"hit": true, "result": v})
}

type agentProfileReq struct {
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
}

func (h *handlers) agentProfile(w http.ResponseWriter, r *http.Request) {
	defer h.record("AGENT.PROFILE", time.Now())
	var req agentProfileReq
	if err := readJSON(r, &req); err != nil || req.Tool == "" {
		writeErr(w, 400, "tool + profile required")
		return
	}
	var d aiops.Determinism
	switch strings.ToLower(req.Profile) {
	case "always":
		d = aiops.DeterminismAlways
	case "day":
		d = aiops.DeterminismDay
	case "never":
		d = aiops.DeterminismNever
	default:
		writeErr(w, 400, "profile must be always|day|never")
		return
	}
	h.eng.AgentCache.SetProfile(req.Tool, d)
	h.eng.RecordWrite("AGENT.PROFILE", []string{req.Tool, req.Profile})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) agentForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("AGENT.FORGET", time.Now())
	tool := r.URL.Query().Get("tool")
	hash := r.URL.Query().Get("args_hash")
	if tool == "" || hash == "" {
		writeErr(w, 400, "?tool= and ?args_hash= required")
		return
	}
	ok := h.eng.AgentCache.Forget(tool, hash)
	if ok {
		h.eng.RecordWrite("AGENT.FORGET", []string{tool, hash})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

func (h *handlers) agentStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.AgentCache.Stats())
}

func (h *handlers) agentPurge(w http.ResponseWriter, _ *http.Request) {
	defer h.record("AGENT.PURGE", time.Now())
	n := h.eng.AgentCache.Purge()
	h.eng.RecordWrite("AGENT.PURGE", nil)
	writeJSON(w, 200, map[string]int{"dropped": n})
}

// ── STREAM.* ────────────────────────────────────────────────────────

type streamSetReq struct {
	PromptHash string               `json:"prompt_hash"`
	Tokens     []aiops.StreamToken  `json:"tokens"`
	TTLSec     int                  `json:"ttl_sec,omitempty"`
}

func (h *handlers) streamSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("STREAM.SET", time.Now())
	var req streamSetReq
	if err := readJSON(r, &req); err != nil || req.PromptHash == "" {
		writeErr(w, 400, "prompt_hash + tokens required")
		return
	}
	ttl := time.Duration(0)
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	h.eng.StreamCache.Set(req.PromptHash, req.Tokens, ttl)
	tokensJSON, _ := json.Marshal(req.Tokens)
	args := []string{req.PromptHash, string(tokensJSON)}
	if req.TTLSec > 0 {
		args = append(args, "EX", strconv.Itoa(req.TTLSec))
	}
	h.eng.RecordWrite("STREAM.SET", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) streamGet(w http.ResponseWriter, r *http.Request) {
	defer h.record("STREAM.GET", time.Now())
	hash := r.URL.Query().Get("prompt_hash")
	if hash == "" {
		writeErr(w, 400, "?prompt_hash= required")
		return
	}
	v, ok := h.eng.StreamCache.Get(hash)
	if !ok {
		writeJSON(w, 200, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"hit": true, "response": v})
}

func (h *handlers) streamReplay(w http.ResponseWriter, r *http.Request) {
	defer h.record("STREAM.REPLAY", time.Now())
	hash := r.URL.Query().Get("prompt_hash")
	if hash == "" {
		writeErr(w, 400, "?prompt_hash= required")
		return
	}
	toks, ok := h.eng.StreamCache.Replay(hash)
	if !ok {
		writeJSON(w, 200, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"hit": true, "tokens": toks})
}

func (h *handlers) streamForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("STREAM.FORGET", time.Now())
	hash := r.URL.Query().Get("prompt_hash")
	if hash == "" {
		writeErr(w, 400, "?prompt_hash= required")
		return
	}
	ok := h.eng.StreamCache.Forget(hash)
	if ok {
		h.eng.RecordWrite("STREAM.FORGET", []string{hash})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

func (h *handlers) streamPurge(w http.ResponseWriter, _ *http.Request) {
	defer h.record("STREAM.PURGE", time.Now())
	n := h.eng.StreamCache.Purge()
	h.eng.RecordWrite("STREAM.PURGE", nil)
	writeJSON(w, 200, map[string]int{"dropped": n})
}

func (h *handlers) streamStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.StreamCache.Stats())
}

// ── COST.* ──────────────────────────────────────────────────────────

type costBudgetReq struct {
	MaxUSD   float64 `json:"max_usd"`
	WindowMs int64   `json:"window_ms"`
}

func (h *handlers) costBudget(w http.ResponseWriter, r *http.Request) {
	defer h.record("COST.BUDGET", time.Now())
	tenant := r.PathValue("tenant")
	var req costBudgetReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if err := h.eng.CostBudgets.SetBudget(tenant, req.MaxUSD, req.WindowMs); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("COST.BUDGET", []string{
		tenant,
		strconv.FormatFloat(req.MaxUSD, 'f', -1, 64),
		strconv.FormatInt(req.WindowMs, 10),
	})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

type costChargeReq struct {
	USD float64 `json:"usd"`
}

func (h *handlers) costCharge(w http.ResponseWriter, r *http.Request) {
	defer h.record("COST.CHARGE", time.Now())
	tenant := r.PathValue("tenant")
	var req costChargeReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	allowed, remaining, err := h.eng.CostBudgets.Charge(tenant, req.USD)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("COST.CHARGE", []string{tenant, strconv.FormatFloat(req.USD, 'f', -1, 64)})
	writeJSON(w, 200, map[string]any{"allowed": allowed, "remaining": remaining})
}

func (h *handlers) costUsage(w http.ResponseWriter, r *http.Request) {
	defer h.record("COST.USAGE", time.Now())
	tenant := r.PathValue("tenant")
	used, remaining, max, window := h.eng.CostBudgets.Usage(tenant)
	writeJSON(w, 200, map[string]any{
		"used":      used,
		"remaining": remaining,
		"max":       max,
		"window_ms": window,
	})
}

func (h *handlers) costReset(w http.ResponseWriter, r *http.Request) {
	defer h.record("COST.RESET", time.Now())
	tenant := r.PathValue("tenant")
	ok := h.eng.CostBudgets.Reset(tenant)
	if ok {
		h.eng.RecordWrite("COST.RESET", []string{tenant})
	}
	writeJSON(w, 200, map[string]bool{"reset": ok})
}

func (h *handlers) costList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"tenants": h.eng.CostBudgets.List()})
}

// ── SHADOW.* ────────────────────────────────────────────────────────

type shadowPutReq struct {
	Value        string `json:"value"`
	StaleAfterMs int    `json:"stale_after_ms,omitempty"`
}

func (h *handlers) shadowPut(w http.ResponseWriter, r *http.Request) {
	defer h.record("SHADOW.PUT", time.Now())
	key := r.PathValue("key")
	var req shadowPutReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	stale := 5 * time.Minute
	if req.StaleAfterMs > 0 {
		stale = time.Duration(req.StaleAfterMs) * time.Millisecond
	}
	h.eng.Shadow.Put(key, req.Value, stale)
	args := []string{key, req.Value}
	if req.StaleAfterMs > 0 {
		args = append(args, "STALE-AFTER", strconv.Itoa(req.StaleAfterMs))
	}
	h.eng.RecordWrite("SHADOW.PUT", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) shadowGet(w http.ResponseWriter, r *http.Request) {
	defer h.record("SHADOW.GET", time.Now())
	key := r.PathValue("key")
	v, fresh, had := h.eng.Shadow.Get(key)
	if !had {
		writeJSON(w, 200, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"hit": true, "value": v, "is_fresh": fresh})
}

func (h *handlers) shadowForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("SHADOW.FORGET", time.Now())
	key := r.PathValue("key")
	ok := h.eng.Shadow.Forget(key)
	if ok {
		h.eng.RecordWrite("SHADOW.FORGET", []string{key})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

func (h *handlers) shadowStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Shadow.Stats())
}

// ── PERSONA.* ───────────────────────────────────────────────────────

type personaSetReq struct {
	Persona string `json:"persona"`
}

func (h *handlers) personaSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("PERSONA.SET", time.Now())
	user := r.PathValue("user")
	var req personaSetReq
	if err := readJSON(r, &req); err != nil || req.Persona == "" {
		writeErr(w, 400, "persona required")
		return
	}
	h.eng.Personas.SetActive(user, req.Persona)
	h.eng.RecordWrite("PERSONA.SET", []string{user, req.Persona})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) personaGet(w http.ResponseWriter, r *http.Request) {
	defer h.record("PERSONA.GET", time.Now())
	user := r.PathValue("user")
	writeJSON(w, 200, map[string]string{"persona": h.eng.Personas.Active(user)})
}

func (h *handlers) personaList(w http.ResponseWriter, r *http.Request) {
	defer h.record("PERSONA.LIST", time.Now())
	user := r.PathValue("user")
	writeJSON(w, 200, map[string]any{"personas": h.eng.Personas.List(user)})
}

func (h *handlers) personaForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("PERSONA.FORGET", time.Now())
	user := r.PathValue("user")
	ok := h.eng.Personas.Forget(user)
	if ok {
		h.eng.RecordWrite("PERSONA.FORGET", []string{user})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

// ── SAFE.* ──────────────────────────────────────────────────────────

type safeSetReq struct {
	Text       string   `json:"text"`
	Safe       bool     `json:"safe"`
	Score      float64  `json:"score"`
	Categories []string `json:"categories,omitempty"`
	TTLSec     int      `json:"ttl_sec,omitempty"`
}

func (h *handlers) safeSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("SAFE.SET", time.Now())
	var req safeSetReq
	if err := readJSON(r, &req); err != nil || req.Text == "" {
		writeErr(w, 400, "text required")
		return
	}
	ttl := time.Duration(0)
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	h.eng.Moderation.Set(req.Text, aiops.ModerationResult{
		Safe:       req.Safe,
		Score:      req.Score,
		Categories: req.Categories,
	}, ttl)
	safeFlag := "0"
	if req.Safe {
		safeFlag = "1"
	}
	args := []string{req.Text, safeFlag, strconv.FormatFloat(req.Score, 'f', -1, 64)}
	if len(req.Categories) > 0 {
		args = append(args, "CATEGORIES")
		args = append(args, req.Categories...)
	}
	if req.TTLSec > 0 {
		args = append(args, "EX", strconv.Itoa(req.TTLSec))
	}
	h.eng.RecordWrite("SAFE.SET", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) safeCheck(w http.ResponseWriter, r *http.Request) {
	defer h.record("SAFE.CHECK", time.Now())
	text := r.URL.Query().Get("text")
	if text == "" {
		writeErr(w, 400, "?text= required")
		return
	}
	res, ok := h.eng.Moderation.Check(text)
	if !ok {
		writeJSON(w, 200, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"hit": true, "result": res})
}

func (h *handlers) safeInject(w http.ResponseWriter, r *http.Request) {
	defer h.record("SAFE.INJECT", time.Now())
	text := r.URL.Query().Get("text")
	if text == "" {
		writeErr(w, 400, "?text= required")
		return
	}
	score := aiops.InjectionScore(text)
	matched := aiops.MatchedPatterns(text)
	writeJSON(w, 200, map[string]any{"score": score, "matched": matched})
}

func (h *handlers) safeForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("SAFE.FORGET", time.Now())
	text := r.URL.Query().Get("text")
	if text == "" {
		writeErr(w, 400, "?text= required")
		return
	}
	ok := h.eng.Moderation.Forget(text)
	if ok {
		h.eng.RecordWrite("SAFE.FORGET", []string{text})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

func (h *handlers) safePurge(w http.ResponseWriter, _ *http.Request) {
	defer h.record("SAFE.PURGE", time.Now())
	n := h.eng.Moderation.Purge()
	h.eng.RecordWrite("SAFE.PURGE", nil)
	writeJSON(w, 200, map[string]int{"dropped": n})
}

func (h *handlers) safeStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Moderation.Stats())
}

// ── LINEAGE.* ───────────────────────────────────────────────────────

type lineageRecordReq struct {
	OutputID   string  `json:"output_id"`
	SourceID   string  `json:"source_id"`
	Snippet    string  `json:"snippet,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

func (h *handlers) lineageRecord(w http.ResponseWriter, r *http.Request) {
	defer h.record("LINEAGE.RECORD", time.Now())
	var req lineageRecordReq
	if err := readJSON(r, &req); err != nil || req.OutputID == "" || req.SourceID == "" {
		writeErr(w, 400, "output_id + source_id required")
		return
	}
	h.eng.Lineage.Record(req.OutputID, req.SourceID, req.Snippet, req.Confidence)
	args := []string{req.OutputID, req.SourceID}
	if req.Snippet != "" {
		args = append(args, "SNIPPET", req.Snippet)
	}
	if req.Confidence != 0 {
		args = append(args, "CONFIDENCE", strconv.FormatFloat(req.Confidence, 'f', -1, 64))
	}
	h.eng.RecordWrite("LINEAGE.RECORD", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) lineageList(w http.ResponseWriter, r *http.Request) {
	defer h.record("LINEAGE.LIST", time.Now())
	id := r.PathValue("output_id")
	writeJSON(w, 200, map[string]any{"citations": h.eng.Lineage.List(id)})
}

func (h *handlers) lineageSources(w http.ResponseWriter, r *http.Request) {
	defer h.record("LINEAGE.SOURCES", time.Now())
	id := r.PathValue("output_id")
	writeJSON(w, 200, map[string]any{"sources": h.eng.Lineage.Sources(id)})
}

func (h *handlers) lineageConsumers(w http.ResponseWriter, r *http.Request) {
	defer h.record("LINEAGE.CONSUMERS", time.Now())
	id := r.PathValue("source_id")
	writeJSON(w, 200, map[string]any{"consumers": h.eng.Lineage.Consumers(id)})
}

func (h *handlers) lineageForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("LINEAGE.FORGET", time.Now())
	id := r.PathValue("output_id")
	n := h.eng.Lineage.Forget(id)
	if n > 0 {
		h.eng.RecordWrite("LINEAGE.FORGET", []string{id})
	}
	writeJSON(w, 200, map[string]int{"removed": n})
}

func (h *handlers) lineageStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Lineage.Stats())
}

// ── SLO.* ───────────────────────────────────────────────────────────

type sloSetReq struct {
	Percentile string  `json:"percentile"`
	MaxMs      float64 `json:"max_ms"`
}

func (h *handlers) sloSet(w http.ResponseWriter, r *http.Request) {
	defer h.record("SLO.SET", time.Now())
	cmd := r.PathValue("cmd")
	var req sloSetReq
	if err := readJSON(r, &req); err != nil || req.Percentile == "" {
		writeErr(w, 400, "percentile + max_ms required")
		return
	}
	h.eng.SLOTracker.SetTarget(cmd, req.Percentile, req.MaxMs)
	h.eng.RecordWrite("SLO.SET", []string{cmd, req.Percentile, strconv.FormatFloat(req.MaxMs, 'f', -1, 64)})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) sloSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"slos": h.eng.SLOTracker.Snapshot()})
}

type sloResetReq struct {
	Cmd string `json:"cmd,omitempty"`
}

func (h *handlers) sloReset(w http.ResponseWriter, r *http.Request) {
	defer h.record("SLO.RESET", time.Now())
	var req sloResetReq
	_ = readJSON(r, &req)
	n := h.eng.SLOTracker.Reset(req.Cmd)
	args := []string{}
	if req.Cmd != "" {
		args = append(args, req.Cmd)
	}
	h.eng.RecordWrite("SLO.RESET", args)
	writeJSON(w, 200, map[string]int{"reset": n})
}

// ── AB.* ────────────────────────────────────────────────────────────

type abDefineReq struct {
	Name     string    `json:"name"`
	Variants []string  `json:"variants"`
	Weights  []float64 `json:"weights,omitempty"`
}

func (h *handlers) abDefine(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.DEFINE", time.Now())
	var req abDefineReq
	if err := readJSON(r, &req); err != nil || req.Name == "" || len(req.Variants) == 0 {
		writeErr(w, 400, "name + variants required")
		return
	}
	if len(req.Weights) > 0 && len(req.Weights) != len(req.Variants) {
		writeErr(w, 400, "weight count must match variant count")
		return
	}
	if len(req.Weights) > 0 {
		h.eng.Experiments.DefineWeighted(req.Name, req.Variants, req.Weights)
	} else {
		h.eng.Experiments.Define(req.Name, req.Variants)
	}
	args := []string{req.Name}
	if len(req.Weights) > 0 {
		args = append(args, "WEIGHTS")
		for _, wt := range req.Weights {
			args = append(args, strconv.FormatFloat(wt, 'f', -1, 64))
		}
	}
	args = append(args, req.Variants...)
	h.eng.RecordWrite("AB.DEFINE", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) abAssign(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.ASSIGN", time.Now())
	name := r.PathValue("name")
	user := r.URL.Query().Get("user")
	if user == "" {
		writeErr(w, 400, "?user= required")
		return
	}
	v, ok := h.eng.Experiments.Assign(name, user)
	if !ok {
		writeJSON(w, 404, map[string]any{"hit": false})
		return
	}
	writeJSON(w, 200, map[string]any{"variant": v})
}

type abExposeReq struct {
	Variant string `json:"variant"`
}

func (h *handlers) abExpose(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.EXPOSE", time.Now())
	name := r.PathValue("name")
	var req abExposeReq
	if err := readJSON(r, &req); err != nil || req.Variant == "" {
		writeErr(w, 400, "variant required")
		return
	}
	h.eng.Experiments.Expose(name, req.Variant)
	h.eng.RecordWrite("AB.EXPOSE", []string{name, req.Variant})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

type abRecordReq struct {
	Variant string  `json:"variant"`
	Value   float64 `json:"value"`
}

func (h *handlers) abRecord(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.RECORD", time.Now())
	name := r.PathValue("name")
	var req abRecordReq
	if err := readJSON(r, &req); err != nil || req.Variant == "" {
		writeErr(w, 400, "variant required")
		return
	}
	h.eng.Experiments.Record(name, req.Variant, req.Value)
	h.eng.RecordWrite("AB.RECORD", []string{name, req.Variant, strconv.FormatFloat(req.Value, 'f', -1, 64)})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) abStats(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.STATS", time.Now())
	name := r.PathValue("name")
	st, ok := h.eng.Experiments.Stats(name)
	if !ok {
		writeErr(w, 404, "no such experiment")
		return
	}
	writeJSON(w, 200, st)
}

func (h *handlers) abList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"experiments": h.eng.Experiments.List()})
}

func (h *handlers) abReset(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.RESET", time.Now())
	name := r.PathValue("name")
	ok := h.eng.Experiments.Reset(name)
	if ok {
		h.eng.RecordWrite("AB.RESET", []string{name})
	}
	writeJSON(w, 200, map[string]bool{"reset": ok})
}

func (h *handlers) abDelete(w http.ResponseWriter, r *http.Request) {
	defer h.record("AB.DELETE", time.Now())
	name := r.PathValue("name")
	ok := h.eng.Experiments.Delete(name)
	if ok {
		h.eng.RecordWrite("AB.DELETE", []string{name})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

// ── GRAPH.* ─────────────────────────────────────────────────────────

type graphEdgeReq struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

func (h *handlers) graphLink(w http.ResponseWriter, r *http.Request) {
	defer h.record("GRAPH.LINK", time.Now())
	var req graphEdgeReq
	if err := readJSON(r, &req); err != nil || req.Subject == "" || req.Predicate == "" || req.Object == "" {
		writeErr(w, 400, "subject + predicate + object required")
		return
	}
	created := h.eng.Graph.Link(req.Subject, req.Predicate, req.Object)
	h.eng.RecordWrite("GRAPH.LINK", []string{req.Subject, req.Predicate, req.Object})
	writeJSON(w, 200, map[string]bool{"created": created})
}

func (h *handlers) graphUnlink(w http.ResponseWriter, r *http.Request) {
	defer h.record("GRAPH.UNLINK", time.Now())
	var req graphEdgeReq
	if err := readJSON(r, &req); err != nil || req.Subject == "" || req.Predicate == "" || req.Object == "" {
		writeErr(w, 400, "subject + predicate + object required")
		return
	}
	ok := h.eng.Graph.Unlink(req.Subject, req.Predicate, req.Object)
	if ok {
		h.eng.RecordWrite("GRAPH.UNLINK", []string{req.Subject, req.Predicate, req.Object})
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

func (h *handlers) graphNeighbors(w http.ResponseWriter, r *http.Request) {
	defer h.record("GRAPH.NEIGHBORS", time.Now())
	subj := r.URL.Query().Get("subject")
	if subj == "" {
		writeErr(w, 400, "?subject= required")
		return
	}
	pred := r.URL.Query().Get("predicate")
	writeJSON(w, 200, map[string]any{"neighbors": h.eng.Graph.Neighbors(subj, pred)})
}

func (h *handlers) graphIn(w http.ResponseWriter, r *http.Request) {
	defer h.record("GRAPH.IN", time.Now())
	obj := r.URL.Query().Get("object")
	if obj == "" {
		writeErr(w, 400, "?object= required")
		return
	}
	pred := r.URL.Query().Get("predicate")
	writeJSON(w, 200, map[string]any{"subjects": h.eng.Graph.In(obj, pred)})
}

func (h *handlers) graphPath(w http.ResponseWriter, r *http.Request) {
	defer h.record("GRAPH.PATH", time.Now())
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		writeErr(w, 400, "?from= and ?to= required")
		return
	}
	max := 0
	if v := r.URL.Query().Get("max_depth"); v != "" {
		max, _ = strconv.Atoi(v)
	}
	pred := r.URL.Query().Get("predicate")
	path, ok := h.eng.Graph.Path(from, to, max, pred)
	if !ok {
		writeJSON(w, 200, map[string]any{"found": false})
		return
	}
	writeJSON(w, 200, map[string]any{"found": true, "path": path})
}

func (h *handlers) graphSubjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"subjects": h.eng.Graph.Subjects()})
}

func (h *handlers) graphStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Graph.Stats())
}

// ── SCHEDULE.* ──────────────────────────────────────────────────────

type scheduleAtReq struct {
	UnixMs int64    `json:"unix_ms"`
	Cmd    string   `json:"cmd"`
	Args   []string `json:"args,omitempty"`
}

func (h *handlers) scheduleAt(w http.ResponseWriter, r *http.Request) {
	defer h.record("SCHEDULE.AT", time.Now())
	var req scheduleAtReq
	if err := readJSON(r, &req); err != nil || req.Cmd == "" || req.UnixMs == 0 {
		writeErr(w, 400, "unix_ms + cmd required")
		return
	}
	id := h.eng.Scheduler.At(time.UnixMilli(req.UnixMs), req.Cmd, req.Args)
	args := []string{strconv.FormatInt(req.UnixMs, 10), req.Cmd}
	args = append(args, req.Args...)
	h.eng.RecordWrite("SCHEDULE.AT", args)
	writeJSON(w, 200, map[string]int64{"id": id})
}

type scheduleInReq struct {
	DelayMs int64    `json:"delay_ms"`
	Cmd     string   `json:"cmd"`
	Args    []string `json:"args,omitempty"`
}

func (h *handlers) scheduleIn(w http.ResponseWriter, r *http.Request) {
	defer h.record("SCHEDULE.IN", time.Now())
	var req scheduleInReq
	if err := readJSON(r, &req); err != nil || req.Cmd == "" {
		writeErr(w, 400, "delay_ms + cmd required")
		return
	}
	id := h.eng.Scheduler.In(time.Duration(req.DelayMs)*time.Millisecond, req.Cmd, req.Args)
	args := []string{strconv.FormatInt(req.DelayMs, 10), req.Cmd}
	args = append(args, req.Args...)
	h.eng.RecordWrite("SCHEDULE.IN", args)
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (h *handlers) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	defer h.record("SCHEDULE.CANCEL", time.Now())
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, 400, "id must be an integer")
		return
	}
	ok := h.eng.Scheduler.Cancel(id)
	if ok {
		h.eng.RecordWrite("SCHEDULE.CANCEL", []string{idStr})
	}
	writeJSON(w, 200, map[string]bool{"cancelled": ok})
}

func (h *handlers) scheduleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"tasks": h.eng.Scheduler.List()})
}

func (h *handlers) scheduleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Scheduler.Stats())
}

// ── EVENT.* ─────────────────────────────────────────────────────────

func (h *handlers) eventAppend(w http.ResponseWriter, r *http.Request) {
	defer h.record("EVENT.APPEND", time.Now())
	stream := r.PathValue("stream")
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return
	}
	if len(body) == 0 {
		writeErr(w, 400, "json payload required")
		return
	}
	seq, err := h.eng.EventLog.Append(stream, body)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("EVENT.APPEND", []string{stream, string(body)})
	writeJSON(w, 200, map[string]int64{"seq": seq})
}

type eventProjectReq struct {
	Name    string `json:"name"`
	Reducer string `json:"reducer"`
	Field   string `json:"field,omitempty"`
	GroupBy string `json:"group_by,omitempty"`
}

func (h *handlers) eventProject(w http.ResponseWriter, r *http.Request) {
	defer h.record("EVENT.PROJECT", time.Now())
	stream := r.PathValue("stream")
	var req eventProjectReq
	if err := readJSON(r, &req); err != nil || req.Name == "" || req.Reducer == "" {
		writeErr(w, 400, "name + reducer required")
		return
	}
	h.eng.EventLog.Project(stream, req.Name, req.Reducer, req.Field, req.GroupBy)
	args := []string{stream, req.Name, req.Reducer, req.Field}
	if req.GroupBy != "" {
		args = append(args, "GROUPBY", req.GroupBy)
	}
	h.eng.RecordWrite("EVENT.PROJECT", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) eventRead(w http.ResponseWriter, r *http.Request) {
	defer h.record("EVENT.READ", time.Now())
	stream := r.PathValue("stream")
	name := r.PathValue("name")
	v, ok := h.eng.EventLog.Read(stream, name)
	if !ok {
		writeErr(w, 404, "no such projection")
		return
	}
	writeJSON(w, 200, map[string]any{"projection": v})
}

func (h *handlers) eventRange(w http.ResponseWriter, r *http.Request) {
	defer h.record("EVENT.RANGE", time.Now())
	stream := r.PathValue("stream")
	var start, end int64
	if v := r.URL.Query().Get("start"); v != "" {
		start, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := r.URL.Query().Get("end"); v != "" {
		end, _ = strconv.ParseInt(v, 10, 64)
	}
	writeJSON(w, 200, map[string]any{"events": h.eng.EventLog.Range(stream, start, end)})
}

func (h *handlers) eventLen(w http.ResponseWriter, r *http.Request) {
	defer h.record("EVENT.LEN", time.Now())
	stream := r.PathValue("stream")
	writeJSON(w, 200, map[string]int{"len": h.eng.EventLog.Len(stream)})
}

// ── POLICY.* ────────────────────────────────────────────────────────

type policyAllowReq struct {
	User     string            `json:"user"`
	Resource string            `json:"resource"`
	Action   string            `json:"action"`
	Ctx      map[string]string `json:"ctx,omitempty"`
	TTLSec   int               `json:"ttl_sec,omitempty"`
}

func (h *handlers) policyAllow(w http.ResponseWriter, r *http.Request) {
	defer h.record("POLICY.ALLOW", time.Now())
	var req policyAllowReq
	if err := readJSON(r, &req); err != nil || req.User == "" || req.Resource == "" || req.Action == "" {
		writeErr(w, 400, "user + resource + action required")
		return
	}
	ttl := time.Duration(0)
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	allow, reason := h.eng.Policies.Allow(req.User, req.Resource, req.Action, req.Ctx, ttl)
	writeJSON(w, 200, map[string]any{"allow": allow, "reason": reason})
}

type policySetReq struct {
	User     string            `json:"user"`
	Resource string            `json:"resource"`
	Action   string            `json:"action"`
	Ctx      map[string]string `json:"ctx,omitempty"`
	Allow    bool              `json:"allow"`
	Reason   string            `json:"reason"`
	TTLSec   int               `json:"ttl_sec,omitempty"`
}

func (h *handlers) policySet(w http.ResponseWriter, r *http.Request) {
	defer h.record("POLICY.SET", time.Now())
	var req policySetReq
	if err := readJSON(r, &req); err != nil || req.User == "" || req.Resource == "" || req.Action == "" {
		writeErr(w, 400, "user + resource + action required")
		return
	}
	ttl := time.Duration(0)
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	h.eng.Policies.Set(req.User, req.Resource, req.Action, req.Ctx, req.Allow, req.Reason, ttl)
	allowFlag := "0"
	if req.Allow {
		allowFlag = "1"
	}
	args := []string{req.User, req.Resource, req.Action, allowFlag, req.Reason}
	if req.TTLSec > 0 {
		args = append(args, "TTL", strconv.Itoa(req.TTLSec))
	}
	if len(req.Ctx) > 0 {
		args = append(args, "CTX")
		for k, v := range req.Ctx {
			args = append(args, k, v)
		}
	}
	h.eng.RecordWrite("POLICY.SET", args)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) policyPurge(w http.ResponseWriter, _ *http.Request) {
	defer h.record("POLICY.PURGE", time.Now())
	n := h.eng.Policies.Purge()
	h.eng.RecordWrite("POLICY.PURGE", nil)
	writeJSON(w, 200, map[string]int{"dropped": n})
}

func (h *handlers) policyStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Policies.Stats())
}

// ── INFER.* ─────────────────────────────────────────────────────────

type inferGenerateReq struct {
	Prompt      string  `json:"prompt"`
	Model       string  `json:"model,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Tenant      string  `json:"tenant,omitempty"`
	TTLSec      int     `json:"ttl_sec,omitempty"`
}

func (h *handlers) inferGenerate(w http.ResponseWriter, r *http.Request) {
	defer h.record("INFER.GENERATE", time.Now())
	var req inferGenerateReq
	if err := readJSON(r, &req); err != nil || req.Prompt == "" {
		writeErr(w, 400, "prompt required")
		return
	}
	opts := aiops.InferOpts{
		Model:       req.Model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tenant:      req.Tenant,
	}
	ttl := time.Duration(0)
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	resp, hit, cost, err := h.eng.Inference.Generate(req.Prompt, opts, ttl)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if !hit && cost > 0 && opts.Tenant != "" && h.eng.CostBudgets != nil {
		_, _, _ = h.eng.CostBudgets.Charge(opts.Tenant, cost)
	}
	args := []string{req.Prompt}
	if req.Model != "" {
		args = append(args, "MODEL", req.Model)
	}
	if req.Temperature != 0 {
		args = append(args, "TEMP", strconv.FormatFloat(req.Temperature, 'f', -1, 64))
	}
	if req.MaxTokens != 0 {
		args = append(args, "MAXTOK", strconv.Itoa(req.MaxTokens))
	}
	if req.Tenant != "" {
		args = append(args, "TENANT", req.Tenant)
	}
	if req.TTLSec > 0 {
		args = append(args, "TTL", strconv.Itoa(req.TTLSec))
	}
	h.eng.RecordWrite("INFER.GENERATE", args)
	writeJSON(w, 200, map[string]any{"response": resp, "hit": hit, "cost": cost})
}

type inferForgetReq struct {
	Prompt      string  `json:"prompt"`
	Model       string  `json:"model,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

func (h *handlers) inferForget(w http.ResponseWriter, r *http.Request) {
	defer h.record("INFER.FORGET", time.Now())
	var req inferForgetReq
	if err := readJSON(r, &req); err != nil || req.Prompt == "" {
		writeErr(w, 400, "prompt required")
		return
	}
	ok := h.eng.Inference.Forget(req.Prompt, aiops.InferOpts{
		Model:       req.Model,
		Temperature: req.Temperature,
	})
	args := []string{req.Prompt}
	if req.Model != "" {
		args = append(args, "MODEL", req.Model)
	}
	if req.Temperature != 0 {
		args = append(args, "TEMP", strconv.FormatFloat(req.Temperature, 'f', -1, 64))
	}
	if ok {
		h.eng.RecordWrite("INFER.FORGET", args)
	}
	writeJSON(w, 200, map[string]bool{"removed": ok})
}

func (h *handlers) inferPurge(w http.ResponseWriter, _ *http.Request) {
	defer h.record("INFER.PURGE", time.Now())
	n := h.eng.Inference.Purge()
	h.eng.RecordWrite("INFER.PURGE", nil)
	writeJSON(w, 200, map[string]int{"dropped": n})
}

func (h *handlers) inferStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Inference.Stats())
}

type inferDefaultReq struct {
	Provider string `json:"provider"`
}

func (h *handlers) inferDefault(w http.ResponseWriter, r *http.Request) {
	defer h.record("INFER.DEFAULT", time.Now())
	var req inferDefaultReq
	if err := readJSON(r, &req); err != nil || req.Provider == "" {
		writeErr(w, 400, "provider required")
		return
	}
	if err := h.eng.Inference.SetDefault(req.Provider); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.RecordWrite("INFER.DEFAULT", []string{req.Provider})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ── MCP.* ───────────────────────────────────────────────────────────

func (h *handlers) mcpTools(w http.ResponseWriter, _ *http.Request) {
	defer h.record("MCP.TOOLS", time.Now())
	tools := h.eng.MCP.Tools()
	if tools == nil {
		tools = []*aiops.MCPTool{}
	}
	writeJSON(w, 200, map[string]any{"tools": tools})
}

func (h *handlers) mcpResources(w http.ResponseWriter, _ *http.Request) {
	defer h.record("MCP.RESOURCES", time.Now())
	res := h.eng.MCP.Resources()
	if res == nil {
		res = []*aiops.MCPResource{}
	}
	writeJSON(w, 200, map[string]any{"resources": res})
}

type mcpCallReq struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func (h *handlers) mcpCall(w http.ResponseWriter, r *http.Request) {
	defer h.record("MCP.CALL", time.Now())
	var req mcpCallReq
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		writeErr(w, 400, "name required")
		return
	}
	argsRaw, _ := json.Marshal(req.Arguments)
	frame := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      req.Name,
			"arguments": json.RawMessage(argsRaw),
		},
	}
	raw, _ := json.Marshal(frame)
	out := h.eng.MCP.HandleBytes(raw)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(out)
}

func (h *handlers) mcpRead(w http.ResponseWriter, r *http.Request) {
	defer h.record("MCP.READ", time.Now())
	uri := r.URL.Query().Get("uri")
	if uri == "" {
		writeErr(w, 400, "?uri= required")
		return
	}
	frame := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "resources/read",
		"params":  map[string]interface{}{"uri": uri},
	}
	raw, _ := json.Marshal(frame)
	out := h.eng.MCP.HandleBytes(raw)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(out)
}

func (h *handlers) mcpRPC(w http.ResponseWriter, r *http.Request) {
	defer h.record("MCP.RPC", time.Now())
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return
	}
	out := h.eng.MCP.HandleBytes(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(out)
}
