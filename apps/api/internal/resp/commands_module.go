package resp

import (
	"fmt"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// moduleCmd implements MODULE LOAD | UNLOAD | LIST | LOADEX. Real
// Redis takes a filesystem path; we take a name from the built-in
// available registry — see internal/modules/api.go for the rationale.
func (c *conn) moduleCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'module'")
		return
	}
	reg := c.eng.Modules
	if reg == nil {
		writeError(c.bw, "ERR module support disabled")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "LOAD", "LOADEX":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'module|load'")
			return
		}
		// Args after the module name are passed as opaque options. We
		// don't currently surface them to Init, but the slot reserves
		// the future use without churning the protocol.
		if err := reg.Load(args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RebuildACLForModules()
		writeSimple(c.bw, "OK")
	case "UNLOAD":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'module|unload'")
			return
		}
		if err := reg.Unload(args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		c.eng.RebuildACLForModules()
		writeSimple(c.bw, "OK")
	case "LIST":
		infos := reg.List()
		fmt.Fprintf(c.bw, "*%d\r\n", len(infos))
		for _, info := range infos {
			out := []any{
				"name", info.Name,
				"ver", info.Version,
				"description", info.Description,
				"commands", anyStrings(info.Commands),
				"types", anyStrings(info.Types),
			}
			writeValue(c.bw, out)
		}
	default:
		writeError(c.bw, "Unknown MODULE subcommand "+args[0])
	}
}

// dispatchModule routes a command name through the module registry.
// Returns true when the command was claimed by a module (handled or
// errored), false when the dispatcher should fall back to the
// built-in switch.
func (c *conn) dispatchModule(cmd string, args []string) bool {
	if c.eng.Modules == nil {
		return false
	}
	mc, ok := c.eng.Modules.FindCmd(cmd)
	if !ok {
		return false
	}
	if !validateArity(mc, args) {
		writeError(c.bw, fmt.Sprintf("wrong number of arguments for '%s'", strings.ToLower(cmd)))
		return true
	}
	ctx := &modules.Ctx{
		Engine:   c.eng.Modules.Engine(),
		Reply:    &respWriter{w: c.bw},
		Username: usernameOf(c),
		Args:     args,
	}
	if err := mc.Run(ctx, args); err != nil {
		writeError(c.bw, err.Error())
	}
	if mc.Write {
		c.eng.RecordWrite(cmd, args)
	}
	return true
}

func validateArity(c *modules.Cmd, args []string) bool {
	got := len(args) + 1 // +1 for the command name slot, matching Redis convention
	if c.Arity == 0 {
		return true
	}
	if c.Arity > 0 {
		return got == c.Arity
	}
	return got >= -c.Arity
}

func usernameOf(c *conn) string {
	if c.user == nil {
		return ""
	}
	return c.user.Name
}

func anyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
