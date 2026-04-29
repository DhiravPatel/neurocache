package http

import (
	"errors"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// httpModule mirrors the RESP MODULE LOAD/UNLOAD/LIST surface so the
// dashboard playground can manage modules without dropping into a
// terminal.
func httpModule(h *handlers, args []string) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("MODULE subcommand ...")
	}
	reg := h.eng.Modules
	if reg == nil {
		return nil, errors.New("module support disabled")
	}
	switch strings.ToUpper(args[0]) {
	case "LOAD", "LOADEX":
		if len(args) < 2 {
			return nil, errors.New("MODULE LOAD name")
		}
		if err := reg.Load(args[1]); err != nil {
			return nil, err
		}
		h.eng.RebuildACLForModules()
		return "OK", nil
	case "UNLOAD":
		if len(args) < 2 {
			return nil, errors.New("MODULE UNLOAD name")
		}
		if err := reg.Unload(args[1]); err != nil {
			return nil, err
		}
		h.eng.RebuildACLForModules()
		return "OK", nil
	case "LIST":
		out := []map[string]any{}
		for _, info := range reg.List() {
			out = append(out, map[string]any{
				"name":        info.Name,
				"version":     info.Version,
				"description": info.Description,
				"commands":    info.Commands,
				"types":       info.Types,
			})
		}
		return out, nil
	case "AVAILABLE":
		// NeuroCache extension — surfaces compile-time-linked modules
		// the operator could load. Useful for the dashboard module
		// management screen.
		out := []map[string]any{}
		for _, m := range modules.Available() {
			out = append(out, map[string]any{
				"name": m.Name, "version": m.Version,
				"description": m.Description,
			})
		}
		return out, nil
	}
	return nil, errors.New("unknown MODULE subcommand")
}

// dispatchHTTPModule runs a module-registered command through the HTTP
// path. Returns (value, true, err) when the command was claimed, or
// (nil, false, nil) when no module owns it (caller falls through).
func dispatchHTTPModule(h *handlers, cmd string, args []string) (any, bool, error) {
	if h.eng.Modules == nil {
		return nil, false, nil
	}
	mc, ok := h.eng.Modules.FindCmd(cmd)
	if !ok {
		return nil, false, nil
	}
	if !validateHTTPArity(mc, args) {
		return nil, true, errors.New("wrong number of arguments for '" + strings.ToLower(cmd) + "'")
	}
	w := newCaptureWriter()
	ctx := &modules.Ctx{
		Engine: h.eng.Modules.Engine(),
		Reply:  w,
		Args:   args,
	}
	if err := mc.Run(ctx, args); err != nil {
		return nil, true, err
	}
	if mc.Write {
		h.eng.RecordWrite(cmd, args)
	}
	return w.value, true, w.err
}

func validateHTTPArity(c *modules.Cmd, args []string) bool {
	got := len(args) + 1
	if c.Arity == 0 {
		return true
	}
	if c.Arity > 0 {
		return got == c.Arity
	}
	return got >= -c.Arity
}

// captureWriter implements modules.Writer in a way that buffers the
// reply into a Go value the HTTP handler returns as JSON.
type captureWriter struct {
	value any
	err   error
}

func newCaptureWriter() *captureWriter { return &captureWriter{} }

func (w *captureWriter) SimpleString(s string) { w.value = s }
func (w *captureWriter) Bulk(s string)         { w.value = s }
func (w *captureWriter) Int(n int64)           { w.value = n }
func (w *captureWriter) Float(f float64)       { w.value = f }
func (w *captureWriter) Array(items []any)     { w.value = items }
func (w *captureWriter) Error(msg string)      { w.err = errors.New(msg) }
func (w *captureWriter) Nil()                  { w.value = nil }
func (w *captureWriter) NilArray()             { w.value = nil }
