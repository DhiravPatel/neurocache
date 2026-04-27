package jsonmod

import (
	"encoding/json"
	"strconv"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// jsonMerge implements JSON.MERGE key path value.
//
// Semantics follow RFC 7396 (JSON Merge Patch) at the matched path:
//
//   - if the patch value is an object, every (k, v) is recursively
//     merged into the target; v == null deletes k.
//   - any other patch type (array, scalar, null) replaces the target
//     wholesale (and a top-level null deletes the key entirely).
//
// path "$" creates the key when it doesn't exist (matching Redis).
func jsonMerge(c *modules.Ctx, args []string) error {
	if len(args) < 3 {
		c.Reply.Error("wrong number of arguments for 'json.merge'")
		return nil
	}
	key, pathStr, raw := args[0], args[1], args[2]
	patch, err := New([]byte(raw))
	if err != nil {
		c.Reply.Error("invalid JSON: " + err.Error())
		return nil
	}
	path, err := parsePath(pathStr)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	doc, exists, err := loadDoc(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if !exists {
		// match Redis: missing-key + root path simply sets the value.
		if !path.root {
			c.Reply.Error("ERR no such key")
			return nil
		}
		if patch.Root == nil {
			c.Reply.SimpleString("OK")
			return nil
		}
		if err := saveDoc(c, key, patch); err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
		c.Reply.SimpleString("OK")
		return nil
	}
	if path.root {
		merged := mergePatch(doc.Root, patch.Root)
		if merged == nil {
			c.Engine.DelCustomValue(key)
			c.Reply.SimpleString("OK")
			return nil
		}
		doc.Root = merged
		_ = saveDoc(c, key, doc)
		c.Reply.SimpleString("OK")
		return nil
	}
	// non-root: merge at every match
	mutated := 0
	_, mutated = path.Mutate(doc.Root, func(parent any, k string, idx int, isRoot bool) (any, bool) {
		var cur any
		switch p := parent.(type) {
		case map[string]any:
			cur = p[k]
		case []any:
			cur = p[idx]
		}
		merged := mergePatch(cur, patch.Root)
		if merged == nil {
			return removeMarker, true
		}
		return merged, true
	})
	if mutated == 0 {
		c.Reply.Error("ERR could not perform this operation on a key that doesn't exist")
		return nil
	}
	_ = saveDoc(c, key, doc)
	c.Reply.SimpleString("OK")
	return nil
}

// mergePatch applies an RFC 7396 merge patch.
//
//   patch is an object → recursively merge into target object; null
//                        members delete keys; new members are added.
//   anything else      → replace target wholesale.
//
// A returned nil means "delete this value" (only meaningful when the
// caller is mutating its parent — see jsonMerge).
func mergePatch(target, patch any) any {
	po, isObj := patch.(map[string]any)
	if !isObj {
		return patch
	}
	to, ok := target.(map[string]any)
	if !ok {
		// target is not an object — replace it with a fresh one shaped
		// from the patch (skipping nulls per RFC 7396 §1).
		out := map[string]any{}
		for k, v := range po {
			if v == nil {
				continue
			}
			out[k] = mergePatch(nil, v)
		}
		return out
	}
	for k, v := range po {
		if v == nil {
			delete(to, k)
			continue
		}
		to[k] = mergePatch(to[k], v)
	}
	return to
}

// jsonArrIndex implements JSON.ARRINDEX key path value [start [stop]].
//
// Returns one int per matched array along the path:
//   - 0-based index of the first match in the [start, stop) window;
//   - -1 if no match was found in the window;
//   - nil if the matched value isn't an array (mirrors Redis JSON v2).
//
// start/stop default to (0, 0) which Redis treats as "search the whole
// array". Negative indices count from the array end. The match uses
// JSON value-equality, not just string equality — so ints, floats, and
// json.Number forms compare numerically and nested objects/arrays
// compare deeply.
func jsonArrIndex(c *modules.Ctx, args []string) error {
	if len(args) < 3 {
		c.Reply.Error("wrong number of arguments for 'json.arrindex'")
		return nil
	}
	key, pathStr, raw := args[0], args[1], args[2]
	start, stop := 0, 0
	if len(args) >= 4 {
		v, err := strconv.Atoi(args[3])
		if err != nil {
			c.Reply.Error("invalid start")
			return nil
		}
		start = v
	}
	if len(args) >= 5 {
		v, err := strconv.Atoi(args[4])
		if err != nil {
			c.Reply.Error("invalid stop")
			return nil
		}
		stop = v
	}
	needleDoc, err := New([]byte(raw))
	if err != nil {
		c.Reply.Error("invalid JSON for value: " + err.Error())
		return nil
	}
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
	results := path.Get(doc.Root)
	out := make([]any, 0, len(results))
	for _, v := range results {
		arr, ok := v.([]any)
		if !ok {
			out = append(out, nil)
			continue
		}
		out = append(out, int64(searchInArray(arr, needleDoc.Root, start, stop)))
	}
	c.Reply.Array(out)
	return nil
}

// searchInArray scans arr for needle in the [start, stop) half-open
// window (negative indices count from arr end; stop == 0 means "until
// the end"). Returns -1 when no match is found.
func searchInArray(arr []any, needle any, start, stop int) int {
	n := len(arr)
	a, b := start, stop
	if a < 0 {
		a += n
	}
	if a < 0 {
		a = 0
	}
	if b <= 0 {
		b = n
	} else if b > n {
		b = n
	}
	for i := a; i < b; i++ {
		if jsonDeepEqual(arr[i], needle) {
			return i
		}
	}
	return -1
}

// jsonDeepEqual extends predicate.go's jsonEqual with structural
// recursion for arrays and objects — ARRINDEX needs deep equality so
// callers can search for nested needles like {"a":1}.
func jsonDeepEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonDeepEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			yv, present := y[k]
			if !present || !jsonDeepEqual(v, yv) {
				return false
			}
		}
		return true
	}
	return jsonEqual(a, b)
}

// keep encoding/json imported in case future extras need it.
var _ = json.Number("")
