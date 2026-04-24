package resp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
)

// authCmd implements AUTH [username] password. With one arg the username
// defaults to "default", matching Redis's legacy behaviour.
func (c *conn) authCmd(args []string) {
	if len(args) == 0 {
		writeError(c.bw, "wrong number of arguments for 'auth'")
		return
	}
	username, password := "default", args[0]
	if len(args) >= 2 {
		username, password = args[0], args[1]
	}
	u, err := c.eng.ACL.Authenticate(username, password)
	if err != nil {
		writeTypedError(c.bw, "WRONGPASS", strings.TrimPrefix(err.Error(), "WRONGPASS "))
		return
	}
	c.user = u
	c.info.Username = u.Name
	writeSimple(c.bw, "OK")
}

// aclCmd implements ACL LIST | WHOAMI | GETUSER | SETUSER | DELUSER |
// USERS | CAT | LOG | GENPASS | SAVE | LOAD.
func (c *conn) aclCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'acl'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "LIST":
		out := []string{}
		for _, name := range c.eng.ACL.List() {
			u := c.eng.ACL.Get(name)
			if u == nil {
				continue
			}
			out = append(out, "user "+u.Name+" "+strings.Join(formatRules(u), " "))
		}
		writeArray(c.bw, out)
	case "WHOAMI":
		if c.user == nil {
			writeBulk(c.bw, "")
			return
		}
		writeBulk(c.bw, c.user.Name)
	case "USERS":
		writeArray(c.bw, c.eng.ACL.List())
	case "GETUSER":
		if len(args) < 2 {
			writeError(c.bw, "ACL GETUSER username")
			return
		}
		u := c.eng.ACL.Get(args[1])
		if u == nil {
			writeNilArray(c.bw)
			return
		}
		writeUserDetails(c, u)
	case "SETUSER":
		if len(args) < 2 {
			writeError(c.bw, "ACL SETUSER username [rule ...]")
			return
		}
		if err := c.eng.ACL.SetUser(args[1], args[2:]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		_ = c.eng.ACL.Save()
		writeSimple(c.bw, "OK")
	case "DELUSER":
		if len(args) < 2 {
			writeError(c.bw, "ACL DELUSER user [user ...]")
			return
		}
		n := c.eng.ACL.Delete(args[1:]...)
		_ = c.eng.ACL.Save()
		writeInt(c.bw, int64(n))
	case "CAT":
		if len(args) == 1 {
			writeArray(c.bw, acl.AllCategories)
			return
		}
		writeArray(c.bw, acl.CommandsInCategory(strings.ToLower(args[1])))
	case "LOG":
		count := 0
		if len(args) >= 2 {
			if strings.EqualFold(args[1], "RESET") {
				c.eng.ACL.LogReset()
				writeSimple(c.bw, "OK")
				return
			}
			count, _ = strconv.Atoi(args[1])
		}
		entries := c.eng.ACL.Log(count)
		fmt.Fprintf(c.bw, "*%d\r\n", len(entries))
		for _, e := range entries {
			out := []any{
				"count", int64(e.Count),
				"reason", e.Reason,
				"context", e.Context,
				"object", e.Object,
				"username", e.Username,
				"age-seconds", strconv.FormatFloat(e.AgeSeconds, 'f', 3, 64),
				"client-info", e.ClientInfo,
				"entry-id", int64(e.EntryID),
				"timestamp-created", e.Timestamp.Unix(),
				"timestamp-last-updated", e.Timestamp.Unix(),
			}
			fmt.Fprintf(c.bw, "*%d\r\n", len(out))
			for _, v := range out {
				writeValue(c.bw, v)
			}
		}
	case "GENPASS":
		bits := 256
		if len(args) >= 2 {
			bits, _ = strconv.Atoi(args[1])
		}
		writeBulk(c.bw, genPassword(bits))
	case "SAVE":
		if err := c.eng.ACL.Save(); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown ACL subcommand "+args[0])
	}
}

func writeUserDetails(c *conn, u *acl.User) {
	flags := u.Describe()
	pwHashes := u.Hashes()
	fmt.Fprintf(c.bw, "*12\r\n")
	writeBulk(c.bw, "flags")
	writeArray(c.bw, flags)
	writeBulk(c.bw, "passwords")
	writeArray(c.bw, pwHashes)
	writeBulk(c.bw, "commands")
	writeBulk(c.bw, summarizeCommands(u))
	writeBulk(c.bw, "keys")
	writeArray(c.bw, summarizeKeys(u))
	writeBulk(c.bw, "channels")
	writeArray(c.bw, summarizeChannels(u))
	writeBulk(c.bw, "selectors")
	writeArray(c.bw, []string{})
}

