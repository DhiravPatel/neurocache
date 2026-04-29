// Package modules defines NeuroCache's module ABI: a small, stable Go
// surface that lets third-party (or in-tree) packages register new
// commands, custom data types, and lifecycle hooks without touching
// the engine source.
//
// We deliberately avoid Go's `plugin` package — its Linux/macOS-only
// scope, exact Go-version + dependency pinning requirements, and cgo
// overhead are not appropriate for a long-lived production engine.
// Modules here are *compile-time linked* and surfaced to operators
// through `MODULE LOAD <name>` from a built-in registry — the same
// model used by Tigerbeetle, Tendermint apps, and most Go-native data
// systems.
//
// Wire-level mental model for module authors:
//
//   1. Implement a Module value (Name, Version, Init).
//   2. Inside Init(), call ctx.RegisterCmd / ctx.RegisterType.
//   3. Have your package's init() call modules.RegisterAvailable(myModule).
//   4. Operators run `MODULE LOAD myname` to activate it.
package modules

// Module is the unit of registration. Authors construct one at package
// scope and surface it via RegisterAvailable so operators can load it.
type Module struct {
	Name        string
	Version     string
	Description string

	// Init is called once when the module is loaded. The RegisterCtx
	// is the *only* legal way to add commands or types — return any
	// error from here to abort the load.
	Init func(ctx *RegisterCtx) error

	// Shutdown is called when the module is unloaded or the engine
	// stops. Optional. If a module holds external resources (files,
	// goroutines, connections) it must release them here.
	Shutdown func() error
}

// Cmd is one command the module wants the engine to dispatch. The
// engine treats it identically to a built-in command in every respect
// except origin: ACL gating, slot routing in cluster mode, slowlog,
// metrics, and replication propagation all apply automatically.
type Cmd struct {
	// Name is the all-uppercase command identifier, e.g. "JSON.SET".
	// Conventionally module commands use a "MODULE.OP" namespace so
	// they don't collide with built-ins.
	Name string

	// Arity follows the Redis convention: positive = exact arg count
	// (including the command name itself); negative = "at least
	// |Arity|". Example: Arity=3 means exactly two args; Arity=-3
	// means two or more.
	Arity int

	// Categories integrate with ACL. Strings should match the
	// constants in the acl package (acl.CatRead, acl.CatWrite, etc.).
	// At minimum a module command should declare CatRead or CatWrite.
	Categories []string

	// KeyPosition tells the cluster router where the keys live so
	// MOVED/ASK redirection works for module commands. Use KeyNone for
	// commands with no key arguments.
	KeyPosition KeyPosition

	// Run is the command implementation. Errors from Run flow through
	// to the client as -ERR replies.
	Run func(ctx *Ctx, args []string) error

	// Write reports whether this command mutates the keyspace. True
	// values cause the engine to propagate the command to replicas
	// and the AOF.
	Write bool
}

// KeyPosition tells the engine which arguments are keys, so cluster
// routing and ACL key-pattern checks have something to inspect.
type KeyPosition struct {
	// First is the index of the first key argument, 1-based (matching
	// Redis convention; index 0 is the command name). 0 means "no
	// keys" and disables routing checks.
	First int
	// Last is the index of the last key argument; -1 = end of args.
	Last int
	// Step is the stride between successive keys (1 = consecutive,
	// 2 = "key value key value …" interleaved).
	Step int
}

// KeyNone is the canonical "no keys" position.
var KeyNone = KeyPosition{First: 0, Last: 0, Step: 0}

// KeyAt builds a single-key position at args[1].
func KeyAt(idx int) KeyPosition { return KeyPosition{First: idx, Last: idx, Step: 1} }

// KeyRange builds a multi-key position spanning args[first..last] with
// the given stride.
func KeyRange(first, last, step int) KeyPosition { return KeyPosition{First: first, Last: last, Step: step} }

// Keys extracts the key arguments per the position spec. Used by the
// engine's ACL and cluster routing layers.
func (p KeyPosition) Keys(args []string) []string {
	if p.First <= 0 || len(args) == 0 {
		return nil
	}
	first := p.First - 1 // convert to 0-based; args here excludes command name
	last := p.Last - 1
	if last == -2 {
		last = len(args) - 1
	}
	if last < 0 || last >= len(args) {
		last = len(args) - 1
	}
	if first > last {
		return nil
	}
	step := p.Step
	if step <= 0 {
		step = 1
	}
	out := make([]string, 0, (last-first)/step+1)
	for i := first; i <= last; i += step {
		out = append(out, args[i])
	}
	return out
}

// TypeID is a 8-byte stable identifier for a custom data type. Module
// authors should pick a value derived from a well-defined namespace
// (e.g. ASCII bytes of a 9-char prefix) so two modules don't collide.
type TypeID [8]byte

// CustomType describes how the engine should marshal / unmarshal a
// module-owned value living inside a key. Required when a module wants
// its data to participate in DUMP/RESTORE, RDB snapshots, and
// cross-node MIGRATE.
type CustomType struct {
	ID   TypeID
	Name string

	// FreeFn releases any external resources held by the value. The
	// engine calls it on key delete + eviction. Optional.
	FreeFn func(value any)

	// Marshal encodes the value to a portable byte slice. Required.
	Marshal func(value any) ([]byte, error)

	// Unmarshal decodes a previously-marshaled byte slice. Required.
	Unmarshal func(b []byte) (any, error)

	// MemUsage reports approximate bytes the value occupies. Used by
	// MEMORY USAGE / eviction heuristics. Optional; defaults to the
	// marshaled size when absent.
	MemUsage func(value any) int64
}

// MakeTypeID constructs a TypeID from up to 8 ASCII characters.
// Trailing bytes are zero-padded.
func MakeTypeID(s string) TypeID {
	var id TypeID
	for i := 0; i < len(s) && i < 8; i++ {
		id[i] = s[i]
	}
	return id
}
