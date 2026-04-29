package aiops

import (
	"sync"
)

// Personas is multi-persona memory routing. The same user can have a
// "work" persona, a "personal" persona, an "agent" persona — each
// with its own memory namespace. PERSONA.SET binds the active persona
// to a user; downstream MEMORY_QUERY (when persona-aware) returns
// memories tagged with that persona.
//
// We deliberately don't fork the memory store per persona — that would
// duplicate data. Instead each memory entry carries a persona tag, and
// queries filter on it. The active-persona binding here is just the
// "what persona is this user currently in" lookup.
type Personas struct {
	mu     sync.RWMutex
	active map[string]string // user → active persona
	known  map[string]map[string]bool // user → set of personas they've used
}

// NewPersonas returns an empty manager.
func NewPersonas() *Personas {
	return &Personas{
		active: map[string]string{},
		known:  map[string]map[string]bool{},
	}
}

// SetActive binds the user's active persona.
func (p *Personas) SetActive(user, persona string) {
	p.mu.Lock()
	p.active[user] = persona
	if _, ok := p.known[user]; !ok {
		p.known[user] = map[string]bool{}
	}
	p.known[user][persona] = true
	p.mu.Unlock()
}

// Active returns the user's currently bound persona, or "default" when
// none is set.
func (p *Personas) Active(user string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if v, ok := p.active[user]; ok {
		return v
	}
	return "default"
}

// List returns every persona a user has ever activated (sorted is the
// caller's job).
func (p *Personas) List(user string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	set, ok := p.known[user]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// Forget drops every record for a user.
func (p *Personas) Forget(user string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, hadActive := p.active[user]
	_, hadKnown := p.known[user]
	delete(p.active, user)
	delete(p.known, user)
	return hadActive || hadKnown
}