func summarizeCommands(u *acl.User) string {
	if u.AllCommands {
		return "+@all"
	}
	parts := []string{}
	for c := range u.AllowedCats {
		parts = append(parts, "+@"+c)
	}
	for c := range u.DeniedCats {
		parts = append(parts, "-@"+c)
	}
	for c := range u.AllowedCmds {
		parts = append(parts, "+"+strings.ToLower(c))
	}
	for c := range u.DeniedCmds {
		parts = append(parts, "-"+strings.ToLower(c))
	}
	return strings.Join(parts, " ")
}

func summarizeKeys(u *acl.User) []string {
	if u.AllKeys {
		return []string{"~*"}
	}
	out := make([]string, len(u.KeyPatterns))
	for i, p := range u.KeyPatterns {
		out[i] = "~" + p
	}
	return out
}

func summarizeChannels(u *acl.User) []string {
	if u.AllChannels {
		return []string{"&*"}
	}
	out := make([]string, len(u.ChannelPatterns))
	for i, p := range u.ChannelPatterns {
		out[i] = "&" + p
	}
	return out
}

func formatRules(u *acl.User) []string {
	out := u.Describe()
	if !u.AllCommands {
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
	} else {
		out = append(out, "+@all")
	}
	for _, p := range u.KeyPatterns {
		out = append(out, "~"+p)
	}
	for _, p := range u.ChannelPatterns {
		out = append(out, "&"+p)
	}
	for _, h := range u.Hashes() {
		out = append(out, "#"+h)
	}
	return out
}

// genPassword produces a hex-encoded random password of bits/4 chars.
// Uses crypto/rand — predictable entropy would defeat the purpose of
// the command, which is literally to mint a password.
func genPassword(bits int) string {
	if bits <= 0 {
		bits = 256
	}
	byteLen := (bits + 7) / 8
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand reads from /dev/urandom on *nix and CryptGenRandom
		// on Windows — failures are effectively kernel-level. Fall back
		// to a time-seeded hex so the command never returns empty, but
		// an operator seeing this string should audit the host.
		return fmt.Sprintf("fallback-%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)[:bits/4]
}

// silence unused import for runtime when the build trims out the old impl.
var _ = runtime.NumCPU

// clientCmd implements CLIENT SETNAME/GETNAME/ID/LIST/KILL/PAUSE/REPLY/
// NO-EVICT/INFO. Anything else returns OK to stay compatible with
// drivers that issue CLIENT SETINFO and friends.
func (c *conn) clientCmd(args []string) {
	if len(args) < 1 {
		writeSimple(c.bw, "OK")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "ID":
		writeInt(c.bw, int64(c.info.ID))
	case "GETNAME":
		if c.info.Name == "" {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, c.info.Name)
	case "SETNAME":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments")
			return
		}
		c.info.Name = args[1]
		writeSimple(c.bw, "OK")
	case "INFO":
		writeBulk(c.bw, c.info.FormatLine())
	case "LIST":
		clients := c.eng.Clients.List()
		out := strings.Builder{}
		for i, ci := range clients {
			if i > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(ci.FormatLine())
		}
		writeBulk(c.bw, out.String())
	case "KILL":
		// CLIENT KILL ID id
		if len(args) >= 3 && strings.EqualFold(args[1], "ID") {
			id, _ := strconv.ParseUint(args[2], 10, 64)
			if c.eng.Clients.Kill(id) {
				writeInt(c.bw, 1)
				return
			}
		}
		writeInt(c.bw, 0)
	case "PAUSE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments")
			return
		}
		ms, _ := strconv.Atoi(args[1])
		c.eng.Clients.Pause(time.Duration(ms) * time.Millisecond)
		writeSimple(c.bw, "OK")
	case "UNPAUSE":
		c.eng.Clients.Pause(0)
		writeSimple(c.bw, "OK")
	case "REPLY":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments")
			return
		}
		mode := strings.ToLower(args[1])
		switch mode {
		case "on", "off", "skip":
			c.info.ReplyMode = mode
		default:
			writeError(c.bw, "syntax error")
			return
		}
		// REPLY ON gets an OK; OFF/SKIP suppress all replies including this one.
		if mode == "on" {
			writeSimple(c.bw, "OK")
		}
	case "NO-EVICT":
		if len(args) >= 2 && strings.EqualFold(args[1], "ON") {
			c.info.NoEvict = true
		} else {
			c.info.NoEvict = false
		}
		writeSimple(c.bw, "OK")
	default:
		writeSimple(c.bw, "OK")
	}
}

