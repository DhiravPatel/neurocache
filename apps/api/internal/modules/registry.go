package modules

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Registry is the per-engine module table. Loaded modules register
// commands + types here; the dispatcher consults FindCmd on the slow
// path (after the built-in switch misses).
type Registry struct {
	engine EngineHandle

	mu      sync.RWMutex
	loaded  map[string]*loadedModule       // by lowercase module name
	cmds    map[string]*Cmd                // by uppercase command name
	types   map[TypeID]*CustomType
	cmdMod  map[string]string              // command name -> owning module name
}

// loadedModule is one active registration.
type loadedModule struct {
	mod      Module
	commands []Cmd
	types    []CustomType
}

// NewRegistry builds an empty registry bound to an engine handle.
func NewRegistry(eng EngineHandle) *Registry {
	return &Registry{
		engine: eng,
		loaded: map[string]*loadedModule{},
		cmds:   map[string]*Cmd{},
		types:  map[TypeID]*CustomType{},
		cmdMod: map[string]string{},
	}
}

// Engine exposes the bound engine so commands can call back without
// the registry itself becoming a parameter.
func (r *Registry) Engine() EngineHandle { return r.engine }

// Available reports the set of compile-time-linked modules registered
// via RegisterAvailable. Operators load these by name.
func Available() []Module {
	availMu.RLock()
	defer availMu.RUnlock()
	out := make([]Module, 0, len(availableModules))
	for _, m := range availableModules {
		out = append(out, m)
	}
	return out
}

// availableModules is the global pool of modules linked into this
// binary. Populated by package-init calls to RegisterAvailable.
var (
	availMu          sync.RWMutex
	availableModules = map[string]Module{}
)

// RegisterAvailable adds a module to the load-time pool. Call from
// init() in a module package — the engine then sees it via MODULE LOAD.
func RegisterAvailable(m Module) {
	availMu.Lock()
	defer availMu.Unlock()
	availableModules[strings.ToLower(m.Name)] = m
}

// LookupAvailable fetches a module from the available pool.
func LookupAvailable(name string) (Module, bool) {
	availMu.RLock()
	defer availMu.RUnlock()
	m, ok := availableModules[strings.ToLower(name)]
	return m, ok
}

// Load activates a module by name. Returns the loaded record so
// callers can surface metadata in MODULE LIST.
func (r *Registry) Load(name string) error {
	mod, ok := LookupAvailable(name)
	if !ok {
		return fmt.Errorf("module '%s' not available", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, already := r.loaded[strings.ToLower(name)]; already {
		return fmt.Errorf("module '%s' already loaded", name)
	}
	ctx := &RegisterCtx{mod: &mod, registry: r}
	if mod.Init != nil {
		if err := mod.Init(ctx); err != nil {
			return fmt.Errorf("module '%s' init: %w", name, err)
		}
	}
	// Validate before mutating any global state — modules that collide
	// with built-ins or each other refuse to load.
	for i := range ctx.commands {
		c := &ctx.commands[i]
		c.Name = strings.ToUpper(c.Name)
		if existing, ok := r.cmds[c.Name]; ok {
			return fmt.Errorf("module '%s' command '%s' collides with module '%s'",
				name, c.Name, r.cmdMod[existing.Name])
		}
	}
	for _, t := range ctx.types {
		if _, ok := r.types[t.ID]; ok {
			return fmt.Errorf("module '%s' type id %v already registered", name, t.ID)
		}
	}
	// Commit.
	rec := &loadedModule{mod: mod, commands: ctx.commands, types: ctx.types}
	r.loaded[strings.ToLower(name)] = rec
	for i := range rec.commands {
		c := &rec.commands[i]
		r.cmds[c.Name] = c
		r.cmdMod[c.Name] = name
	}
	for i := range rec.types {
		t := &rec.types[i]
		r.types[t.ID] = t
	}
	return nil
}

// Unload deactivates a module. Returns an error when the module isn't
// loaded or when its Shutdown hook errors.
func (r *Registry) Unload(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.loaded[strings.ToLower(name)]
	if !ok {
		return fmt.Errorf("module '%s' not loaded", name)
	}
	if rec.mod.Shutdown != nil {
		if err := rec.mod.Shutdown(); err != nil {
			return fmt.Errorf("module '%s' shutdown: %w", name, err)
		}
	}
	for _, c := range rec.commands {
		delete(r.cmds, c.Name)
		delete(r.cmdMod, c.Name)
	}
	for _, t := range rec.types {
		delete(r.types, t.ID)
	}
	delete(r.loaded, strings.ToLower(name))
	return nil
}

// FindCmd looks up a module-registered command by uppercase name.
func (r *Registry) FindCmd(name string) (*Cmd, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cmds[strings.ToUpper(name)]
	return c, ok
}

// LookupType resolves a TypeID to its handler.
func (r *Registry) LookupType(id TypeID) (*CustomType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.types[id]
	return t, ok
}

// LoadedNames returns the names of currently-loaded modules in stable order.
func (r *Registry) LoadedNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.loaded))
	for n := range r.loaded {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

// LoadedInfo describes one loaded module — surfaced by MODULE LIST.
type LoadedInfo struct {
	Name        string
	Version     string
	Description string
	Commands    []string
	Types       []string
}

// List returns metadata for every loaded module.
func (r *Registry) List() []LoadedInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.loaded))
	for n := range r.loaded {
		names = append(names, n)
	}
	sortStrings(names)
	out := make([]LoadedInfo, 0, len(names))
	for _, n := range names {
		rec := r.loaded[n]
		info := LoadedInfo{
			Name: rec.mod.Name, Version: rec.mod.Version,
			Description: rec.mod.Description,
		}
		for _, c := range rec.commands {
			info.Commands = append(info.Commands, c.Name)
		}
		for _, t := range rec.types {
			info.Types = append(info.Types, t.Name)
		}
		out = append(out, info)
	}
	return out
}

// CommandsForACL returns the (name, categories) tuples ACL needs to
// authorize module commands. Caller is responsible for syncing with
// the ACL registry whenever Load/Unload happens.
func (r *Registry) CommandsForACL() []ACLCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ACLCommand, 0, len(r.cmds))
	for name, c := range r.cmds {
		out = append(out, ACLCommand{Name: name, Categories: c.Categories})
	}
	return out
}

// ACLCommand is a tiny DTO for the ACL bridge.
type ACLCommand struct {
	Name       string
	Categories []string
}

// ShutdownAll stops every loaded module — called from engine shutdown.
func (r *Registry) ShutdownAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for name, rec := range r.loaded {
		if rec.mod.Shutdown != nil {
			if err := rec.mod.Shutdown(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("module '%s' shutdown: %w", name, err)
			}
		}
	}
	r.loaded = map[string]*loadedModule{}
	r.cmds = map[string]*Cmd{}
	r.types = map[TypeID]*CustomType{}
	r.cmdMod = map[string]string{}
	return firstErr
}

// silence unused-import warnings for any future helpers.
var _ = errors.New

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
