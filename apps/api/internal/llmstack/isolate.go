package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Isolation enforces hard tenant boundaries inside semantic retrieval.
// The most embarrassing failure mode of a shared vector index: user A's
// query cosine-matches user B's confidential doc and returns it.
// RATELIMIT.SEM and POLICY.SEM throttle and classify, but neither
// enforces a structural "this vector is unreachable from this tenant"
// rule. ISOLATE.* is that rule.
//
// The contract is binding-level: every vector that retrieval can
// surface MUST have a tenant binding, and queries declare AS_TENANT.
// AUDIT walks the index for unbound vectors so they can be fixed
// before they leak. The classification is annotation-only ("public",
// "internal", "confidential", ...) but is exposed in CHECK output so
// the caller can apply downstream policy (e.g. mask before display).
//
// This module owns the policy table only; the actual filtering
// happens at retrieval time — the retrieval manager calls Permits()
// for each candidate. Keeping the policy out of the index means a
// re-classification doesn't require re-indexing.
//
// Commands:
//
//   ISOLATE.BIND vector-id TENANT t [CLASS c]
//        Idempotent. Overwrites prior binding.
//   ISOLATE.UNBIND vector-id
//   ISOLATE.CHECK vector-id AS_TENANT t
//        → allowed=1/0, tenant_of_vector, class
//   ISOLATE.PERMITS vector-id AS_TENANT t
//        → 1/0 (boolean, suitable for inline guard)
//   ISOLATE.LIST_FOR TENANT t
//   ISOLATE.AUDIT [VECTORS v1 v2 ...]
//        Without VECTORS: returns the count of bound + the list of
//        explicitly-registered "expected" IDs that have no binding.
//        With VECTORS: returns which of those IDs are unbound.
//   ISOLATE.EXPECT vector-id
//        Register a vector that *should* have a binding — AUDIT
//        flags it if it doesn't. Use this when indexing.
//   ISOLATE.STATS
//
// Hot path: PERMITS is one map lookup. Designed to sit in front of
// every retrieval result (a 100-result top-k pays ~100 cheap lookups).
type Isolation struct {
	mu       sync.RWMutex
	bindings map[string]*isoBinding // vector-id → binding
	expected map[string]bool        // vector-id → "should have a binding"

	totalBinds   atomic.Int64
	totalChecks  atomic.Int64
	totalDenials atomic.Int64
}

type isoBinding struct {
	Tenant string
	Class  string
}

// NewIsolation returns an empty registry.
func NewIsolation() *Isolation {
	return &Isolation{
		bindings: map[string]*isoBinding{},
		expected: map[string]bool{},
	}
}

// Bind attaches a tenant (and optional classification) to a vector.
// Empty class is fine ("" → unclassified). class is normalised to
// lower-case so "Confidential" and "confidential" don't both exist.
func (i *Isolation) Bind(vectorID, tenant, class string) error {
	if vectorID == "" {
		return errors.New("vector_id required")
	}
	if tenant == "" {
		return errors.New("tenant required")
	}
	i.totalBinds.Add(1)
	i.mu.Lock()
	i.bindings[vectorID] = &isoBinding{Tenant: tenant, Class: strings.ToLower(class)}
	i.mu.Unlock()
	return nil
}

// Unbind removes the binding. Returns 1 if a binding was removed, 0
// otherwise. The "expected" registration is left intact so AUDIT can
// still flag the gap.
func (i *Isolation) Unbind(vectorID string) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.bindings[vectorID]; ok {
		delete(i.bindings, vectorID)
		return 1
	}
	return 0
}

// IsolateCheck is CHECK's structured return.
type IsolateCheck struct {
	VectorID string `json:"vector_id"`
	Allowed  bool   `json:"allowed"`
	Tenant   string `json:"tenant_of_vector"`
	Class    string `json:"class"`
	Reason   string `json:"reason"`
}

