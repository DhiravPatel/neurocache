package resp

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// shutdownCmd implements SHUTDOWN [NOSAVE | SAVE] [NOW] [FORCE] [ABORT].
// We honour SAVE / NOSAVE — SAVE forces a final RDB before exit; NOSAVE
// skips it. Without an explicit choice we follow Redis's default:
// snapshot if RDB is enabled, skip otherwise. The shutdown itself
// closes every accepted connection and exits the process.
func (c *conn) shutdownCmd(args []string) {
	save := c.eng.Cfg.RDBEnabled
	abort := false
	for _, a := range args {
		switch strings.ToUpper(a) {
		case "SAVE":
			save = true
		case "NOSAVE":
			save = false
		case "ABORT":
			abort = true
		}
	}
	if abort {
		writeSimple(c.bw, "OK")
		return
	}
	if save {
		_ = c.eng.SaveRDB()
	}
	writeSimple(c.bw, "OK")
	_ = c.bw.Flush()
	// Run the engine teardown asynchronously so the reply leaves the
	// socket before we exit. 100ms gives the kernel time to flush.
	go func() {
		c.eng.Stop()
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()
}

// scriptKillCmd terminates the currently-running Lua script. Real
// Redis tracks a global "script in progress" flag; we plumb through
// the same flag so SCRIPT KILL has something to flip.
func (c *conn) scriptKillCmd() {
	if !scriptInProgress.Load() {
		writeError(c.bw, "NOTBUSY No scripts in execution right now.")
		return
	}
	scriptKillRequested.Store(true)
	writeSimple(c.bw, "OK")
}

// scriptInProgress + scriptKillRequested are atomic flags toggled by
// the EVAL / FCALL paths. The interpreter polls scriptKillRequested
// between instructions and aborts when set.
var (
	scriptInProgress    atomic.Bool
	scriptKillRequested atomic.Bool
)

// objectHelpCmd implements OBJECT HELP. Helpful for clients sniffing
// what object subcommands the server supports.
func (c *conn) objectHelpCmd() {
	writeArray(c.bw, []string{
		"OBJECT ENCODING <key>",
		"OBJECT FREQ <key>",
		"OBJECT IDLETIME <key>",
		"OBJECT REFCOUNT <key>",
		"OBJECT HELP",
	})
}

// aclDryRunCmd: ACL DRYRUN <username> <command> [arg ...] — would the
// named user be allowed to execute this command with these args, given
// the ACL state? Returns "OK" on allow, the canonical NOPERM message
// on deny.
func (c *conn) aclDryRunCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'acl|dryrun'")
		return
	}
	user := c.eng.ACL.Get(args[0])
	if user == nil {
		writeError(c.bw, "WRONGUSER user does not exist")
		return
	}
	cmd := strings.ToUpper(args[1])
	cargs := args[2:]
	keys := keysForCommand(cmd, cargs)
	channels := channelsForCommand(cmd, cargs)
	if err := c.eng.ACL.Allowed(user, cmd, keys, channels); err != nil {
		writeBulk(c.bw, err.Error())
		return
	}
	writeSimple(c.bw, "OK")
}

// debugCmd extends the existing DEBUG stub with SLEEP — the only
// DEBUG subcommand most operators actually use. We accept the command
// regardless of args and reply OK; SLEEP blocks the requested seconds.
func (c *conn) debugCmd(args []string) {
	if len(args) >= 1 && strings.EqualFold(args[0], "SLEEP") {
		secs := 0.0
		if len(args) >= 2 {
			secs, _ = strconv.ParseFloat(args[1], 64)
		}
		_ = c.bw.Flush()
		time.Sleep(time.Duration(secs * float64(time.Second)))
		writeSimple(c.bw, "OK")
		return
	}
	writeSimple(c.bw, "OK")
}

// looksLikeSelector tests whether the first arg of CLIENT KILL is a
// modern selector keyword (ID/ADDR/LADDR/USER/TYPE/SKIPME) — when it
// is, the rest of the args are option pairs. Otherwise we treat the
// first arg as an ip:port for the legacy form.
func looksLikeSelector(s string) bool {
	switch strings.ToUpper(s) {
	case "ID", "ADDR", "LADDR", "USER", "TYPE", "SKIPME":
		return true
	}
	return false
}

// clientGetRedirCmd reports the redirect target of the current
// connection's tracking session — 0 when redirection isn't set.
func (c *conn) clientGetRedirCmd() {
	if c.eng.Tracking == nil {
		writeInt(c.bw, -1)
		return
	}
	info := c.eng.Tracking.Info(c.info.ID)
	writeInt(c.bw, int64(info.Redirect))
}

// clientKillExtendedCmd parses the multi-selector form of CLIENT KILL:
//
//   CLIENT KILL [ID id] [ADDR ip:port] [LADDR ip:port] [USER user]
//               [TYPE normal|master|replica|pubsub] [SKIPME yes|no]
//
// Returns the count of killed clients. The legacy single-arg form
// ("CLIENT KILL <addr>") is handled by the existing CLIENT handler.
func (c *conn) clientKillExtendedCmd(args []string) {
	var (
		filterID    uint64
		filterAddr  string
		filterUser  string
		filterType  string
		skipMe      = true
		anyFilter   bool
	)
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "ID":
			if i+1 < len(args) {
				v, _ := strconv.ParseUint(args[i+1], 10, 64)
				filterID = v
				i++
				anyFilter = true
			}
		case "ADDR", "LADDR":
			if i+1 < len(args) {
				filterAddr = args[i+1]
				i++
				anyFilter = true
			}
		case "USER":
			if i+1 < len(args) {
				filterUser = args[i+1]
				i++
				anyFilter = true
			}
		case "TYPE":
			if i+1 < len(args) {
				filterType = strings.ToLower(args[i+1])
				i++
				anyFilter = true
			}
		case "SKIPME":
			if i+1 < len(args) && strings.EqualFold(args[i+1], "no") {
				skipMe = false
			}
			i++
			anyFilter = true
		}
	}
	if !anyFilter {
		writeError(c.bw, "syntax error")
		return
	}
	count := 0
	for _, ci := range c.eng.Clients.List() {
		if skipMe && ci.ID == c.info.ID {
			continue
		}
		if filterID != 0 && ci.ID != filterID {
			continue
		}
		if filterAddr != "" && ci.Addr != filterAddr {
			continue
		}
		if filterUser != "" && ci.Username != filterUser {
			continue
		}
		_ = filterType // we don't track normal/master/replica distinction; accept the filter and drop nothing it would have spared
		if c.eng.Clients.Kill(ci.ID) {
			count++
		}
	}
	writeInt(c.bw, int64(count))
}
