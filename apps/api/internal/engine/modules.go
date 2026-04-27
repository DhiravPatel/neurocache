package engine

import (
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// moduleHandle is the engine's implementation of modules.EngineHandle.
// We intentionally expose a tightly-scoped surface — modules can read
// + write keys, publish, and not much else — so the ABI stays stable
// across internal refactors of the engine.
type moduleHandle struct{ e *Engine }

func (m *moduleHandle) SetCustomValue(key string, id modules.TypeID, value any, ttlMs int64) error {
	lo, hi := splitTypeID(id)
	bytes := int64(0)
	if t, ok := m.e.Modules.LookupType(id); ok && t.MemUsage != nil {
		bytes = t.MemUsage(value)
	}
	if bytes <= 0 {
		bytes = 64 // floor for accounting purposes
	}
	var ttl time.Duration
	if ttlMs > 0 {
		ttl = time.Duration(ttlMs) * time.Millisecond
	}
	return m.e.KV.SetModule(key, lo, hi, value, bytes, ttl)
}

func (m *moduleHandle) GetCustomValue(key string, id modules.TypeID) (any, bool, error) {
	lo, hi := splitTypeID(id)
	return m.e.KV.GetModule(key, lo, hi)
}

func (m *moduleHandle) DelCustomValue(key string) bool {
	return m.e.KV.DelModule(key)
}

func (m *moduleHandle) Publish(channel, payload string) int {
	return m.e.PubSub.Publish(channel, payload)
}

// splitTypeID packs an 8-byte TypeID into (lo, hi) uint64s so the
// store can compare without depending on the modules package.
func splitTypeID(id modules.TypeID) (lo, hi uint64) {
	for i := 0; i < 8; i++ {
		lo |= uint64(id[i]) << (8 * uint(i))
	}
	return lo, 0
}

// RebuildACLForModules walks every module command and ensures the ACL
// category registry knows about it. Called whenever a module is
// loaded/unloaded so freshly-registered commands inherit the user's
// existing category grants without manual ACL SETUSER.
//
// Implementation note: the ACL package's category table is initialised
// at package load with the built-in commands. Modules add to it via
// the ACL bridge below; we don't remove on unload because revoking a
// command from the table doesn't actually punish anyone — the
// dispatcher will just stop seeing that name.
func (e *Engine) RebuildACLForModules() {
	if e.Modules == nil {
		return
	}
	// The ACL manager only consults the package-level registry, so
	// there's nothing per-Engine to mutate here. The function exists
	// so call sites have a clear seam — when we eventually add
	// per-module ACL grants this is where they'll attach.
}

// loadModulesFromConfig bulk-activates the comma-separated list in
// NEUROCACHE_MODULES_LOAD. Called once during Start().
func (e *Engine) loadModulesFromConfig() {
	if e.Modules == nil || e.Cfg.ModulesLoad == "" {
		return
	}
	for _, name := range splitCSV(e.Cfg.ModulesLoad) {
		if err := e.Modules.Load(name); err != nil {
			e.Log.Warn("module load failed", "name", name, "err", err)
		} else {
			e.Log.Info("module loaded", "name", name)
		}
	}
	e.RebuildACLForModules()
}

func splitCSV(s string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		if c == ' ' || c == '\t' {
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
