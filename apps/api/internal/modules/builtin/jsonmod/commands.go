package jsonmod

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// typeID identifies the module-owned data type carrying *Doc values.
var typeID = modules.MakeTypeID("rejson1!")

// JSONModule is the registration entry point. main wires it via a
// side-effect import of internal/modules/builtin/jsonmod.
var JSONModule = modules.Module{
	Name:        "json",
	Version:     "2.0.0",
	Description: "RedisJSON-compatible document type with JSONPath subset",
	Init:        initModule,
}

func init() { modules.RegisterAvailable(JSONModule) }

func initModule(ctx *modules.RegisterCtx) error {
	if err := ctx.RegisterType(modules.CustomType{
		ID: typeID, Name: "ReJSON-RL",
		Marshal: func(v any) ([]byte, error) { return v.(*Doc).Marshal() },
		Unmarshal: func(b []byte) (any, error) { return New(b) },
		MemUsage: func(v any) int64 {
			if d, ok := v.(*Doc); ok {
				if b, err := d.Marshal(); err == nil {
					return int64(len(b)) + 32
				}
			}
			return 64
		},
	}); err != nil {
		return err
	}

	register := func(name string, arity int, write bool, keyPos modules.KeyPosition, run func(*modules.Ctx, []string) error) error {
		cats := []string{acl.CatRead, acl.CatFast}
		if write {
			cats = []string{acl.CatWrite, acl.CatFast}
		}
		return ctx.RegisterCmd(modules.Cmd{
			Name: name, Arity: arity, Write: write,
			Categories: cats, KeyPosition: keyPos, Run: run,
		})
	}

	cmds := []struct {
		name   string
		arity  int
		write  bool
		key    modules.KeyPosition
		run    func(*modules.Ctx, []string) error
	}{
		{"JSON.SET", -4, true, modules.KeyAt(1), jsonSet},
		{"JSON.GET", -2, false, modules.KeyAt(1), jsonGet},
		{"JSON.DEL", -2, true, modules.KeyAt(1), jsonDel},
		{"JSON.FORGET", -2, true, modules.KeyAt(1), jsonDel},
		{"JSON.TYPE", -2, false, modules.KeyAt(1), jsonType},
		{"JSON.NUMINCRBY", 4, true, modules.KeyAt(1), jsonNumIncrBy},
		{"JSON.NUMMULTBY", 4, true, modules.KeyAt(1), jsonNumMultBy},
		{"JSON.STRAPPEND", -3, true, modules.KeyAt(1), jsonStrAppend},
		{"JSON.STRLEN", -2, false, modules.KeyAt(1), jsonStrLen},
		{"JSON.ARRAPPEND", -4, true, modules.KeyAt(1), jsonArrAppend},
		{"JSON.ARRINSERT", -5, true, modules.KeyAt(1), jsonArrInsert},
		{"JSON.ARRLEN", -2, false, modules.KeyAt(1), jsonArrLen},
		{"JSON.ARRPOP", -2, true, modules.KeyAt(1), jsonArrPop},
		{"JSON.ARRTRIM", 5, true, modules.KeyAt(1), jsonArrTrim},
		{"JSON.OBJKEYS", -2, false, modules.KeyAt(1), jsonObjKeys},
		{"JSON.OBJLEN", -2, false, modules.KeyAt(1), jsonObjLen},
		{"JSON.TOGGLE", 3, true, modules.KeyAt(1), jsonToggle},
		{"JSON.CLEAR", -2, true, modules.KeyAt(1), jsonClear},
		{"JSON.RESP", -2, false, modules.KeyAt(1), jsonResp},
		{"JSON.MGET", -3, false, modules.KeyRange(1, -1, 1), jsonMGet},
		{"JSON.MSET", -4, true, modules.KeyRange(1, -1, 3), jsonMSet},
	}
	for _, c := range cmds {
		if err := register(c.name, c.arity, c.write, c.key, c.run); err != nil {
			return err
		}
	}
	return nil
}

