// Package jsonmod implements the JSON.* command surface as a NeuroCache
// module. JSON values live as ordinary `any` Go trees inside the
// engine's module-typed key entries; addressing happens via a
// JSONPath subset compatible with the Redis JSON v2 protocol.
//
// JSONPath subset:
//
//   $                root
//   $.field          object member
//   $["field"]       quoted member (handles dots, special chars)
//   $.field.sub      nested members
//   $[0]             array index (negatives count from end)
//   $[*]             every array element
//   $.*              every object value
//   $..field         recursive descent — walks the whole subtree
//
// Filter expressions like $..price[?(@.qty > 0)] are deliberately not
// implemented in this module; the parser will reject them with a
// clear error so callers know to use JSON.GET against full subtrees
// and filter client-side.
package jsonmod

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// segment is one step of a parsed path. Exactly one of the index/key
// fields is set per segment kind.
type segment struct {
	kind   segKind
	key    string     // member name for kKey / kRecursive
	idx    int        // array index for kIndex
	filter *predicate // for kFilter — boolean expression evaluated per element
}

type segKind int

const (
	kRoot segKind = iota
	kKey
	kWildcardKey   // $.*
	kIndex         // $[0]
	kWildcardIndex // $[*]
	kRecursive     // $..field
	kFilter        // $[?(@.field op value)]
)

// Path is a parsed JSONPath. Compile once via parsePath, evaluate many
// times via Get/Each/Mutate.
type Path struct {
	raw      string
	segments []segment
	root     bool // true when path is exactly "$" or "."
}

// parsePath turns a textual path into a Path. We accept either the
// classic Redis form (".field.x" leading dot, no $) or the v2 form
// ("$.field.x"); both are produced by drivers in the wild.
func parsePath(raw string) (Path, error) {
	if raw == "" {
		raw = "$"
	}
	p := Path{raw: raw}
	src := raw
	// strip leading "$" if present
	if src[0] == '$' {
		src = src[1:]
	}
	if src == "" || src == "." {
		p.root = true
		return p, nil
	}
	i := 0
	for i < len(src) {
		c := src[i]
		switch c {
		case '.':
			i++
			if i < len(src) && src[i] == '.' {
				// recursive descent: ..name
				i++
				name, n, err := readIdent(src[i:])
				if err != nil {
					return p, err
				}
				p.segments = append(p.segments, segment{kind: kRecursive, key: name})
				i += n
				continue
			}
			if i < len(src) && src[i] == '*' {
				p.segments = append(p.segments, segment{kind: kWildcardKey})
				i++
				continue
			}
			name, n, err := readIdent(src[i:])
			if err != nil {
				return p, err
			}
			p.segments = append(p.segments, segment{kind: kKey, key: name})
			i += n
		case '[':
			end := strings.IndexByte(src[i:], ']')
			if end < 0 {
				return p, errors.New("unclosed [")
			}
			body := strings.TrimSpace(src[i+1 : i+end])
			i += end + 1
			switch {
			case body == "*":
				p.segments = append(p.segments, segment{kind: kWildcardIndex})
			case len(body) >= 2 && (body[0] == '"' && body[len(body)-1] == '"' ||
				body[0] == '\'' && body[len(body)-1] == '\''):
				p.segments = append(p.segments, segment{kind: kKey, key: body[1 : len(body)-1]})
			default:
				if strings.HasPrefix(body, "?") {
					expr := strings.TrimPrefix(body, "?")
					expr = strings.TrimPrefix(expr, "(")
					expr = strings.TrimSuffix(expr, ")")
					pred, err := parsePredicate(expr)
					if err != nil {
						return p, fmt.Errorf("filter parse: %w", err)
					}
					p.segments = append(p.segments, segment{kind: kFilter, filter: pred})
					break
				}
				idx, err := strconv.Atoi(body)
				if err != nil {
					return p, fmt.Errorf("invalid bracket subscript: %s", body)
				}
				p.segments = append(p.segments, segment{kind: kIndex, idx: idx})
			}
		default:
			return p, fmt.Errorf("unexpected character %q at offset %d", c, i)
		}
	}
	return p, nil
}

// readIdent scans a bareword identifier (letters, digits, underscore,
// hyphen). Returns the identifier and how many bytes were consumed.
func readIdent(s string) (string, int, error) {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '.' || c == '[' {
			break
		}
		if c == '_' || c == '-' || c == '$' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return "", 0, errors.New("expected identifier")
	}
	return s[:i], i, nil
}

// Get walks the path and returns every matching value. The result
// slice preserves document order so JSON.GET output is stable.
func (p Path) Get(root any) []any {
	if p.root {
		return []any{root}
	}
	out := []any{root}
	for _, seg := range p.segments {
		out = stepAll(out, seg)
		if len(out) == 0 {
			return out
		}
	}
	return out
}

// stepAll applies one segment to every value in the input set.
func stepAll(in []any, seg segment) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, step(v, seg)...)
	}
	return out
}

