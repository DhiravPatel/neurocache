package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ConvForkManager manages branched conversation trees.
//
// Existing CONV.* gives you a single linear turn log per session — fine
// for chat. The moment you want to explore *what-if* paths ("retry the
// agent from step 7 with a different system prompt", "A/B two tool
// choices from the same history", "let three planners diverge from a
// shared planning prefix"), linear conversations stop working: you'd
// have to copy turns by hand, manage parent pointers in app code, and
// lose the ability to share the prefix.
//
// CONV.FORK.* is a first-class fork tree. Every branch records its
// parent + the index it diverged at; turns are copied on fork (cheap —
// strings are immutable Go-side). The tree lets the dashboard show
// agent exploration as a real DAG and lets apps prune dead branches.
//
// Commands:
//
//   CONV.FORK.SEED root-id
//        Create a new empty root branch.
//   CONV.FORK.CREATE parent-id fork-id [AT index]
//        Fork an existing branch. If AT is omitted, copies all turns
//        from parent. Fork-id must be unique.
//   CONV.FORK.APPEND conv-id role content
//        Append a turn to one branch (independent of siblings).
//   CONV.FORK.GET conv-id
//        Return all turns on one branch.
//   CONV.FORK.LIST parent-id
//        Direct children of parent-id (sorted).
//   CONV.FORK.TREE root-id
//        Full descendant tree as a flat parent→children map.
//   CONV.FORK.DELETE conv-id
//        Delete branch AND every descendant. Returns count dropped.
//   CONV.FORK.STATS
//
// Hot path: APPEND is one map lookup + slice append under a per-branch
// RWMutex. Fork is O(turns copied). Apps that exhaust a deep tree
// should DELETE the parent so the whole subtree is freed in one shot.
type ConvForkManager struct {
	mu       sync.RWMutex
	branches map[string]*forkBranch

	totalSeeds   atomic.Int64
	totalForks   atomic.Int64
	totalAppends atomic.Int64
	totalDeletes atomic.Int64
}

type forkBranch struct {
	mu        sync.RWMutex
	parentID  string  // "" if root
	forkedAt  int     // index in parent at fork time; -1 if root
	turns     []forkTurn
	children  []string
	createdAt int64
}

type forkTurn struct {
	Role    string
	Content string
	TS      int64
}

// NewConvForkManager returns an empty fork tree.
func NewConvForkManager() *ConvForkManager {
	return &ConvForkManager{branches: map[string]*forkBranch{}}
}

// Seed creates a new empty root branch.
func (m *ConvForkManager) Seed(id string) error {
	if id == "" {
		return errors.New("id required")
	}
	m.totalSeeds.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.branches[id]; exists {
		return errors.New("branch id already exists: " + id)
	}
	m.branches[id] = &forkBranch{
		parentID:  "",
		forkedAt:  -1,
		createdAt: time.Now().UnixNano(),
	}
	return nil
}

// Create forks parentID into a new branch with forkID at index `at`.
// at=-1 means "copy all parent turns".
func (m *ConvForkManager) Create(parentID, forkID string, at int) error {
	if parentID == "" || forkID == "" {
		return errors.New("parent_id and fork_id required")
	}
	if parentID == forkID {
		return errors.New("fork_id must differ from parent_id")
	}
	m.totalForks.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, ok := m.branches[parentID]
	if !ok {
		return errors.New("unknown parent_id: " + parentID)
	}
	if _, exists := m.branches[forkID]; exists {
		return errors.New("fork_id already exists: " + forkID)
	}
	parent.mu.RLock()
	cap := len(parent.turns)
	idx := at
	if idx < 0 || idx > cap {
		idx = cap
	}
	turns := make([]forkTurn, idx)
	copy(turns, parent.turns[:idx])
	parent.mu.RUnlock()

	m.branches[forkID] = &forkBranch{
		parentID:  parentID,
		forkedAt:  idx,
		turns:     turns,
		createdAt: time.Now().UnixNano(),
	}
	parent.mu.Lock()
	parent.children = append(parent.children, forkID)
	parent.mu.Unlock()
	return nil
}

// Append adds a turn to one branch.
func (m *ConvForkManager) Append(convID, role, content string) error {
	if convID == "" {
		return errors.New("conv_id required")
	}
	if role == "" {
		return errors.New("role required")
	}
	m.totalAppends.Add(1)
	m.mu.RLock()
	b, ok := m.branches[convID]
	m.mu.RUnlock()
	if !ok {
		return errors.New("unknown conv_id: " + convID)
	}
	b.mu.Lock()
	b.turns = append(b.turns, forkTurn{
		Role: role, Content: content, TS: time.Now().UnixNano(),
	})
	b.mu.Unlock()
	return nil
}

