package llmstack

import (
	"strings"
	"testing"
)

const personSchema = `{
  "type": "object",
  "required": ["name", "age"],
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "age": {"type": "integer", "min": 0, "max": 150},
    "tags": {"type": "array", "items": {"type": "string"}},
    "tier": {"type": "string", "enum": ["free", "pro", "enterprise"]}
  }
}`

func TestStructSetSchemaInvalidJSON(t *testing.T) {
	v := NewStructValidator()
	if err := v.SetSchema("p", "{not json"); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestStructSetSchemaMissingType(t *testing.T) {
	v := NewStructValidator()
	if err := v.SetSchema("p", `{"required": ["x"]}`); err == nil {
		t.Fatal("expected type-required error")
	}
}

func TestStructValidateHappyPath(t *testing.T) {
	v := NewStructValidator()
	if err := v.SetSchema("person", personSchema); err != nil {
		t.Fatal(err)
	}
	r, ok := v.Validate("person", `{"name": "Alice", "age": 30}`)
	if !ok {
		t.Fatal("validate returned false")
	}
	if !r.Valid {
		t.Fatalf("should be valid: %+v", r.Errors)
	}
}

func TestStructValidateMissingRequired(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": "Alice"}`)
	if r.Valid {
		t.Fatal("should be invalid")
	}
	found := false
	for _, e := range r.Errors {
		if strings.Contains(e.Path, "age") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error on missing 'age', got %+v", r.Errors)
	}
}

func TestStructValidateWrongType(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": 42, "age": 30}`)
	if r.Valid {
		t.Fatal("name=42 should fail type check")
	}
}

func TestStructValidateNumericRange(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": "Alice", "age": 200}`)
	if r.Valid {
		t.Fatal("age=200 should fail max=150")
	}
	r2, _ := v.Validate("person", `{"name": "Alice", "age": -5}`)
	if r2.Valid {
		t.Fatal("age=-5 should fail min=0")
	}
}

func TestStructValidateIntegerCheck(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": "Alice", "age": 30.5}`)
	if r.Valid {
		t.Fatal("age=30.5 should fail integer check")
	}
}

func TestStructValidateEnum(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": "Alice", "age": 30, "tier": "platinum"}`)
	if r.Valid {
		t.Fatal("tier=platinum should fail enum")
	}
	r2, _ := v.Validate("person", `{"name": "Alice", "age": 30, "tier": "pro"}`)
	if !r2.Valid {
		t.Fatalf("tier=pro should pass: %+v", r2.Errors)
	}
}

func TestStructValidateArrayItems(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": "A", "age": 1, "tags": ["a", 42, "c"]}`)
	if r.Valid {
		t.Fatal("tags[1]=42 should fail string-item check")
	}
	found := false
	for _, e := range r.Errors {
		if strings.Contains(e.Path, "tags[1]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected tags[1] error, got %+v", r.Errors)
	}
}

func TestStructValidateBadJSON(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	r, _ := v.Validate("person", `{"name": "A", "age":}`)
	if r.Valid {
		t.Fatal("malformed JSON should be invalid")
	}
	if r.Errors[0].Path != "$root" {
		t.Fatalf("expected $root, got %q", r.Errors[0].Path)
	}
}

func TestStructRepairPromptIncludesErrorsAndSchema(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("person", personSchema)
	p, ok := v.RepairPrompt("person", `{"name": 42}`)
	if !ok {
		t.Fatal("repair_prompt returned false")
	}
	if !strings.Contains(p, "Errors:") {
		t.Fatal("prompt missing Errors section")
	}
	if !strings.Contains(p, "schema") {
		t.Fatal("prompt missing schema reference")
	}
	if !strings.Contains(p, `"required"`) {
		t.Fatal("prompt should embed the schema JSON")
	}
}

func TestStructForgetAndList(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("a", `{"type":"string"}`)
	v.SetSchema("b", `{"type":"number"}`)
	if list := v.Schemas(); len(list) != 2 {
		t.Fatalf("schemas = %d", len(list))
	}
	if !v.Forget("a") {
		t.Fatal("forget should return true")
	}
	if list := v.Schemas(); len(list) != 1 || list[0] != "b" {
		t.Fatalf("after forget = %v", list)
	}
}

func TestStructStatsAdvance(t *testing.T) {
	v := NewStructValidator()
	v.SetSchema("p", `{"type":"string"}`)
	v.Validate("p", `"ok"`)
	v.Validate("p", `123`)
	s := v.Stats()
	if s.TotalValidates != 2 || s.TotalValid != 1 || s.TotalInvalid != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
