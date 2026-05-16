package llmstack

import (
	"strings"
	"testing"
)

const searchToolSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string"},
    "limit": {"type": "integer", "min": 1, "max": 100}
  },
  "required": ["query"]
}`

func TestContractRegisterAndValidate(t *testing.T) {
	c := NewContractValidator()
	c.Register("web_search", searchToolSchema)
	r, err := c.Validate(`{"name":"web_search","arguments":{"query":"bitcoin","limit":10}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Fatalf("should be valid: %+v", r.Errors)
	}
	if r.ToolID != "web_search" {
		t.Fatalf("tool_id = %s", r.ToolID)
	}
}

func TestContractHallucinatedTool(t *testing.T) {
	c := NewContractValidator()
	c.Register("web_search", searchToolSchema)
	r, _ := c.Validate(`{"name":"calculatron","arguments":{"x":1}}`)
	if r.Valid {
		t.Fatal("hallucinated tool should be invalid")
	}
	if !strings.Contains(r.Errors[0].Message, "hallucinated tool") {
		t.Fatalf("expected hallucinated-tool error, got %+v", r.Errors)
	}
}

func TestContractMissingRequired(t *testing.T) {
	c := NewContractValidator()
	c.Register("web_search", searchToolSchema)
	r, _ := c.Validate(`{"name":"web_search","arguments":{"limit":5}}`)
	if r.Valid {
		t.Fatal("missing required field should be invalid")
	}
}

func TestContractWrongType(t *testing.T) {
	c := NewContractValidator()
	c.Register("web_search", searchToolSchema)
	r, _ := c.Validate(`{"name":"web_search","arguments":{"query":42}}`)
	if r.Valid {
		t.Fatal("wrong-type arg should be invalid")
	}
}

func TestContractOutOfRange(t *testing.T) {
	c := NewContractValidator()
	c.Register("web_search", searchToolSchema)
	r, _ := c.Validate(`{"name":"web_search","arguments":{"query":"x","limit":500}}`)
	if r.Valid {
		t.Fatal("limit=500 should fail max=100")
	}
}

func TestContractMissingArgumentsTreatedAsEmpty(t *testing.T) {
	c := NewContractValidator()
	// Tool with no required fields
	c.Register("ping", `{"type":"object"}`)
	r, _ := c.Validate(`{"name":"ping"}`)
	if !r.Valid {
		t.Fatalf("no-arg call should pass when no required: %+v", r.Errors)
	}
}

func TestContractMissingName(t *testing.T) {
	c := NewContractValidator()
	r, _ := c.Validate(`{"arguments":{}}`)
	if r.Valid {
		t.Fatal("missing 'name' should be invalid")
	}
}

func TestContractInvalidJSON(t *testing.T) {
	c := NewContractValidator()
	r, _ := c.Validate(`{not json}`)
	if r.Valid {
		t.Fatal("invalid JSON should be invalid")
	}
}

func TestContractUnregister(t *testing.T) {
	c := NewContractValidator()
	c.Register("x", `{"type":"object"}`)
	if !c.Unregister("x") {
		t.Fatal("unregister should return true")
	}
	if c.Unregister("x") {
		t.Fatal("unregister on missing should return false")
	}
}

func TestContractList(t *testing.T) {
	c := NewContractValidator()
	c.Register("a", `{"type":"object"}`)
	c.Register("b", `{"type":"object"}`)
	rows := c.List()
	if len(rows) != 2 {
		t.Fatalf("list = %d", len(rows))
	}
}

func TestContractRejectsBadSchema(t *testing.T) {
	c := NewContractValidator()
	if err := c.Register("x", "not json"); err == nil {
		t.Fatal("bad JSON should fail")
	}
}

func TestContractStatsAdvance(t *testing.T) {
	c := NewContractValidator()
	c.Register("x", `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`)
	c.Validate(`{"name":"x","arguments":{"a":"ok"}}`)
	c.Validate(`{"name":"x","arguments":{}}`)
	s := c.Stats()
	if s.TotalValidates != 2 || s.TotalValid != 1 || s.TotalInvalid != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
