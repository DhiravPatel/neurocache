// Package eviction implements the AI-scored eviction policy.
package eviction

import (
	"sort"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

type Scorer interface {
	Score(e store.Entry, now time.Time) float64
}

// AISmart = freq*0.4 + recency*0.35 - size*0.25  (lower = evict first)
type AISmart struct {
	MaxEntrySize int
}

func (p AISmart) Score(e store.Entry, now time.Time) float64 {
	freq := float64(e.Hits)
	// decay: recency in (0,1]; 1.0 = just accessed, lower = stale
	var recency float64 = 1.0
	if !e.LastRead.IsZero() {
		age := now.Sub(e.LastRead).Seconds()
		recency = 1.0 / (1.0 + age/3600.0) // half-ish within an hour
	}
	sizePenalty := float64(e.Bytes) / 1024.0 // KB
	return freq*0.40 + recency*0.35 - sizePenalty*0.25
}

// LRU: older LastRead = evict first.
type LRU struct{}

func (LRU) Score(e store.Entry, now time.Time) float64 {
	if e.LastRead.IsZero() {
		return -float64(now.Unix())
	}
	return -float64(now.Sub(e.LastRead).Seconds())
}

// LFU: fewer hits = evict first.
type LFU struct{}

func (LFU) Score(e store.Entry, _ time.Time) float64 { return float64(e.Hits) }

func NewScorer(policy string) Scorer {
	switch policy {
	case "lru":
		return LRU{}
	case "lfu":
		return LFU{}
	case "noeviction":
		return nil
	default:
		return AISmart{}
	}
}

// PickVictims returns up to n lowest-scoring keys.
func PickVictims(snap []store.Entry, scorer Scorer, n int) []string {
	if scorer == nil || n <= 0 || len(snap) == 0 {
		return nil
	}
	now := time.Now()
	type pair struct {
		key   string
		score float64
	}
	scored := make([]pair, len(snap))
	for i, e := range snap {
		scored[i] = pair{e.Key, scorer.Score(e, now)}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score < scored[j].score })
	if n > len(scored) {
		n = len(scored)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = scored[i].key
	}
	return out
}