// loadDoc fetches the JSON doc at key. ok=false when the key is missing.
func loadDoc(c *modules.Ctx, key string) (*Doc, bool, error) {
	v, ok, err := c.Engine.GetCustomValue(key, typeID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return v.(*Doc), true, nil
}

func saveDoc(c *modules.Ctx, key string, d *Doc) error {
	return c.Engine.SetCustomValue(key, typeID, d, 0)
}

// JSON.SET key path value [NX|XX]
func jsonSet(c *modules.Ctx, args []string) error {
	if len(args) < 3 {
		c.Reply.Error("wrong number of arguments for 'json.set'")
		return nil
	}
	key, pathStr, raw := args[0], args[1], args[2]
	nx, xx := false, false
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "NX":
			nx = true
		case "XX":
			xx = true
		}
	}
	doc, exists, err := loadDoc(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if nx && exists {
		c.Reply.Nil()
		return nil
	}
	if xx && !exists {
		c.Reply.Nil()
		return nil
	}
	value, err := New([]byte(raw))
	if err != nil {
		c.Reply.Error("invalid JSON: " + err.Error())
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if !exists || path.root {
		newDoc := value
		if exists && !path.root {
			newDoc = doc
		}
		if path.root || !exists {
			if err := saveDoc(c, key, value); err != nil {
				c.Reply.Error(err.Error())
				return nil
			}
		} else {
			path.Mutate(newDoc.Root, func(_ any, _ string, _ int, _ bool) (any, bool) {
				return value.Root, true
			})
			_ = saveDoc(c, key, newDoc)
		}
		c.Reply.SimpleString("OK")
		return nil
	}
	// path is non-root and doc exists — set at the path
	mutations := 0
	_, mutations = path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		return value.Root, true
	})
	if mutations == 0 {
		// path didn't exist — create it if its parent does (single-segment add)
		if !appendIntoParent(doc.Root, path, value.Root) {
			c.Reply.Nil()
			return nil
		}
	}
	if err := saveDoc(c, key, doc); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	c.Reply.SimpleString("OK")
	return nil
}

// appendIntoParent supports JSON.SET creating a single missing object
// member ("$.foo" on a doc that has no foo). It walks all but the
// final segment, then sets the new member if the parent is an object.
func appendIntoParent(root any, p Path, newVal any) bool {
	if len(p.segments) == 0 {
		return false
	}
	last := p.segments[len(p.segments)-1]
	if last.kind != kKey {
		return false
	}
	parents := []any{root}
	for _, seg := range p.segments[:len(p.segments)-1] {
		parents = stepAll(parents, seg)
		if len(parents) == 0 {
			return false
		}
	}
	for _, par := range parents {
		if obj, ok := par.(map[string]any); ok {
			obj[last.key] = newVal
			return true
		}
	}
	return false
}

