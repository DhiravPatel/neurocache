package searchmod

import (
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// FT.ALIASADD alias index — register `alias` as another name for `index`.
// Errors if alias is already taken or the target index doesn't exist.
func ftAliasAdd(c *modules.Ctx, args []string) error {
	if len(args) != 2 {
		c.Reply.Error("FT.ALIASADD alias index")
		return nil
	}
	alias, target := args[0], args[1]
	if _, ok := getIndex(target); !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	aliasMu.Lock()
	defer aliasMu.Unlock()
	if _, taken := aliases[alias]; taken {
		c.Reply.Error("Alias already exists")
		return nil
	}
	aliases[alias] = target
	c.Reply.SimpleString("OK")
	return nil
}

// FT.ALIASUPDATE alias index — repoint an existing alias (or create
// it). Always succeeds; matches Redis semantics (UPDATE is a force).
func ftAliasUpdate(c *modules.Ctx, args []string) error {
	if len(args) != 2 {
		c.Reply.Error("FT.ALIASUPDATE alias index")
		return nil
	}
	alias, target := args[0], args[1]
	if _, ok := getIndex(target); !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	aliasMu.Lock()
	aliases[alias] = target
	aliasMu.Unlock()
	c.Reply.SimpleString("OK")
	return nil
}

// FT.ALIASDEL alias — remove an alias. Returns an error when unknown
// (matches Redis — silent success would mask typos).
func ftAliasDel(c *modules.Ctx, args []string) error {
	if len(args) != 1 {
		c.Reply.Error("FT.ALIASDEL alias")
		return nil
	}
	aliasMu.Lock()
	defer aliasMu.Unlock()
	if _, ok := aliases[args[0]]; !ok {
		c.Reply.Error("Alias does not exist")
		return nil
	}
	delete(aliases, args[0])
	c.Reply.SimpleString("OK")
	return nil
}

// FT.DICTADD dict term [term ...] — add terms to a custom dictionary.
// Returns the count of *new* terms added.
func ftDictAdd(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("FT.DICTADD dict term [term ...]")
		return nil
	}
	c.Reply.Int(int64(DictAdd(args[0], args[1:])))
	return nil
}

// FT.DICTDEL dict term [term ...] — remove terms. Returns the count
// actually deleted.
func ftDictDel(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("FT.DICTDEL dict term [term ...]")
		return nil
	}
	c.Reply.Int(int64(DictDel(args[0], args[1:])))
	return nil
}

// FT.DICTDUMP dict — return every term in the dictionary, sorted.
func ftDictDump(c *modules.Ctx, args []string) error {
	if len(args) != 1 {
		c.Reply.Error("FT.DICTDUMP dict")
		return nil
	}
	terms := DictDump(args[0])
	out := make([]any, len(terms))
	for i, t := range terms {
		out[i] = t
	}
	c.Reply.Array(out)
	return nil
}

// FT.TAGVALS index field — every distinct value present on a TAG field.
// Errors when the index is unknown; missing field returns empty array.
func ftTagVals(c *modules.Ctx, args []string) error {
	if len(args) != 2 {
		c.Reply.Error("FT.TAGVALS index field")
		return nil
	}
	values, ok := TagValues(args[0], args[1])
	if !ok {
		c.Reply.Error("Unknown index")
		return nil
	}
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	c.Reply.Array(out)
	return nil
}

// FT.CONFIG GET <pattern> | SET <key> <value> | RESETSTAT — runtime
// tunables. The set of well-known keys is documented in admin.go;
// unknown keys still round-trip so drivers can experiment.
func ftConfig(c *modules.Ctx, args []string) error {
	if len(args) < 1 {
		c.Reply.Error("FT.CONFIG GET pattern | SET key value")
		return nil
	}
	switch strings.ToUpper(args[0]) {
	case "GET":
		pattern := "*"
		if len(args) >= 2 {
			pattern = args[1]
		}
		entries := ConfigGet(pattern)
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, []any{e[0], e[1]})
		}
		c.Reply.Array(out)
	case "SET":
		if len(args) < 3 {
			c.Reply.Error("FT.CONFIG SET key value")
			return nil
		}
		ConfigSet(args[1], args[2])
		c.Reply.SimpleString("OK")
	case "RESETSTAT":
		// Stats reset is a no-op today (we don't keep cumulative
		// FT.CONFIG-visible counters); reply OK so callers don't choke.
		c.Reply.SimpleString("OK")
	case "HELP":
		c.Reply.Array([]any{
			"FT.CONFIG GET <pattern>",
			"FT.CONFIG SET <key> <value>",
			"FT.CONFIG RESETSTAT",
		})
	default:
		c.Reply.Error("Unknown FT.CONFIG subcommand")
	}
	return nil
}
