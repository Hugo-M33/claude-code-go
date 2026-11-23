package claudecode

import (
	"encoding/json"
	"fmt"
)

// Message is one message the CLI writes: *SystemMessage, *AssistantMessage, *UserMessage,
// *StreamEvent, *ResultMessage, *RateLimitEvent, or *OtherMessage for a type this package does
// not know (the protocol grows; nothing is dropped).
type Message interface {
	// Type is the message's "type" field.
	Type() string
	// Raw is the message as the CLI wrote it.
	Raw() json.RawMessage
}

type base struct{ raw json.RawMessage }

func (b base) Raw() json.RawMessage { return b.raw }

// SystemMessage is a "system" message; Subtype "init" opens a session and lists what it has.
type SystemMessage struct {
	base
	Subtype      string            `json:"subtype"`
	SessionID    string            `json:"session_id"`
	Model        string            `json:"model"`
	Cwd          string            `json:"cwd"`
	Tools        []string          `json:"tools"`
	MCPServers   []MCPServerStatus `json:"mcp_servers"`
	APIKeySource string            `json:"apiKeySource"`
	// PermissionMode and the other fields stay in Raw.
}

// MCPServerStatus is an MCP server as the init message reports it.
type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (*SystemMessage) Type() string { return "system" }

// ContentBlock is one block of a message's content. Type says which fields apply: "text"
// (Text), "thinking" (Thinking, Signature), "tool_use" (ID, Name, Input), "tool_result"
// (ToolUseID, Content, IsError), and others in Raw.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type plain ContentBlock
	if err := json.Unmarshal(data, (*plain)(b)); err != nil {
		return err
	}
	b.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// APIMessage is the Messages API message an assistant or user message carries.
type APIMessage struct {
	ID         string         `json:"id,omitempty"`
	Role       string         `json:"role"`
	Model      string         `json:"model,omitempty"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason,omitempty"`
}

func (m *APIMessage) UnmarshalJSON(data []byte) error {
	type plain APIMessage
	var p struct {
		plain
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*m = APIMessage(p.plain)
	m.Content = nil
	if len(p.Content) > 0 && p.Content[0] == '"' {
		// A user message may carry plain text.
		var s string
		if err := json.Unmarshal(p.Content, &s); err != nil {
			return err
		}
		m.Content = []ContentBlock{{Type: "text", Text: s}}
		return nil
	}
	if len(p.Content) > 0 && string(p.Content) != "null" {
		return json.Unmarshal(p.Content, &m.Content)
	}
	return nil
}

// Text joins the message's text blocks.
func (m *APIMessage) Text() string {
	s := ""
	for _, b := range m.Content {
		if b.Type == "text" {
			s += b.Text
		}
	}
	return s
}

// AssistantMessage is a complete response of the model (or one of its sub-agents when
// ParentToolUseID is set).
type AssistantMessage struct {
	base
	Message         APIMessage `json:"message"`
	ParentToolUseID string     `json:"parent_tool_use_id"`
	SessionID       string     `json:"session_id"`
	// Error is set when the response failed (for example "rate_limit" or "authentication_failed").
	Error string `json:"error"`
}

func (*AssistantMessage) Type() string { return "assistant" }

// UserMessage is a user turn as the CLI recorded it: the prompt, or tool results.
type UserMessage struct {
	base
	Message         APIMessage `json:"message"`
	ParentToolUseID string     `json:"parent_tool_use_id"`
	SessionID       string     `json:"session_id"`
}

func (*UserMessage) Type() string { return "user" }

// StreamEvent is a raw streaming event of the Messages API, delivered with
// Options.IncludePartialMessages while a response streams.
type StreamEvent struct {
	base
	Event           json.RawMessage `json:"event"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	// EventType is the event's "type" (message_start, content_block_delta…); Index and Delta
	// are read from it when present.
	EventType string `json:"-"`
	Index     int    `json:"-"`
	Delta     Delta  `json:"-"`
}

// Delta is a content_block_delta's delta: Type text_delta (Text), thinking_delta (Thinking),
// input_json_delta (PartialJSON), signature_delta.
type Delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
}

func (*StreamEvent) Type() string { return "stream_event" }

// ResultMessage ends a turn: how it ended, what it cost and the session to resume.
type ResultMessage struct {
	base
	Subtype       string          `json:"subtype"` // success, error_max_turns, error_during_execution…
	IsError       bool            `json:"is_error"`
	NumTurns      int             `json:"num_turns"`
	DurationMS    int64           `json:"duration_ms"`
	DurationAPIMS int64           `json:"duration_api_ms"`
	SessionID     string          `json:"session_id"`
	StopReason    string          `json:"stop_reason"`
	TotalCostUSD  float64         `json:"total_cost_usd"` // an estimate at API prices, even on a subscription
	Usage         json.RawMessage `json:"usage"`
	Result        string          `json:"result"`
	Errors        []string        `json:"errors"`
}

func (*ResultMessage) Type() string { return "result" }

// RateLimitEvent reports the account's usage against its limits.
type RateLimitEvent struct{ base }

func (*RateLimitEvent) Type() string { return "rate_limit_event" }

// OtherMessage is a message of a type this package does not know.
type OtherMessage struct {
	base
	MessageType string
}

func (m *OtherMessage) Type() string { return m.MessageType }

// ParseMessage reads one line of the CLI's output.
func ParseMessage(line []byte) (Message, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("claudecode: not a message: %w", err)
	}
	raw := append(json.RawMessage(nil), line...)
	var m Message
	switch head.Type {
	case "system":
		m = &SystemMessage{base: base{raw}}
	case "assistant":
		m = &AssistantMessage{base: base{raw}}
	case "user":
		m = &UserMessage{base: base{raw}}
	case "stream_event":
		m = &StreamEvent{base: base{raw}}
	case "result":
		m = &ResultMessage{base: base{raw}}
	case "rate_limit_event":
		return &RateLimitEvent{base{raw}}, nil
	default:
		return &OtherMessage{base: base{raw}, MessageType: head.Type}, nil
	}
	if err := json.Unmarshal(line, m); err != nil {
		return nil, fmt.Errorf("claudecode: %s message: %w", head.Type, err)
	}
	if ev, ok := m.(*StreamEvent); ok && len(ev.Event) > 0 {
		var e struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta Delta  `json:"delta"`
		}
		if err := json.Unmarshal(ev.Event, &e); err == nil {
			ev.EventType, ev.Index, ev.Delta = e.Type, e.Index, e.Delta
		}
	}
	return m, nil
}
