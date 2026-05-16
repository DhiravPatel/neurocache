package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SpecDecCache is two things at once: the draft-token cache that
// speculative decoding needs to skip the small-model pass, AND the
// acceptance-rate tracker that lets the orchestrator decide whether
// speculative decoding is even worth running for a given (model,
// prefix-class) pair.
//
// Speculative decoding is the standard LLM-inference trick: a small
// fast "draft" model proposes N tokens ahead; the large "verifier"
// model accepts the matching prefix in one forward pass and rejects
// the rest. Typical 2-3× speedup *when the draft model is well
// matched to the input*. When the draft is poorly matched (code
// generation under a chat draft, multilingual under English draft),
// acceptance drops to ~10% and specdec slows the system down.
//
// SPECDEC.* gives apps the two pieces they always end up rebuilding:
//
//   draft-token cache  — (prefix-hash → tokens) so the small model's
//                        prediction is reused across hits in a session
//                        / across replays / across A:B traffic.
//   acceptance tracker — per (model, prefix-class) EMA of "tokens
//                        accepted / tokens proposed", so the
//                        orchestrator can call DECIDE and get a
//                        yes/no on whether to bother spec-decoding.
//
// Commands:
//
//   SPECDEC.CACHE  prefix-hash token-list
//        Cache the small-model's draft for a prefix.
//   SPECDEC.GET    prefix-hash
//        Retrieve a cached draft. Returns nil if no entry.
//   SPECDEC.RECORD model prefix-class accepted total
//        Update the acceptance-rate EMA for a (model, prefix-class).
//   SPECDEC.RATE   model [PREFIX_CLASS class]
//        Current acceptance rate for the model (or one class).
//   SPECDEC.DECIDE model prefix-class
//        → {use, rate, samples, reason}
//        use = 1 if the acceptance rate is high enough to be worth it.
//   SPECDEC.STATUS model
//        Per-class snapshot for one model.
//   SPECDEC.RESET  model|ALL
//   SPECDEC.STATS
//
// Hot path: GET is one map lookup. RECORD is atomic CAS on a float
// EMA. DECIDE is one read.
type SpecDecCache struct {
	mu     sync.RWMutex
	drafts map[string][]string // prefix-hash → tokens

	rateMu sync.RWMutex
	rates  map[string]map[string]*specdecStat // model → class → stat

	cacheCap int

	totalCacheWrites atomic.Int64
	totalCacheHits   atomic.Int64
	totalCacheMisses atomic.Int64
	totalRecords     atomic.Int64
	totalDecisions   atomic.Int64
}

type specdecStat struct {
	rateEMA  float64 // EMA of accepted/total ∈ [0,1]
	samples  int64
	accepted int64
	proposed int64
}

// NewSpecDecCache returns an empty cache with a 100k draft cap.
func NewSpecDecCache() *SpecDecCache {
	return &SpecDecCache{
		drafts:   map[string][]string{},
		rates:    map[string]map[string]*specdecStat{},
		cacheCap: 100_000,
	}
}

// SetCap adjusts the draft-cache soft cap.
func (s *SpecDecCache) SetCap(n int) {
	s.mu.Lock()
	s.cacheCap = n
	s.mu.Unlock()
}

// Cache stores draft tokens for a prefix. The first eviction is
// random — apps that want stricter behavior pre-size cap.
func (s *SpecDecCache) Cache(prefixHash string, tokens []string) error {
	if prefixHash == "" {
		return errors.New("prefix hash required")
	}
	if len(tokens) == 0 {
		return errors.New("at least one token required")
	}
	s.totalCacheWrites.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheCap > 0 && len(s.drafts) >= s.cacheCap {
		// Drop a random entry (map iteration is unspecified order)
		for k := range s.drafts {
			delete(s.drafts, k)
			break
		}
	}
	cp := make([]string, len(tokens))
	copy(cp, tokens)
	s.drafts[prefixHash] = cp
	return nil
}

// Get returns cached draft tokens, or nil + false on miss.
func (s *SpecDecCache) Get(prefixHash string) ([]string, bool) {
	s.mu.RLock()
	t, ok := s.drafts[prefixHash]
	s.mu.RUnlock()
	if !ok {
		s.totalCacheMisses.Add(1)
		return nil, false
	}
	s.totalCacheHits.Add(1)
	out := make([]string, len(t))
	copy(out, t)
	return out, true
}

// Record updates the acceptance EMA for a (model, prefix-class).
// accepted = tokens the verifier accepted; total = tokens proposed.
func (s *SpecDecCache) Record(model, class string, accepted, total int64) error {
	if model == "" || class == "" {
		return errors.New("model and prefix_class required")
	}
	if accepted < 0 || total < 0 || accepted > total {
		return errors.New("accepted must be in [0, total]")
	}
	if total == 0 {
		return errors.New("total must be > 0")
	}
	s.totalRecords.Add(1)
	const alpha = 0.10
	rate := float64(accepted) / float64(total)
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	byClass, ok := s.rates[model]
	if !ok {
		byClass = map[string]*specdecStat{}
		s.rates[model] = byClass
	}
	st, ok := byClass[class]
	if !ok {
		st = &specdecStat{rateEMA: rate}
		byClass[class] = st
	} else {
		st.rateEMA = st.rateEMA + alpha*(rate-st.rateEMA)
	}
	st.samples++
	st.accepted += accepted
	st.proposed += total
	return nil
}

