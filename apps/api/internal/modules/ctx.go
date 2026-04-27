package modules

import "errors"

// EngineHandle is the surface modules use to interact with the live
// keyspace. We expose a tightly-scoped subset rather than the full
// engine struct so the ABI stays stable across internal refactors —
// modules don't depend on engine.* directly.
type EngineHandle interface {
	// SetCustomValue stores a module-typed value at key. typeID is the
	// CustomType's ID. ttl=0 means no expiry.
	SetCustomValue(key string, typeID TypeID, value any, ttlMs int64) error

	// GetCustomValue fetches the value at key. ok=false when the key
	// is missing or holds a different type.
	GetCustomValue(key string, typeID TypeID) (value any, ok bool, err error)

	// DelCustomValue removes a key, returning whether anything was deleted.
	DelCustomValue(key string) bool

	// Publish fans a message out via the engine pub/sub broker.
	Publish(channel, payload string) int
}

// RegisterCtx is what Init() receives; the only legal way to declare
// commands and types. Once Init returns the registration is sealed.
type RegisterCtx struct {
	mod      *Module
	registry *Registry

	// commands + types are accumulated locally so a failing Init
	// rolls back atomically (no half-loaded modules).
	commands []Cmd
	types    []CustomType
}

// RegisterCmd records a command for later activation.
func (r *RegisterCtx) RegisterCmd(c Cmd) error {
	if c.Name == "" {
		return errors.New("module command must have a name")
	}
	if c.Run == nil {
		return errors.New("module command must have a Run handler")
	}
	r.commands = append(r.commands, c)
	return nil
}

// RegisterType records a custom data type.
func (r *RegisterCtx) RegisterType(t CustomType) error {
	var zero TypeID
	if t.ID == zero {
		return errors.New("custom type must have a non-zero TypeID")
	}
	if t.Marshal == nil || t.Unmarshal == nil {
		return errors.New("custom type must have Marshal + Unmarshal")
	}
	r.types = append(r.types, t)
	return nil
}

// Engine returns the engine handle modules use at runtime.
func (r *RegisterCtx) Engine() EngineHandle { return r.registry.engine }

// Ctx is what each command's Run() receives — the per-invocation
// envelope binding engine state, the writable reply, and metadata.
type Ctx struct {
	Engine   EngineHandle
	Reply    Writer
	Username string

	// raw access to the args (excluding the command name) for handlers
	// that need to scan the slice themselves.
	Args []string
}

// Writer is how modules produce RESP replies. It mirrors the most
// common encoding shapes; modules never reach into bufio directly so
// the engine can swap transports later (RESP3, HTTP/JSON, etc.).
type Writer interface {
	SimpleString(s string)
	Bulk(s string)
	Int(n int64)
	Float(f float64)
	Array(items []any)
	Error(msg string)
	Nil()
	NilArray()
}
