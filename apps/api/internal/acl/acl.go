package acl

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager is the central ACL registry. Concurrent-safe — every public
// method takes the appropriate lock. The connection layer holds a *User
// pointer per session and consults Allowed() on each command.
type Manager struct {
	log *slog.Logger

	mu    sync.RWMutex
	users map[string]*User // keyed by lowercase username
	path  string           // file we persist users.acl to (empty = no persistence)

	logMu sync.Mutex
	audit []AuditEntry
}

// User is one ACL principal. Permissions are evaluated as:
//
//  1. command set: explicitly granted (allowedCmds) or via category
//     (allowedCats), minus explicit denies (deniedCmds / deniedCats).
//     A "+@all" or "allcommands" yields wildcard permission.
//  2. key patterns: at least one keyPatterns entry must glob-match
//     every key the command touches. "~*" or "allkeys" is wildcard.
//  3. channel patterns: same idea, for SUBSCRIBE / PUBLISH targets.
type User struct {
	Name        string
	Enabled     bool
	NoPass      bool       // accept any password (or no password)
	Passwords   []string   // sha256 hex digests; never plain text
	AllCommands bool       // +@all
	AllowedCmds map[string]bool
	DeniedCmds  map[string]bool
	AllowedCats map[string]bool
	DeniedCats  map[string]bool
	AllKeys     bool
	KeyPatterns []string
	AllChannels bool
	ChannelPatterns []string
	CreatedAt time.Time
}

// AllowsEverything reports whether this user has unconstrained access:
// every command, every key, every channel, no deny entries. The default
// user (un-customized Redis ACL) hits this — and it's the common case
// for development + simple production setups. The dispatcher uses this
// to skip the entire Allowed() path on the hot road, eliminating
// CategoriesFor + map lookups + slice scans + audit-log mu.RLock.
//
// Recomputed callers should hold the manager lock for read; we expose
// a snapshot rather than a cached bool because users mutate from
// ACL SETUSER and we'd have to invalidate the cache on every edit.
func (u *User) AllowsEverything() bool {
	if u == nil || !u.Enabled {
		return false
	}
	return u.AllCommands && u.AllKeys && u.AllChannels &&
		len(u.DeniedCmds) == 0 && len(u.DeniedCats) == 0
}

// AuditEntry is one rejected-auth or rejected-permission event for ACL LOG.
type AuditEntry struct {
	Count       int
	Reason      string // "auth-fail" | "command-denied" | "key-denied" | "channel-denied"
	Context     string // free-form: command + culprit
	Object      string
	Username    string
	AgeSeconds  float64
	ClientInfo  string
	EntryID     int
	Timestamp   time.Time
}

// NewManager bootstraps an empty registry seeded with the "default"
// user (nopass, all commands, all keys, all channels) — Redis behaviour
// before any other user is configured.
func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{log: log, users: map[string]*User{}}
	m.users["default"] = &User{
		Name: "default", Enabled: true, NoPass: true,
		AllCommands: true, AllowedCmds: map[string]bool{},
		DeniedCmds: map[string]bool{}, AllowedCats: map[string]bool{},
		DeniedCats: map[string]bool{}, AllKeys: true, AllChannels: true,
		CreatedAt: time.Now(),
	}
	return m
}

// ResolvePath picks the on-disk users.acl file. Explicit cfg wins; else
// fall back to <DataDir>/users.acl. Empty when both are unset → manager
// runs in-memory only.
func ResolvePath(cfgPath, dataDir string) string {
	if cfgPath != "" {
		return cfgPath
	}
	if dataDir != "" {
		return filepath.Join(dataDir, "users.acl")
	}
	return ""
}

// SetRequirePass downgrades the legacy `requirepass` config into an ACL
// rule on the default user: removes nopass and registers the password.
func (m *Manager) SetRequirePass(pass string) {
	if pass == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users["default"]
	if u == nil {
		return
	}
	u.NoPass = false
	u.Passwords = []string{hashPassword(pass)}
}

