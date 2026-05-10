package llmstack

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// StructValidator stores named JSON schemas + validates LLM-generated
// strings against them. Every team building a tool-using agent runs
// into the same problem: the model returns "almost-correct" JSON —
// missing a required field, wrong type, extra trailing comma. Apps
// write parser + retry-with-instructions loops in every project,
// often with the wrong error messages back to the model.
//
// STRUCT.* gives the cache a single command — STRUCT.VALIDATE — plus
// a STRUCT.REPAIR_PROMPT that synthesizes a clear remediation
// instruction the app can pass back to the model. Schemas are
// durable (survive restart via AOF) so ops teams maintain them
// alongside the prompts.
//
// The schema dialect is intentionally a *subset* of JSON Schema —
// what teams actually use 95% of the time, validated in microseconds
// without dragging in a full JSON Schema implementation. Supported:
//
//   - type: object|string|number|integer|boolean|array
//   - required: [field, field, ...]   (object only)
//   - properties: {field: subschema}  (object only)
//   - items: subschema                 (array only)
//   - min / max                        (number/integer)
//   - minLength / maxLength            (string)
//   - enum: [v1, v2, ...]              (any scalar)
//
// Unsupported (intentionally): patternProperties, $ref, oneOf, allOf,
// regex patterns, format. Apps that need those graduate to a real
// JSON Schema validator. This is the "practical 80%" version.
type StructValidator struct {
	mu      sync.RWMutex
	schemas map[string]map[string]any // schema_id -> parsed schema

	totalValidates atomic.Int64
	totalValid     atomic.Int64
	totalInvalid   atomic.Int64
}

// NewStructValidator returns an empty registry.
func NewStructValidator() *StructValidator {
	return &StructValidator{schemas: map[string]map[string]any{}}
}

// SetSchema parses + stores a schema by id. Replacing existing id is
// allowed.
func (v *StructValidator) SetSchema(id, jsonSchema string) error {
	if id == "" {
		return errors.New("schema id required")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonSchema), &parsed); err != nil {
		return fmt.Errorf("schema is not valid JSON: %w", err)
	}
	if _, ok := parsed["type"]; !ok {
		return errors.New("schema must have a 'type' field")
	}
	v.mu.Lock()
	v.schemas[id] = parsed
	v.mu.Unlock()
	return nil
}

// GetSchema returns the raw parsed schema or false.
func (v *StructValidator) GetSchema(id string) (map[string]any, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	s, ok := v.schemas[id]
	return s, ok
}

// Forget drops a schema by id.
func (v *StructValidator) Forget(id string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.schemas[id]
	delete(v.schemas, id)
	return ok
}

// Schemas lists every registered schema id, sorted.
func (v *StructValidator) Schemas() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.schemas))
	for id := range v.schemas {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ValidationError is one violation found during validation.
type ValidationError struct {
	Path    string `json:"path"`    // dot-path to the offending field
	Message string `json:"message"`
}

// ValidateResult is what Validate returns.
type ValidateResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors,omitempty"`
}

// Validate parses `text` as JSON and walks the registered schema. On
// parse failure the result is a single error with path="$root".
func (v *StructValidator) Validate(id, text string) (ValidateResult, bool) {
	v.totalValidates.Add(1)
	v.mu.RLock()
	schema, ok := v.schemas[id]
	v.mu.RUnlock()
	if !ok {
		return ValidateResult{}, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		v.totalInvalid.Add(1)
		return ValidateResult{
			Valid: false,
			Errors: []ValidationError{
				{Path: "$root", Message: "not valid JSON: " + err.Error()},
			},
		}, true
	}
	res := ValidateResult{Valid: true}
	walk(schema, parsed, "$root", &res)
	if len(res.Errors) > 0 {
		res.Valid = false
		v.totalInvalid.Add(1)
	} else {
		v.totalValid.Add(1)
	}
	return res, true
}

// RepairPrompt synthesizes a remediation instruction for the LLM. The
// app drops this into a "your previous output was malformed, try
// again" turn. Returns the prompt + ok=false if schema unknown.
func (v *StructValidator) RepairPrompt(id, badText string) (string, bool) {
	res, ok := v.Validate(id, badText)
	if !ok {
		return "", false
	}
	v.mu.RLock()
	schema, _ := v.schemas[id]
	v.mu.RUnlock()
	schemaJSON, _ := json.MarshalIndent(schema, "", "  ")

	var b strings.Builder
	b.WriteString("Your previous output did not match the required schema.\n\n")
	if len(res.Errors) > 0 {
		b.WriteString("Errors:\n")
		for _, e := range res.Errors {
			fmt.Fprintf(&b, "  - %s: %s\n", e.Path, e.Message)
		}
		b.WriteString("\n")
	}
	b.WriteString("Please return ONLY a JSON value matching this schema (no prose, no markdown fences):\n")
	b.Write(schemaJSON)
	b.WriteString("\n")
	return b.String(), true
}

