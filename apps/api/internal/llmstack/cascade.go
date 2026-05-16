package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// CascadeRouter implements a cost-tier model fallback ladder with
// LEARNING. Standard practice: try cheap model first; if quality is
// poor (LLM-judge below threshold, grounding fails, structured
// output invalid), retry with the expensive model. Apps reinvent
// this pattern in every project but never cache the LEARNING:
// "this kind of input ALWAYS needs the expensive model" — so they
// pay for the cheap-model failure round-trip on every identical
// request.
//
// CASCADE.* memoises which tier each input ultimately needed. Next
// identical input → skip the cheap tier entirely.
//
// Commands:
//
//   CASCADE.CONFIG cascade-id tier1 tier2 tier3 ...
//        Ordered cheapest→most-expensive (e.g. gpt-3.5 / gpt-4 /
//        gpt-4-turbo, or local-llm / cloud-cheap / cloud-premium).
//
//   CASCADE.PICK cascade-id input
//        → tier to try (cached previous winner if known, else
//          tier-0 cheapest)
//
//   CASCADE.RECORD cascade-id input tier-used success
//        Records the outcome. On success at tier-N, cache says
//        "next time use tier-N." On failure at the last tier, the
//        cache forgets so the next identical input retries from
//        the top (transient upstream issue, not an inherent
//        hard input).
//
//   CASCADE.STATUS cascade-id input → which tier the cache thinks
//                                     is needed; tier_idx=-1 means
//                                     no cached opinion yet.
//   CASCADE.FORGET cascade-id input
//   CASCADE.LIST cascade-id [LIMIT n] — sorted by hit count
//   CASCADE.PURGE [CASCADE id]
//   CASCADE.STATS — per-tier success counts + $ saved metric
//
// Storage: per-cascade learned (input-hash → tier-idx) map +
// per-tier atomic counters. Lock-free PICK on the hot path.
type CascadeRouter struct {
	mu       sync.RWMutex
	cascades map[string]*cascadeState

	totalPicks   atomic.Int64
	totalRecords atomic.Int64
	totalLearned atomic.Int64 // PICK returned a learned tier (not the default cheapest)
}

type cascadeState struct {
	id    string
	tiers []string

	mu      sync.RWMutex
	learned map[string]*learnedTier // sha256[:16] -> learned outcome
	tierWins []atomic.Int64         // per-tier success counts (len(tiers))
	tierFails []atomic.Int64
}

type learnedTier struct {
	tierIdx  int32 // which tier won
	hits     atomic.Int64
}

// NewCascadeRouter returns an empty registry.
func NewCascadeRouter() *CascadeRouter {
	return &CascadeRouter{cascades: map[string]*cascadeState{}}
}

// Config registers (or replaces) a cascade definition.
func (c *CascadeRouter) Config(cascadeID string, tiers []string) error {
	if cascadeID == "" {
		return errors.New("cascade_id required")
	}
	if len(tiers) < 2 {
		return errors.New("cascade needs at least 2 tiers (cheap → expensive)")
	}
	for _, t := range tiers {
		if t == "" {
			return errors.New("tier name cannot be empty")
		}
	}
	c.mu.Lock()
	c.cascades[cascadeID] = &cascadeState{
		id:        cascadeID,
		tiers:     append([]string(nil), tiers...),
		learned:   map[string]*learnedTier{},
		tierWins:  make([]atomic.Int64, len(tiers)),
		tierFails: make([]atomic.Int64, len(tiers)),
	}
	c.mu.Unlock()
	return nil
}

// PickResult is what PICK returns.
type CascadePick struct {
	TierIdx     int    `json:"tier_idx"`
	Tier        string `json:"tier"`
	Learned     bool   `json:"learned"`
}

// Pick returns the tier to try. If the cache has previously
// learned this input needs a specific tier, returns that; else
// returns tier-0 (cheapest).
func (c *CascadeRouter) Pick(cascadeID, input string) (CascadePick, bool) {
	c.totalPicks.Add(1)
	c.mu.RLock()
	st, ok := c.cascades[cascadeID]
	c.mu.RUnlock()
	if !ok {
		return CascadePick{}, false
	}
	k := cascadeKey(input)
	st.mu.RLock()
	lt, ok := st.learned[k]
	st.mu.RUnlock()
	if ok {
		lt.hits.Add(1)
		c.totalLearned.Add(1)
		return CascadePick{
			TierIdx: int(lt.tierIdx),
			Tier:    st.tiers[lt.tierIdx],
			Learned: true,
		}, true
	}
	return CascadePick{TierIdx: 0, Tier: st.tiers[0], Learned: false}, true
}