// hashPassword returns the lowercase-hex sha256 of pw — same convention
// Redis uses (`>password` rules accept the plaintext, `#hexhash` rules
// take the digest directly).
func hashPassword(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// Authenticate returns the resolved user when (username, password)
// matches, else (nil, error). An empty username is treated as "default"
// so legacy `AUTH <password>` keeps working.
func (m *Manager) Authenticate(username, password string) (*User, error) {
	if username == "" {
		username = "default"
	}
	m.mu.RLock()
	u, ok := m.users[strings.ToLower(username)]
	m.mu.RUnlock()
	if !ok {
		m.audited("auth-fail", "AUTH", username, "")
		return nil, errors.New("WRONGPASS invalid username-password pair or user is disabled")
	}
	if !u.Enabled {
		m.audited("auth-fail", "AUTH", username, "")
		return nil, errors.New("WRONGPASS invalid username-password pair or user is disabled")
	}
	if u.NoPass {
		return u, nil
	}
	hash := hashPassword(password)
	for _, h := range u.Passwords {
		if h == hash {
			return u, nil
		}
	}
	m.audited("auth-fail", "AUTH", username, "")
	return nil, errors.New("WRONGPASS invalid username-password pair or user is disabled")
}

// DefaultUser returns the always-present "default" user — handed to
// new connections before any AUTH so unauthenticated reads/writes work
// as long as the default user permits them (nopass + allkeys by default).
func (m *Manager) DefaultUser() *User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.users["default"]
}

// HasUser checks for existence (case-insensitive).
func (m *Manager) HasUser(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.users[strings.ToLower(name)]
	return ok
}

// Get returns the user by name (case-insensitive), or nil.
func (m *Manager) Get(name string) *User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.users[strings.ToLower(name)]
}

