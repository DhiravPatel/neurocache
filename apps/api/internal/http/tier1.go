package http

import (
	"net/http"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// ─── GROUND.* (semantic groundedness) ────────────────────────────────────

type groundReq struct {
	Answer     string   `json:"answer"`
	Context    []string `json:"context"`
	MinSupport float64  `json:"min_support"`
	Session    string   `json:"session"`
}

func (h *handlers) groundVerify(w http.ResponseWriter, r *http.Request) {
	defer h.record("GROUND.VERIFY", time.Now())
	var req groundReq
	if err := readJSON(r, &req); err != nil || req.Answer == "" {
		writeErr(w, 400, "answer required")
		return
	}
	minSup := req.MinSupport
	if minSup <= 0 {
		minSup = h.eng.Cfg.GroundMinSupport
	}
	writeJSON(w, 200, h.eng.GroundVerify.Verify(req.Answer, req.Context, minSup))
}

func (h *handlers) groundRequire(w http.ResponseWriter, r *http.Request) {
	defer h.record("GROUND.REQUIRE", time.Now())
	var req groundReq
	if err := readJSON(r, &req); err != nil || req.Answer == "" {
		writeErr(w, 400, "answer required")
		return
	}
	minSup := req.MinSupport
	if minSup <= 0 {
		minSup = h.eng.Cfg.GroundMinSupport
	}
	res := h.eng.GroundVerify.Require(req.Answer, req.Context, minSup)
	body := map[string]any{"result": res}
	if req.Session != "" {
		if dr, err := h.eng.RiskBudgets.Debit(req.Session, res.DocScore, "ground.require"); err == nil {
			body["risk"] = dr
			body["risk_session"] = req.Session
		}
	}
	// Not recorded to the AOF: GROUND.* is runtime-only state (see the Tier 1
	// note in resp/writeset.go) — the replayer can't dispatch it anyway.
	writeJSON(w, 200, body)
}

func (h *handlers) groundVStats(w http.ResponseWriter, r *http.Request) {
	defer h.record("GROUND.VSTATS", time.Now())
	writeJSON(w, 200, h.eng.GroundVerify.Stats())
}

// ─── QUOTA.* (composite admission control) ───────────────────────────────

type quotaPolicyReq struct {
	Gates []string `json:"gates"`
	Mode  string   `json:"mode"`
}

func (h *handlers) quotaPolicy(w http.ResponseWriter, r *http.Request) {
	defer h.record("QUOTA.POLICY", time.Now())
	name := r.PathValue("name")
	var req quotaPolicyReq
	if err := readJSON(r, &req); err != nil || name == "" || len(req.Gates) == 0 {
		writeErr(w, 400, "name + gates required")
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = h.eng.Cfg.QuotaDefaultMode
	}
	pol, err := h.eng.Quota.Define(name, req.Gates, mode)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, pol)
}

func (h *handlers) quotaGet(w http.ResponseWriter, r *http.Request) {
	defer h.record("QUOTA.GET", time.Now())
	pol, ok := h.eng.Quota.Get(r.PathValue("name"))
	if !ok {
		writeErr(w, 404, "no such policy")
		return
	}
	writeJSON(w, 200, pol)
}

func (h *handlers) quotaList(w http.ResponseWriter, r *http.Request) {
	defer h.record("QUOTA.LIST", time.Now())
	writeJSON(w, 200, map[string]any{"policies": h.eng.Quota.List()})
}

func (h *handlers) quotaStats(w http.ResponseWriter, r *http.Request) {
	defer h.record("QUOTA.STATS", time.Now())
	writeJSON(w, 200, h.eng.Quota.Stats())
}

func (h *handlers) quotaDelete(w http.ResponseWriter, r *http.Request) {
	defer h.record("QUOTA.DELETE", time.Now())
	name := r.PathValue("name")
	ok := h.eng.Quota.Delete(name)
	writeJSON(w, 200, map[string]bool{"deleted": ok})
}

// quotaAdmitReq carries optional per-gate parameter blocks. A nil block
// means that gate is not supplied (the engine rejects a policy that
// requires it).
type quotaAdmitReq struct {
	Cost *struct {
		Scope string  `json:"scope"`
		USD   float64 `json:"usd"`
	} `json:"cost"`
	Carbon *struct {
		Tenant  string `json:"tenant"`
		Tokens  int64  `json:"tokens"`
		Model   string `json:"model"`
		Feature string `json:"feature"`
		Region  string `json:"region"`
	} `json:"carbon"`
	Risk *struct {
		Session string  `json:"session"`
		Score   float64 `json:"score"`
	} `json:"risk"`
	Rate *struct {
		Key      string `json:"key"`
		WindowMs int64  `json:"window_ms"`
		Max      int64  `json:"max"`
		Cost     int64  `json:"cost"`
	} `json:"rate"`
	Market *struct {
		Market   string  `json:"market"`
		MaxPrice float64 `json:"max_price"`
	} `json:"market"`
}

