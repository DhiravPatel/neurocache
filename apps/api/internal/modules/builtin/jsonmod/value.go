package jsonmod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Doc is what we store in the engine — a wrapper around the JSON tree
// so we can hang metadata on it later (compression, indexing, version
// stamps) without changing the module-type marshaler signature.
type Doc struct {
	Root any
}

// New parses the given JSON text and wraps it. Empty input is treated
// as JSON `null` so callers can JSON.SET an empty document then
// populate it via JSON.SET nested paths.
func New(raw []byte) (*Doc, error) {
	if len(raw) == 0 {
		return &Doc{Root: nil}, nil
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserves integer/float distinction we need for NUMINCRBY
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	return &Doc{Root: root}, nil
}

// Marshal serializes the document. UseNumber-encoded integers are
// preserved exactly so reading back is byte-identical to the input.
func (d *Doc) Marshal() ([]byte, error) {
	if d == nil {
		return []byte("null"), nil
	}
	return json.Marshal(d.Root)
}

// MarshalIndent honours JSON.GET's INDENT/SPACE/NEWLINE formatting.
func (d *Doc) MarshalIndent(indent string) ([]byte, error) {
	if d == nil {
		return []byte("null"), nil
	}
	return json.MarshalIndent(d.Root, "", indent)
}

// kindOf returns the Redis JSON type label for v: "string", "integer",
// "number", "boolean", "object", "array", "null".
func kindOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		s := string(x)
		for i := 0; i < len(s); i++ {
			if s[i] == '.' || s[i] == 'e' || s[i] == 'E' {
				return "number"
			}
		}
		return "integer"
	case float64:
		if x == float64(int64(x)) {
			return "integer"
		}
		return "number"
	case int, int64:
		return "integer"
	}
	return "unknown"
}

// asNumber turns v into (int64, float64, isInt). NUMINCRBY uses the
// int path when both operands are ints; otherwise we widen to float.
func asNumber(v any) (int64, float64, bool, error) {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, 0, true, nil
		}
		f, err := x.Float64()
		if err != nil {
			return 0, 0, false, err
		}
		return 0, f, false, nil
	case float64:
		if x == float64(int64(x)) {
			return int64(x), 0, true, nil
		}
		return 0, x, false, nil
	case int:
		return int64(x), 0, true, nil
	case int64:
		return x, 0, true, nil
	}
	return 0, 0, false, errors.New("value is not a number")
}

// toJSONNumber wraps a numeric back into a json.Number so re-serialisation
// preserves the integer/float distinction.
func toJSONNumber(i int64, f float64, isInt bool) json.Number {
	if isInt {
		return json.Number(strconv.FormatInt(i, 10))
	}
	return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
}

// asString accepts only Go strings — JSON.STRAPPEND / STRLEN are
// strict about this, mirroring Redis JSON.
func asString(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("not a string (kind=%s)", kindOf(v))
}