// resetCmd implements RESET — clear MULTI/WATCH, drop subscriptions,
// reset to default user. The reply is "+RESET\r\n" per Redis.
func (c *conn) resetCmd() {
	c.tx.Discard()
	c.tx.Unwatch()
	for ch, sub := range c.subs {
		sub.Close()
		delete(c.subs, ch)
	}
	for ch, sub := range c.psub {
		sub.Close()
		delete(c.psub, ch)
	}
	c.user = c.eng.ACL.DefaultUser()
	if c.user != nil {
		c.info.Username = c.user.Name
	}
	c.info.ReplyMode = "on"
	writeSimple(c.bw, "RESET")
}

// objectCmd implements OBJECT ENCODING/IDLETIME/FREQ/REFCOUNT/HELP.
func (c *conn) objectCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'object'")
		return
	}
	sub := strings.ToUpper(args[0])
	if sub == "HELP" {
		writeArray(c.bw, []string{
			"OBJECT ENCODING <key>", "OBJECT IDLETIME <key>",
			"OBJECT FREQ <key>", "OBJECT REFCOUNT <key>",
		})
		return
	}
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'object'")
		return
	}
	info, ok := c.eng.KV.Object(args[1])
	if !ok {
		writeTypedError(c.bw, "ERR", "no such key")
		return
	}
	switch sub {
	case "ENCODING":
		writeBulk(c.bw, info.Encoding)
	case "IDLETIME":
		writeInt(c.bw, info.IdleSec)
	case "FREQ":
		writeInt(c.bw, int64(info.FreqHits))
	case "REFCOUNT":
		writeInt(c.bw, 1)
	default:
		writeError(c.bw, "Unknown OBJECT subcommand")
	}
}

// memoryCmd implements MEMORY USAGE/STATS/DOCTOR/PURGE.
func (c *conn) memoryCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'memory'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "USAGE":
		if len(args) < 2 {
			writeError(c.bw, "MEMORY USAGE key")
			return
		}
		info, ok := c.eng.KV.Object(args[1])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeInt(c.bw, info.Bytes)
	case "STATS":
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		out := []any{
			"peak.allocated", int64(m.HeapSys),
			"total.allocated", int64(m.HeapAlloc),
			"keys.bytes-per-key", int64(0),
			"dataset.bytes", c.eng.KV.BytesUsed(),
			"goroutines", int64(runtime.NumGoroutine()),
		}
		fmt.Fprintf(c.bw, "*%d\r\n", len(out))
		for _, v := range out {
			writeValue(c.bw, v)
		}
	case "DOCTOR":
		writeBulk(c.bw, "Sam, I detected a few issues:\n  - none right now.\n")
	case "PURGE":
		runtime.GC()
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown MEMORY subcommand")
	}
}

// slowlogCmd implements SLOWLOG GET/LEN/RESET/HELP.
func (c *conn) slowlogCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'slowlog'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "GET":
		count := 0
		if len(args) >= 2 {
			count, _ = strconv.Atoi(args[1])
		}
		entries := c.eng.SlowLog.Get(count)
		fmt.Fprintf(c.bw, "*%d\r\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(c.bw, "*6\r\n")
			writeInt(c.bw, int64(e.ID))
			writeInt(c.bw, e.At.Unix())
			writeInt(c.bw, e.Duration.Microseconds())
			writeArray(c.bw, e.Command)
			writeBulk(c.bw, e.Client)
			writeBulk(c.bw, "")
		}
	case "LEN":
		writeInt(c.bw, int64(c.eng.SlowLog.Len()))
	case "RESET":
		c.eng.SlowLog.Reset()
		writeSimple(c.bw, "OK")
	case "HELP":
		writeArray(c.bw, []string{"SLOWLOG GET [count]", "SLOWLOG LEN", "SLOWLOG RESET"})
	default:
		writeError(c.bw, "unknown SLOWLOG subcommand")
	}
}

