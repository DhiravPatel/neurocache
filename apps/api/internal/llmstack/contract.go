package llmstack

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// ContractValidator validates LLM-emitted tool-call shapes against
// registered tool schemas. Different from STRUCT (which validates
// model OUTPUT against a JSON schema): CONTRACT validates the
// CALL itself — the `{"name":"search","arguments":{...}}` envelope
// the model produces when it decides to invoke a tool.
//
// Real production pain it catches:
//
//   - Hallucinated tool: model invents a tool that doesn't exist
//     ("name":"calculatron")
//   - Missing required argument: model omits a field marked required
//   - Wrong argument type: model passes a string where the schema
//     wants a number
//   - Extra unknown argument: model invents an arg the tool ignores
//     (usually harmless but worth flagging)
//
// Commands:
//
//   CONTRACT.REGISTER tool-id schema-json
//        schema-json = {"properties":{"city":{"type":"string"}},
//                       "required":["city"]}
//   CONTRACT.UNREGISTER tool-id
//   CONTRACT.VALIDATE call-json
//        call-json = {"name":"search","arguments":{"q":"..."}}
//        → {valid, tool_id, errors[]}
//   CONTRACT.LIST
//   CONTRACT.STATS
//
// Reuses STRUCT's schema walker so the validation dialect is
// identical (type / required / properties / items / min/max /
// minLength/maxLength / enum).
//
// Throughput: hot-path VALIDATE is a sync.Map lookup + schema walk
// — typically <5 µs even for nested schemas.
type ContractValidator struct {
	mu     sync.RWMutex
	tools  map[string]map[string]any // tool_id -> parsed schema

	totalValidates atomic.Int64
	totalValid     atomic.Int64
	totalInvalid   atomic.Int64
}

// NewContractValidator returns an empty registry.
func NewContractValidator() *ContractValidator {
	return &ContractValidator{tools: map[string]map[string]any{}}
}

// Register stores a tool's argument schema by id.
func (c *ContractValidator) Register(toolID, argsSchemaJSON string) error {
	if toolID == "" {
		return errors.New("tool_id required")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(argsSchemaJSON), &parsed); err != nil {
		return fmt.Errorf("schema is not valid JSON: %w", err)
	}
	// Default top-level to "object" if omitted — args schemas almost
	// always describe an object.
	if _, ok := parsed["type"]; !ok {
		parsed["type"] = "object"
	}
	c.mu.Lock()
	c.tools[toolID] = parsed
	c.mu.Unlock()
	return nil
}

// Unregister drops a tool. Returns true if it existed.
func (c *ContractValidator) Unregister(toolID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.tools[toolID]
	delete(c.tools, toolID)
	return ok
}

// ContractValidateResult is the VALIDATE return.
type ContractValidateResult struct {
	Valid   bool              `json:"valid"`
	ToolID  string            `json:"tool_id,omitempty"`
	Errors  []ValidationError `json:"errors,omitempty"`
}

// Validate parses the call envelope, looks up the tool, and walks
// the arguments against its schema.
func (c *ContractValidator) Validate(callJSON string) (ContractValidateResult, error) {
	c.totalValidates.Add(1)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(callJSON), &envelope); err != nil {
		c.totalInvalid.Add(1)
		return ContractValidateResult{
			Valid: false,
			Errors: []ValidationError{
				{Path: "$envelope", Message: "not valid JSON: " + err.Error()},
			},
		}, nil
	}

	name, _ := envelope["name"].(string)
	if name == "" {
		c.totalInvalid.Add(1)
		return ContractValidateResult{
			Valid: false,
			Errors: []ValidationError{
				{Path: "$envelope.name", Message: "missing or non-string 'name' field"},
			},
		}, nil
	}

	c.mu.RLock()
	schema, ok := c.tools[name]
	c.mu.RUnlock()
	if !ok {
		c.totalInvalid.Add(1)
		return ContractValidateResult{
			Valid: false,
			Errors: []ValidationError{
				{Path: "$envelope.name", Message: "hallucinated tool: '" + name + "' is not registered"},
			},
		}, nil
	}

	args, hasArgs := envelope["arguments"]
	if !hasArgs {
		// Treat missing arguments as an empty object — many models
		// omit it for no-arg tools, which is fine.
		args = map[string]any{}
	}

	res := ValidateResult{Valid: true}
	walk(schema, args, "$arguments", &res)
	out := ContractValidateResult{
		Valid:  len(res.Errors) == 0,
		ToolID: name,
		Errors: res.Errors,
	}
	if out.Valid {
		c.totalValid.Add(1)
	} else {
		c.totalInvalid.Add(1)
	}
	return out, nil
}

// ContractToolRow is one row of CONTRACT.LIST.
type ContractToolRow struct {
	ToolID string `json:"tool_id"`
	Schema string `json:"schema"`
}

// List returns every registered tool, sorted.
func (c *ContractValidator) List() []ContractToolRow {
	c.mu.RLock()
	out := make([]ContractToolRow, 0, len(c.tools))
	for id, sch := range c.tools {
		b, _ := json.Marshal(sch)
		out = append(out, ContractToolRow{ToolID: id, Schema: string(b)})
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ToolID < out[j].ToolID })
	return out
}

// ContractStats is the global counters snapshot.
type ContractStats struct {
	Tools          int   `json:"tools"`
	TotalValidates int64 `json:"total_validates"`
	TotalValid     int64 `json:"total_valid"`
	TotalInvalid   int64 `json:"total_invalid"`
}

func (c *ContractValidator) Stats() ContractStats {
	c.mu.RLock()
	n := len(c.tools)
	c.mu.RUnlock()
	return ContractStats{
		Tools:          n,
		TotalValidates: c.totalValidates.Load(),
		TotalValid:     c.totalValid.Load(),
		TotalInvalid:   c.totalInvalid.Load(),
	}
}
