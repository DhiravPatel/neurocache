// Package semcache implements SEMANTIC_SET / SEMANTIC_GET and the LLM
// response cache on top of the vector index.
package semcache

import (
	"sync"
	"sync/atomic"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
)

type Store struct {
	ix      *vector.Index
	mu      sync.RWMutex
	values  map[string]string // id -> value
	hits    atomic.Uint64
	misses  atomic.Uint64
	namespace string
}

func New(dim int, namespace string) *Store {
	return &Store{
		ix:        vector.NewIndex(dim),
		values:    make(map[string]string),
		namespace: namespace,
	}
}

// Set stores a key phrase with its value; the key is embedded for semantic
// retrieval. Returns the stable id used in the vector index.
func (s *Store) Set(key, value string) string {
	s.mu.Lock()
	s.values[key] = value
	s.mu.Unlock()
	s.ix.Upsert(key, key, map[string]string{"ns": s.namespace})
	return key
}

// Get returns (value, score, hit). A score of 0 means no hit.
func (s *Store) Get(query string, threshold float32) (string, float32, bool) {
	hits := s.ix.Search(query, 1, threshold)
	if len(hits) == 0 {
		s.misses.Add(1)
		return "", 0, false
	}
	s.mu.RLock()
	v, ok := s.values[hits[0].ID]
	s.mu.RUnlock()
	if !ok {
		s.misses.Add(1)
		return "", 0, false
	}
	s.hits.Add(1)
	return v, hits[0].Score, true
}

func (s *Store) Del(key string) bool {
	s.mu.Lock()
	_, ok := s.values[key]
	delete(s.values, key)
	s.mu.Unlock()
	s.ix.Delete(key)
	return ok
}

type Stats struct {
	Size    int     `json:"size"`
	Hits    uint64  `json:"hits"`
	Misses  uint64  `json:"misses"`
	HitRate float64 `json:"hit_rate"`
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	size := len(s.values)
	s.mu.RUnlock()
	h := s.hits.Load()
	m := s.misses.Load()
	rate := 0.0
	if h+m > 0 {
		rate = float64(h) / float64(h+m)
	}
	return Stats{Size: size, Hits: h, Misses: m, HitRate: rate}
}
