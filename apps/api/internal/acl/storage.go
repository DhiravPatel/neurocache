package acl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LoadFile parses a Redis-format users.acl file. Lines look like:
//
//	user alice on >secret ~cache:* +@read +@write
//
// Blank lines and `#`-prefixed comments are ignored. The default user
// is preserved unless the file explicitly redefines it.
func (m *Manager) LoadFile(path string) error {
	if path == "" {
		return nil
	}
	m.mu.Lock()
	m.path = path
	m.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	lineNo := 0
	for {
		line, err := br.ReadString('\n')
		lineNo++
		trim := strings.TrimSpace(line)
		if trim != "" && !strings.HasPrefix(trim, "#") {
			tokens := strings.Fields(trim)
			if len(tokens) >= 2 && strings.EqualFold(tokens[0], "user") {
				if e := m.SetUser(tokens[1], tokens[2:]); e != nil {
					return fmt.Errorf("acl line %d: %w", lineNo, e)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// Save writes the current registry back to the configured file. Atomic
// rename keeps a half-written file from being observed.
func (m *Manager) Save() error {
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)

	m.mu.RLock()
	names := make([]string, 0, len(m.users))
	for n := range m.users {
		names = append(names, n)
	}
	m.mu.RUnlock()
	sortStrings(names)

	for _, n := range names {
		m.mu.RLock()
		u := m.users[n]
		m.mu.RUnlock()
		if u == nil {
			continue
		}
		fmt.Fprintln(w, "user "+u.Name+" "+strings.Join(serializeRules(u), " "))
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// serializeRules returns the rule list for a user in the same order
// LoadFile expects, so a save+load round-trip is a no-op.
func serializeRules(u *User) []string {
	out := []string{}
	if u.Enabled {
		out = append(out, "on")
	} else {
		out = append(out, "off")
	}
	if u.NoPass {
		out = append(out, "nopass")
	} else {
		for _, h := range u.Passwords {
			out = append(out, "#"+h)
		}
	}
	if u.AllCommands {
		out = append(out, "+@all")
	}
	for c := range u.AllowedCats {
		out = append(out, "+@"+c)
	}
	for c := range u.DeniedCats {
		out = append(out, "-@"+c)
	}
	for c := range u.AllowedCmds {
		out = append(out, "+"+strings.ToLower(c))
	}
	for c := range u.DeniedCmds {
		out = append(out, "-"+strings.ToLower(c))
	}
	if u.AllKeys {
		out = append(out, "~*")
	} else {
		for _, p := range u.KeyPatterns {
			out = append(out, "~"+p)
		}
	}
	if u.AllChannels {
		out = append(out, "&*")
	} else {
		for _, p := range u.ChannelPatterns {
			out = append(out, "&"+p)
		}
	}
	return out
}