func (req quotaAdmitReq) toDims() aiops.QuotaDims {
	var d aiops.QuotaDims
	if req.Cost != nil {
		d.HasCost, d.CostScope, d.CostUSD = true, req.Cost.Scope, req.Cost.USD
	}
	if req.Carbon != nil {
		d.HasCarbon = true
		d.CarbonTenant, d.CarbonTokens, d.CarbonModel = req.Carbon.Tenant, req.Carbon.Tokens, req.Carbon.Model
		d.CarbonFeature, d.CarbonRegion = req.Carbon.Feature, req.Carbon.Region
		if d.CarbonFeature == "" {
			d.CarbonFeature = "quota"
		}
		if d.CarbonRegion == "" {
			d.CarbonRegion = "default"
		}
	}
	if req.Risk != nil {
		d.HasRisk, d.RiskSession, d.RiskScore = true, req.Risk.Session, req.Risk.Score
	}
	if req.Rate != nil {
		d.HasRate = true
		d.RateKey, d.RateWindowMs, d.RateMax, d.RateCost = req.Rate.Key, req.Rate.WindowMs, req.Rate.Max, req.Rate.Cost
		if d.RateCost <= 0 {
			d.RateCost = 1
		}
	}
	if req.Market != nil {
		d.HasMarket, d.MarketID, d.MarketMaxPrice = true, req.Market.Market, req.Market.MaxPrice
	}
	return d
}

func (h *handlers) quotaAdmit(w http.ResponseWriter, r *http.Request) { h.quotaEval(w, r, true) }

func (h *handlers) quotaSimulate(w http.ResponseWriter, r *http.Request) { h.quotaEval(w, r, false) }

func (h *handlers) quotaEval(w http.ResponseWriter, r *http.Request, commit bool) {
	cmd := "QUOTA.SIMULATE"
	if commit {
		cmd = "QUOTA.ADMIT"
	}
	defer h.record(cmd, time.Now())
	name := r.PathValue("name")
	pol, ok := h.eng.Quota.Get(name)
	if !ok {
		writeErr(w, 404, "no such policy")
		return
	}
	var req quotaAdmitReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	dims := req.toDims()
	dec, err := h.eng.QuotaEvaluate(pol, dims, commit)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.eng.Quota.RecordDecision(dec, !commit)
	writeJSON(w, 200, dec)
}

// ─── REMBED.* (embedding-recompute migration) ────────────────────────────

type rembedStartReq struct {
	Scope    string `json:"scope"`
	ToDim    int    `json:"to_dim"`
	Batch    int    `json:"batch"`
	DualRead bool   `json:"dual_read"`
}

func (h *handlers) rembedPlan(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.PLAN", time.Now())
	var req rembedStartReq
	if err := readJSON(r, &req); err != nil || req.Scope == "" {
		writeErr(w, 400, "scope required")
		return
	}
	plan, err := h.eng.Rembed.Plan(req.Scope, req.ToDim)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, plan)
}

func (h *handlers) rembedStart(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.START", time.Now())
	var req rembedStartReq
	if err := readJSON(r, &req); err != nil || req.Scope == "" {
		writeErr(w, 400, "scope required")
		return
	}
	toDim := req.ToDim
	if toDim <= 0 {
		toDim = h.eng.Cfg.EmbeddingDim
	}
	batch := req.Batch
	if batch <= 0 {
		batch = h.eng.Cfg.RembedBatch
	}
	id, err := h.eng.Rembed.Start(req.Scope, toDim, batch, req.DualRead)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"id": id})
}

func (h *handlers) rembedProgress(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.PROGRESS", time.Now())
	p, ok := h.eng.Rembed.Progress(r.PathValue("job"))
	if !ok {
		writeErr(w, 404, "no such job")
		return
	}
	writeJSON(w, 200, p)
}

func (h *handlers) rembedStatus(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.STATUS", time.Now())
	st, ok := h.eng.Rembed.Status(r.PathValue("job"))
	if !ok {
		writeErr(w, 404, "no such job")
		return
	}
	writeJSON(w, 200, st)
}

func (h *handlers) rembedSwap(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.SWAP", time.Now())
	if err := h.eng.Rembed.Swap(r.PathValue("job")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) rembedRollback(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.ROLLBACK", time.Now())
	if err := h.eng.Rembed.Rollback(r.PathValue("job")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *handlers) rembedList(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.LIST", time.Now())
	writeJSON(w, 200, map[string]any{"jobs": h.eng.Rembed.List()})
}

func (h *handlers) rembedStats(w http.ResponseWriter, r *http.Request) {
	defer h.record("REMBED.STATS", time.Now())
	writeJSON(w, 200, h.eng.Rembed.Stats())
}
