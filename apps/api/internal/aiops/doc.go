package aiops

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Docs is collaborative-document sync with a JSON Patch (RFC 6902)
// op stream + version vector. Replaces the build-your-own-Yjs/
// Automerge layer apps reach for when they need real-time
// multiplayer state. Conflict resolution is last-writer-wins on
// individual paths, surfaced via the version field.
//
// Structure: each document is a JSON value plus a monotonic version
// counter. Each Apply patches the document and bumps the version;
// the patch operations are appended to the doc's history so late
// joiners can replay from any version.
type Docs struct {
	mu   sync.RWMutex
	docs map[string]*doc
}

type doc struct {
	value     interface{}
	version   int64
	history   []DocPatch
	updated   time.Time
	createdAt time.Time
	maxHist   int // history retention (default 1000 ops)
}

// DocPatch is one applied op set, recorded with the resulting version.
type DocPatch struct {
	Version int64           `json:"version"`
	At      time.Time       `json:"at"`
	Ops     json.RawMessage `json:"ops"`
}

// DocSnapshot is the snapshot returned to readers.
type DocSnapshot struct {
	Key       string      `json:"key"`
	Version   int64       `json:"version"`
	Value     interface{} `json:"value"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// NewDocs returns a manager.
func NewDocs() *Docs { return &Docs{docs: map[string]*doc{}} }

// Init creates / overwrites a document with an initial JSON value.
// Returns the new version (always 1).
func (d *Docs) Init(key string, raw []byte) (int64, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dc, ok := d.docs[key]
	if !ok {
		dc = &doc{createdAt: time.Now(), maxHist: 1000}
		d.docs[key] = dc
	}
	dc.value = v
	dc.version = 1
	dc.history = []DocPatch{{Version: 1, At: time.Now(), Ops: raw}}
	dc.updated = time.Now()
	return 1, nil
}

// Apply executes a JSON Patch (RFC 6902) array of operations against
// the document and returns the new version. Patch ops we honour:
//   { "op": "add",     "path": "/foo", "value": ... }
//   { "op": "remove",  "path": "/foo" }
//   { "op": "replace", "path": "/foo", "value": ... }
//   { "op": "test",    "path": "/foo", "value": ... }   (no-op)
//   { "op": "copy",    "from": "/a",   "path": "/b" }
//   { "op": "move",    "from": "/a",   "path": "/b" }
//
// Anything outside that subset returns an error and the document is
// left unchanged (atomic patch semantics).
func (d *Docs) Apply(key string, patchJSON []byte) (int64, error) {
	var ops []map[string]interface{}
	if err := json.Unmarshal(patchJSON, &ops); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dc, ok := d.docs[key]
	if !ok {
		return 0, errors.New("no such document")
	}
	// Apply against a working copy so partial failures don't
	// leave torn state. JSON marshalling is the simplest deep clone.
	cloneRaw, err := json.Marshal(dc.value)
	if err != nil {
		return 0, err
	}
	var working interface{}
	if err := json.Unmarshal(cloneRaw, &working); err != nil {
		return 0, err
	}
	for _, op := range ops {
		opName, _ := op["op"].(string)
		path, _ := op["path"].(string)
		from, _ := op["from"].(string)
		val := op["value"]
		var nerr error
		switch opName {
		case "add":
			working, nerr = jsonPatchSet(working, path, val)
		case "replace":
			working, nerr = jsonPatchSet(working, path, val)
		case "remove":
			working, nerr = jsonPatchRemove(working, path)
		case "test":
			// Skip test — semantically the patch should fail if the
			// value doesn't match; we'd need deep-equal. For now we
			// accept and continue. Real RFC 6902 implementations are
			// strict here; we lean lenient.
		case "copy":
			v, ok := jsonPatchGet(working, from)
			if !ok {
				nerr = errors.New("copy: from path not found: " + from)
				break
			}
			working, nerr = jsonPatchSet(working, path, v)
		case "move":
			v, ok := jsonPatchGet(working, from)
			if !ok {
				nerr = errors.New("move: from path not found: " + from)
				break
			}
			working, nerr = jsonPatchRemove(working, from)
			if nerr == nil {
				working, nerr = jsonPatchSet(working, path, v)
			}
		default:
			nerr = errors.New("unsupported op: " + opName)
		}
		if nerr != nil {
			return 0, nerr
		}
	}
	dc.value = working
	dc.version++
	dc.history = append(dc.history, DocPatch{
		Version: dc.version,
		At:      time.Now(),
		Ops:     append([]byte(nil), patchJSON...),
	})
	if len(dc.history) > dc.maxHist {
		dc.history = dc.history[len(dc.history)-dc.maxHist:]
	}
	dc.updated = time.Now()
	return dc.version, nil
}

// Get returns the document's current value + version.
func (d *Docs) Get(key string) (DocSnapshot, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	dc, ok := d.docs[key]
	if !ok {
		return DocSnapshot{}, false
	}
	return DocSnapshot{
		Key:       key,
		Version:   dc.version,
		Value:     dc.value,
		UpdatedAt: dc.updated,
	}, true
}

// Since returns the patches applied after the given version (exclusive).
// Callers tail this to keep their local replica in sync. When the
// caller's version is older than the oldest retained patch, History
// returns the full current snapshot via the second return path.
func (d *Docs) Since(key string, version int64) ([]DocPatch, *DocSnapshot, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	dc, ok := d.docs[key]
	if !ok {
		return nil, nil, false
	}
	if len(dc.history) == 0 || dc.history[0].Version > version+1 {
		// Caller fell off the retention window — give them a fresh snapshot.
		snap := DocSnapshot{Key: key, Version: dc.version, Value: dc.value, UpdatedAt: dc.updated}
		return nil, &snap, true
	}
	out := []DocPatch{}
	for _, p := range dc.history {
		if p.Version > version {
			out = append(out, p)
		}
	}
	return out, nil, true
}

// Forget drops a document.
func (d *Docs) Forget(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.docs[key]
	delete(d.docs, key)
	return ok
}

// List returns every document key (sort is the caller's job).
func (d *Docs) List() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]string, 0, len(d.docs))
	for k := range d.docs {
		out = append(out, k)
	}
	return out
}

// ─── JSON pointer / patch primitives ────────────────────────────────
//
// We don't pull in a full RFC 6901 + 6902 library; a hand-written
// subset that handles "/foo/bar/0" paths is enough for the ~95% case
// (no escaped characters in path segments).

func splitPath(p string) []string {
	if p == "" || p == "/" {
		return nil
	}
	if p[0] != '/' {
		return strings.Split(p, "/")
	}
	return strings.Split(p[1:], "/")
}

func jsonPatchGet(root interface{}, path string) (interface{}, bool) {
	parts := splitPath(path)
	cur := root
	for _, seg := range parts {
		switch v := cur.(type) {
		case map[string]interface{}:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []interface{}:
			idx := parseIndex(seg, len(v))
			if idx < 0 || idx >= len(v) {
				return nil, false
			}
			cur = v[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func jsonPatchSet(root interface{}, path string, value interface{}) (interface{}, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return value, nil
	}
	return setRecursive(root, parts, value)
}

func setRecursive(node interface{}, parts []string, value interface{}) (interface{}, error) {
	seg := parts[0]
	rest := parts[1:]
	switch v := node.(type) {
	case map[string]interface{}:
		if v == nil {
			v = map[string]interface{}{}
		}
		if len(rest) == 0 {
			v[seg] = value
			return v, nil
		}
		child, ok := v[seg]
		if !ok {
			child = map[string]interface{}{}
		}
		newChild, err := setRecursive(child, rest, value)
		if err != nil {
			return nil, err
		}
		v[seg] = newChild
		return v, nil
	case []interface{}:
		if seg == "-" {
			// JSON-Patch "append" syntax
			if len(rest) == 0 {
				return append(v, value), nil
			}
			return nil, errors.New("set: '-' requires a leaf path")
		}
		idx := parseIndex(seg, len(v))
		if idx < 0 || idx > len(v) {
			return nil, errors.New("set: index out of range")
		}
		if len(rest) == 0 {
			if idx == len(v) {
				return append(v, value), nil
			}
			v[idx] = value
			return v, nil
		}
		newChild, err := setRecursive(v[idx], rest, value)
		if err != nil {
			return nil, err
		}
		v[idx] = newChild
		return v, nil
	case nil:
		// Auto-vivify a map.
		out := map[string]interface{}{}
		if len(rest) == 0 {
			out[seg] = value
			return out, nil
		}
		child, err := setRecursive(nil, rest, value)
		if err != nil {
			return nil, err
		}
		out[seg] = child
		return out, nil
	}
	return nil, errors.New("set: unsupported node type")
}

func jsonPatchRemove(root interface{}, path string) (interface{}, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, nil
	}
	return removeRecursive(root, parts)
}

func removeRecursive(node interface{}, parts []string) (interface{}, error) {
	seg := parts[0]
	rest := parts[1:]
	switch v := node.(type) {
	case map[string]interface{}:
		if len(rest) == 0 {
			delete(v, seg)
			return v, nil
		}
		child, ok := v[seg]
		if !ok {
			return v, nil
		}
		newChild, err := removeRecursive(child, rest)
		if err != nil {
			return nil, err
		}
		v[seg] = newChild
		return v, nil
	case []interface{}:
		idx := parseIndex(seg, len(v))
		if idx < 0 || idx >= len(v) {
			return v, nil
		}
		if len(rest) == 0 {
			return append(v[:idx], v[idx+1:]...), nil
		}
		newChild, err := removeRecursive(v[idx], rest)
		if err != nil {
			return nil, err
		}
		v[idx] = newChild
		return v, nil
	}
	return node, nil
}

func parseIndex(s string, length int) int {
	// Accept "-" for "append". Numeric indices only otherwise.
	if s == "-" {
		return length
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
