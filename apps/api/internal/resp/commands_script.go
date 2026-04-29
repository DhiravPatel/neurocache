package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/scripting"
)

// evalCmd implements EVAL script numkeys [key ...] [arg ...]. The script
// runs through the embedded interpreter; redis.call() bridges back into
// the dispatcher so any RESP command works inside scripts.
func (c *conn) evalCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'eval'")
		return
	}
	src := args[0]
	numKeys, err := strconv.Atoi(args[1])
	if err != nil || numKeys < 0 {
		writeError(c.bw, "value is out of range, must be positive")
		return
	}
	if 2+numKeys > len(args) {
		writeError(c.bw, "Number of keys can't be greater than number of args")
		return
	}
	keys := args[2 : 2+numKeys]
	argv := args[2+numKeys:]
	c.runScript(src, keys, argv)
}

// evalshaCmd resolves the sha1 to source via the cache, then runs it.
func (c *conn) evalshaCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'evalsha'")
		return
	}
	src, ok := c.eng.Scripts.Get(args[0])
	if !ok {
		writeTypedError(c.bw, "NOSCRIPT", "No matching script. Please use EVAL.")
		return
	}
	numKeys, err := strconv.Atoi(args[1])
	if err != nil || numKeys < 0 {
		writeError(c.bw, "value is out of range, must be positive")
		return
	}
	if 2+numKeys > len(args) {
		writeError(c.bw, "Number of keys can't be greater than number of args")
		return
	}
	keys := args[2 : 2+numKeys]
	argv := args[2+numKeys:]
	c.runScript(src, keys, argv)
}

// runScript runs src under the deadline, then encodes the reply.
func (c *conn) runScript(src string, keys, argv []string) {
	timeout := time.Duration(c.eng.Cfg.ScriptTimeoutMs) * time.Millisecond
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	// Cache it so EVALSHA works even after the first EVAL.
	c.eng.Scripts.Load(src)
	caller := scripting.Caller(func(cmd string, a []string) (any, error) {
		return c.callFromScript(cmd, a)
	})
	v, err := scripting.Run(src, keys, argv, caller, deadline)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	writeValue(c.bw, v)
}

// callFromScript dispatches a redis.call() invocation back into the
// engine. Auth/ACL gating still applies — we go through the same
// permission check the conn uses for native commands.
func (c *conn) callFromScript(cmd string, args []string) (any, error) {
	cmd = strings.ToUpper(cmd)
	if c.user != nil {
		if err := c.eng.ACL.Allowed(c.user, cmd, scriptKeysFor(cmd, args), nil); err != nil {
			return nil, err
		}
	}
	// Reuse the HTTP-side dispatcher's pure-data variant via a tiny
	// shim. Keeping a single source of truth would be ideal — long-term
	// the dispatcher should be refactored — but for now we re-implement
	// the most common cases inline so scripting doesn't depend on http.
	return scriptDispatch(c, cmd, args)
}

// scriptKeysFor returns the keys a command touches, for ACL purposes.
// EVAL/EVALSHA pass their KEYS list explicitly — we trust callers to
// pre-validate via the standard ACL check rather than re-parsing here.
func scriptKeysFor(cmd string, args []string) []string {
	if len(args) == 0 {
		return nil
	}
	switch cmd {
	case "MGET", "DEL", "UNLINK", "EXISTS", "WATCH":
		return args
	case "MSET", "MSETNX":
		out := []string{}
		for i := 0; i+1 < len(args); i += 2 {
			out = append(out, args[i])
		}
		return out
	}
	return args[:1]
}

// scriptCmd implements SCRIPT LOAD | EXISTS | FLUSH.
func (c *conn) scriptCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'script'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "LOAD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'script|load'")
			return
		}
		writeBulk(c.bw, c.eng.Scripts.Load(args[1]))
	case "EXISTS":
		out := c.eng.Scripts.Exists(args[1:]...)
		ints := make([]any, len(out))
		for i, b := range out {
			if b {
				ints[i] = int64(1)
			} else {
				ints[i] = int64(0)
			}
		}
		writeValue(c.bw, ints)
	case "FLUSH":
		c.eng.Scripts.Flush()
		writeSimple(c.bw, "OK")
	case "KILL":
		c.scriptKillCmd()
	default:
		writeError(c.bw, "unknown SCRIPT subcommand")
	}
}
