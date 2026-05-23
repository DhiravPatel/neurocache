package llmstack

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// BanditRouter implements an adaptive multi-armed bandit router.
// CANARY.* is a fixed split with manual promote. MOE/CASCADE route
// on static capability. None of them LEARN from the traffic.
// BANDIT.* converges traffic onto whatever arm is actually winning
// — no manual PROMOTE step, no operator intervention.
//
// Two strategies:
//
//   1. thompson (default): Thompson sampling. Each arm tracks a
//      Beta(α, β) posterior over its success probability. On
//      PICK, sample once from each arm's posterior; pick the
//      arm with the highest sample. The math handles
//      exploration vs exploitation correctly — early on,
//      posteriors are wide so all arms get exploration; as
//      data accumulates, posteriors narrow around the winner
//      and traffic concentrates there.
//
//   2. ucb: Upper Confidence Bound (UCB1). Pick the arm with
//      the highest mean + sqrt(2 * ln(total_pulls) / arm_pulls).
//      Deterministic — useful when you want reproducibility for
//      CI / debugging.
//
// Commands:
//
//   BANDIT.CREATE bandit-id ARMS arm1 arm2 arm3 ...
//        [STRATEGY thompson|ucb]
//   BANDIT.PICK bandit-id [SEED seed]
//        → [arm, sampled_score, total_pulls]
//   BANDIT.RECORD bandit-id arm score (0..1)
//   BANDIT.STATS bandit-id          → per-arm posterior + traffic share
//   BANDIT.ARMS bandit-id           → list arms only
//   BANDIT.RESET bandit-id          → wipe stats (keep arms)
//   BANDIT.FORGET bandit-id         → drop entirely
//   BANDIT.LIST                     → all bandits
//   BANDIT.GLOBAL_STATS
//
// Storage: per-arm (alpha, beta) atomic counters under sync/atomic
// — PICK is lock-free except for the RNG (per-call rand.Source).
// At ~500 ns/pick it's a viable hot path.
type BanditRouter struct {
	mu      sync.RWMutex
	bandits map[string]*banditState

	totalCreates atomic.Int64
	totalPicks   atomic.Int64
	totalRecords atomic.Int64
}

type banditState struct {
	id       string
	strategy string // thompson | ucb
	arms     []string

	// Per-arm Beta(alpha, beta) posterior counters.
	// alpha = successes + 1, beta = failures + 1 (priors all 1).
	alpha    []atomic.Uint64 // store as bits of float64
	beta     []atomic.Uint64
	pulls    []atomic.Int64
}

// NewBanditRouter returns an empty registry.
func NewBanditRouter() *BanditRouter {
	return &BanditRouter{bandits: map[string]*banditState{}}
}

// Create registers (or replaces) a bandit. Each arm starts with
// Beta(1, 1) — uniform prior, all arms equally likely to be best.
func (b *BanditRouter) Create(banditID string, arms []string, strategy string) error {
	if banditID == "" {
		return errors.New("bandit_id required")
	}
	if len(arms) < 2 {
		return errors.New("bandit needs at least 2 arms")
	}
	if strategy == "" {
		strategy = "thompson"
	}
	if strategy != "thompson" && strategy != "ucb" {
		return errors.New("strategy must be 'thompson' or 'ucb'")
	}
	for _, a := range arms {
		if a == "" {
			return errors.New("arm name cannot be empty")
		}
	}
	st := &banditState{
		id:       banditID,
		strategy: strategy,
		arms:     append([]string(nil), arms...),
		alpha:    make([]atomic.Uint64, len(arms)),
		beta:     make([]atomic.Uint64, len(arms)),
		pulls:    make([]atomic.Int64, len(arms)),
	}
	for i := range arms {
		st.alpha[i].Store(math.Float64bits(1.0))
		st.beta[i].Store(math.Float64bits(1.0))
	}
	b.mu.Lock()
	b.bandits[banditID] = st
	b.mu.Unlock()
	b.totalCreates.Add(1)
	return nil
}

// PickResult is BANDIT.PICK's return.
type BanditPick struct {
	Arm          string  `json:"arm"`
	SampledScore float64 `json:"sampled_score"`
	TotalPulls   int64   `json:"total_pulls"`
}

