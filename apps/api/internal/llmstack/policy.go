package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// PolicyClassifier is a semantic firewall by example. INJECT.* is
// a regex pattern library; regex libraries rot — attackers
// paraphrase, teams can't maintain them. POLICY.* is the
// complement: define policy by EXAMPLE and classify incoming text
// by nearest-neighbour in embedding space.
//
// Maintenance shifts from "author a regex" to "paste an example."
// When a new attack phrasing shows up in the wild, you POLICY.ADD
// it as a seed — every future paraphrase of it now triggers.
//
// Commands:
//
//   POLICY.DEFINE policy-id ACTION block|allow|escalate
//        SEEDS seed1 seed2 seed3 ...
//   POLICY.ADD policy-id seed                  (incremental learning)
//   POLICY.REMOVE policy-id seed-index
//   POLICY.CHECK policy-id text [THRESHOLD t]
//        → [matched, action, nearest_score, matched_seed_idx]
//   POLICY.LIST [policy-id]
//   POLICY.FORGET policy-id
//   POLICY.STATS
//
// Each seed is stored with its L2-normalised embedding. CHECK
// computes cosine vs every seed, returns the max. Default
// threshold 0.80; tune per policy if attackers learn to barely
// dodge.
//
// Throughput: O(N seeds) per CHECK. At 50 seeds × 128 dims that's
// ~25 µs. Apps grow seed banks slowly (a handful of seeds catches
// most paraphrases due to embedding generalisation).
type PolicyClassifier struct {
	mu       sync.RWMutex
	policies map[string]*policyState

	totalDefines atomic.Int64
	totalChecks  atomic.Int64
	totalBlocks  atomic.Int64
	totalAllows  atomic.Int64
	totalEscalates atomic.Int64
}

type policyState struct {
	id     string
	action string
	mu     sync.RWMutex
	seeds  []policySeed
	dim    int
}

type policySeed struct {
	text string
	vec  []float64 // L2-normalised
}

// NewPolicyClassifier returns an empty classifier.
func NewPolicyClassifier() *PolicyClassifier {
	return &PolicyClassifier{policies: map[string]*policyState{}}
}

// Define registers (or replaces) a policy from a seed list.
func (p *PolicyClassifier) Define(policyID, action string, seeds []string) error {
	if policyID == "" {
		return errors.New("policy_id required")
	}
	if !validAction(action) {
		return errors.New("action must be block | allow | escalate")
	}
	if len(seeds) == 0 {
		return errors.New("at least one seed required")
	}
	st := &policyState{id: policyID, action: action}
	for _, s := range seeds {
		if s == "" {
			continue
		}
		vec := embedFallback(s)
		if st.dim == 0 {
			st.dim = len(vec)
		}
		st.seeds = append(st.seeds, policySeed{text: s, vec: vec})
	}
	if len(st.seeds) == 0 {
		return errors.New("no non-empty seeds provided")
	}
	p.mu.Lock()
	p.policies[policyID] = st
	p.mu.Unlock()
	p.totalDefines.Add(1)
	return nil
}

// Add appends a new seed to an existing policy. The growable-by-
// example primitive — apps paste new attack phrasings as they
// surface.
func (p *PolicyClassifier) Add(policyID, seed string) error {
	if seed == "" {
		return errors.New("seed required")
	}
	p.mu.RLock()
	st, ok := p.policies[policyID]
	p.mu.RUnlock()
	if !ok {
		return errors.New("unknown policy_id: " + policyID)
	}
	vec := embedFallback(seed)
	if st.dim != 0 && len(vec) != st.dim {
		return errors.New("seed embedding dim mismatch")
	}
	st.mu.Lock()
	st.seeds = append(st.seeds, policySeed{text: seed, vec: vec})
	st.mu.Unlock()
	return nil
}

