package config

import (
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Runtime is the mutable mirror of Config that CONFIG GET/SET reads
// and writes. The boot-time Config struct is the seed; every field
// the operator can change at runtime is registered with a getter +
// setter and surfaced through CONFIG GET <pattern>.
//
// Boot-only fields (RESPPort, ClusterEnabled, TLS paths, …) are
// excluded by design — those require a process restart and CONFIG SET
// returning OK on them would be misleading.
type Runtime struct {
	mu sync.RWMutex
	c  *Config

	// pendingRewrite tracks fields that diverged from the on-disk file
	// so CONFIG REWRITE knows what to flush. We don't ship a config
	// file, but we keep the marker for future use.
	pendingRewrite map[string]bool
}

// NewRuntime wraps a Config so CONFIG SET can mutate it.
func NewRuntime(c *Config) *Runtime {
	return &Runtime{c: c, pendingRewrite: map[string]bool{}}
}

// Get returns key=value pairs matching the glob (Redis convention:
// `*`, `?`, character classes are NOT supported — Redis itself only
// honours `*`/`?` in CONFIG GET, and most tools call it with literal
// names).
func (r *Runtime) Get(pattern string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []string{}
	for name, getter := range r.knobs() {
		if !configMatches(pattern, name) {
			continue
		}
		out = append(out, name, getter())
	}
	return out
}

// Set updates one knob. Returns an error when the knob is unknown,
// the value is malformed, or the field is boot-only.
func (r *Runtime) Set(name, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	knob, ok := r.knobs()[strings.ToLower(name)]
	if !ok {
		return errors.New("Unsupported CONFIG parameter: " + name)
	}
	_ = knob
	setter, ok := r.setters()[strings.ToLower(name)]
	if !ok {
		return errors.New("CONFIG parameter '" + name + "' is read-only at runtime")
	}
	if err := setter(value); err != nil {
		return err
	}
	r.pendingRewrite[strings.ToLower(name)] = true
	return nil
}

// ResetStat zeroes per-process counters that CONFIG RESETSTAT clears.
// Caller is responsible for calling into engine.Metrics; this just
// echoes the fields it knows about.
func (r *Runtime) ResetStat() {
	// nothing to do at the config layer; engine wires this to its
	// metrics + slowlog reset paths.
}

// Rewrite is a no-op stub today (we don't persist runtime changes
// since the engine is env-var driven) but kept so REWRITE replies OK
// and the operator can switch to a config file in the future.
func (r *Runtime) Rewrite() error { return nil }

// knobs returns name -> getter mapping for every runtime-visible knob.
// Adding a new knob means adding both a getter here and a setter in
// setters() — keeps the two halves in sync.
func (r *Runtime) knobs() map[string]func() string {
	c := r.c
	return map[string]func() string{
		"maxmemory":                func() string { return strconv.Itoa(c.MaxMemoryMB) + "mb" },
		"maxmemory-policy":         func() string { return c.Eviction },
		"requirepass":              func() string { return mask(c.RequirePass) },
		"protected-mode":           func() string { return boolStr(c.ProtectedMode) },
		"slowlog-log-slower-than":  func() string { return strconv.FormatInt(c.SlowLogThreshold, 10) },
		"slowlog-max-len":          func() string { return strconv.Itoa(c.SlowLogMaxLen) },
		"latency-monitor-len":      func() string { return strconv.Itoa(c.LatencyMaxLen) },
		"timeout":                  func() string { return strconv.Itoa(c.ClientIdleMax) },
		"appendonly":               func() string { return boolStr(c.AOFEnabled) },
		"appendfsync":              func() string { return c.AOFFsync },
		"loglevel":                 func() string { return c.LogLevel },
		"client-output-buffer-limit": func() string { return "" },
		"sem-threshold":            func() string { return strconv.FormatFloat(c.SemThreshold, 'g', -1, 64) },
		"script-timeout-ms":        func() string { return strconv.Itoa(c.ScriptTimeoutMs) },
	}
}

// setters returns the runtime-mutable subset. Boot-only fields
// (ports, cluster bus, TLS paths, replicaof) are deliberately absent.
func (r *Runtime) setters() map[string]func(string) error {
	c := r.c
	return map[string]func(string) error{
		"maxmemory": func(v string) error {
			c.MaxMemoryMB = parseMemoryMB(v)
			return nil
		},
		"maxmemory-policy": func(v string) error {
			c.Eviction = v
			return nil
		},
		"requirepass": func(v string) error {
			c.RequirePass = v
			return nil
		},
		"protected-mode": func(v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			c.ProtectedMode = b
			return nil
		},
		"slowlog-log-slower-than": func(v string) error {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return err
			}
			c.SlowLogThreshold = n
			return nil
		},
		"slowlog-max-len": func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			c.SlowLogMaxLen = n
			return nil
		},
		"latency-monitor-len": func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			c.LatencyMaxLen = n
			return nil
		},
		"timeout": func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			c.ClientIdleMax = n
			return nil
		},
		"appendfsync": func(v string) error {
			v = strings.ToLower(v)
			if v != "always" && v != "everysec" && v != "no" {
				return errors.New("appendfsync must be always|everysec|no")
			}
			c.AOFFsync = v
			return nil
		},
		"loglevel": func(v string) error {
			c.LogLevel = v
			return nil
		},
		"sem-threshold": func(v string) error {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return err
			}
			c.SemThreshold = f
			return nil
		},
		"script-timeout-ms": func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			c.ScriptTimeoutMs = n
			return nil
		},
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return "<set>"
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	}
	return false, errors.New("expected yes/no")
}

func configMatches(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// minimal glob — `*` only, sufficient for `CONFIG GET maxmemory*`
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, s)
	}
	parts := strings.Split(strings.ToLower(pattern), "*")
	low := strings.ToLower(s)
	pos := 0
	for i, p := range parts {
		if p == "" {
			continue
		}
		j := strings.Index(low[pos:], p)
		if j < 0 {
			return false
		}
		if i == 0 && j != 0 && !strings.HasPrefix(pattern, "*") {
			return false
		}
		pos += j + len(p)
	}
	if !strings.HasSuffix(pattern, "*") && pos != len(low) {
		return false
	}
	return true
}