// ForkTurnRow is one row of CONV.FORK.GET.
type ForkTurnRow struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	TS      int64  `json:"ts"`
}

// Get returns the turn list for one branch.
func (m *ConvForkManager) Get(convID string) ([]ForkTurnRow, bool) {
	m.mu.RLock()
	b, ok := m.branches[convID]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]ForkTurnRow, len(b.turns))
	for i, t := range b.turns {
		out[i] = ForkTurnRow{Role: t.Role, Content: t.Content, TS: t.TS / int64(time.Second)}
	}
	return out, true
}

// List returns direct children of a branch, sorted.
func (m *ConvForkManager) List(parentID string) ([]string, bool) {
	m.mu.RLock()
	b, ok := m.branches[parentID]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	b.mu.RLock()
	out := make([]string, len(b.children))
	copy(out, b.children)
	b.mu.RUnlock()
	sort.Strings(out)
	return out, true
}

// ForkTreeNode is one entry in a tree dump.
type ForkTreeNode struct {
	ID         string   `json:"id"`
	ParentID   string   `json:"parent_id,omitempty"`
	ForkedAt   int      `json:"forked_at"`
	TurnCount  int      `json:"turns"`
	ChildIDs   []string `json:"children"`
	CreatedAt  int64    `json:"created_at"`
}

// Tree returns the full descendant tree rooted at rootID, depth-first.
func (m *ConvForkManager) Tree(rootID string) ([]ForkTreeNode, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.branches[rootID]
	if !ok {
		return nil, false
	}
	out := make([]ForkTreeNode, 0, 8)
	var walk func(id string)
	walk = func(id string) {
		b, ok := m.branches[id]
		if !ok {
			return
		}
		b.mu.RLock()
		kids := make([]string, len(b.children))
		copy(kids, b.children)
		sort.Strings(kids)
		out = append(out, ForkTreeNode{
			ID:        id,
			ParentID:  b.parentID,
			ForkedAt:  b.forkedAt,
			TurnCount: len(b.turns),
			ChildIDs:  kids,
			CreatedAt: b.createdAt / int64(time.Second),
		})
		b.mu.RUnlock()
		for _, k := range kids {
			walk(k)
		}
	}
	walk(rootID)
	return out, true
}

// Delete removes a branch and every descendant. Returns drop count.
func (m *ConvForkManager) Delete(convID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.branches[convID]
	if !ok {
		return 0
	}
	// Collect subtree
	var subtree []string
	var walk func(id string)
	walk = func(id string) {
		subtree = append(subtree, id)
		if br, ok := m.branches[id]; ok {
			br.mu.RLock()
			kids := append([]string(nil), br.children...)
			br.mu.RUnlock()
			for _, k := range kids {
				walk(k)
			}
		}
	}
	walk(convID)

	// Detach from parent
	if b.parentID != "" {
		if parent, ok := m.branches[b.parentID]; ok {
			parent.mu.Lock()
			filtered := parent.children[:0]
			for _, c := range parent.children {
				if c != convID {
					filtered = append(filtered, c)
				}
			}
			parent.children = filtered
			parent.mu.Unlock()
		}
	}
	for _, id := range subtree {
		delete(m.branches, id)
	}
	m.totalDeletes.Add(int64(len(subtree)))
	return len(subtree)
}

// ForkStats is the global snapshot.
type ForkStats struct {
	Branches     int   `json:"branches"`
	Roots        int   `json:"roots"`
	TotalSeeds   int64 `json:"total_seeds"`
	TotalForks   int64 `json:"total_forks"`
	TotalAppends int64 `json:"total_appends"`
	TotalDeletes int64 `json:"total_deletes"`
}

func (m *ConvForkManager) Stats() ForkStats {
	m.mu.RLock()
	n := len(m.branches)
	roots := 0
	for _, b := range m.branches {
		if b.parentID == "" {
			roots++
		}
	}
	m.mu.RUnlock()
	return ForkStats{
		Branches:     n,
		Roots:        roots,
		TotalSeeds:   m.totalSeeds.Load(),
		TotalForks:   m.totalForks.Load(),
		TotalAppends: m.totalAppends.Load(),
		TotalDeletes: m.totalDeletes.Load(),
	}
}
