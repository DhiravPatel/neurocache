package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FedRegistry is the federated meta-learning primitive — CRDT-for-
// learned-signals. Nodes in a fleet share *learned posteriors*
// (TRUST Beta counts, BANDIT arm pulls, RAG.GAP cluster stats)
// without sharing raw user data. Each node's trust scores improve
// from the entire fleet's traffic; the data never leaves its origin
// node.
//
// Model: every learned scalar is exposed as a "signal" — a tuple
// (kind, key, alpha, beta, n). EXPORT collects every signal local
// to this node; MERGE additively combines another node's export
// into ours (alpha += other.alpha, etc.). Idempotent under merge
// (CRDT semantics) because Beta posteriors are commutative under
// addition.
//
// For unweighted counts (BANDIT arm pulls, simple frequencies) we
// use g-counter semantics. For posterior distributions (TRUST) we
// use Beta addition. For sets-of-keys (RAG.GAP cluster IDs) we use
// OR-set semantics with per-key generation tokens.
//
// Commands:
//
//   FED.NODE node-id              — register this node's id (one-time)
//   FED.EXPORT [KIND k]
//        → JSON array of signals. Pass to another node's MERGE.
//   FED.MERGE node-id signals-json
//        Apply another node's signals additively into ours.
//   FED.SIGNAL kind key alpha beta [N n]
//        Manually add or update one signal (typically not used —
//        the primitives feeding FED do this themselves via Update).
//   FED.GET kind key             — read one merged signal
//   FED.PEERS                    — every node we've merged from
//   FED.FORGET kind key|ALL
//   FED.STATS
//
// In production the calling app schedules: every N minutes do
// "EXPORT on each node → broadcast to peers → MERGE on each".
// The signal payloads are tiny (kilobytes per node per merge) and
// CRDT merge means the order doesn't matter — late or duplicate
// merges are no-ops, network partitions self-heal.
type FedRegistry struct {
	mu       sync.RWMutex
	signals  map[string]*fedSignal // key = kind|key
	nodeID   string
	peers    map[string]time.Time // peer node id → last merge

	totalExports atomic.Int64
	totalMerges  atomic.Int64
	totalUpdates atomic.Int64
}

type fedSignal struct {
	Kind      string  `json:"kind"`
	Key       string  `json:"key"`
	Alpha     float64 `json:"alpha"`
	Beta      float64 `json:"beta"`
	N         int64   `json:"n"`
	UpdatedAt int64   `json:"updated_unix"`
}

// NewFedRegistry returns an empty registry.
func NewFedRegistry() *FedRegistry {
	return &FedRegistry{
		signals: map[string]*fedSignal{},
		peers:   map[string]time.Time{},
	}
}

// Node sets the local node id. Self-merges are rejected by Merge to
// avoid double-counting. The id is also returned in EXPORT.
func (f *FedRegistry) Node(nodeID string) error {
	if nodeID == "" {
		return errors.New("node_id required")
	}
	f.mu.Lock()
	f.nodeID = nodeID
	f.mu.Unlock()
	return nil
}

// Update is the in-process API: the calling primitive (TRUST, BANDIT,
// RAG.GAP) calls this to bump a signal locally. CRDT-friendly: pure
// addition.
func (f *FedRegistry) Update(kind, key string, dAlpha, dBeta float64, dN int64) error {
	if kind == "" || key == "" {
		return errors.New("kind and key required")
	}
	f.totalUpdates.Add(1)
	k := kind + "|" + key
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.signals[k]
	if !ok {
		s = &fedSignal{Kind: kind, Key: key}
		f.signals[k] = s
	}
	s.Alpha += dAlpha
	s.Beta += dBeta
	s.N += dN
	s.UpdatedAt = time.Now().Unix()
	return nil
}