// StructStats is the global counters snapshot.
type StructStats struct {
	TotalValidates int64 `json:"total_validates"`
	TotalValid     int64 `json:"total_valid"`
	TotalInvalid   int64 `json:"total_invalid"`
	Schemas        int   `json:"schemas"`
}

func (v *StructValidator) Stats() StructStats {
	v.mu.RLock()
	n := len(v.schemas)
	v.mu.RUnlock()
	return StructStats{
		TotalValidates: v.totalValidates.Load(),
		TotalValid:     v.totalValid.Load(),
		TotalInvalid:   v.totalInvalid.Load(),
		Schemas:        n,
	}
}

// ─── walker ────────────────────────────────────────────────────

func walk(schema map[string]any, value any, path string, res *ValidateResult) {
	t, _ := schema["type"].(string)
	switch t {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected object, got %s", typeOf(value)),
			})
			return
		}
		// required
		if reqRaw, ok := schema["required"].([]any); ok {
			for _, r := range reqRaw {
				if rs, ok := r.(string); ok {
					if _, present := obj[rs]; !present {
						res.Errors = append(res.Errors, ValidationError{
							Path:    path + "." + rs,
							Message: "required field missing",
						})
					}
				}
			}
		}
		// properties
		if props, ok := schema["properties"].(map[string]any); ok {
			for k, sub := range props {
				if subSchema, ok := sub.(map[string]any); ok {
					if v, present := obj[k]; present {
						walk(subSchema, v, path+"."+k, res)
					}
				}
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected array, got %s", typeOf(value)),
			})
			return
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, it := range arr {
				walk(items, it, fmt.Sprintf("%s[%d]", path, i), res)
			}
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected string, got %s", typeOf(value)),
			})
			return
		}
		if minL, ok := numField(schema, "minLength"); ok && float64(len(s)) < minL {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("len %d < minLength %.0f", len(s), minL),
			})
		}
		if maxL, ok := numField(schema, "maxLength"); ok && float64(len(s)) > maxL {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("len %d > maxLength %.0f", len(s), maxL),
			})
		}
		checkEnum(schema, value, path, res)
	case "number":
		f, ok := toFloat(value)
		if !ok {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected number, got %s", typeOf(value)),
			})
			return
		}
		checkRange(schema, f, path, res)
		checkEnum(schema, value, path, res)
	case "integer":
		f, ok := toFloat(value)
		if !ok {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected integer, got %s", typeOf(value)),
			})
			return
		}
		if f != float64(int64(f)) {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected integer, got %g", f),
			})
		}
		checkRange(schema, f, path, res)
		checkEnum(schema, value, path, res)
	case "boolean":
		if _, ok := value.(bool); !ok {
			res.Errors = append(res.Errors, ValidationError{
				Path: path, Message: fmt.Sprintf("expected boolean, got %s", typeOf(value)),
			})
		}
	default:
		res.Errors = append(res.Errors, ValidationError{
			Path: path, Message: "unknown schema type: " + t,
		})
	}
}

func typeOf(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, float32, int, int64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func numField(schema map[string]any, key string) (float64, bool) {
	v, ok := schema[key]
	if !ok {
		return 0, false
	}
	return toFloat(v)
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

func checkRange(schema map[string]any, f float64, path string, res *ValidateResult) {
	if mn, ok := numField(schema, "min"); ok && f < mn {
		res.Errors = append(res.Errors, ValidationError{
			Path: path, Message: fmt.Sprintf("%g < min %g", f, mn),
		})
	}
	if mx, ok := numField(schema, "max"); ok && f > mx {
		res.Errors = append(res.Errors, ValidationError{
			Path: path, Message: fmt.Sprintf("%g > max %g", f, mx),
		})
	}
}

func checkEnum(schema map[string]any, value any, path string, res *ValidateResult) {
	enumRaw, ok := schema["enum"].([]any)
	if !ok {
		return
	}
	for _, e := range enumRaw {
		if equalScalar(e, value) {
			return
		}
	}
	enumStr := make([]string, 0, len(enumRaw))
	for _, e := range enumRaw {
		enumStr = append(enumStr, fmt.Sprintf("%v", e))
	}
	res.Errors = append(res.Errors, ValidationError{
		Path: path, Message: fmt.Sprintf("value %v not in enum [%s]", value,
			strings.Join(enumStr, ", ")),
	})
}

func equalScalar(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	// Numbers come out of encoding/json as float64; compare uniformly.
	af, aOk := toFloat(a)
	bf, bOk := toFloat(b)
	if aOk && bOk {
		return af == bf
	}
	return a == b
}
