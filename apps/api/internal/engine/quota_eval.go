package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
)

// QuotaEvaluate runs a composite admission decision against the five gate
// subsystems. It lives in the engine because this is the only layer holding
// references to CostGuard / Carbon / RiskBudgets / RateLimit / Market — the
// aiops.QuotaManager stays a pure policy registry.
//
// Two-phase by design: every required gate is PEEKED first (no mutation), the
// overall verdict is computed, and only then — if committing AND admitted —
// are gates consumed. That ordering is what makes QUOTA.ADMIT safe: a request
// that fails the carbon budget never burns a rate-limit token, and
// QUOTA.SIMULATE (commit=false) is a pure dry run.
//
// Per-gate consume rules: in ALL mode every required gate is consumed (all
// passed, by definition of admitted); in ANY mode only the gates that
// individually had room are consumed (charging an already-over budget would
// just push it further over). MARKET is a contention SIGNAL — it is peeked
// (price vs ceiling) but never consumed, since admission there is an
// asynchronous auction, not a synchronous debit.
func (e *Engine) QuotaEvaluate(pol aiops.QuotaPolicy, dims aiops.QuotaDims, commit bool) (aiops.QuotaDecision, error) {
	// Serialize peek+commit so the two phases are atomic against other
	// admissions — see Engine.quotaMu. SIMULATE takes the lock too so its
	// snapshot is consistent with any in-flight commit.
	e.quotaMu.Lock()
	defer e.quotaMu.Unlock()

	dec := aiops.QuotaDecision{Policy: pol.Name, Mode: pol.Mode}

	// consume returns whether the gate actually committed — the underlying
	// charge re-validates (cost) or the GCRA re-decides (rate), so we report
	// the real outcome rather than asserting consumption blindly.
	type gateEval struct {
		res     aiops.QuotaGateResult
		consume func() bool
	}
	evals := make([]gateEval, 0, len(pol.Gates))

	for _, g := range pol.Gates {
		switch g {
		case aiops.GateCost:
			if !dims.HasCost {
				return dec, errors.New("policy requires COST gate params (COST scope usd)")
			}
			// Compose the SAME per-tenant budget operators configure via
			// COST.BUDGET (e.CostBudgets), so a single budget definition is
			// honoured both by direct COST.CHARGE and by QUOTA admission.
			r := aiops.QuotaGateResult{Gate: g}
			allowed, remaining, hasBudget := e.CostBudgets.Peek(dims.CostScope, dims.CostUSD)
			r.Detail = remaining
			if !hasBudget {
				r.Allowed = true
				r.Reason = "no budget configured"
			} else if allowed {
				r.Allowed = true
			} else {
				r.Reason = "cost budget exceeded"
			}
			scope, usd := dims.CostScope, dims.CostUSD
			evals = append(evals, gateEval{res: r, consume: func() bool {
				ok, _, err := e.CostBudgets.Charge(scope, usd)
				return err == nil && ok
			}})

		case aiops.GateCarbon:
			if !dims.HasCarbon {
				return dec, errors.New("policy requires CARBON gate params (CARBON tenant tokens ...)")
			}
			r := aiops.QuotaGateResult{Gate: g}
			sim, hasBudget := e.Carbon.Simulate(dims.CarbonTenant, dims.CarbonFeature, dims.CarbonModel, dims.CarbonRegion, dims.CarbonTokens)
			r.Detail = sim.ProjectedG
			if hasBudget && sim.WouldExceed {
				r.Reason = "carbon budget exceeded"
			} else {
				r.Allowed = true
				if !hasBudget {
					r.Reason = "no budget configured"
				}
			}
			d := dims
			evals = append(evals, gateEval{res: r, consume: func() bool {
				_, err := e.Carbon.Charge(d.CarbonTenant, d.CarbonFeature, d.CarbonModel, d.CarbonRegion, d.CarbonTokens)
				return err == nil
			}})

		case aiops.GateRisk:
			if !dims.HasRisk {
				return dec, errors.New("policy requires RISK gate params (RISK session score)")
			}
			r := aiops.QuotaGateResult{Gate: g}
			peek := e.RiskBudgets.Peek(dims.RiskSession, dims.RiskScore)
			r.Detail = peek.Balance
			if peek.Enforce {
				r.Reason = "risk budget exhausted"
			} else {
				r.Allowed = true
			}
			sess, score := dims.RiskSession, dims.RiskScore
			evals = append(evals, gateEval{res: r, consume: func() bool {
				_, err := e.RiskBudgets.Debit(sess, score, "quota.admit")
				return err == nil
			}})

		case aiops.GateRate:
			if !dims.HasRate {
				return dec, errors.New("policy requires RATE gate params (RATE key window-ms max [cost])")
			}
			r := aiops.QuotaGateResult{Gate: g}
			window := time.Duration(dims.RateWindowMs) * time.Millisecond
			cost := dims.RateCost
			if cost <= 0 {
				cost = 1
			}
			ok, remaining, retry, _ := e.RateLimit.Peek(dims.RateKey, window, dims.RateMax, cost)
			r.Allowed = ok
			r.Detail = float64(remaining)
			if !ok {
				r.Reason = "rate limit exceeded"
				r.RetryAfterMs = retry
			}
			key := dims.RateKey
			rateMax := dims.RateMax
			evals = append(evals, gateEval{res: r, consume: func() bool {
				ok, _, _, _ := e.RateLimit.Allow(key, window, rateMax, cost)
				return ok
			}})

		case aiops.GateMarket:
			if !dims.HasMarket {
				return dec, errors.New("policy requires MARKET gate params (MARKET market maxprice)")
			}
			r := aiops.QuotaGateResult{Gate: g}
			price, ok := e.Market.Price(dims.MarketID)
			if !ok {
				// Unknown market → no contention measured → allow.
				r.Allowed = true
				r.Reason = "no market data"
			} else {
				r.Detail = price
				if price <= dims.MarketMaxPrice {
					r.Allowed = true
				} else {
					r.Reason = "market price above ceiling"
				}
			}
			// MARKET is a signal-only gate: peeked, never consumed.
			evals = append(evals, gateEval{res: r, consume: nil})

		default:
			return dec, fmt.Errorf("unknown gate %q", g)
		}
	}

	// Verdict.
	allowed := 0
	var maxRetry int64
	for _, ev := range evals {
		if ev.res.Allowed {
			allowed++
		} else {
			dec.DeniedBy = append(dec.DeniedBy, ev.res.Gate)
		}
		if ev.res.RetryAfterMs > maxRetry {
			maxRetry = ev.res.RetryAfterMs
		}
	}
	if pol.Mode == aiops.QuotaModeAny {
		dec.Admitted = allowed > 0
	} else {
		dec.Admitted = allowed == len(evals)
	}
	dec.RetryAfterMs = maxRetry

	// Commit phase — only when actually admitting. The quota mutex makes this
	// atomic with the peek phase, so a gate that peeked OK commits OK; we still
	// honor each consume's real result (a direct COST.CHARGE outside QUOTA
	// could interleave) and only set Consumed on a genuine commit.
	if commit && dec.Admitted {
		dec.Committed = true
		for i := range evals {
			consumeThis := pol.Mode != aiops.QuotaModeAny || evals[i].res.Allowed
			if consumeThis && evals[i].consume != nil {
				evals[i].res.Consumed = evals[i].consume()
			}
		}
	}

	for _, ev := range evals {
		dec.Gates = append(dec.Gates, ev.res)
	}
	return dec, nil
}
