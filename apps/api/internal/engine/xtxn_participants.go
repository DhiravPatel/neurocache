package engine

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// This file wires QUOTA admission and GROUND-requirement into the XTXN 2PC
// coordinator as participants, so an orchestrator can express "admit the
// request, ground the answer, settle the cost, record provenance — all or
// nothing" as one cross-transaction.
//
// Each participant separates the optimistic-validation half (Prepare: peek
// the budgets / score the answer, fail to abort the whole txn) from the
// make-visible half (Commit: charge / debit). Abort discards the staged op.
// The staged op is keyed by an opaque token the coordinator hands back
// verbatim on Commit/Abort.
//
// Args arrive as the flat ARG k v map from XTXN.STAGE. Lists (GROUND context
// chunks) are encoded as one value with the "|||" separator.

const xtxnCtxSep = "|||"

// ─── QUOTA participant ───────────────────────────────────────────────────

type quotaStaged struct {
	pol  aiops.QuotaPolicy
	dims aiops.QuotaDims
}

type quotaParticipant struct {
	e   *Engine
	seq atomic.Uint64
	mu  sync.Mutex
	st  map[string]quotaStaged
}

func newQuotaParticipant(e *Engine) *quotaParticipant {
	return &quotaParticipant{e: e, st: map[string]quotaStaged{}}
}

// Prepare peeks the policy's gates (no consumption). A denied admission, an
// unknown policy, or missing gate params returns an error → the whole txn
// aborts.
//
//	op is ignored (treated as "admit"); args carry: policy, plus the per-gate
//	dims (cost_scope/cost_usd, carbon_tenant/carbon_tokens/carbon_model,
//	risk_session/risk_score, rate_key/rate_window_ms/rate_max,
//	market/market_maxprice).
func (p *quotaParticipant) Prepare(op string, args map[string]string) (string, error) {
	name := args["policy"]
	if name == "" {
		return "", errors.New("quota participant requires arg 'policy'")
	}
	pol, ok := p.e.Quota.Get(name)
	if !ok {
		return "", errors.New("no such quota policy: " + name)
	}
	dims, err := quotaDimsFromArgs(args)
	if err != nil {
		return "", err
	}
	dec, err := p.e.QuotaEvaluate(pol, dims, false) // peek only
	if err != nil {
		return "", err
	}
	if !dec.Admitted {
		return "", errors.New("quota would deny admission (denied_by: " + strings.Join(dec.DeniedBy, ",") + ")")
	}
	tok := "qta-" + strconv.FormatUint(p.seq.Add(1), 10)
	p.mu.Lock()
	p.st[tok] = quotaStaged{pol: pol, dims: dims}
	p.mu.Unlock()
	return tok, nil
}

func (p *quotaParticipant) Commit(token string) error {
	p.mu.Lock()
	s, ok := p.st[token]
	delete(p.st, token)
	p.mu.Unlock()
	if !ok {
		return errors.New("unknown quota token")
	}
	dec, err := p.e.QuotaEvaluate(s.pol, s.dims, true) // consume
	if err != nil {
		return err
	}
	p.e.Quota.RecordDecision(dec, false)
	if !dec.Admitted {
		// A budget changed between prepare and commit; surface for the
		// coordinator's commit_partial path.
		return errors.New("quota admission no longer holds at commit")
	}
	return nil
}

func (p *quotaParticipant) Abort(token string) {
	p.mu.Lock()
	delete(p.st, token)
	p.mu.Unlock()
}

// ─── GROUND / RISK participant ───────────────────────────────────────────

type riskStaged struct {
	session  string
	docScore float64
}

type riskParticipant struct {
	e   *Engine
	seq atomic.Uint64
	mu  sync.Mutex
	st  map[string]riskStaged
}

func newRiskParticipant(e *Engine) *riskParticipant {
	return &riskParticipant{e: e, st: map[string]riskStaged{}}
}

// Prepare scores the answer's groundedness. An ungrounded answer aborts the
// txn — you don't settle the cost of an answer you can't support.
//
//	args: answer, context (chunks joined by "|||"), session, min_support.
func (p *riskParticipant) Prepare(op string, args map[string]string) (string, error) {
	answer := args["answer"]
	if answer == "" {
		return "", errors.New("ground participant requires arg 'answer'")
	}
	var ctx []string
	if c := args["context"]; c != "" {
		ctx = strings.Split(c, xtxnCtxSep)
	}
	minSup := p.e.Cfg.GroundMinSupport
	if v := args["min_support"]; v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			minSup = f
		}
	}
	res := p.e.GroundVerify.Require(answer, ctx, minSup)
	if !res.Grounded {
		return "", errors.New("answer is not grounded (doc_score below min_support)")
	}
	tok := "rsk-" + strconv.FormatUint(p.seq.Add(1), 10)
	p.mu.Lock()
	p.st[tok] = riskStaged{session: args["session"], docScore: res.DocScore}
	p.mu.Unlock()
	return tok, nil
}

func (p *riskParticipant) Commit(token string) error {
	p.mu.Lock()
	s, ok := p.st[token]
	delete(p.st, token)
	p.mu.Unlock()
	if !ok {
		return errors.New("unknown ground token")
	}
	if s.session != "" {
		_, _ = p.e.RiskBudgets.Debit(s.session, s.docScore, "xtxn.ground")
	}
	return nil
}

func (p *riskParticipant) Abort(token string) {
	p.mu.Lock()
	delete(p.st, token)
	p.mu.Unlock()
}

// quotaDimsFromArgs builds QuotaDims from the flat XTXN ARG map. Only the
// gates whose params are present are marked Has*; the engine rejects a policy
// that needs a gate whose dims weren't supplied.
func quotaDimsFromArgs(args map[string]string) (aiops.QuotaDims, error) {
	var d aiops.QuotaDims
	if v, ok := args["cost_scope"]; ok {
		usd, err := strconv.ParseFloat(args["cost_usd"], 64)
		if err != nil {
			return d, errors.New("cost_usd must be a float")
		}
		d.HasCost, d.CostScope, d.CostUSD = true, v, usd
	}
	if v, ok := args["carbon_tenant"]; ok {
		tok, err := strconv.ParseInt(args["carbon_tokens"], 10, 64)
		if err != nil {
			return d, errors.New("carbon_tokens must be an integer")
		}
		d.HasCarbon, d.CarbonTenant, d.CarbonTokens = true, v, tok
		d.CarbonModel = args["carbon_model"]
		d.CarbonFeature, d.CarbonRegion = "xtxn", "default"
	}
	if v, ok := args["risk_session"]; ok {
		sc, err := strconv.ParseFloat(args["risk_score"], 64)
		if err != nil {
			return d, errors.New("risk_score must be a float")
		}
		d.HasRisk, d.RiskSession, d.RiskScore = true, v, sc
	}
	if v, ok := args["rate_key"]; ok {
		win, err := strconv.ParseInt(args["rate_window_ms"], 10, 64)
		if err != nil {
			return d, errors.New("rate_window_ms must be an integer")
		}
		mx, err := strconv.ParseInt(args["rate_max"], 10, 64)
		if err != nil {
			return d, errors.New("rate_max must be an integer")
		}
		d.HasRate, d.RateKey, d.RateWindowMs, d.RateMax, d.RateCost = true, v, win, mx, 1
	}
	if v, ok := args["market"]; ok {
		mp, err := strconv.ParseFloat(args["market_maxprice"], 64)
		if err != nil {
			return d, errors.New("market_maxprice must be a float")
		}
		d.HasMarket, d.MarketID, d.MarketMaxPrice = true, v, mp
	}
	return d, nil
}
