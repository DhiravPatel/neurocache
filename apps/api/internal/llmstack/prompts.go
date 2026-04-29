package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// PromptVersion is one snapshot of a named template. We keep every
// version so an operator can roll back from v4 → v3 without redeploy.
type PromptVersion struct {
	Version   int       `json:"version"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Prompts is the registry of named templates.
type Prompts struct {
	mu sync.RWMutex
	by map[string][]PromptVersion // name → versions sorted ascending
}

// NewPrompts returns an empty registry.
func NewPrompts() *Prompts { return &Prompts{by: map[string][]PromptVersion{}} }

// Set stores a new version of `name`. If `version` is 0, we auto-assign
// (latest + 1). Returns the assigned version.
//
// If a caller passes an explicit version that already exists, we
// overwrite — this is the documented way to fix a typo without forking
// version numbers.
func (p *Prompts) Set(name string, version int, body string) (int, error) {
	if name == "" {
		return 0, errors.New("ERR template name is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	versions := p.by[name]
	if version <= 0 {
		version = 1
		for _, v := range versions {
			if v.Version >= version {
				version = v.Version + 1
			}
		}
	}
	for i, v := range versions {
		if v.Version == version {
			versions[i] = PromptVersion{Version: version, Body: body, CreatedAt: time.Now()}
			p.by[name] = versions
			return version, nil
		}
	}
	versions = append(versions, PromptVersion{Version: version, Body: body, CreatedAt: time.Now()})
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	p.by[name] = versions
	return version, nil
}

// Get returns the body for (name, version). version <= 0 means "latest".
func (p *Prompts) Get(name string, version int) (PromptVersion, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	versions, ok := p.by[name]
	if !ok || len(versions) == 0 {
		return PromptVersion{}, false
	}
	if version <= 0 {
		return versions[len(versions)-1], true
	}
	for _, v := range versions {
		if v.Version == version {
			return v, true
		}
	}
	return PromptVersion{}, false
}

// Render fetches the requested version (latest when version <= 0) and
// substitutes every `{var}` placeholder using the supplied vars map.
// Unknown placeholders are left intact so rendering errors are visible
// to humans rather than silently dropped.
func (p *Prompts) Render(name string, version int, vars map[string]string) (string, error) {
	pv, ok := p.Get(name, version)
	if !ok {
		return "", errors.New("ERR no such template")
	}
	out := pv.Body
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out, nil
}

// Delete removes (name, version). version <= 0 deletes the entire
// template (every version). Returns the number of versions removed.
func (p *Prompts) Delete(name string, version int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	versions, ok := p.by[name]
	if !ok {
		return 0
	}
	if version <= 0 {
		delete(p.by, name)
		return len(versions)
	}
	out := versions[:0]
	removed := 0
	for _, v := range versions {
		if v.Version == version {
			removed++
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		delete(p.by, name)
	} else {
		p.by[name] = out
	}
	return removed
}

// List returns every template name with its highest version (sorted).
type PromptListing struct {
	Name          string `json:"name"`
	LatestVersion int    `json:"latest_version"`
	Versions      int    `json:"versions"`
}

func (p *Prompts) List() []PromptListing {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PromptListing, 0, len(p.by))
	for name, versions := range p.by {
		latest := 0
		if len(versions) > 0 {
			latest = versions[len(versions)-1].Version
		}
		out = append(out, PromptListing{
			Name:          name,
			LatestVersion: latest,
			Versions:      len(versions),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Versions returns every stored version of `name` (ascending).
func (p *Prompts) Versions(name string) []PromptVersion {
	p.mu.RLock()
	defer p.mu.RUnlock()
	versions, ok := p.by[name]
	if !ok {
		return nil
	}
	out := make([]PromptVersion, len(versions))
	copy(out, versions)
	return out
}

// Size returns the number of registered template names.
func (p *Prompts) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.by)
}