// JSON.GET key [INDENT i] [NEWLINE n] [SPACE s] [path ...]
func jsonGet(c *modules.Ctx, args []string) error {
	if len(args) < 1 {
		c.Reply.Error("wrong number of arguments for 'json.get'")
		return nil
	}
	key := args[0]
	indent, newline, space := "", "", ""
	paths := []string{}
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "INDENT":
			if i+1 < len(args) {
				indent = args[i+1]
				i++
			}
		case "NEWLINE":
			if i+1 < len(args) {
				newline = args[i+1]
				i++
			}
		case "SPACE":
			if i+1 < len(args) {
				space = args[i+1]
				i++
			}
		default:
			paths = append(paths, args[i])
		}
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if !ok {
		c.Reply.Nil()
		return nil
	}
	if len(paths) == 0 {
		paths = []string{"$"}
	}
	// One path → bare value (Redis JSON v2 wraps in an array; we follow that for $-paths).
	if len(paths) == 1 {
		path, err := parsePath(paths[0])
		if err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
		results := path.Get(doc.Root)
		if !strings.HasPrefix(paths[0], "$") {
			// legacy ".path" form returns the bare value
			if len(results) == 0 {
				c.Reply.Nil()
				return nil
			}
			out, _ := marshalWith(results[0], indent, newline, space)
			c.Reply.Bulk(out)
			return nil
		}
		out, _ := marshalWith(results, indent, newline, space)
		c.Reply.Bulk(out)
		return nil
	}
	// Multi-path → object keyed by path.
	out := map[string]any{}
	for _, p := range paths {
		path, err := parsePath(p)
		if err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
		results := path.Get(doc.Root)
		if strings.HasPrefix(p, "$") {
			out[p] = results
		} else {
			if len(results) == 0 {
				out[p] = nil
			} else {
				out[p] = results[0]
			}
		}
	}
	body, _ := marshalWith(out, indent, newline, space)
	c.Reply.Bulk(body)
	return nil
}

func marshalWith(v any, indent, newline, space string) (string, error) {
	if indent == "" && newline == "" && space == "" {
		b, err := json.Marshal(v)
		return string(b), err
	}
	b, err := json.MarshalIndent(v, "", indent)
	if err != nil {
		return "", err
	}
	out := string(b)
	if space != "" {
		out = strings.ReplaceAll(out, ": ", ":"+space)
	}
	if newline != "\n" && newline != "" {
		out = strings.ReplaceAll(out, "\n", newline)
	}
	return out, nil
}

// JSON.DEL key [path]
func jsonDel(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if path.root {
		c.Engine.DelCustomValue(key)
		c.Reply.Int(1)
		return nil
	}
	_, n := path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		if obj, ok := parent.(map[string]any); ok {
			delete(obj, k)
			return removeMarker, false // already deleted; tell caller no replacement
		}
		if arr, ok := parent.([]any); ok {
			_ = arr // arrays: we can't remove via callback, skip
		}
		return nil, false
	})
	// For arrays, drop the element via a second pass.
	deleteFromArrays(doc.Root, path)
	if n > 0 {
		_ = saveDoc(c, key, doc)
	}
	c.Reply.Int(int64(n))
	return nil
}

// deleteFromArrays handles array-element removal when the leaf segment
// is an index — the Mutate callback can't shrink the parent slice in
// place, so we patch arrays here.
func deleteFromArrays(root any, p Path) {
	if len(p.segments) == 0 {
		return
	}
	last := p.segments[len(p.segments)-1]
	if last.kind != kIndex && last.kind != kWildcardIndex {
		return
	}
	parents := []any{root}
	for _, seg := range p.segments[:len(p.segments)-1] {
		parents = stepAll(parents, seg)
	}
	for _, par := range parents {
		arr, ok := par.([]any)
		if !ok {
			continue
		}
		switch last.kind {
		case kIndex:
			idx := last.idx
			if idx < 0 {
				idx += len(arr)
			}
			if idx >= 0 && idx < len(arr) {
				replaceParentArray(root, p.segments[:len(p.segments)-1], append(arr[:idx], arr[idx+1:]...))
			}
		case kWildcardIndex:
			replaceParentArray(root, p.segments[:len(p.segments)-1], arr[:0])
		}
	}
}

// replaceParentArray walks segs and overwrites the slice the leaf
// points at. Hacky but contained — JSON arrays are the only spot where
// a callback-style mutate falls short.
func replaceParentArray(root any, segs []segment, newSlice []any) {
	if len(segs) == 0 {
		return
	}
	parents := []any{root}
	for _, seg := range segs[:len(segs)-1] {
		parents = stepAll(parents, seg)
	}
	last := segs[len(segs)-1]
	for _, par := range parents {
		switch last.kind {
		case kKey:
			if obj, ok := par.(map[string]any); ok {
				obj[last.key] = newSlice
			}
		case kIndex:
			if arr, ok := par.([]any); ok {
				idx := last.idx
				if idx < 0 {
					idx += len(arr)
				}
				if idx >= 0 && idx < len(arr) {
					arr[idx] = newSlice
				}
			}
		}
	}
}

