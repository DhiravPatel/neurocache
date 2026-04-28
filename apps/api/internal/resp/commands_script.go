package resp

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/scripting"
)

// evalCmd implements EVAL script numkeys [key ...] [arg ...]. The script
// runs through the embedded interpreter; redis.call() bridges back into
// the dispatcher so any RESP command works inside scripts.
//
// readOnly is set true for EVAL_RO so the bridge rejects any
// keyspace-mutating command before it can reach the store.
func (c *conn) evalCmd(args []string, readOnly bool) {
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
	c.runScript(src, keys, argv, readOnly)
}

// evalshaCmd resolves the sha1 to source via the cache, then runs it.
func (c *conn) evalshaCmd(args []string, readOnly bool) {
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
	c.runScript(src, keys, argv, readOnly)
}

// runScript runs src under the deadline, then encodes the reply. The
// readOnly flag (set by EVAL_RO / EVALSHA_RO) makes the bridged
// callFromScript reject any keyspace-mutating command — letting clients
// run scripts on read-only replicas without risking accidental writes.
func (c *conn) runScript(src string, keys, argv []string, readOnly bool) {
	timeout := time.Duration(c.eng.Cfg.ScriptTimeoutMs) * time.Millisecond
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	// Cache it so EVALSHA works even after the first EVAL.
	c.eng.Scripts.Load(src)
	caller := scripting.Caller(func(cmd string, a []string) (any, error) {
		return c.callFromScript(cmd, a, readOnly)
	})
	scriptInProgress.Store(true)
	scriptKillRequested.Store(false)
	defer func() {
		scriptInProgress.Store(false)
		scriptKillRequested.Store(false)
	}()
	v, err := scripting.Run(src, keys, argv, caller, deadline)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	writeValue(c.bw, v)
}

// callFromScript dispatches a redis.call() invocation back into the
// engine. Auth/ACL gating still applies — we go through the same
// permission check the conn uses for native commands. When readOnly is
// true (EVAL_RO / EVALSHA_RO / FCALL_RO) any write-classified command
// is refused before it can mutate the keyspace.
func (c *conn) callFromScript(cmd string, args []string, readOnly bool) (any, error) {
	cmd = strings.ToUpper(cmd)
	// SCRIPT KILL signals the polling site between Lua VM instructions
	// via the deadline path; this extra check catches a kill that
	// landed mid-bridge and aborts the redis.call so partial side
	// effects don't accumulate.
	if scriptKillRequested.Load() {
		return nil, errors.New("UNKILLABLE Sorry the script already executed write commands against the dataset. You can either wait the script termination or kill the server in a hard way using the SHUTDOWN NOSAVE command.")
	}
	if readOnly && isWriteCommand(cmd) {
		return nil, errors.New("ERR Write commands are not allowed from read-only scripts")
	}
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
	case "SHOW":
		// SCRIPT SHOW <sha1> — Valkey 8.0. Returns the source for a
		// loaded script, useful when an operator wants to audit what
		// EVALSHA is about to run without re-LOAD'ing.
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'script|show'")
			return
		}
		src, ok := c.eng.Scripts.Get(args[1])
		if !ok {
			writeTypedError(c.bw, "NOSCRIPT", "No matching script. Please use SCRIPT LOAD.")
			return
		}
		writeBulk(c.bw, src)
	case "DEBUG":
		// SCRIPT DEBUG YES|SYNC|NO. Real Redis ships an interactive Lua
		// debugger; we have no debugger UI to attach, so we accept the
		// flag (recording it in the same place CLIENT TRACKING modes
		// live would be overkill) and return OK. Drivers that probe for
		// support get a clean affirmative.
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'script|debug'")
			return
		}
		switch strings.ToUpper(args[1]) {
		case "YES", "SYNC", "NO":
			writeSimple(c.bw, "OK")
		default:
			writeError(c.bw, "syntax error")
		}
	case "HELP":
		writeArray(c.bw, []string{
			"SCRIPT LOAD <script>", "SCRIPT EXISTS <sha1> [sha1 ...]",
			"SCRIPT FLUSH", "SCRIPT KILL",
			"SCRIPT SHOW <sha1>", "SCRIPT DEBUG YES|SYNC|NO",
		})
	default:
		writeError(c.bw, "unknown SCRIPT subcommand")
	}
}
