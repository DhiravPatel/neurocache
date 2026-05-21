package llmstack

import (
	"testing"
)

func TestStreamParseSimpleObject(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	fields, err := s.Push("s1", `{"name":"Alice","age":30}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("fields = %d, want 2: %+v", len(fields), fields)
	}
	if fields[0].Key != "name" || fields[0].Value != "Alice" || fields[0].JSONType != "string" {
		t.Fatalf("fields[0] = %+v", fields[0])
	}
	if fields[1].Key != "age" || fields[1].Value != "30" || fields[1].JSONType != "number" {
		t.Fatalf("fields[1] = %+v", fields[1])
	}
}

func TestStreamParseIncrementalChunks(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	// Simulate token streaming: push the JSON one chunk at a time
	chunks := []string{`{"na`, `me":"`, `Alice"`, `,`, `"age`, `":3`, `0}`}
	allFields := []ParsedField{}
	for _, c := range chunks {
		fields, err := s.Push("s1", c)
		if err != nil {
			t.Fatal(err)
		}
		allFields = append(allFields, fields...)
	}
	if len(allFields) != 2 {
		t.Fatalf("total fields = %d, want 2: %+v", len(allFields), allFields)
	}
	if allFields[0].Key != "name" || allFields[0].Value != "Alice" {
		t.Fatalf("got = %+v", allFields[0])
	}
	if allFields[1].Key != "age" || allFields[1].Value != "30" {
		t.Fatalf("got = %+v", allFields[1])
	}
}

func TestStreamParseNestedObjectAsRawJSON(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	fields, err := s.Push("s1", `{"user":{"name":"Alice","email":"a@b.io"},"id":42}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(fields))
	}
	if fields[0].Key != "user" || fields[0].JSONType != "object" {
		t.Fatalf("fields[0] = %+v", fields[0])
	}
	if fields[0].Value != `{"name":"Alice","email":"a@b.io"}` {
		t.Fatalf("nested object body = %q", fields[0].Value)
	}
	if fields[1].Key != "id" || fields[1].Value != "42" {
		t.Fatalf("fields[1] = %+v", fields[1])
	}
}

func TestStreamParseArrayAsRawJSON(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	fields, _ := s.Push("s1", `{"tags":["a","b","c"]}`)
	if len(fields) != 1 || fields[0].JSONType != "array" {
		t.Fatalf("fields = %+v", fields)
	}
	if fields[0].Value != `["a","b","c"]` {
		t.Fatalf("array body = %q", fields[0].Value)
	}
}

func TestStreamParseBoolNullFloat(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	fields, err := s.Push("s1", `{"active":true,"deleted":false,"parent":null,"score":3.14}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 {
		t.Fatalf("fields = %d", len(fields))
	}
	types := []string{"boolean", "boolean", "null", "number"}
	for i, f := range fields {
		if f.JSONType != types[i] {
			t.Errorf("fields[%d].JSONType = %s, want %s (%+v)", i, f.JSONType, types[i], f)
		}
	}
}

func TestStreamParseEscapedString(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	fields, err := s.Push("s1", `{"msg":"hello \"world\""}`)
	if err != nil {
		t.Fatal(err)
	}
	if fields[0].Value != `hello "world"` {
		t.Fatalf("escape failed: %q", fields[0].Value)
	}
}

func TestStreamParseComplete(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	s.Push("s1", `{"name":"Alice"}`)
	r, ok := s.Complete("s1")
	if !ok {
		t.Fatal("complete returned false")
	}
	if r.FieldsEmitted != 1 {
		t.Fatalf("fields_emitted = %d", r.FieldsEmitted)
	}
}

func TestStreamParseUnknownStream(t *testing.T) {
	s := NewStreamParser()
	if _, err := s.Push("nope", "x"); err == nil {
		t.Fatal("push on unknown stream should fail")
	}
}

func TestStreamParseForget(t *testing.T) {
	s := NewStreamParser()
	s.Open("s1")
	if !s.Forget("s1") {
		t.Fatal("forget should return true")
	}
	if s.Forget("s1") {
		t.Fatal("forget on missing should return false")
	}
}

func TestStreamParseStats(t *testing.T) {
	s := NewStreamParser()
	s.Open("a")
	s.Open("b")
	s.Push("a", `{"x":1}`)
	stats := s.Stats()
	if stats.TotalOpens != 2 || stats.TotalPushes != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}
