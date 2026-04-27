package resp

import "strings"

// keysForCommand returns the slice of args that the given command treats
// as keys. The ACL layer uses this to enforce key-pattern permissions.
// Conservative: when in doubt, return args[:1] so the user must have
// permission to the first arg.
func keysForCommand(cmd string, args []string) []string {
	if len(args) == 0 {
		return nil
	}
	switch cmd {
	case "MGET", "DEL", "UNLINK", "EXISTS", "WATCH", "TYPE", "OBJECT", "DUMP",
		"PFCOUNT", "TOUCH":
		return args
	case "MSET", "MSETNX":
		out := []string{}
		for i := 0; i+1 < len(args); i += 2 {
			out = append(out, args[i])
		}
		return out
	case "RENAME", "RENAMENX", "COPY", "RPOPLPUSH", "LMOVE", "BLMOVE", "SMOVE", "BITOP":
		// destination + source (BITOP has dst + sources)
		return args[1:]
	case "SINTERSTORE", "SUNIONSTORE", "SDIFFSTORE":
		// destination + source(s); ACL gates on every key referenced
		return args
	case "GEOSEARCHSTORE":
		// dest src ...search-args — only the first two slots are keys.
		if len(args) >= 2 {
			return args[:2]
		}
		return args
	case "GEORADIUS", "GEORADIUS_RO", "GEORADIUSBYMEMBER", "GEORADIUSBYMEMBER_RO":
		// args[0] is the source key. STORE / STOREDIST options carry an
		// additional destination key — surface it so cluster routing
		// rejects cross-slot writes.
		out := []string{args[0]}
		for i := 1; i < len(args); i++ {
			if (strings.EqualFold(args[i], "STORE") || strings.EqualFold(args[i], "STOREDIST")) && i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			}
		}
		return out
	case "ZADD", "XADD", "GEOADD", "PFADD", "PFMERGE":
		return args[:1]
	case "BLPOP", "BRPOP", "BZPOPMIN", "BZPOPMAX":
		// last arg is timeout; everything before is a key.
		if len(args) >= 2 {
			return args[:len(args)-1]
		}
	case "XREAD", "XREADGROUP":
		// Can't easily extract here without re-parsing the STREAMS clause;
		// returning nil punts the check (XREAD permission already requires
		// CatStream). Real Redis does the same imprecise gating.
		return nil
	case "HOTKEYS":
		// Admin observability — no key arguments. Without this clause
		// the default would treat subcommand keywords like RESET / STATS
		// as keys, which the ACL gate would then check for read perms.
		return nil
	}
	return args[:1]
}

// channelsForCommand returns the channels referenced by SUBSCRIBE,
// PSUBSCRIBE, PUBLISH so ACL can enforce channel patterns.
func channelsForCommand(cmd string, args []string) []string {
	switch cmd {
	case "SUBSCRIBE", "UNSUBSCRIBE", "PSUBSCRIBE", "PUNSUBSCRIBE":
		return args
	case "PUBLISH":
		if len(args) >= 1 {
			return args[:1]
		}
	}
	return nil
}
