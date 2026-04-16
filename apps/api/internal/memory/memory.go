// Package memory implements per-user memory store with semantic recall.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vector"
)

type Entry struct {
	ID        string            `json:"id"`
	UserID    string            `json:"user_id"`
	Text      string            `json:"text"`
	CreatedAt time.Time         `json:"created_at"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	byID    map[string]*Entry
	byUser  map[string]map[string]struct{}
	ix      *vector.Index
}

func New(dim int) *Store {
	return &Store{
		byID:   make(map[string]*Entry),
		byUser: make(map[string]map[string]struct{}),
		ix:     vector.NewIndex(dim),
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) Add(userID, text string, meta map[string]string) *Entry {
	e := &Entry{
		ID:        newID(),
		UserID:    userID,
		Text:      strings.TrimSpace(text),
		CreatedAt: time.Now(),
		Meta:      meta,
	}
	if meta == nil {
		e.Meta = map[string]string{}
	}
	e.Meta["user_id"] = userID

	s.mu.Lock()
	s.byID[e.ID] = e
	if _, ok := s.byUser[userID]; !ok {
		s.byUser[userID] = make(map[string]struct{})
	}
	s.byUser[userID][e.ID] = struct{}{}
	s.mu.Unlock()

	s.ix.Upsert(e.ID, e.Text, e.Meta)
	return e
}

func (s *Store) Delete(userID, id string) bool {
	s.mu.Lock()
	e, ok := s.byID[id]
	if !ok || e.UserID != userID {
		s.mu.Unlock()
		return false
	}
	delete(s.byID, id)
	if set, ok := s.byUser[userID]; ok {
		delete(set, id)
	}
	s.mu.Unlock()
	s.ix.Delete(id)
	return true
}

func (s *Store) List(userID string) []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.byUser[userID]
	out := make([]*Entry, 0, len(set))
	for id := range set {
		if e, ok := s.byID[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

type QueryHit struct {
	Entry *Entry  `json:"entry"`
	Score float32 `json:"score"`
}

// Query returns top-k memories for a user ranked by semantic similarity.
func (s *Store) Query(userID, q string, k int, threshold float32) []QueryHit {
	hits := s.ix.Search(q, 0, 0) // collect all, filter by user
	out := make([]QueryHit, 0, k)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range hits {
		if h.Meta["user_id"] != userID {
			continue
		}
		if h.Score < threshold {
			continue
		}
		if e, ok := s.byID[h.ID]; ok {
			out = append(out, QueryHit{Entry: e, Score: h.Score})
			if k > 0 && len(out) >= k {
				break
			}
		}
	}
	return out
}

// Synthesize returns a compact context string for LLM injection.
func Synthesize(hits []QueryHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Based on stored context:\n")
	for _, h := range hits {
		b.WriteString("- ")
		b.WriteString(h.Entry.Text)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

func (s *Store) Users() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byUser)
}