// SpecDecRateResult is RATE / DECIDE output.
type SpecDecRateResult struct {
	Model       string  `json:"model"`
	PrefixClass string  `json:"prefix_class,omitempty"`
	Rate        float64 `json:"rate"`
	Samples     int64   `json:"samples"`
	TokensSeen  int64   `json:"tokens_seen"`
}

// Rate returns the current acceptance rate for the model. If
// class=="", aggregates across all classes (token-weighted).
func (s *SpecDecCache) Rate(model, class string) (SpecDecRateResult, bool) {
	s.rateMu.RLock()
	defer s.rateMu.RUnlock()
	byClass, ok := s.rates[model]
	if !ok {
		return SpecDecRateResult{Model: model}, false
	}
	if class != "" {
		st, ok := byClass[class]
		if !ok {
			return SpecDecRateResult{Model: model, PrefixClass: class}, false
		}
		return SpecDecRateResult{
			Model:       model,
			PrefixClass: class,
			Rate:        st.rateEMA,
			Samples:     st.samples,
			TokensSeen:  st.proposed,
		}, true
	}
	// Aggregate across classes — token-weighted
	var accepted, proposed, samples int64
	for _, st := range byClass {
		accepted += st.accepted
		proposed += st.proposed
		samples += st.samples
	}
	rate := 0.0
	if proposed > 0 {
		rate = float64(accepted) / float64(proposed)
	}
	return SpecDecRateResult{
		Model:      model,
		Rate:       rate,
		Samples:    samples,
		TokensSeen: proposed,
	}, true
}

// SpecDecDecision is DECIDE's return.
type SpecDecDecision struct {
	Model       string  `json:"model"`
	PrefixClass string  `json:"prefix_class"`
	Use         bool    `json:"use"`
	Rate        float64 `json:"rate"`
	Samples     int64   `json:"samples"`
	Reason      string  `json:"reason"`
}

// Decide answers "is speculative decoding worth running here?"
//
// Heuristics:
//   samples < 30           → use (warmup — give it a chance to learn)
//   rate    < 0.30         → don't use (draft model is poorly matched)
//   else                   → use
//
// Apps that want a stricter / looser threshold can wrap RATE
// themselves.
func (s *SpecDecCache) Decide(model, class string) SpecDecDecision {
	s.totalDecisions.Add(1)
	r, ok := s.Rate(model, class)
	d := SpecDecDecision{Model: model, PrefixClass: class}
	if !ok {
		d.Use = true
		d.Reason = "no history — assume speculative decoding helps"
		return d
	}
	d.Rate = r.Rate
	d.Samples = r.Samples
	switch {
	case r.Samples < 30:
		d.Use = true
		d.Reason = "warmup — too few samples to decide"
	case r.Rate < 0.30:
		d.Use = false
		d.Reason = "acceptance rate too low — draft model poorly matched"
	default:
		d.Use = true
		d.Reason = "acceptance rate justifies speculative decoding"
	}
	return d
}

// SpecDecStatusRow is one row of STATUS output.
type SpecDecStatusRow struct {
	PrefixClass string  `json:"prefix_class"`
	Rate        float64 `json:"rate"`
	Samples     int64   `json:"samples"`
	Accepted    int64   `json:"accepted"`
	Proposed    int64   `json:"proposed"`
}

// Status returns per-class rows for one model.
func (s *SpecDecCache) Status(model string) ([]SpecDecStatusRow, bool) {
	s.rateMu.RLock()
	defer s.rateMu.RUnlock()
	byClass, ok := s.rates[model]
	if !ok {
		return nil, false
	}
	out := make([]SpecDecStatusRow, 0, len(byClass))
	for class, st := range byClass {
		out = append(out, SpecDecStatusRow{
			PrefixClass: class,
			Rate:        st.rateEMA,
			Samples:     st.samples,
			Accepted:    st.accepted,
			Proposed:    st.proposed,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rate > out[j].Rate })
	return out, true
}

// Reset drops the rate stats. model=="ALL" wipes everything.
func (s *SpecDecCache) Reset(model string) int {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if model == "ALL" {
		n := len(s.rates)
		s.rates = map[string]map[string]*specdecStat{}
		return n
	}
	if _, ok := s.rates[model]; ok {
		delete(s.rates, model)
		return 1
	}
	return 0
}

// SpecDecStats is the global snapshot.
type SpecDecStats struct {
	Drafts           int   `json:"drafts"`
	ModelsTracked    int   `json:"models_tracked"`
	TotalCacheWrites int64 `json:"total_cache_writes"`
	TotalCacheHits   int64 `json:"total_cache_hits"`
	TotalCacheMisses int64 `json:"total_cache_misses"`
	TotalRecords     int64 `json:"total_records"`
	TotalDecisions   int64 `json:"total_decisions"`
}

func (s *SpecDecCache) Stats() SpecDecStats {
	s.mu.RLock()
	n := len(s.drafts)
	s.mu.RUnlock()
	s.rateMu.RLock()
	m := len(s.rates)
	s.rateMu.RUnlock()
	return SpecDecStats{
		Drafts:           n,
		ModelsTracked:    m,
		TotalCacheWrites: s.totalCacheWrites.Load(),
		TotalCacheHits:   s.totalCacheHits.Load(),
		TotalCacheMisses: s.totalCacheMisses.Load(),
		TotalRecords:     s.totalRecords.Load(),
		TotalDecisions:   s.totalDecisions.Load(),
	}
}

// touch is a no-op kept to satisfy time import once we extend
// SpecDecStat with a "last_seen" field (planned for the next round).
func (s *SpecDecCache) touch() time.Time { return time.Now() }