// Pick returns the next arm to try. Thread-safe via a per-call
// rand source so concurrent picks don't share state.
func (b *BanditRouter) Pick(banditID string, seed int64) (BanditPick, bool) {
	b.totalPicks.Add(1)
	b.mu.RLock()
	st, ok := b.bandits[banditID]
	b.mu.RUnlock()
	if !ok {
		return BanditPick{}, false
	}
	var rng *rand.Rand
	if seed != 0 {
		rng = rand.New(rand.NewSource(seed))
	} else {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	bestIdx := 0
	bestScore := -1.0
	if st.strategy == "thompson" {
		// Sample once from each arm's Beta posterior, pick max
		for i := range st.arms {
			a := math.Float64frombits(st.alpha[i].Load())
			be := math.Float64frombits(st.beta[i].Load())
			s := sampleBeta(rng, a, be)
			if s > bestScore {
				bestScore = s
				bestIdx = i
			}
		}
	} else {
		// UCB1: argmax(mean + sqrt(2 ln(total) / pulls))
		totalPulls := int64(0)
		for i := range st.arms {
			totalPulls += st.pulls[i].Load()
		}
		ln := 0.0
		if totalPulls > 0 {
			ln = math.Log(float64(totalPulls))
		}
		for i := range st.arms {
			pulls := st.pulls[i].Load()
			if pulls == 0 {
				// Unpulled arms get +Inf — always picked first
				bestScore = math.Inf(1)
				bestIdx = i
				break
			}
			a := math.Float64frombits(st.alpha[i].Load())
			be := math.Float64frombits(st.beta[i].Load())
			mean := (a - 1) / (a + be - 2 + 1e-9)
			conf := math.Sqrt(2 * ln / float64(pulls))
			score := mean + conf
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
	}

	total := int64(0)
	for i := range st.arms {
		total += st.pulls[i].Load()
	}
	return BanditPick{
		Arm:          st.arms[bestIdx],
		SampledScore: bestScore,
		TotalPulls:   total,
	}, true
}

// Record updates the posterior for the named arm. score is in
// [0, 1] — treated as a "success probability" sample. Hard
// success/fail callers pass 1.0 / 0.0; partial-credit graders
// pass the score.
func (b *BanditRouter) Record(banditID, arm string, score float64) error {
	if score < 0 || score > 1 {
		return errors.New("score must be in [0, 1]")
	}
	b.totalRecords.Add(1)
	b.mu.RLock()
	st, ok := b.bandits[banditID]
	b.mu.RUnlock()
	if !ok {
		return errors.New("unknown bandit_id: " + banditID)
	}
	idx := -1
	for i, a := range st.arms {
		if a == arm {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("unknown arm: " + arm)
	}
	// Bayesian update: alpha += score; beta += (1 - score)
	addAtomicFloatU64(&st.alpha[idx], score)
	addAtomicFloatU64(&st.beta[idx], 1.0-score)
	st.pulls[idx].Add(1)
	return nil
}

// BanditArmStats is one arm's snapshot.
type BanditArmStats struct {
	Arm         string  `json:"arm"`
	Alpha       float64 `json:"alpha"`
	Beta        float64 `json:"beta"`
	PosteriorMean float64 `json:"posterior_mean"`
	Pulls       int64   `json:"pulls"`
	Share       float64 `json:"share"`
}

// BanditStats is BANDIT.STATS's return.
type BanditStatsResult struct {
	BanditID   string           `json:"bandit_id"`
	Strategy   string           `json:"strategy"`
	Arms       []BanditArmStats `json:"arms"`
	TotalPulls int64            `json:"total_pulls"`
}

// Stats returns per-arm Beta posteriors + traffic share.
func (b *BanditRouter) Stats(banditID string) (BanditStatsResult, bool) {
	b.mu.RLock()
	st, ok := b.bandits[banditID]
	b.mu.RUnlock()
	if !ok {
		return BanditStatsResult{}, false
	}
	total := int64(0)
	for i := range st.arms {
		total += st.pulls[i].Load()
	}
	out := BanditStatsResult{
		BanditID:   banditID,
		Strategy:   st.strategy,
		TotalPulls: total,
	}
	for i, name := range st.arms {
		a := math.Float64frombits(st.alpha[i].Load())
		be := math.Float64frombits(st.beta[i].Load())
		mean := a / (a + be)
		pulls := st.pulls[i].Load()
		share := 0.0
		if total > 0 {
			share = float64(pulls) / float64(total)
		}
		out.Arms = append(out.Arms, BanditArmStats{
			Arm: name, Alpha: a, Beta: be,
			PosteriorMean: mean, Pulls: pulls, Share: share,
		})
	}
	return out, true
}

// Arms returns the arm list only.
func (b *BanditRouter) Arms(banditID string) ([]string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	st, ok := b.bandits[banditID]
	if !ok {
		return nil, false
	}
	return append([]string(nil), st.arms...), true
}

// Reset wipes posteriors but keeps the arm definitions.
func (b *BanditRouter) Reset(banditID string) bool {
	b.mu.RLock()
	st, ok := b.bandits[banditID]
	b.mu.RUnlock()
	if !ok {
		return false
	}
	for i := range st.arms {
		st.alpha[i].Store(math.Float64bits(1.0))
		st.beta[i].Store(math.Float64bits(1.0))
		st.pulls[i].Store(0)
	}
	return true
}

// Forget drops a bandit entirely.
func (b *BanditRouter) Forget(banditID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.bandits[banditID]
	delete(b.bandits, banditID)
	return ok
}

// List returns every bandit id.
func (b *BanditRouter) List() []string {
	b.mu.RLock()
	out := make([]string, 0, len(b.bandits))
	for id := range b.bandits {
		out = append(out, id)
	}
	b.mu.RUnlock()
	sort.Strings(out)
	return out
}

// GlobalStats is the registry-wide snapshot.
type BanditGlobalStats struct {
	Bandits      int   `json:"bandits"`
	TotalCreates int64 `json:"total_creates"`
	TotalPicks   int64 `json:"total_picks"`
	TotalRecords int64 `json:"total_records"`
}

func (b *BanditRouter) GlobalStats() BanditGlobalStats {
	b.mu.RLock()
	n := len(b.bandits)
	b.mu.RUnlock()
	return BanditGlobalStats{
		Bandits:      n,
		TotalCreates: b.totalCreates.Load(),
		TotalPicks:   b.totalPicks.Load(),
		TotalRecords: b.totalRecords.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

// sampleBeta draws a single sample from Beta(a, b) via two Gamma
// draws — standard textbook construction. Beta(a, b) = X / (X + Y)
// where X ~ Gamma(a, 1), Y ~ Gamma(b, 1).
func sampleBeta(rng *rand.Rand, a, b float64) float64 {
	x := sampleGamma(rng, a)
	y := sampleGamma(rng, b)
	if x+y == 0 {
		return 0
	}
	return x / (x + y)
}

// sampleGamma draws from Gamma(k, 1) via Marsaglia & Tsang's
// "squeeze" method — fast and accurate for k >= 1. For k < 1 we
// boost via Gamma(k+1) * U^(1/k) (Stuart's identity).
func sampleGamma(rng *rand.Rand, k float64) float64 {
	if k < 1 {
		// Stuart's identity: X ~ Gamma(k+1), U ~ Uniform → X * U^(1/k) ~ Gamma(k)
		return sampleGamma(rng, k+1) * math.Pow(rng.Float64(), 1.0/k)
	}
	d := k - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		var x, v float64
		for {
			x = rng.NormFloat64()
			v = 1 + c*x
			if v > 0 {
				break
			}
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// addAtomicFloatU64 is CAS-loop float64 addition on an atomic.Uint64
// holding float bits. Used for lock-free posterior updates.
func addAtomicFloatU64(slot *atomic.Uint64, delta float64) {
	for {
		oldBits := slot.Load()
		newBits := math.Float64frombits(oldBits) + delta
		if slot.CompareAndSwap(oldBits, math.Float64bits(newBits)) {
			return
		}
	}
}