// Check returns the structured guard decision. Unbound vectors are
// denied by default — fail-closed is the only safe policy for an
// isolation primitive. Use ISOLATE.EXPECT + ISOLATE.AUDIT to catch
// the gap, not a permissive default.
func (i *Isolation) Check(vectorID, asTenant string) IsolateCheck {
	out := IsolateCheck{VectorID: vectorID}
	i.totalChecks.Add(1)
	if vectorID == "" || asTenant == "" {
		out.Reason = "vector_id and as_tenant required"
		i.totalDenials.Add(1)
		return out
	}
	i.mu.RLock()
	b, ok := i.bindings[vectorID]
	i.mu.RUnlock()
	if !ok {
		out.Reason = "no binding — fail-closed"
		i.totalDenials.Add(1)
		return out
	}
	out.Tenant = b.Tenant
	out.Class = b.Class
	if b.Tenant != asTenant {
		out.Reason = "tenant mismatch"
		i.totalDenials.Add(1)
		return out
	}
	out.Allowed = true
	out.Reason = "ok"
	return out
}

// Permits is the boolean fast path for inline filtering.
func (i *Isolation) Permits(vectorID, asTenant string) bool {
	return i.Check(vectorID, asTenant).Allowed
}

// ListFor returns every vector currently bound to a tenant.
func (i *Isolation) ListFor(tenant string) []string {
	if tenant == "" {
		return nil
	}
	i.mu.RLock()
	out := make([]string, 0)
	for vid, b := range i.bindings {
		if b.Tenant == tenant {
			out = append(out, vid)
		}
	}
	i.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Expect registers a vector that *must* have a binding. AUDIT flags
// it if it doesn't. Callers register every indexed vector this way so
// the audit catches "oops, forgot to bind tenant X's last upload".
func (i *Isolation) Expect(vectorID string) error {
	if vectorID == "" {
		return errors.New("vector_id required")
	}
	i.mu.Lock()
	i.expected[vectorID] = true
	i.mu.Unlock()
	return nil
}

// IsolateAuditResult is AUDIT's return.
type IsolateAuditResult struct {
	Bound    int      `json:"bound"`
	Expected int      `json:"expected"`
	Unbound  []string `json:"unbound"`
}

// Audit walks the registry. If vectorIDs is empty, it walks the
// "expected" set; otherwise it audits the supplied list directly
// (useful for spot-checking an external index dump).
func (i *Isolation) Audit(vectorIDs []string) IsolateAuditResult {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := IsolateAuditResult{Bound: len(i.bindings), Expected: len(i.expected)}
	if len(vectorIDs) == 0 {
		for v := range i.expected {
			if _, ok := i.bindings[v]; !ok {
				out.Unbound = append(out.Unbound, v)
			}
		}
	} else {
		for _, v := range vectorIDs {
			if _, ok := i.bindings[v]; !ok {
				out.Unbound = append(out.Unbound, v)
			}
		}
	}
	sort.Strings(out.Unbound)
	return out
}

// IsolateStats is the global snapshot.
type IsolateStats struct {
	Bound        int   `json:"bound"`
	Expected     int   `json:"expected"`
	Unbound      int   `json:"unbound_expected"`
	TotalBinds   int64 `json:"total_binds"`
	TotalChecks  int64 `json:"total_checks"`
	TotalDenials int64 `json:"total_denials"`
}

func (i *Isolation) Stats() IsolateStats {
	i.mu.RLock()
	defer i.mu.RUnlock()
	unbound := 0
	for v := range i.expected {
		if _, ok := i.bindings[v]; !ok {
			unbound++
		}
	}
	return IsolateStats{
		Bound:        len(i.bindings),
		Expected:     len(i.expected),
		Unbound:      unbound,
		TotalBinds:   i.totalBinds.Load(),
		TotalChecks:  i.totalChecks.Load(),
		TotalDenials: i.totalDenials.Load(),
	}
}
