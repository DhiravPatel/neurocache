package aiops

import (
	"hash/fnv"
	"math"
	"sync"
	"time"
)

// Flags is feature-flag state with progressive rollout. Where AB.* is
// for *measuring* outcomes across variants, FLAG.* is for *gating*
// access to features per user with a percentage rollout, allow lists,
// and deny lists. Same hashing as AB.* so a user's bucket is stable
// across reconnects.
//
// A flag evaluates in this priority order:
//   1. Deny list — return false
//   2. Allow list — return true
//   3. Percentage rollout — hash(user) < percentage
//   4. Default state (on/off)
type Flags struct {
	mu    sync.RWMutex
	flags map[string]*flag
}

type flag struct {
	name       string
	on         bool      // global on/off (when 0% rollout, this is the default)
	percentage int       // 0–100, rollout percentage
	allow      map[string]bool
	deny       map[string]bool
	createdAt  time.Time
	updatedAt  time.Time
	evals      int64 // total evaluations
	enabled    int64 // count returning true
}

// NewFlags returns an empty manager.
func NewFlags() *Flags { return &Flags{flags: map[string]*flag{}} }

// Set configures a flag's default state, percentage, allow/deny.
// Empty allow/deny lists clear the lists; pass nil to leave them.
func (f *Flags) Set(name string, on bool, percentage int, allow, deny []string) {
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.flags[name]
	if !ok {
		fl = &flag{
			name:      name,
			allow:     map[string]bool{},
			deny:      map[string]bool{},
			createdAt: time.Now(),
		}
		f.flags[name] = fl
	}
	fl.on = on
	fl.percentage = percentage
	if allow != nil {
		fl.allow = map[string]bool{}
		for _, u := range allow {
			fl.allow[u] = true
		}
	}
	if deny != nil {
		fl.deny = map[string]bool{}
		for _, u := range deny {
			fl.deny[u] = true
		}
	}
	fl.updatedAt = time.Now()
}

// Allow appends a user to the allow list.
func (f *Flags) Allow(name string, user string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.flags[name]
	if !ok {
		return false
	}
	fl.allow[user] = true
	fl.updatedAt = time.Now()
	return true
}

// Deny appends a user to the deny list.
func (f *Flags) Deny(name string, user string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.flags[name]
	if !ok {
		return false
	}
	fl.deny[user] = true
	fl.updatedAt = time.Now()
	return true
}

// Is evaluates the flag for a user.
func (f *Flags) Is(name, user string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.flags[name]
	if !ok {
		return false
	}
	fl.evals++
	allow := f.eval(fl, user)
	if allow {
		fl.enabled++
	}
	return allow
}

func (f *Flags) eval(fl *flag, user string) bool {
	if fl.deny[user] {
		return false
	}
	if fl.allow[user] {
		return true
	}
	if fl.percentage > 0 {
		// Stable per-(flag, user) hash → bucket 0..99
		h := fnv.New64a()
		_, _ = h.Write([]byte(fl.name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(user))
		bucket := int(float64(h.Sum64()) / float64(math.MaxUint64) * 100)
		if bucket < fl.percentage {
			return true
		}
	}
	return fl.on
}

// FlagState is the outward-facing snapshot.
type FlagState struct {
	Name       string    `json:"name"`
	On         bool      `json:"on"`
	Percentage int       `json:"percentage"`
	Allow      []string  `json:"allow,omitempty"`
	Deny       []string  `json:"deny,omitempty"`
	Evals      int64     `json:"evals"`
	Enabled    int64     `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Get returns a flag's full state.
func (f *Flags) Get(name string) (FlagState, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	fl, ok := f.flags[name]
	if !ok {
		return FlagState{}, false
	}
	return FlagState{
		Name:       fl.name,
		On:         fl.on,
		Percentage: fl.percentage,
		Allow:      mapKeys(fl.allow),
		Deny:       mapKeys(fl.deny),
		Evals:      fl.evals,
		Enabled:    fl.enabled,
		CreatedAt:  fl.createdAt,
		UpdatedAt:  fl.updatedAt,
	}, true
}

// List returns every flag name.
func (f *Flags) List() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0, len(f.flags))
	for k := range f.flags {
		out = append(out, k)
	}
	return out
}

// Delete removes a flag.
func (f *Flags) Delete(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.flags[name]
	delete(f.flags, name)
	return ok
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
