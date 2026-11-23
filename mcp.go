package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
)

// An MCPServer serves Go functions to Claude as MCP tools, from inside this process: the CLI
// sends each MCP request over the control protocol and this package answers it (the official
// SDKs' "sdk" servers). Claude sees the tools as mcp__<server name>__<tool name>.
type MCPServer struct {
	Name    string
	Version string
	tools   []Tool
	byName  map[string]int
}

// Tool is one tool: its name, description, JSON Schema input and the function that runs it.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object ({"type":"object",…}).
	InputSchema json.RawMessage
	// Annotations are MCP tool annotations (readOnlyHint…), optional.
	Annotations json.RawMessage
	Handler     ToolHandler
}

// ToolHandler runs a tool. An error is reported to Claude as a failed tool call (isError), as
// MCP wants it, and the turn goes on.
type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

// ToolCall is a call of a tool.
type ToolCall struct {
	Name      string
	Arguments json.RawMessage
	// ToolUseID is the id of the tool_use block the call answers, when the CLI says it.
	ToolUseID string
}

// ToolResult is what a tool returns: MCP content blocks (text, image…), and whether it failed.
type ToolResult struct {
	Content []json.RawMessage
	IsError bool
}

// TextResult is a successful result holding a text.
func TextResult(text string) ToolResult {
	b, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	return ToolResult{Content: []json.RawMessage{b}}
}

// ErrorResult is a failed result holding a text (the model reads it and can adapt).
func ErrorResult(text string) ToolResult {
	r := TextResult(text)
	r.IsError = true
	return r
}

// NewMCPServer returns a server with tools.
func NewMCPServer(name, version string, tools ...Tool) *MCPServer {
	s := &MCPServer{Name: name, Version: version, byName: map[string]int{}}
	for _, t := range tools {
		s.Add(t)
	}
	return s
}

// Add adds a tool, replacing one of the same name. Add tools before the client starts.
func (s *MCPServer) Add(t Tool) {
	if len(t.InputSchema) == 0 {
		t.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	if i, ok := s.byName[t.Name]; ok {
		s.tools[i] = t
		return
	}
	s.byName[t.Name] = len(s.tools)
	s.tools = append(s.tools, t)
}

// Tools lists the server's tools, in the order they were added.
func (s *MCPServer) Tools() []Tool { return append([]Tool(nil), s.tools...) }

// ToolName is the name Claude sees for a tool of a server.
func ToolName(server, tool string) string { return "mcp__" + server + "__" + tool }

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handle answers one MCP JSON-RPC message. A notification gets an empty result (the control
// request that carried it still expects an answer).
func (s *MCPServer) Handle(ctx context.Context, message json.RawMessage) json.RawMessage {
	var req rpcRequest
	resp := rpcResponse{JSONRPC: "2.0"}
	if err := json.Unmarshal(message, &req); err != nil {
		resp.Error = &rpcError{Code: -32700, Message: "parse error: " + err.Error()}
		return mustJSON(resp)
	}
	resp.ID = req.ID
	switch req.Method {
	case "initialize":
		version := "2025-06-18"
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			e := map[string]any{"name": t.Name, "inputSchema": t.InputSchema}
			if t.Description != "" {
				e["description"] = t.Description
			}
			if len(t.Annotations) > 0 {
				e["annotations"] = t.Annotations
			}
			list = append(list, e)
		}
		resp.Result = map[string]any{"tools": list}
	case "tools/call":
		resp.Result = s.call(ctx, req.Params)
	default:
		if len(req.ID) == 0 { // a notification
			resp.Result = map[string]any{}
			break
		}
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return mustJSON(resp)
}

func (s *MCPServer) call(ctx context.Context, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ToolUseID string `json:"claudecode/toolUseId"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return resultJSON(ErrorResult("the call could not be read: " + err.Error()))
	}
	i, ok := s.byName[p.Name]
	if !ok {
		return resultJSON(ErrorResult(fmt.Sprintf("there is no tool named %q", p.Name)))
	}
	t := s.tools[i]
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage(`{}`)
	}
	r, err := safeCall(ctx, t.Handler, ToolCall{Name: p.Name, Arguments: p.Arguments, ToolUseID: p.Meta.ToolUseID})
	if err != nil {
		return resultJSON(ErrorResult(err.Error()))
	}
	return resultJSON(r)
}

func safeCall(ctx context.Context, h ToolHandler, call ToolCall) (r ToolResult, err error) {
	if h == nil {
		return ToolResult{}, fmt.Errorf("tool %s has no handler", call.Name)
	}
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("tool %s panicked: %v", call.Name, v)
		}
	}()
	return h(ctx, call)
}

func resultJSON(r ToolResult) map[string]any {
	content := r.Content
	if content == nil {
		content = []json.RawMessage{}
	}
	return map[string]any{"content": content, "isError": r.IsError}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
