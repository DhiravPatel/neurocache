package aiops

import (
	"encoding/json"
	"errors"
	"sync"
)

// MCP is the Model Context Protocol server. It exposes NeuroCache's
// existing primitives (memory, conversations, vector sets, prompts)
// as MCP tools so Claude/Cursor/IDE-style clients can call them
// directly without a wrapper.
//
// MCP itself is JSON-RPC 2.0 over a duplex transport. We implement the
// core method set that LLM clients exercise:
//
//   - initialize     : capability handshake
//   - tools/list     : list registered tools
//   - tools/call     : invoke a tool by name with JSON args
//   - resources/list : list registered resources
//   - resources/read : fetch a resource by URI
//
// Higher-level transport (stdio / WebSocket / HTTP+SSE) is the caller's
// responsibility — they hand us a JSON-RPC frame and we return the
// response. This decouples the protocol logic from the transport so the
// engine can expose MCP over multiple wire formats.
type MCP struct {
	mu        sync.RWMutex
	tools     map[string]*MCPTool
	resources map[string]*MCPResource
}

// MCPTool is a callable function exposed to MCP clients.
type MCPTool struct {
	Name        string                                                                  `json:"name"`
	Description string                                                                  `json:"description"`
	InputSchema map[string]interface{}                                                  `json:"inputSchema"` // JSON Schema
	Handler     func(args map[string]interface{}) (interface{}, error)                  `json:"-"`
}

// MCPResource is a readable URI exposed to MCP clients.
type MCPResource struct {
	URI         string                              `json:"uri"`
	Name        string                              `json:"name"`
	Description string                              `json:"description,omitempty"`
	MimeType    string                              `json:"mimeType,omitempty"`
	Reader      func() (string, error)              `json:"-"`
}

// NewMCP returns an empty MCP server.
func NewMCP() *MCP {
	return &MCP{
		tools:     map[string]*MCPTool{},
		resources: map[string]*MCPResource{},
	}
}

// RegisterTool exposes a function to MCP clients.
func (m *MCP) RegisterTool(t *MCPTool) {
	m.mu.Lock()
	m.tools[t.Name] = t
	m.mu.Unlock()
}

// RegisterResource exposes a readable URI to MCP clients.
func (m *MCP) RegisterResource(r *MCPResource) {
	m.mu.Lock()
	m.resources[r.URI] = r
	m.mu.Unlock()
}

// Tools returns every registered tool's metadata (no handlers).
func (m *MCP) Tools() []*MCPTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*MCPTool, 0, len(m.tools))
	for _, t := range m.tools {
		out = append(out, t)
	}
	return out
}

// Resources returns every registered resource's metadata (no readers).
func (m *MCP) Resources() []*MCPResource {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*MCPResource, 0, len(m.resources))
	for _, r := range m.resources {
		out = append(out, r)
	}
	return out
}

// JSONRPCRequest is the shape MCP transports hand to Handle().
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is the shape Handle() returns.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is the standard JSON-RPC error envelope.
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Handle dispatches one JSON-RPC frame and returns the response.
// Designed so transports (stdio loop, WebSocket, HTTP+SSE) can hand
// us bytes and emit our reply back without knowing the method set.
func (m *MCP) Handle(req JSONRPCRequest) JSONRPCResponse {
	resp := JSONRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2025-06-18",
			"capabilities": map[string]interface{}{
				"tools":     map[string]interface{}{},
				"resources": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "neurocache",
				"version": "0.4.0",
			},
		}
	case "tools/list":
		resp.Result = map[string]interface{}{"tools": m.Tools()}
	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &JSONRPCError{Code: -32602, Message: "invalid params: " + err.Error()}
			break
		}
		m.mu.RLock()
		tool, ok := m.tools[params.Name]
		m.mu.RUnlock()
		if !ok {
			resp.Error = &JSONRPCError{Code: -32601, Message: "tool not found: " + params.Name}
			break
		}
		out, err := tool.Handler(params.Arguments)
		if err != nil {
			resp.Error = &JSONRPCError{Code: -32000, Message: err.Error()}
			break
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": stringOf(out)},
			},
		}
	case "resources/list":
		resp.Result = map[string]interface{}{"resources": m.Resources()}
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &JSONRPCError{Code: -32602, Message: "invalid params: " + err.Error()}
			break
		}
		m.mu.RLock()
		res, ok := m.resources[params.URI]
		m.mu.RUnlock()
		if !ok {
			resp.Error = &JSONRPCError{Code: -32601, Message: "resource not found: " + params.URI}
			break
		}
		body, err := res.Reader()
		if err != nil {
			resp.Error = &JSONRPCError{Code: -32000, Message: err.Error()}
			break
		}
		resp.Result = map[string]interface{}{
			"contents": []map[string]interface{}{
				{"uri": params.URI, "mimeType": res.MimeType, "text": body},
			},
		}
	default:
		resp.Error = &JSONRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

// HandleBytes is the convenience wrapper that takes a raw JSON-RPC
// request, parses it, and returns a serialized response. Used by HTTP
// and WebSocket transports.
func (m *MCP) HandleBytes(in []byte) []byte {
	var req JSONRPCRequest
	if err := json.Unmarshal(in, &req); err != nil {
		errResp := JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &JSONRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		}
		out, _ := json.Marshal(errResp)
		return out
	}
	resp := m.Handle(req)
	out, _ := json.Marshal(resp)
	return out
}

// Errors returned by tool handlers — short-form for the engine wiring.
var (
	ErrMCPNoSuchTool     = errors.New("no such MCP tool")
	ErrMCPNoSuchResource = errors.New("no such MCP resource")
)

func stringOf(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	out, _ := json.Marshal(v)
	return string(out)
}