// List returns user names sorted for stable output.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.users))
	for name := range m.users {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// Delete removes one or more users (the default user is protected).
// Returns the number actually removed.
func (m *Manager) Delete(names ...string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, name := range names {
		key := strings.ToLower(name)
		if key == "default" {
			continue
		}
		if _, ok := m.users[key]; ok {
			delete(m.users, key)
			n++
		}
	}
	return n
}

// SetUser applies a sequence of Redis-style ACL rules to a user, creating
// the user if missing. Rule grammar (matching Redis where applicable):
//
//	on | off                  enable / disable
//	nopass                    accept any password
//	resetpass                 clear all passwords
//	resetkeys / resetchannels clear key / channel patterns
//	reset                     wipe everything (turns user into a fresh one)
//	>pw                       add a plaintext password (hashed)
//	<pw                       remove a plaintext password
//	#hex                      add an already-hashed password
//	!hex                      remove an already-hashed password
//	+CMD / -CMD               grant / revoke a single command
//	+@cat / -@cat             grant / revoke an entire category
//	allcommands / nocommands  +@all / clear allowedCmds and AllCommands
//	~pattern                  add a key pattern (~* = allkeys)
//	allkeys / resetkeys       wildcard / clear key patterns
//	&pattern                  add a pub/sub channel pattern
//	allchannels               wildcard channel access
func (m *Manager) SetUser(name string, rules []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(name)
	u, ok := m.users[key]
	if !ok {
		u = &User{
			Name: name, Enabled: true,
			AllowedCmds: map[string]bool{}, DeniedCmds: map[string]bool{},
			AllowedCats: map[string]bool{}, DeniedCats: map[string]bool{},
			CreatedAt: time.Now(),
		}
		m.users[key] = u
	}
	for _, raw := range rules {
		if err := applyRule(u, raw); err != nil {
			return err
		}
	}
	return nil
}

// applyRule mutates u in place; returns an error for malformed rules so
// the caller can reject the whole SETUSER atomically.
func applyRule(u *User, raw string) error {
	if raw == "" {
		return nil
	}
	switch {
	case raw == "on":
		u.Enabled = true
	case raw == "off":
		u.Enabled = false
	case raw == "nopass":
		u.NoPass = true
		u.Passwords = nil
	case raw == "resetpass":
		u.NoPass = false
		u.Passwords = nil
	case raw == "resetkeys":
		u.AllKeys = false
		u.KeyPatterns = nil
	case raw == "resetchannels":
		u.AllChannels = false
		u.ChannelPatterns = nil
	case raw == "reset":
		u.Enabled = true
		u.NoPass = false
		u.Passwords = nil
		u.AllCommands = false
		u.AllowedCmds = map[string]bool{}
		u.DeniedCmds = map[string]bool{}
		u.AllowedCats = map[string]bool{}
		u.DeniedCats = map[string]bool{}
		u.AllKeys = false
		u.KeyPatterns = nil
		u.AllChannels = false
		u.ChannelPatterns = nil
	case raw == "allcommands" || raw == "+@all":
		u.AllCommands = true
	case raw == "nocommands" || raw == "-@all":
		u.AllCommands = false
		u.AllowedCmds = map[string]bool{}
		u.AllowedCats = map[string]bool{}
	case raw == "allkeys" || raw == "~*":
		u.AllKeys = true
	case raw == "allchannels" || raw == "&*":
		u.AllChannels = true
	case strings.HasPrefix(raw, ">"):
		u.NoPass = false
		u.Passwords = appendUnique(u.Passwords, hashPassword(raw[1:]))
	case strings.HasPrefix(raw, "<"):
		u.Passwords = removeStr(u.Passwords, hashPassword(raw[1:]))
	case strings.HasPrefix(raw, "#"):
		u.NoPass = false
		u.Passwords = appendUnique(u.Passwords, strings.ToLower(raw[1:]))
	case strings.HasPrefix(raw, "!"):
		u.Passwords = removeStr(u.Passwords, strings.ToLower(raw[1:]))
	case strings.HasPrefix(raw, "+@"):
		u.AllowedCats[strings.ToLower(raw[2:])] = true
	case strings.HasPrefix(raw, "-@"):
		u.DeniedCats[strings.ToLower(raw[2:])] = true
		delete(u.AllowedCats, strings.ToLower(raw[2:]))
	case strings.HasPrefix(raw, "+"):
		cmd := strings.ToUpper(raw[1:])
		u.AllowedCmds[cmd] = true
		delete(u.DeniedCmds, cmd)
	case strings.HasPrefix(raw, "-"):
		cmd := strings.ToUpper(raw[1:])
		u.DeniedCmds[cmd] = true
		delete(u.AllowedCmds, cmd)
	case strings.HasPrefix(raw, "~"):
		pat := raw[1:]
		if pat == "*" {
			u.AllKeys = true
		} else {
			u.KeyPatterns = appendUnique(u.KeyPatterns, pat)
		}
	case strings.HasPrefix(raw, "&"):
		pat := raw[1:]
		if pat == "*" {
			u.AllChannels = true
		} else {
			u.ChannelPatterns = appendUnique(u.ChannelPatterns, pat)
		}
	default:
		return fmt.Errorf("Syntax error in ACL SETUSER rule: %s", raw)
	}
	return nil
}

// Allowed checks command + key permissions. Empty keys means "no key
// args"; the caller passes whatever it has parsed. Returns nil on
// success and a typed error otherwise so the dispatcher can format the
// canonical NOPERM reply.
func (m *Manager) Allowed(u *User, cmd string, keys, channels []string) error {
	if u == nil {
		return errors.New("NOPERM no user authenticated")
	}
	if !u.Enabled {
		return errors.New("NOPERM user is disabled")
	}
	cmd = strings.ToUpper(cmd)
	if u.DeniedCmds[cmd] {
		m.audited("command-denied", cmd, u.Name, cmd)
		return fmt.Errorf("NOPERM this user has no permissions to run the '%s' command", strings.ToLower(cmd))
	}
	cats := CategoriesFor(cmd)
	for _, c := range cats {
		if u.DeniedCats[c] {
			m.audited("command-denied", cmd, u.Name, "@"+c)
			return fmt.Errorf("NOPERM this user has no permissions to run the '%s' command", strings.ToLower(cmd))
		}
	}
	allowed := u.AllCommands || u.AllowedCmds[cmd]
	if !allowed {
		for _, c := range cats {
			if u.AllowedCats[c] {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		m.audited("command-denied", cmd, u.Name, cmd)
		return fmt.Errorf("NOPERM this user has no permissions to run the '%s' command", strings.ToLower(cmd))
	}
	if !u.AllKeys && len(keys) > 0 {
		for _, k := range keys {
			if !matchesAny(u.KeyPatterns, k) {
				m.audited("key-denied", cmd, u.Name, k)
				return fmt.Errorf("NOPERM this user has no permissions to access one of the keys used as arguments")
			}
		}
	}
	if !u.AllChannels && len(channels) > 0 {
		for _, ch := range channels {
			if !matchesAny(u.ChannelPatterns, ch) {
				m.audited("channel-denied", cmd, u.Name, ch)
				return fmt.Errorf("NOPERM this user has no permissions to access one of the channels used as arguments")
			}
		}
	}
	return nil
}

// audited records an entry in the in-memory audit log (capped to 128).
func (m *Manager) audited(reason, ctx, user, object string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	id := len(m.audit) + 1
	// dedupe: if the previous entry matches reason/object/user, bump count.
	if n := len(m.audit); n > 0 {
		prev := &m.audit[n-1]
		if prev.Reason == reason && prev.Object == object && prev.Username == user && prev.Context == ctx {
			prev.Count++
			prev.Timestamp = time.Now()
			prev.AgeSeconds = 0
			return
		}
	}
	m.audit = append(m.audit, AuditEntry{
		Count: 1, Reason: reason, Context: ctx, Object: object,
		Username: user, EntryID: id, Timestamp: time.Now(),
	})
	if len(m.audit) > 128 {
		m.audit = m.audit[len(m.audit)-128:]
	}
}

// Log returns up to count audit entries (most recent first). count <= 0
// returns the full buffer.
func (m *Manager) Log(count int) []AuditEntry {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	out := make([]AuditEntry, len(m.audit))
	now := time.Now()
	for i, e := range m.audit {
		e.AgeSeconds = now.Sub(e.Timestamp).Seconds()
		out[len(m.audit)-1-i] = e
	}
	if count > 0 && count < len(out) {
		out = out[:count]
	}
	return out
}

// LogReset wipes the audit log.
func (m *Manager) LogReset() {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	m.audit = nil
}

// Describe returns a Redis-style flag list that ACL GETUSER renders
// alongside the password/key/channel arrays.
func (u *User) Describe() []string {
	out := []string{}
	if u.Enabled {
		out = append(out, "on")
	} else {
		out = append(out, "off")
	}
	if u.NoPass {
		out = append(out, "nopass")
	}
	if u.AllCommands {
		out = append(out, "allcommands")
	}
	if u.AllKeys {
		out = append(out, "allkeys")
	}
	if u.AllChannels {
		out = append(out, "allchannels")
	}
	return out
}

// Hashes returns the password digests in stable order.
func (u *User) Hashes() []string {
	out := append([]string{}, u.Passwords...)
	sortStrings(out)
	return out
}

// matchesAny is a tiny glob matcher (supports * ? [abc]). Exposed inside
// the package so command handlers can pre-check pub/sub patterns.
func matchesAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if globMatch(p, s) {
			return true
		}
	}
	return false
}

func globMatch(pattern, s string) bool {
	return matchRunes([]rune(pattern), []rune(s))
}

func matchRunes(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchRunes(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func removeStr(xs []string, v string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