// latencyCmd implements LATENCY HISTORY/LATEST/RESET/DOCTOR/GRAPH/HELP.
func (c *conn) latencyCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'latency'")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "HISTORY":
		if len(args) < 2 {
			writeError(c.bw, "LATENCY HISTORY event")
			return
		}
		events := c.eng.Latency.History(args[1])
		fmt.Fprintf(c.bw, "*%d\r\n", len(events))
		for _, e := range events {
			fmt.Fprintf(c.bw, "*2\r\n")
			writeInt(c.bw, e.At.Unix())
			writeInt(c.bw, e.Latency.Milliseconds())
		}
	case "LATEST":
		latest := c.eng.Latency.Latest()
		fmt.Fprintf(c.bw, "*%d\r\n", len(latest))
		for _, e := range latest {
			fmt.Fprintf(c.bw, "*4\r\n")
			writeBulk(c.bw, e.Name)
			writeInt(c.bw, e.At.Unix())
			writeInt(c.bw, e.Latency.Milliseconds())
			writeInt(c.bw, e.Latency.Milliseconds())
		}
	case "RESET":
		n := c.eng.Latency.Reset(args[1:]...)
		writeInt(c.bw, int64(n))
	case "DOCTOR":
		writeBulk(c.bw, c.eng.Latency.Doctor())
	case "GRAPH":
		writeBulk(c.bw, "")
	case "HELP":
		writeArray(c.bw, []string{
			"LATENCY HISTORY <event>", "LATENCY LATEST",
			"LATENCY RESET [event ...]", "LATENCY DOCTOR", "LATENCY GRAPH",
		})
	default:
		writeError(c.bw, "unknown LATENCY subcommand")
	}
}

// copyCmd implements COPY src dst [REPLACE] [DB n].
func (c *conn) copyCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'copy'")
		return
	}
	replace := false
	for _, a := range args[2:] {
		if strings.EqualFold(a, "REPLACE") {
			replace = true
		}
	}
	ok, err := c.eng.KV.Copy(args[0], args[1], replace)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	c.eng.RecordWrite("COPY", args)
	if ok {
		writeInt(c.bw, 1)
	} else {
		writeInt(c.bw, 0)
	}
}

// dumpCmd serializes a key for RESTORE.
func (c *conn) dumpCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'dump'")
		return
	}
	blob, ok, err := c.eng.KV.Dump(args[0])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeBulk(c.bw, blob)
}

// restoreCmd implements RESTORE key ttl serialized [REPLACE] [IDLETIME s]
// [FREQ f]. ttl is in milliseconds.
func (c *conn) restoreCmd(args []string) {
	if len(args) < 3 {
		writeError(c.bw, "wrong number of arguments for 'restore'")
		return
	}
	ttl, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		writeError(c.bw, "value is not an integer")
		return
	}
	replace := false
	for _, a := range args[3:] {
		if strings.EqualFold(a, "REPLACE") {
			replace = true
		}
	}
	if err := c.eng.KV.RestoreKey(args[0], ttl, args[2], replace); err != nil {
		writeError(c.bw, err.Error())
		return
	}
	c.eng.RecordWrite("RESTORE", args)
	writeSimple(c.bw, "OK")
}

// helloCmd implements HELLO [protover [AUTH user pass] [SETNAME name]].
// We always respond with RESP2 metadata since we don't yet implement
// RESP3 — clients fall back to RESP2 cleanly.
func (c *conn) helloCmd(args []string) {
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "AUTH":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			u, err := c.eng.ACL.Authenticate(args[i+1], args[i+2])
			if err != nil {
				writeTypedError(c.bw, "WRONGPASS", strings.TrimPrefix(err.Error(), "WRONGPASS "))
				return
			}
			c.user = u
			c.info.Username = u.Name
			i += 2
		case "SETNAME":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			c.info.Name = args[i+1]
			i++
		}
	}
	out := []any{
		"server", "neurocache",
		"version", "0.4.0",
		"proto", int64(2),
		"id", int64(c.info.ID),
		"mode", "standalone",
		"role", "master",
		"modules", []any{},
	}
	fmt.Fprintf(c.bw, "*%d\r\n", len(out))
	for _, v := range out {
		writeValue(c.bw, v)
	}
}