// JSON.TYPE key [path]
func jsonType(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if !ok {
		c.Reply.NilArray()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := path.Get(doc.Root)
	out := make([]any, len(results))
	for i, v := range results {
		out[i] = kindOf(v)
	}
	c.Reply.Array(out)
	return nil
}

// JSON.NUMINCRBY key path delta
func jsonNumIncrBy(c *modules.Ctx, args []string) error {
	return numericMutate(c, args, false)
}

// JSON.NUMMULTBY key path delta
func jsonNumMultBy(c *modules.Ctx, args []string) error {
	return numericMutate(c, args, true)
}

func numericMutate(c *modules.Ctx, args []string, mult bool) error {
	key, pathStr, deltaStr := args[0], args[1], args[2]
	doc, ok, err := loadDoc(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if !ok {
		c.Reply.Error("ERR no such key")
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	deltaInt, deltaFlt, deltaIsInt, err := asNumber(deltaToNumber(deltaStr))
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		var cur any
		switch p := parent.(type) {
		case map[string]any:
			cur = p[k]
		case []any:
			cur = p[idx]
		}
		curInt, curFlt, curIsInt, err := asNumber(cur)
		if err != nil {
			return nil, false
		}
		var newNum json.Number
		if mult {
			if curIsInt && deltaIsInt {
				newNum = toJSONNumber(curInt*deltaInt, 0, true)
			} else {
				cf, df := toFloat(curInt, curFlt, curIsInt), toFloat(deltaInt, deltaFlt, deltaIsInt)
				newNum = toJSONNumber(0, cf*df, false)
			}
		} else {
			if curIsInt && deltaIsInt {
				newNum = toJSONNumber(curInt+deltaInt, 0, true)
			} else {
				cf, df := toFloat(curInt, curFlt, curIsInt), toFloat(deltaInt, deltaFlt, deltaIsInt)
				newNum = toJSONNumber(0, cf+df, false)
			}
		}
		results = append(results, newNum)
		return newNum, true
	})
	if n == 0 {
		c.Reply.Error("ERR could not perform this operation on a key that doesn't exist")
		return nil
	}
	_ = saveDoc(c, key, doc)
	body, _ := json.Marshal(results)
	c.Reply.Bulk(string(body))
	return nil
}

func deltaToNumber(s string) any {
	return json.Number(s)
}

func toFloat(i int64, f float64, isInt bool) float64 {
	if isInt {
		return float64(i)
	}
	return f
}

// JSON.STRAPPEND key [path] value
func jsonStrAppend(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr, raw := "$", args[1]
	if len(args) >= 3 {
		pathStr, raw = args[1], args[2]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Error("ERR no such key")
		return nil
	}
	addDoc, err := New([]byte(raw))
	if err != nil {
		c.Reply.Error("invalid JSON for value: " + err.Error())
		return nil
	}
	addStr, err := asString(addDoc.Root)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		var cur any
		if isRoot {
			cur = doc.Root
		} else {
			switch p := parent.(type) {
			case map[string]any:
				cur = p[k]
			case []any:
				cur = p[idx]
			}
		}
		s, err := asString(cur)
		if err != nil {
			return nil, false
		}
		nv := s + addStr
		results = append(results, int64(len(nv)))
		return nv, true
	})
	if n == 0 {
		c.Reply.Nil()
		return nil
	}
	_ = saveDoc(c, key, doc)
	c.Reply.Array(results)
	return nil
}

