// Package echo is a tiny demonstration module exercising every leg of
// the module ABI: a no-key command, a key-routed command, a custom
// data type with marshal/unmarshal, and a Shutdown hook. Operators
// activate it with `MODULE LOAD echo`.
//
// Real-world modules (RedisJSON, RediSearch, …) follow the same shape:
// a package-init RegisterAvailable, a single-purpose Module value, and
// per-command handlers using ctx.Reply + ctx.Engine.
package echo

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// typeID is a stable 8-byte tag for our custom data type. We use ASCII
// "modecho!" so it's recognisable in DUMP output and stays unique.
var typeID = modules.MakeTypeID("modecho!")

// pingCount is exposed via MOD.STATS so the example also shows how a
// module can hold per-process state without abusing the keyspace.
var pingCount uint64

// Echo is the module value. We hold no other state on the struct
// itself — Init does all the registration.
var Echo = modules.Module{
	Name:        "echo",
	Version:     "1.0.0",
	Description: "Demonstration module — exercises commands, types, and lifecycle hooks",
	Init:        initModule,
	Shutdown:    func() error { atomic.StoreUint64(&pingCount, 0); return nil },
}

// init wires the module into the available pool so MODULE LOAD echo works.
// We rely on Go's package init order — main → engine.New → modules
// package — to guarantee availableModules has us before the engine
// reads the load list from config.
func init() { modules.RegisterAvailable(Echo) }

// payload is the value the custom type stores. Anything JSON-serialisable
// works; modules typically pick a binary format for efficiency.
type payload struct {
	Message  string    `json:"m"`
	StoredAt time.Time `json:"t"`
}

func initModule(ctx *modules.RegisterCtx) error {
	if err := ctx.RegisterType(modules.CustomType{
		ID: typeID, Name: "ModEchoString",
		Marshal: func(v any) ([]byte, error) {
			return json.Marshal(v.(*payload))
		},
		Unmarshal: func(b []byte) (any, error) {
			var p payload
			if err := json.Unmarshal(b, &p); err != nil {
				return nil, err
			}
			return &p, nil
		},
		MemUsage: func(v any) int64 {
			if p, ok := v.(*payload); ok {
				return int64(len(p.Message)) + 32
			}
			return 32
		},
	}); err != nil {
		return err
	}

	// MOD.PING — keyless. Demonstrates writing a SimpleString reply
	// and bumping module-local counters from a hot path.
	if err := ctx.RegisterCmd(modules.Cmd{
		Name: "MOD.PING", Arity: 1,
		Categories:  []string{acl.CatFast, acl.CatRead},
		KeyPosition: modules.KeyNone,
		Run: func(c *modules.Ctx, _ []string) error {
			atomic.AddUint64(&pingCount, 1)
			c.Reply.SimpleString("MODPONG")
			return nil
		},
	}); err != nil {
		return err
	}

	// MOD.SET key value — single-key write. Stores a *payload via the
	// engine's custom-value API so the data participates in TTL +
	// eviction without a special path.
	if err := ctx.RegisterCmd(modules.Cmd{
		Name: "MOD.SET", Arity: 3, Write: true,
		Categories:  []string{acl.CatFast, acl.CatWrite},
		KeyPosition: modules.KeyAt(1),
		Run: func(c *modules.Ctx, args []string) error {
			if len(args) < 2 {
				c.Reply.Error("MOD.SET requires key + value")
				return nil
			}
			if err := c.Engine.SetCustomValue(args[0], typeID, &payload{
				Message: args[1], StoredAt: time.Now(),
			}, 0); err != nil {
				c.Reply.Error(err.Error())
				return nil
			}
			c.Reply.SimpleString("OK")
			return nil
		},
	}); err != nil {
		return err
	}

	// MOD.GET key — single-key read. Surfaces both the stored message
	// and the wall-clock time it was set, so the round-trip through
	// marshal+unmarshal is observable.
	if err := ctx.RegisterCmd(modules.Cmd{
		Name: "MOD.GET", Arity: 2,
		Categories:  []string{acl.CatFast, acl.CatRead},
		KeyPosition: modules.KeyAt(1),
		Run: func(c *modules.Ctx, args []string) error {
			v, ok, err := c.Engine.GetCustomValue(args[0], typeID)
			if err != nil {
				c.Reply.Error(err.Error())
				return nil
			}
			if !ok {
				c.Reply.Nil()
				return nil
			}
			p := v.(*payload)
			c.Reply.Array([]any{
				"message", p.Message,
				"stored_at", p.StoredAt.Format(time.RFC3339),
			})
			return nil
		},
	}); err != nil {
		return err
	}

	// MOD.DEL key — companion mutation, registered as a write so the
	// AOF + replication backlog capture it for replay.
	if err := ctx.RegisterCmd(modules.Cmd{
		Name: "MOD.DEL", Arity: 2, Write: true,
		Categories:  []string{acl.CatFast, acl.CatWrite},
		KeyPosition: modules.KeyAt(1),
		Run: func(c *modules.Ctx, args []string) error {
			if c.Engine.DelCustomValue(args[0]) {
				c.Reply.Int(1)
			} else {
				c.Reply.Int(0)
			}
			return nil
		},
	}); err != nil {
		return err
	}

	// MOD.STATS — module observability. Shows how a module surfaces
	// internal counters without needing a dashboard wire.
	if err := ctx.RegisterCmd(modules.Cmd{
		Name: "MOD.STATS", Arity: 1,
		Categories:  []string{acl.CatFast, acl.CatRead},
		KeyPosition: modules.KeyNone,
		Run: func(c *modules.Ctx, _ []string) error {
			c.Reply.Array([]any{
				"pings", int64(atomic.LoadUint64(&pingCount)),
			})
			return nil
		},
	}); err != nil {
		return err
	}

	return nil
}

// silence the unused-import alias in builds that strip strings.
var _ = strings.ToLower
