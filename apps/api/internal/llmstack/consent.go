package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ConsentLedger records per-user data-use grants with expiry. Memory
// and retrieval consult the ledger before surfacing user-derived
// facts, and refuse to return facts whose consent has lapsed. This
// is GDPR's "right to be forgotten" and CCPA's opt-out wired as an
// enforced primitive, not a policy doc somebody is supposed to
// remember to consult.
//
// The model:
//
//   - A *grant* binds (user, scope, purpose) → granted-until expiry.
//     scope is opaque (the app names what data is covered:
//     "memory:summary", "training:embeddings", "share:third-party").
//     purpose names why the data is used ("billing", "personalization",
//     "ml-training"). Both are case-insensitive on lookup.
//
//   - REVOKE explicitly drops a grant (right to be forgotten).
//
//   - PERMITS is the fast-path check the data layer calls. It returns
//     allow=1 only if a non-expired, non-revoked grant exists for the
//     (user, scope, purpose) triple. Default deny.
//
//   - WITHDRAW user wipes every grant for a user (the "delete me" flow).
//
//   - AUDIT walks the ledger for expiring-soon grants so the app can
//     re-prompt before the data goes dark.
//
// Commands:
//
//   CONSENT.GRANT user scope purpose [TTL seconds] [META k v ...]
//        TTL=0 → permanent (until REVOKE / WITHDRAW)
//   CONSENT.REVOKE user scope purpose
//   CONSENT.WITHDRAW user            — drop every grant for user
//   CONSENT.PERMITS user scope purpose
//        → 1/0 (the inline guard the data layer calls)
//   CONSENT.CHECK user scope purpose
//        → structured: allow, expires_unix, granted_unix, reason
//   CONSENT.LIST user                — every active grant for user
//   CONSENT.EXPIRING [WITHIN seconds] — grants expiring soon
//   CONSENT.STATS
//
// Hot path: PERMITS is one map lookup. Designed to sit on every
// memory/retrieval read.
type ConsentLedger struct {
	mu     sync.RWMutex
	grants map[string]*consentGrant // key = user|scope|purpose

	totalGrants   atomic.Int64
	totalRevokes  atomic.Int64
	totalChecks   atomic.Int64
	totalDenials  atomic.Int64
}

type consentGrant struct {
	User      string
	Scope     string
	Purpose   string
	GrantedAt time.Time
	ExpiresAt time.Time // zero = permanent
	Revoked   bool
	Meta      map[string]string
}

// NewConsentLedger returns an empty ledger.
func NewConsentLedger() *ConsentLedger {
	return &ConsentLedger{grants: map[string]*consentGrant{}}
}

func consentKey(user, scope, purpose string) string {
	return strings.ToLower(user) + "|" + strings.ToLower(scope) + "|" + strings.ToLower(purpose)
}

// Grant adds (or refreshes) a consent grant. TTL=0 means permanent.
// Re-granting a previously revoked tuple resurrects it.
func (c *ConsentLedger) Grant(user, scope, purpose string, ttl time.Duration, meta map[string]string) error {
	if user == "" {
		return errors.New("user required")
	}
	if scope == "" {
		return errors.New("scope required")
	}
	if purpose == "" {
		return errors.New("purpose required")
	}
	if ttl < 0 {
		return errors.New("ttl must be non-negative")
	}
	c.totalGrants.Add(1)
	g := &consentGrant{
		User: user, Scope: scope, Purpose: purpose,
		GrantedAt: time.Now(),
		Meta: copyMetaProv(meta),
	}
	if ttl > 0 {
		g.ExpiresAt = g.GrantedAt.Add(ttl)
	}
	c.mu.Lock()
	c.grants[consentKey(user, scope, purpose)] = g
	c.mu.Unlock()
	return nil
}

// Revoke explicitly tears down a grant.
func (c *ConsentLedger) Revoke(user, scope, purpose string) (int, error) {
	if user == "" || scope == "" || purpose == "" {
		return 0, errors.New("user, scope, purpose required")
	}
	c.totalRevokes.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	key := consentKey(user, scope, purpose)
	if _, ok := c.grants[key]; ok {
		delete(c.grants, key)
		return 1, nil
	}
	return 0, nil
}