// JSON.STRLEN key [path]
func jsonStrLen(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.NilArray()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := path.Get(doc.Root)
	out := make([]any, 0, len(results))
	for _, v := range results {
		s, err := asString(v)
		if err != nil {
			out = append(out, nil)
			continue
		}
		out = append(out, int64(len(s)))
	}
	c.Reply.Array(out)
	return nil
}

// JSON.ARRAPPEND key path value [value ...]
func jsonArrAppend(c *modules.Ctx, args []string) error {
	key, pathStr := args[0], args[1]
	values := args[2:]
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Error("ERR no such key")
		return nil
	}
	parsed := make([]any, len(values))
	for i, v := range values {
		d, err := New([]byte(v))
		if err != nil {
			c.Reply.Error("invalid JSON: " + err.Error())
			return nil
		}
		parsed[i] = d.Root
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := arrayMutate(doc.Root, path, func(arr []any) ([]any, any) {
		nv := append(arr, parsed...)
		return nv, int64(len(nv))
	}, &results)
	if n == 0 {
		c.Reply.Nil()
		return nil
	}
	_ = saveDoc(c, key, doc)
	c.Reply.Array(results)
	return nil
}

// JSON.ARRINSERT key path index value [value ...]
func jsonArrInsert(c *modules.Ctx, args []string) error {
	key, pathStr := args[0], args[1]
	idx, err := strconv.Atoi(args[2])
	if err != nil {
		c.Reply.Error("invalid index")
		return nil
	}
	values := args[3:]
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Error("ERR no such key")
		return nil
	}
	parsed := make([]any, len(values))
	for i, v := range values {
		d, err := New([]byte(v))
		if err != nil {
			c.Reply.Error("invalid JSON: " + err.Error())
			return nil
		}
		parsed[i] = d.Root
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := arrayMutate(doc.Root, path, func(arr []any) ([]any, any) {
		ix := idx
		if ix < 0 {
			ix += len(arr)
		}
		if ix < 0 || ix > len(arr) {
			return arr, int64(-1)
		}
		out := make([]any, 0, len(arr)+len(parsed))
		out = append(out, arr[:ix]...)
		out = append(out, parsed...)
		out = append(out, arr[ix:]...)
		return out, int64(len(out))
	}, &results)
	if n == 0 {
		c.Reply.Nil()
		return nil
	}
	_ = saveDoc(c, key, doc)
	c.Reply.Array(results)
	return nil
}

// JSON.ARRLEN key [path]
func jsonArrLen(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.NilArray()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := path.Get(doc.Root)
	out := make([]any, 0, len(results))
	for _, v := range results {
		if arr, ok := v.([]any); ok {
			out = append(out, int64(len(arr)))
		} else {
			out = append(out, nil)
		}
	}
	c.Reply.Array(out)
	return nil
}