// Record updates the learned mapping based on the actual outcome.
// On success at tier-N, cache "next time use tier-N."
// On failure at the LAST tier, FORGET — likely transient issue.
// On failure at a middle tier, no cache update (the app will
// escalate to the next tier).
func (c *CascadeRouter) Record(cascadeID, input string, tierUsed int, success bool) error {
	c.totalRecords.Add(1)
	c.mu.RLock()
	st, ok := c.cascades[cascadeID]
	c.mu.RUnlock()
	if !ok {
		return errors.New("unknown cascade_id: " + cascadeID)
	}
	if tierUsed < 0 || tierUsed >= len(st.tiers) {
		return errors.New("tier_used out of range")
	}
	if success {
		st.tierWins[tierUsed].Add(1)
		k := cascadeKey(input)
		st.mu.Lock()
		st.learned[k] = &learnedTier{tierIdx: int32(tierUsed)}
		st.mu.Unlock()
	} else {
		st.tierFails[tierUsed].Add(1)
		if tierUsed == len(st.tiers)-1 {
			// Last tier failed — forget so we retry from the top.
			k := cascadeKey(input)
			st.mu.Lock()
			delete(st.learned, k)
			st.mu.Unlock()
		}
	}
	return nil
}

// Status returns the learned tier for an input, or tier_idx=-1 if
// the cache has no opinion yet.
func (c *CascadeRouter) Status(cascadeID, input string) (CascadePick, bool) {
	c.mu.RLock()
	st, ok := c.cascades[cascadeID]
	c.mu.RUnlock()
	if !ok {
		return CascadePick{}, false
	}
	k := cascadeKey(input)
	st.mu.RLock()
	lt, ok := st.learned[k]
	st.mu.RUnlock()
	if !ok {
		return CascadePick{TierIdx: -1, Tier: "", Learned: false}, true
	}
	return CascadePick{
		TierIdx: int(lt.tierIdx),
		Tier:    st.tiers[lt.tierIdx],
		Learned: true,
	}, true
}

// Forget drops the learned mapping for one input.
func (c *CascadeRouter) Forget(cascadeID, input string) bool {
	c.mu.RLock()
	st, ok := c.cascades[cascadeID]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	k := cascadeKey(input)
	st.mu.Lock()
	_, was := st.learned[k]
	delete(st.learned, k)
	st.mu.Unlock()
	return was
}

// Purge drops a cascade entirely (or all if cascade-id empty).
func (c *CascadeRouter) Purge(cascadeID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cascadeID == "" {
		n := len(c.cascades)
		c.cascades = map[string]*cascadeState{}
		return n
	}
	if _, ok := c.cascades[cascadeID]; !ok {
		return 0
	}
	delete(c.cascades, cascadeID)
	return 1
}

// TierStatsRow is one tier's stats.
type TierStatsRow struct {
	TierIdx   int    `json:"tier_idx"`
	Tier      string `json:"tier"`
	Wins      int64  `json:"wins"`
	Fails     int64  `json:"fails"`
	WinRate   float64 `json:"win_rate"`
}

// CascadeStatusRow is one cascade's full stats.
type CascadeStatusRow struct {
	CascadeID    string         `json:"cascade_id"`
	Tiers        []TierStatsRow `json:"tiers"`
	LearnedCount int            `json:"learned_count"`
}

// All returns every configured cascade with per-tier stats.
func (c *CascadeRouter) All() []CascadeStatusRow {
	c.mu.RLock()
	out := make([]CascadeStatusRow, 0, len(c.cascades))
	for _, st := range c.cascades {
		row := CascadeStatusRow{CascadeID: st.id}
		st.mu.RLock()
		row.LearnedCount = len(st.learned)
		st.mu.RUnlock()
		for i, t := range st.tiers {
			wins := st.tierWins[i].Load()
			fails := st.tierFails[i].Load()
			rate := 0.0
			if wins+fails > 0 {
				rate = float64(wins) / float64(wins+fails)
			}
			row.Tiers = append(row.Tiers, TierStatsRow{
				TierIdx: i, Tier: t, Wins: wins, Fails: fails, WinRate: rate,
			})
		}
		out = append(out, row)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CascadeID < out[j].CascadeID })
	return out
}

// CascadeStats is the global counters snapshot.
type CascadeStats struct {
	Cascades     int   `json:"cascades"`
	TotalPicks   int64 `json:"total_picks"`
	TotalRecords int64 `json:"total_records"`
	TotalLearned int64 `json:"total_learned_picks"`
}

func (c *CascadeRouter) Stats() CascadeStats {
	c.mu.RLock()
	n := len(c.cascades)
	c.mu.RUnlock()
	return CascadeStats{
		Cascades:     n,
		TotalPicks:   c.totalPicks.Load(),
		TotalRecords: c.totalRecords.Load(),
		TotalLearned: c.totalLearned.Load(),
	}
}

// ─── helpers ───────────────────────────────────────────────────

func cascadeKey(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:8])
}