// Withdraw wipes every grant for a user (GDPR "delete me" flow).
func (c *ConsentLedger) Withdraw(user string) (int, error) {
	if user == "" {
		return 0, errors.New("user required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := strings.ToLower(user) + "|"
	dropped := 0
	for k := range c.grants {
		if strings.HasPrefix(k, prefix) {
			delete(c.grants, k)
			dropped++
		}
	}
	return dropped, nil
}

// ConsentCheck is CHECK's structured return.
type ConsentCheck struct {
	User        string `json:"user"`
	Scope       string `json:"scope"`
	Purpose     string `json:"purpose"`
	Allow       bool   `json:"allow"`
	GrantedUnix int64  `json:"granted_unix"`
	ExpiresUnix int64  `json:"expires_unix"`
	Reason      string `json:"reason"`
}

// Check returns the structured guard decision. Fail-closed by default.
func (c *ConsentLedger) Check(user, scope, purpose string) ConsentCheck {
	c.totalChecks.Add(1)
	out := ConsentCheck{User: user, Scope: scope, Purpose: purpose}
	if user == "" || scope == "" || purpose == "" {
		out.Reason = "user, scope, purpose required"
		c.totalDenials.Add(1)
		return out
	}
	c.mu.RLock()
	g, ok := c.grants[consentKey(user, scope, purpose)]
	c.mu.RUnlock()
	if !ok {
		out.Reason = "no grant — default deny"
		c.totalDenials.Add(1)
		return out
	}
	now := time.Now()
	out.GrantedUnix = g.GrantedAt.Unix()
	if !g.ExpiresAt.IsZero() {
		out.ExpiresUnix = g.ExpiresAt.Unix()
		if now.After(g.ExpiresAt) {
			out.Reason = "grant expired"
			c.totalDenials.Add(1)
			return out
		}
	}
	if g.Revoked {
		out.Reason = "grant revoked"
		c.totalDenials.Add(1)
		return out
	}
	out.Allow = true
	out.Reason = "ok"
	return out
}

// Permits is the boolean fast-path for inline guards.
func (c *ConsentLedger) Permits(user, scope, purpose string) bool {
	return c.Check(user, scope, purpose).Allow
}

// ConsentGrantRow is one row of LIST.
type ConsentGrantRow struct {
	Scope       string `json:"scope"`
	Purpose     string `json:"purpose"`
	GrantedUnix int64  `json:"granted_unix"`
	ExpiresUnix int64  `json:"expires_unix"` // 0 if permanent
	Expired     bool   `json:"expired"`
}

// List returns every active grant for a user.
func (c *ConsentLedger) List(user string) ([]ConsentGrantRow, bool) {
	if user == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	prefix := strings.ToLower(user) + "|"
	now := time.Now()
	out := make([]ConsentGrantRow, 0)
	for k, g := range c.grants {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		row := ConsentGrantRow{
			Scope: g.Scope, Purpose: g.Purpose,
			GrantedUnix: g.GrantedAt.Unix(),
		}
		if !g.ExpiresAt.IsZero() {
			row.ExpiresUnix = g.ExpiresAt.Unix()
			row.Expired = now.After(g.ExpiresAt)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out, true
}

// ConsentExpiringRow is one row of EXPIRING.
type ConsentExpiringRow struct {
	User        string `json:"user"`
	Scope       string `json:"scope"`
	Purpose     string `json:"purpose"`
	ExpiresUnix int64  `json:"expires_unix"`
	SecondsLeft int64  `json:"seconds_left"`
}

// Expiring returns grants expiring within the window (default 86400 s).
func (c *ConsentLedger) Expiring(within time.Duration) []ConsentExpiringRow {
	if within <= 0 {
		within = 24 * time.Hour
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	cutoff := now.Add(within)
	out := make([]ConsentExpiringRow, 0)
	for _, g := range c.grants {
		if g.ExpiresAt.IsZero() || g.ExpiresAt.Before(now) {
			continue
		}
		if g.ExpiresAt.Before(cutoff) {
			out = append(out, ConsentExpiringRow{
				User: g.User, Scope: g.Scope, Purpose: g.Purpose,
				ExpiresUnix: g.ExpiresAt.Unix(),
				SecondsLeft: int64(g.ExpiresAt.Sub(now).Seconds()),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SecondsLeft < out[j].SecondsLeft })
	return out
}

// ConsentStats is the global snapshot.
type ConsentStats struct {
	Grants       int   `json:"grants"`
	TotalGrants  int64 `json:"total_grants"`
	TotalRevokes int64 `json:"total_revokes"`
	TotalChecks  int64 `json:"total_checks"`
	TotalDenials int64 `json:"total_denials"`
}

func (c *ConsentLedger) Stats() ConsentStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ConsentStats{
		Grants:       len(c.grants),
		TotalGrants:  c.totalGrants.Load(),
		TotalRevokes: c.totalRevokes.Load(),
		TotalChecks:  c.totalChecks.Load(),
		TotalDenials: c.totalDenials.Load(),
	}
}