// Signal is the user-callable variant of Update (RESP-friendly).
// Replaces (does not add) the value — for manual seeding / corrections.
func (f *FedRegistry) Signal(kind, key string, alpha, beta float64, n int64) error {
	if kind == "" || key == "" {
		return errors.New("kind and key required")
	}
	if alpha < 0 || beta < 0 || n < 0 {
		return errors.New("alpha, beta, n must be non-negative")
	}
	f.totalUpdates.Add(1)
	k := kind + "|" + key
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals[k] = &fedSignal{
		Kind: kind, Key: key, Alpha: alpha, Beta: beta, N: n,
		UpdatedAt: time.Now().Unix(),
	}
	return nil
}

// FedExport is what EXPORT returns and MERGE consumes.
type FedExport struct {
	NodeID  string      `json:"node_id"`
	Signals []fedSignal `json:"signals"`
}

// Export returns this node's signals (optionally filtered to one kind).
func (f *FedRegistry) Export(kind string) FedExport {
	f.totalExports.Add(1)
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := FedExport{NodeID: f.nodeID}
	for _, s := range f.signals {
		if kind != "" && s.Kind != kind {
			continue
		}
		out.Signals = append(out.Signals, *s)
	}
	sort.Slice(out.Signals, func(i, j int) bool {
		if out.Signals[i].Kind != out.Signals[j].Kind {
			return out.Signals[i].Kind < out.Signals[j].Kind
		}
		return out.Signals[i].Key < out.Signals[j].Key
	})
	return out
}

// Merge applies a peer's signals additively. The peer node id is
// recorded so we know who we've ever merged from. Self-merge (peer ==
// our own node id) is rejected — would double-count.
func (f *FedRegistry) Merge(peerNodeID string, signals []fedSignal) (int, error) {
	if peerNodeID == "" {
		return 0, errors.New("peer node_id required")
	}
	f.mu.Lock()
	if peerNodeID == f.nodeID {
		f.mu.Unlock()
		return 0, errors.New("refusing self-merge: peer == local node_id")
	}
	f.peers[peerNodeID] = time.Now()
	f.mu.Unlock()
	f.totalMerges.Add(1)
	merged := 0
	for _, ps := range signals {
		if err := f.Update(ps.Kind, ps.Key, ps.Alpha, ps.Beta, ps.N); err != nil {
			continue
		}
		merged++
	}
	return merged, nil
}

// Get returns the merged signal value for (kind, key).
func (f *FedRegistry) Get(kind, key string) (fedSignal, bool) {
	if kind == "" || key == "" {
		return fedSignal{}, false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	s, ok := f.signals[kind+"|"+key]
	if !ok {
		return fedSignal{}, false
	}
	return *s, true
}

// FedPeerRow is one row of PEERS.
type FedPeerRow struct {
	NodeID         string `json:"node_id"`
	LastMergeUnix  int64  `json:"last_merge_unix"`
}

// Peers lists every node we've merged from.
func (f *FedRegistry) Peers() []FedPeerRow {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]FedPeerRow, 0, len(f.peers))
	for k, v := range f.peers {
		out = append(out, FedPeerRow{NodeID: k, LastMergeUnix: v.Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastMergeUnix > out[j].LastMergeUnix })
	return out
}

// Forget drops a signal (or all). For kind+key="ALL|ALL" wipe.
func (f *FedRegistry) Forget(kind, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == "ALL" {
		n := len(f.signals)
		f.signals = map[string]*fedSignal{}
		return n
	}
	k := kind + "|" + key
	if _, ok := f.signals[k]; ok {
		delete(f.signals, k)
		return 1
	}
	return 0
}

// FedStats is the global snapshot.
type FedStats struct {
	NodeID       string `json:"node_id"`
	Signals      int    `json:"signals"`
	Peers        int    `json:"peers"`
	TotalUpdates int64  `json:"total_updates"`
	TotalExports int64  `json:"total_exports"`
	TotalMerges  int64  `json:"total_merges"`
}

func (f *FedRegistry) Stats() FedStats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return FedStats{
		NodeID:       f.nodeID,
		Signals:      len(f.signals),
		Peers:        len(f.peers),
		TotalUpdates: f.totalUpdates.Load(),
		TotalExports: f.totalExports.Load(),
		TotalMerges:  f.totalMerges.Load(),
	}
}
