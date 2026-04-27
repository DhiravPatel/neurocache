package scripting

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// FunctionLibrary is a named bundle of server-stored Lua functions —
// the FUNCTION LOAD / FCALL family from Redis 7. Each library declares
// an engine (we only support "LUA"), a name, and one or more functions
// addressable by name. FCALL invokes them by name with KEYS+ARGV like
// EVAL, but without re-uploading source on every call.
type FunctionLibrary struct {
	Name    string
	Engine  string
	Source  string
	Funcs   map[string]string // function name -> body source
	Replace bool
}

// FunctionRegistry holds every loaded library. One per engine.
type FunctionRegistry struct {
	mu  sync.RWMutex
	lib map[string]*FunctionLibrary    // by lib name
	fn  map[string]*registeredFunction // by function name (must be unique cluster-wide)

	calls     uint64
	errors    uint64
	totalNs   uint64
}

type registeredFunction struct {
	libName string
	name    string
	body    string
}

// NewFunctionRegistry builds an empty registry.
func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{
		lib: map[string]*FunctionLibrary{},
		fn:  map[string]*registeredFunction{},
	}
}

// Load parses source, extracts function declarations, and stores the
// library. The source format we accept:
//
//	#!lua name=mylib
//	redis.register_function('myfunc', function(keys, args) ... end)
//	redis.register_function('other', function(keys, args) ... end)
//
// When `replace` is true, an existing library with the same name is
// overwritten; otherwise we error to match Redis behaviour.
func (r *FunctionRegistry) Load(source string, replace bool) (string, error) {
	libName, funcs, err := parseFunctionLibrary(source)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.lib[libName]; exists && !replace {
		return "", fmt.Errorf("Library '%s' already exists", libName)
	}
	if existing, ok := r.lib[libName]; ok {
		// Replacing — strip the previous library's function names.
		for fname := range existing.Funcs {
			delete(r.fn, fname)
		}
	}
	lib := &FunctionLibrary{
		Name: libName, Engine: "LUA", Source: source,
		Funcs: funcs, Replace: replace,
	}
	r.lib[libName] = lib
	for fname, body := range funcs {
		// Function names are global across libraries — duplicate names
		// from a different library are a hard error.
		if existing, dup := r.fn[fname]; dup && existing.libName != libName {
			return "", fmt.Errorf("Function '%s' already exists in library '%s'", fname, existing.libName)
		}
		r.fn[fname] = &registeredFunction{libName: libName, name: fname, body: body}
	}
	return libName, nil
}

// Delete removes a library and every function it owned.
func (r *FunctionRegistry) Delete(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lib, ok := r.lib[name]
	if !ok {
		return fmt.Errorf("Library not found: %s", name)
	}
	for fname := range lib.Funcs {
		delete(r.fn, fname)
	}
	delete(r.lib, name)
	return nil
}

// Flush wipes every loaded library.
func (r *FunctionRegistry) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lib = map[string]*FunctionLibrary{}
	r.fn = map[string]*registeredFunction{}
}

// List returns library metadata in stable order.
func (r *FunctionRegistry) List() []*FunctionLibrary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.lib))
	for n := range r.lib {
		names = append(names, n)
	}
	sortStrings(names)
	out := make([]*FunctionLibrary, 0, len(names))
	for _, n := range names {
		out = append(out, r.lib[n])
	}
	return out
}

// LookupFunction resolves a function name to its body. ok=false when
// the name isn't registered.
func (r *FunctionRegistry) LookupFunction(name string) (libName, body string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rf, found := r.fn[name]
	if !found {
		return "", "", false
	}
	return rf.libName, rf.body, true
}

// Stats returns the FUNCTION STATS counters.
func (r *FunctionRegistry) Stats() (calls, errs, totalNs uint64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.calls, r.errors, r.totalNs
}

// RecordCall is invoked by the dispatcher on every FCALL completion.
func (r *FunctionRegistry) RecordCall(durNs uint64, err bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.totalNs += durNs
	if err {
		r.errors++
	}
}

// parseFunctionLibrary extracts the library name from the shebang and
// every `redis.register_function('name', ...)` declaration. The
// extracted body is the substring between the function's opening
// `function(...)` and its matching `end` — close enough to feed our
// existing scripting interpreter via Run().
func parseFunctionLibrary(src string) (string, map[string]string, error) {
	libName := ""
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#!") {
			// `#!lua name=mylib`
			fields := strings.Fields(trim)
			for _, f := range fields {
				if strings.HasPrefix(f, "name=") {
					libName = strings.TrimPrefix(f, "name=")
				}
			}
			continue
		}
	}
	if libName == "" {
		return "", nil, errors.New("FUNCTION LOAD: missing #!lua name= directive")
	}
	_ = sortStrings // referenced from List(); keep linker happy when both files build
	funcs := map[string]string{}
	cursor := 0
	for {
		i := strings.Index(src[cursor:], "redis.register_function")
		if i < 0 {
			break
		}
		i += cursor
		// extract the name argument (single-quoted or double-quoted)
		open := strings.IndexAny(src[i:], "'\"")
		if open < 0 {
			break
		}
		open += i
		quote := src[open]
		close := strings.IndexByte(src[open+1:], quote)
		if close < 0 {
			return "", nil, errors.New("FUNCTION LOAD: malformed function name")
		}
		name := src[open+1 : open+1+close]
		// the body starts at the next `function(`
		bodyStart := strings.Index(src[open+1+close:], "function(")
		if bodyStart < 0 {
			return "", nil, errors.New("FUNCTION LOAD: missing function body")
		}
		bodyStart += open + 1 + close
		// find the matching `end` (depth-tracked by counting nested
		// function/if/while/for blocks).
		end, err := findMatchingEnd(src[bodyStart:])
		if err != nil {
			return "", nil, err
		}
		body := strings.TrimSpace(src[bodyStart : bodyStart+end])
		funcs[name] = body
		cursor = bodyStart + end + 3 // skip "end"
	}
	if len(funcs) == 0 {
		return "", nil, errors.New("FUNCTION LOAD: no functions registered")
	}
	return libName, funcs, nil
}

// sortStrings is a tiny in-place sort so List() output is stable.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// findMatchingEnd walks the source counting `function`/`if`/`while`/
// `for`/`do` openings against `end` closings. Returns the offset of
// the matching `end` (exclusive).
func findMatchingEnd(s string) (int, error) {
	depth := 0
	tokens := strings.Fields(s)
	pos := 0
	for _, tok := range tokens {
		idx := strings.Index(s[pos:], tok)
		if idx < 0 {
			break
		}
		pos += idx
		switch tok {
		case "function", "if", "while", "for", "do":
			depth++
		case "end":
			depth--
			if depth <= 0 {
				return pos, nil
			}
		}
		pos += len(tok)
	}
	return 0, errors.New("unterminated function body")
}