// JSON.ARRPOP key [path [index]]
func jsonArrPop(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	idx := -1
	if len(args) >= 2 {
		pathStr = args[1]
	}
	if len(args) >= 3 {
		i, err := strconv.Atoi(args[2])
		if err != nil {
			c.Reply.Error("invalid index")
			return nil
		}
		idx = i
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Nil()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := arrayMutate(doc.Root, path, func(arr []any) ([]any, any) {
		if len(arr) == 0 {
			return arr, nil
		}
		ix := idx
		if ix < 0 {
			ix += len(arr)
		}
		if ix < 0 || ix >= len(arr) {
			return arr, nil
		}
		popped := arr[ix]
		out := append(arr[:ix], arr[ix+1:]...)
		return out, popped
	}, &results)
	if n == 0 {
		c.Reply.Nil()
		return nil
	}
	_ = saveDoc(c, key, doc)
	body, _ := json.Marshal(results)
	c.Reply.Bulk(string(body))
	return nil
}

// JSON.ARRTRIM key path start stop
func jsonArrTrim(c *modules.Ctx, args []string) error {
	key, pathStr := args[0], args[1]
	start, err := strconv.Atoi(args[2])
	if err != nil {
		c.Reply.Error("invalid start")
		return nil
	}
	stop, err := strconv.Atoi(args[3])
	if err != nil {
		c.Reply.Error("invalid stop")
		return nil
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Error("ERR no such key")
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := arrayMutate(doc.Root, path, func(arr []any) ([]any, any) {
		s, e := start, stop
		if s < 0 {
			s += len(arr)
		}
		if e < 0 {
			e += len(arr)
		}
		if s < 0 {
			s = 0
		}
		if e >= len(arr) {
			e = len(arr) - 1
		}
		if s > e {
			return arr[:0], int64(0)
		}
		out := append([]any{}, arr[s:e+1]...)
		return out, int64(len(out))
	}, &results)
	if n == 0 {
		c.Reply.Nil()
		return nil
	}
	_ = saveDoc(c, key, doc)
	c.Reply.Array(results)
	return nil
}

// JSON.OBJKEYS key [path]
func jsonObjKeys(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.NilArray()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := path.Get(doc.Root)
	out := make([]any, 0, len(results))
	for _, v := range results {
		if obj, ok := v.(map[string]any); ok {
			ks := make([]any, 0, len(obj))
			for k := range obj {
				ks = append(ks, k)
			}
			out = append(out, ks)
		} else {
			out = append(out, nil)
		}
	}
	c.Reply.Array(out)
	return nil
}

// JSON.OBJLEN key [path]
func jsonObjLen(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.NilArray()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := path.Get(doc.Root)
	out := make([]any, 0, len(results))
	for _, v := range results {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, int64(len(obj)))
		} else {
			out = append(out, nil)
		}
	}
	c.Reply.Array(out)
	return nil
}

// JSON.TOGGLE key path
func jsonToggle(c *modules.Ctx, args []string) error {
	key, pathStr := args[0], args[1]
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Error("ERR no such key")
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := []any{}
	_, n := path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		var cur any
		if isRoot {
			cur = doc.Root
		} else {
			switch p := parent.(type) {
			case map[string]any:
				cur = p[k]
			case []any:
				cur = p[idx]
			}
		}
		b, ok := cur.(bool)
		if !ok {
			return nil, false
		}
		nv := !b
		toggled := int64(0)
		if nv {
			toggled = 1
		}
		results = append(results, toggled)
		return nv, true
	})
	if n == 0 {
		c.Reply.Nil()
		return nil
	}
	_ = saveDoc(c, key, doc)
	c.Reply.Array(results)
	return nil
}

// JSON.CLEAR key [path]
func jsonClear(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.Int(0)
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	cleared := 0
	_, _ = path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		var cur any
		if isRoot {
			cur = doc.Root
		} else {
			switch p := parent.(type) {
			case map[string]any:
				cur = p[k]
			case []any:
				cur = p[idx]
			}
		}
		switch v := cur.(type) {
		case map[string]any:
			for k := range v {
				delete(v, k)
			}
			cleared++
			return v, true
		case []any:
			cleared++
			return []any{}, true
		case json.Number:
			cleared++
			return json.Number("0"), true
		case float64:
			cleared++
			return float64(0), true
		case string:
			cleared++
			return "", true
		case bool:
			cleared++
			return false, true
		}
		return nil, false
	})
	if cleared > 0 {
		_ = saveDoc(c, key, doc)
	}
	c.Reply.Int(int64(cleared))
	return nil
}

