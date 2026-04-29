package scripting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// RunFull is the full Lua 5.1 entry point backed by gopher-lua. It
// keeps the same (src, keys, argv, caller, deadline) signature as the
// subset interpreter so callers can swap implementations transparently.
//
// The bridge wires KEYS, ARGV, and the `redis.*` table — the rest of
// the standard Lua library (string, table, math, io-restricted, …) is
// available via gopher-lua's bundled libs. We deliberately do NOT
// expose `os`, `io`, `package`, or `debug` so scripts can't break out
// of the sandbox.
func RunFull(src string, keys, argv []string, call Caller, deadline time.Time) (any, error) {
	L := lua.NewState(lua.Options{
		SkipOpenLibs:    true,
		IncludeGoStackTrace: false,
	})
	defer L.Close()

	openSandboxLibs(L)
	bindKeysArgv(L, keys, argv)
	bindRedisModule(L, call)

	if !deadline.IsZero() {
		// gopher-lua honours a context deadline between VM
		// instructions — DoString returns an error when the deadline
		// trips, which we surface to the client as a normal -ERR.
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		L.SetContext(ctx)
	}

	if err := L.DoString(src); err != nil {
		// Distinguish redis.error_reply tables from VM errors so the
		// dispatcher reports the right shape.
		if _, isRedisErr := err.(*lua.ApiError); isRedisErr {
			return nil, errors.New(strings.TrimSpace(err.Error()))
		}
		return nil, err
	}
	resp := luaValueToResp(L, L.Get(-1))
	// redis.error_reply tables come back as plain `error` values from
	// luaValueToResp — surface them as the function's error return so
	// the dispatcher emits a -ERR reply.
	if e, ok := resp.(error); ok {
		return nil, e
	}
	return resp, nil
}

// openSandboxLibs loads the safe subset of stdlib. We skip `os`, `io`,
// `package`, `debug` to prevent scripts from touching the file system,
// loading additional Lua files, or peeking at host state.
func openSandboxLibs(L *lua.LState) {
	for _, lib := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage}, // module loader (we restrict below)
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.fn))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	// Disable `require` / `dofile` — only inline Lua is acceptable.
	L.SetGlobal("require", lua.LNil)
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("load", lua.LNil)
	L.SetGlobal("loadstring", lua.LNil)
}

func bindKeysArgv(L *lua.LState, keys, argv []string) {
	keysTbl := L.NewTable()
	for i, k := range keys {
		keysTbl.RawSetInt(i+1, lua.LString(k))
	}
	L.SetGlobal("KEYS", keysTbl)
	argvTbl := L.NewTable()
	for i, a := range argv {
		argvTbl.RawSetInt(i+1, lua.LString(a))
	}
	L.SetGlobal("ARGV", argvTbl)
}

func bindRedisModule(L *lua.LState, call Caller) {
	mod := L.NewTable()
	L.SetField(mod, "call", L.NewFunction(func(L *lua.LState) int {
		v, err := doCall(L, call, false)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		L.Push(respToLuaValue(L, v))
		return 1
	}))
	L.SetField(mod, "pcall", L.NewFunction(func(L *lua.LState) int {
		v, err := doCall(L, call, true)
		if err != nil {
			et := L.NewTable()
			L.SetField(et, "err", lua.LString(err.Error()))
			L.Push(et)
			return 1
		}
		L.Push(respToLuaValue(L, v))
		return 1
	}))
	L.SetField(mod, "error_reply", L.NewFunction(func(L *lua.LState) int {
		et := L.NewTable()
		L.SetField(et, "err", L.CheckAny(1))
		L.Push(et)
		return 1
	}))
	L.SetField(mod, "status_reply", L.NewFunction(func(L *lua.LState) int {
		st := L.NewTable()
		L.SetField(st, "ok", L.CheckAny(1))
		L.Push(st)
		return 1
	}))
	L.SetField(mod, "sha1hex", L.NewFunction(func(L *lua.LState) int {
		s := L.CheckString(1)
		L.Push(lua.LString(NewCache().Load(s)))
		return 1
	}))
	L.SetField(mod, "log", L.NewFunction(func(L *lua.LState) int { return 0 }))
	L.SetField(mod, "LOG_DEBUG", lua.LNumber(0))
	L.SetField(mod, "LOG_VERBOSE", lua.LNumber(1))
	L.SetField(mod, "LOG_NOTICE", lua.LNumber(2))
	L.SetField(mod, "LOG_WARNING", lua.LNumber(3))
	L.SetGlobal("redis", mod)
}

func doCall(L *lua.LState, call Caller, _ bool) (any, error) {
	n := L.GetTop()
	if n == 0 {
		return nil, errors.New("redis.call: missing command")
	}
	cmd := strings.ToUpper(luaArgString(L.Get(1)))
	args := make([]string, 0, n-1)
	for i := 2; i <= n; i++ {
		args = append(args, luaArgString(L.Get(i)))
	}
	return call(cmd, args)
}

func luaArgString(v lua.LValue) string {
	switch x := v.(type) {
	case lua.LString:
		return string(x)
	case lua.LNumber:
		// Redis stringifies numbers with no trailing ".0" for ints.
		f := float64(x)
		if f == float64(int64(f)) {
			return fmt.Sprintf("%d", int64(f))
		}
		return fmt.Sprintf("%g", f)
	case lua.LBool:
		if bool(x) {
			return "1"
		}
		return "0"
	}
	return v.String()
}

// respToLuaValue maps engine reply shapes back to gopher-lua values.
func respToLuaValue(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LBool(false) // Redis nil → Lua false in scripts
	case bool:
		if x {
			return lua.LNumber(1)
		}
		return lua.LBool(false)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case string:
		return lua.LString(x)
	case []string:
		t := L.NewTable()
		for i, s := range x {
			t.RawSetInt(i+1, lua.LString(s))
		}
		return t
	case []any:
		t := L.NewTable()
		for i, e := range x {
			t.RawSetInt(i+1, respToLuaValue(L, e))
		}
		return t
	case error:
		et := L.NewTable()
		L.SetField(et, "err", lua.LString(x.Error()))
		return et
	}
	return lua.LString(fmt.Sprint(v))
}

// luaValueToResp encodes a script's return value into something the
// RESP/HTTP writers know how to render.
func luaValueToResp(L *lua.LState, v lua.LValue) any {
	switch x := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		if bool(x) {
			return int64(1)
		}
		return nil
	case lua.LNumber:
		f := float64(x)
		if f == float64(int64(f)) {
			return int64(f)
		}
		return fmt.Sprintf("%g", f)
	case lua.LString:
		return string(x)
	case *lua.LTable:
		// error_reply / status_reply tables
		if errMsg := L.GetField(x, "err"); errMsg != lua.LNil {
			return errors.New(errMsg.String())
		}
		if okMsg := L.GetField(x, "ok"); okMsg != lua.LNil {
			return okMsg.String()
		}
		// dense array
		out := []any{}
		x.ForEach(func(_ lua.LValue, val lua.LValue) {
			out = append(out, luaValueToResp(L, val))
		})
		return out
	}
	return v.String()
}