// Remove drops a seed by 0-based index.
func (p *PolicyClassifier) Remove(policyID string, seedIdx int) bool {
	p.mu.RLock()
	st, ok := p.policies[policyID]
	p.mu.RUnlock()
	if !ok {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if seedIdx < 0 || seedIdx >= len(st.seeds) {
		return false
	}
	st.seeds = append(st.seeds[:seedIdx], st.seeds[seedIdx+1:]...)
	return true
}

// CheckResult is what CHECK returns.
type PolicyCheckResult struct {
	Matched         bool    `json:"matched"`
	Action          string  `json:"action"`
	NearestScore    float64 `json:"nearest_score"`
	MatchedSeedIdx  int     `json:"matched_seed_idx"`
	MatchedSeed     string  `json:"matched_seed,omitempty"`
}

// Check returns the nearest-neighbour verdict.
func (p *PolicyClassifier) Check(policyID, text string, threshold float64) (PolicyCheckResult, bool) {
	p.totalChecks.Add(1)
	if threshold <= 0 {
		threshold = 0.80
	}
	p.mu.RLock()
	st, ok := p.policies[policyID]
	p.mu.RUnlock()
	if !ok {
		return PolicyCheckResult{}, false
	}
	vec := embedFallback(text)
	st.mu.RLock()
	if len(st.seeds) == 0 || (st.dim != 0 && len(vec) != st.dim) {
		st.mu.RUnlock()
		return PolicyCheckResult{Action: st.action, MatchedSeedIdx: -1}, true
	}
	bestIdx := 0
	bestScore := 0.0
	for i, s := range st.seeds {
		score := dotProduct(vec, s.vec)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	matchedSeed := st.seeds[bestIdx].text
	st.mu.RUnlock()

	res := PolicyCheckResult{
		Matched:        bestScore >= threshold,
		Action:         st.action,
		NearestScore:   bestScore,
		MatchedSeedIdx: bestIdx,
		MatchedSeed:    matchedSeed,
	}
	if res.Matched {
		switch st.action {
		case "block":
			p.totalBlocks.Add(1)
		case "allow":
			p.totalAllows.Add(1)
		case "escalate":
			p.totalEscalates.Add(1)
		}
	}
	return res, true
}

// PolicyRow is one row of LIST.
type PolicyRow struct {
	PolicyID string   `json:"policy_id"`
	Action   string   `json:"action"`
	Seeds    []string `json:"seeds"`
	SeedN    int      `json:"seed_count"`
}

// List returns every policy (or one specific policy if id != "").
func (p *PolicyClassifier) List(policyID string) []PolicyRow {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PolicyRow, 0)
	for id, st := range p.policies {
		if policyID != "" && id != policyID {
			continue
		}
		st.mu.RLock()
		seeds := make([]string, len(st.seeds))
		for i, s := range st.seeds {
			seeds[i] = s.text
		}
		st.mu.RUnlock()
		out = append(out, PolicyRow{
			PolicyID: id, Action: st.action,
			Seeds: seeds, SeedN: len(seeds),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PolicyID < out[j].PolicyID })
	return out
}

// Forget drops a policy entirely.
func (p *PolicyClassifier) Forget(policyID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.policies[policyID]
	delete(p.policies, policyID)
	return ok
}

// PolicyStats is the global snapshot.
type PolicyStats struct {
	Policies       int   `json:"policies"`
	TotalDefines   int64 `json:"total_defines"`
	TotalChecks    int64 `json:"total_checks"`
	TotalBlocks    int64 `json:"total_blocks"`
	TotalAllows    int64 `json:"total_allows"`
	TotalEscalates int64 `json:"total_escalates"`
}

func (p *PolicyClassifier) Stats() PolicyStats {
	p.mu.RLock()
	n := len(p.policies)
	p.mu.RUnlock()
	return PolicyStats{
		Policies:       n,
		TotalDefines:   p.totalDefines.Load(),
		TotalChecks:    p.totalChecks.Load(),
		TotalBlocks:    p.totalBlocks.Load(),
		TotalAllows:    p.totalAllows.Load(),
		TotalEscalates: p.totalEscalates.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func validAction(a string) bool {
	return a == "block" || a == "allow" || a == "escalate"
}