// JSON.RESP key [path] — returns the value as a RESP-shaped tree.
// Implemented as JSON encoding for simplicity; full RESP3 mapping
// would require a richer Writer ABI.
func jsonResp(c *modules.Ctx, args []string) error {
	key := args[0]
	pathStr := "$"
	if len(args) >= 2 {
		pathStr = args[1]
	}
	doc, ok, err := loadDoc(c, key)
	if err != nil || !ok {
		c.Reply.NilArray()
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	results := path.Get(doc.Root)
	body, _ := json.Marshal(results)
	c.Reply.Bulk(string(body))
	return nil
}

// JSON.MGET key [key ...] path
func jsonMGet(c *modules.Ctx, args []string) error {
	if len(args) < 2 {
		c.Reply.Error("wrong number of arguments for 'json.mget'")
		return nil
	}
	pathStr := args[len(args)-1]
	keys := args[:len(args)-1]
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		doc, ok, err := loadDoc(c, k)
		if err != nil || !ok {
			out = append(out, nil)
			continue
		}
		results := path.Get(doc.Root)
		body, _ := json.Marshal(results)
		out = append(out, string(body))
	}
	c.Reply.Array(out)
	return nil
}

// JSON.MSET key path value [key path value ...]
func jsonMSet(c *modules.Ctx, args []string) error {
	if len(args) < 3 || len(args)%3 != 0 {
		c.Reply.Error("wrong number of arguments for 'json.mset'")
		return nil
	}
	for i := 0; i+2 < len(args); i += 3 {
		key, pathStr, raw := args[i], args[i+1], args[i+2]
		val, err := New([]byte(raw))
		if err != nil {
			c.Reply.Error(fmt.Sprintf("invalid JSON for %s: %v", key, err))
			return nil
		}
		path, err := parsePath(pathStr)
		if err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
		doc, ok, _ := loadDoc(c, key)
		if !ok || path.root {
			_ = saveDoc(c, key, val)
			continue
		}
		path.Mutate(doc.Root, func(_ any, _ string, _ int, _ bool) (any, bool) {
			return val.Root, true
		})
		_ = saveDoc(c, key, doc)
	}
	c.Reply.SimpleString("OK")
	return nil
}

// arrayMutate is the shared driver for ARRAPPEND / ARRINSERT / ARRPOP /
// ARRTRIM. It walks the path, replaces the matched array via fn, and
// records the per-match result.
func arrayMutate(root any, p Path, fn func([]any) ([]any, any), out *[]any) (any, int) {
	if p.root {
		arr, ok := root.([]any)
		if !ok {
			return root, 0
		}
		nv, res := fn(arr)
		_ = nv
		*out = append(*out, res)
		return root, 1
	}
	count := 0
	last := p.segments[len(p.segments)-1]
	parents := []any{root}
	for _, seg := range p.segments[:len(p.segments)-1] {
		parents = stepAll(parents, seg)
	}
	for _, par := range parents {
		switch last.kind {
		case kKey:
			obj, ok := par.(map[string]any)
			if !ok {
				continue
			}
			arr, ok := obj[last.key].([]any)
			if !ok {
				continue
			}
			nv, res := fn(arr)
			obj[last.key] = nv
			*out = append(*out, res)
			count++
		case kIndex:
			arr, ok := par.([]any)
			if !ok {
				continue
			}
			idx := last.idx
			if idx < 0 {
				idx += len(arr)
			}
			if idx < 0 || idx >= len(arr) {
				continue
			}
			child, ok := arr[idx].([]any)
			if !ok {
				continue
			}
			nv, res := fn(child)
			arr[idx] = nv
			*out = append(*out, res)
			count++
		case kRecursive:
			// recursive arrays: walk the whole subtree.
			walkArrays(par, last.key, fn, out, &count)
		}
	}
	return root, count
}

func walkArrays(node any, name string, fn func([]any) ([]any, any), out *[]any, count *int) {
	switch x := node.(type) {
	case map[string]any:
		if arr, ok := x[name].([]any); ok {
			nv, res := fn(arr)
			x[name] = nv
			*out = append(*out, res)
			*count++
		}
		for _, child := range x {
			walkArrays(child, name, fn, out, count)
		}
	case []any:
		for _, child := range x {
			walkArrays(child, name, fn, out, count)
		}
	}
}

// silence "imported and not used" if a future refactor touches errors.
var _ = errors.New
