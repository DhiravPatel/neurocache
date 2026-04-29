package primitives

import (
	"sort"
	"sync"
	"time"
)

// Recommender wires NeuroCache's existing semantic infrastructure
// into a first-class collaborative-filtering surface. Each user's
// interactions (views, likes, purchases) feed a per-user profile;
// AI.RECOMMEND returns the top-K items that similar users liked but
// the requester hasn't seen.
//
// The implementation is the textbook user-based CF algorithm:
//
//   1. Each AI.LIKE user item [weight] records (user, item, weight).
//   2. AI.RECOMMEND user [k] computes
//        cosine_similarity(user_profile, peer_profile)
//      across every other user, then ranks items by
//        sum_over_peers(peer_score(item) × user_similarity(peer))
//      excluding items the user has already interacted with.
//   3. AI.SIMILAR user [k] returns the top-K most similar users
//      (handy for "people you might want to follow").
//
// This is the classic recommender substrate Reddit/Spotify/etc. all
// rebuild from scratch. Now it's three commands. The cosine math is
// shared with the existing semantic cache vector code.
type Recommender struct {
	mu      sync.RWMutex
	users   map[string]map[string]float64 // user -> item -> weight
	items   map[string]map[string]float64 // item -> user -> weight (reverse index for fast lookup)
	updated map[string]time.Time          // last interaction per user — used to bias toward fresh activity
}

// NewRecommender returns an empty recommender.
func NewRecommender() *Recommender {
	return &Recommender{
		users:   map[string]map[string]float64{},
		items:   map[string]map[string]float64{},
		updated: map[string]time.Time{},
	}
}

// Like records an interaction. weight defaults to 1; callers can pass
// higher values for stronger signals (e.g. "purchase" vs "view").
func (r *Recommender) Like(user, item string, weight float64) {
	if weight == 0 {
		weight = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[user]; !ok {
		r.users[user] = map[string]float64{}
	}
	r.users[user][item] += weight
	if _, ok := r.items[item]; !ok {
		r.items[item] = map[string]float64{}
	}
	r.items[item][user] += weight
	r.updated[user] = time.Now()
}

// Forget drops every interaction for a user (e.g. account deletion).
func (r *Recommender) Forget(user string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for item := range r.users[user] {
		delete(r.items[item], user)
	}
	delete(r.users, user)
	delete(r.updated, user)
}

// Recommend returns the top-k items for `user`, scored by
// peer-similarity-weighted item scores.
type Recommendation struct {
	Item  string
	Score float64
}

func (r *Recommender) Recommend(user string, k int) []Recommendation {
	if k <= 0 {
		k = 10
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.users[user]
	if !ok || len(profile) == 0 {
		return nil
	}
	// Find peers — only consider users who share at least one item
	// with the requester. Reverse-lookup from the requester's items.
	peerSeen := map[string]struct{}{}
	for item := range profile {
		for peer := range r.items[item] {
			if peer != user {
				peerSeen[peer] = struct{}{}
			}
		}
	}
	// Score each candidate item by similarity-weighted peer interest.
	itemScores := map[string]float64{}
	for peer := range peerSeen {
		sim := r.cosine(user, peer)
		if sim <= 0 {
			continue
		}
		for item, w := range r.users[peer] {
			if _, seen := profile[item]; seen {
				continue // skip items the user already knows
			}
			itemScores[item] += sim * w
		}
	}
	// Top-K by score.
	out := make([]Recommendation, 0, len(itemScores))
	for item, s := range itemScores {
		out = append(out, Recommendation{Item: item, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Item < out[j].Item
	})
	if k < len(out) {
		out = out[:k]
	}
	return out
}

// Similar returns the top-K users with highest cosine similarity to
// `user`. Useful for follower suggestions, peer matching, etc.
type SimilarUser struct {
	User       string
	Similarity float64
}

func (r *Recommender) Similar(user string, k int) []SimilarUser {
	if k <= 0 {
		k = 10
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.users[user]
	if !ok {
		return nil
	}
	peers := map[string]struct{}{}
	for item := range profile {
		for peer := range r.items[item] {
			if peer != user {
				peers[peer] = struct{}{}
			}
		}
	}
	out := make([]SimilarUser, 0, len(peers))
	for peer := range peers {
		sim := r.cosine(user, peer)
		if sim > 0 {
			out = append(out, SimilarUser{User: peer, Similarity: sim})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		return out[i].User < out[j].User
	})
	if k < len(out) {
		out = out[:k]
	}
	return out
}

// cosine computes weight-vector cosine similarity between two user
// profiles. Caller holds r.mu.RLock().
func (r *Recommender) cosine(a, b string) float64 {
	pa := r.users[a]
	pb := r.users[b]
	if len(pa) == 0 || len(pb) == 0 {
		return 0
	}
	var dot, na, nb float64
	for item, w := range pa {
		na += w * w
		if pw, ok := pb[item]; ok {
			dot += w * pw
		}
	}
	for _, w := range pb {
		nb += w * w
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

// Stats reports rollups for AI.STATS.
type RecStats struct {
	Users        int
	Items        int
	Interactions int
}

func (r *Recommender) Stats() RecStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, items := range r.users {
		total += len(items)
	}
	return RecStats{Users: len(r.users), Items: len(r.items), Interactions: total}
}

// sqrt is a tiny shim so we don't pull in math just for a single call
// from this package's hot path (every rec sums many cosines).
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = (z + x/z) / 2
	}
	return z
}
