package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// EgressGuard is the semantic DLP primitive on outbound generation.
// ISOLATE guards retrieval *in*: don't surface tenant B's doc when
// tenant A queries. EGRESS guards generation *out*: don't include
// content semantically too close to a known-sensitive cluster in a
// request leaving the system (whether to a third-party tool, a user,
// or a log sink).
//
// The model: register one or more "sensitive clusters" — each a list
// of embeddings of sensitive documents. CHECK an outbound text
// against them; if the max cosine to any cluster member exceeds
// MIN_BLOCK, return blocked=1.
//
// Commands:
//
//   EGRESS.REGISTER cluster-id text [LABEL "name"]
//        Add one sample to the cluster. Multiple REGISTER calls
//        accumulate.
//   EGRESS.CHECK text [CLUSTER c] [MIN_BLOCK f]
//        Default CLUSTER = all; default MIN_BLOCK = 0.85.
//        → blocked, cluster_id, score, sample_label
//   EGRESS.CLUSTERS         — registered cluster ids
//   EGRESS.UNREGISTER cluster-id sample-label
//   EGRESS.RESET cluster-id|ALL
//   EGRESS.STATS
type EgressGuard struct {
	mu       sync.RWMutex
	clusters map[string]*egressCluster

	totalRegisters atomic.Int64
	totalChecks    atomic.Int64
	totalBlocks    atomic.Int64
}

type egressCluster struct {
	mu      sync.RWMutex
	samples []egressSample
}

type egressSample struct {
	Label string
	Vec   []float64
}

// NewEgressGuard returns an empty guard.
func NewEgressGuard() *EgressGuard {
	return &EgressGuard{clusters: map[string]*egressCluster{}}
}

// Register adds one text sample to a cluster.
func (e *EgressGuard) Register(clusterID, text, label string) error {
	if clusterID == "" {
		return errors.New("cluster_id required")
	}
	if text == "" {
		return errors.New("text required")
	}
	e.totalRegisters.Add(1)
	c := e.clusterOrCreate(clusterID)
	c.mu.Lock()
	c.samples = append(c.samples, egressSample{Label: label, Vec: embedFallback(text)})
	c.mu.Unlock()
	return nil
}

// EgressCheckResult is CHECK's return.
type EgressCheckResult struct {
	Blocked     bool    `json:"blocked"`
	ClusterID   string  `json:"cluster_id"`
	Score       float64 `json:"score"`
	SampleLabel string  `json:"sample_label"`
	Reason      string  `json:"reason"`
}

// Check tests an outbound text. clusterID="" means "check against
// every registered cluster"; minBlock=0 → default 0.85.
func (e *EgressGuard) Check(text, clusterID string, minBlock float64) EgressCheckResult {
	e.totalChecks.Add(1)
	if text == "" {
		return EgressCheckResult{Reason: "empty text — nothing to check"}
	}
	if minBlock <= 0 {
		minBlock = 0.85
	}
	q := embedFallback(text)
	e.mu.RLock()
	defer e.mu.RUnlock()
	var bestScore float64
	bestCluster := ""
	bestLabel := ""
	for cid, c := range e.clusters {
		if clusterID != "" && cid != clusterID {
			continue
		}
		c.mu.RLock()
		for _, s := range c.samples {
			score := dotProduct(s.Vec, q)
			if score > bestScore {
				bestScore = score
				bestCluster = cid
				bestLabel = s.Label
			}
		}
		c.mu.RUnlock()
	}
	out := EgressCheckResult{Score: bestScore, ClusterID: bestCluster, SampleLabel: bestLabel}
	if bestScore >= minBlock {
		out.Blocked = true
		out.Reason = "text exceeds similarity threshold to sensitive cluster"
		e.totalBlocks.Add(1)
	} else {
		out.Reason = "ok"
	}
	return out
}

// Unregister drops one sample by label from a cluster.
func (e *EgressGuard) Unregister(clusterID, label string) int {
	e.mu.RLock()
	c, ok := e.clusters[clusterID]
	e.mu.RUnlock()
	if !ok {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.samples {
		if s.Label == label {
			c.samples = append(c.samples[:i], c.samples[i+1:]...)
			return 1
		}
	}
	return 0
}

// Reset drops a whole cluster.
func (e *EgressGuard) Reset(clusterID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if clusterID == "ALL" {
		n := len(e.clusters)
		e.clusters = map[string]*egressCluster{}
		return n
	}
	if _, ok := e.clusters[clusterID]; ok {
		delete(e.clusters, clusterID)
		return 1
	}
	return 0
}

// Clusters lists registered clusters with sample counts.
func (e *EgressGuard) Clusters() []string {
	e.mu.RLock()
	out := make([]string, 0, len(e.clusters))
	for k := range e.clusters {
		out = append(out, k)
	}
	e.mu.RUnlock()
	sort.Strings(out)
	return out
}

// EgressStats is the global snapshot.
type EgressStats struct {
	Clusters       int   `json:"clusters"`
	TotalRegisters int64 `json:"total_registers"`
	TotalChecks    int64 `json:"total_checks"`
	TotalBlocks    int64 `json:"total_blocks"`
}

func (e *EgressGuard) Stats() EgressStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return EgressStats{
		Clusters: len(e.clusters),
		TotalRegisters: e.totalRegisters.Load(),
		TotalChecks: e.totalChecks.Load(),
		TotalBlocks: e.totalBlocks.Load(),
	}
}

func (e *EgressGuard) clusterOrCreate(id string) *egressCluster {
	e.mu.RLock()
	c, ok := e.clusters[id]
	e.mu.RUnlock()
	if ok {
		return c
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.clusters[id]; ok {
		return c
	}
	c = &egressCluster{}
	e.clusters[id] = c
	return c
}
