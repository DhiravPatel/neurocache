package resp

import (
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/scripting"
)

// functionCmd implements FUNCTION LOAD/DELETE/LIST/STATS/FLUSH/DUMP/RESTORE.
// We use the engine's existing scripting interpreter as the runtime;
// FUNCTION just stores the source server-side so FCALL can reference
// functions by name without re-uploading.
func (c *conn) functionCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'function'")
		return
	}
	reg := c.eng.Functions
	switch strings.ToUpper(args[0]) {
	case "LOAD":
		replace := false
		i := 1
		if i < len(args) && strings.EqualFold(args[i], "REPLACE") {
			replace = true
			i++
		}
		if i >= len(args) {
			writeError(c.bw, "FUNCTION LOAD requires source")
			return
		}
		name, err := reg.Load(args[i], replace)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, name)
	case "DELETE":
		if len(args) < 2 {
			writeError(c.bw, "FUNCTION DELETE name")
			return
		}
		if err := reg.Delete(args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "FLUSH":
		reg.Flush()
		writeSimple(c.bw, "OK")
	case "LIST":
		libs := reg.List()
		out := make([]any, 0, len(libs))
		for _, lib := range libs {
			funcs := []any{}
			for fname := range lib.Funcs {
				funcs = append(funcs, []any{
					"name", fname,
					"description", "",
					"flags", []any{},
				})
			}
			out = append(out, []any{
				"library_name", lib.Name,
				"engine", lib.Engine,
				"functions", funcs,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		calls, errs, totalNs := reg.Stats()
		writeValue(c.bw, []any{
			"running_script", nil,
			"engines", []any{
				"LUA", []any{
					"libraries_count", int64(len(reg.List())),
					"functions_count", int64(0),
				},
			},
			"calls", int64(calls),
			"errors", int64(errs),
			"total_ns", int64(totalNs),
		})
	case "DUMP":
		// Concatenate every loaded library's source — opaque to the
		// caller, restorable via FUNCTION RESTORE. Real Redis serialises
		// to a binary format; ours is just the concatenation since we
		// don't have a separate on-disk representation.
		var sb strings.Builder
		for _, lib := range reg.List() {
			sb.WriteString(lib.Source)
			sb.WriteString("\n")
		}
		writeBulk(c.bw, sb.String())
	case "RESTORE":
		if len(args) < 2 {
			writeError(c.bw, "FUNCTION RESTORE payload [FLUSH|APPEND|REPLACE]")
			return
		}
		mode := "APPEND"
		if len(args) >= 3 {
			mode = strings.ToUpper(args[2])
		}
		if mode == "FLUSH" {
			reg.Flush()
		}
		// Each library's source ends with `end\n` and the next starts
		// with `#!lua name=` — we split on that and load them one by
		// one. Simpler than parsing the whole blob.
		blocks := splitLibraryBlocks(args[1])
		for _, b := range blocks {
			if _, err := reg.Load(b, mode == "REPLACE"); err != nil {
				writeError(c.bw, err.Error())
				return
			}
		}
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "Unknown FUNCTION subcommand "+args[0])
	}
}

// fcallCmd implements FCALL function numkeys [key ...] [arg ...].
// FCALL_RO is the read-only variant — same dispatch but we still let
// the engine's per-command ACL gate decide if the caller can mutate.
func (c *conn) fcallCmd(args []string, _ bool) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'fcall'")
		return
	}
	name := args[0]
	numKeys, err := strconv.Atoi(args[1])
	if err != nil || numKeys < 0 {
		writeError(c.bw, "Number of keys can't be negative")
		return
	}
	if 2+numKeys > len(args) {
		writeError(c.bw, "Number of keys can't be greater than number of args")
		return
	}
	keys := args[2 : 2+numKeys]
	argv := args[2+numKeys:]
	_, body, ok := c.eng.Functions.LookupFunction(name)
	if !ok {
		writeError(c.bw, "Function not found: "+name)
		return
	}
	timeout := time.Duration(c.eng.Cfg.ScriptTimeoutMs) * time.Millisecond
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	caller := scripting.Caller(func(cmd string, a []string) (any, error) {
		return c.callFromScript(cmd, a)
	})
	start := time.Now()
	v, err := scripting.Run(body, keys, argv, caller, deadline)
	c.eng.Functions.RecordCall(uint64(time.Since(start).Nanoseconds()), err != nil)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	writeValue(c.bw, v)
}

// splitLibraryBlocks separates a concatenated FUNCTION DUMP payload
// back into individual `#!lua name=...` blocks.
func splitLibraryBlocks(s string) []string {
	parts := strings.Split(s, "#!lua")
	out := []string{}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// the leading "#!lua" was the split delimiter — restore it for
		// every block except an empty leading element.
		out = append(out, "#!lua "+p)
		_ = i
	}
	return out
}
