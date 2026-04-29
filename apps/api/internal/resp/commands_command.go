package resp

import (
	"bufio"
	"sort"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
)

// commandCmd implements COMMAND COUNT/INFO/LIST/DOCS/GETKEYS/HELP.
// We derive metadata from the ACL command registry — the same source
// of truth that scopes ACL category permissions, so adding a new
// command anywhere automatically surfaces in COMMAND.
func (c *conn) commandCmd(args []string) {
	if len(args) == 0 {
		// Bare COMMAND returns every command's metadata array. Real
		// Redis encodes it as nested arrays; we follow the same shape
		// so cluster-aware drivers parse it correctly.
		writeCommandList(c.bw, acl.AllCommands(), true)
		return
	}
	switch strings.ToUpper(args[0]) {
	case "COUNT":
		writeInt(c.bw, int64(len(acl.AllCommands())))
	case "LIST":
		// Optional FILTERBY MODULE name / ACLCAT cat clauses. We honour
		// ACLCAT; FILTERBY MODULE returns an empty list since modules
		// register through their own surface.
		names := acl.AllCommands()
		if len(args) >= 3 && strings.EqualFold(args[1], "FILTERBY") {
			switch strings.ToUpper(args[2]) {
			case "ACLCAT":
				if len(args) < 4 {
					writeArray(c.bw, []string{})
					return
				}
				names = acl.CommandsInCategory(strings.ToLower(args[3]))
			case "MODULE":
				writeArray(c.bw, []string{})
				return
			case "PATTERN":
				if len(args) < 4 {
					writeArray(c.bw, []string{})
					return
				}
				pat := strings.ToUpper(args[3])
				out := []string{}
				for _, n := range names {
					if matchesPattern(pat, n) {
						out = append(out, n)
					}
				}
				names = out
			}
		}
		sort.Strings(names)
		writeArray(c.bw, names)
	case "INFO":
		// COMMAND INFO with no args returns every command — same as
		// bare COMMAND. With args, only the named ones.
		want := args[1:]
		if len(want) == 0 {
			writeCommandList(c.bw, acl.AllCommands(), true)
			return
		}
		writeCommandList(c.bw, want, false)
	case "DOCS":
		// COMMAND DOCS replies with a flat key/value map per command
		// covering the documentation Redis ships. We surface the bits
		// we know (categories, summary, since, complexity).
		want := args[1:]
		if len(want) == 0 {
			want = acl.AllCommands()
		}
		writeCommandDocs(c.bw, want)
	case "GETKEYS":
		// COMMAND GETKEYS <cmd> <args ...> — extract the keys touched
		// by a command without executing it. Reuses our ACL keysFor
		// helper so the answer matches what the dispatcher actually
		// gates against.
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'command|getkeys'")
			return
		}
		keys := keysForCommand(strings.ToUpper(args[1]), args[2:])
		writeArray(c.bw, keys)
	case "HELP":
		writeArray(c.bw, []string{
			"COMMAND",
			"COMMAND COUNT",
			"COMMAND DOCS [name [name ...]]",
			"COMMAND INFO [name [name ...]]",
			"COMMAND LIST [FILTERBY MODULE name|ACLCAT cat|PATTERN pat]",
			"COMMAND GETKEYS cmd [arg ...]",
		})
	default:
		writeError(c.bw, "Unknown COMMAND subcommand "+args[0])
	}
}

// writeCommandList emits Redis's canonical 6-element command-info
// array for each named command. Fields:
//
//   [name, arity, flags, first-key, last-key, key-step]
//
// where arity is 1 for the single command name + variadic args and
// flags is the ACL category set as RESP simple strings.
func writeCommandList(w *bufWriter, names []string, sortNames bool) {
	if sortNames {
		sort.Strings(names)
	}
	header(w, len(names))
	for _, name := range names {
		cats := acl.CategoriesFor(name)
		flags := make([]any, 0, len(cats))
		for _, c := range cats {
			flags = append(flags, "@"+c)
		}
		first, last, step := commandKeySpec(name)
		header(w, 6)
		writeBulk(w, name)
		writeInt(w, -1) // unknown arity (we don't track it precisely)
		writeValue(w, flags)
		writeInt(w, int64(first))
		writeInt(w, int64(last))
		writeInt(w, int64(step))
	}
}

// writeCommandDocs surfaces COMMAND DOCS metadata. We don't ship a
// per-command summary string — Redis copies these from its docs site.
// We surface what we *do* know (categories, the key spec) so drivers
// that lean on COMMAND DOCS for capability discovery work.
func writeCommandDocs(w *bufWriter, names []string) {
	header(w, len(names)*2)
	for _, name := range names {
		writeBulk(w, name)
		cats := acl.CategoriesFor(name)
		flags := make([]any, 0, len(cats))
		for _, c := range cats {
			flags = append(flags, "@"+c)
		}
		first, last, step := commandKeySpec(name)
		out := []any{
			"summary", "",
			"since", "0.4.0",
			"group", primaryGroup(cats),
			"complexity", "",
			"acl_categories", flags,
			"key_specs", []any{
				[]any{
					"begin_search", []any{"type", "index", "spec", []any{"index", int64(first)}},
					"find_keys", []any{"type", "range", "spec", []any{"lastkey", int64(last - first), "keystep", int64(step), "limit", int64(0)}},
				},
			},
		}
		writeValue(w, out)
	}
}

// commandKeySpec resolves the first/last/step indices for a command —
// matches what the cluster slot router uses, so the answers stay
// consistent.
func commandKeySpec(name string) (first, last, step int) {
	switch name {
	case "MSET", "MSETNX":
		return 1, -1, 2
	case "MGET", "DEL", "UNLINK", "EXISTS", "WATCH", "TYPE", "OBJECT", "DUMP", "PFCOUNT":
		return 1, -1, 1
	case "RENAME", "RENAMENX", "COPY", "RPOPLPUSH", "BLMOVE", "SMOVE":
		return 1, 2, 1
	case "BITOP":
		return 2, -1, 1
	case "ZADD", "XADD", "GEOADD", "PFADD", "PFMERGE", "SADD", "SREM", "HSET", "HDEL", "LPUSH", "RPUSH":
		return 1, 1, 1
	case "BLPOP", "BRPOP", "BZPOPMIN", "BZPOPMAX":
		return 1, -2, 1
	case "MIGRATE", "XREAD", "XREADGROUP", "WAIT", "WAITAOF":
		return 0, 0, 0
	}
	if name == "" {
		return 0, 0, 0
	}
	return 1, 1, 1
}

func primaryGroup(cats []string) string {
	for _, c := range cats {
		switch c {
		case acl.CatString, acl.CatList, acl.CatHash, acl.CatSet, acl.CatSortedSet,
			acl.CatStream, acl.CatBitmap, acl.CatHyperLogLog, acl.CatGeo,
			acl.CatPubSub, acl.CatTransaction, acl.CatScripting, acl.CatAdmin,
			acl.CatConnection, acl.CatAI:
			return c
		}
	}
	return "generic"
}

// header writes a RESP array header — exposed here as a small helper
// so writeCommandList/Docs read cleanly.
func header(w *bufio.Writer, n int) {
	_, _ = w.WriteString("*")
	_, _ = w.WriteString(itoa(n))
	_, _ = w.WriteString("\r\n")
}

// bufWriter aliases the bufio.Writer used by the rest of the package.
type bufWriter = bufio.Writer

// silence unused-acl import warnings in trimmed builds.
var _ = acl.CatRead