func step(v any, seg segment) []any {
	switch seg.kind {
	case kKey:
		if obj, ok := v.(map[string]any); ok {
			if val, ok := obj[seg.key]; ok {
				return []any{val}
			}
		}
		return nil
	case kWildcardKey:
		if obj, ok := v.(map[string]any); ok {
			out := make([]any, 0, len(obj))
			for _, val := range obj {
				out = append(out, val)
			}
			return out
		}
		return nil
	case kIndex:
		if arr, ok := v.([]any); ok {
			idx := seg.idx
			if idx < 0 {
				idx += len(arr)
			}
			if idx >= 0 && idx < len(arr) {
				return []any{arr[idx]}
			}
		}
		return nil
	case kWildcardIndex:
		if arr, ok := v.([]any); ok {
			out := make([]any, 0, len(arr))
			out = append(out, arr...)
			return out
		}
		return nil
	case kRecursive:
		out := []any{}
		walkRecursive(v, seg.key, &out)
		return out
	case kFilter:
		// Filter only operates on collections. For arrays, keep elements
		// the predicate matches; for objects, keep values whose
		// predicate evaluates true. Anything else falls through to nil.
		if arr, ok := v.([]any); ok {
			out := make([]any, 0, len(arr))
			for _, el := range arr {
				if seg.filter.Eval(el) {
					out = append(out, el)
				}
			}
			return out
		}
		if obj, ok := v.(map[string]any); ok {
			out := []any{}
			for _, val := range obj {
				if seg.filter.Eval(val) {
					out = append(out, val)
				}
			}
			return out
		}
		return nil
	}
	return nil
}

func walkRecursive(v any, name string, out *[]any) {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[name]; ok {
			*out = append(*out, val)
		}
		for _, val := range x {
			walkRecursive(val, name, out)
		}
	case []any:
		for _, val := range x {
			walkRecursive(val, name, out)
		}
	}
}

// Mutate finds the parent of the matched value and applies fn to it.
// If the path matches the document root, fn is called on root itself
// (and the engine should swap the whole document with the returned
// value). Returns the count of mutations applied.
func (p Path) Mutate(root any, fn func(parent any, lastKey string, lastIdx int, isRoot bool) (newValue any, mutated bool)) (any, int) {
	if p.root {
		newRoot, ok := fn(nil, "", 0, true)
		if !ok {
			return root, 0
		}
		return newRoot, 1
	}
	count := 0
	mutateRec(root, p.segments, 0, fn, &count)
	return root, count
}

// mutateRec walks segments and invokes fn at the leaf parents.
func mutateRec(node any, segs []segment, depth int, fn func(any, string, int, bool) (any, bool), count *int) {
	if depth == len(segs)-1 {
		seg := segs[depth]
		switch seg.kind {
		case kKey:
			if obj, ok := node.(map[string]any); ok {
				if newV, ok := fn(node, seg.key, 0, false); ok {
					if newV == removeMarker {
						delete(obj, seg.key)
					} else {
						obj[seg.key] = newV
					}
					*count++
				}
			}
		case kWildcardKey:
			if obj, ok := node.(map[string]any); ok {
				for k := range obj {
					if newV, ok := fn(node, k, 0, false); ok {
						if newV == removeMarker {
							delete(obj, k)
						} else {
							obj[k] = newV
						}
						*count++
					}
				}
			}
		case kIndex:
			if arr, ok := node.([]any); ok {
				idx := seg.idx
				if idx < 0 {
					idx += len(arr)
				}
				if idx >= 0 && idx < len(arr) {
					if newV, ok := fn(node, "", idx, false); ok {
						arr[idx] = newV
						*count++
					}
				}
			}
		case kWildcardIndex:
			if arr, ok := node.([]any); ok {
				for i := range arr {
					if newV, ok := fn(node, "", i, false); ok {
						arr[i] = newV
						*count++
					}
				}
			}
		case kRecursive:
			walkRecursiveMutate(node, seg.key, fn, count)
		}
		return
	}
	// recurse into the child the segment points at
	seg := segs[depth]
	switch seg.kind {
	case kKey:
		if obj, ok := node.(map[string]any); ok {
			if child, ok := obj[seg.key]; ok {
				mutateRec(child, segs, depth+1, fn, count)
			}
		}
	case kWildcardKey:
		if obj, ok := node.(map[string]any); ok {
			for _, child := range obj {
				mutateRec(child, segs, depth+1, fn, count)
			}
		}
	case kIndex:
		if arr, ok := node.([]any); ok {
			idx := seg.idx
			if idx < 0 {
				idx += len(arr)
			}
			if idx >= 0 && idx < len(arr) {
				mutateRec(arr[idx], segs, depth+1, fn, count)
			}
		}
	case kWildcardIndex:
		if arr, ok := node.([]any); ok {
			for _, child := range arr {
				mutateRec(child, segs, depth+1, fn, count)
			}
		}
	}
}

func walkRecursiveMutate(node any, name string, fn func(any, string, int, bool) (any, bool), count *int) {
	switch x := node.(type) {
	case map[string]any:
		if _, ok := x[name]; ok {
			if newV, ok := fn(x, name, 0, false); ok {
				if newV == removeMarker {
					delete(x, name)
				} else {
					x[name] = newV
				}
				*count++
			}
		}
		for _, child := range x {
			walkRecursiveMutate(child, name, fn, count)
		}
	case []any:
		for _, child := range x {
			walkRecursiveMutate(child, name, fn, count)
		}
	}
}

// removeMarker is returned by mutate callbacks that want to delete
// the matched member instead of replace it.
var removeMarker = struct{ remove bool }{remove: true}
